package session

import (
	"cmp"
	"errors"
	"slices"
	"sync"

	"github.com/lucasmendoncca/OrbMQ/internal/protocol"
)

var ErrPacketIDExhausted = errors.New("no packet identifiers available")

// Store persists session state per client.
type Store struct {
	mu   sync.RWMutex
	data map[string]*State
}

type Subscription struct {
	Filter string
	QoS    byte
}

type State struct {
	mu            sync.Mutex
	subscriptions map[string]byte
	inflight      map[uint16]*protocol.PublishPacket
	nextPacketID  uint16
}

func New() *Store {
	return &Store{
		data: make(map[string]*State),
	}
}

func NewState() *State {
	return &State{
		subscriptions: make(map[string]byte),
		inflight:      make(map[uint16]*protocol.PublishPacket),
	}
}

// Get returns the persisted session for the given client.
func (s *Store) Get(clientID string) (*State, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	state, ok := s.data[clientID]
	return state, ok
}

// GetOrCreate returns the existing session for the given client or creates a new one.
func (s *Store) GetOrCreate(clientID string) (*State, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.data[clientID]
	if ok {
		return state, true
	}

	state = NewState()
	s.data[clientID] = state
	return state, false
}

// Delete removes the persisted session for the given client.
func (s *Store) Delete(clientID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, clientID)
}

func (s *State) SetSubscription(filter string, qos byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscriptions[filter] = qos
}

func (s *State) DeleteSubscription(filter string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subscriptions, filter)
}

func (s *State) SnapshotSubscriptions() []Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()

	subs := make([]Subscription, 0, len(s.subscriptions))
	for filter, qos := range s.subscriptions {
		subs = append(subs, Subscription{
			Filter: filter,
			QoS:    qos,
		})
	}

	slices.SortFunc(subs, func(a, b Subscription) int {
		return cmp.Compare(a.Filter, b.Filter)
	})

	return subs
}

func (s *State) TrackOutbound(pub *protocol.PublishPacket) (*protocol.PublishPacket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	packetID, ok := s.nextAvailablePacketIDLocked()
	if !ok {
		return nil, ErrPacketIDExhausted
	}

	cp := clonePublish(pub)
	cp.PacketID = packetID
	cp.DUP = false
	s.inflight[packetID] = clonePublish(cp)

	return cp, nil
}

func (s *State) Ack(packetID uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inflight, packetID)
}

func (s *State) ReplayPending() []*protocol.PublishPacket {
	s.mu.Lock()
	defer s.mu.Unlock()

	packetIDs := make([]int, 0, len(s.inflight))
	for packetID := range s.inflight {
		packetIDs = append(packetIDs, int(packetID))
	}
	slices.Sort(packetIDs)

	pending := make([]*protocol.PublishPacket, 0, len(packetIDs))
	for _, packetID := range packetIDs {
		cp := clonePublish(s.inflight[uint16(packetID)])
		cp.DUP = true
		pending = append(pending, cp)
	}

	return pending
}

func (s *State) nextAvailablePacketIDLocked() (uint16, bool) {
	candidate := s.nextPacketID

	for range 65535 {
		candidate++
		if candidate == 0 {
			candidate = 1
		}
		if _, exists := s.inflight[candidate]; exists {
			continue
		}

		s.nextPacketID = candidate
		return candidate, true
	}

	return 0, false
}

func clonePublish(pub *protocol.PublishPacket) *protocol.PublishPacket {
	cp := *pub
	cp.Payload = append([]byte(nil), pub.Payload...)
	return &cp
}
