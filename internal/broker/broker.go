package broker

import (
	"sync/atomic"

	"github.com/lucasmendoncca/OrbMQ/internal/protocol"
	"github.com/lucasmendoncca/OrbMQ/internal/topic"
)

type Broker struct {
	topics atomic.Value
}

func New() *Broker {
	b := &Broker{}
	b.topics.Store(topic.NewTree())
	return b
}

// Subscribe adds a client to the broker's subscription list.
// It will receive all messages published to topics that match the filter.
// The filter string is a topic name, or a topic name with a single-level or
// multi-level wildcard. For example: "foo/bar", "foo/+", "foo/#".
// A client can subscribe to multiple topics by calling Subscribe multiple times.
// If a client is already subscribed to a topic, calling Subscribe again will not
// cause the client to receive duplicate messages.
func (b *Broker) Subscribe(filter string, sub topic.Subscriber) {
	oldTree := b.topics.Load().(*topic.Tree)

	newTree := oldTree.Clone()
	newTree.Subscribe(filter, sub)

	b.topics.Store(newTree)
}

// Publish sends a message to all clients subscribed to topics that match the
// given PublishPacket's topic name.
func (b *Broker) Publish(pub *protocol.PublishPacket, raw []byte) {
	tree := b.topics.Load().(*topic.Tree)
	subs := tree.Match(pub.Topic)

	for _, sub := range subs {
		if err := sub.Enqueue(raw); err != nil {
			// TODO: metrics / disconnect slow client
		}
	}

	topic.PutSubs(subs)
}
