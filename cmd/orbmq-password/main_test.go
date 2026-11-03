package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/lucasmendoncca/OrbMQ/internal/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_ShouldOutputOnlyHash_WhenPasswordIsReadFromStdin(t *testing.T) {
	password := "\x00\xffsecret\r\n"
	var output bytes.Buffer
	require.NoError(t, run(strings.NewReader(password), &output))
	require.NoError(t, auth.VerifyPassword(strings.TrimSuffix(output.String(), "\n"), password))
	assert.NotContains(t, output.String(), password)
}

func TestRun_ShouldRejectOversizedPassword_WhenInputExceedsMQTTLimit(t *testing.T) {
	var output bytes.Buffer
	require.Error(t, run(strings.NewReader(strings.Repeat("x", 65536)), &output))
	assert.Empty(t, output.String())
}
