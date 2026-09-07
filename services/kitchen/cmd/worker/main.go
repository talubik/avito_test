package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/avito-kitchen/kitchen/internal/config"
	"github.com/avito-kitchen/kitchen/internal/store"
)

type outboxStore interface {
	ClaimOutbox(context.Context) (store.OutboxEvent, error)
	CompleteOutbox(context.Context, string) error
	FailOutbox(context.Context, string, string, int) error
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("load configuration", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	database, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer database.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	logger.Info("outbox worker started")
	for {
		select {
		case <-ctx.Done():
			logger.Info("outbox worker stopped")
			return
		case <-ticker.C:
			deliver(ctx, logger, client, database, cfg.WebhookSecret)
		}
	}
}

func deliver(ctx context.Context, logger *slog.Logger, client *http.Client, database outboxStore, secret string) {
	event, err := database.ClaimOutbox(ctx)
	if err == store.ErrNotFound {
		return
	}
	if err != nil {
		logger.Error("claim outbox event", "error", err)
		return
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(event.Payload)
	signature := hex.EncodeToString(mac.Sum(nil))
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, event.DestinationURL, bytes.NewReader(event.Payload))
	if err == nil {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Webhook-Timestamp", timestamp)
		request.Header.Set("X-Webhook-Signature", "sha256="+signature)
		request.Header.Set("X-Event-ID", event.ID)
		var response *http.Response
		response, err = client.Do(request)
		if err == nil {
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
				err = fmt.Errorf("webhook returned %s: %s", response.Status, string(body))
			}
		}
	}
	if err != nil {
		logger.Warn("webhook delivery failed", "event_id", event.ID, "attempt", event.Attempts, "error", err)
		if updateErr := database.FailOutbox(ctx, event.ID, err.Error(), event.Attempts); updateErr != nil {
			logger.Error("schedule outbox retry", "event_id", event.ID, "error", updateErr)
		}
		return
	}
	if err := database.CompleteOutbox(ctx, event.ID); err != nil {
		logger.Error("complete outbox event", "event_id", event.ID, "error", err)
		return
	}
	logger.Info("webhook delivered", "event_id", event.ID, "attempt", event.Attempts)
}
