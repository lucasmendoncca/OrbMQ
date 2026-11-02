package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/lucasmendoncca/OrbMQ/internal/broker"
	"github.com/lucasmendoncca/OrbMQ/internal/client"
	"github.com/lucasmendoncca/OrbMQ/internal/protocol"
	"github.com/lucasmendoncca/OrbMQ/internal/session"
	"github.com/lucasmendoncca/OrbMQ/internal/topic"
)

// errDisconnect is returned by serve when the client sends a DISCONNECT packet.
var errDisconnect = errors.New("client disconnected")

type Option func(*Server)

type Server struct {
	addr             string
	broker           *broker.Broker
	sessions         *session.Store
	listenFn         func(network, addr string) (net.Listener, error)
	encodePublishFn  func(pub *protocol.PublishPacket) ([]byte, error)
	handshakeTimeout time.Duration
	maxPacketSize    int
	writeTimeout     time.Duration
	connectionLimits ConnectionLimits
	connections      *connectionGroup
	clientsMu        sync.RWMutex
	clients          map[string]*activeConnection
}

type activeConnection struct {
	client         *client.Client
	session        *session.State
	cleanSession   bool
	closing        bool
	ready          bool
	disconnectedAt time.Time
	done           chan struct{}
}

// beginClosing is called under clientsMu and records the first closing event.
func (c *activeConnection) beginClosing() {
	if !c.closing {
		c.closing = true
		c.disconnectedAt = time.Now()
	}
}

type connectionGroup struct {
	mu          sync.Mutex
	connections map[net.Conn]bool
	handlers    sync.WaitGroup
	stats       Snapshot
}

type ConnectionLimits struct {
	MaxConnections int
	MaxHandshakes  int
}

func DefaultConnectionLimits() ConnectionLimits {
	return ConnectionLimits{MaxConnections: 10000, MaxHandshakes: 1000}
}

func (l ConnectionLimits) Validate() error {
	if l.MaxConnections <= 0 || l.MaxHandshakes <= 0 || l.MaxHandshakes > l.MaxConnections {
		return errors.New("invalid connection limits")
	}
	return nil
}

type Snapshot struct {
	Connections          int
	Handshakes           int
	ConnectionRejections uint64
	HandshakeRejections  uint64
}

func (s *Server) Snapshot() Snapshot {
	s.connections.mu.Lock()
	defer s.connections.mu.Unlock()
	stats := s.connections.stats
	stats.Connections = len(s.connections.connections)
	return stats
}

func WithConnectionLimits(limits ConnectionLimits) Option {
	return func(s *Server) { s.connectionLimits = limits }
}

func WithWriteTimeout(timeout time.Duration) Option {
	return func(s *Server) { s.writeTimeout = timeout }
}

type publishTemplate struct {
	raw            []byte
	packetIDOffset int
}

type publishDeliveryCache struct {
	qos0 []byte
	qos1 *publishTemplate
}

var publishEncodeBufferPool = sync.Pool{
	New: func() any {
		return &bytes.Buffer{}
	},
}

func New(addr string, b *broker.Broker, sessions *session.Store, opts ...Option) *Server {
	srv := &Server{
		addr:             addr,
		broker:           b,
		sessions:         sessions,
		listenFn:         net.Listen,
		encodePublishFn:  encodePublishPacket,
		handshakeTimeout: 10 * time.Second,
		maxPacketSize:    1 << 20,
		clients:          make(map[string]*activeConnection),
		writeTimeout:     30 * time.Second,
		connectionLimits: DefaultConnectionLimits(),
		connections:      &connectionGroup{connections: make(map[net.Conn]bool)},
	}

	for _, opt := range opts {
		opt(srv)
	}

	return srv
}

func WithListener(fn func(network, addr string) (net.Listener, error)) Option {
	return func(s *Server) {
		s.listenFn = fn
	}
}

func WithPublishEncoder(fn func(pub *protocol.PublishPacket) ([]byte, error)) Option {
	return func(s *Server) {
		s.encodePublishFn = fn
	}
}

// WithHandshakeTimeout limits CONNECT parsing and the initial CONNACK write.
// The timeout must be positive.
func WithHandshakeTimeout(timeout time.Duration) Option {
	return func(s *Server) { s.handshakeTimeout = timeout }
}

// WithMaxPacketSize limits inbound packets, including the fixed header.
// The default is 1 MiB; the limit must be between 1 and protocol.MaxPacketSize.
func WithMaxPacketSize(size int) Option {
	return func(s *Server) { s.maxPacketSize = size }
}

