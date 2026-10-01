package stats

import (
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func TestDomainRankingsSeparateResolvedAndBlockedClients(t *testing.T) {
	s := NewStats()
	for range 2 {
		s.RecordClientQuery("192.0.2.1", "resolved.example.")
	}
	s.RecordClientQuery("192.0.2.2", "resolved.example.")
	for range 3 {
		s.RecordBlockedQuery("192.0.2.2", "blocked.example.")
	}
	s.RecordBlockedQuery("192.0.2.1", "blocked.example.")

	snapshot := s.GetSnapshot()
	if len(snapshot.TopDomains) != 1 || snapshot.TopDomains[0].Count != 3 {
		t.Fatalf("resolved domains = %+v", snapshot.TopDomains)
	}
	resolved := snapshot.TopDomains[0]
	if resolved.TopClient != "192.0.2.1" || resolved.TopClientCount != 2 || len(resolved.TopClients) != 2 {
		t.Errorf("resolved client ranking = %+v", resolved)
	}
	if len(snapshot.TopBlockedDomains) != 1 || snapshot.TopBlockedDomains[0].Count != 4 {
		t.Fatalf("blocked domains = %+v", snapshot.TopBlockedDomains)
	}
	blocked := snapshot.TopBlockedDomains[0]
	if blocked.TopClient != "192.0.2.2" || blocked.TopClientCount != 3 || len(blocked.TopClients) != 2 {
		t.Errorf("blocked client ranking = %+v", blocked)
	}
	if len(snapshot.TopClients) != 2 || snapshot.TopClients[0].Key != "192.0.2.2" || snapshot.TopClients[0].Count != 4 {
		t.Errorf("all client ranking = %+v", snapshot.TopClients)
	}
}

func TestDomainRankingsPersistAndReturnTopFifty(t *testing.T) {
	s := NewStats()
	for i := 0; i < 60; i++ {
		domain := "domain-" + strconv.Itoa(i) + ".example."
		s.RecordBlockedQuery("192.0.2.1", domain)
	}
	dataPath := t.TempDir()
	if err := s.Save(dataPath); err != nil {
		t.Fatal(err)
	}

	loaded := NewStats()
	if err := loaded.Load(dataPath); err != nil {
		t.Fatalf("load %s: %v", filepath.Join(dataPath, "cache", "stats.json"), err)
	}
	got := loaded.GetSnapshot().TopBlockedDomains
	if len(got) != topDomainLimit {
		t.Fatalf("blocked domain count = %d, want %d", len(got), topDomainLimit)
	}
	if got[0].TopClient != "192.0.2.1" || got[0].TopClientCount != 1 {
		t.Errorf("persisted client stats = %+v", got[0])
	}
}

func TestClientTrackingIsBoundedPerDomain(t *testing.T) {
	s := NewStats()
	for i := 0; i < 1000; i++ {
		s.RecordBlockedQuery("client-"+strconv.Itoa(i), "blocked.example.")
	}
	item := s.topBlockedDomains.items["blocked.example."]
	if got := len(item.clients); got != maxClientsPerDomain {
		t.Fatalf("tracked clients = %d, want bounded at %d", got, maxClientsPerDomain)
	}
	if got := len(s.GetSnapshot().TopBlockedDomains[0].TopClients); got != topClientLimit {
		t.Fatalf("returned clients = %d, want %d", got, topClientLimit)
	}
}

func BenchmarkStatsRecord(b *testing.B) {
	s := NewStats()
	s.RecordClientQuery("192.0.2.1", "resolved.example.")
	s.RecordBlockedQuery("192.0.2.2", "blocked.example.")
	b.Run("resolved_existing", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			s.RecordClientQuery("192.0.2.1", "resolved.example.")
		}
	})
	b.Run("blocked_existing", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			s.RecordBlockedQuery("192.0.2.2", "blocked.example.")
		}
	})

	clients := make([]string, 1024)
	for i := range clients {
		clients[i] = "client-" + strconv.Itoa(i)
	}
	b.Run("blocked_high_cardinality", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; b.Loop(); i++ {
			s.RecordBlockedQuery(clients[i%len(clients)], "high-cardinality.example.")
		}
	})
}

func BenchmarkStatsSnapshot(b *testing.B) {
	s := NewStats()
	for domain := 0; domain < 200; domain++ {
		name := "resolved-" + strconv.Itoa(domain) + ".example."
		blockedName := "blocked-" + strconv.Itoa(domain) + ".example."
		for client := 0; client < maxClientsPerDomain; client++ {
			address := "client-" + strconv.Itoa(client)
			s.RecordClientQuery(address, name)
			s.RecordBlockedQuery(address, blockedName)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = s.GetSnapshot()
	}
}

func BenchmarkStatsPopulateBounded(b *testing.B) {
	domains := make([]string, 200)
	clients := make([]string, maxClientsPerDomain)
	for i := range domains {
		domains[i] = "domain-" + strconv.Itoa(i) + ".example."
	}
	for i := range clients {
		clients[i] = "client-" + strconv.Itoa(i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s := NewStats()
		for _, domain := range domains {
			for _, client := range clients {
				s.RecordClientQuery(client, domain)
				s.RecordBlockedQuery(client, domain)
			}
		}
		runtime.KeepAlive(s)
	}
}
