package state

import (
	"bytes"
	"encoding/base64"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func keyOf(b byte) Key {
	return Key(bytes.Repeat([]byte{b}, KeyLength))
}

func testKey(b byte) string {
	return keyOf(b).String()
}

func validState() *State {
	return &State{
		Version: 1,
		AWG: map[string]Entry{
			"awg0/phone": {Address: netip.MustParseAddr("10.8.1.2"), PrivateKey: keyOf(1), PresharedKey: keyOf(2)},
			"awg1/phone": {Address: netip.MustParseAddr("10.9.1.2"), PrivateKey: keyOf(3), PresharedKey: keyOf(4)},
		},
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "users.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Version != 1 || len(s.AWG) != 0 {
		t.Fatalf("want empty version 1 state, got %+v", s)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	want := validState()
	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip: got %+v, want %+v", got, want)
	}
}

// TestSaveFormat pins the users.json format: addresses are plain strings and keys are base64.
func TestSaveFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	s := &State{AWG: map[string]Entry{
		"awg0/phone": {Address: netip.MustParseAddr("10.8.1.2"), PrivateKey: keyOf(1), PresharedKey: keyOf(2)},
	}}
	if err := Save(path, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "version": 1,
  "awg": {
    "awg0/phone": {
      "address": "10.8.1.2",
      "private_key": "` + testKey(1) + `",
      "preshared_key": "` + testKey(2) + `"
    }
  }
}
`
	if string(got) != want {
		t.Fatalf("users.json = %q, want %q", got, want)
	}
}

func TestEntryDerived(t *testing.T) {
	e := Entry{Address: netip.MustParseAddr("10.8.1.2"), PrivateKey: keyOf(1), PresharedKey: keyOf(2)}
	if got := e.Route().String(); got != "10.8.1.2/32" {
		t.Errorf("Route = %q, want 10.8.1.2/32", got)
	}
	if got := e.PublicKey(); len(got) != KeyLength || bytes.Equal(got, e.PrivateKey[:]) {
		t.Errorf("PublicKey = %x, want %d bytes derived from the private key", got, KeyLength)
	}
}

func TestInterfaceEntries(t *testing.T) {
	s := validState()
	s.AWG[EntryName("awg0", "laptop")] = s.AWG["awg0/phone"]
	s.AWG["awg01/other"] = s.AWG["awg0/phone"]

	got := map[string]Entry{}
	for user, e := range s.InterfaceEntries("awg0") {
		got[user] = e
	}
	if want := []string{"laptop", "phone"}; !slices.Equal(slices.Sorted(maps.Keys(got)), want) {
		t.Fatalf("users = %v, want %v", slices.Sorted(maps.Keys(got)), want)
	}
	if got["phone"] != s.AWG["awg0/phone"] {
		t.Errorf("phone entry = %+v", got["phone"])
	}
}

func TestSaveMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")
	for range 2 {
		if err := Save(path, validState()); err != nil {
			t.Fatalf("Save: %v", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want only users.json in %s, got %d files", dir, len(entries))
	}
}

func TestLoadRejects(t *testing.T) {
	secret := "SECRETSECRETSECRET"
	encodedSecret := base64.StdEncoding.EncodeToString([]byte(secret))
	tests := []struct {
		name string
		json string
	}{
		{"bad version", `{"version":2,"awg":{}}`},
		{"missing version", `{"awg":{}}`},
		{"private key not base64", `{"version":1,"awg":{"awg0/a":{"address":"10.8.1.2","private_key":"` + secret + `","preshared_key":"` + testKey(2) + `"}}}`},
		{"private key wrong length", `{"version":1,"awg":{"awg0/a":{"address":"10.8.1.2","private_key":"` + encodedSecret + `","preshared_key":"` + testKey(2) + `"}}}`},
		{"preshared key wrong length", `{"version":1,"awg":{"awg0/a":{"address":"10.8.1.2","private_key":"` + testKey(1) + `","preshared_key":"` + secret + `"}}}`},
		{"address not IP", `{"version":1,"awg":{"awg0/a":{"address":"nope","private_key":"` + testKey(1) + `","preshared_key":"` + testKey(2) + `"}}}`},
		{"address IPv6", `{"version":1,"awg":{"awg0/a":{"address":"fd00::2","private_key":"` + testKey(1) + `","preshared_key":"` + testKey(2) + `"}}}`},
		{"address with prefix", `{"version":1,"awg":{"awg0/a":{"address":"10.8.1.2/32","private_key":"` + testKey(1) + `","preshared_key":"` + testKey(2) + `"}}}`},
		{"name without slash", `{"version":1,"awg":{"awg0phone":{"address":"10.8.1.2","private_key":"` + testKey(1) + `","preshared_key":"` + testKey(2) + `"}}}`},
		{"name with empty user", `{"version":1,"awg":{"awg0/":{"address":"10.8.1.2","private_key":"` + testKey(1) + `","preshared_key":"` + testKey(2) + `"}}}`},
		{"name with empty interface", `{"version":1,"awg":{"/a":{"address":"10.8.1.2","private_key":"` + testKey(1) + `","preshared_key":"` + testKey(2) + `"}}}`},
		{"name with two slashes", `{"version":1,"awg":{"awg0/a/b":{"address":"10.8.1.2","private_key":"` + testKey(1) + `","preshared_key":"` + testKey(2) + `"}}}`},
		{"private key missing", `{"version":1,"awg":{"awg0/a":{"address":"10.8.1.2","preshared_key":"` + testKey(2) + `"}}}`},
		{"preshared key missing", `{"version":1,"awg":{"awg0/a":{"address":"10.8.1.2","private_key":"` + testKey(1) + `"}}}`},
		{"address missing", `{"version":1,"awg":{"awg0/a":{"private_key":"` + testKey(1) + `","preshared_key":"` + testKey(2) + `"}}}`},
		{"not json", `version: 1`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "users.json")
			if err := os.WriteFile(path, []byte(tt.json), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatal("Load succeeded, want error")
			}
			for _, stored := range []string{secret, encodedSecret, testKey(1), testKey(2)} {
				if strings.Contains(err.Error(), stored) {
					t.Fatalf("error quotes a stored value %q: %v", stored, err)
				}
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil || string(got) != tt.json {
				t.Fatalf("file changed after failed Load: %q, %v", got, readErr)
			}
		})
	}
}
