package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    map[string][]string
		errHas  []string
	}{
		{
			name: "example",
			content: "user1-phone: [awg0]\n" +
				"user1-laptop: [awg0, awg1]\n" +
				"guest: []\n",
			want: map[string][]string{
				"user1-phone":  {"awg0"},
				"user1-laptop": {"awg0", "awg1"},
				"guest":        {},
			},
		},
		{
			name:    "longest user name",
			content: strings.Repeat("a", 32) + ": [awg0]\n",
			want:    map[string][]string{strings.Repeat("a", 32): {"awg0"}},
		},
		{
			name:    "longest interface name",
			content: "alice: [" + strings.Repeat("a", 15) + "]\n",
			want:    map[string][]string{"alice": {strings.Repeat("a", 15)}},
		},
		{
			name:    "block list",
			content: "alice:\n  - awg0\n  - awg1\n",
			want:    map[string][]string{"alice": {"awg0", "awg1"}},
		},
		{
			name:    "empty list for one user next to a real pair",
			content: "alice: [awg0]\nbob: []\n",
			want:    map[string][]string{"alice": {"awg0"}, "bob": {}},
		},
		{
			name:    "repeated user key",
			content: "alice: [awg0]\nalice: [awg1]\n",
			errHas:  []string{"usher.yml", "line 2", "alice"},
		},
		{
			name:    "bad user name uppercase",
			content: "Alice: [awg0]\n",
			errHas:  []string{"usher.yml", "line 1", "Alice"},
		},
		{
			name:    "bad user name leading dash",
			content: "ok: [awg0]\n-bob: [awg0]\n",
			errHas:  []string{"line 2", "-bob"},
		},
		{
			name:    "user name too long",
			content: strings.Repeat("a", 33) + ": [awg0]\n",
			errHas:  []string{"line 1"},
		},
		{
			name:    "bad interface name",
			content: "alice: [awg0]\nbob: [\"bad name\"]\n",
			errHas:  []string{"line 2", "bob", "bad name"},
		},
		{
			name:    "interface name too long",
			content: "alice: [abcdefghijklmnop]\n",
			errHas:  []string{"line 1", "alice"},
		},
		{
			name:    "repeated interface",
			content: "alice: [awg0, awg0]\n",
			errHas:  []string{"line 1", "alice", "awg0"},
		},
		{
			name:    "scalar value",
			content: "alice: awg0\n",
			errHas:  []string{"line 1", "alice"},
		},
		{
			name:    "null value",
			content: "alice:\n",
			errHas:  []string{"line 1", "alice"},
		},
		{
			name:    "nested list item",
			content: "alice: [[awg0]]\n",
			errHas:  []string{"line 1", "alice"},
		},
		{
			name:    "top level list",
			content: "- alice\n",
			errHas:  []string{"usher.yml", "mapping"},
		},
		{
			name:    "syntax error names the line",
			content: "alice: [awg0]\nbob: x: y\n",
			errHas:  []string{"usher.yml", "line 2"},
		},
		{
			name:    "empty file",
			content: "",
			errHas:  []string{"usher.yml", "no user"},
		},
		{
			name:    "comments only",
			content: "# nobody yet\n",
			errHas:  []string{"usher.yml", "no user"},
		},
		{
			name:    "empty mapping",
			content: "{}\n",
			errHas:  []string{"usher.yml", "no user"},
		},
		{
			name:    "all users switched off",
			content: "alice: []\nbob: []\n",
			errHas:  []string{"usher.yml", "no user"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "usher.yml")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := Load(path)
			if len(tt.errHas) > 0 {
				if err == nil {
					t.Fatalf("Load = %v, want error", got)
				}
				for _, s := range tt.errHas {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q lacks %q", err, s)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Load = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usher.yml")
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load of a missing file succeeded")
	}
	if !strings.Contains(err.Error(), "usher.yml") {
		t.Errorf("error %q does not name the file", err)
	}
}
