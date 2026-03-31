package server

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net"
	"time"

	"github.com/lucasmendoncca/OrbMQ/internal/broker"
	"github.com/lucasmendoncca/OrbMQ/internal/client"
	"github.com/lucasmendoncca/OrbMQ/internal/protocol"
	"github.com/lucasmendoncca/OrbMQ/internal/session"
)

// errDisconnect is returned by serve when the client sends a DISCONNECT packet.
var errDisconnect = errors.New("client disconnected")

type Server struct {
	addr     string
	broker   *broker.Broker
	sessions *session.Store
}

func New(addr string, b *broker.Broker, sessions *session.Store) *Server {
	return &Server{
		addr:     addr,
		broker:   b,
		sessions: sessions,
	}
}

func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
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

	activeFilters, sessionPresent := s.openSession(connect, cli)
	defer s.closeSession(connect, cli, activeFilters)

	log.Printf("client connected: %s (cleanSession=%v, sessionPresent=%v)",
		cli.ID(), connect.CleanSession, sessionPresent)

	if err := protocol.Encode(conn, &protocol.ConnAckPacket{
		SessionPresent: sessionPresent,
		ReturnCode:     protocol.ConnAckAccepted,
	}); err != nil {
		log.Printf("connack error: %v", err)
		return
	}

	s.serve(ctx, conn, cli, activeFilters, connect.KeepAlive)
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

// openSession restores a persistent session or clears a stale one.
// It returns the active filter set and whether a prior session was found.
func (s *Server) openSession(connect *protocol.ConnectPacket, cli *client.Client) (map[string]struct{}, bool) {
	activeFilters := make(map[string]struct{})

	if connect.CleanSession {
		s.sessions.Delete(connect.ClientID)
		return activeFilters, false
	}

	saved, found := s.sessions.Load(connect.ClientID)
	if !found {
		return activeFilters, false
	}

	for _, f := range saved {
		s.broker.Subscribe(f, cli)
		activeFilters[f] = struct{}{}
	}

	return activeFilters, true
}

// closeSession persists or discards the session and cleans up the client.
// Intended for use in a defer.
func (s *Server) closeSession(connect *protocol.ConnectPacket, cli *client.Client, activeFilters map[string]struct{}) {
	if !connect.CleanSession {
		filters := make([]string, 0, len(activeFilters))
		for f := range activeFilters {
			filters = append(filters, f)
		}
		s.sessions.Save(connect.ClientID, filters)
	}

	s.broker.UnsubscribeAll(cli.ID())
	cli.Close()
}

// serve runs the main packet loop for an established connection.
func (s *Server) serve(ctx context.Context, conn net.Conn, cli *client.Client, activeFilters map[string]struct{}, keepAlive uint16) {
	var timeout time.Duration
	if keepAlive > 0 {
		timeout = time.Duration(float64(keepAlive)*1.5) * time.Second
	}

	for {
		select {
		case <-ctx.Done():
			return
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
				return
			}

			if err := s.handlePacket(conn, cli, pkt, activeFilters); err != nil {
				if !errors.Is(err, errDisconnect) {
					log.Printf("packet error: %v", err)
				}
				return
			}
		}
	}
}

// handlePacket dispatches a decoded packet to the appropriate handler.
// Returns errDisconnect when the client sends DISCONNECT, or another error
// on failure.
func (s *Server) handlePacket(conn net.Conn, cli *client.Client, pkt protocol.Packet, activeFilters map[string]struct{}) error {
	switch p := pkt.(type) {
	case *protocol.PingReqPacket:
		return s.handlePing(conn)
	case *protocol.SubscribePacket:
		return s.handleSubscribe(conn, cli, p, activeFilters)
	case *protocol.UnsubscribePacket:
		return s.handleUnsubscribe(conn, cli, p, activeFilters)
	case *protocol.PublishPacket:
		return s.handlePublish(p)
	case *protocol.DisconnectPacket:
		log.Printf("client %s sent DISCONNECT", cli.ID())
		return errDisconnect
	default:
		return errors.New("unsupported packet type")
	}
}

func (s *Server) handlePing(conn net.Conn) error {
	return protocol.Encode(conn, &protocol.PingRespPacket{})
}

func (s *Server) handleSubscribe(conn net.Conn, cli *client.Client, p *protocol.SubscribePacket, activeFilters map[string]struct{}) error {
	for _, sub := range p.Subscriptions {
		s.broker.Subscribe(sub.Topic, cli)
		activeFilters[sub.Topic] = struct{}{}
		s.broker.SendRetained(sub.Topic, cli)
	}

	returnCodes := make([]byte, len(p.Subscriptions))
	// all QoS 0
	return protocol.Encode(conn, &protocol.SubAckPacket{
		PacketID:    p.PacketID,
		ReturnCodes: returnCodes,
	})
}

func (s *Server) handleUnsubscribe(conn net.Conn, cli *client.Client, p *protocol.UnsubscribePacket, activeFilters map[string]struct{}) error {
	for _, filter := range p.Topics {
		s.broker.Unsubscribe(filter, cli.ID())
		delete(activeFilters, filter)
	}

	return protocol.Encode(conn, &protocol.UnsubAckPacket{PacketID: p.PacketID})
}

func (s *Server) handlePublish(p *protocol.PublishPacket) error {
	var buf bytes.Buffer
	if err := protocol.EncodePublish(&buf, p.Topic, p.Payload, p.Retain); err != nil {
		return err
	}

	s.broker.Publish(p, buf.Bytes())
	return nil
}
