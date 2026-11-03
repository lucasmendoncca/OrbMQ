package config

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"

	"github.com/lucasmendoncca/OrbMQ/internal/auth"
)

type Security struct {
	Mode            string `yaml:"mode"`
	CredentialsFile string `yaml:"credentials_file"`
	TLS             *TLS   `yaml:"tls"`
}

type TLS struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

func (s Security) validate(address string) error {
	switch s.Mode {
	case "development":
		host, _, _ := net.SplitHostPort(address)
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("development mode requires a literal loopback address")
		}
		if s.CredentialsFile != "" || s.TLS != nil {
			return errors.New("development mode cannot contain security credentials or tls settings")
		}
	case "secure":
		if s.CredentialsFile == "" || s.TLS == nil || s.TLS.CertFile == "" || s.TLS.KeyFile == "" {
			return errors.New("secure mode requires credentials and a tls certificate and key")
		}
	default:
		return errors.New("security mode must be explicitly secure or development")
	}
	return nil
}

// Prepare loads one immutable snapshot before the listener is opened.
func (s Security) Prepare() (*auth.Local, *tls.Config, error) {
	if s.Mode == "development" {
		return nil, nil, nil
	}
	if s.Mode != "secure" || s.TLS == nil || s.CredentialsFile == "" {
		return nil, nil, errors.New("invalid security configuration")
	}
	policy, err := loadCredentials(s.CredentialsFile)
	if err != nil {
		return nil, nil, fmt.Errorf("load credentials: %w", err)
	}
	certificate, err := tls.LoadX509KeyPair(s.TLS.CertFile, s.TLS.KeyFile)
	if err != nil {
		return nil, nil, errors.New("load tls certificate and key failed")
	}
	return policy, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}, nil
}

func loadCredentials(path string) (*auth.Local, error) {
	var document struct {
		Users []struct {
			Username     string   `yaml:"username"`
			PasswordHash string   `yaml:"password_hash"`
			ClientIDs    []string `yaml:"client_ids"`
			Publish      []string `yaml:"publish"`
			Subscribe    []string `yaml:"subscribe"`
		} `yaml:"users"`
	}
	if err := loadYAML(path, &document); err != nil {
		return nil, err
	}
	users := make([]auth.User, len(document.Users))
	for i, entry := range document.Users {
		users[i] = auth.User{
			Username:     entry.Username,
			PasswordHash: entry.PasswordHash,
			ClientIDs:    entry.ClientIDs,
			Publish:      entry.Publish,
			Subscribe:    entry.Subscribe,
		}
	}
	return auth.New(users)
}
