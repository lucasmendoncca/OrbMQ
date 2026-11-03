package session

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetOrCreateOwned_ShouldPreserveSession_WhenAnotherIdentityRequestsItsID(t *testing.T) {
	store := New()
	state, present, err := store.GetOrCreateOwned("device", "alice")
	require.NoError(t, err)
	assert.False(t, present)
	require.NoError(t, state.SetSubscription("private/#", 1))
	_, _, err = store.GetOrCreateOwned("device", "bob")
	require.ErrorIs(t, err, ErrSessionOwner)
	restored, present, err := store.GetOrCreateOwned("device", "alice")
	require.NoError(t, err)
	assert.True(t, present)
	assert.Same(t, state, restored)
	assert.Equal(t, "alice", restored.Owner())
	assert.Len(t, restored.SnapshotSubscriptions(), 1)
}

func TestStoreGetOrCreate_ShouldEnforceGlobalBudgets_WhenAdmissionsRunConcurrently(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxSessions = 2
	limits.MaxSubscriptionsPerSession, limits.MaxSubscriptionsGlobal = 2, 2
	store, err := NewWithLimits(limits)
	require.NoError(t, err)
	states := make(chan *State, 32)
	var workers sync.WaitGroup
	for i := range 32 {
		workers.Go(func() {
			state, _, err := store.GetOrCreate(fmt.Sprint(i))
			if err == nil {
				states <- state
			} else {
				assert.ErrorIs(t, err, ErrSessionLimit)
			}
		})
	}
	workers.Wait()
	close(states)
	var admitted []*State
	for state := range states {
		admitted = append(admitted, state)
	}
	require.Len(t, admitted, 2)
	var subscriptions atomic.Int32
	for i := range 32 {
		workers.Go(func() {
			err := admitted[i%2].SetSubscription(fmt.Sprint(i), 1)
			if err == nil {
				subscriptions.Add(1)
			} else {
				assert.ErrorIs(t, err, ErrSubscriptionLimit)
			}
		})
	}
	workers.Wait()
	assert.Equal(t, int32(2), subscriptions.Load())
	assert.Equal(t, 2, store.Snapshot().SessionCount)
	assert.Equal(t, 2, store.Snapshot().SubscriptionCount)
	assert.Equal(t, uint64(30), store.Snapshot().SessionRejections)
	for _, state := range admitted {
		store.Delete(state.ID())
	}
	assert.Zero(t, store.Snapshot().SubscriptionCount)
}

func TestStoreExpire_ShouldNotRenewOfflineDeadline_WhenMessagesArrive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limits := DefaultLimits()
		limits.OfflineTTL = time.Hour
		store, err := NewWithLimits(limits)
		require.NoError(t, err)
		state, _, err := store.GetOrCreate("offline")
		require.NoError(t, err)
		state.MarkConnected()
		state.MarkDisconnected()
		time.Sleep(30 * time.Minute)
		_, err = state.Admit([]byte{0x32, 6, 0, 1, 'a', 0, 1, 'x'}, 5)
		require.NoError(t, err)
		time.Sleep(30 * time.Minute)
		assert.Equal(t, []string{"offline"}, store.Expire(time.Now()))
		assert.Zero(t, store.Snapshot().PendingBytes)
		_, err = state.Admit([]byte{0x32, 6, 0, 1, 'a', 0, 1, 'x'}, 5)
		require.Error(t, err)
	})
}

func TestStoreExpire_ShouldRespectReconnectAndDisabledExpiry_WhenSweeping(t *testing.T) {
	store := New()
	state, _, err := store.GetOrCreate("disabled")
	require.NoError(t, err)
	state.MarkConnected()
	state.MarkDisconnected()
	assert.Empty(t, store.Expire(time.Now().Add(365*24*time.Hour)))
	assert.False(t, store.ExpireSession("disabled", time.Now().Add(365*24*time.Hour)))
	limits := DefaultLimits()
	limits.OfflineTTL = time.Minute
	store, err = NewWithLimits(limits)
	require.NoError(t, err)
	state, _, err = store.GetOrCreate("reconnected")
	require.NoError(t, err)
	state.MarkConnected()
	state.MarkDisconnected()
	state.MarkConnected()
	assert.False(t, store.ExpireSession("reconnected", time.Now().Add(time.Hour)))
	assert.Empty(t, store.Expire(time.Now().Add(time.Hour)))
}

func TestStoreGetOrCreate_ShouldPreserveExistingSessions_WhenCapacityIsReached(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxSessions = 1
	store, err := NewWithLimits(limits)
	require.NoError(t, err)
	first, _, err := store.GetOrCreate("first")
	require.NoError(t, err)
	_, _, err = store.GetOrCreate("second")
	require.ErrorIs(t, err, ErrSessionLimit)
	again, found, err := store.GetOrCreate("first")
	require.NoError(t, err)
	require.True(t, found)
	assert.Same(t, first, again)
	store.Delete("first")
	_, _, err = store.GetOrCreate("second")
	require.NoError(t, err)
}

