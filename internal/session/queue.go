package session

import (
	"errors"
	"time"
)

type Limits struct {
	MaxPendingMessagesPerSession  int `yaml:"max_pending_messages_per_session"`
	MaxPendingBytesPerSession     int `yaml:"max_pending_bytes_per_session"`
	MaxPendingMessagesGlobal      int `yaml:"max_pending_messages_global"`
	MaxPendingBytesGlobal         int `yaml:"max_pending_bytes_global"`
	MaxInflightMessagesPerSession int `yaml:"max_inflight_messages_per_session"`
}

func DefaultLimits() Limits {
	return Limits{
		MaxPendingMessagesPerSession:  1000,
		MaxPendingBytesPerSession:     16 << 20,
		MaxPendingMessagesGlobal:      100000,
		MaxPendingBytesGlobal:         256 << 20,
		MaxInflightMessagesPerSession: 20,
	}
}

func (l Limits) Validate() error {
	if l.MaxPendingMessagesPerSession <= 0 || l.MaxPendingBytesPerSession <= 0 || l.MaxPendingMessagesGlobal <= 0 || l.MaxPendingBytesGlobal <= 0 || l.MaxInflightMessagesPerSession <= 0 {
		return errors.New("session limits must be positive")
	}
	if l.MaxPendingMessagesPerSession > l.MaxPendingMessagesGlobal || l.MaxPendingBytesPerSession > l.MaxPendingBytesGlobal {
		return errors.New("session limits exceed global limits")
	}
	if l.MaxInflightMessagesPerSession > 65535 || l.MaxInflightMessagesPerSession > l.MaxPendingMessagesPerSession {
		return errors.New("invalid inflight limit")
	}
	return nil
}

type DropReason string

const (
	SessionLimit DropReason = "session_limit"
	GlobalLimit  DropReason = "global_limit"
)

type Snapshot struct {
	PendingMessages   int
	PendingBytes      int
	InflightMessages  int
	SessionLimitDrops uint64
	GlobalLimitDrops  uint64
	OldestPendingAge  time.Duration
}
type entry struct {
	data           []byte
	packetIDOffset int
	packetID       uint16
	lastEpoch      uint64
	accepted       time.Time
}
type Delivery struct {
	Data     []byte
	PacketID uint16
}

// Admit reserves capacity before retaining a private copy of an encoded QoS 1
// PUBLISH. Next assigns its identifier when starting the first delivery attempt.
func (s *State) Admit(data []byte, packetIDOffset int) (DropReason, error) {
	if len(data) < 2 || data[0]>>4 != 3 || (data[0]>>1)&3 != 1 || packetIDOffset < 2 || packetIDOffset > len(data)-2 {
		return "", errors.New("invalid queued publish")
	}
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if s.deleted {
		return "", errors.New("session removed")
	}
	l := s.store.limits
	var reason DropReason
	if s.stats.PendingMessages >= l.MaxPendingMessagesPerSession || len(data) > l.MaxPendingBytesPerSession-s.stats.PendingBytes {
		reason = SessionLimit
	} else if s.store.totals.PendingMessages >= l.MaxPendingMessagesGlobal || len(data) > l.MaxPendingBytesGlobal-s.store.totals.PendingBytes {
		reason = GlobalLimit
	}
	if reason != "" {
		if reason == SessionLimit {
			s.stats.SessionLimitDrops++
			s.store.totals.SessionLimitDrops++
		} else {
			s.stats.GlobalLimitDrops++
			s.store.totals.GlobalLimitDrops++
		}
		return reason, nil
	}
	s.entries = append(s.entries, &entry{data: append([]byte(nil), data...), packetIDOffset: packetIDOffset, accepted: time.Now()})
	s.stats.PendingMessages++
	s.stats.PendingBytes += len(data)
	s.store.totals.PendingMessages++
	s.store.totals.PendingBytes += len(data)
	s.notifyLocked()
	return "", nil
}

// BeginDelivery starts a new connection lifetime after its predecessor exits.
// Epochs prevent obsolete consumers from advancing a replacement connection.
func (s *State) BeginDelivery() uint64 {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	s.epoch++
	s.activeInflight = 0
	s.notifyLocked()
	return s.epoch
}
func (s *State) PauseDelivery() {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	s.paused = true
}

func (s *State) ResumeDelivery() {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	s.paused = false
	s.notifyLocked()
}
func (s *State) Next(epoch uint64) (Delivery, bool) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if s.deleted || s.paused || epoch != s.epoch || s.activeInflight >= s.store.limits.MaxInflightMessagesPerSession {
		return Delivery{}, false
	}
	for _, message := range s.entries {
		if message.lastEpoch == epoch {
			continue
		}
		replay := message.packetID != 0
		if !replay {
			for {
				s.nextPacketID++
				if s.nextPacketID == 0 {
					s.nextPacketID++
				}
				if s.inflight[s.nextPacketID] == nil {
					break
				}
			}
			message.packetID = s.nextPacketID
			s.inflight[message.packetID] = message
			s.store.totals.InflightMessages++
		}
		message.lastEpoch = epoch
		s.activeInflight++
		data := append([]byte(nil), message.data...)
		if replay {
			data[0] |= 8
		} else {
			data[0] &^= 8
		}
		data[message.packetIDOffset] = byte(message.packetID >> 8)
		data[message.packetIDOffset+1] = byte(message.packetID)
		return Delivery{data, message.packetID}, true
	}
	return Delivery{}, false
}
func (s *State) Ack(packetID uint16) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	message := s.inflight[packetID]
	if message == nil || s.deleted {
		return
	}
	delete(s.inflight, packetID)
	if message.lastEpoch == s.epoch {
		s.activeInflight--
	}
	for i, candidate := range s.entries {
		if candidate == message {
			copy(s.entries[i:], s.entries[i+1:])
			s.entries[len(s.entries)-1] = nil
			s.entries = s.entries[:len(s.entries)-1]
			break
		}
	}
	s.stats.PendingMessages--
	s.stats.PendingBytes -= len(message.data)
	s.store.totals.PendingMessages--
	s.store.totals.PendingBytes -= len(message.data)
	s.store.totals.InflightMessages--
	s.notifyLocked()
}
func (s *State) Changed() <-chan struct{} { return s.changed }
func (s *State) notifyLocked() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}
func (s *State) snapshotLocked(now time.Time) Snapshot {
	result := s.stats
	result.InflightMessages = len(s.inflight)
	if len(s.entries) > 0 {
		result.OldestPendingAge = now.Sub(s.entries[0].accepted)
	}
	return result
}
func (s *State) Snapshot() Snapshot {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	return s.snapshotLocked(time.Now())
}
func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := s.totals
	now := time.Now()
	for _, state := range s.data {
		age := state.snapshotLocked(now).OldestPendingAge
		if age > result.OldestPendingAge {
			result.OldestPendingAge = age
		}
	}
	return result
}
