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
	host      string
	log       *slog.Logger
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
		host:      host,
		log:       log,
	}, nil
}

// Interfaces returns the tag of every managed inbound of the file, valid or not, so a tag that
// fails keeps its client files.
func (s *Session) Interfaces() []string {
	tags := make([]string, 0, len(s.inbounds))
	for _, in := range s.inbounds {
		tags = append(tags, in.Tag)
	}
	return tags
}

// Prepare enrolls the users on the inbound in st and renders their client files. It fails an inbound that did not validate.
func (s *Session) Prepare(st *state.State, name string, users []string) (map[string][]byte, bool, error) {
	in := s.byTag[name]
	if in.Err != nil {
		return nil, false, in.Err
	}
	changed, err := enroll(st, in, users)
	if err != nil {
		return nil, false, err
	}
	files := make(map[string][]byte, 2*len(users))
	for _, user := range users {
		userFiles, err := renderFiles(s.log, in, s.host, user, st.Xray[state.EntryName(name, user)])
		if err != nil {
			return nil, false, fmt.Errorf("user %s: %w", user, err)
		}
		maps.Copy(files, userFiles)
	}
	return files, changed, nil
}

// Apply makes the inbound hold exactly the users, then returns the drift error of the tag, if
// any. Users are reconciled even under drift, so a revoked user loses access.
func (s *Session) Apply(ctx context.Context, st *state.State, name string, users []string) error {
	in := s.byTag[name]
	if err := s.reconcile(ctx, st, in, users); err != nil {
		return err
	}
	return s.drift[name]
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
		e := st.Xray[state.EntryName(in.Tag, user)]
		desired[state.EntryName(in.Tag, user)] = typedAccount(in, e)
		credentials[state.EntryName(in.Tag, user)] = spec.credential(spec.account(in, e))
	}

	live, err := s.liveCredentials(ctx, in)
	if err != nil {
		return err
	}

	var changes userChanges
	var errs []error
	present := make(map[string]bool, len(live))
	for _, email := range slices.Sorted(maps.Keys(live)) {
		want, wanted := credentials[email]
		if wanted && live[email] == want {
			present[email] = true
			continue
		}
		if err := s.alter(ctx, in.Tag, &command.RemoveUserOperation{Email: email}); err != nil {
			errs = append(errs, fmt.Errorf("removing %s: %w", email, err))
			present[email] = true
			continue
		}
		if wanted {
			changes.updated = append(changes.updated, email)
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
		if _, listed := live[email]; !listed {
			changes.added = append(changes.added, email)
		}
	}
	if len(changes.added)+len(changes.removed)+len(changes.updated) > 0 {
		s.log.Info("users changed", "inbound", in.Tag, "added", changes.added,
			"removed", changes.removed, "updated", changes.updated)
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("applying users: %w", err)
	}
	return nil
}

// liveCredentials maps the email of every user of the inbound to the credential of its account.
func (s *Session) liveCredentials(ctx context.Context, in Inbound) (map[string]string, error) {
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := s.client.GetInboundUsers(callCtx, &command.GetInboundUserRequest{Tag: in.Tag})
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	spec := protocols[in.Protocol]
	live := make(map[string]string, len(resp.GetUsers()))
	for _, user := range resp.GetUsers() {
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
