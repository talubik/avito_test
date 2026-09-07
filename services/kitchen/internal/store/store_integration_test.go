//go:build integration

package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

const testPartnerKey = "demo-partner-key-change-me"

func openIntegrationStore(t *testing.T) (*Store, string) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(s.Close)
	_, err = s.pool.Exec(ctx, `TRUNCATE TABLE
		outbox_events, order_status_history, order_items, orders,
		cart_items, carts, menu_items, menu_categories,
		partner_credentials, establishments RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("reset test database: %v", err)
	}
	var establishmentID string
	err = s.pool.QueryRow(ctx, `INSERT INTO establishments(
		external_id, slug, name, webhook_url, min_order_amount_minor, delivery_fee_minor, currency
	) VALUES('demo-cafe','demo-cafe','Demo Cafe','http://restaurant.test/webhook',50000,19900,'RUB') RETURNING id`).
		Scan(&establishmentID)
	if err != nil {
		t.Fatalf("seed establishment: %v", err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO partner_credentials(establishment_id,key_hash,label)
		VALUES($1,'fbf417589e5811819fb0e3bab01c241ec0499699e914a12c237eb29349a29b6d','integration')`, establishmentID)
	if err != nil {
		t.Fatalf("seed partner credential: %v", err)
	}
	return s, establishmentID
}

func seedMenuItem(t *testing.T, s *Store, establishmentID, externalID string, price int64) MenuItem {
	t.Helper()
	item, err := s.UpsertMenuItem(context.Background(), establishmentID, MenuUpsert{
		ExternalID: externalID, Category: "Pizza", Name: "Margherita",
		Description: "Test item", PriceMinor: price, Currency: "RUB",
		Available: true, Revision: 1,
	})
	if err != nil {
		t.Fatalf("seed menu item: %v", err)
	}
	return item
}

func checkoutInput(userID, key string) CheckoutInput {
	return CheckoutInput{
		UserID: userID, IdempotencyKey: key,
		DeliveryAddress: json.RawMessage(`{"city":"Moscow","street":"Lesnaya","house":"7"}`),
		CustomerPhone:   "+79990000000",
	}
}

func TestCartRulesIntegration(t *testing.T) {
	s, establishmentID := openIntegrationStore(t)
	item := seedMenuItem(t, s, establishmentID, "margherita", 59000)
	ctx := context.Background()

	cart, err := s.AddCartItem(ctx, "cart-user", item.ID, 2)
	if err != nil {
		t.Fatalf("add cart item: %v", err)
	}
	if cart.SubtotalMinor != 118000 || len(cart.Items) != 1 {
		t.Fatalf("unexpected cart: %+v", cart)
	}
	if _, err := s.AddCartItem(ctx, "cart-user", item.ID, 98); !errors.Is(err, ErrQuantityLimit) {
		t.Fatalf("quantity overflow error = %v; want ErrQuantityLimit", err)
	}
	cart, err = s.SetCartItem(ctx, "cart-user", item.ID, 3)
	if err != nil || cart.Items[0].Quantity != 3 {
		t.Fatalf("set cart quantity: cart=%+v err=%v", cart, err)
	}

	var otherEstablishmentID, otherItemID string
	err = s.pool.QueryRow(ctx, `INSERT INTO establishments(external_id,slug,name,webhook_url)
		VALUES('other','other','Other','http://other.test/webhook') RETURNING id`).Scan(&otherEstablishmentID)
	if err != nil {
		t.Fatalf("seed other establishment: %v", err)
	}
	err = s.pool.QueryRow(ctx, `INSERT INTO menu_items(establishment_id,external_id,name,price_minor,currency)
		VALUES($1,'other-item','Other item',60000,'RUB') RETURNING id`, otherEstablishmentID).Scan(&otherItemID)
	if err != nil {
		t.Fatalf("seed other item: %v", err)
	}
	if _, err := s.AddCartItem(ctx, "cart-user", otherItemID, 1); !errors.Is(err, ErrMixedEstablishments) {
		t.Fatalf("mixed establishment error = %v; want ErrMixedEstablishments", err)
	}
	if err := s.DeleteCartItem(ctx, "cart-user", item.ID); err != nil {
		t.Fatalf("delete last item: %v", err)
	}
	newCart, err := s.AddCartItem(ctx, "cart-user", otherItemID, 1)
	if err != nil || newCart.EstablishmentID != otherEstablishmentID {
		t.Fatalf("start cart for another establishment: cart=%+v err=%v", newCart, err)
	}
}

