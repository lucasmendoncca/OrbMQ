package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/lucasmendoncca/OrbMQ/internal/broker"
	"github.com/lucasmendoncca/OrbMQ/internal/server"
)

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	broker := broker.New()
	srv := server.New(":1883", broker)

	go func() {
		if err := srv.Start(ctx); err != nil {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")
}
