package broker

import (
	"errors"
	"maps"
	"sync"
	"sync/atomic"

	"github.com/lucasmendoncca/OrbMQ/internal/protocol"
	"github.com/lucasmendoncca/OrbMQ/internal/topic"
)

type Broker struct {
	// Writers serialize each copy-on-write update; readers use immutable snapshots.
	topicsMu   sync.Mutex
	topics     atomic.Value
	retainedMu sync.Mutex
	retained   atomic.Value
	limits     RetainedLimits
	stats      Snapshot
}

var ErrRetainedLimit = errors.New("retained limit reached")

type RetainedLimits struct {
	MaxMessages int `yaml:"max_messages"`
	MaxBytes    int `yaml:"max_bytes"`
}

func DefaultRetainedLimits() RetainedLimits {
	return RetainedLimits{MaxMessages: 10000, MaxBytes: 64 << 20}
}

func (l RetainedLimits) Validate() error {
	if l.MaxMessages <= 0 || l.MaxBytes <= 0 {
		return errors.New("retained limits must be positive")
	}
	return nil
}

type Snapshot struct {
	RetainedMessages   int
	RetainedBytes      int
	RetainedRejections uint64
}

func (b *Broker) Snapshot() Snapshot {
	b.retainedMu.Lock()
	defer b.retainedMu.Unlock()
	return b.stats
}

func New() *Broker {
	b, _ := NewWithLimits(DefaultRetainedLimits())
	return b
}

func NewWithLimits(limits RetainedLimits) (*Broker, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	b := &Broker{limits: limits}
	b.topics.Store(topic.NewTree())
	b.retained.Store(make(map[string]*protocol.PublishPacket))
	return b, nil
}

// Subscribe adds a client subscription to the broker.
func (b *Broker) Subscribe(filter string, qos byte, sub topic.Subscriber) {
	b.topicsMu.Lock()
	defer b.topicsMu.Unlock()

	oldTree := b.topics.Load().(*topic.Tree)

	newTree := oldTree.Clone()
	newTree.Subscribe(filter, topic.Subscription{
		ClientID:   sub.ID(),
		Subscriber: sub,
		QoS:        qos,
	})

	b.topics.Store(newTree)
}

// Publish sends a message to all clients subscribed to topics that match the
// given PublishPacket's topic name.
func (b *Broker) Publish(pub *protocol.PublishPacket, deliver func(topic.Subscription) error) error {
	if pub.Retain {
		if err := b.updateRetained(pub); err != nil {
			return err
		}
	}

	tree := b.topics.Load().(*topic.Tree)
	subs := tree.Match(pub.Topic)

	for _, sub := range subs {
		_ = deliver(sub)
	}

	topic.PutSubs(subs)
	return nil
}

// Unsubscribe removes the client's subscription for the given filter only.
func (b *Broker) Unsubscribe(filter string, clientID string) {
	b.topicsMu.Lock()
	defer b.topicsMu.Unlock()

	oldTree := b.topics.Load().(*topic.Tree)

	newTree := oldTree.Clone()
	newTree.Unsubscribe(filter, clientID)

	b.topics.Store(newTree)
}

// UnsubscribeAll removes all subscriptions for the given clientID from the broker.
func (b *Broker) UnsubscribeAll(clientID string) {
	b.topicsMu.Lock()
	defer b.topicsMu.Unlock()

	oldTree := b.topics.Load().(*topic.Tree)

	newTree := oldTree.Clone()
	newTree.UnsubscribeAll(clientID)

	b.topics.Store(newTree)
}

// RetainedMatching returns retained messages that match the given filter.
func (b *Broker) RetainedMatching(filter string) []*protocol.PublishPacket {
	retained := b.retained.Load().(map[string]*protocol.PublishPacket)
	matches := make([]*protocol.PublishPacket, 0)

	for topicName, msg := range retained {
		if !topic.MatchFilter(filter, topicName) {
			continue
		}
		cp := *msg
		cp.Payload = append([]byte(nil), msg.Payload...)
		matches = append(matches, &cp)
	}

	return matches
}

func (b *Broker) updateRetained(pub *protocol.PublishPacket) error {
	b.retainedMu.Lock()
	defer b.retainedMu.Unlock()

	oldMap := b.retained.Load().(map[string]*protocol.PublishPacket)
	oldBytes := 0
	if old := oldMap[pub.Topic]; old != nil {
		oldBytes = len(old.Topic) + len(old.Payload)
	}
	newBytes := 0
	if len(pub.Payload) > 0 {
		newBytes = len(pub.Topic) + len(pub.Payload)
		if (oldMap[pub.Topic] == nil && len(oldMap) >= b.limits.MaxMessages) || newBytes > b.limits.MaxBytes-(b.stats.RetainedBytes-oldBytes) {
			b.stats.RetainedRejections++
			return ErrRetainedLimit
		}
	}

	newMap := make(map[string]*protocol.PublishPacket, len(oldMap))
	maps.Copy(newMap, oldMap)

	if len(pub.Payload) == 0 {
		delete(newMap, pub.Topic)
	} else {
		cp := *pub
		cp.Payload = append([]byte(nil), pub.Payload...)
		cp.DUP = false
		cp.Retain = true
		newMap[pub.Topic] = &cp
	}

	b.retained.Store(newMap)
	b.stats.RetainedMessages = len(newMap)
	b.stats.RetainedBytes += newBytes - oldBytes
	return nil
}
