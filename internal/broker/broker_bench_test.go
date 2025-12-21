package broker

import (
	"testing"

	"github.com/lucasmendoncca/OrbMQ/internal/protocol"
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

func setupBroker(numSubs int) *Broker {
	b := New()

	sub := &mockSub{id: "sub-1"}

	for i := 0; i < numSubs; i++ {
		b.Subscribe("sensors/+", sub)
	}

	return b
}

func BenchmarkBrokerPublish(b *testing.B) {
	broker := setupBroker(1)

	pub := &protocol.PublishPacket{
		Topic:   "sensors/temp",
		Payload: []byte("25.3"),
	}

	raw := []byte("fake-mqtt-publish")

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		broker.Publish(pub, raw)
	}
}

func BenchmarkBrokerPublish_10Subs(b *testing.B) {
	broker := setupBroker(10)

	pub := &protocol.PublishPacket{
		Topic:   "sensors/temp",
		Payload: []byte("25.3"),
	}

	raw := []byte("fake-mqtt-publish")

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		broker.Publish(pub, raw)
	}
}

func BenchmarkBrokerPublish_Parallel(b *testing.B) {
	broker := setupBroker(10)

	pub := &protocol.PublishPacket{
		Topic:   "sensors/temp",
		Payload: []byte("25.3"),
	}

	raw := []byte("fake-mqtt-publish")

	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			broker.Publish(pub, raw)
		}
	})
}
