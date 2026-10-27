package pass

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/mrcsin/usher/internal/atomicfile"
	"github.com/mrcsin/usher/internal/clients"
	"github.com/mrcsin/usher/internal/config"
	"github.com/mrcsin/usher/internal/state"
	"github.com/mrcsin/usher/internal/status"
)

// Pass keeps the last valid usher.yml it read.
type Pass struct {
	settings Settings
	backends []Backend
	log      *slog.Logger
	last     map[string][]string
}

func New(settings Settings, backends []Backend, log *slog.Logger) *Pass {
	return &Pass{settings: settings, backends: backends, log: log}
}

type openBackend struct {
	Backend
	session Session
}

type interfaceKey struct {
	backend int
	name    string
}

// Run executes one pass. It logs every failure; one failed interface or down backend never
// blocks the others.
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
	rendered, changed := p.prepareAll(st, open, byInterface, owners, failed)

	if changed {
		if err := state.Save(p.settings.StatePath, st); err != nil {
			p.log.Error("saving state", "error", err)
			return
		}
	}

	prepareFailed := maps.Clone(failed)
	p.applyAll(ctx, st, open, byInterface, failed)
	// A cancelled pass fails every call, so its file would show every interface down.
	if p.settings.StatusPath != "" && ctx.Err() == nil {
		p.writeStatus(ctx, st, open, byInterface, prepareFailed, failed)
	}

	// A backend that is down hides its interface names, so every unreferenced name could be one.
	if len(down) == 0 {
		p.logUnknown(cfg, owners)
	}

	p.mirrorClients(open, down, rendered, failed)
}

func (p *Pass) prepareAll(st *state.State, open []openBackend, byInterface map[string][]string,
	owners map[string][]string, failed map[interfaceKey]bool) (map[interfaceKey]map[string][]byte, bool) {
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
	return rendered, changed
}

// applyAll marks in failed each interface whose Apply returned an error.
func (p *Pass) applyAll(ctx context.Context, st *state.State, open []openBackend,
	byInterface map[string][]string, failed map[interfaceKey]bool) {
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
}

// writeStatus writes the status file for the interfaces of the open backends. A name that two
// backends report is written once, so the file holds no duplicate series.
func (p *Pass) writeStatus(ctx context.Context, st *state.State, open []openBackend,
	byInterface map[string][]string, prepareFailed, failed map[interfaceKey]bool) {
	var interfaces []status.Interface
	written := make(map[string]bool)
	for i, b := range open {
		for _, name := range b.session.Interfaces() {
			if written[name] {
				continue
			}
			written[name] = true
			key := interfaceKey{i, name}
			iface := status.Interface{Name: name, Up: !failed[key]}
			if prepareFailed[key] {
				interfaces = append(interfaces, iface)
				continue
			}
			users, err := b.session.Stats(ctx, st, name, byInterface[name])
			if err != nil {
				p.log.Error("reading stats", "backend", b.Name, "interface", name, "error", err)
			} else {
				iface.Users = users
			}
			interfaces = append(interfaces, iface)
		}
	}
	if err := atomicfile.Write(p.settings.StatusPath, status.Render(interfaces), 0o644); err != nil {
		p.log.Error("writing status file", "error", err)
	}
}

// mirrorClients keeps the current files of failed interfaces and of backends that are down.
func (p *Pass) mirrorClients(open []openBackend, down []Backend,
	rendered map[interfaceKey]map[string][]byte, failed map[interfaceKey]bool) {
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

func (p *Pass) keepFiles(desired map[string][]byte, b Backend, keep func(ifaceName string) bool) {
	kept, err := clients.Existing(p.settings.ClientsDir, b.Suffixes, keep)
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
