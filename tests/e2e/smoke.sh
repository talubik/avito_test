#!/usr/bin/env bash
set -euo pipefail

KITCHEN_URL="${KITCHEN_URL:-http://localhost:8080}"
RESTAURANT_URL="${RESTAURANT_URL:-http://localhost:8081}"
USER_ID="smoke-$(date +%s)"

for _ in $(seq 1 40); do
  if curl --fail --silent "$KITCHEN_URL/health/ready" >/dev/null; then break; fi
  sleep 1
done

establishments="$(curl --fail --silent "$KITCHEN_URL/v1/establishments")"
establishment_id="$(grep -o '"id":"[^"]*"' <<<"$establishments" | sed -n '1p' | cut -d'"' -f4)"

for _ in $(seq 1 40); do
  menu="$(curl --fail --silent "$KITCHEN_URL/v1/establishments/$establishment_id/menu")"
  item_id="$(grep -o '"id":"[^"]*"' <<<"$menu" | sed -n '1p' | cut -d'"' -f4 || true)"
  if [[ -n "$item_id" ]]; then break; fi
  sleep 1
done

curl --fail --silent -X POST "$KITCHEN_URL/v1/cart/items" \
  -H 'Content-Type: application/json' -H "X-User-ID: $USER_ID" \
  -d "{\"menu_item_id\":\"$item_id\",\"quantity\":1}" >/dev/null

order="$(curl --fail --silent -X POST "$KITCHEN_URL/v1/orders" \
  -H 'Content-Type: application/json' -H "X-User-ID: $USER_ID" \
  -H "Idempotency-Key: smoke-$USER_ID" \
  -d '{"delivery_address":{"city":"Москва","street":"Лесная","house":"7"},"customer_phone":"+79990000000"}')"
order_id="$(grep -o '"id":"[^"]*"' <<<"$order" | sed -n '1p' | cut -d'"' -f4)"

for _ in $(seq 1 20); do
  if curl --fail --silent "$RESTAURANT_URL/v1/orders/$order_id" >/dev/null; then break; fi
  sleep 1
done

curl --fail --silent -X POST "$RESTAURANT_URL/v1/orders/$order_id/status" \
  -H 'Content-Type: application/json' -d '{"status":"accepted"}' >/dev/null

status="$(curl --fail --silent "$KITCHEN_URL/v1/orders/$order_id" -H "X-User-ID: $USER_ID" \
  | grep -o '"status":"[^"]*"' | sed -n '1p' | cut -d'"' -f4)"
test "$status" = accepted
printf 'Smoke test passed: order %s is accepted\n' "$order_id"
