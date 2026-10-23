package server

import (
	"bytes"
	"context"
	"errors"
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
	addr            string
	broker          *broker.Broker
	sessions        *session.Store
	listenFn        func(network, addr string) (net.Listener, error)
	encodePublishFn func(pub *protocol.PublishPacket) ([]byte, error)
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
		addr:            addr,
		broker:          b,
		sessions:        sessions,
		listenFn:        net.Listen,
		encodePublishFn: encodePublishPacket,
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

func (s *Server) Start(ctx context.Context) error {
	ln, err := s.listenFn("tcp", s.addr)
	if err != nil {
		return err
	}
	defer ln.Close()

	log.Printf("Listening on %s", s.addr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				log.Printf("accept error: %v", err)
				continue
			}
		}

		go s.handleConn(ctx, conn)
	}
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	connect, cli, err := s.handshake(conn)
	if err != nil {
		return
	}

	sess, sessionPresent := s.openSession(connect, cli)
	defer s.closeSession(connect, cli)

	log.Printf("client connected: %s (cleanSession=%v, sessionPresent=%v)",
		cli.ID(), connect.CleanSession, sessionPresent)

	if err := protocol.Encode(conn, &protocol.ConnAckPacket{
		SessionPresent: sessionPresent,
		ReturnCode:     protocol.ConnAckAccepted,
	}); err != nil {
		log.Printf("connack error: %v", err)
		return
	}

	if err := s.replayInflight(cli, sess); err != nil {
		log.Printf("replay inflight error: %v", err)
		return
	}

	cleanDisconnect := false
	defer func() {
		if connect.Will != nil && !cleanDisconnect {
			s.publishWill(connect.Will)
		}
	}()

	cleanDisconnect = s.serve(ctx, conn, cli, sess, connect.KeepAlive)
}

// handshake reads the first packet from conn, validates it is a CONNECT,
// and returns the parsed packet and a new Client.
func (s *Server) handshake(conn net.Conn) (*protocol.ConnectPacket, *client.Client, error) {
	pkt, err := protocol.Decode(conn)
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
func (s *Server) openSession(connect *protocol.ConnectPacket, cli *client.Client) (*session.State, bool) {
	if connect.CleanSession {
		s.sessions.Delete(connect.ClientID)
		state, _ := s.sessions.GetOrCreate(connect.ClientID)
		return state, false
	}

	state, found := s.sessions.GetOrCreate(connect.ClientID)
	for _, sub := range state.SnapshotSubscriptions() {
		s.broker.Subscribe(sub.Filter, sub.QoS, cli)
	}

	return state, found
}

// closeSession persists or discards the session and cleans up the client.
func (s *Server) closeSession(connect *protocol.ConnectPacket, cli *client.Client) {
	if connect.CleanSession {
		s.sessions.Delete(connect.ClientID)
	}

	s.broker.UnsubscribeAll(cli.ID())
	cli.Close()
}

// serve runs the main packet loop. Returns true if the client disconnected
// cleanly (sent DISCONNECT), false on error or timeout.
func (s *Server) serve(ctx context.Context, conn net.Conn, cli *client.Client, sess *session.State, keepAlive uint16) bool {
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

			pkt, err := protocol.Decode(conn)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					log.Printf("client %s keep-alive timeout", cli.ID())
				} else {
					log.Printf("decode error: %v", err)
				}
				return false
			}

			if err := s.handlePacket(conn, cli, sess, pkt); err != nil {
				if !errors.Is(err, errDisconnect) {
					log.Printf("packet error: %v", err)
				}
				return errors.Is(err, errDisconnect)
			}
		}
	}
}

// handlePacket dispatches a decoded packet to the appropriate handler.
func (s *Server) handlePacket(conn net.Conn, cli *client.Client, sess *session.State, pkt protocol.Packet) error {
	switch p := pkt.(type) {
	case *protocol.PingReqPacket:
		return s.handlePing(conn)
	case *protocol.PubAckPacket:
		return s.handlePubAck(sess, p)
	case *protocol.SubscribePacket:
		return s.handleSubscribe(conn, cli, sess, p)
	case *protocol.UnsubscribePacket:
		return s.handleUnsubscribe(conn, cli, sess, p)
	case *protocol.PublishPacket:
		return s.handlePublish(conn, p)
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

func (s *Server) handlePing(conn net.Conn) error {
	return protocol.Encode(conn, &protocol.PingRespPacket{})
}

func (s *Server) handlePubAck(sess *session.State, p *protocol.PubAckPacket) error {
	sess.Ack(p.PacketID)
	return nil
}

func (s *Server) handleSubscribe(conn net.Conn, cli *client.Client, sess *session.State, p *protocol.SubscribePacket) error {
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

	return protocol.Encode(conn, &protocol.SubAckPacket{
		PacketID:    p.PacketID,
		ReturnCodes: returnCodes,
	})
}

func (s *Server) handleUnsubscribe(conn net.Conn, cli *client.Client, sess *session.State, p *protocol.UnsubscribePacket) error {
	for _, filter := range p.Topics {
		s.broker.Unsubscribe(filter, cli.ID())
		sess.DeleteSubscription(filter)
	}

	return protocol.Encode(conn, &protocol.UnsubAckPacket{PacketID: p.PacketID})
}

func (s *Server) handlePublish(conn net.Conn, p *protocol.PublishPacket) error {
	if err := s.publishMessage(p); err != nil {
		return err
	}

	if p.QoS == 1 {
		return protocol.Encode(conn, &protocol.PubAckPacket{PacketID: p.PacketID})
	}

	return nil
}

func (s *Server) publishMessage(p *protocol.PublishPacket) error {
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
	state, ok := s.sessions.Get(sub.ClientID)
	if !ok {
		return nil
	}

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
