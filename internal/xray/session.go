package xray

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"google.golang.org/protobuf/proto"

	"github.com/mrcsin/usher/internal/state"
)

const callTimeout = 10 * time.Second

type Dial func() (command.HandlerServiceClient, func(), error)

type Session struct {
	client    command.HandlerServiceClient
	closeConn func()
	inbounds  []inbound
	byTag     map[string]inbound
	drift     map[string]error
	// broken maps a tag whose accounts or client files cannot be built to the reason.
	broken map[string]error
	host   string
	log    *slog.Logger
}

// Open removes a stale socket left by a killed Xray and fails the pass.
func Open(ctx context.Context, dial Dial, socketPath, configPath, host string, log *slog.Logger) (*Session, error) {
	inbounds, err := readConfig(configPath)
	if err != nil {
		return nil, err
	}
	removed, err := removeStaleSocket(socketPath)
	if err != nil {
		return nil, err
	}
	if removed {
		log.Warn("removed stale xray socket", "path", socketPath)
		return nil, errors.New("xray is down: removed its stale api socket")
	}
	client, closeConn, err := dial()
	if err != nil {
		return nil, fmt.Errorf("connecting to xray: %w", err)
	}
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	drift, err := checkDrift(callCtx, client, log, inbounds)
	if err != nil {
		closeConn()
		return nil, err
	}
	byTag := make(map[string]inbound, len(inbounds))
	for _, in := range inbounds {
		byTag[in.Tag] = in
	}
	return &Session{
		client:    client,
		closeConn: closeConn,
		inbounds:  inbounds,
		byTag:     byTag,
		drift:     drift,
		broken:    map[string]error{},
		host:      host,
		log:       log,
	}, nil
}

// Interfaces includes invalid tags, so their client files are kept.
func (s *Session) Interfaces() []string {
	tags := make([]string, 0, len(s.byTag))
	for _, in := range s.inbounds {
		if !slices.Contains(tags, in.Tag) {
			tags = append(tags, in.Tag)
		}
	}
	return tags
}

// Prepare returns structural errors; for a hint, enroll or render error it returns nil and
// Apply only revokes.
func (s *Session) Prepare(st *state.State, name string, users []string) (map[string][]byte, bool, error) {
	in := s.byTag[name]
	if in.Err != nil {
		if !in.hintsOnly {
			return nil, false, in.Err
		}
		s.broken[name] = in.Err
		return nil, false, nil
	}
	changed, err := enroll(st, in, users)
	if err != nil {
		s.broken[name] = err
		return nil, false, nil
	}
	files := make(map[string][]byte, 2*len(users))
	for _, user := range users {
		userFiles, err := renderFiles(s.log, in, s.host, user, st.Xray[state.EntryName(name, user)])
		if err != nil {
			s.broken[name] = fmt.Errorf("user %s: %w", user, err)
			return nil, changed, nil
		}
		maps.Copy(files, userFiles)
	}
	return files, changed, nil
}

// Apply never touches a tag whose running inbound is missing or runs another protocol: an
// account of the wrong type crashes Xray.
func (s *Session) Apply(ctx context.Context, st *state.State, name string, users []string) error {
	if in := s.byTag[name]; in.Err != nil && !in.hintsOnly {
		return in.Err
	}
	drift := s.drift[name]
	var driftErr *driftError
	if errors.As(drift, &driftErr) && driftErr.unusable {
		return drift
	}
	if broken := s.broken[name]; broken != nil {
		return errors.Join(s.revoke(ctx, name, users), broken)
	}
	return errors.Join(s.reconcile(ctx, st, s.byTag[name], users), drift)
}

func (s *Session) Close() {
	s.closeConn()
}

type userChanges struct {
	added, removed, updated []string
}

