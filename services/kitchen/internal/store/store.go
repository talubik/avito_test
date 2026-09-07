package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/avito-kitchen/kitchen/internal/domain/money"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound            = errors.New("not found")
	ErrMixedEstablishments = errors.New("cart may contain items from only one establishment")
	ErrUnavailable         = errors.New("one or more items are unavailable")
	ErrMinimumOrder        = errors.New("minimum order amount is not reached")
	ErrQuantityLimit       = errors.New("resulting item quantity must be from 1 to 99")
	ErrCurrencyMismatch    = errors.New("menu item currency differs from establishment currency")
	ErrInvalidTransition   = errors.New("invalid order status transition")
)

type Store struct{ pool *pgxpool.Pool }

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close()                         { s.pool.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) ListEstablishments(ctx context.Context) ([]Establishment, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, slug, name, description, accepts_orders,
		       min_order_amount_minor, delivery_fee_minor, currency
		FROM establishments WHERE status = 'active' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Establishment, 0)
	for rows.Next() {
		var item Establishment
		if err := rows.Scan(&item.ID, &item.Slug, &item.Name, &item.Description,
			&item.AcceptsOrders, &item.MinOrderAmountMinor, &item.DeliveryFeeMinor, &item.Currency); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) ListMenu(ctx context.Context, establishmentID string, onlyAvailable bool) ([]MenuItem, error) {
	query := `SELECT mi.id, mi.external_id, mi.establishment_id, COALESCE(mc.name, ''),
		mi.name, mi.description, mi.price_minor, mi.currency, mi.is_available, mi.revision
		FROM menu_items mi LEFT JOIN menu_categories mc ON mc.id = mi.category_id
		WHERE mi.establishment_id = $1`
	if onlyAvailable {
		query += ` AND mi.is_available = true`
	}
	query += ` ORDER BY mc.position, mi.name`
	rows, err := s.pool.Query(ctx, query, establishmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]MenuItem, 0)
	for rows.Next() {
		var item MenuItem
		if err := rows.Scan(&item.ID, &item.ExternalID, &item.EstablishmentID, &item.Category,
			&item.Name, &item.Description, &item.PriceMinor, &item.Currency,
			&item.IsAvailable, &item.Revision); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetCart(ctx context.Context, userID string) (Cart, error) {
	var cart Cart
	err := s.pool.QueryRow(ctx, `SELECT id, establishment_id, expires_at FROM carts
		WHERE user_id=$1 AND status='active' AND expires_at > now()`, userID).
		Scan(&cart.ID, &cart.EstablishmentID, &cart.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Cart{}, ErrNotFound
	}
	if err != nil {
		return Cart{}, err
	}
	rows, err := s.pool.Query(ctx, `SELECT mi.id, mi.name, mi.price_minor, mi.currency,
		mi.is_available, ci.quantity, mi.price_minor * ci.quantity
		FROM cart_items ci JOIN menu_items mi ON mi.id=ci.menu_item_id
		WHERE ci.cart_id=$1 ORDER BY ci.created_at`, cart.ID)
	if err != nil {
		return Cart{}, err
	}
	defer rows.Close()
	cart.Items = make([]CartItem, 0)
	for rows.Next() {
		var item CartItem
		if err := rows.Scan(&item.MenuItemID, &item.Name, &item.PriceMinor, &item.Currency,
			&item.Available, &item.Quantity, &item.LineTotal); err != nil {
			return Cart{}, err
		}
		cart.SubtotalMinor += item.LineTotal
		cart.Currency = item.Currency
		cart.Items = append(cart.Items, item)
	}
	return cart, rows.Err()
}

func (s *Store) AddCartItem(ctx context.Context, userID, menuItemID string, quantity int) (Cart, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Cart{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, userID); err != nil {
		return Cart{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE carts SET status='abandoned', updated_at=now()
		WHERE user_id=$1 AND status='active' AND expires_at<=now()`, userID); err != nil {
		return Cart{}, err
	}
	var establishmentID string
	if err := tx.QueryRow(ctx, `SELECT establishment_id FROM menu_items WHERE id=$1 AND is_available=true`, menuItemID).Scan(&establishmentID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Cart{}, ErrUnavailable
		}
		return Cart{}, err
	}
	var cartID, existingEstablishment string
	err = tx.QueryRow(ctx, `SELECT id, establishment_id FROM carts
		WHERE user_id=$1 AND status='active' AND expires_at > now() FOR UPDATE`, userID).Scan(&cartID, &existingEstablishment)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `INSERT INTO carts(user_id, establishment_id) VALUES ($1,$2) RETURNING id`, userID, establishmentID).Scan(&cartID)
	} else if err == nil && existingEstablishment != establishmentID {
		return Cart{}, ErrMixedEstablishments
	}
	if err != nil {
		return Cart{}, err
	}
	var resultingQuantity int
	err = tx.QueryRow(ctx, `INSERT INTO cart_items(cart_id, menu_item_id, quantity) VALUES($1,$2,$3)
		ON CONFLICT(cart_id, menu_item_id) DO UPDATE SET quantity=cart_items.quantity+EXCLUDED.quantity, updated_at=now()
		WHERE cart_items.quantity+EXCLUDED.quantity<=99 RETURNING quantity`, cartID, menuItemID, quantity).Scan(&resultingQuantity)
	if errors.Is(err, pgx.ErrNoRows) {
		return Cart{}, ErrQuantityLimit
	}
	if err != nil {
		return Cart{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Cart{}, err
	}
	return s.GetCart(ctx, userID)
}

func (s *Store) SetCartItem(ctx context.Context, userID, menuItemID string, quantity int) (Cart, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE cart_items SET quantity=$3, updated_at=now()
		WHERE menu_item_id=$2 AND cart_id=(SELECT id FROM carts WHERE user_id=$1 AND status='active')`, userID, menuItemID, quantity)
	if err != nil {
		return Cart{}, err
	}
	if tag.RowsAffected() == 0 {
		return Cart{}, ErrNotFound
	}
	return s.GetCart(ctx, userID)
}

func (s *Store) DeleteCartItem(ctx context.Context, userID, menuItemID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `DELETE FROM cart_items WHERE menu_item_id=$2
		AND cart_id=(SELECT id FROM carts WHERE user_id=$1 AND status='active')`, userID, menuItemID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	_, err = tx.Exec(ctx, `UPDATE carts SET status='abandoned', updated_at=now()
		WHERE user_id=$1 AND status='active' AND NOT EXISTS(SELECT 1 FROM cart_items WHERE cart_id=carts.id)`, userID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type CheckoutInput struct {
	UserID          string
	IdempotencyKey  string
	DeliveryAddress json.RawMessage
	CustomerPhone   string
	Comment         string
}

func (s *Store) Checkout(ctx context.Context, input CheckoutInput) (Order, bool, error) {
	// Read Committed is intentional: the advisory lock serializes equal
	// idempotency keys, and each statement must see a transaction committed by
	// the previous lock holder. Cart rows provide serialization for other keys.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Order{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || ':' || $2, 0))`, input.UserID, input.IdempotencyKey); err != nil {
		return Order{}, false, err
	}
	var existingID string
	err = tx.QueryRow(ctx, `SELECT id FROM orders WHERE user_id=$1 AND idempotency_key=$2`, input.UserID, input.IdempotencyKey).Scan(&existingID)
	if err == nil {
		_ = tx.Rollback(ctx)
		order, getErr := s.GetOrder(ctx, existingID, input.UserID, "")
		return order, true, getErr
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Order{}, false, err
	}
	var cartID, establishmentID, webhookURL, currency string
	var deliveryFee, minimum int64
	err = tx.QueryRow(ctx, `SELECT c.id, c.establishment_id, e.webhook_url, e.currency,
		e.delivery_fee_minor, e.min_order_amount_minor
		FROM carts c JOIN establishments e ON e.id=c.establishment_id
		WHERE c.user_id=$1 AND c.status='active' AND c.expires_at>now()
		AND e.status='active' AND e.accepts_orders=true FOR UPDATE OF c`, input.UserID).
		Scan(&cartID, &establishmentID, &webhookURL, &currency, &deliveryFee, &minimum)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, false, ErrNotFound
	}
	if err != nil {
		return Order{}, false, err
	}
	rows, err := tx.Query(ctx, `SELECT mi.id, mi.external_id, mi.name, mi.price_minor,
		ci.quantity, mi.is_available FROM cart_items ci JOIN menu_items mi ON mi.id=ci.menu_item_id
		WHERE ci.cart_id=$1 FOR SHARE OF mi`, cartID)
	if err != nil {
		return Order{}, false, err
	}
	type checkoutItem struct {
		id, externalID, name string
		price                int64
		quantity             int
		available            bool
	}
	items := make([]checkoutItem, 0)
	lines := make([]money.Line, 0)
	for rows.Next() {
		var item checkoutItem
		if err := rows.Scan(&item.id, &item.externalID, &item.name, &item.price, &item.quantity, &item.available); err != nil {
			rows.Close()
			return Order{}, false, err
		}
		if !item.available {
			rows.Close()
			return Order{}, false, ErrUnavailable
		}
		lines = append(lines, money.Line{UnitPriceMinor: item.price, Quantity: item.quantity})
		items = append(items, item)
	}
	rows.Close()
	if len(items) == 0 {
		return Order{}, false, ErrNotFound
	}
	subtotal, err := money.Subtotal(lines)
	if err != nil {
		return Order{}, false, err
	}
	if subtotal < minimum {
		return Order{}, false, ErrMinimumOrder
	}
	var orderID string
	err = tx.QueryRow(ctx, `INSERT INTO orders(user_id, establishment_id, source_cart_id, subtotal_minor,
		delivery_fee_minor, total_minor, currency, delivery_address, customer_phone, customer_comment, idempotency_key)
		VALUES($1,$2,$3,$4,$5,$4::bigint+$5::bigint,$6,$7,$8,$9,$10) RETURNING id`, input.UserID, establishmentID,
		cartID, subtotal, deliveryFee, currency, input.DeliveryAddress, input.CustomerPhone, input.Comment, input.IdempotencyKey).Scan(&orderID)
	if err != nil {
		return Order{}, false, err
	}
	orderItems := make([]OrderItem, 0, len(items))
	for _, item := range items {
		lineTotal := item.price * int64(item.quantity)
		_, err = tx.Exec(ctx, `INSERT INTO order_items(order_id, menu_item_id, external_item_id, name,
			unit_price_minor, quantity, line_total_minor, currency) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
			orderID, item.id, item.externalID, item.name, item.price, item.quantity, lineTotal, currency)
		if err != nil {
			return Order{}, false, err
		}
		orderItems = append(orderItems, OrderItem{ExternalItemID: item.externalID, Name: item.name, UnitPriceMinor: item.price, Quantity: item.quantity, LineTotalMinor: lineTotal, Currency: currency})
	}
	_, err = tx.Exec(ctx, `INSERT INTO order_status_history(order_id, to_status, actor) VALUES($1,'pending_restaurant','customer')`, orderID)
	if err != nil {
		return Order{}, false, err
	}
	eventPayload, err := json.Marshal(map[string]any{
		"event_id": "", "event_type": "order.created", "order": map[string]any{
			"id": orderID, "status": "pending_restaurant", "items": orderItems,
			"subtotal_minor": subtotal, "delivery_fee_minor": deliveryFee, "total_minor": subtotal + deliveryFee,
			"currency": currency, "delivery_address": input.DeliveryAddress, "customer_phone": input.CustomerPhone,
			"customer_comment": input.Comment,
		},
	})
	if err != nil {
		return Order{}, false, err
	}
	var eventID string
	err = tx.QueryRow(ctx, `INSERT INTO outbox_events(aggregate_type, aggregate_id, event_type, payload, destination_url)
		VALUES('order',$1,'order.created',$2,$3) RETURNING id`, orderID, eventPayload, webhookURL).Scan(&eventID)
	if err != nil {
		return Order{}, false, err
	}
	// Put the database-generated event ID into the envelope without changing its identity.
	var envelope map[string]any
	if err := json.Unmarshal(eventPayload, &envelope); err != nil {
		return Order{}, false, err
	}
	envelope["event_id"] = eventID
	eventPayload, _ = json.Marshal(envelope)
	if _, err = tx.Exec(ctx, `UPDATE outbox_events SET payload=$2 WHERE id=$1`, eventID, eventPayload); err != nil {
		return Order{}, false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE carts SET status='checked_out', updated_at=now() WHERE id=$1`, cartID); err != nil {
		return Order{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Order{}, false, err
	}
	order, err := s.GetOrder(ctx, orderID, input.UserID, "")
	return order, false, err
}

func (s *Store) GetOrder(ctx context.Context, orderID, userID, establishmentID string) (Order, error) {
	query := `SELECT id, user_id, establishment_id, status, subtotal_minor, delivery_fee_minor,
		total_minor, currency, delivery_address, customer_phone, customer_comment, created_at, updated_at
		FROM orders WHERE id=$1`
	args := []any{orderID}
	if userID != "" {
		query += ` AND user_id=$2`
		args = append(args, userID)
	}
	if establishmentID != "" {
		query += ` AND establishment_id=$2`
		args = append(args, establishmentID)
	}
	var order Order
	err := s.pool.QueryRow(ctx, query, args...).Scan(&order.ID, &order.UserID, &order.EstablishmentID,
		&order.Status, &order.SubtotalMinor, &order.DeliveryFeeMinor, &order.TotalMinor, &order.Currency,
		&order.DeliveryAddress, &order.CustomerPhone, &order.CustomerComment, &order.CreatedAt, &order.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, err
	}
	rows, err := s.pool.Query(ctx, `SELECT external_item_id, name, unit_price_minor, quantity,
		line_total_minor, currency FROM order_items WHERE order_id=$1 ORDER BY id`, orderID)
	if err != nil {
		return Order{}, err
	}
	defer rows.Close()
	order.Items = make([]OrderItem, 0)
	for rows.Next() {
		var item OrderItem
		if err := rows.Scan(&item.ExternalItemID, &item.Name, &item.UnitPriceMinor, &item.Quantity, &item.LineTotalMinor, &item.Currency); err != nil {
			return Order{}, err
		}
		order.Items = append(order.Items, item)
	}
	return order, rows.Err()
}

func (s *Store) ListOrders(ctx context.Context, userID, establishmentID string) ([]Order, error) {
	query := `SELECT id FROM orders WHERE `
	var value string
	if userID != "" {
		query += `user_id=$1`
		value = userID
	} else {
		query += `establishment_id=$1`
		value = establishmentID
	}
	query += ` ORDER BY created_at DESC LIMIT 100`
	rows, err := s.pool.Query(ctx, query, value)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	orders := make([]Order, 0, len(ids))
	for _, id := range ids {
		order, err := s.GetOrder(ctx, id, userID, establishmentID)
		if err != nil {
			return nil, err
		}
		orders = append(orders, order)
	}
	return orders, nil
}

func (s *Store) CancelOrder(ctx context.Context, userID, orderID string) (Order, error) {
	var establishmentID string
	err := s.pool.QueryRow(ctx, `SELECT establishment_id FROM orders WHERE id=$1 AND user_id=$2`, orderID, userID).Scan(&establishmentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, err
	}
	return s.TransitionOrder(ctx, establishmentID, orderID, "pending_restaurant", "cancelled", "customer", "cancelled by customer")
}

func (s *Store) AuthenticatePartner(ctx context.Context, apiKey string) (string, error) {
	sum := sha256.Sum256([]byte(apiKey))
	hash := hex.EncodeToString(sum[:])
	var establishmentID string
	err := s.pool.QueryRow(ctx, `SELECT establishment_id FROM partner_credentials WHERE key_hash=$1 AND revoked_at IS NULL`, hash).Scan(&establishmentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return establishmentID, err
}

func (s *Store) UpsertMenuItem(ctx context.Context, establishmentID string, item MenuUpsert) (MenuItem, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MenuItem{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var establishmentCurrency string
	if err := tx.QueryRow(ctx, `SELECT currency FROM establishments WHERE id=$1`, establishmentID).Scan(&establishmentCurrency); err != nil {
		return MenuItem{}, err
	}
	if establishmentCurrency != item.Currency {
		return MenuItem{}, ErrCurrencyMismatch
	}
	var categoryID string
	err = tx.QueryRow(ctx, `INSERT INTO menu_categories(establishment_id, external_id, name)
		VALUES($1,$2,$2) ON CONFLICT(establishment_id, external_id) DO UPDATE SET name=EXCLUDED.name, updated_at=now()
		RETURNING id`, establishmentID, item.Category).Scan(&categoryID)
	if err != nil {
		return MenuItem{}, err
	}
	var result MenuItem
	err = tx.QueryRow(ctx, `INSERT INTO menu_items(establishment_id, category_id, external_id, name, description,
		price_minor, currency, is_available, revision) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT(establishment_id, external_id) DO UPDATE SET category_id=EXCLUDED.category_id,
		name=EXCLUDED.name, description=EXCLUDED.description, price_minor=EXCLUDED.price_minor,
		currency=EXCLUDED.currency, is_available=EXCLUDED.is_available, revision=EXCLUDED.revision, updated_at=now()
		WHERE menu_items.revision < EXCLUDED.revision
		RETURNING id, external_id, establishment_id, name, description, price_minor, currency, is_available, revision`,
		establishmentID, categoryID, item.ExternalID, item.Name, item.Description, item.PriceMinor,
		item.Currency, item.Available, item.Revision).Scan(&result.ID, &result.ExternalID, &result.EstablishmentID,
		&result.Name, &result.Description, &result.PriceMinor, &result.Currency, &result.IsAvailable, &result.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT mi.id, mi.external_id, mi.establishment_id, mi.name, mi.description,
			mi.price_minor, mi.currency, mi.is_available, mi.revision, COALESCE(mc.name,'')
			FROM menu_items mi LEFT JOIN menu_categories mc ON mc.id=mi.category_id
			WHERE mi.establishment_id=$1 AND mi.external_id=$2`, establishmentID, item.ExternalID).
			Scan(&result.ID, &result.ExternalID, &result.EstablishmentID, &result.Name, &result.Description,
				&result.PriceMinor, &result.Currency, &result.IsAvailable, &result.Revision, &result.Category)
	}
	if err != nil {
		return MenuItem{}, err
	}
	if result.Category == "" {
		result.Category = item.Category
	}
	if err := tx.Commit(ctx); err != nil {
		return MenuItem{}, err
	}
	return result, nil
}

func (s *Store) TransitionOrder(ctx context.Context, establishmentID, orderID, from, to, actor, reason string) (Order, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Order{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE orders SET status=$4, version=version+1, updated_at=now()
		WHERE id=$1 AND establishment_id=$2 AND status=$3`, orderID, establishmentID, from, to)
	if err != nil {
		return Order{}, err
	}
	if tag.RowsAffected() == 0 {
		return Order{}, ErrInvalidTransition
	}
	_, err = tx.Exec(ctx, `INSERT INTO order_status_history(order_id, from_status, to_status, actor, reason)
		VALUES($1,$2,$3,$4,$5)`, orderID, from, to, actor, reason)
	if err != nil {
		return Order{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Order{}, err
	}
	return s.GetOrder(ctx, orderID, "", establishmentID)
}

func (s *Store) ClaimOutbox(ctx context.Context) (OutboxEvent, error) {
	var event OutboxEvent
	err := s.pool.QueryRow(ctx, `UPDATE outbox_events SET attempts=attempts+1, available_at=now()+interval '30 seconds'
		WHERE id=(SELECT id FROM outbox_events WHERE delivered_at IS NULL AND available_at<=now()
		AND attempts<10 ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING id,event_type,payload,destination_url,attempts`).Scan(&event.ID, &event.EventType, &event.Payload, &event.DestinationURL, &event.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return OutboxEvent{}, ErrNotFound
	}
	return event, err
}

func (s *Store) CompleteOutbox(ctx context.Context, eventID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE outbox_events SET delivered_at=now(), last_error=NULL WHERE id=$1`, eventID)
	return err
}

func (s *Store) FailOutbox(ctx context.Context, eventID, message string, attempts int) error {
	delay := time.Duration(1<<min(attempts, 8)) * time.Second
	_, err := s.pool.Exec(ctx, `UPDATE outbox_events SET last_error=$2, available_at=$3 WHERE id=$1`, eventID, message, time.Now().Add(delay))
	return err
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
