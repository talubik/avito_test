package config

import (
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("DEMO_RESTAURANT_WEBHOOK_SECRET", "secret")
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("WORKER_POLL_INTERVAL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.PollInterval != time.Second {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadValidation(t *testing.T) {
	t.Run("database is required", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "")
		t.Setenv("DEMO_RESTAURANT_WEBHOOK_SECRET", "secret")
		if _, err := Load(); err == nil {
			t.Fatal("Load() error = nil")
		}
	})
	t.Run("secret is required", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgres://test")
		t.Setenv("DEMO_RESTAURANT_WEBHOOK_SECRET", "")
		if _, err := Load(); err == nil {
			t.Fatal("Load() error = nil")
		}
	})
	t.Run("duration must be valid", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgres://test")
		t.Setenv("DEMO_RESTAURANT_WEBHOOK_SECRET", "secret")
		t.Setenv("WORKER_POLL_INTERVAL", "soon")
		if _, err := Load(); err == nil {
			t.Fatal("Load() error = nil")
		}
	})
}
