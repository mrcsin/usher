package pass

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/mrcsin/usher/internal/clients"
	"github.com/mrcsin/usher/internal/config"
	"github.com/mrcsin/usher/internal/state"
)

// Pass runs reconcile passes. It keeps the last valid usher.yml it read.
type Pass struct {
	settings Settings
	backends []Backend
	log      *slog.Logger
	last     map[string][]string
}

// New returns a Pass that works with the paths in settings and moves users into backends.
func New(settings Settings, backends []Backend, log *slog.Logger) *Pass {
	return &Pass{settings: settings, backends: backends, log: log}
}

type openBackend struct {
	Backend
	session Session
}

// interfaceKey identifies an interface of one backend, by the backend's index in the open list.
type interfaceKey struct {
	backend int
	name    string
}

// Run executes one pass. It logs every failure and never panics on one; a failure of one
// interface leaves the others to apply, and a backend that is down leaves the other backends to
// apply.
func (p *Pass) Run(ctx context.Context) {
	cfg, ok := p.loadConfig()
	if !ok {
		return
	}
	st, err := state.Load(p.settings.StatePath)
	if err != nil {
		p.log.Error("loading state", "error", err)
		return
	}
	open, down := p.openBackends(ctx)
	defer func() {
		for _, b := range open {
			b.session.Close()
		}
	}()

	byInterface := usersByInterface(cfg)
	owners := interfaceOwners(open)
	failed := make(map[interfaceKey]bool)
	rendered := make(map[interfaceKey]map[string][]byte)
	changed := false
	for i, b := range open {
		for _, name := range b.session.Interfaces() {
			key := interfaceKey{i, name}
			if len(owners[name]) > 1 {
				err := fmt.Errorf("reported by %s", strings.Join(owners[name], " and "))
				p.log.Error("interface failed", "backend", b.Name, "interface", name, "error", err)
				failed[key] = true
				continue
			}
			files, entriesChanged, err := b.session.Prepare(st, name, byInterface[name])
			if err != nil {
				p.log.Error("interface failed", "backend", b.Name, "interface", name, "error", err)
				failed[key] = true
				continue
			}
			rendered[key] = files
			changed = changed || entriesChanged
		}
	}

	if changed {
		if err := state.Save(p.settings.StatePath, st); err != nil {
			p.log.Error("saving state", "error", err)
			return
		}
	}

	for i, b := range open {
		for _, name := range b.session.Interfaces() {
			key := interfaceKey{i, name}
			if failed[key] {
				continue
			}
			if err := b.session.Apply(ctx, st, name, byInterface[name]); err != nil {
				p.log.Error("interface failed", "backend", b.Name, "interface", name, "error", err)
				failed[key] = true
			}
		}
	}

	// A backend that is down hides its interface names, so every unreferenced name could be one.
	if len(down) == 0 {
		p.logUnknown(cfg, owners)
	}

	desired := make(map[string][]byte)
	for key, files := range rendered {
		if !failed[key] {
			maps.Copy(desired, files)
		}
	}
	for i, b := range open {
		p.keepFiles(desired, b.Backend, func(name string) bool { return failed[interfaceKey{i, name}] })
	}
	for _, b := range down {
		p.keepFiles(desired, b, func(string) bool { return true })
	}
	if err := clients.Mirror(p.settings.ClientsDir, desired); err != nil {
		p.log.Error("mirroring clients directory", "error", err)
	}
}

// openBackends opens a session for every backend and returns the backends that failed to open.
func (p *Pass) openBackends(ctx context.Context) ([]openBackend, []Backend) {
	var open []openBackend
	var down []Backend
	for _, b := range p.backends {
		session, err := b.Open(ctx)
		if err != nil {
			p.log.Error("backend is down", "backend", b.Name, "error", err)
			down = append(down, b)
			continue
		}
		open = append(open, openBackend{b, session})
	}
	return open, down
}

// keepFiles copies the current client files of the backend's interfaces that keep selects into
// desired.
func (p *Pass) keepFiles(desired map[string][]byte, b Backend, keep func(ifaceName string) bool) {
	kept, err := clients.Existing(p.settings.ClientsDir, b.Suffix, keep)
	if err != nil {
		p.log.Error("keeping client files", "backend", b.Name, "error", err)
	}
	maps.Copy(desired, kept)
}

func (p *Pass) loadConfig() (map[string][]string, bool) {
	cfg, err := config.Load(p.settings.ConfigPath)
	if err == nil {
		p.last = cfg
		return cfg, true
	}
	if p.last == nil {
		p.log.Error("config is invalid and there is no earlier valid one", "error", err)
		return nil, false
	}
	p.log.Error("config is invalid, using the last valid one", "error", err)
	return p.last, true
}

func interfaceOwners(open []openBackend) map[string][]string {
	owners := make(map[string][]string)
	for _, b := range open {
		for _, name := range b.session.Interfaces() {
			owners[name] = append(owners[name], b.Name)
		}
	}
	return owners
}

func (p *Pass) logUnknown(cfg map[string][]string, owners map[string][]string) {
	for _, user := range slices.Sorted(maps.Keys(cfg)) {
		for _, ifaceName := range cfg[user] {
			if _, ok := owners[ifaceName]; !ok {
				p.log.Error("interface is not reported by any backend", "interface", ifaceName, "user", user)
			}
		}
	}
}

func usersByInterface(cfg map[string][]string) map[string][]string {
	out := make(map[string][]string)
	for user, ifaceNames := range cfg {
		for _, ifaceName := range ifaceNames {
			out[ifaceName] = append(out[ifaceName], user)
		}
	}
	for _, users := range out {
		slices.Sort(users)
	}
	return out
}
