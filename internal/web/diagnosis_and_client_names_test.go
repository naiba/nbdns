package web

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/internal/handler"
	"github.com/naiba/nbdns/internal/stats"
	"github.com/naiba/nbdns/pkg/logger"
)

func TestDiagnoseEndpoint(t *testing.T) {
	h := NewHandler(stats.NewStats(), "1.0.0", make(chan struct{}, 1), logger.New(false))
	var gotName string
	var gotType uint16
	h.SetDiagnoser(func(_ context.Context, name string, qtype uint16) (handler.Diagnosis, error) {
		if name == "bad..name" {
			return handler.Diagnosis{}, handler.ErrInvalidDiagnosisName
		}
		gotName, gotType = name, qtype
		return handler.Diagnosis{Name: "www.example.", Type: dns.TypeToString[qtype], Route: handler.DiagnosisRouteDefault}, nil
	})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	get := func(target string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		return response
	}

	response := get("/api/diagnose?name=www.example&type=aaaa")
	var result handler.Diagnosis
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK {
		t.Fatalf("status %d, body %s, err %v", response.Code, response.Body, err)
	}
	if gotName != "www.example" || gotType != dns.TypeAAAA || result.Type != "AAAA" || result.Route != handler.DiagnosisRouteDefault {
		t.Errorf("diagnosed %q type %d, result %+v", gotName, gotType, result)
	}
	if response := get("/api/diagnose?name=www.example"); response.Code != http.StatusOK || gotType != dns.TypeA {
		t.Errorf("default type: status %d, type %d", response.Code, gotType)
	}
	if response := get("/api/diagnose?name=www.example&type=NOPE"); response.Code != http.StatusBadRequest {
		t.Errorf("unknown type status = %d", response.Code)
	}
	if response := get("/api/diagnose?name=bad..name"); response.Code != http.StatusBadRequest {
		t.Errorf("invalid name status = %d", response.Code)
	}
	unconfigured := http.NewServeMux()
	NewHandler(stats.NewStats(), "", make(chan struct{}, 1), logger.New(false)).RegisterRoutes(unconfigured)
	recorder := httptest.NewRecorder()
	unconfigured.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/diagnose?name=www.example", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured status = %d", recorder.Code)
	}
}

func startPTRServer(t *testing.T, names map[string]string) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &dns.Server{PacketConn: conn, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		resp := new(dns.Msg).SetReply(req)
		if name, ok := names[req.Question[0].Name]; ok {
			resp.Answer = []dns.RR{&dns.PTR{Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypePTR, Class: dns.ClassINET, Ttl: 60}, Ptr: name}}
		} else {
			resp.Rcode = dns.RcodeNameError
		}
		_ = w.WriteMsg(resp)
	})}
	go func() { _ = server.ActivateAndServe() }()
	t.Cleanup(func() { _ = server.Shutdown() })
	return conn.LocalAddr().String()
}

func TestStatsIncludeClientNamesResolvedInBackground(t *testing.T) {
	server := startPTRServer(t, map[string]string{
		"10.2.0.192.in-addr.arpa.": "client-a.example.",
		"1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa.": "client-b.example.",
	})
	resolver := NewClientNameResolver(server)
	t.Cleanup(resolver.Close)
	recorder := stats.NewStats()
	recorder.RecordClientQuery("192.0.2.10", "www.example.")
	recorder.RecordClientQuery("2001:db8::1", "www.example.")
	recorder.RecordClientQuery("192.0.2.20", "www.example.") // No PTR record.
	recorder.RecordClientQuery("127.0.0.1", "www.example.")  // Never looked up.
	h := NewHandler(recorder, "", make(chan struct{}, 1), logger.New(false))
	h.SetClientNameResolver(resolver)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	var names map[string]string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
		var body struct {
			ClientNames map[string]string `json:"client_names"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		names = body.ClientNames
		if len(names) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(names) != 2 || names["192.0.2.10"] != "client-a.example" || names["2001:db8::1"] != "client-b.example" {
		t.Fatalf("client names = %v", names)
	}
	resolver.mu.Lock()
	_, loopbackQueried := resolver.entries["127.0.0.1"]
	missing := resolver.entries["192.0.2.20"]
	resolver.mu.Unlock()
	if loopbackQueried {
		t.Error("loopback client was looked up")
	}
	if missing.name != "" || missing.inFlight || time.Until(missing.expires) > clientNameMissingTTL {
		t.Errorf("unnamed client cache entry = %+v", missing)
	}
}

func TestClientNameResolverDefaultsToPort53(t *testing.T) {
	for input, want := range map[string]string{"192.0.2.1": "192.0.2.1:53", "192.0.2.1:5353": "192.0.2.1:5353", "2001:db8::1": "[2001:db8::1]:53", "[2001:db8::1]:54": "[2001:db8::1]:54"} {
		resolver := NewClientNameResolver(input)
		resolver.Close()
		if resolver.server != want {
			t.Errorf("NewClientNameResolver(%q).server = %q, want %q", input, resolver.server, want)
		}
	}
}
