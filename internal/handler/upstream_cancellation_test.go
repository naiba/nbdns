package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/naiba/nbdns/internal/model"
	"github.com/naiba/nbdns/internal/stats"
	"github.com/naiba/nbdns/pkg/logger"
)

func TestWinningUpstreamCancelsSlowDoHQuery(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	slowServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
			close(canceled)
		case <-release:
		}
	}))
	t.Cleanup(slowServer.Close)
	t.Cleanup(func() { close(release) })
	slow := &model.Upstream{Address: slowServer.URL}
	slow.Init(&model.Config{Timeout: 2}, logger.New(false))
	slow.InitConnectionPool(nil)
	fast, calls := testUpstream(t, func(req *dns.Msg) *dns.Msg {
		<-started
		return coalescedDNSAnswer(req)
	})
	recorder := stats.NewStats()
	h := NewHandler(false, []*model.Upstream{slow, fast}, "", logger.New(false), recorder)
	query := testQuery("race.example.")
	reply := h.HandleDnsMsg(query, "", "")
	if reply.Id != query.Id || reply.Rcode != dns.RcodeSuccess || len(reply.Answer) != 1 || calls.Load() != 1 {
		t.Fatalf("invalid winning response: %v", reply)
	}
	select {
	case <-canceled:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("winning response left slow upstream running")
	}
}
