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

// callTimeout bounds each call to Xray.
const callTimeout = 10 * time.Second

// Dial opens a connection to the Xray API. It returns the client and the function that closes the
// connection.
type Dial func() (command.HandlerServiceClient, func(), error)

// Session is one pass's view of Xray: the managed inbounds of the config file and the drift of
// each against the running Xray.
type Session struct {
	client    command.HandlerServiceClient
	closeConn func()
	inbounds  []Inbound
	byTag     map[string]Inbound
	drift     map[string]error
	// broken maps a tag whose accounts and client files cannot be built this pass to the reason.
	// Apply only revokes users on such a tag.
	broken map[string]error
	host   string
	log    *slog.Logger
}

// Open reads the Xray config at configPath, connects to the API and checks every inbound of the
// file against the running Xray. host is the address clients dial unless an inbound sets one. When
// socketPath is a stale socket of a killed Xray, Open removes it and fails, so the pass reports Xray
// down and the restart policy can start Xray again.
func Open(ctx context.Context, dial Dial, socketPath, configPath, host string, log *slog.Logger) (*Session, error) {
	inbounds, err := ReadConfig(configPath)
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
	byTag := make(map[string]Inbound, len(inbounds))
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

// Interfaces returns the tag of every managed inbound of the file, valid or not, so a tag that
// fails keeps its client files. A tag the file defines twice appears once; its entry carries the
// error.
func (s *Session) Interfaces() []string {
	tags := make([]string, 0, len(s.byTag))
	for _, in := range s.inbounds {
		if !slices.Contains(tags, in.Tag) {
			tags = append(tags, in.Tag)
		}
	}
	return tags
}

// Prepare enrolls the users on the inbound in st and renders their client files. When the inbound
// failed structural validation, Prepare returns the error and the pass skips Apply, so usher
// touches no user of that tag. When only the x-usher block failed, an enrolled credential does not
// fit the inbound or a file cannot be rendered, Prepare keeps
// the reason and returns no error, and Apply then still revokes the users who left and returns the
// reason, which keeps the old client files.
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

// Apply makes the inbound hold exactly the users, then returns the error of the tag, if any: the
// reason Prepare kept, else the drift. Users are reconciled even under most drift, so a revoked
// user loses access. When the running inbound is missing or runs another protocol, Apply changes
// nothing, because an account of the wrong type crashes Xray. It also changes nothing when the
// inbound failed structural validation. When Prepare kept a reason, Apply only removes users.
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

// Close closes the connection to Xray.
func (s *Session) Close() {
	s.closeConn()
}

// userChanges is what reconcile did to an inbound, by email.
type userChanges struct {
	added, removed, updated []string
}

// reconcile lists the users of the inbound, removes foreign and changed ones, adds missing and
// changed ones, and logs the difference. It tries every operation and joins the errors, so one
// failure does not hold back a removal; the next pass converges from the listed state.
func (s *Session) reconcile(ctx context.Context, st *state.State, in Inbound, users []string) error {
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
	// A changed credential whose new account was refused left the user without access.
	changes.removed = append(changes.removed, slices.Sorted(maps.Keys(replaced))...)
	s.logChanges(in.Tag, changes)
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("applying users: %w", err)
	}
	return nil
}

// revoke removes every user of the inbound whose email is not one of users, and touches no other
// account. It serves an inbound whose accounts cannot be built.
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
		email := u.GetEmail()
		if u == nil || keep[email] {
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
		s.log.Info("users changed", "inbound", tag, "added", changes.added,
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

// liveCredentials maps the email of every user of the inbound to the credential of its account.
func (s *Session) liveCredentials(ctx context.Context, in Inbound) (map[string]string, error) {
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
