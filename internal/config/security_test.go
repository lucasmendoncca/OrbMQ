package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lucasmendoncca/OrbMQ/internal/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func securityConfigFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	hash, err := auth.HashPassword("fixture-password")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "users.yaml"), []byte(fmt.Sprintf("users:\n  - username: sensor\n    password_hash: %q\n    client_ids: [device]\n    publish: [devices/#]\n    subscribe: [commands/#]\n", hash)), 0600))
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	privateKey, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "server.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "server.key"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKey}), 0600))
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("server: {address: ':8883'}\nsecurity:\n  mode: secure\n  credentials_file: users.yaml\n  tls: {cert_file: server.crt, key_file: server.key}\n"), 0600))
	return path, hash
}

func TestPrepare_ShouldLoadSecuritySnapshot_WhenConfigUsesRelativeFiles(t *testing.T) {
	path, _ := securityConfigFixture(t)
	dir := filepath.Dir(path)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "users.yaml"), cfg.Security.CredentialsFile)
	policy, transport, err := cfg.Security.Prepare()
	require.NoError(t, err)
	require.NotNil(t, policy)
	identity, err := policy.Authenticate(t.Context(), "sensor", "fixture-password")
	require.NoError(t, err)
	assert.True(t, policy.CanUseClientID(identity, "device"))
	assert.True(t, policy.CanPublish(identity, "devices/42/status"))
	assert.True(t, policy.CanSubscribe(identity, "commands/+"))
	assert.Equal(t, uint16(tls.VersionTLS12), transport.MinVersion)
	assert.Equal(t, tls.NoClientCert, transport.ClientAuth)
	assert.Len(t, transport.Certificates, 1)
	for _, file := range []string{"users.yaml", "server.crt", "server.key"} {
		filePath := filepath.Join(dir, file)
		original, err := os.ReadFile(filePath)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filePath, []byte("sensitive-marker"), 0600))
		_, _, err = cfg.Security.Prepare()
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "sensitive-marker")
		require.NoError(t, os.WriteFile(filePath, original, 0600))
	}
}

func TestPrepare_ShouldRejectUnsafeIdentityFiles_WhenConfigurationIsInvalid(t *testing.T) {
	path, hash := securityConfigFixture(t)
	cfg, err := Load(path)
	require.NoError(t, err)
	base, err := os.ReadFile(cfg.Security.CredentialsFile)
	require.NoError(t, err)
	valid := string(base)
	for i, input := range []string{
		"", "users: []", "users: null", "users: [", "unknown: sensitive-marker",
		valid + "---\n{}", valid + "extra: sensitive-marker\n", strings.Repeat("x", 65537),
		strings.Replace(valid, "username: sensor", "username: null", 1),
		strings.Replace(valid, "username: sensor", "username: sensor\n    username: duplicate", 1),
		strings.Replace(valid, hash, "sensitive-marker", 1),
		strings.Replace(valid, "[device]", "[device, device]", 1),
		strings.Replace(valid, "[device]", "[]", 1),
		strings.Replace(valid, "[device]", "['device+']", 1),
		strings.Replace(valid, "[devices/#]", "[devices/#/bad]", 1),
		valid + strings.TrimPrefix(valid, "users:\n"),
		valid + strings.Replace(strings.TrimPrefix(valid, "users:\n"), "username: sensor", "username: other", 1),
		"users: &users []\nother: *users",
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			require.NoError(t, os.WriteFile(cfg.Security.CredentialsFile, []byte(input), 0600))
			_, _, err := cfg.Security.Prepare()
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "sensitive-marker")
			assert.NotContains(t, err.Error(), hash)
		})
	}
}
