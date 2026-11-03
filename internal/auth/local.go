package auth

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/lucasmendoncca/OrbMQ/internal/protocol"
	"github.com/lucasmendoncca/OrbMQ/internal/topic"
)

type Identity string

var ErrBusy = errors.New("authentication capacity reached")

// User supplies the identity and permissions used to construct a local policy.
type User struct {
	Username     string
	PasswordHash string
	ClientIDs    []string
	Publish      []string
	Subscribe    []string
}

type user struct {
	ClientIDs []string
	Publish   []string
	Subscribe []string
	hash      passwordHash
}

// Local is an immutable identity and permission snapshot. Only authentication
// admission changes at runtime; topic checks do not block or perform I/O.
type Local struct {
	users map[Identity]user
	slots chan struct{}
	dummy passwordHash
}

// New validates users and owns a private copy of their permissions.
func New(users []User) (*Local, error) {
	if len(users) == 0 {
		return nil, errors.New("credentials require at least one user")
	}
	local := &Local{users: make(map[Identity]user), slots: make(chan struct{}, runtime.GOMAXPROCS(0)), dummy: passwordHash{salt: make([]byte, 16), key: make([]byte, 32)}}
	ids := make(map[string]bool)
	for _, entry := range users {
		identity := Identity(entry.Username)
		if !validName(entry.Username) || len(entry.ClientIDs) == 0 {
			return nil, errors.New("invalid identity configuration")
		}
		if _, exists := local.users[identity]; exists {
			return nil, errors.New("duplicate username")
		}
		hash, err := parsePasswordHash(entry.PasswordHash)
		if err != nil {
			return nil, err
		}
		for _, id := range entry.ClientIDs {
			if !validName(id) || strings.ContainsAny(id, "+#") || ids[id] {
				return nil, errors.New("invalid or duplicate client id")
			}
			ids[id] = true
		}
		for _, filters := range [][]string{entry.Publish, entry.Subscribe} {
			for _, filter := range filters {
				if err := protocol.ValidateTopicFilter(filter); err != nil {
					return nil, errors.New("invalid permission filter")
				}
			}
		}
		local.users[identity] = user{
			ClientIDs: slices.Clone(entry.ClientIDs),
			Publish:   slices.Clone(entry.Publish),
			Subscribe: slices.Clone(entry.Subscribe),
			hash:      hash,
		}
	}
	return local, nil
}

func validName(value string) bool {
	return value != "" && len(value) <= 65535 && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func (l *Local) Authenticate(ctx context.Context, username, password string) (Identity, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	select {
	case l.slots <- struct{}{}:
		defer func() { <-l.slots }()
	default:
		return "", ErrBusy
	}
	identity := Identity(username)
	entry, exists := l.users[identity]
	hash := l.dummy
	if exists {
		hash = entry.hash
	}
	err := hash.verify(password)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if !exists || err != nil {
		return "", ErrCredentials
	}
	return identity, nil
}

func (l *Local) CanUseClientID(identity Identity, id string) bool {
	for _, allowed := range l.users[identity].ClientIDs {
		if allowed == id {
			return true
		}
	}
	return false
}

func (l *Local) CanPublish(identity Identity, name string) bool {
	if !validName(name) || strings.ContainsAny(name, "+#") {
		return false
	}
	for _, allowed := range l.users[identity].Publish {
		if topic.MatchFilter(allowed, name) {
			return true
		}
	}
	return false
}

func (l *Local) CanSubscribe(identity Identity, filter string) bool {
	if protocol.ValidateTopicFilter(filter) != nil {
		return false
	}
	for _, allowed := range l.users[identity].Subscribe {
		if topic.CoversFilter(allowed, filter) {
			return true
		}
	}
	return false
}
