package filter

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func directoryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

// Interrupted downloads must not accumulate temporary files or poison later
// saves: every save reuses one fixed, truncated temporary file per list.
func TestSaveCacheReusesOneTemporaryFileAfterInterruptions(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "list.txt")
	// A longer leftover from a power cut, plus one from the old random naming.
	if err := os.WriteFile(filepath.Join(dir, ".list.txt.tmp"), bytes.Repeat([]byte("x"), 1<<16), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := saveCache(target, []byte("||ads.example^\n")); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	if names := directoryNames(t, dir); len(names) != 1 || names[0] != "list.txt" {
		t.Errorf("directory = %v, want only list.txt", names)
	}
	if data, _ := os.ReadFile(target); string(data) != "||ads.example^\n" {
		t.Errorf("content = %.40q", data)
	}
}

func TestSaveCacheRecoversWhenTemporaryPathIsOccupied(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".list.txt.tmp", "junk"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveCache(filepath.Join(dir, "list.txt"), []byte("||ads.example^\n")); err != nil {
		t.Fatalf("occupied temporary path blocked saving: %v", err)
	}
	if names := directoryNames(t, dir); len(names) != 1 {
		t.Errorf("directory = %v", names)
	}
}

func TestLoadInitialRemovesInterruptedDownloads(t *testing.T) {
	dataPath := t.TempDir()
	m, err := NewManager(dataPath, []string{"https://example.com/list.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Dir(m.sources[0].cachePath)
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, leftover := range []string{".download-123456", ".download-abcdef", "." + filepath.Base(m.sources[0].cachePath) + ".tmp"} {
		if err := os.WriteFile(filepath.Join(cacheDir, leftover), []byte("partial"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(m.sources[0].cachePath, []byte("||ads.example^\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.LoadInitial(); err != nil {
		t.Fatal(err)
	}
	if names := directoryNames(t, cacheDir); len(names) != 1 || names[0] != filepath.Base(m.sources[0].cachePath) {
		t.Errorf("leftovers not removed: %v", names)
	}
	if !m.Blocked("ads.example.") {
		t.Error("cached list must still load")
	}
}