func TestStateSetSubscription_ShouldPreserveAcceptedFilters_WhenLimitsAreReached(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxSubscriptionsPerSession, limits.MaxSubscriptionsGlobal = 1, 1
	limits.MaxFilterBytes, limits.MaxFilterLevels = 8, 2
	store, err := NewWithLimits(limits)
	require.NoError(t, err)
	state, _, err := store.GetOrCreate("first")
	require.NoError(t, err)
	other, _, err := store.GetOrCreate("other")
	require.NoError(t, err)
	require.NoError(t, state.SetSubscription("a/+", 0))
	require.NoError(t, state.SetSubscription("a/+", 1))
	require.ErrorIs(t, state.SetSubscription("b/#", 1), ErrSubscriptionLimit)
	require.ErrorIs(t, other.SetSubscription("b/#", 1), ErrSubscriptionLimit)
	require.ErrorIs(t, other.SetSubscription("a/b/c", 1), ErrFilterLimit)
	require.ErrorIs(t, other.SetSubscription("123456789", 1), ErrFilterLimit)
	state.DeleteSubscription("a/+")
	state.DeleteSubscription("a/+")
	require.NoError(t, other.SetSubscription("b/#", 1))
	other.DeleteSubscription("b/#")
	require.NoError(t, other.SetSubscription("12345678", 1))
	store.Delete("other")
	store.Delete("other")
	assert.Zero(t, store.Snapshot().SubscriptionCount)
}

func TestStoreExpire_ShouldRemoveOnlyOfflineSessions_WhenDeadlineIsReached(t *testing.T) {
	limits := DefaultLimits()
	limits.OfflineTTL = time.Hour
	store, err := NewWithLimits(limits)
	require.NoError(t, err)
	offline, _, err := store.GetOrCreate("offline")
	require.NoError(t, err)
	online, _, err := store.GetOrCreate("online")
	require.NoError(t, err)
	online.MarkConnected()
	offline.MarkConnected()
	offline.MarkDisconnected()
	require.NoError(t, offline.SetSubscription("a/#", 1))
	_, err = offline.Admit([]byte{0x32, 6, 0, 1, 'a', 0, 1, 'x'}, 5)
	require.NoError(t, err)
	now := time.Now()
	assert.Empty(t, store.Expire(now.Add(time.Hour-time.Second)))
	assert.Equal(t, []string{"offline"}, store.Expire(now.Add(time.Hour)))
	assert.Empty(t, store.Expire(now.Add(2*time.Hour)))
	assert.Zero(t, store.Snapshot().PendingBytes)
	assert.Zero(t, store.Snapshot().SubscriptionCount)
	assert.Equal(t, uint64(1), store.Snapshot().ExpiredSessions)
	_, ok := store.Get("online")
	assert.True(t, ok)
}

func TestNew_ShouldCreateEmptyStore_WhenCalled(t *testing.T) {
	store := New()

	require.NotNil(t, store)
	assert.Empty(t, store.data)
}

func TestNewState_ShouldCreateEmptyState_WhenCalled(t *testing.T) {
	state := NewState()

	require.NotNil(t, state)
	assert.Empty(t, state.subscriptions)
	assert.Empty(t, state.inflight)
	assert.Equal(t, uint16(0), state.nextPacketID)
}

func TestStoreGet_ShouldReturnStateAndFoundFlag_WhenClientExists(t *testing.T) {
	store := New()
	want, found, sessionErr := store.GetOrCreate("client-1")
	require.NoError(t, sessionErr)
	require.False(t, found)

	got, ok := store.Get("client-1")

	require.True(t, ok)
	assert.Same(t, want, got)
}

func TestStoreGet_ShouldReturnFalse_WhenClientDoesNotExist(t *testing.T) {
	store := New()

	got, ok := store.Get("missing")

	assert.Nil(t, got)
	assert.False(t, ok)
}

func TestStoreGetOrCreate_ShouldReturnExistingState_WhenClientAlreadyExists(t *testing.T) {
	store := New()
	want, found, sessionErr := store.GetOrCreate("client-1")
	require.NoError(t, sessionErr)
	require.False(t, found)

	got, found, sessionErr := store.GetOrCreate("client-1")

	require.NoError(t, sessionErr)

	require.True(t, found)
	assert.Same(t, want, got)
}

func TestStoreDelete_ShouldRemoveState_WhenClientExists(t *testing.T) {
	store := New()
	_, _, sessionErr := store.GetOrCreate("client-1")
	require.NoError(t, sessionErr)

	store.Delete("client-1")

	got, found := store.Get("client-1")
	assert.Nil(t, got)
	assert.False(t, found)
}

func TestStateSetSubscription_ShouldStoreLatestQoS_WhenFilterAlreadyExists(t *testing.T) {
	state := NewState()

	state.SetSubscription("devices/+", 0)
	state.SetSubscription("devices/+", 1)

	assert.Equal(t, []Subscription{
		{Filter: "devices/+", QoS: 1},
	}, state.SnapshotSubscriptions())
}

func TestStateDeleteSubscription_ShouldRemoveFilter_WhenSubscriptionExists(t *testing.T) {
	state := NewState()
	state.SetSubscription("devices/+", 1)
	state.SetSubscription("alerts/#", 0)

	state.DeleteSubscription("devices/+")

	assert.Equal(t, []Subscription{
		{Filter: "alerts/#", QoS: 0},
	}, state.SnapshotSubscriptions())
}

func TestStateSnapshotSubscriptions_ShouldReturnSortedSnapshot_WhenStateHasSubscriptions(t *testing.T) {
	state := NewState()
	state.SetSubscription("zeta/#", 1)
	state.SetSubscription("alpha/+", 0)
	state.SetSubscription("beta/topic", 1)

	snapshot := state.SnapshotSubscriptions()

	assert.Equal(t, []Subscription{
		{Filter: "alpha/+", QoS: 0},
		{Filter: "beta/topic", QoS: 1},
		{Filter: "zeta/#", QoS: 1},
	}, snapshot)
}

func TestStateSnapshotSubscriptions_ShouldReturnEmptySlice_WhenStateHasNoSubscriptions(t *testing.T) {
	state := NewState()

	snapshot := state.SnapshotSubscriptions()

	assert.Empty(t, snapshot)
}
