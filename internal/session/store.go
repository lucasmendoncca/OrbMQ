package session

import (
	"cmp"
	"slices"
	"sync"
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
	state, _ := New().GetOrCreate("")
	return state
}

func (s *Store) Get(id string) (*State, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.data[id]
	return state, ok
}
func (s *Store) GetOrCreate(id string) (*State, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state, ok := s.data[id]; ok {
		return state, true
	}
	state := &State{store: s, id: id, subscriptions: make(map[string]byte), inflight: make(map[uint16]*entry), changed: make(chan struct{}, 1)}
	s.data[id] = state
	return state, false
}
func (s *Store) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.data[id]
	if state == nil {
		return
	}
	s.totals.PendingMessages -= state.stats.PendingMessages
	s.totals.PendingBytes -= state.stats.PendingBytes
	s.totals.InflightMessages -= len(state.inflight)
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

func (s *State) SetSubscription(filter string, qos byte) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if !s.deleted {
		s.subscriptions[filter] = qos
	}
}
func (s *State) DeleteSubscription(filter string) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	delete(s.subscriptions, filter)
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
