// Command compost runs the Compost caching proxy for Composer.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/geekette86/compost/internal/cache"
	"github.com/geekette86/compost/internal/config"
	"github.com/geekette86/compost/internal/proxy"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "compost:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", os.Getenv("COMPOST_CONFIG"), "path to a JSON config file (env COMPOST_CONFIG)")
	printDefaults := flag.Bool("print-config", false, "print the effective configuration and exit")
	debug := flag.Bool("debug", os.Getenv("COMPOST_DEBUG") != "", "enable debug logging (env COMPOST_DEBUG)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("compost", version)
		return nil
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *printDefaults {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(cfg)
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	store, err := cache.New(cfg.CacheDir, cfg.MaxObjectSize)
	if err != nil {
		return err
	}
	handler, err := proxy.New(proxy.Options{Config: cfg, Store: store, Logger: logger, Version: version})
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: large dists on slow links must be allowed to finish.
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		logger.Info("compost is rotting nicely", "listen", cfg.Listen, "cache_dir", cfg.CacheDir, "mirrors", len(cfg.Mirrors), "version", version)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
