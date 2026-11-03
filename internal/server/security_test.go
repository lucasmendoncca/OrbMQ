package server

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/lucasmendoncca/OrbMQ/internal/auth"
	"github.com/lucasmendoncca/OrbMQ/internal/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStart_ShouldEnforceTopicPermissions_WhenSecureClientsPublishAndSubscribe(t *testing.T) {
	policy, serverTLS, clientTLS := securityFixture(t)
	srv := startTestServer(t, WithSecurity(policy, serverTLS))
	subscriber := dialSecureClient(t, srv, clientTLS)
	defer subscriber.Close()
	_, err := subscriber.Write(encodeConnectPacket(t, secureConnect("device", false)))
	require.NoError(t, err)
	readConnAck(t, subscriber)
	var body bytes.Buffer
	body.Write([]byte{0, 1})
	for _, filter := range []string{"devices/42/commands/#", "devices/+/#"} {
		writeUTF8(t, &body, filter)
		body.WriteByte(1)
	}
	var packet bytes.Buffer
	packet.WriteByte(0x82)
	writeRemainingLength(t, &packet, body.Len())
	packet.Write(body.Bytes())
	_, err = subscriber.Write(packet.Bytes())
	require.NoError(t, err)
	assert.Equal(t, []byte{0x90, 4, 0, 1, 1, 0x80}, readPacketBytes(t, subscriber))
	publisher := dialSecureClient(t, srv, clientTLS)
	defer publisher.Close()
	_, err = publisher.Write(encodeConnectPacket(t, secureConnect("publisher", true)))
	require.NoError(t, err)
	readConnAck(t, publisher)
	writePublish(t, publisher, &protocol.PublishPacket{Topic: "devices/42/commands/start", Payload: []byte("go"), QoS: 1, PacketID: 3})
	assert.Equal(t, []byte{0x40, 2, 0, 3}, readPacketBytes(t, publisher))
	message := readPublishWithinPackets(t, subscriber, 1)
	require.NotNil(t, message)
	assert.Equal(t, "go", string(message.Payload))
	writePublish(t, publisher, &protocol.PublishPacket{Topic: "private/retained", Payload: []byte("forbidden"), Retain: true, QoS: 1, PacketID: 4})
	response := readClosedConnection(t, publisher)
	assert.Empty(t, response, "a denied QoS 1 publication must not receive PUBACK")
	assert.Empty(t, srv.srv.broker.RetainedMatching("private/#"))
	assert.Equal(t, uint64(2), srv.srv.Snapshot().AuthorizationRejections)
}

func TestStart_ShouldReplayOwnedSession_WhenAuthenticatedClientReconnects(t *testing.T) {
	policy, serverTLS, clientTLS := securityFixture(t)
	srv := startTestServer(t, WithSecurity(policy, serverTLS))
	subscriber := dialSecureClient(t, srv, clientTLS)
	_, err := subscriber.Write(encodeConnectPacket(t, secureConnect("device", false)))
	require.NoError(t, err)
	assert.False(t, readConnAck(t, subscriber))
	subscribe(t, subscriber, 1, "devices/42/commands/#", 1, []byte{1})
	publisher := dialSecureClient(t, srv, clientTLS)
	defer publisher.Close()
	_, err = publisher.Write(encodeConnectPacket(t, secureConnect("publisher", true)))
	require.NoError(t, err)
	readConnAck(t, publisher)
	writePublish(t, publisher, &protocol.PublishPacket{Topic: "devices/42/commands/start", Payload: []byte("pending"), QoS: 1, PacketID: 3})
	assert.Equal(t, []byte{0x40, 2, 0, 3}, readPacketBytes(t, publisher))
	first := readPublishWithinPackets(t, subscriber, 1)
	require.NotNil(t, first)
	require.NoError(t, subscriber.Close())
	reconnected := dialSecureClient(t, srv, clientTLS)
	defer reconnected.Close()
	_, err = reconnected.Write(encodeConnectPacket(t, secureConnect("device", false)))
	require.NoError(t, err)
	assert.True(t, readConnAck(t, reconnected))
	replayed := readPublishWithinPackets(t, reconnected, 1)
	require.NotNil(t, replayed)
	assert.True(t, replayed.DUP)
	assert.Equal(t, first.PacketID, replayed.PacketID)
	assert.Equal(t, first.Payload, replayed.Payload)
}

