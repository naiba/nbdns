package stats

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/naiba/nbdns/pkg/utils"
)

func TestSaveWritesCompactJSONWithoutLeftovers(t *testing.T) {
	s := NewStats()
	for range 3 {
		s.RecordClientQuery("192.0.2.1", "resolved.example.")
	}
	dataPath := t.TempDir()
	for range 2 { // The second save replaces the first in place.
		if err := s.Save(dataPath); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(dataPath, "cache", "stats.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Indented JSON rewritten every few minutes was ~45% whitespace on SD cards.
	if bytes.Contains(data, []byte("\n ")) {
		t.Error("stats.json must be compact JSON")
	}
	entries, err := os.ReadDir(filepath.Join(dataPath, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "stats.json" {
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("cache directory = %v, want only stats.json", names)
	}
	loaded := NewStats()
	if err := loaded.Load(dataPath); err != nil {
		t.Fatal(err)
	}
	if got := loaded.GetSnapshot().TopDomains; len(got) != 1 || got[0].Count != 3 {
		t.Errorf("compact stats did not round-trip: %+v", got)
	}
}

func TestReplaceFileAtomicallyKeepsOldFileOnFailedWrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "stats.json")
	if err := os.WriteFile(target, []byte(`{"total_queries":42}`), 0644); err != nil {
		t.Fatal(err)
	}
	// Simulates power loss or a full card in the middle of writing.
	failure := errors.New("disk full")
	err := utils.ReplaceFileAtomically(target, 0644, func(f *os.File) error {
		if _, err := f.Write([]byte(`{"total_qu`)); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("err = %v, want %v", err, failure)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"total_queries":42}` {
		t.Errorf("previous stats damaged by a failed write: %q", data)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temporary file left behind: %d entries", len(entries))
	}

	if err := utils.ReplaceFileAtomically(target, 0644, func(f *os.File) error {
		_, err := f.Write([]byte(`{"total_queries":43}`))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != `{"total_queries":43}` {
		t.Errorf("replacement content = %q", data)
	}
	if info.Mode().Perm() != 0644 {
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}
}

func TestLoadRemovesTemporaryFilesLeftByPowerLoss(t *testing.T) {
	dataPath := t.TempDir()
	cacheDir := filepath.Join(dataPath, "cache")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(cacheDir, ".stats.json.tmp")
	if err := os.WriteFile(orphan, []byte(`{"total_qu`), 0600); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(cacheDir, "000001.vlog") // Badger shares this directory.
	if err := os.WriteFile(unrelated, []byte("badger"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := NewStats().Load(dataPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Error("interrupted temporary stats file must be removed")
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Errorf("unrelated cache file removed: %v", err)
	}
}

func cacheDirNames(t *testing.T, dataPath string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataPath, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// The temporary file has one fixed name and is overwritten, so repeated or
// interrupted saves can never accumulate temporary files on the card.
func TestSaveReusesSingleFixedTemporaryFile(t *testing.T) {
	dataPath := t.TempDir()
	cacheDir := filepath.Join(dataPath, "cache")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Left by a power cut, and longer than the next save: must be truncated.
	stale := bytes.Repeat([]byte("x"), 1<<16)
	if err := os.WriteFile(filepath.Join(cacheDir, ".stats.json.tmp"), stale, 0644); err != nil {
		t.Fatal(err)
	}
	s := NewStats()
	for i := 0; i < 20; i++ {
		s.RecordClientQuery("192.0.2.1", "d"+strconv.Itoa(i)+".example.")
		if err := s.Save(dataPath); err != nil {
			t.Fatal(err)
		}
		if names := cacheDirNames(t, dataPath); len(names) != 1 || names[0] != "stats.json" {
			t.Fatalf("save %d left %v", i, names)
		}
	}
	data, err := os.ReadFile(filepath.Join(cacheDir, "stats.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) || bytes.Contains(data, []byte("xxx")) {
		t.Fatalf("stale temporary content leaked into stats.json: %.80q", data)
	}
}

func TestConcurrentSavesShareTheTemporaryFileSafely(t *testing.T) {
	dataPath := t.TempDir()
	s := NewStats()
	s.RecordClientQuery("192.0.2.1", "resolved.example.")
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 { // Periodic save racing the shutdown save.
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.Save(dataPath)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(dataPath, "cache", "stats.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatalf("concurrent saves corrupted stats.json: %.80q", data)
	}
	if names := cacheDirNames(t, dataPath); len(names) != 1 {
		t.Errorf("concurrent saves left %v", names)
	}
}

// A save stuck in the kernel (e.g. a stalled SD card) must neither block later
// saves forever nor stop statistics from being saved once the card recovers.
func TestStuckSaveDoesNotLockSavingForever(t *testing.T) {
	previousTimeout := saveWaitTimeout
	saveWaitTimeout = 50 * time.Millisecond
	t.Cleanup(func() { saveWaitTimeout = previousTimeout })

	dataPath := t.TempDir()
	s := NewStats()
	stuck := make(chan struct{})
	entered := make(chan struct{})
	s.beforeStatsWrite = func() {
		close(entered)
		<-stuck
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- s.Save(dataPath) }()
	<-entered

	started := time.Now()
	if err := s.Save(dataPath); !errors.Is(err, ErrSaveInProgress) {
		t.Fatalf("second save while the first is stuck: err = %v, want ErrSaveInProgress", err)
	}
	if waited := time.Since(started); waited > time.Second {
		t.Errorf("second save waited %v instead of giving up", waited)
	}

	// The card recovers: the stuck save finishes and saving works again.
	s.beforeStatsWrite = nil
	close(stuck)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	s.RecordClientQuery("192.0.2.1", "after-recovery.example.")
	if err := s.Save(dataPath); err != nil {
		t.Fatalf("save after recovery: %v", err)
	}
	loaded := NewStats()
	if err := loaded.Load(dataPath); err != nil {
		t.Fatal(err)
	}
	if got := loaded.GetSnapshot().TopDomains; len(got) != 1 || got[0].Key != "after-recovery.example." {
		t.Errorf("stats after recovery = %+v", got)
	}
}

func TestSaveRecoversWhenTemporaryPathIsOccupied(t *testing.T) {
	dataPath := t.TempDir()
	occupied := filepath.Join(dataPath, "cache", ".stats.json.tmp")
	if err := os.MkdirAll(filepath.Join(occupied, "junk"), 0755); err != nil {
		t.Fatal(err)
	}
	s := NewStats()
	s.RecordClientQuery("192.0.2.1", "resolved.example.")
	for i := 0; i < 2; i++ {
		if err := s.Save(dataPath); err != nil {
			t.Fatalf("save %d with occupied temporary path: %v", i, err)
		}
	}
	if names := cacheDirNames(t, dataPath); len(names) != 1 || names[0] != "stats.json" {
		t.Errorf("cache directory = %v", names)
	}
}
