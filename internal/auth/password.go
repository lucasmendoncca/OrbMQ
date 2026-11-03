package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
)

const passwordIterations = 600000

var ErrCredentials = errors.New("invalid credentials")

type passwordHash struct {
	salt []byte
	key  []byte
}

// HashPassword preserves password bytes, including whitespace and binary data.
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	if err != nil {
		return "", err
	}
	return "pbkdf2-sha256$600000$" + base64.StdEncoding.EncodeToString(salt) + "$" + base64.StdEncoding.EncodeToString(key), nil
}

func parsePasswordHash(encoded string) (passwordHash, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" || parts[1] != "600000" {
		return passwordHash{}, errors.New("invalid password hash")
	}
	salt, saltErr := base64.StdEncoding.Strict().DecodeString(parts[2])
	key, keyErr := base64.StdEncoding.Strict().DecodeString(parts[3])
	if saltErr != nil || keyErr != nil || len(salt) != 16 || len(key) != 32 {
		return passwordHash{}, errors.New("invalid password hash")
	}
	return passwordHash{salt: salt, key: key}, nil
}

func (h passwordHash) verify(password string) error {
	key, err := pbkdf2.Key(sha256.New, password, h.salt, passwordIterations, 32)
	if err != nil || subtle.ConstantTimeCompare(key, h.key) != 1 {
		return ErrCredentials
	}
	return nil
}

func VerifyPassword(encoded, password string) error {
	hash, err := parsePasswordHash(encoded)
	if err != nil {
		return err
	}
	return hash.verify(password)
}
