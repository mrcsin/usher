// Package state loads and saves users.json, the only source of per-user secrets and addresses.
package state

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"net/netip"
	"os"
	"strings"

	"github.com/mrcsin/usher/internal/atomicfile"
)

const version = 1

const fileMode = 0o600

// KeyLength is the size in bytes of a private key and of a preshared key.
const KeyLength = 32

// hostBits is the prefix length of the one-address route a peer gets.
const hostBits = 32

// Key is a private or a preshared key. It is base64 in users.json.
type Key [KeyLength]byte

// MarshalText encodes the key as standard base64.
func (k Key) MarshalText() ([]byte, error) {
	return []byte(k.String()), nil
}

// UnmarshalText decodes standard base64 of exactly KeyLength bytes. The error never quotes text.
func (k *Key) UnmarshalText(text []byte) error {
	raw, err := base64.StdEncoding.DecodeString(string(text))
	if err != nil || len(raw) != KeyLength {
		return fmt.Errorf("not base64 of %d bytes", KeyLength)
	}
	copy(k[:], raw)
	return nil
}

// String returns the standard base64 form of the key.
func (k Key) String() string {
	return base64.StdEncoding.EncodeToString(k[:])
}

// Entry holds the secrets and the address of one user on one AmneziaWG interface.
type Entry struct {
	Address      netip.Addr `json:"address"`
	PrivateKey   Key        `json:"private_key"`
	PresharedKey Key        `json:"preshared_key"`
}

// PublicKey returns the X25519 public key of the private key.
func (e Entry) PublicKey() []byte {
	// NewPrivateKey fails only for a key that is not 32 bytes, and a Key always is.
	key, _ := ecdh.X25519().NewPrivateKey(e.PrivateKey[:])
	return key.PublicKey().Bytes()
}

// Route returns the one-address prefix of the entry.
func (e Entry) Route() netip.Prefix {
	return netip.PrefixFrom(e.Address, hostBits)
}

// EntryName returns the key of a user's entry on an interface in State.AWG.
func EntryName(iface, user string) string {
	return iface + "/" + user
}

// State is the content of users.json. AWG maps "interface/user" to its entry.
type State struct {
	Version int              `json:"version"`
	AWG     map[string]Entry `json:"awg"`
}

// InterfaceEntries yields the user name and the entry of every entry on the interface.
func (s *State) InterfaceEntries(iface string) iter.Seq2[string, Entry] {
	return func(yield func(string, Entry) bool) {
		for name, e := range s.AWG {
			if user, ok := strings.CutPrefix(name, iface+"/"); ok && !yield(user, e) {
				return
			}
		}
	}
}

// Load reads path. A missing file is an empty state. Errors never quote a key, and Load never
// writes the file.
func Load(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &State{Version: version, AWG: map[string]Entry{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if s.Version != version {
		return nil, fmt.Errorf("%s: unsupported version %d", path, s.Version)
	}
	if s.AWG == nil {
		s.AWG = map[string]Entry{}
	}
	for name, e := range s.AWG {
		if err := validate(name, e); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return &s, nil
}

// Save writes s to path atomically with mode 0600.
func Save(path string, s *State) error {
	s.Version = version
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding state: %w", err)
	}
	data = append(data, '\n')

	return atomicfile.Write(path, data, fileMode)
}

func validate(name string, e Entry) error {
	iface, user, ok := strings.Cut(name, "/")
	if !ok || iface == "" || user == "" || strings.Contains(user, "/") {
		return fmt.Errorf("entry %q: name is not interface/user", name)
	}
	if !e.Address.Is4() {
		return fmt.Errorf("entry %q: address is not IPv4", name)
	}
	if e.PrivateKey == (Key{}) {
		return fmt.Errorf("entry %q: private_key is missing", name)
	}
	if e.PresharedKey == (Key{}) {
		return fmt.Errorf("entry %q: preshared_key is missing", name)
	}
	return nil
}
