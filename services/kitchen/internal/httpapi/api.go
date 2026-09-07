package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	orderdomain "github.com/avito-kitchen/kitchen/internal/domain/order"
	"github.com/avito-kitchen/kitchen/internal/store"
)

type API struct {
	store  *store.Store
	logger *slog.Logger
}

func New(s *store.Store, logger *slog.Logger) http.Handler {
	api := &API{store: s, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", api.live)
	mux.HandleFunc("GET /health/ready", api.ready)
	mux.HandleFunc("GET /v1/establishments", api.listEstablishments)
	mux.HandleFunc("GET /v1/establishments/{establishmentID}/menu", api.listMenu)
	mux.HandleFunc("GET /v1/cart", api.getCart)
	mux.HandleFunc("POST /v1/cart/items", api.addCartItem)
	mux.HandleFunc("PATCH /v1/cart/items/{itemID}", api.setCartItem)
	mux.HandleFunc("DELETE /v1/cart/items/{itemID}", api.deleteCartItem)
	mux.HandleFunc("POST /v1/orders", api.checkout)
	mux.HandleFunc("GET /v1/orders", api.listCustomerOrders)
	mux.HandleFunc("GET /v1/orders/{orderID}", api.getCustomerOrder)
	mux.HandleFunc("POST /v1/orders/{orderID}/cancel", api.cancelOrder)
	mux.HandleFunc("PUT /partner/v1/menu/items/{externalID}", api.upsertMenuItem)
	mux.HandleFunc("GET /partner/v1/orders", api.listPartnerOrders)
	mux.HandleFunc("GET /partner/v1/orders/{orderID}", api.getPartnerOrder)
	mux.HandleFunc("POST /partner/v1/orders/{orderID}/status", api.transitionOrder)
	return api.middleware(mux)
}

func (a *API) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		defer func() {
			if recovered := recover(); recovered != nil {
				a.logger.Error("panic recovered", "panic", recovered)
				problem(w, http.StatusInternalServerError, "internal_error", "Internal server error")
			}
			a.logger.Info("http request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
		}()
		next.ServeHTTP(w, r)
	})
}

func (a *API) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		problem(w, http.StatusServiceUnavailable, "database_unavailable", "Database is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) listEstablishments(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListEstablishments(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) listMenu(w http.ResponseWriter, r *http.Request) {
	onlyAvailable := r.URL.Query().Get("available") != "false"
	items, err := a.store.ListMenu(r.Context(), r.PathValue("establishmentID"), onlyAvailable)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func userID(r *http.Request) string { return strings.TrimSpace(r.Header.Get("X-User-ID")) }

func requireUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := userID(r)
	if id == "" {
		problem(w, http.StatusBadRequest, "missing_user_id", "X-User-ID header is required")
		return "", false
	}
	if len(id) > 128 {
		problem(w, http.StatusBadRequest, "invalid_user_id", "X-User-ID is too long")
		return "", false
	}
	return id, true
}

func (a *API) getCart(w http.ResponseWriter, r *http.Request) {
	uid, ok := requireUser(w, r)
	if !ok {
		return
	}
	cart, err := a.store.GetCart(r.Context(), uid)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}, "subtotal_minor": 0})
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cart)
}

type cartItemRequest struct {
	MenuItemID string `json:"menu_item_id"`
	Quantity   int    `json:"quantity"`
}

func (a *API) addCartItem(w http.ResponseWriter, r *http.Request) {
	uid, ok := requireUser(w, r)
	if !ok {
		return
	}
	var request cartItemRequest
	if !decode(w, r, &request) {
		return
	}
	if request.MenuItemID == "" || request.Quantity < 1 || request.Quantity > 99 {
		problem(w, 400, "invalid_request", "menu_item_id and quantity from 1 to 99 are required")
		return
	}
	cart, err := a.store.AddCartItem(r.Context(), uid, request.MenuItemID, request.Quantity)
	if err != nil {
		a.businessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cart)
}

