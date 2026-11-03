package session

import (
	"cmp"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
)

var (
	ErrSessionOwner      = errors.New("session belongs to another identity")
	ErrSessionLimit      = errors.New("session limit reached")
	ErrSubscriptionLimit = errors.New("subscription limit reached")
	ErrFilterLimit       = errors.New("filter limit reached")
)

// Store holds process-local sessions. Its mutex reserves local and global
// capacity atomically. No transport I/O runs under this mutex.
type Store struct {
	mu     sync.Mutex
	data   map[string]*State
	limits Limits
	totals Snapshot
}

type Subscription struct {
	Filter string
	QoS    byte
}

type State struct {
	store          *Store
	id             string
	owner          string
	deleted        bool
	subscriptions  map[string]byte
	entries        []*entry
	inflight       map[uint16]*entry
	nextPacketID   uint16
	epoch          uint64
	activeInflight int
	paused         bool
	changed        chan struct{}
	stats          Snapshot
	connected      bool
	offlineSince   time.Time
}

func New() *Store {
	store, _ := NewWithLimits(DefaultLimits())
	return store
}

func NewWithLimits(limits Limits) (*Store, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	return &Store{data: make(map[string]*State), limits: limits}, nil
}
func NewState() *State {
	state, _, _ := New().GetOrCreate("")
	return state
}

func (s *Store) Get(id string) (*State, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.data[id]
	return state, ok
}
func (s *Store) GetOrCreate(id string) (*State, bool, error) {
	return s.GetOrCreateOwned(id, "")
}

// GetOrCreateOwned binds new sessions to an immutable owner and refuses access
// by another identity without changing subscriptions, queues or capacity.
func (s *Store) GetOrCreateOwned(id, owner string) (*State, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state, ok := s.data[id]; ok {
		if state.owner != owner {
			return nil, false, ErrSessionOwner
		}
		return state, true, nil
	}
	if len(s.data) >= s.limits.MaxSessions {
		s.totals.SessionRejections++
		return nil, false, ErrSessionLimit
	}
	state := &State{store: s, id: id, owner: owner, subscriptions: make(map[string]byte), inflight: make(map[uint16]*entry), changed: make(chan struct{}, 1), offlineSince: time.Now()}
	s.data[id] = state
	return state, false, nil
}
func (s *Store) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteLocked(id)
}

func (s *Store) deleteLocked(id string) {
	state := s.data[id]
	if state == nil {
		return
	}
	s.totals.PendingMessages -= state.stats.PendingMessages
	s.totals.PendingBytes -= state.stats.PendingBytes
	s.totals.InflightMessages -= len(state.inflight)
	s.totals.SubscriptionCount -= len(state.subscriptions)
	state.deleted = true
	state.entries = nil
	clear(state.inflight)
	clear(state.subscriptions)
	state.stats.PendingMessages = 0
	state.stats.PendingBytes = 0
	state.notifyLocked()
	delete(s.data, id)
}
func (s *State) ID() string { return s.id }

func (s *State) Owner() string { return s.owner }

func (s *State) SetSubscription(filter string, qos byte) error {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if s.deleted {
		return errors.New("session removed")
	}
	limits := s.store.limits
	if len(filter) > limits.MaxFilterBytes || strings.Count(filter, "/") >= limits.MaxFilterLevels {
		s.store.totals.FilterRejections++
		return ErrFilterLimit
	}
	if _, exists := s.subscriptions[filter]; !exists {
		if len(s.subscriptions) >= limits.MaxSubscriptionsPerSession || s.store.totals.SubscriptionCount >= limits.MaxSubscriptionsGlobal {
			s.store.totals.SubscriptionRejections++
			return ErrSubscriptionLimit
		}
		s.store.totals.SubscriptionCount++
	}
	s.subscriptions[filter] = qos
	return nil
}
func (s *State) DeleteSubscription(filter string) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if _, exists := s.subscriptions[filter]; exists {
		delete(s.subscriptions, filter)
		s.store.totals.SubscriptionCount--
	}
}

func (s *Store) Limits() Limits { return s.limits }

func (s *State) MarkConnected() {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if !s.deleted {
		s.connected = true
		s.offlineSince = time.Time{}
	}
}

func (s *State) MarkDisconnected() {
	s.MarkDisconnectedAt(time.Now())
}

// MarkDisconnectedAt preserves when disconnection began even if joining the
// previous connection's writer took longer. Expiry only runs after this call.
func (s *State) MarkDisconnectedAt(at time.Time) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if !s.deleted && s.connected {
		s.connected = false
		s.offlineSince = at
	}
}

// Expire checks offline status and deletes under the same store lock. The server
// holds its ownership lock until the returned IDs are removed from routing.
func (s *Store) Expire(now time.Time) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.limits.OfflineTTL == 0 {
		return nil
	}
	var expired []string
	for id, state := range s.data {
		if s.expireLocked(state, now) {
			expired = append(expired, id)
		}
	}
	return expired
}

func (s *Store) expireLocked(state *State, now time.Time) bool {
	if s.limits.OfflineTTL == 0 || state == nil || state.connected || state.offlineSince.IsZero() || now.Sub(state.offlineSince) < s.limits.OfflineTTL {
		return false
	}
	s.deleteLocked(state.id)
	s.totals.ExpiredSessions++
	return true
}

// ExpireSession rechecks a reconnecting ID even between periodic sweeps.
func (s *Store) ExpireSession(id string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expireLocked(s.data[id], now)
}
func (s *State) SnapshotSubscriptions() []Subscription {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	subs := make([]Subscription, 0, len(s.subscriptions))
	for filter, qos := range s.subscriptions {
		subs = append(subs, Subscription{filter, qos})
	}
	slices.SortFunc(subs, func(a, b Subscription) int { return cmp.Compare(a.Filter, b.Filter) })
	return subs
}
