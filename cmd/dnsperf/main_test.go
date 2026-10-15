package main

import (
	"github.com/miekg/dns"
	"net"
	"testing"
)

func TestMemoryCountersParseRuntimeFooter(t *testing.T) {
	counters := memoryCounters([]byte("heap profile: ignored\n# Alloc = 123\n# TotalAlloc = 456\n# Mallocs = 789\n# HeapAlloc = 120\n# PauseNs = [1 2]\n"))
	if counters["Alloc"] != 123 || counters["TotalAlloc"] != 456 || counters["Mallocs"] != 789 || counters["HeapAlloc"] != 120 || len(counters) != 4 {
		t.Fatalf("unexpected counters: %#v", counters)
	}
}

func TestReplyValidationRejectsWrongData(t *testing.T) {
	query := new(dns.Msg).SetQuestion("example.", dns.TypeA)
	response := new(dns.Msg).SetReply(query)
	response.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: "example.", Rrtype: dns.TypeA, Class: 1, Ttl: 300}, A: net.IPv4(192, 0, 2, 42)}}
	if !validReply(query, response, "cache") {
		t.Fatal("valid response rejected")
	}
	for _, mutate := range []func(*dns.Msg){func(r *dns.Msg) { r.Id++ }, func(r *dns.Msg) { r.Truncated = true }, func(r *dns.Msg) { r.Answer[0].Header().Name = "wrong.example." }, func(r *dns.Msg) { r.Answer[0].Header().Class = dns.ClassCHAOS }, func(r *dns.Msg) { r.Question[0].Name = "wrong.example." }, func(r *dns.Msg) { r.Response = false }, func(r *dns.Msg) { r.Rcode = dns.RcodeServerFailure }, func(r *dns.Msg) { r.Answer[0].(*dns.A).A = net.IPv4(192, 0, 2, 43) }} {
		copy := response.Copy()
		mutate(copy)
		if validReply(query, copy, "cache") {
			t.Fatalf("accepted invalid response: %v", copy)
		}
	}
	if validReply(query, nil, "cache") {
		t.Fatal("nil response accepted")
	}
	for _, kind := range []string{"blocked", "negative", "failed"} {
		r := new(dns.Msg).SetReply(query)
		if kind == "failed" {
			r.Rcode = dns.RcodeServerFailure
		} else {
			r.Rcode = dns.RcodeNameError
		}
		if !validReply(query, r, kind) {
			t.Fatalf("expected %s response rejected", kind)
		}
		r.Rcode = dns.RcodeSuccess
		if validReply(query, r, kind) {
			t.Fatalf("wrong %s response accepted", kind)
		}
	}
}

func TestTimeoutReplyValidation(t *testing.T) {
	q := new(dns.Msg).SetQuestion("timeout.example.", dns.TypeA)
	r := new(dns.Msg).SetRcode(q, dns.RcodeServerFailure)
	if !validReply(q, r, "timeout") {
		t.Fatal("expected timeout SERVFAIL rejected")
	}
	r.Rcode = dns.RcodeSuccess
	if validReply(q, r, "timeout") {
		t.Fatal("successful empty answer must not hide timeout")
	}
}
