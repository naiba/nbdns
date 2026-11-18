package utils

import (
	"os"
	"path/filepath"
)

// TemporaryPathFor is the single fixed temporary file next to path. A fixed
// name means an interrupted write (power cut, kill) leaves at most one file,
// which the next write truncates and reuses instead of accumulating garbage.
// Callers must not write the same path concurrently.
func TemporaryPathFor(path string) string {
	return filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
}

// ReplaceFileAtomically writes TemporaryPathFor(path), flushes it and renames
// it over path, so readers see either the old or the new file, never a
// half-written one. Anything left at the temporary path is overwritten or
// cleared rather than making every later write fail.
func ReplaceFileAtomically(path string, mode os.FileMode, write func(*os.File) error) (err error) {
	tempPath := TemporaryPathFor(path)
	// O_TRUNC: a longer leftover from an interrupted write must not leak through.
	f, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		// The name is ours alone: clear whatever occupies it (e.g. a directory) once.
		if removeErr := os.RemoveAll(tempPath); removeErr != nil {
			return err
		}
		if f, err = os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode); err != nil {
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
	if err = f.Chmod(mode); err != nil {
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
