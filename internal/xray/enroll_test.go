package xray

import (
	"testing"
	"uuid"

	"github.com/mrcsin/usher/internal/state"
)

func TestEnroll(t *testing.T) {
	st := &state.State{Xray: map[string]state.XrayEntry{
		"vless/alice": {ID: seededID},
		"other/carol": {ID: uuid.NewV4()},
	}}

	if !enroll(st, "vless", []string{"alice", "bob"}) {
		t.Fatal("enroll reported no change after adding bob")
	}
	if got := st.Xray["vless/alice"].ID; got != seededID {
		t.Errorf("alice id changed to %v", got)
	}
	bob, ok := st.Xray["vless/bob"]
	if !ok || bob.ID == uuid.Nil() {
		t.Fatalf("bob entry = %+v, %v", bob, ok)
	}
	if bob.ID[6]>>4 != 4 {
		t.Errorf("bob id %v is not version 4", bob.ID)
	}
	if _, ok := st.Xray["other/carol"]; !ok {
		t.Error("entry of another inbound was dropped")
	}

	if enroll(st, "vless", []string{"alice", "bob"}) {
		t.Error("second enroll reported a change")
	}
	if got := st.Xray["vless/bob"].ID; got != bob.ID {
		t.Errorf("bob id changed to %v", got)
	}
}
