// dnsperf runs isolated, loopback-only end-to-end DNS workloads against a real binary.
package main

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/miekg/dns"
)

type scenario struct {
	Name, Transport, Kind string
	Cache                 bool
	Upstreams             int
	Delay                 time.Duration
}
type result struct {
	Scenario                                            string
	Concurrency, Requests, Errors                       int
	QPS, P50US, P95US, P99US, CPUSeconds, CPUUSPerQuery float64
	RSSPeakKB, HeapBytes, AllocBytes, AllocObjects      uint64
	UpstreamQueries                                     uint64
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

var targetDebug = flag.Bool("target-debug", false, "enable target debug logging for diagnosis, not performance comparisons")

var nextPort atomic.Uint32

// Avoid the ephemeral source-port range: the generator and resolver create
// many sockets, so a TCP-only ephemeral probe does not reserve a DNS UDP port.
func reserve() string {
	for attempts := 0; attempts < 10000; attempts++ {
		port := 18000 + nextPort.Add(1)%10000
		address := fmt.Sprintf("127.0.0.1:%d", port)
		tcp, err := net.Listen("tcp", address)
		if err != nil {
			continue
		}
		udp, err := net.ListenPacket("udp", address)
		if err != nil {
			tcp.Close()
			continue
		}
		tcp.Close()
		udp.Close()
		return address
	}
	panic("no local test ports available")
}
func fetch(url string) []byte {
	c := http.Client{Timeout: time.Minute}
	r, e := c.Get(url)
	must(e)
	defer r.Body.Close()
	if r.StatusCode != 200 {
		panic(r.Status)
	}
	b, e := io.ReadAll(r.Body)
	must(e)
	return b
}
func memoryCounters(data []byte) map[string]uint64 {
	out := map[string]uint64{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 4 && fields[0] == "#" && fields[2] == "=" {
			v, e := strconv.ParseUint(fields[3], 10, 64)
			if e == nil {
				out[fields[1]] = v
			}
		}
	}
	return out
}
func cpuTicks(pid int) uint64 {
	b, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	must(e)
	fields := strings.Fields(string(b)[strings.LastIndexByte(string(b), ')')+2:])
	u, e := strconv.ParseUint(fields[11], 10, 64)
	must(e)
	v, e := strconv.ParseUint(fields[12], 10, 64)
	must(e)
	return u + v
}
func processRSS(pid int) uint64 {
	b, e := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if e != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "VmRSS:") {
			v, _ := strconv.ParseUint(strings.Fields(l)[1], 10, 64)
			return v
		}
	}
	return 0
}
func main() {
	binary := flag.String("binary", "", "unversioned NbDNS binary (automatic updates must be disabled)")
	out := flag.String("output", "", "artifact directory")
	duration := flag.Duration("duration", 3*time.Second, "load duration per scenario/concurrency")
	levels := flag.String("concurrency", "1,16,64", "concurrency levels")
	repeat := flag.Int("repeat", 1, "repetitions")
	only := flag.String("scenario", "", "optional exact scenario")
	cooldown := flag.Duration("cooldown", 0, "idle interval between isolated workloads")
	profile := flag.Bool("profile", false, "collect a separate CPU profile during measured load")
	flag.Parse()
	if *duration < time.Second || *repeat < 1 {
		panic("duration must be at least 1s and repeat positive")
	}
	if *binary == "" || *out == "" {
		panic("binary and output required")
	}
	bin, e := filepath.Abs(*binary)
	must(e)
	must(os.MkdirAll(*out, 0700))
	cases := []scenario{{"working_set_udp", "udp", "working_set", true, 1, 0}, {"upstream_dot", "udp", "dot_upstream", false, 1, 0}, {"upstream_https", "udp", "https_upstream", false, 1, 0}, {"mixed_udp", "udp", "mixed", true, 1, 0}, {"timeout_udp", "udp", "timeout", false, 1, 0}, {"custom_udp", "udp", "custom", true, 1, 0}, {"filtered_udp", "udp", "blocked", true, 1, 0}, {"cache_udp", "udp", "cache", true, 1, 0}, {"cache_tcp", "tcp", "cache", true, 1, 0}, {"cache_doh", "doh", "cache", true, 1, 0}, {"uncached_udp", "udp", "uncached", false, 1, 0}, {"upstream_tcp", "udp", "tcp_upstream", false, 1, 0}, {"upstream_doh", "udp", "doh_upstream", false, 1, 0}, {"truncated_udp", "udp", "truncated", false, 1, 0}, {"race_failure", "udp", "race_failure", false, 3, 2 * time.Millisecond}, {"cache_write_udp", "udp", "unique", true, 1, 0}, {"race_udp", "udp", "uncached", false, 3, 2 * time.Millisecond}, {"failed_udp", "udp", "failed", false, 1, 0}, {"nxdomain_udp", "udp", "negative", true, 1, 0}}
	encoder := json.NewEncoder(os.Stdout)
	for run := 0; run < *repeat; run++ {
		for _, s := range cases {
			if *only != "" && s.Name != *only {
				continue
			}
			for _, level := range strings.Split(*levels, ",") {
				concurrency, e := strconv.Atoi(level)
				must(e)
				if concurrency < 1 {
					panic("invalid concurrency")
				}
				r := execute(bin, *out, s, concurrency, *duration, run, *profile)
				must(encoder.Encode(r))
				if *cooldown > 0 {
					time.Sleep(*cooldown)
				}
				if r.Errors != 0 {
					panic("invalid workload responses; reject this benchmark")
				}
			}
		}
	}
}
func execute(binary, output string, s scenario, concurrency int, duration time.Duration, run int, profile bool) result {
	dir := filepath.Join(output, fmt.Sprintf("%s-c%d-r%d", s.Name, concurrency, run))
	if _, err := os.Stat(dir); err == nil {
		panic("output already exists: " + dir)
	} else if !os.IsNotExist(err) {
		must(err)
	}
	must(os.MkdirAll(filepath.Join(dir, "data"), 0700))
	dnsAddr, webAddr := reserve(), reserve()
	certificateServer := httptest.NewTLSServer(http.NotFoundHandler())
	certificate := certificateServer.TLS.Certificates[0]
	certificateServer.Close()
	certificatePath := filepath.Join(dir, "upstream-ca.pem")
	must(os.WriteFile(certificatePath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]}), 0600))
	var upstreamCount atomic.Uint64
	upstreams := []map[string]any{}
	for i := 0; i < s.Upstreams; i++ {
		delay := s.Delay * time.Duration(i+1)
		var tcpListener net.Listener
		udpAddress := reserve()
		if s.Kind == "tcp_upstream" || s.Kind == "truncated" || s.Kind == "dot_upstream" {
			var e error
			tcpListener, e = net.Listen("tcp", reserve())
			must(e)
			defer tcpListener.Close()
			udpAddress = tcpListener.Addr().String()
		}
		sock, e := net.ListenPacket("udp", udpAddress)
		must(e)
		handler := dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) {
			upstreamCount.Add(1)
			if s.Kind == "timeout" {
				return
			}
			if delay > 0 {
				time.Sleep(delay)
			}
			r := new(dns.Msg).SetReply(q)
			r.RecursionAvailable = true
			if s.Kind == "truncated" {
				if _, ok := w.RemoteAddr().(*net.UDPAddr); ok {
					r.Truncated = true
					_ = w.WriteMsg(r)
					return
				}
			}
			if s.Kind == "race_failure" && delay == s.Delay {
				r.Rcode = dns.RcodeRefused
				_ = w.WriteMsg(r)
				return
			}
			switch s.Kind {
			case "failed":
				r.Rcode = dns.RcodeServerFailure
			case "negative":
				r.Rcode = dns.RcodeNameError
				r.Ns = []dns.RR{&dns.SOA{Hdr: dns.RR_Header{Name: "example.", Rrtype: dns.TypeSOA, Class: 1, Ttl: 300}, Ns: "ns.example.", Mbox: "hostmaster.example.", Minttl: 300}}
			default:
				r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: 1, Ttl: 300}, A: net.IPv4(192, 0, 2, 42)}}
			}
			_ = w.WriteMsg(r)
		})
		srv := &dns.Server{PacketConn: sock, Handler: handler}
		go func() { _ = srv.ActivateAndServe() }()
		defer srv.Shutdown()
		address := "udp://" + sock.LocalAddr().String()
		if s.Kind == "tcp_upstream" || s.Kind == "truncated" || s.Kind == "dot_upstream" {
			listener := tcpListener
			if s.Kind == "dot_upstream" {
				listener = tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
			}
			tcp := &dns.Server{Listener: listener, Handler: handler}
			go func() { _ = tcp.ActivateAndServe() }()
			defer tcp.Shutdown()
			if s.Kind == "dot_upstream" {
				address = "tcp-tls://" + listener.Addr().String()
			}
			if s.Kind == "tcp_upstream" {
				address = "tcp://" + listener.Addr().String()
			}
		}
		if s.Kind == "doh_upstream" || s.Kind == "https_upstream" {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamCount.Add(1)
				wire, e := base64.RawURLEncoding.DecodeString(r.URL.Query().Get("dns"))
				if e != nil {
					http.Error(w, "query", 400)
					return
				}
				q := new(dns.Msg)
				if q.Unpack(wire) != nil {
					http.Error(w, "dns", 400)
					return
				}
				reply := new(dns.Msg).SetReply(q)
				reply.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: 1, Ttl: 300}, A: net.IPv4(192, 0, 2, 42)}}
				wire, e = reply.Pack()
				must(e)
				w.Header().Set("Content-Type", "application/dns-message")
				_, _ = w.Write(wire)
			}))
			if s.Kind == "https_upstream" {
				server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}}
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			address = server.URL
		}
		upstreams = append(upstreams, map[string]any{"address": address})
	}
	config := map[string]any{"serve_addr": dnsAddr, "web_addr": webAddr, "profiling": true, "debug": *targetDebug, "ecs": "192.0.2.0/24", "built_in_cache": s.Cache, "upstreams": upstreams, "timeout": 2, "max_active_connections": 128, "max_idle_connections": 64, "doh_server": map[string]any{}, "filter_lists": []string{"rules.txt"}, "filter_update_interval_hours": -1, "custom_records": []map[string]any{{"name": "local.example", "type": "A", "value": "192.0.2.42", "ttl": 300}}}
	data, e := json.Marshal(config)
	must(e)
	must(os.WriteFile(filepath.Join(dir, "data/config.json"), data, 0600))
	var rules strings.Builder
	for i := 0; i < 50000; i++ {
		fmt.Fprintf(&rules, "||ad%d.example^\n", i)
	}
	must(os.WriteFile(filepath.Join(dir, "data/rules.txt"), []byte(rules.String()), 0600))
	log, e := os.Create(filepath.Join(dir, "server.log"))
	must(e)
	defer log.Close()
	cmd := exec.Command(binary)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOMAXPROCS=2", "SSL_CERT_FILE="+certificatePath)
	cmd.Stdout = log
	cmd.Stderr = log
	must(cmd.Start())
	stopped := false
	defer func() {
		if !stopped {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	httpClient := &http.Client{Timeout: 3 * time.Second}
	ready := false
	for i := 0; i < 100; i++ {
		r, e := httpClient.Get("http://" + webAddr + "/api/stats")
		if e == nil {
			r.Body.Close()
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		panic("server not ready: " + dir)
	}
	name := "cached.example."
	if s.Kind == "custom" {
		name = "local.example."
	}
	if s.Kind == "blocked" {
		name = "ad42420.example."
	}
	client := &dns.Client{Timeout: 3 * time.Second}
	q := new(dns.Msg).SetQuestion(name, dns.TypeA)
	q.SetEdns0(1232, true)
	// Warm the cache and connection-independent initialization outside measurement.
	_, _, e = client.Exchange(q, dnsAddr)
	must(e)
	before := fetch("http://" + webAddr + "/debug/pprof/allocs?debug=1")
	must(os.WriteFile(filepath.Join(dir, "allocs-before.txt"), before, 0600))
	initialUpstream := upstreamCount.Load()
	var rss atomic.Uint64
	rssFile, e := os.Create(filepath.Join(dir, "rss.csv"))
	must(e)
	defer rssFile.Close()
	_, _ = fmt.Fprintln(rssFile, "elapsed_ms,rss_kb")
	sampleStart := time.Now()
	var sampling sync.WaitGroup
	sampling.Add(1)
	done := make(chan struct{})
	go func() {
		defer sampling.Done()
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				v := processRSS(cmd.Process.Pid)
				_, _ = fmt.Fprintf(rssFile, "%d,%d\n", time.Since(sampleStart).Milliseconds(), v)
				if v > rss.Load() {
					rss.Store(v)
				}
			}
		}
	}()
	var profileWG sync.WaitGroup
	if profile {
		profileWG.Add(1)
		go func() {
			defer profileWG.Done()
			seconds := int(duration.Seconds())
			if seconds < 1 {
				seconds = 1
			}
			must(os.WriteFile(filepath.Join(dir, "cpu.pprof"), fetch(fmt.Sprintf("http://%s/debug/pprof/profile?seconds=%d", webAddr, seconds)), 0600))
		}()
	}
	ticksBefore := cpuTicks(cmd.Process.Pid)
	start := time.Now()
	deadline := start.Add(duration)
	var wg sync.WaitGroup
	var mu sync.Mutex
	latencies := []float64{}
	errors := 0
	var sequence atomic.Uint64
	for worker := 0; worker < concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := []float64{}
			fail := 0
			var conn *dns.Conn
			netClient := &dns.Client{Net: s.Transport, Timeout: 3 * time.Second}
			if s.Transport != "doh" {
				var e error
				conn, e = netClient.Dial(dnsAddr)
				if e != nil {
					panic(e)
				}
				defer func() {
					if conn != nil {
						_ = conn.Close()
					}
				}()
			}
			transport := &http.Transport{MaxIdleConnsPerHost: concurrency}
			hc := &http.Client{Transport: transport, Timeout: 3 * time.Second}
			defer transport.CloseIdleConnections()
			for time.Now().Before(deadline) {
				query := q.Copy()
				query.Id = dns.Id()
				expectedKind := s.Kind
				if s.Kind == "mixed" {
					n := sequence.Add(1)
					switch n % 10 {
					case 0:
						query.Question[0].Name = "local.example."
					case 1:
						query.Question[0].Name = "ad42420.example."
						expectedKind = "blocked"
					case 2, 3:
						query.Question[0].Name = fmt.Sprintf("mixed%d.example.", n)
					default:
						query.Question[0].Name = "cached.example."
					}
				}
				if s.Kind == "working_set" {
					query.Question[0].Name = fmt.Sprintf("working%d.example.", sequence.Add(1)%100000)
				}
				if s.Kind == "unique" {
					query.Question[0].Name = fmt.Sprintf("q%d.example.", sequence.Add(1))
				}
				t := time.Now()
				var reply *dns.Msg
				var err error
				if s.Transport == "doh" {
					wire, e := query.Pack()
					err = e
					if err == nil {
						var response *http.Response
						response, err = hc.Get("http://" + webAddr + "/dns-query?dns=" + base64.RawURLEncoding.EncodeToString(wire))
						if err == nil {
							data, e := io.ReadAll(response.Body)
							response.Body.Close()
							err = e
							if response.StatusCode != 200 {
								err = fmt.Errorf("HTTP %d", response.StatusCode)
							}
							if err == nil {
								reply = new(dns.Msg)
								err = reply.Unpack(data)
							}
						}
					}
				} else {
					if conn == nil {
						conn, err = netClient.Dial(dnsAddr)
					}
					if conn != nil {
						reply, _, err = netClient.ExchangeWithConn(query, conn)
					}
					// DNS servers may close persistent TCP sessions after a query limit.
					// Reconnect once and include that cost in the request latency.
					if err != nil && s.Transport == "tcp" {
						if conn != nil {
							_ = conn.Close()
						}
						conn, err = netClient.Dial(dnsAddr)
						if err == nil {
							reply, _, err = netClient.ExchangeWithConn(query, conn)
						}
					}
				}
				local = append(local, float64(time.Since(t).Nanoseconds())/1000)
				valid := err == nil && validReply(query, reply, expectedKind)
				if !valid {
					if fail < 3 {
						fmt.Fprintf(os.Stderr, "invalid query=%s err=%v response=%v\n", query.Question[0].Name, err, reply)
					}
					fail++
				}
			}
			mu.Lock()
			latencies = append(latencies, local...)
			errors += fail
			mu.Unlock()
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	ticksAfter := cpuTicks(cmd.Process.Pid)
	after := fetch("http://" + webAddr + "/debug/pprof/allocs?debug=1")
	beforeMem, afterMem := memoryCounters(before), memoryCounters(after)
	close(done)
	sampling.Wait()
	profileWG.Wait()
	if smaps, err := os.ReadFile(fmt.Sprintf("/proc/%d/smaps_rollup", cmd.Process.Pid)); err == nil {
		must(os.WriteFile(filepath.Join(dir, "smaps-rollup.txt"), smaps, 0600))
	}
	must(os.WriteFile(filepath.Join(dir, "heap.pprof"), fetch("http://"+webAddr+"/debug/pprof/heap?gc=1"), 0600))
	must(os.WriteFile(filepath.Join(dir, "allocs.pprof"), fetch("http://"+webAddr+"/debug/pprof/allocs"), 0600))
	must(os.WriteFile(filepath.Join(dir, "allocs-after.txt"), fetch("http://"+webAddr+"/debug/pprof/allocs?debug=1"), 0600))
	must(os.WriteFile(filepath.Join(dir, "stats.json"), fetch("http://"+webAddr+"/api/stats"), 0600))
	_ = cmd.Process.Signal(syscall.SIGTERM)
	must(cmd.Wait())
	stopped = true
	sort.Float64s(latencies)
	if len(latencies) == 0 {
		panic("no samples")
	}
	pct := func(p float64) float64 { return latencies[int(float64(len(latencies)-1)*p)] }
	tickRate, e := exec.Command("getconf", "CLK_TCK").Output()
	must(e)
	hz, e := strconv.ParseFloat(strings.TrimSpace(string(tickRate)), 64)
	must(e)
	cpu := float64(ticksAfter-ticksBefore) / hz
	return result{Scenario: s.Name, Concurrency: concurrency, Requests: len(latencies), Errors: errors, QPS: float64(len(latencies)) / elapsed.Seconds(), P50US: pct(.50), P95US: pct(.95), P99US: pct(.99), CPUSeconds: cpu, CPUUSPerQuery: cpu * 1e6 / float64(len(latencies)), RSSPeakKB: rss.Load(), HeapBytes: afterMem["HeapAlloc"], AllocBytes: afterMem["TotalAlloc"] - beforeMem["TotalAlloc"], AllocObjects: afterMem["Mallocs"] - beforeMem["Mallocs"], UpstreamQueries: upstreamCount.Load() - initialUpstream}
}

func validReply(query, reply *dns.Msg, kind string) bool {
	valid := reply != nil && !reply.Truncated && reply.Response && reply.Id == query.Id && len(reply.Question) == 1 && reply.Question[0] == query.Question[0]
	if valid {
		switch kind {
		case "blocked", "negative":
			valid = reply.Rcode == dns.RcodeNameError && len(reply.Answer) == 0
		case "failed", "timeout":
			valid = reply.Rcode == dns.RcodeServerFailure
		default:
			valid = reply.Rcode == 0 && len(reply.Answer) == 1
			if valid {
				a, ok := reply.Answer[0].(*dns.A)
				valid = ok && a.Hdr.Name == query.Question[0].Name && a.Hdr.Class == dns.ClassINET && a.Hdr.Rrtype == dns.TypeA && a.A.Equal(net.IPv4(192, 0, 2, 42))
			}
		}
	}
	return valid
}
