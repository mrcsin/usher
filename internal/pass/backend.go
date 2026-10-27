package pass

import (
	"context"

	"github.com/mrcsin/usher/internal/state"
	"github.com/mrcsin/usher/internal/status"
)

// Session is one pass's connection to a backend, working one interface at a time.
type Session interface {
	Interfaces() []string
	// Prepare enrolls the users on the interface in st and returns their client files, keyed by
	// clients.Path, and whether it changed st. The pass saves st before any Apply.
	Prepare(st *state.State, name string, users []string) (files map[string][]byte, changed bool, err error)
	Apply(ctx context.Context, st *state.State, name string, users []string) error
	// Stats returns stats for users in users; a backend may leave out a user it has no entry for.
	Stats(ctx context.Context, st *state.State, name string, users []string) (map[string]status.UserStats, error)
	Close()
}

type Backend struct {
	Name     string   // used in logs, such as "awg-grpc"
	Suffixes []string // suffixes of the client files the backend renders, such as ".conf"
	Open     func(ctx context.Context) (Session, error)
}
