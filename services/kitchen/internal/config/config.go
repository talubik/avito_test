package config

import (
	"fmt"
	"os"
	"time"
)

type Config struct {
	DatabaseURL   string
	HTTPAddr      string
	WebhookSecret string
	PollInterval  time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		HTTPAddr:      valueOrDefault("HTTP_ADDR", ":8080"),
		WebhookSecret: os.Getenv("DEMO_RESTAURANT_WEBHOOK_SECRET"),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.WebhookSecret == "" {
		return Config{}, fmt.Errorf("DEMO_RESTAURANT_WEBHOOK_SECRET is required")
	}
	var err error
	cfg.PollInterval, err = time.ParseDuration(valueOrDefault("WORKER_POLL_INTERVAL", "1s"))
	if err != nil {
		return Config{}, fmt.Errorf("parse WORKER_POLL_INTERVAL: %w", err)
	}
	return cfg, nil
}

func valueOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
