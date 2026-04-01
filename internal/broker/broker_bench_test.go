package broker

import (
	"testing"

	"github.com/lucasmendoncca/OrbMQ/internal/protocol"
	"github.com/lucasmendoncca/OrbMQ/internal/topic"
)

type mockSub struct {
	id string
}

func (m *mockSub) ID() string {
	return m.id
}

func (m *mockSub) Enqueue(_ []byte) error {
	return nil
}

func setupBroker(numSubs int, qos byte) *Broker {
	b := New()

	for i := 0; i < numSubs; i++ {
		sub := &mockSub{id: "sub-" + string(rune('a'+i))}
		b.Subscribe("sensors/+", qos, sub)
	}

	return b
}

func benchmarkBrokerPublish(b *testing.B, numSubs int, qos byte) {
	broker := setupBroker(numSubs, qos)

	pub := &protocol.PublishPacket{
		Topic:   "sensors/temp",
		Payload: []byte("25.3"),
		QoS:     qos,
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		broker.Publish(pub, func(sub topic.Subscription) error {
			return sub.Subscriber.Enqueue([]byte("fake-mqtt-publish"))
		})
	}
}

func BenchmarkBrokerPublish_QoS0(b *testing.B) {
	benchmarkBrokerPublish(b, 1, 0)
}

func BenchmarkBrokerPublish_QoS1(b *testing.B) {
	benchmarkBrokerPublish(b, 1, 1)
}

func BenchmarkBrokerPublish_10Subs_QoS0(b *testing.B) {
	benchmarkBrokerPublish(b, 10, 0)
}

func BenchmarkBrokerPublish_10Subs_QoS1(b *testing.B) {
	benchmarkBrokerPublish(b, 10, 1)
}
