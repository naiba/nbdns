package stats

import (
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestECSBlackholeFixesAggregatePerDomain(t *testing.T) {
	s := NewStats()
	s.RecordECSBlackholeFallback(ECSBlackholeEvent{Domain: "geo-blocked.example.", Upstream: "tcp-tls://dns.example:853",
		BlackholeAddresses: []string{"169.254.0.2"}, PublicAddresses: []string{"203.0.113.10", "203.0.113.11"}})
	time.Sleep(2 * time.Millisecond)
	// A remembered fix reports public addresses; blackholes come from detection memory.
	s.RecordECSBlackholeFallback(ECSBlackholeEvent{Domain: "GEO-BLOCKED.EXAMPLE.", Upstream: "tcp-tls://dns.example:853",
		BlackholeAddresses: []string{"169.254.0.2"}, PublicAddresses: []string{"203.0.113.11"}, Remembered: true})
	s.RecordECSBlackholeFallback(ECSBlackholeEvent{Domain: "geo.example.", Upstream: "udp://192.0.2.53:53",
		BlackholeAddresses: []string{"0.0.0.0"}, PublicAddresses: []string{"198.51.100.7"}})

	got := s.GetSnapshot().ECSBlackhole
	if got.Fixed != 3 {
		t.Errorf("fixed = %d, want 3", got.Fixed)
	}
	if len(got.Domains) != 2 {
		t.Fatalf("domains = %+v", got.Domains)
	}
	// Most recently fixed domain first.
	if got.Domains[0].Domain != "geo.example." || got.Domains[1].Domain != "geo-blocked.example." {
		t.Fatalf("order = %s, %s", got.Domains[0].Domain, got.Domains[1].Domain)
	}
	repaired := got.Domains[1]
	if repaired.Count != 2 || repaired.Upstream != "tcp-tls://dns.example:853" {
		t.Errorf("repaired aggregate = %+v", repaired)
	}
	if !reflect.DeepEqual(repaired.BlackholeAddresses, []string{"169.254.0.2"}) || !reflect.DeepEqual(repaired.PublicAddresses, []string{"203.0.113.11"}) {
		t.Errorf("repaired addresses = %+v", repaired)
	}
	if repaired.FirstSeen.IsZero() || !repaired.LastSeen.After(repaired.FirstSeen) {
		t.Errorf("repaired timestamps first=%v last=%v", repaired.FirstSeen, repaired.LastSeen)
	}
}

func TestECSBlackholeFixesPersistAndReset(t *testing.T) {
	s := NewStats()
	s.RecordECSBlackholeFallback(ECSBlackholeEvent{Domain: "geo-blocked.example.", Upstream: "tcp-tls://dns.example:853",
		BlackholeAddresses: []string{"169.254.0.2"}, PublicAddresses: []string{"203.0.113.10"}})
	dataPath := t.TempDir()
	if err := s.Save(dataPath); err != nil {
		t.Fatal(err)
	}
	loaded := NewStats()
	if err := loaded.Load(dataPath); err != nil {
		t.Fatal(err)
	}
	want := s.GetSnapshot().ECSBlackhole
	got := loaded.GetSnapshot().ECSBlackhole
	if got.Fixed != 1 || len(got.Domains) != 1 {
		t.Fatalf("loaded = %+v", got)
	}
	if !got.Domains[0].FirstSeen.Equal(want.Domains[0].FirstSeen) || got.Domains[0].Domain != "geo-blocked.example." ||
		!reflect.DeepEqual(got.Domains[0].BlackholeAddresses, []string{"169.254.0.2"}) || got.Domains[0].Count != 1 {
		t.Errorf("loaded domain = %+v want %+v", got.Domains[0], want.Domains[0])
	}

	loaded.Reset()
	if reset := loaded.GetSnapshot().ECSBlackhole; reset.Fixed != 0 || len(reset.Domains) != 0 {
		t.Errorf("reset left %+v", reset)
	}
}

func TestECSBlackholeFixesAreBounded(t *testing.T) {
	s := NewStats()
	for i := 0; i < ecsBlackholeDomainCapacity+20; i++ {
		s.RecordECSBlackholeFallback(ECSBlackholeEvent{Domain: "d" + strconv.Itoa(i) + ".example.", PublicAddresses: []string{"198.51.100.1"}})
	}
	s.mu.RLock()
	tracked := len(s.ecsBlackholeDomains)
	s.mu.RUnlock()
	if tracked != ecsBlackholeDomainCapacity {
		t.Errorf("tracked = %d, want %d", tracked, ecsBlackholeDomainCapacity)
	}
	got := s.GetSnapshot().ECSBlackhole
	if got.Fixed != uint64(ecsBlackholeDomainCapacity+20) {
		t.Errorf("fixed = %d", got.Fixed)
	}
	if len(got.Domains) != ecsBlackholeDomainLimit {
		t.Fatalf("shown = %d, want %d", len(got.Domains), ecsBlackholeDomainLimit)
	}
	// The oldest entries are evicted, the newest are shown first.
	last := "d" + strconv.Itoa(ecsBlackholeDomainCapacity+19) + ".example."
	if got.Domains[0].Domain != last {
		t.Errorf("first shown = %s, want %s", got.Domains[0].Domain, last)
	}
	s.mu.RLock()
	_, oldestKept := s.ecsBlackholeDomains["d0.example."]
	s.mu.RUnlock()
	if oldestKept {
		t.Error("oldest domain must be evicted")
	}
}

func TestECSBlackholeSnapshotNeverNull(t *testing.T) {
	if got := NewStats().GetSnapshot().ECSBlackhole.Domains; got == nil {
		t.Error("domains must encode as [] for the dashboard")
	}
}

func TestECSBlackholeAddressesMergePerFamily(t *testing.T) {
	s := NewStats()
	s.RecordECSBlackholeFallback(ECSBlackholeEvent{Domain: "geo-blocked.example.", BlackholeAddresses: []string{"169.254.0.2"},
		PublicAddresses: []string{"203.0.113.10", "203.0.113.11"}})
	// The AAAA fix of the same name must not hide the IPv4 answer, and vice versa.
	s.RecordECSBlackholeFallback(ECSBlackholeEvent{Domain: "geo-blocked.example.", BlackholeAddresses: []string{"169.254.0.2"},
		PublicAddresses: []string{"2001:db8:10::10"}})
	s.RecordECSBlackholeFallback(ECSBlackholeEvent{Domain: "geo-blocked.example.", BlackholeAddresses: []string{"169.254.0.2"},
		PublicAddresses: []string{"203.0.113.11"}})
	got := s.GetSnapshot().ECSBlackhole.Domains[0]
	if want := []string{"203.0.113.11", "2001:db8:10::10"}; !reflect.DeepEqual(got.PublicAddresses, want) {
		t.Errorf("public addresses = %v, want %v", got.PublicAddresses, want)
	}
	if want := []string{"169.254.0.2"}; !reflect.DeepEqual(got.BlackholeAddresses, want) {
		t.Errorf("blackhole addresses = %v, want %v", got.BlackholeAddresses, want)
	}
}

func TestECSFixReasonIsRecordedAndPersisted(t *testing.T) {
	s := NewStats()
	s.RecordECSBlackholeFallback(ECSBlackholeEvent{Domain: "refused.example.", Reason: "refused", PublicAddresses: []string{"203.0.113.20"}})
	s.RecordECSBlackholeFallback(ECSBlackholeEvent{Domain: "geo-blocked.example.", Reason: "blackhole",
		BlackholeAddresses: []string{"169.254.0.2"}, PublicAddresses: []string{"203.0.113.10"}})
	// A remembered fix without a reason (older callers) keeps the known reason.
	s.RecordECSBlackholeFallback(ECSBlackholeEvent{Domain: "refused.example.", PublicAddresses: []string{"203.0.113.20"}})
	dataPath := t.TempDir()
	if err := s.Save(dataPath); err != nil {
		t.Fatal(err)
	}
	loaded := NewStats()
	if err := loaded.Load(dataPath); err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, d := range loaded.GetSnapshot().ECSBlackhole.Domains {
		reasons[d.Domain] = d.Reason
	}
	if reasons["refused.example."] != "refused" || reasons["geo-blocked.example."] != "blackhole" {
		t.Errorf("reasons = %v", reasons)
	}
}

func TestECSFixQueryTypesAreRecordedAndPersisted(t *testing.T) {
	s := NewStats()
	s.RecordECSBlackholeFallback(ECSBlackholeEvent{Domain: "edge.example.", Reason: "refused", QueryType: "HTTPS"})
	s.RecordECSBlackholeFallback(ECSBlackholeEvent{Domain: "edge.example.", Reason: "refused", QueryType: "HTTPS"})
	s.RecordECSBlackholeFallback(ECSBlackholeEvent{Domain: "edge.example.", Reason: "refused", QueryType: "A", PublicAddresses: []string{"203.0.113.20"}})
	s.RecordECSBlackholeFallback(ECSBlackholeEvent{Domain: "legacy.example.", Reason: "refused"}) // Callers without a type.
	dataPath := t.TempDir()
	if err := s.Save(dataPath); err != nil {
		t.Fatal(err)
	}
	loaded := NewStats()
	if err := loaded.Load(dataPath); err != nil {
		t.Fatal(err)
	}
	types := map[string][]string{}
	for _, d := range loaded.GetSnapshot().ECSBlackhole.Domains {
		types[d.Domain] = d.QueryTypes
	}
	if !reflect.DeepEqual(types["edge.example."], []string{"A", "HTTPS"}) || len(types["legacy.example."]) != 0 {
		t.Errorf("query types = %v", types)
	}
}
