package xray

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/mrcsin/usher/gen/xray/app/proxyman/command"
	userproto "github.com/mrcsin/usher/gen/xray/common/protocol"
	"github.com/mrcsin/usher/gen/xray/common/serial"
	"github.com/mrcsin/usher/internal/state"
)

// Suffix is the file suffix of an Xray client link file.
const Suffix = ".txt"

// callTimeout bounds each call to Xray.
const callTimeout = 10 * time.Second

// userLevel is the level of every user usher adds.
const userLevel = 0

// Dial opens a connection to the Xray API. It returns the client and the function that closes the
// connection.
type Dial func() (command.HandlerServiceClient, func(), error)

// Session is one pass's view of Xray: the inbounds it reported when the session opened.
type Session struct {
	client    command.HandlerServiceClient
	closeConn func()
	inbounds  map[string]inbound
	names     []string
	log       *slog.Logger
}

// Open connects to Xray and lists its inbounds.
func Open(ctx context.Context, dial Dial, log *slog.Logger) (*Session, error) {
	client, closeConn, err := dial()
	if err != nil {
		return nil, fmt.Errorf("connecting to xray: %w", err)
	}
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	listed, err := listInbounds(callCtx, client)
	if err != nil {
		closeConn()
		return nil, err
	}
	s := &Session{
		client:    client,
		closeConn: closeConn,
		inbounds:  make(map[string]inbound, len(listed)),
		log:       log,
	}
	for _, in := range listed {
		s.inbounds[in.tag] = in
		s.names = append(s.names, in.tag)
	}
	return s, nil
}

// Interfaces returns the tags of the inbounds Xray reported, in the order it reported them.
func (s *Session) Interfaces() []string {
	return s.names
}

// Prepare enrolls the users on the inbound. It reports whether it changed st. It fails an inbound
// that could not be decoded.
func (s *Session) Prepare(st *state.State, name string, users []string) (map[string][]byte, bool, error) {
	if err := s.inbounds[name].err; err != nil {
		return nil, false, err
	}
	return nil, enroll(st, name, users), nil
}

// Apply makes the inbound hold exactly the users: it removes the users that are not wanted or
// whose account differs, adds the missing ones and logs what changed.
func (s *Session) Apply(ctx context.Context, st *state.State, name string, users []string) error {
	in := s.inbounds[name]
	if in.err != nil {
		return in.err
	}
	desired := make(map[string]*serial.TypedMessage, len(users))
	for _, user := range users {
		desired[state.EntryName(name, user)] = in.protocol.account(st.Xray[state.EntryName(name, user)])
	}

	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	response, err := s.client.GetInboundUsers(callCtx, &command.GetInboundUserRequest{Tag: name})
	if err != nil {
		return fmt.Errorf("getting users: %w", err)
	}
	current := make(map[string]*serial.TypedMessage, len(response.GetUsers()))
	for _, u := range response.GetUsers() {
		current[u.GetEmail()] = u.GetAccount()
	}

	var removed, added, updated []string
	for email, account := range current {
		want, wanted := desired[email]
		switch {
		case !wanted:
			removed = append(removed, email)
		case !sameAccount(account, want):
			updated = append(updated, email)
		}
	}
	for email := range desired {
		if _, ok := current[email]; !ok {
			added = append(added, email)
		}
	}
	slices.Sort(removed)
	slices.Sort(added)
	slices.Sort(updated)

	for _, email := range append(slices.Clone(removed), updated...) {
		if err := s.alter(ctx, name, &command.RemoveUserOperation{Email: email}); err != nil {
			return fmt.Errorf("removing user %s: %w", userOf(name, email), err)
		}
	}
	for _, email := range append(slices.Clone(added), updated...) {
		operation := &command.AddUserOperation{User: &userproto.User{
			Level: userLevel, Email: email, Account: desired[email],
		}}
		if err := s.alter(ctx, name, operation); err != nil {
			return fmt.Errorf("adding user %s: %w", userOf(name, email), err)
		}
	}

	if len(removed)+len(added)+len(updated) > 0 {
		s.log.Info("users changed", "interface", name,
			"added", userNames(name, added), "removed", userNames(name, removed), "updated", userNames(name, updated))
	}
	return nil
}

// Close closes the connection to Xray.
func (s *Session) Close() {
	s.closeConn()
}

func (s *Session) alter(ctx context.Context, tag string, operation proto.Message) error {
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	_, err := s.client.AlterInbound(callCtx, &command.AlterInboundRequest{
		Tag:       tag,
		Operation: toTypedMessage(operation),
	})
	return err
}

// sameAccount compares two accounts as messages, so field order and default values do not
// matter. An account of a type this binary does not know differs from everything.
func sameAccount(a, b *serial.TypedMessage) bool {
	if a.GetType() != b.GetType() {
		return false
	}
	messageType, err := protoregistry.GlobalTypes.FindMessageByName(protoreflect.FullName(a.GetType()))
	if err != nil {
		return false
	}
	left, right := messageType.New().Interface(), messageType.New().Interface()
	if proto.Unmarshal(a.GetValue(), left) != nil || proto.Unmarshal(b.GetValue(), right) != nil {
		return false
	}
	return proto.Equal(left, right)
}

// userOf returns the user name of an email "tag/user"; any other email is returned as it is.
func userOf(tag, email string) string {
	user, _ := strings.CutPrefix(email, tag+"/")
	return user
}

func userNames(tag string, emails []string) []string {
	names := make([]string, 0, len(emails))
	for _, email := range emails {
		names = append(names, userOf(tag, email))
	}
	return names
}
