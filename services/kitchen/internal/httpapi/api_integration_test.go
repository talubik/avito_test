//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/avito-kitchen/kitchen/internal/store"
)

func TestCustomerAndPartnerHTTPFlowIntegration(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	database, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(database.Close)
	server := httptest.NewServer(New(database, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(server.Close)

	response := request(t, server.Client(), http.MethodGet, server.URL+"/v1/cart", "", "", nil)
	assertStatus(t, response, http.StatusBadRequest)
	if contentType := response.Header.Get("Content-Type"); contentType != "application/problem+json" {
		t.Fatalf("problem content type = %q", contentType)
	}
	closeBody(response)

	response = request(t, server.Client(), http.MethodPut, server.URL+"/partner/v1/menu/items/http-margherita", "", "wrong-key", map[string]any{
		"category": "Pizza", "name": "HTTP Margherita", "price_minor": 60000,
		"currency": "RUB", "is_available": true, "revision": 1,
	})
	assertStatus(t, response, http.StatusUnauthorized)
	closeBody(response)

	response = request(t, server.Client(), http.MethodPut, server.URL+"/partner/v1/menu/items/http-margherita", "", "demo-partner-key-change-me", map[string]any{
		"category": "Pizza", "name": "HTTP Margherita", "description": "integration",
		"price_minor": 60000, "currency": "RUB", "is_available": true, "revision": 1,
	})
	assertStatus(t, response, http.StatusOK)
	var menuItem store.MenuItem
	decodeResponse(t, response, &menuItem)

	response = request(t, server.Client(), http.MethodPost, server.URL+"/v1/cart/items", "http-user", "", map[string]any{
		"menu_item_id": menuItem.ID, "quantity": 1,
	})
	assertStatus(t, response, http.StatusOK)
	var cart store.Cart
	decodeResponse(t, response, &cart)
	if len(cart.Items) != 1 || cart.SubtotalMinor != 60000 {
		t.Fatalf("unexpected cart: %+v", cart)
	}

	checkoutBody := map[string]any{
		"delivery_address": map[string]string{"city": "Moscow", "street": "Lesnaya", "house": "7"},
		"customer_phone":   "+79990000000",
	}
	response = requestWithIdempotency(t, server.Client(), server.URL+"/v1/orders", "http-user", "http-key", checkoutBody)
	assertStatus(t, response, http.StatusCreated)
	var created store.Order
	decodeResponse(t, response, &created)
	if created.Status != "pending_restaurant" || created.TotalMinor != 79900 {
		t.Fatalf("unexpected created order: %+v", created)
	}

	response = requestWithIdempotency(t, server.Client(), server.URL+"/v1/orders", "http-user", "http-key", checkoutBody)
	assertStatus(t, response, http.StatusCreated)
	if response.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatal("idempotent replay header is missing")
	}
	var replay store.Order
	decodeResponse(t, response, &replay)
	if replay.ID != created.ID {
		t.Fatalf("replay order ID = %s; want %s", replay.ID, created.ID)
	}

	response = request(t, server.Client(), http.MethodGet, server.URL+"/v1/orders/"+created.ID, "another-user", "", nil)
	assertStatus(t, response, http.StatusNotFound)
	closeBody(response)

	response = request(t, server.Client(), http.MethodPost, server.URL+"/partner/v1/orders/"+created.ID+"/status", "", "demo-partner-key-change-me", map[string]any{"status": "accepted"})
	assertStatus(t, response, http.StatusOK)
	var accepted store.Order
	decodeResponse(t, response, &accepted)
	if accepted.Status != "accepted" {
		t.Fatalf("order status = %s; want accepted", accepted.Status)
	}

	response = request(t, server.Client(), http.MethodPost, server.URL+"/partner/v1/orders/"+created.ID+"/status", "", "demo-partner-key-change-me", map[string]any{"status": "accepted"})
	assertStatus(t, response, http.StatusOK)
	closeBody(response)

	response = request(t, server.Client(), http.MethodPost, server.URL+"/v1/orders/"+created.ID+"/cancel", "http-user", "", nil)
	assertStatus(t, response, http.StatusConflict)
	closeBody(response)

	response = request(t, server.Client(), http.MethodGet, server.URL+"/v1/orders", "http-user", "", nil)
	assertStatus(t, response, http.StatusOK)
	var orders struct {
		Items []store.Order `json:"items"`
	}
	decodeResponse(t, response, &orders)
	if len(orders.Items) != 1 || orders.Items[0].ID != created.ID {
		t.Fatalf("unexpected order list: %+v", orders.Items)
	}
}

func request(t *testing.T, client *http.Client, method, url, userID, apiKey string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, reader)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatalf("perform request: %v", err)
	}
	return response
}

func requestWithIdempotency(t *testing.T, client *http.Client, url, userID, key string, body any) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", userID)
	req.Header.Set("Idempotency-Key", key)
	response, err := client.Do(req)
	if err != nil {
		t.Fatalf("perform request: %v", err)
	}
	return response
}

func assertStatus(t *testing.T, response *http.Response, expected int) {
	t.Helper()
	if response.StatusCode != expected {
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		t.Fatalf("HTTP status = %d, body=%s; want %d", response.StatusCode, body, expected)
	}
}

func decodeResponse(t *testing.T, response *http.Response, target any) {
	t.Helper()
	defer closeBody(response)
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func closeBody(response *http.Response) {
	_ = response.Body.Close()
}
