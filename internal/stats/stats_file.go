package stats

import (
	"os"
	"path/filepath"
)

// replaceFileAtomically writes to a temporary file in the same directory and
// renames it over path. stats.json used to be truncated and rewritten in place,
// so a power cut on a router left a half-written file and lost all statistics.
func replaceFileAtomically(path string, write func(*os.File) error) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tempPath)
		}
	}()
	if err = write(f); err != nil {
		return err
	}
	if err = f.Chmod(0644); err != nil {
		return err
	}
	// Flush data before the rename so the new name never points at empty blocks.
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
