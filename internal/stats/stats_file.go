package stats

import (
	"os"
	"path/filepath"
)

// temporaryPathFor is the single fixed temporary file next to path. A fixed
// name means saves overwrite it instead of accumulating files on flash
// storage, even after interrupted saves; callers must serialize saves.
func temporaryPathFor(path string) string {
	return filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
}

// replaceFileAtomically writes the temporary file and renames it over path.
// stats.json used to be truncated and rewritten in place, so a power cut on a
// router left a half-written file and lost all statistics.
func replaceFileAtomically(path string, write func(*os.File) error) (err error) {
	tempPath := temporaryPathFor(path)
	// O_TRUNC: a longer leftover from an interrupted save must not leak through.
	f, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		// The name is ours alone: clear whatever occupies it (e.g. a directory)
		// once, so a stray entry cannot make every later save fail.
		if removeErr := os.RemoveAll(tempPath); removeErr != nil {
			return err
		}
		if f, err = os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644); err != nil {
			return err
		}
	}
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
