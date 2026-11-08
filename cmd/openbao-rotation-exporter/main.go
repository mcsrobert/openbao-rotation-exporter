// Command openbao-rotation-exporter exposes OpenBao KV v2 rotation deadlines
// as Prometheus metrics.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mcsrobert/openbao-rotation-exporter/internal/collector"
	"github.com/mcsrobert/openbao-rotation-exporter/internal/config"
	"github.com/mcsrobert/openbao-rotation-exporter/internal/metrics"
	"github.com/mcsrobert/openbao-rotation-exporter/internal/openbao"
	"github.com/mcsrobert/openbao-rotation-exporter/internal/server"
)

const shutdownTimeout = 5 * time.Second

var version = "dev"

func main() {
	configPath := flag.String("config", "/etc/openbao-rotation-exporter/config.yaml", "path to the configuration file")
	listenAddress := flag.String("listen-address", ":8080", "address to serve HTTP on")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(*configPath, *listenAddress, logger); err != nil {
		logger.Error("exiting", "error", err)
		os.Exit(1)
	}
}

func run(configPath, listenAddress string, logger *slog.Logger) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	client, err := openbao.New(cfg.OpenBao, time.Now)
	if err != nil {
		return err
	}

	keys := make(map[string]time.Duration, len(cfg.Keys))
	for key, interval := range cfg.Keys {
		keys[key] = time.Duration(interval)
	}
	rotation := collector.New(client, collector.Options{
		Keys:        keys,
		Interval:    cfg.Collection.Interval,
		Timeout:     cfg.Collection.Timeout,
		Concurrency: cfg.Collection.Concurrency,
		Now:         time.Now,
		Logger:      logger,
	})
	registry := metrics.NewRegistry(rotation.Snapshot, version)
	httpServer := server.New(listenAddress, registry, func() bool { return rotation.Snapshot().Attempted })

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	collected := make(chan struct{})
	go func() {
		defer close(collected)
		rotation.Run(ctx)
	}()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("serving", "address", listenAddress, "version", version, "keys", len(keys))
		serveErr <- httpServer.ListenAndServe()
	}()

	select {
	case err = <-serveErr:
		stop()
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		err = httpServer.Shutdown(shutdownCtx)
	}
	<-collected
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
