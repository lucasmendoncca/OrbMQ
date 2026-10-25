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
	clientsMu        sync.RWMutex
	clients          map[string]*activeConnection
}

type activeConnection struct {
	client       *client.Client
	session      *session.State
	cleanSession bool
	closing      bool
	done         chan struct{}
}

type connectionGroup struct {
	mu          sync.Mutex
	connections map[net.Conn]struct{}
	handlers    sync.WaitGroup
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
	connections := &connectionGroup{connections: make(map[net.Conn]struct{})}
	shutdownDone := make(chan struct{})
	context.AfterFunc(runCtx, func() {
		defer close(shutdownDone)
		_ = ln.Close()
		connections.closeAll()
	})
	defer func() {
		cancel()
		<-shutdownDone
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

		if !connections.start(ctx, conn, func() { s.handleConn(runCtx, conn) }) {
			return nil
		}
	}
}

func (g *connectionGroup) start(ctx context.Context, conn net.Conn, handle func()) bool {
	g.mu.Lock()
	if ctx.Err() != nil {
		g.mu.Unlock()
		_ = conn.Close()
		return false
	}
	g.connections[conn] = struct{}{}
	g.handlers.Add(1)
	g.mu.Unlock()
	go func() {
		defer g.handlers.Done()
		defer func() {
			g.mu.Lock()
			delete(g.connections, conn)
			g.mu.Unlock()
		}()
		handle()
	}()
	return true
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
	if err := conn.SetDeadline(time.Now().Add(s.handshakeTimeout)); err != nil {
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
	defer func() {
		if owner != nil {
			s.clientsMu.Lock()
			owner.closing = true
			s.clientsMu.Unlock()
		}
		cli.Close()
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

	log.Printf("client connected: %s (cleanSession=%v, sessionPresent=%v)",
		cli.ID(), connect.CleanSession, sessionPresent)

	if err := sendPacket(cli, &protocol.ConnAckPacket{
		SessionPresent: sessionPresent,
		ReturnCode:     protocol.ConnAckAccepted,
	}); err != nil {
		log.Printf("connack error: %v", err)
		return
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		log.Printf("clear handshake deadline: %v", err)
		return
	}

	if err := s.replayInflight(cli, sess); err != nil {
		log.Printf("replay inflight error: %v", err)
		return
	}
	// Make live deliveries visible only after CONNACK and replay are queued.
	for _, sub := range sess.SnapshotSubscriptions() {
		s.broker.Subscribe(sub.Filter, sub.QoS, cli)
	}

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
func (s *Server) openSession(connect *protocol.ConnectPacket) (*session.State, bool) {
	if connect.CleanSession {
		s.sessions.Delete(connect.ClientID)
		state, _ := s.sessions.GetOrCreate(connect.ClientID)
		return state, false
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
			state, present := s.openSession(connect)
			owner := &activeConnection{client: cli, session: state, cleanSession: connect.CleanSession, done: make(chan struct{})}
			s.clients[cli.ID()] = owner
			s.clientsMu.Unlock()
			return owner, present, nil
		}
		previous.closing = true
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
		}
		s.broker.UnsubscribeAll(id)
		delete(s.clients, id)
	}
	close(owner.done)
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
	var buf bytes.Buffer
	if err := protocol.Encode(&buf, pkt); err != nil {
		return err
	}
	return cli.Send(buf.Bytes())
}

func (s *Server) handlePing(cli *client.Client) error {
	return sendPacket(cli, &protocol.PingRespPacket{})
}

func (s *Server) handlePubAck(sess *session.State, p *protocol.PubAckPacket) error {
	sess.Ack(p.PacketID)
	return nil
}

func (s *Server) handleSubscribe(cli *client.Client, sess *session.State, p *protocol.SubscribePacket) error {
	returnCodes := make([]byte, len(p.Subscriptions))

	for i, sub := range p.Subscriptions {
		grantedQoS := sub.QoS
		if grantedQoS > 1 {
			grantedQoS = 1
		}

		sess.SetSubscription(sub.Topic, grantedQoS)
		s.broker.Subscribe(sub.Topic, grantedQoS, cli)
		returnCodes[i] = grantedQoS

		if err := s.sendRetained(cli, sess, sub.Topic, grantedQoS); err != nil {
			return err
		}
	}

	return sendPacket(cli, &protocol.SubAckPacket{
		PacketID:    p.PacketID,
		ReturnCodes: returnCodes,
	})
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
	// Keep snapshot delivery and slow-client eviction within one ownership epoch.
	// Concurrent publishers share this read lock; takeover and cleanup are writers.
	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()
	cache := &publishDeliveryCache{}
	var publishErr error

	s.broker.Publish(p, func(sub topic.Subscription) error {
		err := s.deliverPublishToSubscriber(p, sub, cache)
		if err != nil {
			publishErr = err
		}
		return err
	})

	return publishErr
}

func (s *Server) sendRetained(cli *client.Client, sess *session.State, filter string, grantedQoS byte) error {
	for _, retained := range s.broker.RetainedMatching(filter) {
		deliveryQoS := retained.QoS
		if grantedQoS < deliveryQoS {
			deliveryQoS = grantedQoS
		}

		outbound := &protocol.PublishPacket{
			Topic:   retained.Topic,
			Payload: retained.Payload,
			Retain:  true,
			QoS:     deliveryQoS,
		}

		if err := s.enqueuePublish(cli, sess, outbound, nil); err != nil {
			return err
		}
	}

	return nil
}

func (s *Server) replayInflight(cli *client.Client, sess *session.State) error {
	for _, pub := range sess.ReplayPending() {
		raw, err := s.encodePublishFn(pub)
		if err != nil {
			return err
		}
		if err := cli.Enqueue(raw); err != nil {
			return err
		}
	}

	return nil
}

func (s *Server) deliverPublishToSubscriber(pub *protocol.PublishPacket, sub topic.Subscription, cache *publishDeliveryCache) error {
	// publishMessage holds clientsMu while the broker walks its subscriber snapshot.
	owner := s.clients[sub.ClientID]
	if owner == nil || owner.closing || owner.client != sub.Subscriber {
		return nil
	}
	state := owner.session

	deliveryQoS := deliveryQoS(pub.QoS, sub.QoS)
	outbound := newOutboundPublish(pub, deliveryQoS)

	if deliveryQoS == 0 {
		raw, err := cache.qos0Payload(s.encodePublishFn, outbound)
		if err != nil {
			return err
		}
		return sub.Subscriber.Enqueue(raw)
	}

	template, err := cache.qos1Template(s.encodePublishFn, outbound)
	if err != nil {
		return err
	}

	return s.enqueuePublish(sub.Subscriber, state, outbound, template)
}

func (s *Server) enqueuePublish(sub topic.Subscriber, sess *session.State, pub *protocol.PublishPacket, template *publishTemplate) error {
	rawPub := pub
	if pub.QoS == 1 {
		tracked, err := sess.TrackOutbound(pub)
		if err != nil {
			return err
		}
		rawPub = tracked
	}

	var raw []byte
	if template != nil {
		raw = template.withPacketID(rawPub.PacketID)
	} else {
		var err error
		raw, err = s.encodePublishFn(rawPub)
		if err != nil {
			if rawPub.QoS == 1 {
				sess.Ack(rawPub.PacketID)
			}
			return err
		}
	}

	if err := sub.Enqueue(raw); err != nil {
		if rawPub.QoS == 1 {
			sess.Ack(rawPub.PacketID)
		}
		return err
	}

	return nil
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

func (t *publishTemplate) withPacketID(packetID uint16) []byte {
	raw := append([]byte(nil), t.raw...)
	raw[t.packetIDOffset] = byte(packetID >> 8)
	raw[t.packetIDOffset+1] = byte(packetID)
	return raw
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
