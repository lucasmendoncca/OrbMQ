package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_ShouldRequireExplicitSecurity_WhenOperationalConfigurationIsLoaded(t *testing.T) {
	for _, tc := range []struct {
		input string
		valid bool
	}{
		{"{}", false},
		{"security: {mode: development}", true},
		{"security: {mode: development}\nserver: {address: ':1883'}", false},
		{"security: {mode: development}\nserver: {address: 'localhost:1883'}", false},
		{"security: {mode: development}\nserver: {address: '[::1]:1883'}", true},
		{"security: {mode: development, tls: {}}", false},
		{"security: {mode: development, credentials_file: users.yaml}", false},
		{"security: {mode: secure}", false},
		{"security: {mode: other}", false},
	} {
		t.Run(tc.input, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(path, []byte(tc.input), 0600))
			_, err := Load(path)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestLoad_ShouldConfigureResourceBudgets_WhenFieldsAreProvided(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resources.yaml")
	require.NoError(t, os.WriteFile(path, []byte("security: {mode: development}\nserver:\n  max_connections: 4\n  max_handshakes: 2\n  write_timeout: 2s\nsessions:\n  max_sessions: 3\n  offline_ttl: 1h\n  cleanup_interval: 1m\nretained:\n  max_messages: 2\n  max_bytes: 100\n"), 0600))
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, 4, cfg.Server.MaxConnections)
	assert.Equal(t, 2*time.Second, cfg.Server.WriteTimeout)
	assert.Equal(t, time.Hour, cfg.Sessions.OfflineTTL)
	assert.Equal(t, 100, cfg.Retained.MaxBytes)
}

func TestLoad_ShouldApplyDefaultsAndOverrides_WhenFileIsValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom.yaml")
	require.NoError(t, os.WriteFile(path, []byte("security: {mode: development}\nserver:\n  address: '127.0.0.1:1884'\n  handshake_timeout: 2s\n"), 0600))
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:1884", cfg.Server.Address)
	assert.Equal(t, 2*time.Second, cfg.Server.HandshakeTimeout)
	assert.Equal(t, 1000, cfg.Sessions.MaxPendingMessagesPerSession)
	assert.Equal(t, 30*time.Second, cfg.Observability.SummaryInterval)
	_, err = Load(filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)
}

func TestLoad_ShouldRejectInvalidConfiguration_WhenFileCannotBeUsed(t *testing.T) {
	for _, input := range []string{
		"server:\n  max_connections: 0", "server:\n  max_connections: 1\n  max_handshakes: 2", "server:\n  max_handshakes: 0", "server:\n  write_timeout: 0s",
		"sessions:\n  max_sessions: 0", "sessions:\n  offline_ttl: -1s", "sessions:\n  cleanup_interval: 0s",
		"sessions:\n  max_subscriptions_per_session: 0", "sessions:\n  max_subscriptions_global: 1",
		"sessions:\n  max_filter_bytes: 65536", "sessions:\n  max_filter_levels: 0", "retained:\n  max_bytes: 0", "retained:\n  max_messages: 0",
		"", "null", "[]", "server: null", "server:\n  address: null", "unknown: 1",
		"server:\n  typo: 1", "server:\n  address: '127.0.0.1:1883'\n  address: '127.0.0.1:1884'",
		"security: {mode: development}\n---\n{}", "security: {mode: development}\n---", "server: [", "server:\n  address: bad",
		"server:\n  handshake_timeout: 0s", "server:\n  max_packet_size_bytes: 0",
		"server:\n  max_packet_size_bytes: 268435461", "sessions:\n  max_pending_messages_per_session: 0",
		"sessions:\n  max_pending_messages_per_session: 100001", "sessions:\n  max_pending_bytes_global: 1",
		"sessions:\n  max_inflight_messages_per_session: 1001", "observability:\n  summary_interval: -1s",
		"observability:\n  saturation_log_interval: null", "server: &server {}\nobservability: *server",
	} {
		t.Run(input, func(t *testing.T) {
			for _, section := range []string{"server:", "sessions:", "retained:", "observability:", "unknown:"} {
				if strings.HasPrefix(input, section) {
					input = "security: {mode: development}\n" + input
					break
				}
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(path, []byte(input), 0600))
			_, err := Load(path)
			require.Error(t, err)
		})
	}
}

func TestLoad_ShouldMatchDefaults_WhenProjectConfigurationIsLoaded(t *testing.T) {
	cfg, err := Load("../../config.yaml")
	require.NoError(t, err)
	expected := Defaults()
	expected.Security.Mode = "development"
	assert.Equal(t, expected, cfg)
}
