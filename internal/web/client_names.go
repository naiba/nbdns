package web

import (
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const (
	clientNameTTL         = time.Hour
	clientNameMissingTTL  = 10 * time.Minute // Retry unnamed clients: DHCP may learn the name later.
	clientNameTimeout     = time.Second
	clientNameCacheLimit  = 4096
	clientNameQueueLength = 256
)

// ClientNameResolver labels client addresses with PTR names from a resolver
// that knows the LAN, typically the DHCP server's dnsmasq. Lookups run in the
// background so the dashboard never waits on DNS; a name appears on a later refresh.
type ClientNameResolver struct {
	server  string
	client  *dns.Client
	queue   chan string
	stop    chan struct{}
	mu      sync.Mutex
	entries map[string]clientNameEntry
}

type clientNameEntry struct {
	name     string
	expires  time.Time
	inFlight bool
}

// NewClientNameResolver queries server ("host" or "host:port", port 53 by default).
func NewClientNameResolver(server string) *ClientNameResolver {
	if _, _, err := net.SplitHostPort(server); err != nil {
		server = net.JoinHostPort(strings.Trim(server, "[]"), "53")
	}
	r := &ClientNameResolver{
		server:  server,
		client:  &dns.Client{Timeout: clientNameTimeout},
		queue:   make(chan string, clientNameQueueLength),
		stop:    make(chan struct{}),
		entries: make(map[string]clientNameEntry),
	}
	go r.work()
	return r
}

// Close stops the background lookups.
func (r *ClientNameResolver) Close() { close(r.stop) }

// Names returns the known names of the given addresses and schedules lookups
// for unknown or expired ones.
func (r *ClientNameResolver) Names(addresses []string) map[string]string {
	names := make(map[string]string)
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, address := range addresses {
		ip, err := netip.ParseAddr(address)
		if err != nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		entry, known := r.entries[address]
		if known && entry.name != "" {
			names[address] = entry.name
		}
		if entry.inFlight || (known && now.Before(entry.expires)) {
			continue
		}
		if len(r.entries) >= clientNameCacheLimit {
			r.evictExpired(now)
		}
		select {
		case r.queue <- address:
			entry.inFlight = true
			r.entries[address] = entry
		default: // Queue full: retry on a later refresh.
		}
	}
	return names
}

// evictExpired keeps the cache bounded against spoofed client subnets.
func (r *ClientNameResolver) evictExpired(now time.Time) {
	for address, entry := range r.entries {
		if !entry.inFlight && now.After(entry.expires) {
			delete(r.entries, address)
		}
	}
	if len(r.entries) >= clientNameCacheLimit {
		for address, entry := range r.entries {
			if !entry.inFlight {
				delete(r.entries, address)
			}
		}
	}
}

func (r *ClientNameResolver) work() {
	for {
		select {
		case <-r.stop:
			return
		case address := <-r.queue:
			name := r.lookup(address)
			ttl := clientNameTTL
			if name == "" {
				ttl = clientNameMissingTTL
			}
			r.mu.Lock()
			r.entries[address] = clientNameEntry{name: name, expires: time.Now().Add(ttl)}
			r.mu.Unlock()
		}
	}
}

func (r *ClientNameResolver) lookup(address string) string {
	reverse, err := dns.ReverseAddr(address)
	if err != nil {
		return ""
	}
	query := new(dns.Msg).SetQuestion(reverse, dns.TypePTR)
	resp, _, err := r.client.Exchange(query, r.server)
	if err != nil || resp.Rcode != dns.RcodeSuccess {
		return ""
	}
	for _, rr := range resp.Answer {
		if ptr, ok := rr.(*dns.PTR); ok {
			return strings.TrimSuffix(ptr.Ptr, ".")
		}
	}
	return ""
}
