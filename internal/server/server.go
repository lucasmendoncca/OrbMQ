package server

import (
	"bytes"
	"context"
	"log"
	"net"

	"github.com/lucasmendoncca/OrbMQ/internal/broker"
	"github.com/lucasmendoncca/OrbMQ/internal/client"
	"github.com/lucasmendoncca/OrbMQ/internal/protocol"
)

type Server struct {
	addr   string
	broker *broker.Broker
}

func New(addr string, b *broker.Broker) *Server {
	return &Server{
		addr:   addr,
		broker: b,
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
	defer conn.Close()

	// --- 1. CONNECT ---
	pkt, err := protocol.Decode(conn)
	if err != nil {
		log.Printf("decode error (CONNECT): %v", err)
		return
	}

	connect, ok := pkt.(*protocol.ConnectPacket)
	if !ok {
		log.Printf("first packet is not CONNECT")
		return
	}

	cli := client.New(connect.ClientID, conn)

	log.Printf("client connected: %s", cli.ID())

	// --- 2. CONNACK ---
	err = protocol.Encode(conn, &protocol.ConnAckPacket{
		SessionPresent: false,
		ReturnCode:     protocol.ConnAckAccepted,
	})
	if err != nil {
		log.Printf("connack error: %v", err)
		return
	}

	// --- 3. Loop pós-handshake ---
	for {
		select {
		case <-ctx.Done():
			return
		default:
			pkt, err := protocol.Decode(conn)
			if err != nil {
				log.Printf("decode error: %v", err)
				return
			}

			switch p := pkt.(type) {

			case *protocol.PingReqPacket:
				// PINGRESP obrigatório
				if err := protocol.Encode(conn, &protocol.PingRespPacket{}); err != nil {
					log.Printf("pingresp error: %v", err)
					return
				}

			case *protocol.SubscribePacket:
				// Registrar subscriptions
				for _, sub := range p.Subscriptions {
					s.broker.Subscribe(sub.Topic, cli)
				}

				// SUBACK
				returnCodes := make([]byte, len(p.Subscriptions))
				for i := range returnCodes {
					returnCodes[i] = 0x00 // QoS 0
				}

				if err := protocol.Encode(conn, &protocol.SubAckPacket{
					PacketID:    p.PacketID,
					ReturnCodes: returnCodes,
				}); err != nil {
					log.Printf("suback error: %v", err)
					return
				}

			case *protocol.PublishPacket:
				// Re-encode PUBLISH para fan-out
				var buf bytes.Buffer
				if err := protocol.EncodePublish(&buf, p.Topic, p.Payload); err != nil {
					log.Printf("publish encode error: %v", err)
					return
				}

				s.broker.Publish(p, buf.Bytes())

			default:
				log.Printf("unsupported packet type")
				return
			}
		}
	}
}
