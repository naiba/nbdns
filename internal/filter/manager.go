package filter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxSubscriptionBytes = 32 << 20

type SourceStatus struct {
	Source      string    `json:"source"`
	Rules       int       `json:"rules"`
	Unsupported int       `json:"unsupported"`
	LastAttempt time.Time `json:"last_attempt"`
	LastSuccess time.Time `json:"last_success"`
	LastError   string    `json:"last_error,omitempty"`
	FromCache   bool      `json:"from_cache"`
}

type Snapshot struct {
	Rules          int            `json:"rules"`
	Unsupported    int            `json:"unsupported"`
	BlockedQueries uint64         `json:"blocked_queries"`
	Sources        []SourceStatus `json:"sources"`
}

type source struct {
	name, path, cachePath string
	remote                bool
}

// Manager rebuilds the complete index off the query path. Readers only need
// one atomic pointer load and never observe a partially updated filter.
type Manager struct {
	sources []source
	client  *http.Client
	active  atomic.Pointer[Filter]
	blocked atomic.Uint64

	refreshMu sync.Mutex
	statusMu  sync.RWMutex
	status    []SourceStatus
	rules     int
	ignored   int
}

func NewManager(dataPath string, entries []string, client *http.Client) (*Manager, error) {
	if client == nil {
		client = &http.Client{}
	}
	cloned := *client
	if cloned.Timeout == 0 || cloned.Timeout > 20*time.Second {
		cloned.Timeout = 20 * time.Second
	}
	previousRedirect := cloned.CheckRedirect
	cloned.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return errors.New("subscription redirect requires HTTPS")
		}
		if previousRedirect != nil {
			return previousRedirect(req, via)
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}
	m := &Manager{client: &cloned}
	m.active.Store(New())
	for _, entry := range entries {
		if entry == "" {
			return nil, errors.New("empty filter list entry")
		}
		s := source{name: entry}
		if strings.Contains(entry, "://") {
			u, err := url.Parse(entry)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
				return nil, fmt.Errorf("subscription must be an HTTPS URL without embedded credentials: %q", entry)
			}
			u.Fragment = ""
			s.remote = true
			s.path = u.String()
			u.RawQuery = "" // Do not expose URL query tokens in the dashboard.
			s.name = u.String()
			digest := sha256.Sum256([]byte(s.path))
			s.cachePath = filepath.Join(dataPath, "filter_subscriptions", hex.EncodeToString(digest[:])+".txt")
		} else {
			s.path = entry
			if !filepath.IsAbs(entry) {
				s.path = filepath.Join(dataPath, entry)
			}
		}
		m.sources = append(m.sources, s)
		m.status = append(m.status, SourceStatus{Source: s.name})
	}
	return m, nil
}

func (m *Manager) Blocked(name string) bool {
	if m.active.Load().Blocked(name) {
		m.blocked.Add(1)
		return true
	}
	return false
}

func (m *Manager) Snapshot() Snapshot {
	m.statusMu.RLock()
	snapshot := Snapshot{Rules: m.rules, Unsupported: m.ignored, Sources: append([]SourceStatus(nil), m.status...)}
	m.statusMu.RUnlock()
	snapshot.BlockedQueries = m.blocked.Load()
	return snapshot
}

// LoadInitial uses local files and last known good subscriptions only, so
// startup and DNS service availability never depend on a remote server.
func (m *Manager) LoadInitial() error {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	next := New()
	statuses := make([]SourceStatus, len(m.sources))
	var errs []error
	for i, source := range m.sources {
		statuses[i].Source = source.name
		path := source.path
		if source.remote {
			path = source.cachePath
		}
		data, err := os.ReadFile(path)
		if source.remote && errors.Is(err, os.ErrNotExist) {
			continue // First download runs asynchronously after DNS starts.
		}
		if err == nil {
			_, err = Validate(bytes.NewReader(data), source.remote)
		}
		if err == nil {
			statuses[i].Rules, statuses[i].Unsupported, err = loadData(next, data, source.remote)
		}
		if err != nil {
			statuses[i].LastError = err.Error()
			errs = append(errs, fmt.Errorf("filter %s: %w", source.name, err))
		} else if info, statErr := os.Stat(path); statErr == nil {
			statuses[i].LastSuccess = time.Now()
			if source.remote {
				statuses[i].LastSuccess = info.ModTime()
			}
			statuses[i].FromCache = source.remote
		}
	}
	m.publish(next, statuses)
	return errors.Join(errs...)
}

