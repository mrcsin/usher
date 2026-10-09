package pass

import (
	"context"

	"github.com/mrcsin/usher/internal/state"
)

// Session is one pass's connection to a backend. Open lists the backend's interfaces; the
// session prepares and applies one interface at a time.
type Session interface {
	// Interfaces returns the names of the interfaces the backend reported.
	Interfaces() []string
	// Prepare enrolls the users on the interface in st and returns their client files, keyed by
	// clients.Path, and whether it changed st. The pass saves st before any Apply.
	Prepare(st *state.State, name string, users []string) (files map[string][]byte, changed bool, err error)
	// Apply makes the interface hold exactly the users.
	Apply(ctx context.Context, st *state.State, name string, users []string) error
	// Close releases the connection.
	Close()
}

// Backend is a VPN server usher moves users into.
type Backend struct {
	Name   string // used in logs, such as "awg-grpc"
	Suffix string // suffix of the client files the backend renders, such as ".conf"
	Open   func(ctx context.Context) (Session, error)
}