func (s *Server) Start(ctx context.Context) error {
	if err := s.connectionLimits.Validate(); err != nil {
		return err
	}
	if s.writeTimeout <= 0 {
		return errors.New("write timeout must be positive")
	}
	if s.maxPacketSize <= 0 || s.maxPacketSize > protocol.MaxPacketSize {
		return errors.New("invalid maximum packet size")
	}
	if s.handshakeTimeout <= 0 {
		return errors.New("handshake timeout must be positive")
	}
	ln, err := s.listenFn("tcp", s.addr)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	expiryDone := s.startSessionExpiry(runCtx)
	connections := s.connections
	shutdownDone := make(chan struct{})
	context.AfterFunc(runCtx, func() {
		defer close(shutdownDone)
		_ = ln.Close()
		connections.closeAll()
	})
	defer func() {
		cancel()
		<-shutdownDone
		<-expiryDone
		connections.wait()
	}()

	log.Printf("Listening on %s", s.addr)
	return s.acceptConnections(ctx, runCtx, ln, connections)
}

func (s *Server) acceptConnections(ctx, runCtx context.Context, ln net.Listener, connections *connectionGroup) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Temporary() {
				log.Printf("accept error: %v", err)
				continue
			}
			return fmt.Errorf("accept connection: %w", err)
		}

		if !connections.start(ctx, conn, s.connectionLimits, func() { s.handleConn(runCtx, conn) }) {
			return nil
		}
	}
}

func (g *connectionGroup) start(ctx context.Context, conn net.Conn, limits ConnectionLimits, handle func()) bool {
	g.mu.Lock()
	if ctx.Err() != nil {
		g.mu.Unlock()
		_ = conn.Close()
		return false
	}
	if len(g.connections) >= limits.MaxConnections || g.stats.Handshakes >= limits.MaxHandshakes {
		if len(g.connections) >= limits.MaxConnections {
			g.stats.ConnectionRejections++
		} else {
			g.stats.HandshakeRejections++
		}
		g.mu.Unlock()
		_ = conn.Close()
		return true
	}
	g.connections[conn] = true
	g.stats.Handshakes++
	g.handlers.Add(1)
	g.mu.Unlock()
	go func() {
		defer g.handlers.Done()
		defer func() {
			g.mu.Lock()
			if g.connections[conn] {
				g.stats.Handshakes--
			}
			delete(g.connections, conn)
			g.mu.Unlock()
		}()
		handle()
	}()
	return true
}

func (g *connectionGroup) completeHandshake(conn net.Conn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.connections[conn] {
		g.connections[conn] = false
		g.stats.Handshakes--
	}
}

