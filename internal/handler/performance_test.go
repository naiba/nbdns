package handler

import (
	"fmt"
	"strings"
	"testing"

	"github.com/miekg/dns"
)

func BenchmarkDNSCacheKey(b *testing.B) {
	q := new(dns.Msg).SetQuestion("WWW.Example.COM.", dns.TypeAAAA)
	q.SetEdns0(1232, true)
	b.ReportAllocs()
	for b.Loop() {
		if getDnsRequestCacheKey(q) != "www.example.com.#28#1#false#DO" {
			b.Fatal("cache key changed")
		}
	}
}

func TestDNSCacheKeyCompatibility(t *testing.T) {
	for _, name := range []string{"a.", "WWW.Example.COM.", strings.Repeat("x", 63) + ".example."} {
		for _, kind := range []uint16{1, 28, 255, 65535} {
			for _, class := range []uint16{1, 3, 65535} {
				for _, cd := range []bool{false, true} {
					for _, do := range []bool{false, true} {
						q := new(dns.Msg).SetQuestion(name, kind)
						q.Question[0].Qclass = class
						q.CheckingDisabled = cd
						suffix := ""
						if do {
							q.SetEdns0(1232, true)
							suffix = "DO"
						}
						want := fmt.Sprintf("%s#%d#%d#%t#%s", strings.ToLower(name), kind, class, cd, suffix)
						if got := getDnsRequestCacheKey(q); got != want {
							t.Fatalf("got %q want %q", got, want)
						}
					}
				}
			}
		}
	}
}
