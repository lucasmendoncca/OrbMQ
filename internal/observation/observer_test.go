package observation

import (
	"bytes"
	"context"
	"log"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/lucasmendoncca/OrbMQ/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_ShouldAggregateSaturationAndJoin_WhenClockAdvancesAndContextStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limits := session.DefaultLimits()
		limits.MaxPendingMessagesPerSession = 1
		limits.MaxPendingMessagesGlobal = 1
		limits.MaxInflightMessagesPerSession = 1
		store, err := session.NewWithLimits(limits)
		require.NoError(t, err)
		state, _ := store.GetOrCreate("client")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var output lockedBuffer
		done := make(chan struct{})
		go func() { defer close(done); Run(ctx, store, 30*time.Second, 10*time.Second, log.New(&output, "", 0)) }()
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
		other, _ := store.GetOrCreate("other")
		_, err = other.Admit([]byte{0x32, 6, 0, 1, 'a', 0, 1, 'x'}, 5)
		require.NoError(t, err)
		time.Sleep(20 * time.Second)
		synctest.Wait()
		assert.Contains(t, output.String(), "reason=global_limit dropped=1")
		assert.Contains(t, output.String(), "pending_messages=1 pending_bytes=8 inflight_messages=0")
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
