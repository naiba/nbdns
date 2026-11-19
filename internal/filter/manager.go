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

	"github.com/naiba/nbdns/pkg/utils"
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

// sourceState is what the active index was built from, per source. It lets a
// refresh skip unchanged lists: no download (ETag), no rewrite, no rebuild.
type sourceState struct {
	etag   string
	digest [sha256.Size]byte
	known  bool
}

// listParser parses one list into f, or only validates it when f is nil.
type listParser func(r io.Reader, f *Filter) (Stats, error)

// Manager rebuilds the complete index off the query path. Readers only need
// one atomic pointer load and never observe a partially updated filter.
type Manager struct {
	sources []source
	states  []sourceState // Guarded by refreshMu
	parse   listParser
	client  *http.Client
	active  atomic.Pointer[Filter]
	blocked atomic.Uint64

	refreshMu sync.Mutex
	statusMu  sync.RWMutex
	status    []SourceStatus
	rules     int
	ignored   int
}

// NewManager manages AdGuard/hosts blocking lists ("filter_lists").
func NewManager(dataPath string, entries []string, client *http.Client) (*Manager, error) {
	return newManager(dataPath, entries, client, "filter_subscriptions", parseFilterRules)
}

// NewDomainListManager manages domain lists that route names to an upstream
// ("match_lists"). Its cache directory is separate from filter subscriptions:
// both managers refresh concurrently and each cache file has a fixed
// temporary file, which must never be shared.
func NewDomainListManager(dataPath string, entries []string, client *http.Client) (*Manager, error) {
	return newManager(dataPath, entries, client, "match_lists", parseDomainList)
}

// Contains reports whether name is in the active domain list. Unlike Blocked
// it does not count as a blocked query.
func (m *Manager) Contains(name string) bool {
	return m.active.Load().Blocked(name)
}

