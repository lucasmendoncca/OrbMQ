package broker

import (
	"errors"
	"maps"
	"sync/atomic"

	"github.com/lucasmendoncca/OrbMQ/internal/client"
	"github.com/lucasmendoncca/OrbMQ/internal/protocol"
	"github.com/lucasmendoncca/OrbMQ/internal/topic"
)

type Broker struct {
	topics   atomic.Value
	retained atomic.Value
}

func New() *Broker {
	b := &Broker{}
	b.topics.Store(topic.NewTree())
	b.retained.Store(make(map[string]*protocol.PublishPacket))
	return b
}

// Subscribe adds a client subscription to the broker.
func (b *Broker) Subscribe(filter string, qos byte, sub topic.Subscriber) {
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
func (b *Broker) Publish(pub *protocol.PublishPacket, deliver func(topic.Subscription) error) {
	if pub.Retain {
		b.updateRetained(pub)
	}

	tree := b.topics.Load().(*topic.Tree)
	subs := tree.Match(pub.Topic)

	for _, sub := range subs {
		if err := deliver(sub); err != nil {
			if errors.Is(err, client.ErrClientQueueFull) {
				b.evictSlowClient(sub.Subscriber)
			}
		}
	}

	topic.PutSubs(subs)
}

// Unsubscribe removes the client's subscription for the given filter only.
func (b *Broker) Unsubscribe(filter string, clientID string) {
	oldTree := b.topics.Load().(*topic.Tree)

	newTree := oldTree.Clone()
	newTree.Unsubscribe(filter, clientID)

	b.topics.Store(newTree)
}

// UnsubscribeAll removes all subscriptions for the given clientID from the broker.
func (b *Broker) UnsubscribeAll(clientID string) {
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

func (b *Broker) updateRetained(pub *protocol.PublishPacket) {
	oldMap := b.retained.Load().(map[string]*protocol.PublishPacket)

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
}

func (b *Broker) evictSlowClient(sub topic.Subscriber) {
	id := sub.ID()

	b.UnsubscribeAll(id)

	if c, ok := sub.(interface{ Close() }); ok {
		c.Close()
	}
}
