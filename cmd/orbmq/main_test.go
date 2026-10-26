package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRun_ShouldLoadCustomConfigurationAndStop_WhenContextIsCancelled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom.yaml")
	require.NoError(t, os.WriteFile(path, []byte("server:\n  address: '127.0.0.1:0'\n"), 0600))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, run(ctx, path))
}

func TestRun_ShouldReturnConfigurationError_WhenFileIsMissingOrInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.ErrorContains(t, run(context.Background(), path), "load configuration")
	require.NoError(t, os.WriteFile(path, []byte("server:\n  address: 'invalid'\n"), 0600))
	require.ErrorContains(t, run(context.Background(), path), "invalid server address")
}
