package model

import (
	"net"
	"testing"

	"github.com/miekg/dns"
)

func TestIsDnsmasqRebindAddressMatchesDnsmasqPrivateNet(t *testing.T) {
	cases := map[string]bool{
		// IPv4 ranges from dnsmasq private_net() with ban_localhost (OpenWrt default).
		"0.0.0.0":         true,
		"10.0.0.1":        true,
		"100.64.0.1":      true,
		"100.127.255.255": true,
		"127.0.0.1":       true,
		"169.254.0.2":     true,
		"172.16.0.1":      true,
		"172.31.255.255":  true,
		"192.168.0.1":     true,
		"192.0.2.10":      true,
		"198.51.100.10":   true,
		"203.0.113.10":    true,
		"255.255.255.255": true,
		"1.1.1.1":         false,
		"100.128.0.1":     false,
		"172.32.0.1":      false,
		"198.18.0.1":      false, // Benchmarking range: dnsmasq does not treat it as private.
		"224.0.0.1":       false,
		// IPv6 ranges from dnsmasq private_net6().
		"::":                 true,
		"::1":                true,
		"fe80::1":            true,
		"fec0::1":            true,
		"fd00::1":            true,
		"2001:db8::1":        true,
		"::ffff:10.0.0.1":    true,
		"::ffff:1.1.1.1":     false,
		"fc00::1":            false, // dnsmasq only checks fd00::/8 of the ULA range.
		"2001:4860:4860::88": false,
	}
	for text, want := range cases {
		ip := net.ParseIP(text)
		if ip == nil {
			t.Fatalf("invalid test address %q", text)
		}
		if got := IsDnsmasqRebindAddress(ip); got != want {
			t.Errorf("IsDnsmasqRebindAddress(%s) = %v, want %v", text, got, want)
		}
	}
	if IsDnsmasqRebindAddress(nil) {
		t.Error("nil address must not be reported")
	}
}

func TestAnswerHasDnsmasqRebindAddress(t *testing.T) {
	header := func(rrtype uint16) dns.RR_Header {
		return dns.RR_Header{Name: "internal.example.", Rrtype: rrtype, Class: dns.ClassINET, Ttl: 60}
	}
	public := &dns.A{Hdr: header(dns.TypeA), A: net.ParseIP("1.1.1.1")}
	private := &dns.A{Hdr: header(dns.TypeA), A: net.ParseIP("10.0.0.1")}
	privateV6 := &dns.AAAA{Hdr: header(dns.TypeAAAA), AAAA: net.ParseIP("fd00::1")}
	cname := &dns.CNAME{Hdr: header(dns.TypeCNAME), Target: "edge.example."}

	cases := []struct {
		name string
		msg  *dns.Msg
		want bool
	}{
		{"nil", nil, false},
		{"public only", &dns.Msg{Answer: []dns.RR{public}}, false},
		{"mixed public and private", &dns.Msg{Answer: []dns.RR{public, private}}, true},
		{"private behind cname", &dns.Msg{Answer: []dns.RR{cname, private}}, true},
		{"private ipv6", &dns.Msg{Answer: []dns.RR{privateV6}}, true},
		{"cname only", &dns.Msg{Answer: []dns.RR{cname}}, false},
		{"private address outside answer", &dns.Msg{Extra: []dns.RR{private}}, false},
	}
	for _, tc := range cases {
		if got := AnswerHasDnsmasqRebindAddress(tc.msg); got != tc.want {
			t.Errorf("%s: AnswerHasDnsmasqRebindAddress = %v, want %v", tc.name, got, tc.want)
		}
	}
}
