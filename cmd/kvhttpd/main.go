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

	"httpkvdb/internal/auth"
	"httpkvdb/internal/config"
	"httpkvdb/internal/httpapi"
	"httpkvdb/internal/lock"
	"httpkvdb/internal/observe"
	"httpkvdb/internal/storage"
	httptx "httpkvdb/internal/tx"
)

func main() {
	configPath := flag.String("config", "", "path to env-style config file")
	flag.Parse()
	cfg := config.Load()
	if *configPath != "" {
		var err error
		cfg, err = config.LoadFromFile(*configPath)
		if err != nil {
			log.Fatalf("config_error: %v", err)
		}
	}
	store, err := storage.Open(cfg.StoragePath)
	if err != nil {
		log.Fatalf("storage_error: %v", err)
	}
	if err := store.Bootstrap(cfg.BootstrapUserID, cfg.BootstrapUserspaceID, auth.APIKeyHash(cfg.BootstrapAPIKey)); err != nil {
		log.Fatalf("bootstrap_error: %v", err)
	}
	serial := &lock.Serializable{}
	authn := auth.New(store, cfg.JWTSecret, cfg.JWTIssuer, cfg.JWTAudience, time.Duration(cfg.AuthCacheTTLMS)*time.Millisecond, cfg.AuthCacheMaxEntries)
	metrics := &observe.Metrics{}
	coord := httptx.NewCoordinator(store, serial, cfg.MaxTxOps, time.Duration(cfg.DefaultTxTimeoutMS)*time.Millisecond, time.Duration(cfg.MaxTxTimeoutMS)*time.Millisecond)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	httptx.StartCleaner(ctx, coord, time.Duration(cfg.TxCleanIntervalMS)*time.Millisecond)
	srv := &http.Server{Addr: cfg.Addr, Handler: httpapi.NewServer(cfg, store, authn, serial, coord, metrics).Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Printf("server_start addr=%s", cfg.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server_error: %v", err)
	}
	log.Printf("server_stop")
}
