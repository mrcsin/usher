// Package clients mirrors client files into the clients directory.
package clients

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/mrcsin/usher/internal/atomicfile"
)

const (
	clientFileMode = 0o600
	clientDirMode  = 0o755
)

// Path returns the slash-separated path of a user's client file on an interface, relative to the
// clients directory. suffix is the backend's file suffix, such as ".conf".
func Path(user, ifaceName, suffix string) string {
	return user + "/" + ifaceName + suffix
}

// Existing returns the current content of the files in dir that end in suffix and belong to an
// interface for which keep returns true, keyed by Path. It reads as much as it can and returns
// the errors of the rest joined.
func Existing(dir, suffix string, keep func(ifaceName string) bool) (map[string][]byte, error) {
	files := make(map[string][]byte)
	userDirs, err := os.ReadDir(dir)
	if err != nil {
		return files, err
	}
	var errs []error
	for _, d := range userDirs {
		if !d.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(dir, d.Name()))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, f := range entries {
			ifaceName, ok := strings.CutSuffix(f.Name(), suffix)
			if !ok || f.IsDir() || !keep(ifaceName) {
				continue
			}
			content, err := os.ReadFile(filepath.Join(dir, d.Name(), f.Name()))
			if err != nil {
				errs = append(errs, err)
				continue
			}
			files[Path(d.Name(), ifaceName, suffix)] = content
		}
	}
	return files, errors.Join(errs...)
}

// Mirror makes dir hold exactly the desired files. Keys of desired are slash-separated paths
// relative to dir, such as "phone/awg0.conf". Files are written atomically with mode 0600 and
// only when the content differs. Every other file and every empty directory is
// removed.
func Mirror(dir string, desired map[string][]byte) error {
	wanted := make(map[string]bool, len(desired))
	for rel := range desired {
		wanted[filepath.Join(dir, filepath.FromSlash(rel))] = true
	}

	if err := removeUnwanted(dir, wanted); err != nil {
		return err
	}
	for rel, content := range desired {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := writeIfChanged(path, content); err != nil {
			return err
		}
	}
	return nil
}

// removeUnwanted deletes every non-directory entry under dir that is not wanted, then every
// directory left empty. dir itself stays.
func removeUnwanted(dir string, wanted map[string]bool) error {
	var dirs []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir {
				dirs = append(dirs, path)
			}
			return nil
		}
		if wanted[path] {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("removing %s: %w", path, err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("scanning %s: %w", dir, err)
	}
	// WalkDir lists a parent before its children, so reverse order removes children first.
	for i := len(dirs) - 1; i >= 0; i-- {
		entries, err := os.ReadDir(dirs[i])
		if err != nil {
			return fmt.Errorf("reading %s: %w", dirs[i], err)
		}
		if len(entries) > 0 {
			continue
		}
		if err := os.Remove(dirs[i]); err != nil {
			return fmt.Errorf("removing %s: %w", dirs[i], err)
		}
	}
	return nil
}

func writeIfChanged(path string, content []byte) error {
	current, err := os.ReadFile(path)
	if err == nil && bytes.Equal(current, content) {
		return nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), clientDirMode); err != nil {
		return fmt.Errorf("creating directory for %s: %w", path, err)
	}
	return atomicfile.Write(path, content, clientFileMode)
}
