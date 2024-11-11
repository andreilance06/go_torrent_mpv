package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/andreilance06/go_torrent_mpv/internal/options"
	"github.com/andreilance06/go_torrent_mpv/internal/torrentclient"
	"github.com/andreilance06/go_torrent_mpv/internal/torrentserver"
	"golang.org/x/sys/windows"
)

func run(ctx context.Context, config *options.Config) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, cancel = context.WithCancel(ctx)
	defer cancel()

	db, err := torrentclient.InitStorage(config)
	if err != nil {
		return err
	}

	c, err := torrentclient.InitClient(config, db, ctx)
	if err != nil {
		return err
	}
	log.Print("Torrent client started")

	defer func() {
		errs := c.Close()
		<-c.Closed()
		for _, err := range errs {
			log.Printf("error shutting down client: %v", err)
		}
		log.Print("Torrent client shutdown successfully")
	}()

	server := torrentserver.InitServer(c, config, cancel)
	log.Printf("Listening on %s...", server.Addr)

	<-ctx.Done()
	log.Print("Shutdown signal received")
	if err := torrentserver.GracefulShutdown(server); err != nil {
		return err
	}

	return nil
}

func main() {
	config := options.ParseFlags()

	_, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/torrents", config.Port))
	if err == nil {
		log.Fatalf("server already listening on port %d", config.Port)
	}
	if err != nil && !errors.Is(err, windows.WSAECONNREFUSED) {
		log.Fatalf("error checking if server already exists: %v", err)
	}

	ctx := context.Background()
	if err := run(ctx, config); err != nil {
		log.Fatal(err)
	}
}
