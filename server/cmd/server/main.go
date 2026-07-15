package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/p2pplayer/server/internal/api"
	"github.com/p2pplayer/server/internal/cache"
	"github.com/p2pplayer/server/internal/config"
	"github.com/p2pplayer/server/internal/session"
	"github.com/p2pplayer/server/internal/signaling"
)

func main() {
	cfg := config.Load()
	store, err := cache.New(cfg.CacheDir, cfg.CacheMaxBytes)
	if err != nil {
		log.Fatalf("cache: %v", err)
	}
	sessions := session.NewManager(cfg, store)
	hub := signaling.NewHub()
	srv := api.New(cfg, sessions, hub, store)

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("P2PPlayer server listening on %s", cfg.ListenAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
	log.Printf("shutdown complete")
}
