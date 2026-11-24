package stats

import (
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	ecsBlackholeDomainCapacity = 200 // Tracked domains; the least recently fixed is evicted
	ecsBlackholeDomainLimit    = 50  // Domains shown on the dashboard
)

// ECSBlackholeEvent is one answer served without ECS because the ECS answer
// was a geo-blocking blackhole (e.g. 169.254.0.2).
type ECSBlackholeEvent struct {
	Domain             string
	Upstream           string
	BlackholeAddresses []string
	PublicAddresses    []string
	Remembered         bool
	Reason             string // "blackhole" or "refused"; empty keeps the known reason
	QueryType          string // e.g. "A" or "HTTPS"; empty when unknown
}

// ECSBlackholeDomainJSON aggregates fixes for one domain.
type ECSBlackholeDomainJSON struct {
	Domain             string   `json:"domain"`
	Upstream           string   `json:"upstream"`
	BlackholeAddresses []string `json:"blackhole_addresses"`
	PublicAddresses    []string `json:"public_addresses"`
	Reason             string   `json:"reason,omitempty"` // "blackhole" or "refused"
	// QueryTypes lists the repaired query types, sorted. A name repaired only for
	// HTTPS or AAAA queries without records legitimately has no public addresses.
	QueryTypes []string  `json:"query_types,omitempty"`
	Count      uint64    `json:"count"`
	FirstSeen  time.Time `json:"first_seen"`
	LastSeen   time.Time `json:"last_seen"`
}

// ECSBlackholeStats is the dashboard view. Status is filled by the web layer
// from configuration: "enabled", "disabled" or "no_ecs".
type ECSBlackholeStats struct {
	Status  string                   `json:"status,omitempty"`
	Fixed   uint64                   `json:"fixed"`
	Domains []ECSBlackholeDomainJSON `json:"domains"`
}

type ecsBlackholeDomain struct {
	ECSBlackholeDomainJSON
	sequence uint64 // Recency without relying on clock resolution
}

// RecordECSBlackholeFallback counts an answer repaired by ECS blackhole fallback.
func (s *Stats) RecordECSBlackholeFallback(event ECSBlackholeEvent) {
	domain := strings.ToLower(event.Domain)
	if domain == "" {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ecsBlackholeFixed++
	s.ecsBlackholeSequence++
	entry, ok := s.ecsBlackholeDomains[domain]
	if !ok {
		if len(s.ecsBlackholeDomains) >= ecsBlackholeDomainCapacity {
			s.evictOldestECSBlackholeDomainLocked()
		}
		entry = &ecsBlackholeDomain{ECSBlackholeDomainJSON: ECSBlackholeDomainJSON{Domain: domain, FirstSeen: now}}
		s.ecsBlackholeDomains[domain] = entry
	}
	entry.Count++
	entry.LastSeen = now
	entry.sequence = s.ecsBlackholeSequence
	if event.Upstream != "" {
		entry.Upstream = event.Upstream
	}
	if event.Reason != "" {
		entry.Reason = event.Reason
	}
	if event.QueryType != "" && !slices.Contains(entry.QueryTypes, event.QueryType) {
		entry.QueryTypes = append(entry.QueryTypes, event.QueryType)
		slices.Sort(entry.QueryTypes)
	}
	entry.BlackholeAddresses = mergeAddressFamilies(entry.BlackholeAddresses, event.BlackholeAddresses)
	entry.PublicAddresses = mergeAddressFamilies(entry.PublicAddresses, event.PublicAddresses)
}

// mergeAddressFamilies replaces only the families present in incoming: an AAAA
// fix of a name must not hide the addresses learned from its A fix. IPv4 first.
func mergeAddressFamilies(existing, incoming []string) []string {
	if len(incoming) == 0 {
		return existing
	}
	isIPv6 := func(address string) bool { return strings.Contains(address, ":") }
	replaceV4, replaceV6 := false, false
	for _, address := range incoming {
		if isIPv6(address) {
			replaceV6 = true
		} else {
			replaceV4 = true
		}
	}
	var v4, v6 []string
	add := func(address string) {
		if isIPv6(address) {
			v6 = append(v6, address)
		} else {
			v4 = append(v4, address)
		}
	}
	for _, address := range existing {
		if ipv6 := isIPv6(address); (ipv6 && !replaceV6) || (!ipv6 && !replaceV4) {
			add(address)
		}
	}
	for _, address := range incoming {
		add(address)
	}
	return append(v4, v6...)
}

func (s *Stats) evictOldestECSBlackholeDomainLocked() {
	var oldest *ecsBlackholeDomain
	for _, entry := range s.ecsBlackholeDomains {
		if oldest == nil || entry.sequence < oldest.sequence {
			oldest = entry
		}
	}
	if oldest != nil {
		delete(s.ecsBlackholeDomains, oldest.Domain)
	}
}

// ecsBlackholeDomainsLocked returns copies ordered from most to least recent.
func (s *Stats) ecsBlackholeDomainsLocked(limit int) []ECSBlackholeDomainJSON {
	entries := make([]*ecsBlackholeDomain, 0, len(s.ecsBlackholeDomains))
	for _, entry := range s.ecsBlackholeDomains {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].sequence > entries[j].sequence })
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	result := make([]ECSBlackholeDomainJSON, 0, len(entries))
	for _, entry := range entries {
		item := entry.ECSBlackholeDomainJSON
		item.BlackholeAddresses = append([]string{}, entry.BlackholeAddresses...)
		item.PublicAddresses = append([]string{}, entry.PublicAddresses...)
		item.QueryTypes = slices.Clone(entry.QueryTypes)
		result = append(result, item)
	}
	return result
}

func (s *Stats) ecsBlackholeSnapshot() ECSBlackholeStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return ECSBlackholeStats{Fixed: s.ecsBlackholeFixed, Domains: s.ecsBlackholeDomainsLocked(ecsBlackholeDomainLimit)}
}

// PersistentECSBlackhole is stored in stats.json; domains are ordered oldest first.
type PersistentECSBlackhole struct {
	Fixed   uint64                   `json:"fixed"`
	Domains []ECSBlackholeDomainJSON `json:"domains"`
}

func (s *Stats) persistentECSBlackholeLocked() *PersistentECSBlackhole {
	if s.ecsBlackholeFixed == 0 && len(s.ecsBlackholeDomains) == 0 {
		return nil
	}
	domains := s.ecsBlackholeDomainsLocked(0)
	for i, j := 0, len(domains)-1; i < j; i, j = i+1, j-1 {
		domains[i], domains[j] = domains[j], domains[i]
	}
	return &PersistentECSBlackhole{Fixed: s.ecsBlackholeFixed, Domains: domains}
}

func (s *Stats) restoreECSBlackholeLocked(persistent *PersistentECSBlackhole) {
	if persistent == nil {
		return
	}
	s.ecsBlackholeFixed = persistent.Fixed
	for _, item := range persistent.Domains {
		domain := strings.ToLower(item.Domain)
		if domain == "" {
			continue
		}
		if len(s.ecsBlackholeDomains) >= ecsBlackholeDomainCapacity {
			s.evictOldestECSBlackholeDomainLocked()
		}
		s.ecsBlackholeSequence++
		item.Domain = domain
		s.ecsBlackholeDomains[domain] = &ecsBlackholeDomain{ECSBlackholeDomainJSON: item, sequence: s.ecsBlackholeSequence}
	}
}
