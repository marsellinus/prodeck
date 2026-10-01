package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to path via a temporary file in the same
// directory followed by a rename, so a crash can never leave a half-written
// configuration file behind.
func WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("store: create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("store: temp file for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() {
		// Best effort: if the rename succeeded the file is gone already.
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: write %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: sync %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: close %s: %w", path, err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("store: chmod %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("store: rename %s: %w", path, err)
	}
	return nil
}

// ReadFileOr reads a file, returning def when it does not exist. Any other
// error is reported: silently ignoring a permission problem would hide a real
// misconfiguration.
func ReadFileOr(path string, def []byte) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		return b, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return def, nil
	}
	return nil, fmt.Errorf("store: read %s: %w", path, err)
}
