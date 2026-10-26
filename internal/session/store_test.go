package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	want, found := store.GetOrCreate("client-1")
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
	want, found := store.GetOrCreate("client-1")
	require.False(t, found)

	got, found := store.GetOrCreate("client-1")

	require.True(t, found)
	assert.Same(t, want, got)
}

func TestStoreDelete_ShouldRemoveState_WhenClientExists(t *testing.T) {
	store := New()
	_, _ = store.GetOrCreate("client-1")

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
