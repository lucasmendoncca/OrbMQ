package server

import (
	"context"
	"crypto/tls"
	"errors"
	"time"

	"github.com/lucasmendoncca/OrbMQ/internal/auth"
	"github.com/lucasmendoncca/OrbMQ/internal/client"
	"github.com/lucasmendoncca/OrbMQ/internal/protocol"
)

// WithSecurity installs a fixed policy and server TLS configuration together.
// A policy requires TLS and vice versa; nil for both selects development mode.
// Changing identities or permissions requires a restart.
func WithSecurity(policy *auth.Local, config *tls.Config) Option {
	return func(s *Server) {
		s.auth = policy
		if config != nil {
			s.tlsConfig = config.Clone()
			if s.tlsConfig.MinVersion < tls.VersionTLS12 {
				s.tlsConfig.MinVersion = tls.VersionTLS12
			}
		}
	}
}

func (s *Server) authenticateConnect(ctx context.Context, connect *protocol.ConnectPacket) (auth.Identity, protocol.ConnAckReturnCode) {
	if s.auth == nil {
		return "", protocol.ConnAckAccepted
	}
	username, password := "", ""
	if connect.Username != nil {
		username = *connect.Username
	}
	if connect.Password != nil {
		password = *connect.Password
	}
	identity, err := s.auth.Authenticate(ctx, username, password)
	if errors.Is(err, auth.ErrBusy) || ctx.Err() != nil {
		return "", protocol.ConnAckServerUnavailable
	}
	if err != nil || connect.Username == nil || connect.Password == nil {
		return "", protocol.ConnAckBadCredentials
	}
	if !s.auth.CanUseClientID(identity, connect.ClientID) || (connect.Will != nil && !s.auth.CanPublish(identity, connect.Will.Topic)) {
		return "", protocol.ConnAckNotAuthorized
	}
	return identity, protocol.ConnAckAccepted
}

func (s *Server) rejectConnection(cli *client.Client, code protocol.ConnAckReturnCode, deadline time.Time) {
	s.connections.mu.Lock()
	if code == protocol.ConnAckNotAuthorized {
		s.connections.stats.AuthorizationRejections++
	} else {
		s.connections.stats.AuthenticationRejections++
	}
	s.connections.mu.Unlock()
	_ = sendPacketWithDeadline(cli, &protocol.ConnAckPacket{ReturnCode: code}, deadline)
}

func (s *Server) canPublish(identity auth.Identity, name string) bool {
	if s.auth == nil || s.auth.CanPublish(identity, name) {
		return true
	}
	s.recordAuthorizationRejection()
	return false
}

func (s *Server) canSubscribe(identity auth.Identity, filter string) bool {
	if s.auth == nil || s.auth.CanSubscribe(identity, filter) {
		return true
	}
	s.recordAuthorizationRejection()
	return false
}

func (s *Server) recordAuthorizationRejection() {
	s.connections.mu.Lock()
	s.connections.stats.AuthorizationRejections++
	s.connections.mu.Unlock()
}