// reconcile joins the errors, so one failure never blocks a removal.
func (s *Session) reconcile(ctx context.Context, st *state.State, in inbound, users []string) error {
	desired := make(map[string]*serial.TypedMessage, len(users))
	credentials := make(map[string]string, len(users))
	spec := protocols[in.Protocol]
	for _, user := range users {
		email := state.EntryName(in.Tag, user)
		account := spec.account(in, st.Xray[email])
		desired[email] = serial.ToTypedMessage(account)
		credentials[email] = spec.credential(account)
	}

	live, err := s.liveCredentials(ctx, in)
	if err != nil {
		return err
	}

	var changes userChanges
	var errs []error
	present := make(map[string]bool, len(live))
	replaced := make(map[string]bool) // changed credentials whose old account is gone
	for _, email := range slices.Sorted(maps.Keys(live)) {
		want, wanted := credentials[email]
		if wanted && live[email] == want {
			present[email] = true
			continue
		}
		if err := s.removeUser(ctx, in.Tag, email); err != nil {
			errs = append(errs, err)
			present[email] = true
			continue
		}
		if wanted {
			replaced[email] = true
		} else {
			changes.removed = append(changes.removed, email)
		}
	}
	for _, email := range slices.Sorted(maps.Keys(desired)) {
		if present[email] {
			continue
		}
		op := &command.AddUserOperation{User: &protocol.User{Email: email, Account: desired[email]}}
		if err := s.alter(ctx, in.Tag, op); err != nil {
			errs = append(errs, fmt.Errorf("adding %s: %w", email, err))
			continue
		}
		if replaced[email] {
			changes.updated = append(changes.updated, email)
			delete(replaced, email)
		} else {
			changes.added = append(changes.added, email)
		}
	}
	// A changed credential whose new account was refused leaves the user without access.
	changes.removed = append(changes.removed, slices.Sorted(maps.Keys(replaced))...)
	s.logChanges(in.Tag, changes)
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("applying users: %w", err)
	}
	return nil
}

// revoke only removes users, so it is safe on an inbound whose accounts cannot be built.
func (s *Session) revoke(ctx context.Context, tag string, users []string) error {
	keep := make(map[string]bool, len(users))
	for _, user := range users {
		keep[state.EntryName(tag, user)] = true
	}
	listed, err := s.listUsers(ctx, tag)
	if err != nil {
		return err
	}
	var changes userChanges
	var errs []error
	for _, u := range listed {
		if u == nil {
			continue
		}
		email := u.GetEmail()
		if keep[email] {
			continue
		}
		if err := s.removeUser(ctx, tag, email); err != nil {
			errs = append(errs, err)
			continue
		}
		changes.removed = append(changes.removed, email)
	}
	slices.Sort(changes.removed)
	s.logChanges(tag, changes)
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("revoking users: %w", err)
	}
	return nil
}

func (s *Session) logChanges(tag string, changes userChanges) {
	if len(changes.added)+len(changes.removed)+len(changes.updated) > 0 {
		s.log.Info("users changed", "interface", tag, "added", changes.added,
			"removed", changes.removed, "updated", changes.updated)
	}
}

func (s *Session) removeUser(ctx context.Context, tag, email string) error {
	if err := s.alter(ctx, tag, &command.RemoveUserOperation{Email: email}); err != nil {
		return fmt.Errorf("removing %s: %w", email, err)
	}
	return nil
}

func (s *Session) listUsers(ctx context.Context, tag string) ([]*protocol.User, error) {
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := s.client.GetInboundUsers(callCtx, &command.GetInboundUserRequest{Tag: tag})
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	return resp.GetUsers(), nil
}

func (s *Session) liveCredentials(ctx context.Context, in inbound) (map[string]string, error) {
	users, err := s.listUsers(ctx, in.Tag)
	if err != nil {
		return nil, err
	}
	spec := protocols[in.Protocol]
	live := make(map[string]string, len(users))
	for _, user := range users {
		if user == nil {
			continue
		}
		var credential string
		if instance, err := user.GetAccount().GetInstance(); err == nil {
			credential = spec.credential(instance)
		}
		live[user.GetEmail()] = credential
	}
	return live, nil
}

func (s *Session) alter(ctx context.Context, tag string, op proto.Message) error {
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	_, err := s.client.AlterInbound(callCtx, &command.AlterInboundRequest{
		Tag:       tag,
		Operation: serial.ToTypedMessage(op),
	})
	return err
}
