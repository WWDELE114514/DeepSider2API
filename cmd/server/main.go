// Command server runs the DeepSider2API gateway.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/WWDELE114514/DeepSider2API/internal/config"
	"github.com/WWDELE114514/DeepSider2API/internal/server"
	"github.com/WWDELE114514/DeepSider2API/internal/sign"
)

func panelURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://" + listen + "/panel/"
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/panel/"
}

func main() {
	configPath := flag.String("config", "config.json", "path to config.json (relative paths are resolved next to the executable)")
	flag.Parse()

	// Anchor the config file to the executable directory so that the working
	// directory does not change which config (and data) is used.
	path := *configPath
	if !filepath.IsAbs(path) {
		if exe, err := os.Executable(); err == nil {
			path = filepath.Join(filepath.Dir(exe), path)
		}
	}

	store, err := config.Load(path)
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

	panel := panelURL(store.Get().Listen)

	go func() {
		log.Printf("DeepSider2API 已启动")
		log.Printf("管理面板: %s", panel)
		log.Printf("API 地址: %s/v1", panel[:len(panel)-len("/panel/")])
		log.Printf("监听地址: %s", store.Get().Listen)
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
