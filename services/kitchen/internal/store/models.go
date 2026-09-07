package store

import (
	"encoding/json"
	"time"
)

type Establishment struct {
	ID                  string `json:"id"`
	Slug                string `json:"slug"`
	Name                string `json:"name"`
	Description         string `json:"description"`
	AcceptsOrders       bool   `json:"accepts_orders"`
	MinOrderAmountMinor int64  `json:"min_order_amount_minor"`
	DeliveryFeeMinor    int64  `json:"delivery_fee_minor"`
	Currency            string `json:"currency"`
}

type MenuItem struct {
	ID              string `json:"id"`
	ExternalID      string `json:"external_id,omitempty"`
	EstablishmentID string `json:"establishment_id,omitempty"`
	Category        string `json:"category,omitempty"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	PriceMinor      int64  `json:"price_minor"`
	Currency        string `json:"currency"`
	IsAvailable     bool   `json:"is_available"`
	Revision        int64  `json:"revision,omitempty"`
}

type CartItem struct {
	MenuItemID string `json:"menu_item_id"`
	Name       string `json:"name"`
	PriceMinor int64  `json:"price_minor"`
	Currency   string `json:"currency"`
	Available  bool   `json:"available"`
	Quantity   int    `json:"quantity"`
	LineTotal  int64  `json:"line_total_minor"`
}

type Cart struct {
	ID              string     `json:"id"`
	EstablishmentID string     `json:"establishment_id"`
	ExpiresAt       time.Time  `json:"expires_at"`
	Items           []CartItem `json:"items"`
	SubtotalMinor   int64      `json:"subtotal_minor"`
	Currency        string     `json:"currency"`
}

type OrderItem struct {
	ExternalItemID string `json:"external_item_id"`
	Name           string `json:"name"`
	UnitPriceMinor int64  `json:"unit_price_minor"`
	Quantity       int    `json:"quantity"`
	LineTotalMinor int64  `json:"line_total_minor"`
	Currency       string `json:"currency"`
}

type Order struct {
	ID               string          `json:"id"`
	UserID           string          `json:"user_id,omitempty"`
	EstablishmentID  string          `json:"establishment_id"`
	Status           string          `json:"status"`
	SubtotalMinor    int64           `json:"subtotal_minor"`
	DeliveryFeeMinor int64           `json:"delivery_fee_minor"`
	TotalMinor       int64           `json:"total_minor"`
	Currency         string          `json:"currency"`
	DeliveryAddress  json.RawMessage `json:"delivery_address"`
	CustomerPhone    string          `json:"customer_phone,omitempty"`
	CustomerComment  string          `json:"customer_comment,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	Items            []OrderItem     `json:"items"`
}

type MenuUpsert struct {
	ExternalID  string
	Category    string
	Name        string
	Description string
	PriceMinor  int64
	Currency    string
	Available   bool
	Revision    int64
}

type OutboxEvent struct {
	ID             string
	EventType      string
	Payload        []byte
	DestinationURL string
	Attempts       int
}
