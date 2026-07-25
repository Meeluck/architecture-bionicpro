package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := loadConfig()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	httpClient := &http.Client{Timeout: 15 * time.Second}
	store := newClickHouseStore(
		cfg.ClickHouseURL, cfg.ClickHouseDB, cfg.ClickHouseUser, cfg.ClickHousePassword, httpClient,
	)
	verifier := newTokenVerifier(
		cfg.KeycloakIssuer, cfg.JWTAudience, cfg.JWTRequiredRole, cfg.KeycloakJWKSURL,
		&http.Client{Timeout: 5 * time.Second},
	)
	api := &apiServer{
		verifier: verifier, store: store, logger: logger, allowedOrigin: cfg.CORSAllowedOrigin,
		defaultReportRange: cfg.DefaultReportRange, maxReportRange: cfg.MaxReportRange,
	}

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           api.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		logger.Info("reports API started", "address", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("reports API stopped unexpectedly", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("reports API shutdown failed", "error", err)
	}
}
