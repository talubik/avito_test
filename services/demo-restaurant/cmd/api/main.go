package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type menuItem struct {
	ExternalID  string `json:"external_id"`
	Category    string `json:"category"`
	Name        string `json:"name"`
	Description string `json:"description"`
	PriceMinor  int64  `json:"price_minor"`
	Currency    string `json:"currency"`
	IsAvailable bool   `json:"is_available"`
	Revision    int64  `json:"revision"`
}

type order struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Items  []struct {
		ExternalItemID string `json:"external_item_id"`
		Name           string `json:"name"`
		Quantity       int    `json:"quantity"`
	} `json:"items"`
	TotalMinor      int64           `json:"total_minor"`
	Currency        string          `json:"currency"`
	DeliveryAddress json.RawMessage `json:"delivery_address"`
	CustomerPhone   string          `json:"customer_phone"`
	CustomerComment string          `json:"customer_comment"`
}

type service struct {
	mu            sync.RWMutex
	orders        map[string]order
	events        map[string]struct{}
	menu          map[string]menuItem
	kitchenURL    string
	apiKey        string
	webhookSecret string
	client        *http.Client
	logger        *slog.Logger
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	kitchenURL := value("KITCHEN_API_URL", "http://localhost:8080")
	apiKey, secret := os.Getenv("PARTNER_API_KEY"), os.Getenv("WEBHOOK_SECRET")
	if apiKey == "" || secret == "" {
		logger.Error("PARTNER_API_KEY and WEBHOOK_SECRET are required")
		os.Exit(1)
	}
	svc := &service{orders: make(map[string]order), events: make(map[string]struct{}), menu: make(map[string]menuItem),
		kitchenURL: kitchenURL, apiKey: apiKey, webhookSecret: secret, client: &http.Client{Timeout: 5 * time.Second}, logger: logger}
	for _, item := range []menuItem{
		{ExternalID: "margherita", Category: "Пицца", Name: "Маргарита", Description: "Томаты, моцарелла и базилик", PriceMinor: 59000, Currency: "RUB", IsAvailable: true, Revision: 1},
		{ExternalID: "pepperoni", Category: "Пицца", Name: "Пепперони", Description: "Пепперони, моцарелла и томатный соус", PriceMinor: 69000, Currency: "RUB", IsAvailable: true, Revision: 1},
		{ExternalID: "morse", Category: "Напитки", Name: "Морс", Description: "Ягодный морс, 500 мл", PriceMinor: 19000, Currency: "RUB", IsAvailable: true, Revision: 1},
	} {
		svc.menu[item.ExternalID] = item
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /v1/menu", svc.listMenu)
	mux.HandleFunc("PATCH /v1/menu/{externalID}/availability", svc.availability)
	mux.HandleFunc("GET /v1/orders", svc.listOrders)
	mux.HandleFunc("GET /v1/orders/{orderID}", svc.getOrder)
	mux.HandleFunc("POST /v1/orders/{orderID}/status", svc.status)
	mux.HandleFunc("POST /webhooks/v1/orders", svc.webhook)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go svc.syncMenuUntilSuccess(ctx)
	server := &http.Server{Addr: value("HTTP_ADDR", ":8081"), Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	logger.Info("demo restaurant started", "address", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("serve", "error", err)
		os.Exit(1)
	}
}

func (s *service) syncMenuUntilSuccess(ctx context.Context) {
	for {
		if err := s.syncMenu(ctx); err == nil {
			s.logger.Info("menu synchronized")
			return
		} else {
			s.logger.Warn("menu synchronization failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *service) syncMenu(ctx context.Context) error {
	s.mu.RLock()
	items := make([]menuItem, 0, len(s.menu))
	for _, item := range s.menu {
		items = append(items, item)
	}
	s.mu.RUnlock()
	for _, item := range items {
		if err := s.syncItem(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (s *service) syncItem(ctx context.Context, item menuItem) error {
	body, _ := json.Marshal(struct {
		Category    string `json:"category"`
		Name        string `json:"name"`
		Description string `json:"description"`
		PriceMinor  int64  `json:"price_minor"`
		Currency    string `json:"currency"`
		IsAvailable bool   `json:"is_available"`
		Revision    int64  `json:"revision"`
	}{
		Category: item.Category, Name: item.Name, Description: item.Description,
		PriceMinor: item.PriceMinor, Currency: item.Currency,
		IsAvailable: item.IsAvailable, Revision: item.Revision,
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, s.kitchenURL+"/partner/v1/menu/items/"+item.ExternalID, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+s.apiKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return fmt.Errorf("kitchen API returned %s: %s", response.Status, data)
	}
	return nil
}

func (s *service) listMenu(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]menuItem, 0, len(s.menu))
	for _, item := range s.menu {
		items = append(items, item)
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *service) availability(w http.ResponseWriter, r *http.Request) {
	var input struct {
		IsAvailable bool `json:"is_available"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		problem(w, 400, "invalid_json")
		return
	}
	s.mu.Lock()
	item, ok := s.menu[r.PathValue("externalID")]
	if !ok {
		s.mu.Unlock()
		problem(w, 404, "not_found")
		return
	}
	item.IsAvailable = input.IsAvailable
	item.Revision++
	s.menu[item.ExternalID] = item
	s.mu.Unlock()
	if err := s.syncItem(r.Context(), item); err != nil {
		s.logger.Error("synchronize availability", "error", err)
		problem(w, 502, "kitchen_unavailable")
		return
	}
	writeJSON(w, 200, item)
}

func (s *service) listOrders(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]order, 0, len(s.orders))
	for _, item := range s.orders {
		items = append(items, item)
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *service) getOrder(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	item, ok := s.orders[r.PathValue("orderID")]
	s.mu.RUnlock()
	if !ok {
		problem(w, 404, "not_found")
		return
	}
	writeJSON(w, 200, item)
}

func (s *service) status(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		problem(w, 400, "invalid_json")
		return
	}
	s.mu.RLock()
	item, ok := s.orders[r.PathValue("orderID")]
	s.mu.RUnlock()
	if !ok {
		problem(w, 404, "not_found")
		return
	}
	body, _ := json.Marshal(input)
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.kitchenURL+"/partner/v1/orders/"+item.ID+"/status", bytes.NewReader(body))
	if err == nil {
		request.Header.Set("Authorization", "Bearer "+s.apiKey)
		request.Header.Set("Content-Type", "application/json")
		var response *http.Response
		response, err = s.client.Do(request)
		if err == nil {
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				data, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
				err = fmt.Errorf("kitchen API returned %s: %s", response.Status, data)
			}
		}
	}
	if err != nil {
		s.logger.Error("update order status", "error", err)
		problem(w, 502, "kitchen_unavailable")
		return
	}
	s.mu.Lock()
	item.Status = input.Status
	s.orders[item.ID] = item
	s.mu.Unlock()
	writeJSON(w, 200, item)
}

func (s *service) webhook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		problem(w, 400, "invalid_body")
		return
	}
	if !s.verifySignature(r, body) {
		problem(w, 401, "invalid_signature")
		return
	}
	var envelope struct {
		EventID   string `json:"event_id"`
		EventType string `json:"event_type"`
		Order     order  `json:"order"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.EventID == "" || envelope.Order.ID == "" {
		problem(w, 400, "invalid_event")
		return
	}
	s.mu.Lock()
	if _, exists := s.events[envelope.EventID]; exists {
		s.mu.Unlock()
		writeJSON(w, 200, map[string]string{"status": "already_processed"})
		return
	}
	s.events[envelope.EventID] = struct{}{}
	s.orders[envelope.Order.ID] = envelope.Order
	s.mu.Unlock()
	s.logger.Info("order received", "event_id", envelope.EventID, "order_id", envelope.Order.ID)
	writeJSON(w, 202, map[string]string{"status": "accepted"})
}

func (s *service) verifySignature(r *http.Request, body []byte) bool {
	timestamp := r.Header.Get("X-Webhook-Timestamp")
	unix, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || time.Since(time.Unix(unix, 0)) > 5*time.Minute || time.Until(time.Unix(unix, 0)) > time.Minute {
		return false
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(r.Header.Get("X-Webhook-Signature"), "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(s.webhookSecret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	return hmac.Equal(provided, mac.Sum(nil))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func problem(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/problem+json")
	writeJSON(w, status, map[string]any{"title": http.StatusText(status), "status": status, "code": code})
}
func value(key, fallback string) string {
	if result := os.Getenv(key); result != "" {
		return result
	}
	return fallback
}
