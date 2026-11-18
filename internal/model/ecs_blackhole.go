package model

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// Some authorities geo-block by ECS: an ECS from a blocked region gets a CNAME
// to an edge answering A 169.254.0.2. dnsmasq rebind protection then drops it
// and clients see an empty NOERROR. Such blackholes are always special-use
// addresses, so an ECS answer containing one is retried once without ECS.
// The retry is only accepted when it is fully public: domains that genuinely
// point at LAN addresses resolve privately either way and are left untouched.
var ecsBlackholePrefixes = func() []netip.Prefix {
	var prefixes []netip.Prefix
	for _, cidr := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4",
		"::/128", "::1/128", "fe80::/10", "fc00::/7", "ff00::/8",
	} {
		prefixes = append(prefixes, netip.MustParsePrefix(cidr))
	}
	return prefixes
}()

const (
	ecsBlackholeMemoryTTL      = 24 * time.Hour
	ecsBlackholeMemoryCapacity = 4096
)

func isECSBlackholeAddress(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range ecsBlackholePrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func answerIPs(msg *dns.Msg) []net.IP {
	if msg == nil {
		return nil
	}
	var ips []net.IP
	for _, rr := range msg.Answer {
		switch record := rr.(type) {
		case *dns.A:
			ips = append(ips, record.A)
		case *dns.AAAA:
			ips = append(ips, record.AAAA)
		}
	}
	return ips
}

// answerAddressClasses counts A/AAAA answers and how many of them are blackholes.
func answerAddressClasses(msg *dns.Msg) (addresses, blackholes int) {
	for _, ip := range answerIPs(msg) {
		addresses++
		if isECSBlackholeAddress(ip) {
			blackholes++
		}
	}
	return addresses, blackholes
}

// answerAddressStrings lists unique A/AAAA answers for display.
func answerAddressStrings(msg *dns.Msg, blackholes bool) []string {
	var result []string
	seen := make(map[string]bool)
	for _, ip := range answerIPs(msg) {
		if isECSBlackholeAddress(ip) != blackholes {
			continue
		}
		text := ip.String()
		if !seen[text] {
			seen[text] = true
			result = append(result, text)
		}
	}
	return result
}

// ECSBlackholeFallback describes an answer served without ECS because the ECS
// answer of the same name was a blackhole.
type ECSBlackholeFallback struct {
	Domain             string
	Upstream           string
	BlackholeAddresses []string // From the ECS answer that triggered detection
	PublicAddresses    []string // From the answer actually served
	Remembered         bool     // Served from memory without a new ECS probe
	Reason             string   // ECSFallbackReasonBlackhole or ECSFallbackReasonRefused
}

const (
	// ECSFallbackReasonBlackhole: the ECS answer contained special-use addresses.
	ECSFallbackReasonBlackhole = "blackhole"
	// ECSFallbackReasonRefused: the ECS query was REFUSED but the name resolves
	// without ECS. Some GeoDNS names refuse every query that carries ECS, which
	// used to reach clients as SERVFAIL.
	ECSFallbackReasonRefused = "refused"
)

// isUsableReply is an answer worth serving instead of a failed ECS probe.
func isUsableReply(msg *dns.Msg) bool {
	return msg != nil && !msg.Truncated && (msg.Rcode == dns.RcodeSuccess || msg.Rcode == dns.RcodeNameError)
}

// ecsFallbackReason reports why an ECS reply must be retried without ECS, if at all.
func ecsFallbackReason(msg *dns.Msg) string {
	switch {
	case msg != nil && msg.Rcode == dns.RcodeRefused:
		return ECSFallbackReasonRefused
	case hasECSBlackhole(msg):
		return ECSFallbackReasonBlackhole
	}
	return ""
}

// acceptsECSFallback reports whether the retry without ECS repairs the reply:
// blackholes need a fully public answer, refusals any usable non-blackhole one.
func acceptsECSFallback(reason string, plain *dns.Msg) bool {
	if reason == ECSFallbackReasonBlackhole {
		return isPublicAddressAnswer(plain)
	}
	return isUsableReply(plain) && !hasECSBlackhole(plain)
}

type ecsBlackholeReportKey struct{}

// ECSBlackholeReport receives the fallback of one exchange. Each racing upstream
// gets its own report so only the winning answer is counted.
type ECSBlackholeReport struct {
	Fallback *ECSBlackholeFallback
}

func WithECSBlackholeReport(ctx context.Context, report *ECSBlackholeReport) context.Context {
	return context.WithValue(ctx, ecsBlackholeReportKey{}, report)
}

func reportECSBlackholeFallback(ctx context.Context, fallback *ECSBlackholeFallback) {
	if report, ok := ctx.Value(ecsBlackholeReportKey{}).(*ECSBlackholeReport); ok && report != nil {
		report.Fallback = fallback
	}
}

func hasECSBlackhole(msg *dns.Msg) bool {
	if msg == nil || msg.Rcode != dns.RcodeSuccess {
		return false
	}
	_, blackholes := answerAddressClasses(msg)
	return blackholes > 0
}

func isPublicAddressAnswer(msg *dns.Msg) bool {
	if msg == nil || msg.Rcode != dns.RcodeSuccess || msg.Truncated {
		return false
	}
	addresses, blackholes := answerAddressClasses(msg)
	return addresses > 0 && blackholes == 0
}

// ecsBlackholeMemory remembers names whose ECS answers were blackholes so later
// cache misses query them without ECS directly instead of probing twice.
type ecsBlackholeMemory struct {
	mu       sync.Mutex
	entries  map[string]ecsBlackholeMemoryEntry
	sequence uint64
}

type ecsBlackholeMemoryEntry struct {
	expires    time.Time
	sequence   uint64 // Breaks expiry ties deterministically
	reason     string
	blackholes []string
}

func ecsBlackholeMemoryKey(name string) string {
	return strings.ToLower(dns.Fqdn(name))
}

// lookup returns why the name was remembered and the blackholes seen then.
func (m *ecsBlackholeMemory) lookup(name string) (reason string, blackholes []string, ok bool) {
	key := ecsBlackholeMemoryKey(name)
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[key]
	if ok && time.Now().After(entry.expires) {
		delete(m.entries, key)
		return "", nil, false
	}
	return entry.reason, entry.blackholes, ok
}

func (m *ecsBlackholeMemory) remember(name, reason string, blackholes []string) {
	key := ecsBlackholeMemoryKey(name)
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = make(map[string]ecsBlackholeMemoryEntry)
	}
	if _, exists := m.entries[key]; !exists && len(m.entries) >= ecsBlackholeMemoryCapacity {
		for name, entry := range m.entries {
			if now.After(entry.expires) {
				delete(m.entries, name)
			}
		}
		if len(m.entries) >= ecsBlackholeMemoryCapacity {
			// Bounded memory: forget the entry expiring first; that only costs it one extra probe.
			var victim string
			var earliest ecsBlackholeMemoryEntry
			for name, entry := range m.entries {
				if victim == "" || entry.expires.Before(earliest.expires) ||
					(entry.expires.Equal(earliest.expires) && entry.sequence < earliest.sequence) {
					victim, earliest = name, entry
				}
			}
			delete(m.entries, victim)
		}
	}
	m.sequence++
	m.entries[key] = ecsBlackholeMemoryEntry{expires: now.Add(ecsBlackholeMemoryTTL), sequence: m.sequence, reason: reason, blackholes: blackholes}
}