func TestStart_ShouldBoundTLSHandshake_WhenClientStallsOrServerStops(t *testing.T) {
	policy, serverTLS, _ := securityFixture(t)
	srv := startTestServer(t, WithSecurity(policy, serverTLS), WithHandshakeTimeout(100*time.Millisecond), WithConnectionLimits(ConnectionLimits{MaxConnections: 2, MaxHandshakes: 1}))
	stalled := openClientConn(t, srv)
	defer stalled.Close()
	require.Eventually(t, func() bool { return srv.srv.Snapshot().Handshakes == 1 }, time.Second, time.Millisecond)
	excess := openClientConn(t, srv)
	defer excess.Close()
	setDeadline(t, excess)
	_, err := excess.Read(make([]byte, 1))
	require.Error(t, err)
	assert.Equal(t, uint64(1), srv.srv.Snapshot().HandshakeRejections)
	setDeadline(t, stalled)
	_, err = stalled.Read(make([]byte, 1))
	require.Error(t, err)
	require.Eventually(t, func() bool { return srv.srv.Snapshot().Connections == 0 }, time.Second, time.Millisecond)
	last := openClientConn(t, srv)
	defer last.Close()
	require.Eventually(t, func() bool { return srv.srv.Snapshot().Handshakes == 1 }, time.Second, time.Millisecond)
	srv.cancel()
	setDeadline(t, last)
	_, err = last.Read(make([]byte, 1))
	require.Error(t, err)
}

func TestStart_ShouldAuthenticateBeforeTakeover_WhenTLSClientPresentsCredentials(t *testing.T) {
	policy, serverTLS, clientTLS := securityFixture(t)
	srv := startTestServer(t, WithSecurity(policy, serverTLS))
	legitimate := dialSecureClient(t, srv, clientTLS)
	defer legitimate.Close()
	_, err := legitimate.Write(encodeConnectPacket(t, secureConnect("device", false)))
	require.NoError(t, err)
	assert.False(t, readConnAck(t, legitimate))
	subscribe(t, legitimate, 1, "devices/42/commands/#", 1, []byte{1})
	for _, tc := range []struct {
		name, username, password, id string
		will                         *protocol.WillMessage
		code                         byte
	}{
		{"wrong password", "sensor", "wrong", "device", nil, 4},
		{"unknown user", "missing", "password", "device", nil, 4},
		{"another identity", "other", "password", "device", nil, 5},
		{"forbidden id", "sensor", "password", "other", nil, 5},
		{"forbidden will", "sensor", "password", "device", &protocol.WillMessage{Topic: "private/will"}, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := dialSecureClient(t, srv, clientTLS)
			defer conn.Close()
			opts := connectOptions{clientID: tc.id, cleanSession: true, username: &tc.username, password: &tc.password, will: tc.will}
			_, err := conn.Write(encodeConnectPacket(t, opts))
			require.NoError(t, err)
			assert.Equal(t, []byte{0x20, 2, 0, tc.code}, readPacketBytes(t, conn))
			_, err = legitimate.Write([]byte{0xc0, 0})
			require.NoError(t, err)
			assert.Equal(t, []byte{0xd0, 0}, readPacketBytes(t, legitimate))
		})
	}
	anonymous := dialSecureClient(t, srv, clientTLS)
	defer anonymous.Close()
	_, err = anonymous.Write(encodeConnectPacket(t, connectOptions{clientID: "device", cleanSession: true}))
	require.NoError(t, err)
	assert.Equal(t, []byte{0x20, 2, 0, 4}, readPacketBytes(t, anonymous))
	assert.Equal(t, uint64(3), srv.srv.Snapshot().AuthenticationRejections)
	assert.Equal(t, uint64(3), srv.srv.Snapshot().AuthorizationRejections)
}

