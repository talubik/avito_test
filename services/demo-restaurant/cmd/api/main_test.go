package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

type testRoundTripFunc func(*http.Request) (*http.Response, error)

func (f testRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestWebhookAuthenticatesAndDeduplicates(t *testing.T) {
	t.Parallel()
	const secret = "test-secret"
	svc := &service{
		orders:        make(map[string]order),
		events:        make(map[string]struct{}),
		menu:          make(map[string]menuItem),
		webhookSecret: secret,
		logger:        slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	}
	body := []byte(`{"event_id":"84a38e5a-70d6-4a7e-a5c9-508704bcddf2","event_type":"order.created","order":{"id":"db624bdb-d566-4ea2-a25f-13fa375f6455","status":"pending_restaurant","items":[],"total_minor":100,"currency":"RUB"}}`)

	first := signedWebhookRequest(body, secret)
	firstResponse := httptest.NewRecorder()
	svc.webhook(firstResponse, first)
	if firstResponse.Code != http.StatusAccepted {
		t.Fatalf("first webhook status = %d; want 202", firstResponse.Code)
	}

	duplicate := signedWebhookRequest(body, secret)
	duplicateResponse := httptest.NewRecorder()
	svc.webhook(duplicateResponse, duplicate)
	if duplicateResponse.Code != http.StatusOK {
		t.Fatalf("duplicate webhook status = %d; want 200", duplicateResponse.Code)
	}
	if len(svc.orders) != 1 || len(svc.events) != 1 {
		t.Fatalf("deduplicated state has %d orders and %d events; want 1 and 1", len(svc.orders), len(svc.events))
	}
}

func TestWebhookRejectsInvalidSignature(t *testing.T) {
	t.Parallel()
	svc := &service{orders: make(map[string]order), events: make(map[string]struct{}), webhookSecret: "correct", logger: slog.Default()}
	request := signedWebhookRequest([]byte(`{"event_id":"event","order":{"id":"order"}}`), "wrong")
	response := httptest.NewRecorder()
	svc.webhook(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("webhook status = %d; want 401", response.Code)
	}
}

func TestAvailabilityUpdatesAndSynchronizesMenu(t *testing.T) {
	t.Parallel()
	item := menuItem{ExternalID: "pizza", Category: "Pizza", Name: "Pizza", PriceMinor: 50000, Currency: "RUB", IsAvailable: true, Revision: 4}
	var synchronized map[string]any
	client := &http.Client{Transport: testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/partner/v1/menu/items/pizza" || request.Header.Get("Authorization") != "Bearer partner-key" {
			t.Errorf("unexpected Kitchen request: %s, auth=%q", request.URL.Path, request.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(request.Body).Decode(&synchronized); err != nil {
			t.Errorf("decode synchronized item: %v", err)
		}
		return testHTTPResponse(request, http.StatusOK, `{}`), nil
	})}
	svc := &service{
		orders: make(map[string]order), events: make(map[string]struct{}),
		menu: map[string]menuItem{"pizza": item}, kitchenURL: "https://kitchen.test",
		apiKey: "partner-key", client: client, logger: slog.Default(),
	}
	request := httptest.NewRequest(http.MethodPatch, "/v1/menu/pizza/availability", bytes.NewBufferString(`{"is_available":false}`))
	request.SetPathValue("externalID", "pizza")
	response := httptest.NewRecorder()
	svc.availability(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("availability status = %d, body=%s", response.Code, response.Body.String())
	}
	updated := svc.menu["pizza"]
	if updated.IsAvailable || updated.Revision != 5 {
		t.Fatalf("updated menu item = %+v", updated)
	}
	if synchronized["external_id"] != nil || synchronized["revision"] != float64(5) || synchronized["is_available"] != false {
		t.Fatalf("synchronized payload = %+v", synchronized)
	}
}

func TestStatusForwardsToKitchenAndUpdatesLocalOrder(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/partner/v1/orders/order-1/status" || request.Header.Get("Authorization") != "Bearer partner-key" {
			t.Errorf("unexpected Kitchen request: %s, auth=%q", request.URL.Path, request.Header.Get("Authorization"))
		}
		return testHTTPResponse(request, http.StatusOK, `{}`), nil
	})}
	svc := &service{
		orders: map[string]order{"order-1": {ID: "order-1", Status: "pending_restaurant"}},
		events: make(map[string]struct{}), menu: make(map[string]menuItem),
		kitchenURL: "https://kitchen.test", apiKey: "partner-key", client: client, logger: slog.Default(),
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/orders/order-1/status", bytes.NewBufferString(`{"status":"accepted"}`))
	request.SetPathValue("orderID", "order-1")
	response := httptest.NewRecorder()
	svc.status(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status response = %d, body=%s", response.Code, response.Body.String())
	}
	if svc.orders["order-1"].Status != "accepted" {
		t.Fatalf("local status = %q; want accepted", svc.orders["order-1"].Status)
	}
}

func TestRestaurantReadEndpoints(t *testing.T) {
	t.Parallel()
	svc := &service{
		orders: map[string]order{"order-1": {ID: "order-1", Status: "accepted"}},
		events: make(map[string]struct{}),
		menu:   map[string]menuItem{"pizza": {ExternalID: "pizza", Name: "Pizza"}},
	}
	menuResponse := httptest.NewRecorder()
	svc.listMenu(menuResponse, httptest.NewRequest(http.MethodGet, "/v1/menu", nil))
	if menuResponse.Code != http.StatusOK || !strings.Contains(menuResponse.Body.String(), "pizza") {
		t.Fatalf("list menu response = %d, %s", menuResponse.Code, menuResponse.Body.String())
	}
	orderRequest := httptest.NewRequest(http.MethodGet, "/v1/orders/order-1", nil)
	orderRequest.SetPathValue("orderID", "order-1")
	orderResponse := httptest.NewRecorder()
	svc.getOrder(orderResponse, orderRequest)
	if orderResponse.Code != http.StatusOK || !strings.Contains(orderResponse.Body.String(), "order-1") {
		t.Fatalf("get order response = %d, %s", orderResponse.Code, orderResponse.Body.String())
	}
}

func testHTTPResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status, Status: http.StatusText(status), Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(body)), Request: request,
	}
}

func signedWebhookRequest(body []byte, secret string) *http.Request {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	request := httptest.NewRequest(http.MethodPost, "/webhooks/v1/orders", bytes.NewReader(body))
	request.Header.Set("X-Webhook-Timestamp", timestamp)
	request.Header.Set("X-Webhook-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return request
}
