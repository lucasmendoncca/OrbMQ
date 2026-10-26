package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_ShouldApplyDefaultsAndOverrides_WhenFileIsValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom.yaml")
	require.NoError(t, os.WriteFile(path, []byte("server:\n  address: '127.0.0.1:1884'\n  handshake_timeout: 2s\n"), 0600))
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
		"", "null", "[]", "server: null", "server:\n  address: null", "unknown: 1",
		"server:\n  typo: 1", "server:\n  address: ':1883'\n  address: ':1884'",
		"{}\n---\n{}", "{}\n---", "server: [", "server:\n  address: bad",
		"server:\n  handshake_timeout: 0s", "server:\n  max_packet_size_bytes: 0",
		"server:\n  max_packet_size_bytes: 268435461", "sessions:\n  max_pending_messages_per_session: 0",
		"sessions:\n  max_pending_messages_per_session: 100001", "sessions:\n  max_pending_bytes_global: 1",
		"sessions:\n  max_inflight_messages_per_session: 1001", "observability:\n  summary_interval: -1s",
		"observability:\n  saturation_log_interval: null", "server: &server {}\nobservability: *server",
	} {
		t.Run(input, func(t *testing.T) {
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
	assert.Equal(t, Defaults(), cfg)
}
