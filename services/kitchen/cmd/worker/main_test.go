package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/avito-kitchen/kitchen/internal/store"
)

type fakeOutbox struct {
	event       store.OutboxEvent
	claimErr    error
	completedID string
	failedID    string
	failure     string
	attempts    int
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func (f *fakeOutbox) ClaimOutbox(context.Context) (store.OutboxEvent, error) {
	return f.event, f.claimErr
}

func (f *fakeOutbox) CompleteOutbox(_ context.Context, id string) error {
	f.completedID = id
	return nil
}

func (f *fakeOutbox) FailOutbox(_ context.Context, id, message string, attempts int) error {
	f.failedID, f.failure, f.attempts = id, message, attempts
	return nil
}

func TestDeliverSignsAndCompletesEvent(t *testing.T) {
	t.Parallel()
	const secret = "worker-secret"
	payload := []byte(`{"event_id":"event-1"}`)
	received := false
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if string(body) != string(payload) || r.Header.Get("X-Event-ID") != "event-1" {
			t.Errorf("unexpected webhook: body=%s event=%s", body, r.Header.Get("X-Event-ID"))
		}
		timestamp := r.Header.Get("X-Webhook-Timestamp")
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(timestamp + "."))
		_, _ = mac.Write(body)
		expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(r.Header.Get("X-Webhook-Signature")), []byte(expected)) {
			t.Error("webhook signature is invalid")
		}
		received = true
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Status:     "202 Accepted",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    r,
		}, nil
	})}
	fake := &fakeOutbox{event: store.OutboxEvent{
		ID: "event-1", EventType: "order.created", Payload: payload,
		DestinationURL: "https://restaurant.test/webhook", Attempts: 1,
	}}
	deliver(context.Background(), discardLogger(), client, fake, secret)
	if !received {
		t.Fatal("webhook was not received")
	}
	if fake.completedID != "event-1" || fake.failedID != "" {
		t.Fatalf("completion state: %+v", fake)
	}
}

func TestDeliverSchedulesRetryOnHTTPFailure(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Status:     "503 Service Unavailable",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("temporary failure")),
			Request:    r,
		}, nil
	})}
	fake := &fakeOutbox{event: store.OutboxEvent{
		ID: "event-2", Payload: []byte(`{}`), DestinationURL: "https://restaurant.test/webhook", Attempts: 3,
	}}
	deliver(context.Background(), discardLogger(), client, fake, "secret")
	if fake.completedID != "" || fake.failedID != "event-2" || fake.attempts != 3 {
		t.Fatalf("retry state: %+v", fake)
	}
	if !strings.Contains(fake.failure, "503 Service Unavailable") {
		t.Fatalf("failure = %q; want HTTP status", fake.failure)
	}
}

func TestDeliverIgnoresEmptyQueue(t *testing.T) {
	t.Parallel()
	fake := &fakeOutbox{claimErr: store.ErrNotFound}
	deliver(context.Background(), discardLogger(), &http.Client{}, fake, "secret")
	if fake.completedID != "" || fake.failedID != "" {
		t.Fatalf("empty queue changed state: %+v", fake)
	}
}

func TestDeliverHandlesClaimError(t *testing.T) {
	t.Parallel()
	fake := &fakeOutbox{claimErr: errors.New("database unavailable")}
	deliver(context.Background(), discardLogger(), &http.Client{}, fake, "secret")
	if fake.completedID != "" || fake.failedID != "" {
		t.Fatalf("claim error changed state: %+v", fake)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
