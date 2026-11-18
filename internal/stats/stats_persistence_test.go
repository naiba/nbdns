package stats

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
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
	err := replaceFileAtomically(target, func(f *os.File) error {
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

	if err := replaceFileAtomically(target, func(f *os.File) error {
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
	orphan := filepath.Join(cacheDir, ".stats.json.tmp-123")
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
