package observation

import (
	"bytes"
	"context"
	"log"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/lucasmendoncca/OrbMQ/internal/broker"
	"github.com/lucasmendoncca/OrbMQ/internal/protocol"
	"github.com/lucasmendoncca/OrbMQ/internal/server"
	"github.com/lucasmendoncca/OrbMQ/internal/session"
	"github.com/lucasmendoncca/OrbMQ/internal/topic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_ShouldAggregateSaturationAndJoin_WhenClockAdvancesAndContextStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limits := session.DefaultLimits()
		limits.MaxSessions = 2
		limits.MaxSubscriptionsPerSession, limits.MaxSubscriptionsGlobal = 1, 1
		limits.MaxFilterBytes = 3
		limits.MaxPendingMessagesPerSession = 1
		limits.MaxPendingMessagesGlobal = 1
		limits.MaxInflightMessagesPerSession = 1
		store, err := session.NewWithLimits(limits)
		require.NoError(t, err)
		state, _, sessionErr := store.GetOrCreate("client")
		require.NoError(t, sessionErr)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var output lockedBuffer
		b, err := broker.NewWithLimits(broker.RetainedLimits{MaxMessages: 1, MaxBytes: 2})
		require.NoError(t, err)
		srv := server.New(":0", b, store)
		done := make(chan struct{})
		go func() {
			defer close(done)
			Run(ctx, Sources{Sessions: store, Broker: b, Server: srv}, 30*time.Second, 10*time.Second, log.New(&output, "", 0))
		}()
		synctest.Wait()
		for range 4 {
			_, err := state.Admit([]byte{0x32, 6, 0, 1, 'a', 0, 1, 'x'}, 5)
			require.NoError(t, err)
		}
		time.Sleep(9 * time.Second)
		synctest.Wait()
		assert.Empty(t, output.String())
		time.Sleep(time.Second)
		synctest.Wait()
		assert.Contains(t, output.String(), "reason=session_limit dropped=3")
		assert.NotContains(t, output.String(), "reason=global_limit")
		other, _, sessionErr := store.GetOrCreate("other")
		require.NoError(t, sessionErr)
		_, err = other.Admit([]byte{0x32, 6, 0, 1, 'a', 0, 1, 'x'}, 5)
		require.NoError(t, err)
		_, _, err = store.GetOrCreate("rejected")
		require.ErrorIs(t, err, session.ErrSessionLimit)
		require.NoError(t, state.SetSubscription("a/#", 1))
		require.ErrorIs(t, other.SetSubscription("b/#", 1), session.ErrSubscriptionLimit)
		require.ErrorIs(t, other.SetSubscription("long", 1), session.ErrFilterLimit)
		require.NoError(t, b.Publish(&protocol.PublishPacket{Topic: "a", Payload: []byte("x"), Retain: true}, func(topic.Subscription) error { return nil }))
		require.ErrorIs(t, b.Publish(&protocol.PublishPacket{Topic: "b", Payload: []byte("x"), Retain: true}, func(topic.Subscription) error { return nil }), broker.ErrRetainedLimit)
		time.Sleep(20 * time.Second)
		synctest.Wait()
		assert.Contains(t, output.String(), "reason=global_limit dropped=1")
		assert.Contains(t, output.String(), "pending_messages=1 pending_bytes=8 inflight_messages=0")
		for _, reason := range []string{"sessions", "subscriptions", "filters", "retained"} {
			assert.Contains(t, output.String(), "resource rejection reason="+reason+" rejected=1")
		}
		assert.Contains(t, output.String(), "sessions=2 subscriptions=1 retained_messages=1 retained_bytes=2 expired_sessions=0")
		cancel()
		<-done
		before := output.String()
		time.Sleep(time.Minute)
		assert.Equal(t, before, output.String())
	})
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}
