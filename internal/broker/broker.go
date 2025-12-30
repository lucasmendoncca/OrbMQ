package broker

import (
	"bytes"
	"maps"
	"sync/atomic"

	"github.com/lucasmendoncca/OrbMQ/internal/protocol"
	"github.com/lucasmendoncca/OrbMQ/internal/topic"
)

type Broker struct {
	topics   atomic.Value
	retained atomic.Value
}

type retainedMessage struct {
	Topic   string
	Payload []byte
}

func New() *Broker {
	b := &Broker{}
	b.topics.Store(topic.NewTree())
	b.retained.Store(make(map[string]*retainedMessage))
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
	if pub.Retain {
		b.updateRetained(pub)
	}

	tree := b.topics.Load().(*topic.Tree)
	subs := tree.Match(pub.Topic)

	for _, sub := range subs {
		if err := sub.Enqueue(raw); err != nil {
			// TODO: metrics / disconnect slow client
		}
	}

	topic.PutSubs(subs)
}

// UnsubscribeAll removes all subscriptions for the given clientID from the broker.
// It is used by the Broker's UnsubscribeAll function to remove all subscriptions
// for a client when the client disconnects.
func (b *Broker) UnsubscribeAll(clientID string) {
	oldTree := b.topics.Load().(*topic.Tree)

	newTree := oldTree.Clone()
	newTree.UnsubscribeAll(clientID)

	b.topics.Store(newTree)
}

// SendRetained sends all retained messages matching the given filter to the
// given Subscriber. This is used by the Broker to send retained messages
// to clients when they subscribe to a topic.
func (b *Broker) SendRetained(filter string, sub topic.Subscriber) {
	retained := b.retained.Load().(map[string]*retainedMessage)

	for t, msg := range retained {
		if topic.MatchFilter(filter, t) {
			var buf bytes.Buffer
			_ = protocol.EncodePublish(&buf, msg.Topic, msg.Payload, true)
			_ = sub.Enqueue(buf.Bytes())
		}
	}
}

// updateRetained updates the broker's retained messages map with the given
// publish packet. If the packet's payload is empty, it removes the
// retained message for the given topic. Otherwise, it adds the
// retained message for the given topic if it doesn't already exist.
// If the retained message already exists, it will be overwritten with the
// new message.
func (b *Broker) updateRetained(pub *protocol.PublishPacket) {
	oldMap := b.retained.Load().(map[string]*retainedMessage)

	newMap := make(map[string]*retainedMessage, len(oldMap))
	maps.Copy(newMap, oldMap)

	if len(pub.Payload) == 0 {
		// remove retained
		delete(newMap, pub.Topic)
	} else {
		newMap[pub.Topic] = &retainedMessage{
			Topic:   pub.Topic,
			Payload: pub.Payload,
		}
	}

	b.retained.Store(newMap)
}