// Refresh downloads each HTTPS source and atomically publishes a new filter.
// Failed downloads use the previously verified on-disk copy. Without a valid
// copy, an existing active filter is retained rather than dropping rules.
func (m *Manager) Refresh(ctx context.Context) error {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	m.statusMu.RLock()
	statuses := append([]SourceStatus(nil), m.status...)
	m.statusMu.RUnlock()
	previous := append([]SourceStatus(nil), statuses...)
	next := New()
	var errs []error
	missing := false
	for i, source := range m.sources {
		status := statuses[i]
		status.Source = source.name
		status.LastAttempt = time.Now()
		status.LastError = ""
		status.FromCache = false
		if !source.remote {
			data, err := os.ReadFile(source.path)
			if err == nil {
				status.Rules, status.Unsupported, err = loadData(next, data, false)
			}
			if err != nil {
				status.LastError = err.Error()
				errs = append(errs, fmt.Errorf("filter %s: %w", source.name, err))
				missing = true
			} else {
				status.LastSuccess = status.LastAttempt
			}
		} else {
			data, err := m.fetch(ctx, source)
			if err == nil {
				// Validate the complete download before touching the active index
				// or the last-known-good copy on disk.
				_, err = Validate(bytes.NewReader(data), true)
			}
			if err == nil {
				status.Rules, status.Unsupported, err = loadData(next, data, true)
			}
			if err == nil {
				if cacheErr := saveCache(source.cachePath, data); cacheErr != nil {
					status.LastError = "cache write failed: " + cacheErr.Error()
					errs = append(errs, fmt.Errorf("filter %s: %w", source.name, cacheErr))
				}
				status.LastSuccess = status.LastAttempt
			} else {
				status.LastError = sanitizeError(err, source)
				errs = append(errs, fmt.Errorf("filter %s: %s", source.name, status.LastError))
				cached, cacheErr := os.ReadFile(source.cachePath)
				if cacheErr == nil {
					status.Rules, status.Unsupported, cacheErr = loadData(next, cached, true)
				}
				if cacheErr != nil {
					missing = true
				} else {
					status.FromCache = true
				}
				if cacheErr == nil && status.LastSuccess.IsZero() {
					if info, statErr := os.Stat(source.cachePath); statErr == nil {
						status.LastSuccess = info.ModTime()
					}
				}
			}
		}
		statuses[i] = status
	}
	if !missing {
		m.publish(next, statuses)
	} else {
		for i := range statuses {
			statuses[i].Rules = previous[i].Rules
			statuses[i].Unsupported = previous[i].Unsupported
			statuses[i].LastSuccess = previous[i].LastSuccess
		}
		m.publishStatus(statuses)
	}
	return errors.Join(errs...)
}

// Run performs an immediate refresh, then repeats on the configured interval
// until ctx is cancelled. A non-positive interval disables periodic updates.
func (m *Manager) Run(ctx context.Context, interval time.Duration, report func(error)) {
	refresh := func() {
		if err := m.Refresh(ctx); report != nil {
			report(err)
		}
	}
	refresh()
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}

func (m *Manager) fetch(ctx context.Context, s source) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxSubscriptionBytes {
		return nil, errors.New("subscription exceeds 32 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSubscriptionBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSubscriptionBytes {
		return nil, errors.New("subscription exceeds 32 MiB")
	}
	return data, nil
}

func loadData(f *Filter, data []byte, requireRules bool) (int, int, error) {
	stats, err := f.Load(bytes.NewReader(data))
	if err != nil {
		return 0, 0, err
	}
	if requireRules && stats.Rules == 0 {
		return 0, 0, errors.New("subscription has no supported rules")
	}
	return stats.Rules, stats.Unsupported, nil
}

func saveCache(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func sanitizeError(err error, s source) string {
	return strings.ReplaceAll(err.Error(), s.path, s.name)
}

func (m *Manager) publish(next *Filter, statuses []SourceStatus) {
	m.active.Store(next)
	m.statusMu.Lock()
	m.status = statuses
	m.rules, m.ignored = 0, 0
	for _, status := range statuses {
		m.rules += status.Rules
		m.ignored += status.Unsupported
	}
	m.statusMu.Unlock()
}

func (m *Manager) publishStatus(statuses []SourceStatus) {
	m.statusMu.Lock()
	m.status = statuses
	m.statusMu.Unlock()
}