func newManager(dataPath string, entries []string, client *http.Client, cacheDir string, parse listParser) (*Manager, error) {
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
	m := &Manager{client: &cloned, parse: parse}
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
			s.cachePath = filepath.Join(dataPath, cacheDir, hex.EncodeToString(digest[:])+".txt")
		} else {
			s.path = entry
			if !filepath.IsAbs(entry) {
				s.path = filepath.Join(dataPath, entry)
			}
		}
		m.sources = append(m.sources, s)
		m.status = append(m.status, SourceStatus{Source: s.name})
	}
	m.states = make([]sourceState, len(m.sources))
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
	m.removeInterruptedDownloads()
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
			err = m.validate(data, source.remote)
		}
		if err == nil {
			statuses[i].Rules, statuses[i].Unsupported, err = m.loadData(next, data, source.remote)
		}
		if err == nil {
			m.states[i] = sourceState{digest: sha256.Sum256(data), known: true}
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
// Unchanged sources cost little: a 304 skips the download, identical content
// is not rewritten to disk, and if nothing changed the index is not rebuilt.
func (m *Manager) Refresh(ctx context.Context) error {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	m.statusMu.RLock()
	statuses := append([]SourceStatus(nil), m.status...)
	m.statusMu.RUnlock()
	previous := append([]SourceStatus(nil), statuses...)
	// Data for the rebuild per source; nil means "load the verified cache file".
	contents := make([][]byte, len(m.sources))
	nextStates := append([]sourceState(nil), m.states...)
	var errs []error
	missing, changed := false, false
	for i, source := range m.sources {
		status := statuses[i]
		status.Source = source.name
		status.LastAttempt = time.Now()
		status.LastError = ""
		status.FromCache = false
		if !source.remote {
			data, err := os.ReadFile(source.path)
			if err == nil {
				err = m.validate(data, false)
			}
			if err != nil {
				status.LastError = err.Error()
				errs = append(errs, fmt.Errorf("filter %s: %w", source.name, err))
				missing = true
			} else {
				status.LastSuccess = status.LastAttempt
				contents[i] = data
				digest := sha256.Sum256(data)
				changed = changed || !m.states[i].known || digest != m.states[i].digest
				nextStates[i] = sourceState{digest: digest, known: true}
			}
			statuses[i] = status
			continue
		}
		state := m.states[i]
		_, statErr := os.Stat(source.cachePath)
		cacheMissing := statErr != nil
		if cacheMissing {
			// A 304 cannot restore a deleted cache: ask for the full list.
			state.etag = ""
		}
		data, notModified, etag, err := m.fetch(ctx, source, state)
		if err == nil && notModified {
			status.LastSuccess = status.LastAttempt // Verified copy is current.
		} else if err == nil {
			// Validate the complete download before touching the active index
			// or the last-known-good copy on disk.
			err = m.validate(data, true)
			if err == nil {
				digest := sha256.Sum256(data)
				nextStates[i] = sourceState{etag: etag, digest: digest, known: true}
				status.LastSuccess = status.LastAttempt
				contentChanged := !m.states[i].known || digest != m.states[i].digest
				if contentChanged {
					changed = true
					contents[i] = data
				}
				if contentChanged || cacheMissing {
					if cacheErr := saveCache(source.cachePath, data); cacheErr != nil {
						status.LastError = "cache write failed: " + cacheErr.Error()
						errs = append(errs, fmt.Errorf("filter %s: %w", source.name, cacheErr))
						nextStates[i].etag = "" // Never trust a 304 for a copy not on disk.
					}
				}
			}
		}
		if err != nil {
			status.LastError = sanitizeError(err, source)
			errs = append(errs, fmt.Errorf("filter %s: %s", source.name, status.LastError))
			if m.states[i].known {
				status.FromCache = true // The active index already holds the verified copy.
			} else if info, statErr := os.Stat(source.cachePath); statErr == nil {
				changed = true // Verified cache not loaded yet (e.g. LoadInitial failed).
				status.FromCache = true
				if status.LastSuccess.IsZero() {
					status.LastSuccess = info.ModTime()
				}
			} else {
				missing = true
			}
		}
		statuses[i] = status
	}
	if !changed && !missing {
		// Nothing changed: keep the active index and its rule counts.
		m.states = nextStates
		m.publishStatus(statuses)
		return errors.Join(errs...)
	}
	next := New()
	for i, source := range m.sources {
		data := contents[i]
		if data == nil {
			cached, err := os.ReadFile(source.cachePath)
			if err != nil {
				missing = true
				continue
			}
			data = cached
			if !nextStates[i].known {
				nextStates[i] = sourceState{digest: sha256.Sum256(data), known: true}
			}
		}
		var err error
		statuses[i].Rules, statuses[i].Unsupported, err = m.loadData(next, data, source.remote)
		if err != nil {
			missing = true
		}
	}
	if !missing {
		m.states = nextStates
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

func (m *Manager) validate(data []byte, requireRules bool) error {
	stats, err := m.parse(bytes.NewReader(data), nil)
	if err == nil && requireRules && stats.Rules == 0 {
		return errors.New("subscription has no supported rules")
	}
	return err
}

func (m *Manager) loadData(f *Filter, data []byte, requireRules bool) (int, int, error) {
	stats, err := m.parse(bytes.NewReader(data), f)
	if err != nil {
		return 0, 0, err
	}
	if requireRules && stats.Rules == 0 {
		return 0, 0, errors.New("subscription has no supported rules")
	}
	return stats.Rules, stats.Unsupported, nil
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

// fetch downloads a source. With a known ETag it asks the server to skip an
// unchanged list (HTTP 304); notModified then means the verified copy is current.
func (m *Manager) fetch(ctx context.Context, s source, state sourceState) (data []byte, notModified bool, etag string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.path, nil)
	if err != nil {
		return nil, false, "", err
	}
	if state.known && state.etag != "" {
		req.Header.Set("If-None-Match", state.etag)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, false, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified && state.known && state.etag != "" {
		return nil, true, state.etag, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxSubscriptionBytes {
		return nil, false, "", errors.New("subscription exceeds 32 MiB")
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, maxSubscriptionBytes+1))
	if err != nil {
		return nil, false, "", err
	}
	if len(data) > maxSubscriptionBytes {
		return nil, false, "", errors.New("subscription exceeds 32 MiB")
	}
	return data, false, resp.Header.Get("ETag"), nil
}

func saveCache(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	// Fixed temporary name: random ".download-*" names accumulated one file per
	// interrupted download, since the deferred cleanup never ran after a power cut.
	return utils.ReplaceFileAtomically(path, 0600, func(f *os.File) error {
		_, err := f.Write(data)
		return err
	})
}

// removeInterruptedDownloads clears temporaries of downloads cut short by power
// loss or a kill, including the random names used before the fixed one.
func (m *Manager) removeInterruptedDownloads() {
	dirs := make(map[string]bool)
	for _, source := range m.sources {
		if source.remote {
			dirs[filepath.Dir(source.cachePath)] = true
			_ = os.RemoveAll(utils.TemporaryPathFor(source.cachePath))
		}
	}
	for dir := range dirs {
		if legacy, err := filepath.Glob(filepath.Join(dir, ".download-*")); err == nil {
			for _, leftover := range legacy {
				_ = os.RemoveAll(leftover)
			}
		}
	}
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
