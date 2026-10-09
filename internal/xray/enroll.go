package xray

import (
	"uuid"

	"github.com/mrcsin/usher/internal/state"
)

// enroll makes st hold an entry for every user on the inbound. It creates a missing entry with a
// random UUID and never changes an existing one. It reports whether it created any.
func enroll(st *state.State, tag string, users []string) bool {
	changed := false
	for _, user := range users {
		name := state.EntryName(tag, user)
		if _, ok := st.Xray[name]; ok {
			continue
		}
		st.Xray[name] = state.XrayEntry{ID: uuid.NewV4()}
		changed = true
	}
	return changed
}
