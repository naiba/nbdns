package handler

import (
	"strings"
	"testing"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/pkg/logger"
)

type listedDomains map[string]bool

func (l listedDomains) Contains(name string) bool { return l[strings.ToLower(name)] }

func TestMatchListUpstreamTakesListedDomains(t *testing.T) {
	domestic, domesticRequests := testUpstream(t, func(q *dns.Msg) *dns.Msg { return new(dns.Msg).SetReply(q) })
	domestic.MatchLists = []string{"https://lists.example/cn.conf"}
	global, globalRequests := testUpstream(t, func(q *dns.Msg) *dns.Msg { return new(dns.Msg).SetReply(q) })
	h := NewHandler(false, []*model.Upstream{domestic, global}, t.TempDir(), logger.New(false), nil)

	// Lists not loaded yet (first start, no cache): everything uses the default upstream.
	h.HandleDnsMsg(new(dns.Msg).SetQuestion("www.listed.example.", dns.TypeA), "", "")
	if domesticRequests.Load() != 0 || globalRequests.Load() != 1 {
		t.Fatalf("before load: domestic=%d global=%d", domesticRequests.Load(), globalRequests.Load())
	}
	domestic.SetMatchLists(listedDomains{"listed.example.": true, "www.listed.example.": true})
	h.HandleDnsMsg(new(dns.Msg).SetQuestion("www.listed.example.", dns.TypeAAAA), "", "")
	h.HandleDnsMsg(new(dns.Msg).SetQuestion("unlisted.example.", dns.TypeA), "", "")
	if domesticRequests.Load() != 1 || globalRequests.Load() != 2 {
		t.Errorf("after load: domestic=%d global=%d, want listed->domestic, unlisted->global", domesticRequests.Load(), globalRequests.Load())
	}
}

func TestCacheNamespaceIncludesMatchLists(t *testing.T) {
	plain := &model.Upstream{Address: "udp://192.0.2.53:53"}
	plain.Init(&model.Config{}, logger.New(false))
	listed := &model.Upstream{Address: "udp://192.0.2.53:53", MatchLists: []string{"https://lists.example/cn.conf"}}
	listed.Init(&model.Config{}, logger.New(false))
	if upstreamCacheNamespace([]*model.Upstream{plain}) == upstreamCacheNamespace([]*model.Upstream{listed}) {
		t.Error("routing domains by list changes answers and must change the cache namespace")
	}
}