func (g *connectionGroup) closeAll() {
	g.mu.Lock()
	conns := make([]net.Conn, 0, len(g.connections))
	for conn := range g.connections {
		conns = append(conns, conn)
	}
	g.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

func (g *connectionGroup) wait() {
	g.handlers.Wait()
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	handshakeDeadline := time.Now().Add(s.handshakeTimeout)
	if err := conn.SetDeadline(handshakeDeadline); err != nil {
		_ = conn.Close()
		return
	}
	reader := bufio.NewReader(conn)
	connect, cli, err := s.handshake(conn, reader)
	if err != nil {
		_ = conn.Close()
		return
	}
	var owner *activeConnection
	var consumerDone <-chan struct{}
	defer func() {
		if owner != nil {
			s.clientsMu.Lock()
			owner.beginClosing()
			s.clientsMu.Unlock()
		}
		cli.Close()
		if consumerDone != nil {
			<-consumerDone
		}
		cli.Wait()
		if owner != nil {
			s.closeSession(owner)
		}
	}()

	owner, sessionPresent, err := s.acquireConnection(ctx, connect, cli)
	if err != nil {
		return
	}
	sess := owner.session
	if err := cli.SetWriteTimeout(s.writeTimeout); err != nil {
		return
	}

	log.Printf("client connected: %s (cleanSession=%v, sessionPresent=%v)",
		cli.ID(), connect.CleanSession, sessionPresent)

	if err := sendPacketWithDeadline(cli, &protocol.ConnAckPacket{
		SessionPresent: sessionPresent,
		ReturnCode:     protocol.ConnAckAccepted,
	}, handshakeDeadline); err != nil {
		log.Printf("connack error: %v", err)
		return
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		log.Printf("clear handshake deadline: %v", err)
		return
	}
	s.connections.completeHandshake(conn)

	s.clientsMu.Lock()
	owner.ready = true
	s.clientsMu.Unlock()
	done := make(chan struct{})
	consumerDone = done
	epoch := sess.BeginDelivery()
	go func() {
		defer close(done)
		s.consumeDeliveries(ctx, cli, sess, epoch)
	}()

	cleanDisconnect := false
	defer func() {
		if connect.Will != nil && !cleanDisconnect {
			s.publishWill(connect.Will)
		}
	}()

	cleanDisconnect = s.serve(ctx, conn, reader, cli, sess, connect.KeepAlive)
}

// handshake reads the first packet from reader, validates it is a CONNECT,
// and returns the parsed packet and a new Client.
func (s *Server) handshake(conn net.Conn, reader *bufio.Reader) (*protocol.ConnectPacket, *client.Client, error) {
	pkt, err := protocol.DecodeWithLimit(reader, s.maxPacketSize)
	if err != nil {
		log.Printf("decode error (CONNECT): %v", err)
		return nil, nil, err
	}

	connect, ok := pkt.(*protocol.ConnectPacket)
	if !ok {
		log.Printf("first packet is not CONNECT")
		return nil, nil, errors.New("first packet is not CONNECT")
	}

	return connect, client.New(connect.ClientID, conn), nil
}

// openSession restores a persistent session or creates a clean one.
func (s *Server) openSession(connect *protocol.ConnectPacket) (*session.State, bool, error) {
	if s.sessions.ExpireSession(connect.ClientID, time.Now()) {
		s.broker.UnsubscribeAll(connect.ClientID)
	}
	if connect.CleanSession {
		s.broker.UnsubscribeAll(connect.ClientID)
		s.sessions.Delete(connect.ClientID)
	}

	return s.sessions.GetOrCreate(connect.ClientID)
}

// acquireConnection waits for the previous owner's handlers, writer and cleanup
// before opening a session. No network I/O or waiting holds clientsMu.
func (s *Server) acquireConnection(ctx context.Context, connect *protocol.ConnectPacket, cli *client.Client) (*activeConnection, bool, error) {
	for {
		s.clientsMu.Lock()
		if err := ctx.Err(); err != nil {
			s.clientsMu.Unlock()
			return nil, false, err
		}
		previous := s.clients[cli.ID()]
		if previous == nil {
			state, present, err := s.openSession(connect)
			if err != nil {
				s.clientsMu.Unlock()
				return nil, false, err
			}
			state.MarkConnected()
			for _, sub := range state.SnapshotSubscriptions() {
				s.broker.Subscribe(sub.Filter, sub.QoS, state)
			}
			owner := &activeConnection{client: cli, session: state, cleanSession: connect.CleanSession, done: make(chan struct{})}
			s.clients[cli.ID()] = owner
			s.clientsMu.Unlock()
			return owner, present, nil
		}
		previous.beginClosing()
		s.clientsMu.Unlock()
		previous.client.Close()
		select {
		case <-previous.done:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
}

// closeSession releases only this connection's resources, after its writer exits.
func (s *Server) closeSession(owner *activeConnection) {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	id := owner.client.ID()
	if s.clients[id] == owner {
		if owner.cleanSession {
			s.sessions.Delete(id)
			s.broker.UnsubscribeAll(id)
		} else {
			owner.session.MarkDisconnectedAt(owner.disconnectedAt)
		}
		delete(s.clients, id)
	}
	close(owner.done)
}

func (s *Server) startSessionExpiry(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	limits := s.sessions.Limits()
	if limits.OfflineTTL == 0 {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(limits.CleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				s.clientsMu.Lock()
				for _, id := range s.sessions.Expire(now) {
					s.broker.UnsubscribeAll(id)
				}
				s.clientsMu.Unlock()
			}
		}
	}()
	return done
}

// serve runs the main packet loop. Returns true if the client disconnected
// cleanly (sent DISCONNECT), false on error or timeout.
func (s *Server) serve(ctx context.Context, conn net.Conn, reader *bufio.Reader, cli *client.Client, sess *session.State, keepAlive uint16) bool {
	var timeout time.Duration
	if keepAlive > 0 {
		timeout = time.Duration(float64(keepAlive)*1.5) * time.Second
	}

	for {
		select {
		case <-ctx.Done():
			return false
		default:
			if timeout > 0 {
				conn.SetReadDeadline(time.Now().Add(timeout))
			}

			pkt, err := protocol.DecodeWithLimit(reader, s.maxPacketSize)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					log.Printf("client %s keep-alive timeout", cli.ID())
				} else {
					log.Printf("decode error: %v", err)
				}
				return false
			}

			if err := s.handlePacket(cli, sess, pkt); err != nil {
				if !errors.Is(err, errDisconnect) {
					log.Printf("packet error: %v", err)
				}
				return errors.Is(err, errDisconnect)
			}
		}
	}
}

