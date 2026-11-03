package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_ShouldRejectInvalidUsers_WhenIdentityConfigurationIsInvalid(t *testing.T) {
	hash, err := HashPassword("fixture-password")
	require.NoError(t, err)
	valid := User{Username: "sensor", PasswordHash: hash, ClientIDs: []string{"device"}}
	for _, users := range [][]User{
		nil, {},
		{{Username: "", PasswordHash: hash, ClientIDs: []string{"device"}}},
		{{Username: "sensor", PasswordHash: "sensitive-marker", ClientIDs: []string{"device"}}},
		{{Username: "sensor", PasswordHash: hash}},
		{{Username: "sensor", PasswordHash: hash, ClientIDs: []string{"device", "device"}}},
		{{Username: "sensor", PasswordHash: hash, ClientIDs: []string{"device+"}}},
		{{Username: "sensor", PasswordHash: hash, ClientIDs: []string{"device"}, Subscribe: []string{"devices/#/bad"}}},
		{valid, valid},
		{valid, {Username: "other", PasswordHash: hash, ClientIDs: []string{"device"}}},
	} {
		_, err := New(users)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "sensitive-marker")
		assert.NotContains(t, err.Error(), hash)
	}
}

func TestNew_ShouldAuthenticateAndAuthorize_WhenIdentityHasExplicitPermissions(t *testing.T) {
	hash, err := HashPassword("\x00\xffpassword")
	require.NoError(t, err)
	users := []User{{Username: "sensor", PasswordHash: hash, ClientIDs: []string{"sensor-42"}, Publish: []string{"devices/42/#"}, Subscribe: []string{"devices/42/commands/#"}}}
	policy, err := New(users)
	require.NoError(t, err)
	// Callers retain their input; mutating it must not alter the live policy.
	users[0].ClientIDs[0] = "other"
	users[0].Publish[0] = "#"
	users[0].Subscribe[0] = "#"
	identity, err := policy.Authenticate(context.Background(), "sensor", "\x00\xffpassword")
	require.NoError(t, err)
	assert.Equal(t, Identity("sensor"), identity)
	assert.True(t, policy.CanUseClientID(identity, "sensor-42"))
	assert.False(t, policy.CanUseClientID(identity, "other"))
	assert.True(t, policy.CanPublish(identity, "devices/42/telemetry"))
	assert.False(t, policy.CanPublish(identity, "devices/43/telemetry"))
	assert.False(t, policy.CanPublish(identity, "$SYS/status"))
	assert.True(t, policy.CanSubscribe(identity, "devices/42/commands/+"))
	assert.False(t, policy.CanSubscribe(identity, "devices/+/#"))
	assert.False(t, policy.CanPublish("unknown", "devices/42/telemetry"))
	for _, username := range []string{"sensor", "unknown"} {
		_, err := policy.Authenticate(context.Background(), username, "wrong")
		assert.ErrorIs(t, err, ErrCredentials)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = policy.Authenticate(ctx, "sensor", "\x00\xffpassword")
	assert.ErrorIs(t, err, context.Canceled)
}
