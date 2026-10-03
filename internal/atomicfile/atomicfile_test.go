package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWrite(t *testing.T) {
	tests := []struct {
		name     string
		existing string
		data     string
		mode     os.FileMode
	}{
		{"new file", "", "new", 0o600},
		{"replaces content", "old", "new", 0o600},
		{"sets the requested mode", "old", "new", 0o640},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "file")
			if tt.existing != "" {
				if err := os.WriteFile(path, []byte(tt.existing), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			if err := Write(path, []byte(tt.data), tt.mode); err != nil {
				t.Fatal(err)
			}

			got, err := os.ReadFile(path)
			if err != nil || string(got) != tt.data {
				t.Fatalf("content = %q, %v", got, err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != tt.mode {
				t.Errorf("mode = %v, want %v", info.Mode().Perm(), tt.mode)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Errorf("directory holds %d entries after Write, want 1 (%v)", len(entries), err)
			}
		})
	}
}

func TestWriteMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "file")

	if err := Write(path, []byte("x"), 0o600); err == nil {
		t.Fatal("Write into a missing directory succeeded")
	}
}