// handlePacket dispatches a decoded packet to the appropriate handler.
func (s *Server) handlePacket(cli *client.Client, sess *session.State, pkt protocol.Packet) error {
	switch p := pkt.(type) {
	case *protocol.PingReqPacket:
		return s.handlePing(cli)
	case *protocol.PubAckPacket:
		return s.handlePubAck(sess, p)
	case *protocol.SubscribePacket:
		return s.handleSubscribe(cli, sess, p)
	case *protocol.UnsubscribePacket:
		return s.handleUnsubscribe(cli, sess, p)
	case *protocol.PublishPacket:
		return s.handlePublish(cli, p)
	case *protocol.DisconnectPacket:
		log.Printf("client %s sent DISCONNECT", cli.ID())
		return errDisconnect
	default:
		return errors.New("unsupported packet type")
	}
}

func (s *Server) publishWill(will *protocol.WillMessage) {
	_ = s.publishMessage(&protocol.PublishPacket{
		Topic:   will.Topic,
		Payload: will.Payload,
		Retain:  will.Retain,
		QoS:     will.QoS,
	})
}

func sendPacket(cli *client.Client, pkt protocol.Packet) error {
	return sendPacketWithDeadline(cli, pkt, time.Time{})
}

func sendPacketWithDeadline(cli *client.Client, pkt protocol.Packet, deadline time.Time) error {
	var buf bytes.Buffer
	if err := protocol.Encode(&buf, pkt); err != nil {
		return err
	}
	return cli.SendWithDeadline(buf.Bytes(), deadline)
}

func (s *Server) handlePing(cli *client.Client) error {
	return sendPacket(cli, &protocol.PingRespPacket{})
}

func (s *Server) handlePubAck(sess *session.State, p *protocol.PubAckPacket) error {
	sess.Ack(p.PacketID)
	return nil
}