func TestCheckoutIdempotencyAndSnapshotIntegration(t *testing.T) {
	s, establishmentID := openIntegrationStore(t)
	item := seedMenuItem(t, s, establishmentID, "margherita", 59000)
	ctx := context.Background()
	if _, err := s.AddCartItem(ctx, "checkout-user", item.ID, 1); err != nil {
		t.Fatalf("add cart item: %v", err)
	}

	created, replayed, err := s.Checkout(ctx, checkoutInput("checkout-user", "checkout-key"))
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if replayed || created.Status != "pending_restaurant" || created.TotalMinor != 78900 {
		t.Fatalf("unexpected created order: %+v, replayed=%v", created, replayed)
	}
	replay, replayed, err := s.Checkout(ctx, checkoutInput("checkout-user", "checkout-key"))
	if err != nil || !replayed || replay.ID != created.ID {
		t.Fatalf("idempotent replay: order=%+v replayed=%v err=%v", replay, replayed, err)
	}

	_, err = s.UpsertMenuItem(ctx, establishmentID, MenuUpsert{
		ExternalID: "margherita", Category: "Pizza", Name: "Renamed",
		PriceMinor: 70000, Currency: "RUB", Available: true, Revision: 2,
	})
	if err != nil {
		t.Fatalf("update menu item: %v", err)
	}
	stored, err := s.GetOrder(ctx, created.ID, "checkout-user", "")
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if stored.Items[0].Name != "Margherita" || stored.Items[0].UnitPriceMinor != 59000 {
		t.Fatalf("order snapshot changed with menu: %+v", stored.Items[0])
	}
}

