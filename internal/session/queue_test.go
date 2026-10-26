package session

import (
	"math"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStateAdmit_ShouldEnforceByteLimitsAndPrecedence_WhenBudgetsAreFull(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxPendingBytesPerSession = 8
	limits.MaxPendingBytesGlobal = 8
	store, err := NewWithLimits(limits)
	require.NoError(t, err)
	state, _ := store.GetOrCreate("client")
	raw := []byte{0x32, 6, 0, 1, 'a', 0, 1, 'x'}
	reason, err := state.Admit(raw, 5)
	require.NoError(t, err)
	require.Empty(t, reason)
	reason, err = state.Admit(raw, 5)
	require.NoError(t, err)
	assert.Equal(t, SessionLimit, reason)
	other, _ := store.GetOrCreate("other")
	reason, err = other.Admit(raw, 5)
	require.NoError(t, err)
	assert.Equal(t, GlobalLimit, reason)
	state.Ack(60000)
	assert.Equal(t, 8, store.Snapshot().PendingBytes)
	store.Delete("client")
	store.Delete("client")
	_, err = state.Admit(raw, 5)
	require.Error(t, err)
	reason, err = other.Admit(raw, 5)
	require.NoError(t, err)
	require.Empty(t, reason)
	for _, offset := range []int{-1, 7, math.MaxInt} {
		_, err := other.Admit(raw, offset)
		require.Error(t, err)
	}
	assert.Equal(t, 1, store.Snapshot().PendingMessages)
}

func TestLimitsValidate_ShouldRejectInvalidLimits_WhenValuesAreOutsideSupportedRange(t *testing.T) {
	for _, mutate := range []func(*Limits){
		func(l *Limits) { l.MaxPendingMessagesPerSession = 0 },
		func(l *Limits) { l.MaxPendingBytesPerSession = -1 },
		func(l *Limits) { l.MaxPendingMessagesGlobal = 0 },
		func(l *Limits) { l.MaxPendingBytesGlobal = 1 },
		func(l *Limits) { l.MaxInflightMessagesPerSession = 0 },
		func(l *Limits) { l.MaxInflightMessagesPerSession = 1001 },
		func(l *Limits) { l.MaxPendingMessagesPerSession = 70000; l.MaxInflightMessagesPerSession = 65536 },
	} {
		limits := DefaultLimits()
		mutate(&limits)
		_, err := NewWithLimits(limits)
		require.Error(t, err)
	}
}

func TestStateAdmit_ShouldReserveCapacityAtomically_WhenSessionsPublishConcurrently(t *testing.T) {
	for _, byteLimit := range []bool{false, true} {
		limits := DefaultLimits()
		limits.MaxPendingMessagesPerSession = 2
		limits.MaxPendingMessagesGlobal = 2
		limits.MaxInflightMessagesPerSession = 1
		if byteLimit {
			limits.MaxPendingBytesPerSession = 8
			limits.MaxPendingBytesGlobal = 8
		}
		store, err := NewWithLimits(limits)
		require.NoError(t, err)
		var wg sync.WaitGroup
		for i := range 10 {
			state, _ := store.GetOrCreate(string(rune('a' + i)))
			wg.Go(func() { _, err := state.Admit([]byte{0x32, 6, 0, 1, 'a', 0, 1, 'x'}, 5); assert.NoError(t, err) })
		}
		wg.Wait()
		want := 2
		if byteLimit {
			want = 1
		}
		stats := store.Snapshot()
		assert.Equal(t, want, stats.PendingMessages)
		assert.Equal(t, want*8, stats.PendingBytes)
		assert.Equal(t, uint64(10-want), stats.GlobalLimitDrops)
	}
}

func TestStateNext_ShouldReplayInSendOrder_WhenPacketIdentifiersWrap(t *testing.T) {
	state := NewState()
	epoch := state.BeginDelivery()
	raw := []byte{0x32, 6, 0, 1, 'a', 0, 1, 'x'}
	for range 65534 {
		_, err := state.Admit(raw, 5)
		require.NoError(t, err)
		delivery, ok := state.Next(epoch)
		require.True(t, ok)
		state.Ack(delivery.PacketID)
	}
	for range 2 {
		_, err := state.Admit(raw, 5)
		require.NoError(t, err)
		_, ok := state.Next(epoch)
		require.True(t, ok)
	}
	epoch = state.BeginDelivery()
	_, ok := state.Next(epoch - 1)
	assert.False(t, ok)
	for _, id := range []uint16{65535, 1} {
		delivery, ok := state.Next(epoch)
		require.True(t, ok)
		assert.Equal(t, id, delivery.PacketID)
		assert.True(t, delivery.Data[0]&8 != 0)
	}
}

func TestStateSnapshot_ShouldReportAgeAndPreserveCopies_WhenTimeAdvances(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		state := NewState()
		raw := []byte{0x32, 6, 0, 1, 'a', 0, 1, 'x'}
		_, err := state.Admit(raw, 5)
		require.NoError(t, err)
		raw[7] = 'y'
		time.Sleep(3 * time.Second)
		assert.Equal(t, 3*time.Second, state.Snapshot().OldestPendingAge)
		epoch := state.BeginDelivery()
		delivery, ok := state.Next(epoch)
		require.True(t, ok)
		assert.Equal(t, byte('x'), delivery.Data[7])
		delivery.Data[7] = 'z'
		epoch = state.BeginDelivery()
		replay, ok := state.Next(epoch)
		require.True(t, ok)
		assert.Equal(t, byte('x'), replay.Data[7])
		state.Ack(replay.PacketID)
		assert.Zero(t, state.Snapshot().OldestPendingAge)
	})
}

func TestStateAdmit_ShouldPreservePendingMessages_WhenSessionLimitIsReached(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxPendingMessagesPerSession = 2
	limits.MaxInflightMessagesPerSession = 1
	store, err := NewWithLimits(limits)
	require.NoError(t, err)
	state, _ := store.GetOrCreate("client")
	raw := []byte{0x32, 6, 0, 1, 'a', 0, 1, 'x'}
	for range 2 {
		reason, err := state.Admit(raw, 5)
		require.NoError(t, err)
		require.Empty(t, reason)
	}
	reason, err := state.Admit(raw, 5)
	require.NoError(t, err)
	assert.Equal(t, SessionLimit, reason)
	epoch := state.BeginDelivery()
	first, ok := state.Next(epoch)
	require.True(t, ok)
	assert.False(t, first.Data[0]&8 != 0)
	_, ok = state.Next(epoch)
	assert.False(t, ok, "inflight window must stop further delivery")
	assert.Equal(t, 2, store.Snapshot().PendingMessages)
	state.Ack(first.PacketID)
	state.Ack(first.PacketID)
	second, ok := state.Next(epoch)
	require.True(t, ok)
	assert.NotEqual(t, first.PacketID, second.PacketID)
	assert.Equal(t, 1, state.Snapshot().PendingMessages)
	assert.Equal(t, len(raw), state.Snapshot().PendingBytes)
	epoch = state.BeginDelivery()
	replay, ok := state.Next(epoch)
	require.True(t, ok)
	assert.Equal(t, second.PacketID, replay.PacketID)
	assert.True(t, replay.Data[0]&8 != 0)
	store.Delete("client")
	assert.Zero(t, store.Snapshot().PendingMessages)
	assert.Zero(t, store.Snapshot().PendingBytes)
	state.Ack(second.PacketID)
	assert.Zero(t, store.Snapshot().PendingMessages)
}