func (s *Server) handleSubscribe(cli *client.Client, sess *session.State, p *protocol.SubscribePacket) error {
	// Pause selection until SUBACK and retained admission have finished.
	sess.PauseDelivery()
	defer sess.ResumeDelivery()
	codes := make([]byte, len(p.Subscriptions))
	for i, sub := range p.Subscriptions {
		qos := sub.QoS
		if qos > 1 {
			qos = 1
		}
		if err := sess.SetSubscription(sub.Topic, qos); err != nil {
			codes[i] = 0x80
			continue
		}
		s.broker.Subscribe(sub.Topic, qos, sess)
		codes[i] = qos
	}
	if err := sendPacket(cli, &protocol.SubAckPacket{PacketID: p.PacketID, ReturnCodes: codes}); err != nil {
		return err
	}
	// Process one filter at a time after SUBACK instead of accumulating copies of
	// every retained match across all requested filters before writing anything.
	for i, sub := range p.Subscriptions {
		if codes[i] == 0x80 {
			continue
		}
		for _, retained := range s.broker.RetainedMatching(sub.Topic) {
			outbound := newOutboundPublish(retained, deliveryQoS(retained.QoS, codes[i]))
			if outbound.QoS == 1 {
				template, err := newPublishTemplate(s.encodePublishFn, outbound)
				if err != nil {
					return err
				}
				if _, err := sess.Admit(template.raw, template.packetIDOffset); err != nil {
					return err
				}
			} else {
				raw, err := s.encodePublishFn(outbound)
				if err != nil {
					return err
				}
				if err := cli.Enqueue(raw); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func (s *Server) handleUnsubscribe(cli *client.Client, sess *session.State, p *protocol.UnsubscribePacket) error {
	for _, filter := range p.Topics {
		s.broker.Unsubscribe(filter, cli.ID())
		sess.DeleteSubscription(filter)
	}

	return sendPacket(cli, &protocol.UnsubAckPacket{PacketID: p.PacketID})
}

func (s *Server) handlePublish(cli *client.Client, p *protocol.PublishPacket) error {
	if err := s.publishMessage(p); err != nil {
		return err
	}

	if p.QoS == 1 {
		return sendPacket(cli, &protocol.PubAckPacket{PacketID: p.PacketID})
	}

	return nil
}

func (s *Server) publishMessage(p *protocol.PublishPacket) error {
	// Keep snapshot admission within one session ownership lifetime.
	// Concurrent publishers share this read lock; takeover and cleanup are writers.
	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()
	cache := &publishDeliveryCache{}
	var publishErr error

	err := s.broker.Publish(p, func(sub topic.Subscription) error {
		err := s.deliverPublishToSubscriber(p, sub, cache)
		if err != nil {
			publishErr = err
		}
		return err
	})
	if err != nil {
		return err
	}

	return publishErr
}

func (s *Server) consumeDeliveries(ctx context.Context, cli *client.Client, sess *session.State, epoch uint64) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-cli.Done():
			return
		default:
		}
		delivery, ok := sess.Next(epoch)
		if ok {
			if err := cli.Send(delivery.Data); err != nil {
				log.Printf("delivery write error: %v", err)
				cli.Close()
				return
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-cli.Done():
			return
		case <-sess.Changed():
		}
	}
}

func (s *Server) deliverPublishToSubscriber(pub *protocol.PublishPacket, sub topic.Subscription, cache *publishDeliveryCache) error {
	// The registry read lock keeps session replacement outside this admission.
	state, found := s.sessions.Get(sub.ClientID)
	if !found || sub.Subscriber != state {
		return nil
	}
	qos := deliveryQoS(pub.QoS, sub.QoS)
	outbound := newOutboundPublish(pub, qos)
	if qos == 0 {
		owner := s.clients[sub.ClientID]
		if owner == nil || owner.closing || !owner.ready || owner.session != state {
			return nil
		}
		raw, err := cache.qos0Payload(s.encodePublishFn, outbound)
		if err != nil {
			return err
		}
		if err := owner.client.Enqueue(raw); err != nil {
			owner.client.Close()
		}
		return nil
	}
	template, err := cache.qos1Template(s.encodePublishFn, outbound)
	if err != nil {
		return err
	}
	_, err = state.Admit(template.raw, template.packetIDOffset)
	return err
}
func deliveryQoS(publishQoS byte, subscriptionQoS byte) byte {
	if subscriptionQoS < publishQoS {
		return subscriptionQoS
	}

	return publishQoS
}

func newOutboundPublish(pub *protocol.PublishPacket, qos byte) *protocol.PublishPacket {
	return &protocol.PublishPacket{
		Topic:   pub.Topic,
		Payload: pub.Payload,
		Retain:  pub.Retain,
		QoS:     qos,
	}
}

func encodePublishPacket(pub *protocol.PublishPacket) ([]byte, error) {
	buf := publishEncodeBufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer publishEncodeBufferPool.Put(buf)

	if err := protocol.EncodePublish(buf, pub); err != nil {
		return nil, err
	}

	return append([]byte(nil), buf.Bytes()...), nil
}

func newPublishTemplate(encodeFn func(pub *protocol.PublishPacket) ([]byte, error), pub *protocol.PublishPacket) (*publishTemplate, error) {
	if pub.QoS != 1 {
		return nil, errors.New("publish template requires QoS 1")
	}

	raw, err := encodeFn(&protocol.PublishPacket{
		Topic:    pub.Topic,
		Payload:  pub.Payload,
		Retain:   pub.Retain,
		QoS:      pub.QoS,
		PacketID: 1,
	})
	if err != nil {
		return nil, err
	}

	return &publishTemplate{
		raw:            raw,
		packetIDOffset: publishPacketIDOffset(pub.Topic, len(pub.Payload)),
	}, nil
}

func (c *publishDeliveryCache) qos0Payload(encodeFn func(pub *protocol.PublishPacket) ([]byte, error), pub *protocol.PublishPacket) ([]byte, error) {
	if c.qos0 != nil {
		return c.qos0, nil
	}

	raw, err := encodeFn(pub)
	if err != nil {
		return nil, err
	}

	c.qos0 = raw
	return c.qos0, nil
}

func (c *publishDeliveryCache) qos1Template(encodeFn func(pub *protocol.PublishPacket) ([]byte, error), pub *protocol.PublishPacket) (*publishTemplate, error) {
	if c.qos1 != nil {
		return c.qos1, nil
	}

	template, err := newPublishTemplate(encodeFn, pub)
	if err != nil {
		return nil, err
	}

	c.qos1 = template
	return c.qos1, nil
}

func publishPacketIDOffset(topic string, payloadLen int) int {
	remainingLength := 2 + len(topic) + 2 + payloadLen
	return 1 + encodedLengthSize(remainingLength) + 2 + len(topic)
}

func encodedLengthSize(length int) int {
	size := 0
	for {
		size++
		length /= 128
		if length == 0 {
			return size
		}
	}
}