func securityFixture(t *testing.T) (*auth.Local, *tls.Config, *tls.Config) {
	t.Helper()
	hash, err := auth.HashPassword("password")
	require.NoError(t, err)
	policy, err := auth.New([]auth.User{
		{Username: "sensor", PasswordHash: hash, ClientIDs: []string{"device", "publisher"}, Publish: []string{"devices/42/#"}, Subscribe: []string{"devices/42/commands/#"}},
		{Username: "other", PasswordHash: hash, ClientIDs: []string{"other"}, Publish: []string{"private/#"}, Subscribe: []string{"private/#"}},
	})
	require.NoError(t, err)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	certificate, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(certificate)
	return policy, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: "localhost"}
}

func dialSecureClient(t *testing.T, srv *runningServer, config *tls.Config) net.Conn {
	t.Helper()
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", srv.addr, config)
	require.NoError(t, err)
	setDeadline(t, conn)
	return conn
}

func secureConnect(id string, clean bool) connectOptions {
	username, password := "sensor", "password"
	return connectOptions{clientID: id, cleanSession: clean, username: &username, password: &password}
}

func TestStart_ShouldRequireTLS12OrNewer_WhenClientNegotiatesProtocolVersion(t *testing.T) {
	policy, serverTLS, clientTLS := securityFixture(t)
	srv := startTestServer(t, WithSecurity(policy, serverTLS))
	for _, version := range []uint16{tls.VersionTLS11, tls.VersionTLS12, tls.VersionTLS13} {
		config := clientTLS.Clone()
		config.MinVersion, config.MaxVersion = version, version
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", srv.addr, config)
		if version == tls.VersionTLS11 {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
			assert.Equal(t, version, conn.ConnectionState().Version)
		}
		if conn != nil {
			conn.Close()
		}
	}
}

func TestStart_ShouldRejectIncompleteSecurity_WhenOnlyPolicyOrTLSIsConfigured(t *testing.T) {
	policy, serverTLS, _ := securityFixture(t)
	for _, option := range []Option{WithSecurity(policy, nil), WithSecurity(nil, serverTLS)} {
		srv := newTestServer(option, WithListener(func(string, string) (net.Listener, error) {
			t.Fatal("invalid security must be rejected before listening")
			return nil, nil
		}))
		require.ErrorContains(t, srv.Start(t.Context()), "security requires both")
	}
}

func TestStart_ShouldRejectUntrustedCertificate_WhenClientCannotVerifyServer(t *testing.T) {
	policy, serverTLS, _ := securityFixture(t)
	srv := startTestServer(t, WithSecurity(policy, serverTLS))
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", srv.addr, &tls.Config{RootCAs: x509.NewCertPool(), ServerName: "localhost"})
	if conn != nil {
		conn.Close()
	}
	require.Error(t, err)
	plain := openClientConn(t, srv)
	defer plain.Close()
	setDeadline(t, plain)
	_, err = plain.Write(encodeConnectPacket(t, secureConnect("device", true)))
	require.NoError(t, err)
	response := readClosedConnection(t, plain)
	assert.Empty(t, response)
}

