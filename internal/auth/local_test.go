package auth

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_ShouldRejectUnsafeIdentityFiles_WhenConfigurationIsInvalid(t *testing.T) {
	hash, err := HashPassword("fixture-password")
	require.NoError(t, err)
	base := fmt.Sprintf("users:\n  - username: sensor\n    password_hash: %q\n    client_ids: [device]\n    subscribe: [devices/#]\n", hash)
	for _, input := range []string{
		"", "users: []", "users: null", "users: [", "unknown: sensitive-marker",
		base + "---\n{}", base + "extra: sensitive-marker\n", strings.Repeat("x", 65537),
		strings.Replace(base, "username: sensor", "username: null", 1),
		strings.Replace(base, "username: sensor", "username: sensor\n    username: duplicate", 1),
		strings.Replace(base, hash, "sensitive-marker", 1),
		strings.Replace(base, "[device]", "[device, device]", 1),
		strings.Replace(base, "[device]", "[]", 1),
		strings.Replace(base, "[device]", "['device+']", 1),
		strings.Replace(base, "[devices/#]", "[devices/#/bad]", 1),
		base + strings.TrimPrefix(base, "users:\n"),
		base + strings.Replace(strings.TrimPrefix(base, "users:\n"), "username: sensor", "username: other", 1),
		"users: &users []\nother: *users",
	} {
		path := filepath.Join(t.TempDir(), "users.yaml")
		require.NoError(t, os.WriteFile(path, []byte(input), 0600))
		_, err := Load(path)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "sensitive-marker")
		assert.NotContains(t, err.Error(), hash)
	}
}

func TestLoad_ShouldAuthenticateAndAuthorize_WhenIdentityHasExplicitPermissions(t *testing.T) {
	hash, err := HashPassword("\x00\xffpassword")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "users.yaml")
	require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf("users:\n  - username: sensor\n    password_hash: %q\n    client_ids: [sensor-42]\n    publish: [devices/42/#]\n    subscribe: [devices/42/commands/#]\n", hash)), 0600))
	policy, err := Load(path)
	require.NoError(t, err)
	identity, err := policy.Authenticate(context.Background(), "sensor", "\x00\xffpassword")
	require.NoError(t, err)
	assert.Equal(t, Identity("sensor"), identity)
	assert.True(t, policy.CanUseClientID(identity, "sensor-42"))
	assert.False(t, policy.CanUseClientID(identity, "other"))
	assert.True(t, policy.CanPublish(identity, "devices/42/telemetry"))
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
