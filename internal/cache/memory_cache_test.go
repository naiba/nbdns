package cache

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

type fakeStore struct {
	mu      sync.Mutex
	items   map[string]*CachedMsg
	gets    int
	sets    int
	closed  bool
	setFail bool
}

func (s *fakeStore) Get(key string) (*CachedMsg, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	v, ok := s.items[key]
	return v, ok
}
func (s *fakeStore) Set(key string, value *CachedMsg, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.setFail {
		return errors.New("store failed")
	}
	s.sets++
	s.items[key] = value
	return nil
}
func (s *fakeStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, key)
	return nil
}
func (s *fakeStore) Close() error  { s.closed = true; return nil }
func (s *fakeStore) Stats() string { return "fake disk" }

func cacheMessage(last byte, expires time.Time) *CachedMsg {
	msg := new(dns.Msg)
	msg.SetQuestion("test.example.", dns.TypeA)
	msg.Answer = []dns.RR{&dns.A{
		Hdr: dns.RR_Header{Name: "test.example.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
		A:   net.IPv4(192, 0, 2, last),
	}}
	return &CachedMsg{Msg: msg, Expires: expires}
}

func TestMemoryCacheEvictionAndBackfill(t *testing.T) {
	store := &fakeStore{items: make(map[string]*CachedMsg)}
	c := NewMemoryCache(store, 2, 4096)
	defer c.Close()
	for i := 1; i <= 3; i++ {
		if err := c.Set(fmt.Sprintf("k%d", i), cacheMessage(byte(i), time.Now().Add(time.Minute)), time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := c.Get("k2"); !ok {
		t.Fatal("hot entry missing")
	}
	if store.gets != 0 {
		t.Fatalf("hot memory hit read disk %d times", store.gets)
	}
	if _, ok := c.Get("k1"); !ok || store.gets != 1 {
		t.Fatalf("evicted entry did not backfill from disk (gets=%d)", store.gets)
	}
	if _, ok := c.Get("k1"); !ok || store.gets != 1 {
		t.Fatalf("backfill did not stay hot (gets=%d)", store.gets)
	}
}

func TestMemoryCacheNeverServesExpiredAsFresh(t *testing.T) {
	store := &fakeStore{items: make(map[string]*CachedMsg)}
	c := NewMemoryCache(store, 4, 4096)
	stale := cacheMessage(1, time.Now().Add(-time.Second))
	if err := c.Set("stale", stale, time.Hour); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		v, ok := c.Get("stale")
		if !ok || !v.Expires.Equal(stale.Expires) {
			t.Fatal("stale disk fallback unexpectedly removed")
		}
	}
	if store.gets != 2 {
		t.Errorf("expired entry stayed in memory; disk gets=%d", store.gets)
	}
}

func TestMemoryCacheBoundedByBytesAndDelete(t *testing.T) {
	store := &fakeStore{items: make(map[string]*CachedMsg)}
	msg := cacheMessage(1, time.Now().Add(time.Minute))
	c := NewMemoryCache(store, 100, msg.Msg.Len()+len("one")+128)
	if err := c.Set("one", msg, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := c.Set("two", cacheMessage(2, time.Now().Add(time.Minute)), time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := c.Stats(); !strings.Contains(got, "1/100 entries") {
		t.Errorf("byte budget not enforced: %s", got)
	}
	if err := c.Delete("two"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get("two"); ok {
		t.Fatal("deleted entry was returned")
	}
	if err := c.Close(); err != nil || !store.closed {
		t.Fatal("backing cache not closed")
	}
}

func TestMemoryCacheDoesNotPublishFailedWrite(t *testing.T) {
	store := &fakeStore{items: make(map[string]*CachedMsg), setFail: true}
	c := NewMemoryCache(store, 2, 4096)
	if err := c.Set("failed", cacheMessage(1, time.Now().Add(time.Minute)), time.Minute); err == nil {
		t.Fatal("failed disk write reported as success")
	}
	if _, ok := c.Get("failed"); ok {
		t.Fatal("unpersisted response added to hot cache")
	}
}

func TestMemoryCacheConcurrentReadsAndWrites(t *testing.T) {
	store := &fakeStore{items: make(map[string]*CachedMsg)}
	c := NewMemoryCache(store, 32, 8192)
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 300 {
				key := fmt.Sprintf("k%d", (worker+i)%64)
				if err := c.Set(key, cacheMessage(byte(i%255), time.Now().Add(time.Minute)), time.Minute); err != nil {
					t.Errorf("Set: %v", err)
				}
				if value, ok := c.Get(key); ok && value.Msg == nil {
					t.Error("nil cached message")
				}
			}
		}()
	}
	wg.Wait()
	if got := c.Stats(); !strings.Contains(got, "hot cache:") {
		t.Errorf("unexpected cache statistics: %s", got)
	}
}