func TestStart_ShouldPreserveRetainedMessage_WhenAnotherIdentityAttemptsDeletion(t *testing.T) {
	policy, serverTLS, clientTLS := securityFixture(t)
	srv := startTestServer(t, WithSecurity(policy, serverTLS))
	owner := dialSecureClient(t, srv, clientTLS)
	defer owner.Close()
	username, password := "other", "password"
	_, err := owner.Write(encodeConnectPacket(t, connectOptions{clientID: "other", cleanSession: true, username: &username, password: &password}))
	require.NoError(t, err)
	readConnAck(t, owner)
	writePublish(t, owner, &protocol.PublishPacket{Topic: "private/status", Payload: []byte("original"), Retain: true, QoS: 1, PacketID: 1})
	assert.Equal(t, []byte{0x40, 2, 0, 1}, readPacketBytes(t, owner))
	attacker := dialSecureClient(t, srv, clientTLS)
	defer attacker.Close()
	_, err = attacker.Write(encodeConnectPacket(t, secureConnect("publisher", true)))
	require.NoError(t, err)
	readConnAck(t, attacker)
	writePublish(t, attacker, &protocol.PublishPacket{Topic: "private/status", Retain: true, QoS: 1, PacketID: 2})
	response := readClosedConnection(t, attacker)
	assert.Empty(t, response)
	subscribe(t, owner, 3, "private/#", 0, []byte{0})
	retained := readPublishWithinPackets(t, owner, 1)
	require.NotNil(t, retained)
	assert.Equal(t, "original", string(retained.Payload))
	assert.True(t, retained.Retain)
}

func TestStart_ShouldOmitCredentialsFromLogs_WhenAuthenticationFails(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() {
		log.SetOutput(previous)
		assert.NotContains(t, output.String(), "sensitive-marker")
		assert.NotContains(t, output.String(), "pbkdf2-sha256")
	})
	policy, serverTLS, clientTLS := securityFixture(t)
	srv := startTestServer(t, WithSecurity(policy, serverTLS))
	conn := dialSecureClient(t, srv, clientTLS)
	defer conn.Close()
	username, password := "sensitive-marker-user", "sensitive-marker-password"
	_, err := conn.Write(encodeConnectPacket(t, connectOptions{clientID: "device", cleanSession: true, username: &username, password: &password}))
	require.NoError(t, err)
	assert.Equal(t, []byte{0x20, 2, 0, 4}, readPacketBytes(t, conn))
}

func readClosedConnection(t *testing.T, conn net.Conn) []byte {
	t.Helper()
	data, err := io.ReadAll(conn)
	var timeout net.Error
	require.False(t, errors.As(err, &timeout) && timeout.Timeout(), "connection must close instead of waiting for the read deadline")
	return data
}

func TestStart_ShouldRefuseAuthenticationOverloadAndRecover_WhenSecureClientsConnectConcurrently(t *testing.T) {
	previous := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(previous) })
	policy, serverTLS, clientTLS := securityFixture(t)
	srv := startTestServer(t, WithSecurity(policy, serverTLS))
	const clients = 24
	connections := make([]net.Conn, clients)
	for i := range connections {
		connections[i] = dialSecureClient(t, srv, clientTLS)
		t.Cleanup(func() { connections[i].Close() })
	}
	type result struct {
		packet [4]byte
		err    error
	}
	results := make(chan result, clients)
	start := make(chan struct{})
	connect := encodeConnectPacket(t, secureConnect("publisher", true))
	for _, conn := range connections {
		go func() {
			<-start
			var response result
			_, response.err = conn.Write(connect)
			if response.err == nil {
				_, response.err = io.ReadFull(conn, response.packet[:])
			}
			results <- response
		}()
	}
	close(start)
	accepted, refused := 0, 0
	for range clients {
		response := <-results
		require.NoError(t, response.err)
		require.Equal(t, []byte{0x20, 2, 0}, response.packet[:3])
		switch response.packet[3] {
		case 0:
			accepted++
		case 3:
			refused++
		default:
			t.Fatalf("unexpected connack code %d", response.packet[3])
		}
	}
	assert.Positive(t, accepted)
	assert.Positive(t, refused, "concurrent CONNECT requests must exceed the single authentication slot")
	assert.Equal(t, uint64(refused), srv.srv.Snapshot().AuthenticationRejections)
	recovered := dialSecureClient(t, srv, clientTLS)
	defer recovered.Close()
	_, err := recovered.Write(encodeConnectPacket(t, secureConnect("device", true)))
	require.NoError(t, err)
	assert.False(t, readConnAck(t, recovered))
}
