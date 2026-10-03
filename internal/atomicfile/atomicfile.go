// Package atomicfile replaces a file so a reader or a crash sees the old or the new content.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write puts data into path with the given mode: it writes a temp file in the same directory,
// syncs it, renames it over path and syncs the directory so the rename survives power loss. The
// directory of path must exist.
func Write(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".atomic-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file for %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("setting mode of %s: %w", tmp.Name(), err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("opening %s: %w", dir, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", dir, err)
	}
	return nil
}
