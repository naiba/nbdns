package model

import (
	"index/suffixarray"
	"net"
	"strings"
	"testing"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/pkg/logger"
	"github.com/naiba/nbdns/pkg/utils"
	"github.com/yl2chen/cidranger"
)

func TestUDPTruncationRetriesOverTCP(t *testing.T) {
	udpConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcpConn, err := net.Listen("tcp", udpConn.LocalAddr().String())
	if err != nil {
		_ = udpConn.Close()
		t.Fatal(err)
	}
	udp := &dns.Server{PacketConn: udpConn, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		resp := new(dns.Msg).SetReply(req)
		resp.Truncated = true
		_ = w.WriteMsg(resp)
	})}
	tcp := &dns.Server{Listener: tcpConn, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		resp := new(dns.Msg).SetReply(req)
		resp.Answer = []dns.RR{&dns.A{
			Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30},
			A:   net.IPv4(192, 0, 2, 1),
		}}
		_ = w.WriteMsg(resp)
	})}
	go func() { _ = udp.ActivateAndServe() }()
	go func() { _ = tcp.ActivateAndServe() }()
	t.Cleanup(func() { _ = udp.Shutdown(); _ = tcp.Shutdown() })
	up := &Upstream{Address: "udp://" + udpConn.LocalAddr().String()}
	up.Init(&Config{Timeout: 2}, cidranger.NewPCTrieRanger(), logger.New(false))
	query := new(dns.Msg)
	query.SetQuestion("example.org.", dns.TypeA)
	resp, _, err := up.Exchange(query)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Truncated || len(resp.Answer) != 1 {
		t.Errorf("did not retry truncated UDP answer via TCP: %+v", resp)
	}
}

var primaryLocations = []string{"中国", "省", "市", "自治区"}
var nonPrimaryLocations = []string{"台湾", "香港", "澳门"}

var primaryLocationsBytes = [][]byte{[]byte("中国"), []byte("省"), []byte("市"), []byte("自治区")}
var nonPrimaryLocationsBytes = [][]byte{[]byte("台湾"), []byte("香港"), []byte("澳门")}

func BenchmarkCheckPrimary(b *testing.B) {
	for i := 0; i < b.N; i++ {
		checkPrimary("哈哈")
	}
}

func BenchmarkCheckPrimaryStringsContains(b *testing.B) {
	for i := 0; i < b.N; i++ {
		checkPrimaryStringsContains("哈哈")
	}
}

func TestIsMatch(t *testing.T) {
	var up Upstream
	up.matchSplited = utils.ParseRules([]string{"."})
	checkUpstreamMatch(&up, map[string]bool{
		"":             false,
		"a.com.":       true,
		"b.a.com.":     true,
		".b.a.com.cn.": true,
		"b.a.com.cn.":  true,
		"d.b.a.com.":   true,
	}, t)

	up.matchSplited = utils.ParseRules([]string{""})
	checkUpstreamMatch(&up, map[string]bool{
		"":             false,
		"a.com.":       false,
		"b.a.com.":     false,
		".b.a.com.cn.": false,
		"b.a.com.cn.":  false,
		"d.b.a.com.":   false,
	}, t)

	up.matchSplited = utils.ParseRules([]string{"a.com."})
	checkUpstreamMatch(&up, map[string]bool{
		"":             false,
		"a.com.":       true,
		"b.a.com.":     false,
		".b.a.com.cn.": false,
		"b.a.com.cn.":  false,
		"d.b.a.com.":   false,
	}, t)

	up.matchSplited = utils.ParseRules([]string{".a.com."})
	checkUpstreamMatch(&up, map[string]bool{
		"":             false,
		"a.com.":       false,
		"b.a.com.":     true,
		".b.a.com.cn.": false,
		"b.a.com.cn.":  false,
		"d.b.a.com.":   true,
	}, t)

	up.matchSplited = utils.ParseRules([]string{"b.d.com."})
	checkUpstreamMatch(&up, map[string]bool{
		"":             false,
		"a.com.":       false,
		".a.com.":      false,
		"b.d.com.":     true,
		".b.d.com.cn.": false,
		"b.d.com.cn.":  false,
		".c.d.com.":    false,
		"b.d.a.com.":   false,
	}, t)
}

func checkUpstreamMatch(up *Upstream, cases map[string]bool, t *testing.T) {
	for k, v := range cases {
		isMatch := up.IsMatch(k)
		if isMatch != v {
			t.Errorf("Upstream(%s).IsMatch(%s) = %v, want %v", up.matchSplited, k, isMatch, v)
		}
	}
}

func checkPrimary(str string) bool {
	index := suffixarray.New([]byte(str))
	for i := 0; i < len(nonPrimaryLocationsBytes); i++ {
		if len(index.Lookup(nonPrimaryLocationsBytes[i], 1)) > 0 {
			return false
		}
	}
	for i := 0; i < len(primaryLocationsBytes); i++ {
		if len(index.Lookup(primaryLocationsBytes[i], 1)) > 0 {
			return true
		}
	}
	return false
}

func checkPrimaryStringsContains(str string) bool {
	for i := 0; i < len(nonPrimaryLocations); i++ {
		if strings.Contains(str, nonPrimaryLocations[i]) {
			return false
		}
	}
	for i := 0; i < len(primaryLocations); i++ {
		if strings.Contains(str, primaryLocations[i]) {
			return true
		}
	}
	return false
}
