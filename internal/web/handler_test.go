package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/naiba/nbdns/internal/filter"
	"github.com/naiba/nbdns/internal/stats"
	"github.com/naiba/nbdns/pkg/logger"
)

func TestFilterStatusEndpoint(t *testing.T) {
	h := NewHandler(stats.NewStats(), "", make(chan struct{}, 1), logger.New(false))
	h.SetFilterStatus(func() filter.Snapshot {
		return filter.Snapshot{Rules: 42, BlockedQueries: 3, Sources: []filter.SourceStatus{{Source: "https://example.com/ads.txt", Rules: 42}}}
	})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/filters", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got filter.Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Rules != 42 || got.BlockedQueries != 3 || len(got.Sources) != 1 {
		t.Errorf("status response: %+v, %v", got, err)
	}
}

func TestStatsEndpointIncludesBlockedDomainRanking(t *testing.T) {
	recorder := stats.NewStats()
	recorder.RecordBlockedQuery("192.0.2.10", "ads.example.")
	h := NewHandler(recorder, "", make(chan struct{}, 1), logger.New(false))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got stats.StatsSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.TopBlockedDomains) != 1 || got.TopBlockedDomains[0].Key != "ads.example." || got.TopBlockedDomains[0].TopClientCount != 1 {
		t.Errorf("blocked ranking response = %+v", got.TopBlockedDomains)
	}
}

func TestStatsEndpointIncludesECSBlackholeFixes(t *testing.T) {
	recorder := stats.NewStats()
	recorder.RecordECSBlackholeFallback(stats.ECSBlackholeEvent{Domain: "geo-blocked.example.", Upstream: "tcp-tls://dns.example:853",
		BlackholeAddresses: []string{"169.254.0.2"}, PublicAddresses: []string{"203.0.113.10"}})
	h := NewHandler(recorder, "", make(chan struct{}, 1), logger.New(false))
	h.SetECSBlackholeStatus(func() string { return "enabled" })
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got stats.StatsSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	fixes := got.ECSBlackhole
	if fixes.Status != "enabled" || fixes.Fixed != 1 || len(fixes.Domains) != 1 || fixes.Domains[0].Domain != "geo-blocked.example." ||
		len(fixes.Domains[0].BlackholeAddresses) != 1 || fixes.Domains[0].BlackholeAddresses[0] != "169.254.0.2" {
		t.Errorf("ecs blackhole response = %+v", fixes)
	}
}

func TestStatsEndpointECSBlackholeStatusUnknownWithoutProvider(t *testing.T) {
	h := NewHandler(stats.NewStats(), "", make(chan struct{}, 1), logger.New(false))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	var got map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	raw, ok := got["ecs_blackhole"]
	if !ok {
		t.Fatal("ecs_blackhole section missing")
	}
	var section map[string]any
	if err := json.Unmarshal(raw, &section); err != nil {
		t.Fatal(err)
	}
	if _, hasStatus := section["status"]; hasStatus {
		t.Errorf("status must be omitted when unknown: %v", section)
	}
	if domains, ok := section["domains"].([]any); !ok || len(domains) != 0 {
		t.Errorf("domains must be an empty array: %v", section["domains"])
	}
}