func TestConcurrentCheckoutUsesOneOrderIntegration(t *testing.T) {
	s, establishmentID := openIntegrationStore(t)
	item := seedMenuItem(t, s, establishmentID, "concurrent", 60000)
	ctx := context.Background()
	if _, err := s.AddCartItem(ctx, "concurrent-user", item.ID, 1); err != nil {
		t.Fatalf("add cart item: %v", err)
	}

	const workers = 8
	ids := make(chan string, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			result, _, err := s.Checkout(ctx, checkoutInput("concurrent-user", "same-key"))
			if err != nil {
				errs <- err
				return
			}
			ids <- result.ID
		}()
	}
	group.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Errorf("concurrent checkout: %v", err)
	}
	var expected string
	for id := range ids {
		if expected == "" {
			expected = id
		}
		if id != expected {
			t.Errorf("created multiple orders: %s and %s", expected, id)
		}
	}
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE user_id='concurrent-user'`).Scan(&count); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if count != 1 {
		t.Fatalf("order count = %d; want 1", count)
	}
}

func TestCheckoutValidationIntegration(t *testing.T) {
	t.Run("unavailable item", func(t *testing.T) {
		s, establishmentID := openIntegrationStore(t)
		item := seedMenuItem(t, s, establishmentID, "unavailable", 60000)
		if _, err := s.AddCartItem(context.Background(), "unavailable-user", item.ID, 1); err != nil {
			t.Fatalf("add cart item: %v", err)
		}
		_, err := s.UpsertMenuItem(context.Background(), establishmentID, MenuUpsert{
			ExternalID: "unavailable", Category: "Pizza", Name: "Unavailable",
			PriceMinor: 60000, Currency: "RUB", Available: false, Revision: 2,
		})
		if err != nil {
			t.Fatalf("disable item: %v", err)
		}
		if _, _, err := s.Checkout(context.Background(), checkoutInput("unavailable-user", "key")); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("checkout error = %v; want ErrUnavailable", err)
		}
	})

	t.Run("minimum amount", func(t *testing.T) {
		s, establishmentID := openIntegrationStore(t)
		item := seedMenuItem(t, s, establishmentID, "drink", 19000)
		if _, err := s.AddCartItem(context.Background(), "minimum-user", item.ID, 1); err != nil {
			t.Fatalf("add cart item: %v", err)
		}
		if _, _, err := s.Checkout(context.Background(), checkoutInput("minimum-user", "key")); !errors.Is(err, ErrMinimumOrder) {
			t.Fatalf("checkout error = %v; want ErrMinimumOrder", err)
		}
	})
}

func TestPartnerRevisionStatusAndOutboxIntegration(t *testing.T) {
	s, establishmentID := openIntegrationStore(t)
	ctx := context.Background()
	authenticatedID, err := s.AuthenticatePartner(ctx, testPartnerKey)
	if err != nil || authenticatedID != establishmentID {
		t.Fatalf("authenticate partner: id=%s err=%v", authenticatedID, err)
	}
	if _, err := s.AuthenticatePartner(ctx, "wrong-key"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong partner key error = %v", err)
	}
	item := seedMenuItem(t, s, establishmentID, "revisioned", 60000)
	stale, err := s.UpsertMenuItem(ctx, establishmentID, MenuUpsert{
		ExternalID: "revisioned", Category: "Pizza", Name: "Stale",
		PriceMinor: 1, Currency: "RUB", Available: false, Revision: 1,
	})
	if err != nil || stale.ID != item.ID || stale.PriceMinor != 60000 || !stale.IsAvailable {
		t.Fatalf("stale revision overwrote item: item=%+v err=%v", stale, err)
	}
	if _, err := s.UpsertMenuItem(ctx, establishmentID, MenuUpsert{
		ExternalID: "currency", Category: "Pizza", Name: "Currency",
		PriceMinor: 1, Currency: "USD", Available: true, Revision: 1,
	}); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("currency mismatch error = %v", err)
	}

	if _, err := s.AddCartItem(ctx, "status-user", item.ID, 1); err != nil {
		t.Fatalf("add cart item: %v", err)
	}
	created, _, err := s.Checkout(ctx, checkoutInput("status-user", "status-key"))
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if _, err := s.TransitionOrder(ctx, establishmentID, created.ID, "accepted", "preparing", "restaurant", ""); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("invalid transition error = %v", err)
	}
	accepted, err := s.TransitionOrder(ctx, establishmentID, created.ID, "pending_restaurant", "accepted", "restaurant", "")
	if err != nil || accepted.Status != "accepted" {
		t.Fatalf("accept order: order=%+v err=%v", accepted, err)
	}

	event, err := s.ClaimOutbox(ctx)
	if err != nil || event.EventType != "order.created" || event.Attempts != 1 {
		t.Fatalf("claim outbox: event=%+v err=%v", event, err)
	}
	if err := s.FailOutbox(ctx, event.ID, "temporary failure", event.Attempts); err != nil {
		t.Fatalf("fail outbox: %v", err)
	}
	var lastError string
	var availableAt time.Time
	if err := s.pool.QueryRow(ctx, `SELECT last_error, available_at FROM outbox_events WHERE id=$1`, event.ID).Scan(&lastError, &availableAt); err != nil {
		t.Fatalf("read failed outbox: %v", err)
	}
	if lastError != "temporary failure" || !availableAt.After(time.Now()) {
		t.Fatalf("outbox retry was not scheduled: error=%q available_at=%s", lastError, availableAt)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE outbox_events SET available_at=now() WHERE id=$1`, event.ID); err != nil {
		t.Fatalf("make outbox available: %v", err)
	}
	event, err = s.ClaimOutbox(ctx)
	if err != nil || event.Attempts != 2 {
		t.Fatalf("reclaim outbox: event=%+v err=%v", event, err)
	}
	if err := s.CompleteOutbox(ctx, event.ID); err != nil {
		t.Fatalf("complete outbox: %v", err)
	}
	if _, err := s.ClaimOutbox(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("claim completed outbox error = %v; want ErrNotFound", err)
	}
	var historyCount int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM order_status_history WHERE order_id=$1`, created.ID).Scan(&historyCount); err != nil {
		t.Fatalf("count history: %v", err)
	}
	if historyCount != 2 {
		t.Fatalf("status history count = %d; want 2", historyCount)
	}
}
