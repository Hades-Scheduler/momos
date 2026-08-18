// Command momos is the Momos service: webhook receiver, job builder, Hades
// client, run store, callback endpoint, and reconciler (plan.md §3).
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ls1intum/momos/internal/config"
	"github.com/ls1intum/momos/internal/prompt"
	"github.com/ls1intum/momos/internal/server"
	"github.com/ls1intum/momos/internal/store"
	"github.com/ls1intum/momos/internal/token"
)

func main() {
	configPath := flag.String("config", envOr("MOMOS_CONFIG", "config.yaml"), "path to config.yaml")
	dbPath := flag.String("db", envOr("MOMOS_DB", "momos.db"), "path to the run-store SQLite database")
	promptDir := flag.String("prompts", envOr("MOMOS_PROMPTS", "prompts"), "directory of prompt files")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Error("load config", "err", err)
		os.Exit(1)
	}

	secret := os.Getenv("MOMOS_TOKEN_SECRET")
	if secret == "" {
		log.Error("MOMOS_TOKEN_SECRET is required (signs step and callback tokens)")
		os.Exit(1)
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Error("open run store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	srv := server.New(cfg, st, prompt.NewFilesystemStore(*promptDir), token.NewMinter([]byte(secret)), log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	httpSrv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("momos listening", "addr", cfg.Server.Addr, "external", cfg.Server.ExternalURL)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http server", "err", err)
			stop()
		}
	}()

	go srv.Start(ctx)

	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
