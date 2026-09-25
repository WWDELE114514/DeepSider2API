// Command server runs the DeepSider2API gateway.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/WWDELE114514/DeepSider2API/internal/config"
	"github.com/WWDELE114514/DeepSider2API/internal/server"
	"github.com/WWDELE114514/DeepSider2API/internal/sign"
)

func main() {
	configPath := flag.String("config", "config.json", "path to config.json")
	flag.Parse()

	store, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx := context.Background()
	signer, err := sign.New(ctx)
	if err != nil {
		log.Fatalf("init signer: %v", err)
	}
	defer func() { _ = signer.Close(ctx) }()

	srv, err := server.New(store, signer)
	if err != nil {
		log.Fatalf("init server: %v", err)
	}

	httpServer := &http.Server{
		Addr:              store.Get().Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 30 * time.Second,
	}

	go func() {
		log.Printf("DeepSider2API listening on %s", store.Get().Listen)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
}
