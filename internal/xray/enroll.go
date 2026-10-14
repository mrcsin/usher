package xray

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"maps"

	"github.com/google/uuid"

	"github.com/mrcsin/usher/internal/state"
)

// passwordBytes is the entropy of a generated Trojan or Hysteria2 password.
const passwordBytes = 32

// enroll makes st hold a credential of the kind the inbound's protocol uses for every user in
// users. It creates the missing ones and never changes an existing entry; an existing entry of the
// wrong kind is an error. It reports whether it created any. On error st is left as it was.
func enroll(st *state.State, in inbound, users []string) (bool, error) {
	spec := protocols[in.Protocol]
	created := make(map[string]state.XrayEntry)
	for _, user := range users {
		name := state.EntryName(in.Tag, user)
		if e, ok := st.Xray[name]; ok {
			if hasID := e.ID != uuid.Nil; hasID != spec.usesID {
				return false, fmt.Errorf("user %s: stored credential does not fit protocol %s", user, in.Protocol)
			}
			continue
		}
		e, err := newXrayEntry(spec.usesID)
		if err != nil {
			return false, fmt.Errorf("user %s: %w", user, err)
		}
		created[name] = e
	}
	maps.Copy(st.Xray, created)
	return len(created) > 0, nil
}

func newXrayEntry(usesID bool) (state.XrayEntry, error) {
	if usesID {
		id, err := uuid.NewRandom()
		if err != nil {
			return state.XrayEntry{}, fmt.Errorf("generating id: %w", err)
		}
		return state.XrayEntry{ID: id}, nil
	}
	buf := make([]byte, passwordBytes)
	if _, err := rand.Read(buf); err != nil {
		return state.XrayEntry{}, fmt.Errorf("generating password: %w", err)
	}
	return state.XrayEntry{Password: base64.RawURLEncoding.EncodeToString(buf)}, nil
}
