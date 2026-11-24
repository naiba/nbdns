package model

import (
	"net"
	"testing"

	"github.com/miekg/dns"
)

func BenchmarkAnswerHasDnsmasqRebindAddressPublicCDNAnswer(b *testing.B) {
	msg := new(dns.Msg)
	hdr := dns.RR_Header{Name: "cdn.example.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}
	msg.Answer = append(msg.Answer, &dns.CNAME{Hdr: dns.RR_Header{Name: "www.example.", Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 60}, Target: "cdn.example."})
	for i := 1; i <= 8; i++ {
		msg.Answer = append(msg.Answer, &dns.A{Hdr: hdr, A: net.IPv4(1, 1, 1, byte(i))})
	}
	b.ReportAllocs()
	for b.Loop() {
		if AnswerHasDnsmasqRebindAddress(msg) {
			b.Fatal("public answer reported as private")
		}
	}
}
