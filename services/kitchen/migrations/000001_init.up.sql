CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TYPE establishment_status AS ENUM ('active', 'suspended');
CREATE TYPE cart_status AS ENUM ('active', 'checked_out', 'abandoned');
CREATE TYPE order_status AS ENUM (
    'pending_restaurant',
    'accepted',
    'preparing',
    'ready_for_delivery',
    'delivering',
    'delivered',
    'rejected',
    'cancelled'
);

CREATE TABLE establishments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    external_id text NOT NULL UNIQUE,
    slug text NOT NULL UNIQUE,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    status establishment_status NOT NULL DEFAULT 'active',
    accepts_orders boolean NOT NULL DEFAULT true,
    webhook_url text NOT NULL,
    min_order_amount_minor bigint NOT NULL DEFAULT 0 CHECK (min_order_amount_minor >= 0),
    delivery_fee_minor bigint NOT NULL DEFAULT 0 CHECK (delivery_fee_minor >= 0),
    currency char(3) NOT NULL DEFAULT 'RUB',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE partner_credentials (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    establishment_id uuid NOT NULL REFERENCES establishments(id) ON DELETE CASCADE,
    key_hash char(64) NOT NULL UNIQUE,
    label text NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE menu_categories (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    establishment_id uuid NOT NULL REFERENCES establishments(id) ON DELETE CASCADE,
    external_id text NOT NULL,
    name text NOT NULL,
    position integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (establishment_id, external_id)
);

CREATE TABLE menu_items (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    establishment_id uuid NOT NULL REFERENCES establishments(id) ON DELETE CASCADE,
    category_id uuid REFERENCES menu_categories(id),
    external_id text NOT NULL,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    price_minor bigint NOT NULL CHECK (price_minor >= 0),
    currency char(3) NOT NULL DEFAULT 'RUB',
    is_available boolean NOT NULL DEFAULT true,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (establishment_id, external_id)
);

CREATE INDEX menu_items_establishment_available_idx
    ON menu_items (establishment_id, is_available);

CREATE TABLE carts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id text NOT NULL,
    establishment_id uuid NOT NULL REFERENCES establishments(id),
    status cart_status NOT NULL DEFAULT 'active',
    expires_at timestamptz NOT NULL DEFAULT now() + interval '24 hours',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX one_active_cart_per_user_idx
    ON carts (user_id) WHERE status = 'active';

CREATE TABLE cart_items (
    cart_id uuid NOT NULL REFERENCES carts(id) ON DELETE CASCADE,
    menu_item_id uuid NOT NULL REFERENCES menu_items(id),
    quantity integer NOT NULL CHECK (quantity BETWEEN 1 AND 99),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (cart_id, menu_item_id)
);

CREATE TABLE orders (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id text NOT NULL,
    establishment_id uuid NOT NULL REFERENCES establishments(id),
    source_cart_id uuid REFERENCES carts(id),
    status order_status NOT NULL DEFAULT 'pending_restaurant',
    subtotal_minor bigint NOT NULL CHECK (subtotal_minor >= 0),
    delivery_fee_minor bigint NOT NULL CHECK (delivery_fee_minor >= 0),
    total_minor bigint NOT NULL CHECK (total_minor = subtotal_minor + delivery_fee_minor),
    currency char(3) NOT NULL,
    delivery_address jsonb NOT NULL,
    customer_phone text NOT NULL,
    customer_comment text NOT NULL DEFAULT '',
    idempotency_key text NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, idempotency_key)
);

CREATE INDEX orders_user_created_idx ON orders (user_id, created_at DESC);
CREATE INDEX orders_establishment_created_idx ON orders (establishment_id, created_at DESC);

CREATE TABLE order_items (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    menu_item_id uuid REFERENCES menu_items(id),
    external_item_id text NOT NULL,
    name text NOT NULL,
    unit_price_minor bigint NOT NULL CHECK (unit_price_minor >= 0),
    quantity integer NOT NULL CHECK (quantity > 0),
    line_total_minor bigint NOT NULL CHECK (line_total_minor = unit_price_minor * quantity),
    currency char(3) NOT NULL
);

CREATE TABLE order_status_history (
    id bigserial PRIMARY KEY,
    order_id uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    from_status order_status,
    to_status order_status NOT NULL,
    actor text NOT NULL,
    reason text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX order_status_history_order_idx
    ON order_status_history (order_id, created_at);

CREATE TABLE outbox_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    aggregate_type text NOT NULL,
    aggregate_id uuid NOT NULL,
    event_type text NOT NULL,
    payload jsonb NOT NULL,
    destination_url text NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX outbox_pending_idx
    ON outbox_events (available_at, created_at) WHERE delivered_at IS NULL;

INSERT INTO establishments (
    external_id, slug, name, description, webhook_url,
    min_order_amount_minor, delivery_fee_minor
) VALUES (
    'demo-cafe', 'demo-cafe', 'Демо Кафе',
    'Тестовое заведение Авито.Кухни',
    'http://demo-restaurant:8081/webhooks/v1/orders', 50000, 19900
);

INSERT INTO partner_credentials (establishment_id, key_hash, label)
SELECT id, 'fbf417589e5811819fb0e3bab01c241ec0499699e914a12c237eb29349a29b6d', 'demo-compose'
FROM establishments WHERE external_id = 'demo-cafe';

