package session

import (
	"testing"

	"github.com/lucasmendoncca/OrbMQ/internal/protocol"
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

func TestStateTrackOutbound_ShouldAssignPacketIDAndClonePayload_WhenPublishIsTracked(t *testing.T) {
	state := NewState()
	payload := []byte("payload")

	tracked, err := state.TrackOutbound(&protocol.PublishPacket{
		Topic:   "devices/temp",
		Payload: payload,
		QoS:     1,
	})

	require.NoError(t, err)
	require.NotNil(t, tracked)
	assert.Equal(t, uint16(1), tracked.PacketID)
	assert.False(t, tracked.DUP)
	assert.Equal(t, []byte("payload"), tracked.Payload)

	payload[0] = 'P'
	tracked.Payload[1] = 'A'

	pending := state.ReplayPending()
	require.Len(t, pending, 1)
	assert.Equal(t, []byte("payload"), pending[0].Payload)
	assert.True(t, pending[0].DUP)
}

func TestStateTrackOutbound_ShouldWrapPacketID_WhenCounterOverflows(t *testing.T) {
	state := NewState()
	state.nextPacketID = 65535

	tracked, err := state.TrackOutbound(&protocol.PublishPacket{
		Topic:   "devices/temp",
		Payload: []byte("payload"),
		QoS:     1,
	})

	require.NoError(t, err)
	assert.Equal(t, uint16(1), tracked.PacketID)
}

func TestStateTrackOutbound_ShouldReturnError_WhenPacketIDsAreExhausted(t *testing.T) {
	state := NewState()
	state.nextPacketID = 65535
	for packetID := 1; packetID <= 65535; packetID++ {
		state.inflight[uint16(packetID)] = &protocol.PublishPacket{
			Topic:    "devices/temp",
			Payload:  []byte("payload"),
			QoS:      1,
			PacketID: uint16(packetID),
		}
	}

	tracked, err := state.TrackOutbound(&protocol.PublishPacket{
		Topic:   "devices/temp",
		Payload: []byte("payload"),
		QoS:     1,
	})

	assert.Nil(t, tracked)
	require.ErrorIs(t, err, ErrPacketIDExhausted)
}

func TestStateAck_ShouldRemoveInflightPacket_WhenPacketIDExists(t *testing.T) {
	state := NewState()
	tracked, err := state.TrackOutbound(&protocol.PublishPacket{
		Topic:   "devices/temp",
		Payload: []byte("payload"),
		QoS:     1,
	})
	require.NoError(t, err)

	state.Ack(tracked.PacketID)

	assert.Empty(t, state.ReplayPending())
}

func TestStateReplayPending_ShouldReturnSortedDuplicatedCopies_WhenInflightMessagesExist(t *testing.T) {
	state := NewState()
	state.inflight[3] = &protocol.PublishPacket{
		Topic:    "devices/third",
		Payload:  []byte("third"),
		QoS:      1,
		PacketID: 3,
	}
	state.inflight[1] = &protocol.PublishPacket{
		Topic:    "devices/first",
		Payload:  []byte("first"),
		QoS:      1,
		PacketID: 1,
	}
	state.inflight[2] = &protocol.PublishPacket{
		Topic:    "devices/second",
		Payload:  []byte("second"),
		QoS:      1,
		PacketID: 2,
	}

	pending := state.ReplayPending()

	require.Len(t, pending, 3)
	assert.Equal(t, uint16(1), pending[0].PacketID)
	assert.Equal(t, uint16(2), pending[1].PacketID)
	assert.Equal(t, uint16(3), pending[2].PacketID)
	assert.True(t, pending[0].DUP)
	assert.True(t, pending[1].DUP)
	assert.True(t, pending[2].DUP)

	pending[0].Payload[0] = 'X'

	replayedAgain := state.ReplayPending()
	assert.Equal(t, []byte("first"), replayedAgain[0].Payload)
}
