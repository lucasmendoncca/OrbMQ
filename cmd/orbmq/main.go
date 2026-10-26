package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/lucasmendoncca/OrbMQ/internal/broker"
	"github.com/lucasmendoncca/OrbMQ/internal/config"
	"github.com/lucasmendoncca/OrbMQ/internal/observation"
	"github.com/lucasmendoncca/OrbMQ/internal/server"
	"github.com/lucasmendoncca/OrbMQ/internal/session"
)

func main() {
	path := flag.String("config", "config.yaml", "path to broker configuration")
	flag.Parse()
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	if err := run(ctx, *path); err != nil {
		log.Fatalf("server error: %v", err)
	}
	log.Println("server stopped")
}

func run(ctx context.Context, path string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	sessions, err := session.NewWithLimits(cfg.Sessions)
	if err != nil {
		return err
	}
	srv := server.New(cfg.Server.Address, broker.New(), sessions,
		server.WithHandshakeTimeout(cfg.Server.HandshakeTimeout),
		server.WithMaxPacketSize(cfg.Server.MaxPacketSizeBytes))
	observeCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		observation.Run(observeCtx, sessions, cfg.Observability.SummaryInterval, cfg.Observability.SaturationLogInterval, log.Default())
	}()
	defer func() { cancel(); <-done }()
	return srv.Start(ctx)
}
