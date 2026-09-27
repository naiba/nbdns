package filter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSubscriptionRefreshKeepsLastGoodList(t *testing.T) {
	var body atomic.Value
	body.Store("||ads.example^\n")
	var failing atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	defer server.Close()
	dir := t.TempDir()
	m, err := NewManager(dir, []string{server.URL + "/ads.txt"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.LoadInitial(); err != nil {
		t.Fatal(err)
	}
	if m.Blocked("ads.example.") {
		t.Fatal("list unexpectedly active before first download")
	}
	if err := m.Refresh(context.Background()); err != nil || !m.Blocked("ads.example.") {
		t.Fatalf("initial refresh: %v, blocked=%t", err, m.Blocked("ads.example."))
	}
	body.Store("||new.example^\n")
	if err := m.Refresh(context.Background()); err != nil || m.Blocked("ads.example.") || !m.Blocked("new.example.") {
		t.Fatalf("refresh did not atomically replace rules: %v", err)
	}
	body.Store("<html>bad response</html>")
	if err := m.Refresh(context.Background()); err == nil || !m.Blocked("new.example.") {
		t.Fatalf("invalid list replaced live rules: %v", err)
	}
	body.Store("||partial.example^\n" + strings.Repeat("x", (1<<20)+1))
	if err := m.Refresh(context.Background()); err == nil || m.Blocked("partial.example.") || !m.Blocked("new.example.") {
		t.Fatalf("partially parsed list replaced live rules: %v", err)
	}
	failing.Store(true)
	if err := m.Refresh(context.Background()); err == nil || !m.Blocked("new.example.") {
		t.Fatalf("failed request replaced live rules: %v", err)
	}
	if got := m.Snapshot(); len(got.Sources) != 1 || got.Sources[0].LastError == "" || got.Rules != 1 {
		t.Errorf("failed subscription status: %+v", got)
	}
	restarted, err := NewManager(dir, []string{server.URL + "/ads.txt"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.LoadInitial(); err != nil || !restarted.Blocked("new.example.") {
		t.Fatalf("cached list unavailable after restart: %v", err)
	}
}

func TestMultipleSubscriptionsAndLocalExceptions(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("||ads.example^\n"))
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "allow.txt"), []byte("@@||safe.ads.example^\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(dir, []string{"allow.txt", server.URL + "/ads.txt"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.LoadInitial(); err != nil {
		t.Fatal(err)
	}
	if err := m.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !m.Blocked("ads.example.") || m.Blocked("safe.ads.example.") {
		t.Error("local allow rule or remote block rule was not applied")
	}
	got := m.Snapshot()
	if len(got.Sources) != 2 || got.Rules != 2 || got.BlockedQueries != 1 {
		t.Errorf("snapshot = %+v", got)
	}
}

func TestSubscriptionRejectsInsecureURLAndDoesNotFollowDowngrade(t *testing.T) {
	if _, err := NewManager(t.TempDir(), []string{"http://example.com/list.txt"}, nil); err == nil {
		t.Fatal("plain HTTP subscription accepted")
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("||wrong.example^\n"))
	}))
	defer target.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer server.Close()
	m, err := NewManager(t.TempDir(), []string{server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Refresh(context.Background()); err == nil || m.Blocked("wrong.example.") {
		t.Fatalf("insecure redirect followed: %v", err)
	}
	if m.Snapshot().Sources[0].FromCache {
		t.Fatal("dashboard reports cached rules when no cache exists")
	}
}

func TestSubscriptionURLRedactsQueryInStatus(t *testing.T) {
	m, err := NewManager(t.TempDir(), []string{"https://example.com/list.txt?token=secret"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Snapshot().Sources[0].Source; strings.Contains(got, "secret") {
		t.Fatalf("credentials exposed in UI status: %q", got)
	}
}

func TestPeriodicSubscriptionUpdates(t *testing.T) {
	var body atomic.Value
	body.Store("||first.example^\n")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	defer server.Close()
	m, err := NewManager(t.TempDir(), []string{server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { m.Run(ctx, 15*time.Millisecond, nil); close(finished) }()
	t.Cleanup(func() { cancel(); <-finished })
	deadline := time.Now().Add(2 * time.Second)
	for !m.Blocked("first.example.") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !m.Blocked("first.example.") {
		t.Fatal("initial background refresh did not occur")
	}
	body.Store("||second.example^\n")
	for !m.Blocked("second.example.") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !m.Blocked("second.example.") || m.Blocked("first.example.") {
		t.Fatal("periodic update did not replace active rules")
	}
}
