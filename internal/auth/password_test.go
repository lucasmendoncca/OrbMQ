package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashPassword_ShouldPreserveBinaryPassword_WhenGeneratingSaltedHashes(t *testing.T) {
	password := "\x00\xffsecret\r\n"
	first, err := HashPassword(password)
	require.NoError(t, err)
	second, err := HashPassword(password)
	require.NoError(t, err)
	assert.NotEqual(t, first, second)
	assert.NoError(t, VerifyPassword(first, password))
	assert.Error(t, VerifyPassword(first, "wrong"))
	assert.Error(t, VerifyPassword(first, "\x00\xffsecret"))
}

func TestVerifyPassword_ShouldRejectMalformedHashes_WhenFormatIsInvalid(t *testing.T) {
	for _, hash := range []string{"", "plaintext", "pbkdf2-sha256$1$AA==$AA==", "pbkdf2-sha256$600000$bad$bad"} {
		t.Run(hash, func(t *testing.T) { assert.Error(t, VerifyPassword(hash, "password")) })
	}
}
