package pass

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	awgv1 "github.com/mrcsin/awg-grpc/gen/awg/v1"

	"github.com/mrcsin/usher/internal/clients"
	"github.com/mrcsin/usher/internal/config"
	"github.com/mrcsin/usher/internal/state"
)

// callTimeout bounds each call to awg-grpc.
const callTimeout = 10 * time.Second

// Dial opens a connection to awg-grpc. It returns the client and the function that closes the
// connection.
type Dial func() (awgv1.ManagementServiceClient, func(), error)

// Pass runs reconcile passes. It keeps the last valid usher.yml it read.
type Pass struct {
	settings Settings
	dial     Dial
	log      *slog.Logger
	last     map[string][]string
}

// New returns a Pass that works with the paths and server facts in settings.
func New(settings Settings, dial Dial, log *slog.Logger) *Pass {
	return &Pass{settings: settings, dial: dial, log: log}
}

// Run executes one pass. It logs every failure and never panics on one; a failure of one
// interface leaves the others to apply.
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
	client, closeConn, err := p.dial()
	if err != nil {
		p.log.Error("connecting to awg-grpc", "error", err)
		return
	}
	defer closeConn()

	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	status, err := client.GetStatus(callCtx, &awgv1.GetStatusRequest{})
	cancel()
	if err != nil {
		p.log.Error("getting status", "error", err)
		return
	}

	byInterface := usersByInterface(cfg)
	reported := make(map[string]bool, len(status.GetInterfaces()))
	rendered := make(map[string]map[string][]byte)
	failed := make(map[string]bool)
	changed := false
	for _, iface := range status.GetInterfaces() {
		name := iface.GetName()
		reported[name] = true
		files, entriesChanged, err := p.prepare(st, iface, byInterface[name])
		if err != nil {
			p.log.Error("interface failed", "interface", name, "error", err)
			failed[name] = true
			continue
		}
		rendered[name] = files
		changed = changed || entriesChanged
	}

	if changed {
		if err := state.Save(p.settings.StatePath, st); err != nil {
			p.log.Error("saving state", "error", err)
			return
		}
	}

	for _, iface := range status.GetInterfaces() {
		name := iface.GetName()
		if failed[name] {
			continue
		}
		if err := p.apply(ctx, client, st, name, byInterface[name]); err != nil {
			p.log.Error("interface failed", "interface", name, "error", err)
			failed[name] = true
		}
	}

	p.logUnknown(cfg, reported)

	desired := make(map[string][]byte)
	for name, files := range rendered {
		if !failed[name] {
			maps.Copy(desired, files)
		}
	}
	kept, err := clients.Existing(p.settings.ClientsDir, failed)
	if err != nil {
		p.log.Error("keeping client configs", "error", err)
	}
	maps.Copy(desired, kept)
	if err := clients.Mirror(p.settings.ClientsDir, desired); err != nil {
		p.log.Error("mirroring clients directory", "error", err)
	}
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

func (p *Pass) prepare(st *state.State, iface *awgv1.InterfaceStatus, users []string) (map[string][]byte, bool, error) {
	if !iface.GetPresent() {
		return nil, false, errors.New("interface is not present in the kernel")
	}
	changed, err := enroll(p.log, st, iface, users)
	if err != nil {
		return nil, false, err
	}
	files := make(map[string][]byte, len(users))
	for _, user := range users {
		content, err := clients.Render(st.AWG[state.EntryName(iface.GetName(), user)], iface, p.settings.Host, p.settings.DNS)
		if err != nil {
			return nil, false, fmt.Errorf("user %s: %w", user, err)
		}
		files[clients.Path(user, iface.GetName())] = content
	}
	return files, changed, nil
}

func (p *Pass) apply(ctx context.Context, client awgv1.ManagementServiceClient, st *state.State, ifaceName string, users []string) error {
	peers := make([]*awgv1.Peer, 0, len(users))
	for _, user := range users {
		e := st.AWG[state.EntryName(ifaceName, user)]
		peers = append(peers, &awgv1.Peer{
			PublicKey:    e.PublicKey(),
			PresharedKey: e.PresharedKey[:],
			AllowedIp:    e.Route().String(),
		})
	}

	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := client.ApplyPeers(callCtx, &awgv1.ApplyPeersRequest{
		InterfaceName: ifaceName,
		Peers:         peers,
		AllowEmpty:    len(peers) == 0,
	})
	if err != nil {
		return fmt.Errorf("applying peers: %w", err)
	}

	names := publicKeyNames(st, ifaceName)
	added, removed, updated := nameKeys(names, resp.GetAdded()), nameKeys(names, resp.GetRemoved()), nameKeys(names, resp.GetUpdated())
	if len(added)+len(removed)+len(updated) > 0 {
		p.log.Info("peers changed", "interface", ifaceName, "added", added, "removed", removed, "updated", updated)
	}
	return nil
}

// publicKeyNames maps the public key of every entry of the interface, switched off or not, to
// its user name.
func publicKeyNames(st *state.State, ifaceName string) map[string]string {
	names := make(map[string]string)
	for user, e := range st.InterfaceEntries(ifaceName) {
		names[string(e.PublicKey())] = user
	}
	return names
}

func nameKeys(names map[string]string, keys [][]byte) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if user, ok := names[string(k)]; ok {
			out = append(out, user)
		} else {
			out = append(out, base64.StdEncoding.EncodeToString(k))
		}
	}
	slices.Sort(out)
	return out
}

func (p *Pass) logUnknown(cfg map[string][]string, reported map[string]bool) {
	for _, user := range slices.Sorted(maps.Keys(cfg)) {
		for _, ifaceName := range cfg[user] {
			if !reported[ifaceName] {
				p.log.Error("interface is not reported by awg-grpc", "interface", ifaceName, "user", user)
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
