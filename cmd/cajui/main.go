package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/cajui/cajui-central/internal/config"
	"github.com/cajui/cajui-central/internal/httpapi"
	"github.com/cajui/cajui-central/internal/mqttingest"
	"github.com/cajui/cajui-central/internal/storage"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("cajui stopped", "error", err)
		os.Exit(1)
	}
}
func run(ctx context.Context) error {
	c, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(c.Database), 0700); err != nil {
		return err
	}
	db, err := storage.Open(c.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	var options []httpapi.Option
	if c.MQTT.URL != "" {
		consumer := mqttingest.New(c.MQTT, db, slog.Default())
		options = append(options, httpapi.WithCommands(consumer))
		mqttContext, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); consumer.Run(mqttContext) }()
		defer func() { stop(); <-done }()
	}
	handler, err := httpapi.New(db, c.Token, slog.Default(), options...)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: c.Address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	result := make(chan error, 1)
	go func() { result <- server.ListenAndServe() }()
	slog.Info("cajui starting", "address", c.Address)
	select {
	case err = <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err = server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	}
}