func (a *API) setCartItem(w http.ResponseWriter, r *http.Request) {
	uid, ok := requireUser(w, r)
	if !ok {
		return
	}
	var request struct {
		Quantity int `json:"quantity"`
	}
	if !decode(w, r, &request) {
		return
	}
	if request.Quantity < 1 || request.Quantity > 99 {
		problem(w, 400, "invalid_request", "quantity must be from 1 to 99")
		return
	}
	cart, err := a.store.SetCartItem(r.Context(), uid, r.PathValue("itemID"), request.Quantity)
	if err != nil {
		a.businessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cart)
}

func (a *API) deleteCartItem(w http.ResponseWriter, r *http.Request) {
	uid, ok := requireUser(w, r)
	if !ok {
		return
	}
	if err := a.store.DeleteCartItem(r.Context(), uid, r.PathValue("itemID")); err != nil {
		a.businessError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) checkout(w http.ResponseWriter, r *http.Request) {
	uid, ok := requireUser(w, r)
	if !ok {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 128 {
		problem(w, 400, "invalid_idempotency_key", "Idempotency-Key header is required and must not exceed 128 characters")
		return
	}
	var request struct {
		DeliveryAddress json.RawMessage `json:"delivery_address"`
		CustomerPhone   string          `json:"customer_phone"`
		Comment         string          `json:"comment"`
	}
	if !decode(w, r, &request) {
		return
	}
	if len(request.DeliveryAddress) == 0 || request.CustomerPhone == "" {
		problem(w, 400, "invalid_request", "delivery_address and customer_phone are required")
		return
	}
	var address map[string]any
	if err := json.Unmarshal(request.DeliveryAddress, &address); err != nil || address == nil {
		problem(w, 400, "invalid_address", "delivery_address must be a JSON object")
		return
	}
	if len(request.CustomerPhone) > 32 || len(request.Comment) > 500 {
		problem(w, 400, "invalid_request", "customer_phone or comment is too long")
		return
	}
	order, replayed, err := a.store.Checkout(r.Context(), store.CheckoutInput{UserID: uid, IdempotencyKey: key,
		DeliveryAddress: request.DeliveryAddress, CustomerPhone: request.CustomerPhone, Comment: request.Comment})
	if err != nil {
		a.businessError(w, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeJSON(w, http.StatusCreated, order)
}

func (a *API) listCustomerOrders(w http.ResponseWriter, r *http.Request) {
	uid, ok := requireUser(w, r)
	if !ok {
		return
	}
	orders, err := a.store.ListOrders(r.Context(), uid, "")
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": orders})
}

func (a *API) getCustomerOrder(w http.ResponseWriter, r *http.Request) {
	uid, ok := requireUser(w, r)
	if !ok {
		return
	}
	order, err := a.store.GetOrder(r.Context(), r.PathValue("orderID"), uid, "")
	if err != nil {
		a.businessError(w, err)
		return
	}
	writeJSON(w, 200, order)
}

func (a *API) cancelOrder(w http.ResponseWriter, r *http.Request) {
	uid, ok := requireUser(w, r)
	if !ok {
		return
	}
	order, err := a.store.CancelOrder(r.Context(), uid, r.PathValue("orderID"))
	if err != nil {
		a.businessError(w, err)
		return
	}
	writeJSON(w, 200, order)
}

func (a *API) partner(w http.ResponseWriter, r *http.Request) (string, bool) {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(value, "Bearer ") {
		problem(w, 401, "unauthorized", "Bearer partner API key is required")
		return "", false
	}
	id, err := a.store.AuthenticatePartner(r.Context(), strings.TrimSpace(strings.TrimPrefix(value, "Bearer ")))
	if err != nil {
		problem(w, 401, "unauthorized", "Invalid partner API key")
		return "", false
	}
	return id, true
}

func (a *API) upsertMenuItem(w http.ResponseWriter, r *http.Request) {
	establishmentID, ok := a.partner(w, r)
	if !ok {
		return
	}
	var request struct {
		Category    string `json:"category"`
		Name        string `json:"name"`
		Description string `json:"description"`
		PriceMinor  int64  `json:"price_minor"`
		Currency    string `json:"currency"`
		IsAvailable bool   `json:"is_available"`
		Revision    int64  `json:"revision"`
	}
	if !decode(w, r, &request) {
		return
	}
	if request.Category == "" || request.Name == "" || request.PriceMinor < 0 || request.Revision < 1 {
		problem(w, 400, "invalid_request", "category, name, non-negative price_minor and positive revision are required")
		return
	}
	if request.Currency == "" {
		request.Currency = "RUB"
	}
	request.Currency = strings.ToUpper(request.Currency)
	if len(request.Currency) != 3 {
		problem(w, 400, "invalid_currency", "currency must be a three-letter ISO 4217 code")
		return
	}
	item, err := a.store.UpsertMenuItem(r.Context(), establishmentID, store.MenuUpsert{ExternalID: r.PathValue("externalID"),
		Category: request.Category, Name: request.Name, Description: request.Description, PriceMinor: request.PriceMinor,
		Currency: request.Currency, Available: request.IsAvailable, Revision: request.Revision})
	if err != nil {
		a.businessError(w, err)
		return
	}
	writeJSON(w, 200, item)
}

func (a *API) listPartnerOrders(w http.ResponseWriter, r *http.Request) {
	establishmentID, ok := a.partner(w, r)
	if !ok {
		return
	}
	orders, err := a.store.ListOrders(r.Context(), "", establishmentID)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": orders})
}

func (a *API) getPartnerOrder(w http.ResponseWriter, r *http.Request) {
	establishmentID, ok := a.partner(w, r)
	if !ok {
		return
	}
	order, err := a.store.GetOrder(r.Context(), r.PathValue("orderID"), "", establishmentID)
	if err != nil {
		a.businessError(w, err)
		return
	}
	writeJSON(w, 200, order)
}

func (a *API) transitionOrder(w http.ResponseWriter, r *http.Request) {
	establishmentID, ok := a.partner(w, r)
	if !ok {
		return
	}
	var request struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &request) {
		return
	}
	from, valid := orderdomain.PreviousStatus(request.Status)
	if !valid {
		problem(w, 422, "invalid_transition", "Unsupported target order status")
		return
	}
	order, err := a.store.TransitionOrder(r.Context(), establishmentID, r.PathValue("orderID"), from, request.Status, "restaurant", request.Reason)
	if errors.Is(err, store.ErrInvalidTransition) {
		current, getErr := a.store.GetOrder(r.Context(), r.PathValue("orderID"), "", establishmentID)
		if getErr == nil && current.Status == request.Status {
			writeJSON(w, 200, current)
			return
		}
	}
	if err != nil {
		a.businessError(w, err)
		return
	}
	writeJSON(w, 200, order)
}

func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		problem(w, 400, "invalid_json", "Request body contains invalid JSON")
		return false
	}
	return true
}

func (a *API) businessError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		problem(w, 404, "not_found", "Resource was not found")
	case errors.Is(err, store.ErrMixedEstablishments):
		problem(w, 409, "mixed_establishments", err.Error())
	case errors.Is(err, store.ErrUnavailable):
		problem(w, 409, "item_unavailable", err.Error())
	case errors.Is(err, store.ErrMinimumOrder):
		problem(w, 409, "minimum_order", err.Error())
	case errors.Is(err, store.ErrQuantityLimit):
		problem(w, 409, "quantity_limit", err.Error())
	case errors.Is(err, store.ErrCurrencyMismatch):
		problem(w, 409, "currency_mismatch", err.Error())
	case errors.Is(err, store.ErrInvalidTransition):
		problem(w, 409, "invalid_transition", err.Error())
	default:
		a.internal(w, err)
	}
}

func (a *API) internal(w http.ResponseWriter, err error) {
	a.logger.Error("request failed", "error", err)
	problem(w, 500, "internal_error", "Internal server error")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func problem(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":  "https://errors.avito-kitchen.local/" + code,
		"title": http.StatusText(status), "status": status, "detail": detail, "code": code,
	})
}
