package broker

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/lucasmendoncca/OrbMQ/internal/client"
	"github.com/lucasmendoncca/OrbMQ/internal/protocol"
	"github.com/lucasmendoncca/OrbMQ/internal/topic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_ShouldReturnBrokerWithoutSubscriptionsOrRetainedMessages_WhenCalled(t *testing.T) {
	broker := New()

	require.NotNil(t, broker)
	assert.Empty(t, broker.RetainedMatching("sensors/#"))
}

func TestBrokerPublish_ShouldDeliverToMatchingSubscribers_WhenTopicMatchesFilter(t *testing.T) {
	broker := New()
	matching := &stubSubscriber{id: "client-1"}
	nonMatching := &stubSubscriber{id: "client-2"}

	broker.Subscribe("sensors/+", 1, matching)
	broker.Subscribe("alerts/#", 1, nonMatching)

	var delivered []topic.Subscription
	broker.Publish(&protocol.PublishPacket{
		Topic:   "sensors/temp",
		Payload: []byte("25.3"),
		QoS:     1,
	}, func(sub topic.Subscription) error {
		delivered = append(delivered, sub)
		return nil
	})

	require.Len(t, delivered, 1)
	assert.Equal(t, matching.ID(), delivered[0].ClientID)
	assert.Same(t, matching, delivered[0].Subscriber)
	assert.Equal(t, byte(1), delivered[0].QoS)
}

func TestBrokerPublish_ShouldDeliverHighestQoSSubscription_WhenSameClientMatchesMultipleFilters(t *testing.T) {
	broker := New()
	sub := &stubSubscriber{id: "client-1"}

	broker.Subscribe("factory/+/status", 0, sub)
	broker.Subscribe("factory/line-1/#", 1, sub)

	var delivered []topic.Subscription
	broker.Publish(&protocol.PublishPacket{
		Topic:   "factory/line-1/status",
		Payload: []byte("running"),
		QoS:     1,
	}, func(subscription topic.Subscription) error {
		delivered = append(delivered, subscription)
		return nil
	})

	require.Len(t, delivered, 1)
	assert.Equal(t, byte(1), delivered[0].QoS)
}

func TestBrokerUnsubscribe_ShouldStopDeliveringMessages_WhenClientIsRemovedFromFilter(t *testing.T) {
	broker := New()
	sub := &stubSubscriber{id: "client-1"}

	broker.Subscribe("alerts/critical", 1, sub)
	broker.Unsubscribe("alerts/critical", sub.ID())

	var delivered []topic.Subscription
	broker.Publish(&protocol.PublishPacket{
		Topic:   "alerts/critical",
		Payload: []byte("fire"),
	}, func(subscription topic.Subscription) error {
		delivered = append(delivered, subscription)
		return nil
	})

	assert.Empty(t, delivered)
}

func TestBrokerUnsubscribeAll_ShouldStopDeliveringMessagesAcrossAllFilters_WhenClientIsRemoved(t *testing.T) {
	broker := New()
	sub := &stubSubscriber{id: "client-1"}

	broker.Subscribe("alerts/#", 1, sub)
	broker.Subscribe("sensors/+", 0, sub)
	broker.UnsubscribeAll(sub.ID())

	var delivered []topic.Subscription
	testCases := []*protocol.PublishPacket{
		{Topic: "alerts/critical", Payload: []byte("fire")},
		{Topic: "sensors/temp", Payload: []byte("30")},
	}

	require.NotEmpty(t, testCases)

	for _, testCase := range testCases {
		broker.Publish(testCase, func(subscription topic.Subscription) error {
			delivered = append(delivered, subscription)
			return nil
		})
	}

	assert.Empty(t, delivered)
}

func TestBrokerPublish_ShouldPreserveSubscriptionChanges_WhenWritersRunConcurrently(t *testing.T) {
	const clients = 64
	testCases := []struct {
		name   string
		remove func(*Broker, string)
		add    bool
	}{
		{name: "subscribe", add: true},
		{name: "unsubscribe", remove: func(b *Broker, id string) { b.Unsubscribe("sensors/+", id) }},
		{name: "unsubscribe all", remove: func(b *Broker, id string) { b.UnsubscribeAll(id) }},
		{name: "mixed", remove: func(b *Broker, id string) { b.UnsubscribeAll(id) }, add: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			b := New()
			for i := range clients {
				if tc.remove != nil {
					b.Subscribe("sensors/+", 1, &stubSubscriber{id: fmt.Sprintf("old-%d", i)})
				}
			}

			start := make(chan struct{})
			var writers sync.WaitGroup
			for i := range clients {
				writers.Go(func() {
					<-start
					if tc.remove != nil {
						tc.remove(b, fmt.Sprintf("old-%d", i))
					}
					if tc.add {
						b.Subscribe("sensors/+", 1, &stubSubscriber{id: fmt.Sprintf("new-%d", i)})
					}
				})
			}
			close(start)
			writers.Wait()

			var expected, delivered []string
			if tc.add {
				for i := range clients {
					expected = append(expected, fmt.Sprintf("new-%d", i))
				}
			}
			b.Publish(&protocol.PublishPacket{Topic: "sensors/temp"}, func(sub topic.Subscription) error {
				delivered = append(delivered, sub.ClientID)
				return nil
			})
			assert.ElementsMatch(t, expected, delivered)
		})
	}
}

