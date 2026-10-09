// Package awg is the AmneziaWG backend: it enrolls users, renders their client configs and
// applies their peers through the awg-grpc socket.
package awg

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"time"

	awgv1 "github.com/mrcsin/awg-grpc/gen/awg/v1"

	"github.com/mrcsin/usher/internal/clients"
	"github.com/mrcsin/usher/internal/state"
)

// Suffix is the file suffix of an AmneziaWG client config.
const Suffix = ".conf"

// callTimeout bounds each call to awg-grpc.
const callTimeout = 10 * time.Second

// Dial opens a connection to awg-grpc. It returns the client and the function that closes the
// connection.
type Dial func() (awgv1.ManagementServiceClient, func(), error)

// Session is one pass's view of awg-grpc: the interfaces it reported when the session opened.
type Session struct {
	client    awgv1.ManagementServiceClient
	closeConn func()
	ifaces    map[string]*awgv1.InterfaceStatus
	names     []string
	host      netip.Addr
	dns       []netip.Addr
	log       *slog.Logger
}

// Open connects to awg-grpc and lists its interfaces. host and dns go into the client configs.
func Open(ctx context.Context, dial Dial, host netip.Addr, dns []netip.Addr, log *slog.Logger) (*Session, error) {
	client, closeConn, err := dial()
	if err != nil {
		return nil, fmt.Errorf("connecting to awg-grpc: %w", err)
	}
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	status, err := client.GetStatus(callCtx, &awgv1.GetStatusRequest{})
	if err != nil {
		closeConn()
		return nil, fmt.Errorf("getting status: %w", err)
	}
	s := &Session{
		client:    client,
		closeConn: closeConn,
		ifaces:    make(map[string]*awgv1.InterfaceStatus, len(status.GetInterfaces())),
		host:      host,
		dns:       dns,
		log:       log,
	}
	for _, iface := range status.GetInterfaces() {
		s.ifaces[iface.GetName()] = iface
		s.names = append(s.names, iface.GetName())
	}
	return s, nil
}

// Interfaces returns the names of the interfaces awg-grpc reported, in the order it reported them.
func (s *Session) Interfaces() []string {
	return s.names
}

// Prepare enrolls the users on the interface and renders their client configs, keyed by
// clients.Path. It reports whether it changed st. It fails an interface that is not present in
// the kernel.
func (s *Session) Prepare(st *state.State, name string, users []string) (map[string][]byte, bool, error) {
	iface := s.ifaces[name]
	if !iface.GetPresent() {
		return nil, false, errors.New("interface is not present in the kernel")
	}
	changed, err := enroll(s.log, st, iface, users)
	if err != nil {
		return nil, false, err
	}
	files := make(map[string][]byte, len(users))
	for _, user := range users {
		content, err := Render(st.AWG[state.EntryName(name, user)], iface, s.host, s.dns)
		if err != nil {
			return nil, false, fmt.Errorf("user %s: %w", user, err)
		}
		files[clients.Path(user, name, Suffix)] = content
	}
	return files, changed, nil
}

// Apply sends the peers of the users to awg-grpc and logs what changed.
func (s *Session) Apply(ctx context.Context, st *state.State, name string, users []string) error {
	peers := make([]*awgv1.Peer, 0, len(users))
	for _, user := range users {
		e := st.AWG[state.EntryName(name, user)]
		peers = append(peers, &awgv1.Peer{
			PublicKey:    e.PublicKey(),
			PresharedKey: e.PresharedKey[:],
			AllowedIp:    e.Route().String(),
		})
	}

	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := s.client.ApplyPeers(callCtx, &awgv1.ApplyPeersRequest{
		InterfaceName: name,
		Peers:         peers,
		AllowEmpty:    len(peers) == 0,
	})
	if err != nil {
		return fmt.Errorf("applying peers: %w", err)
	}

	names := publicKeyNames(st, name)
	added, removed, updated := nameKeys(names, resp.GetAdded()), nameKeys(names, resp.GetRemoved()), nameKeys(names, resp.GetUpdated())
	if len(added)+len(removed)+len(updated) > 0 {
		s.log.Info("peers changed", "interface", name, "added", added, "removed", removed, "updated", updated)
	}
	return nil
}

// Close closes the connection to awg-grpc.
func (s *Session) Close() {
	s.closeConn()
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
