package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Y1le/hajimi-payment-service/internal/api"
	"github.com/Y1le/hajimi-payment-service/internal/order"
)

func main() {
	if err := run(); err != nil {
		slog.Error("payment service stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	handler, err := api.New(order.NewMemoryStore(), os.Getenv("PAYMENT_API_KEY"))
	if err != nil {
		return err
	}
	addr := os.Getenv("PAYMENT_ADDR")
	if addr == "" {
		addr = "127.0.0.1:5210"
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErrors := make(chan error, 1)
	go func() {
		slog.Info("payment service listening", "address", addr, "storage", "memory", "gateway_connected", false)
		serveErrors <- srv.ListenAndServe()
	}()
	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			_ = srv.Close()
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	}
}