func TestBrokerRetainedMatching_ShouldPreserveRetainedChanges_WhenWritersRunConcurrently(t *testing.T) {
	const messages = 64
	testCases := []struct {
		name   string
		remove bool
		add    bool
	}{
		{name: "add", add: true},
		{name: "delete", remove: true},
		{name: "mixed", remove: true, add: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			b := New()
			deliver := func(topic.Subscription) error { return nil }
			if tc.remove {
				for i := range messages {
					b.Publish(&protocol.PublishPacket{
						Topic: fmt.Sprintf("sensors/old-%d", i), Payload: []byte("old"), Retain: true,
					}, deliver)
				}
			}

			start := make(chan struct{})
			var writers sync.WaitGroup
			for i := range messages {
				writers.Go(func() {
					<-start
					if tc.remove {
						b.Publish(&protocol.PublishPacket{
							Topic: fmt.Sprintf("sensors/old-%d", i), Retain: true,
						}, deliver)
					}
					if tc.add {
						b.Publish(&protocol.PublishPacket{
							Topic:   fmt.Sprintf("sensors/new-%d", i),
							Payload: []byte(fmt.Sprintf("value-%d", i)),
							Retain:  true,
						}, deliver)
					}
				})
			}
			close(start)
			writers.Wait()

			expected := make(map[string]string)
			if tc.add {
				for i := range messages {
					expected[fmt.Sprintf("sensors/new-%d", i)] = fmt.Sprintf("value-%d", i)
				}
			}
			actual := make(map[string]string)
			for _, pub := range b.RetainedMatching("sensors/#") {
				actual[pub.Topic] = string(pub.Payload)
			}
			assert.Equal(t, expected, actual)
		})
	}
}

func TestBrokerRetainedMatching_ShouldReturnDefensiveCopies_WhenRetainedMessagesExist(t *testing.T) {
	broker := New()
	pub := &protocol.PublishPacket{
		Topic:   "sensors/temp",
		Payload: []byte("25.3"),
		QoS:     1,
		Retain:  true,
		DUP:     true,
	}

	broker.Publish(pub, func(topic.Subscription) error {
		return nil
	})

	matches := broker.RetainedMatching("sensors/#")
	require.Len(t, matches, 1)
	assert.Equal(t, "sensors/temp", matches[0].Topic)
	assert.Equal(t, []byte("25.3"), matches[0].Payload)
	assert.Equal(t, byte(1), matches[0].QoS)
	assert.True(t, matches[0].Retain)
	assert.False(t, matches[0].DUP)

	matches[0].Payload[0] = '9'

	fetchedAgain := broker.RetainedMatching("sensors/#")
	require.Len(t, fetchedAgain, 1)
	assert.Equal(t, []byte("25.3"), fetchedAgain[0].Payload)
}

func TestBrokerRetainedMatching_ShouldDeleteRetainedMessage_WhenRetainedPublishHasEmptyPayload(t *testing.T) {
	broker := New()

	broker.Publish(&protocol.PublishPacket{
		Topic:   "sensors/temp",
		Payload: []byte("25.3"),
		Retain:  true,
	}, func(topic.Subscription) error {
		return nil
	})
	broker.Publish(&protocol.PublishPacket{
		Topic:   "sensors/temp",
		Payload: []byte{},
		Retain:  true,
	}, func(topic.Subscription) error {
		return nil
	})

	assert.Empty(t, broker.RetainedMatching("sensors/#"))
}

func TestBrokerPublish_ShouldEvictSlowClientAndCloseSubscriber_WhenDeliverReturnsClientQueueFull(t *testing.T) {
	broker := New()
	slow := &closableStubSubscriber{id: "client-1"}
	fast := &stubSubscriber{id: "client-2"}

	broker.Subscribe("sensors/+", 1, slow)
	broker.Subscribe("sensors/+", 1, fast)

	var firstDelivered []string
	broker.Publish(&protocol.PublishPacket{
		Topic:   "sensors/temp",
		Payload: []byte("25.3"),
	}, func(subscription topic.Subscription) error {
		firstDelivered = append(firstDelivered, subscription.ClientID)
		if subscription.ClientID == slow.ID() {
			return client.ErrClientQueueFull
		}
		return nil
	})

	require.ElementsMatch(t, []string{slow.ID(), fast.ID()}, firstDelivered)
	assert.Equal(t, 1, slow.closeCount)

	var secondDelivered []string
	broker.Publish(&protocol.PublishPacket{
		Topic:   "sensors/temp",
		Payload: []byte("26.1"),
	}, func(subscription topic.Subscription) error {
		secondDelivered = append(secondDelivered, subscription.ClientID)
		return nil
	})

	assert.Equal(t, []string{fast.ID()}, secondDelivered)
}

func TestBrokerPublish_ShouldNotEvictSubscriber_WhenDeliverReturnsDifferentError(t *testing.T) {
	broker := New()
	sub := &closableStubSubscriber{id: "client-1"}

	broker.Subscribe("sensors/+", 1, sub)

	broker.Publish(&protocol.PublishPacket{
		Topic:   "sensors/temp",
		Payload: []byte("25.3"),
	}, func(topic.Subscription) error {
		return errors.New("temporary failure")
	})

	var delivered []string
	broker.Publish(&protocol.PublishPacket{
		Topic:   "sensors/temp",
		Payload: []byte("26.1"),
	}, func(subscription topic.Subscription) error {
		delivered = append(delivered, subscription.ClientID)
		return nil
	})

	assert.Equal(t, 0, sub.closeCount)
	assert.Equal(t, []string{sub.ID()}, delivered)
}

type stubSubscriber struct {
	id string
}

func (s *stubSubscriber) ID() string {
	return s.id
}

func (s *stubSubscriber) Enqueue([]byte) error {
	return nil
}

type closableStubSubscriber struct {
	id         string
	closeCount int
}

func (s *closableStubSubscriber) ID() string {
	return s.id
}

func (s *closableStubSubscriber) Enqueue([]byte) error {
	return nil
}

func (s *closableStubSubscriber) Close() {
	s.closeCount++
}
