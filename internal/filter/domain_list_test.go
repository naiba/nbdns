package filter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDomainListFormats(t *testing.T) {
	list := strings.Join([]string{
		"# comment",
		"! comment",
		"",
		"server=/dnsmasq.example/114.114.114.114",           // felixonmars dnsmasq format
		"server=/multi-a.example/multi-b.example/127.0.0.1", // several domains on one line
		"nftset=/nftset.example/4#inet#fw4#set",
		"ipset=/ipset.example/set",
		"plain.example",
		".leading-dot.example",
		"+.clash-plus.example",
		"domain:prefixed.example",
		"full:exact.example", // Loyalsoldier exact match
		"DOMAIN-SUFFIX,clash-suffix.example",
		"DOMAIN,clash-exact.example",
		"regexp:^unsupported\\.example$",
		"keyword:unsupported",
		"not a domain line",
	}, "\n")
	f := New()
	stats, err := parseDomainList(strings.NewReader(list), f)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Rules != 12 || stats.Unsupported != 3 {
		t.Errorf("stats = %+v, want 12 rules and 3 unsupported", stats)
	}
	for name, want := range map[string]bool{
		"dnsmasq.example.": true, "sub.dnsmasq.example.": true,
		"multi-a.example.": true, "x.multi-b.example.": true,
		"nftset.example.": true, "ipset.example.": true,
		"plain.example.": true, "deep.sub.plain.example.": true,
		"leading-dot.example.": true, "clash-plus.example.": true, "a.prefixed.example.": true,
		"exact.example.": true, "sub.exact.example.": false, // full: never matches subdomains
		"clash-suffix.example.": true, "a.clash-suffix.example.": true,
		"clash-exact.example.": true, "a.clash-exact.example.": false,
		"example.": false, "other.example.": false, "unsupported.example.": false,
		"PLAIN.Example.": true, // DNS names are case-insensitive
	} {
		if got := f.Blocked(name); got != want {
			t.Errorf("match(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestDomainListExactAndSuffixOnSameName(t *testing.T) {
	f := New()
	if _, err := parseDomainList(strings.NewReader("full:both.example\nboth.example\n"), f); err != nil {
		t.Fatal(err)
	}
	if !f.Blocked("both.example.") || !f.Blocked("sub.both.example.") {
		t.Error("a suffix entry must still match subdomains when an exact entry also exists")
	}
}

// Large lists are rewritten daily upstream; unchanged content must cost no
// download (ETag) and no write to flash storage.
func TestDomainListRefreshSkipsUnchangedContent(t *testing.T) {
	var body atomic.Value
	body.Store("server=/first.example/114.114.114.114\n")
	var etagEnabled atomic.Bool
	etagEnabled.Store(true)
	var fullResponses, notModified atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content := body.Load().(string)
		etag := `"` + strconv.Itoa(len(content)) + strings.ReplaceAll(content[:5], " ", "") + `"`
		if etagEnabled.Load() {
			w.Header().Set("ETag", etag)
			if r.Header.Get("If-None-Match") == etag {
				notModified.Add(1)
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		fullResponses.Add(1)
		_, _ = w.Write([]byte(content))
	}))
	defer server.Close()
	dir := t.TempDir()
	m, err := NewDomainListManager(dir, []string{server.URL + "/china.conf"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.LoadInitial(); err != nil {
		t.Fatal(err)
	}
	if err := m.Refresh(context.Background()); err != nil || !m.Contains("first.example.") {
		t.Fatalf("initial refresh: %v", err)
	}
	cachePath := m.sources[0].cachePath
	first, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond) // Make a rewrite visible in the mtime.

	// Unchanged with ETag: 304, nothing downloaded or written.
	if err := m.Refresh(context.Background()); err != nil || !m.Contains("first.example.") {
		t.Fatalf("304 refresh: %v", err)
	}
	if notModified.Load() != 1 || fullResponses.Load() != 1 {
		t.Errorf("full=%d notModified=%d, want a conditional request", fullResponses.Load(), notModified.Load())
	}
	// Unchanged without ETag support: downloaded, but not rewritten.
	etagEnabled.Store(false)
	if err := m.Refresh(context.Background()); err != nil || !m.Contains("first.example.") {
		t.Fatalf("identical refresh: %v", err)
	}
	if again, err := os.Stat(cachePath); err != nil || !again.ModTime().Equal(first.ModTime()) {
		t.Errorf("unchanged list was rewritten (mtime %v -> %v)", first.ModTime(), again.ModTime())
	}
	if got := m.Snapshot().Sources[0]; got.LastError != "" || got.Rules != 1 || got.LastSuccess.Before(first.ModTime()) {
		t.Errorf("status after unchanged refresh = %+v", got)
	}

	// Changed content is written and published.
	body.Store("server=/second.example/114.114.114.114\n")
	if err := m.Refresh(context.Background()); err != nil || !m.Contains("second.example.") || m.Contains("first.example.") {
		t.Fatalf("changed refresh: %v", err)
	}
	if data, _ := os.ReadFile(cachePath); !strings.Contains(string(data), "second.example") {
		t.Errorf("changed list not cached: %q", data)
	}

	// Restart: the cache loads without any request.
	requests := fullResponses.Load() + notModified.Load()
	restarted, err := NewDomainListManager(dir, []string{server.URL + "/china.conf"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.LoadInitial(); err != nil || !restarted.Contains("second.example.") {
		t.Fatalf("cached list after restart: %v", err)
	}
	if fullResponses.Load()+notModified.Load() != requests {
		t.Error("LoadInitial must not touch the network")
	}
	if restarted.Snapshot().BlockedQueries != 0 {
		t.Error("domain list lookups must not count as blocked queries")
	}
}

func TestFilterSubscriptionSkipsRewritingUnchangedList(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("||ads.example^\n"))
	}))
	defer server.Close()
	m, err := NewManager(t.TempDir(), []string{server.URL + "/ads.txt"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(m.sources[0].cachePath)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := m.Refresh(context.Background()); err != nil || !m.Blocked("ads.example.") {
		t.Fatalf("second refresh: %v", err)
	}
	if again, _ := os.Stat(m.sources[0].cachePath); !again.ModTime().Equal(first.ModTime()) {
		t.Error("unchanged filter subscription was rewritten")
	}
}

func TestDomainListCacheIsSeparateFromFilterCache(t *testing.T) {
	dir := t.TempDir()
	lists, err := NewDomainListManager(dir, []string{"https://lists.example/shared.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	filters, err := NewManager(dir, []string{"https://lists.example/shared.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Separate fixed temporary files: the two managers refresh concurrently.
	if filepath.Dir(lists.sources[0].cachePath) == filepath.Dir(filters.sources[0].cachePath) {
		t.Error("domain lists and filter subscriptions must use separate cache directories")
	}
}

func BenchmarkDomainListContains(b *testing.B) {
	var list strings.Builder
	for i := 0; i < 110000; i++ {
		list.WriteString("server=/site" + strconv.Itoa(i) + ".example/114.114.114.114\n")
	}
	f := New()
	if _, err := parseDomainList(strings.NewReader(list.String()), f); err != nil {
		b.Fatal(err)
	}
	names := []string{"www.site54321.example.", "cdn.images.unlisted.example.", "site109999.example."}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f.Blocked(names[i%len(names)])
	}
}

// A deleted cache must be restored even though the list itself is unchanged,
// otherwise skipping unchanged content would leave it missing forever.
func TestDomainListRestoresDeletedCache(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte("server=/kept.example/114.114.114.114\n"))
	}))
	defer server.Close()
	m, err := NewDomainListManager(t.TempDir(), []string{server.URL + "/list.conf"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(m.sources[0].cachePath); err != nil {
		t.Fatal(err)
	}
	if err := m.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(m.sources[0].cachePath); err != nil || !strings.Contains(string(data), "kept.example") {
		t.Fatalf("deleted cache not restored: %q, %v", data, err)
	}
	if !m.Contains("kept.example.") {
		t.Error("active list lost")
	}
}
