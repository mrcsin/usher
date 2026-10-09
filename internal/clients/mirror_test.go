package clients

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func listFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	got := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		got[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestMirror(t *testing.T) {
	tests := []struct {
		name     string
		existing map[string]string
		desired  map[string][]byte
		want     map[string]string
	}{
		{
			name:    "writes desired files",
			desired: map[string][]byte{"phone/awg0.conf": []byte("a"), "laptop/awg0.conf": []byte("b")},
			want:    map[string]string{"phone/awg0.conf": "a", "laptop/awg0.conf": "b"},
		},
		{
			name:     "replaces changed content",
			existing: map[string]string{"phone/awg0.conf": "old"},
			desired:  map[string][]byte{"phone/awg0.conf": []byte("new")},
			want:     map[string]string{"phone/awg0.conf": "new"},
		},
		{
			name:     "removes a file that is not desired",
			existing: map[string]string{"phone/awg0.conf": "a", "phone/awg1.conf": "b"},
			desired:  map[string][]byte{"phone/awg0.conf": []byte("a")},
			want:     map[string]string{"phone/awg0.conf": "a"},
		},
		{
			name:     "removes a stray file at the top level",
			existing: map[string]string{"notes.txt": "x", "phone/awg0.conf": "a"},
			desired:  map[string][]byte{"phone/awg0.conf": []byte("a")},
			want:     map[string]string{"phone/awg0.conf": "a"},
		},
		{
			name:     "empty desired set empties the directory",
			existing: map[string]string{"phone/awg0.conf": "a"},
			desired:  map[string][]byte{},
			want:     map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for rel, content := range tt.existing {
				writeFile(t, filepath.Join(dir, filepath.FromSlash(rel)), content, 0o600)
			}
			if err := Mirror(dir, tt.desired); err != nil {
				t.Fatalf("Mirror: %v", err)
			}
			got := listFiles(t, dir)
			if len(got) != len(tt.want) {
				t.Fatalf("files = %v, want %v", got, tt.want)
			}
			for rel, content := range tt.want {
				if got[rel] != content {
					t.Errorf("%s = %q, want %q", rel, got[rel], content)
				}
				info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0o600 {
					t.Errorf("%s mode = %v, want 0600", rel, info.Mode().Perm())
				}
			}
		})
	}
}

func TestMirrorRemovesEmptyUserDirectory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "laptop", "awg0.conf"), "b", 0o600)
	if err := os.MkdirAll(filepath.Join(dir, "ghost"), 0o755); err != nil {
		t.Fatal(err)
	}
	desired := map[string][]byte{"phone/awg0.conf": []byte("a")}
	if err := Mirror(dir, desired); err != nil {
		t.Fatalf("Mirror: %v", err)
	}
	for _, gone := range []string{"laptop", "ghost"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("directory %s still exists (err %v)", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "phone")); err != nil {
		t.Errorf("phone directory: %v", err)
	}
}

func TestMirrorKeepsMtimeOfUnchangedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "phone", "awg0.conf")
	writeFile(t, path, "a", 0o600)
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := Mirror(dir, map[string][]byte{"phone/awg0.conf": []byte("a")}); err != nil {
		t.Fatalf("Mirror: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(old) {
		t.Errorf("mtime = %v, want %v", info.ModTime(), old)
	}
}

func TestExisting(t *testing.T) {
	only := func(names ...string) func(string) bool {
		return func(name string) bool { return slices.Contains(names, name) }
	}
	all := func(string) bool { return true }
	none := func(string) bool { return false }
	tests := []struct {
		name     string
		existing map[string]string
		suffix   string
		keep     func(string) bool
		want     map[string][]byte
	}{
		{
			name: "returns only the files of the kept interfaces",
			existing: map[string]string{
				"phone/awg0.conf": "a", "phone/awg1.conf": "b", "laptop/awg1.conf": "c", "phone/notes.txt": "d",
			},
			suffix: ".conf",
			keep:   only("awg1"),
			want:   map[string][]byte{"phone/awg1.conf": []byte("b"), "laptop/awg1.conf": []byte("c")},
		},
		{
			name:     "keeping all returns every file of the suffix",
			existing: map[string]string{"phone/awg0.conf": "a", "phone/vless.txt": "b", "laptop/vless.txt": "c"},
			suffix:   ".txt",
			keep:     all,
			want:     map[string][]byte{"phone/vless.txt": []byte("b"), "laptop/vless.txt": []byte("c")},
		},
		{
			name:     "keeping none reads nothing",
			existing: map[string]string{"phone/awg0.conf": "a"},
			suffix:   ".conf",
			keep:     none,
			want:     map[string][]byte{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for rel, content := range tt.existing {
				writeFile(t, filepath.Join(dir, filepath.FromSlash(rel)), content, 0o600)
			}
			got, err := Existing(dir, tt.suffix, tt.keep)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Existing = %v, want %v", got, tt.want)
			}
		})
	}
	t.Run("missing directory is an error", func(t *testing.T) {
		if _, err := Existing(filepath.Join(t.TempDir(), "gone"), ".conf", all); err == nil {
			t.Fatal("want an error")
		}
	})
}

func TestPath(t *testing.T) {
	tests := []struct{ suffix, want string }{
		{".conf", "phone/awg0.conf"},
		{".txt", "phone/awg0.txt"},
	}
	for _, tt := range tests {
		if got := Path("phone", "awg0", tt.suffix); got != tt.want {
			t.Errorf("Path(%q) = %q, want %q", tt.suffix, got, tt.want)
		}
	}
}
