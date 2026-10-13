CREATE TABLE IF NOT EXISTS inventory (
    item_id TEXT PRIMARY KEY,
    total_stock BIGINT NOT NULL CHECK (total_stock >= 0),
    reserved_stock BIGINT NOT NULL DEFAULT 0 CHECK (reserved_stock >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT inventory_reserved_not_above_total CHECK (reserved_stock <= total_stock)
);

CREATE TABLE IF NOT EXISTS reservations (
    reservation_id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    item_id TEXT NOT NULL REFERENCES inventory(item_id),
    quantity BIGINT NOT NULL CHECK (quantity > 0),
    status TEXT NOT NULL CHECK (status IN ('active', 'confirmed', 'expired')),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    confirmed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_reservations_expiry
    ON reservations (item_id, expires_at)
    WHERE status = 'active';

CREATE INDEX IF NOT EXISTS idx_reservations_user_created
    ON reservations (user_id, created_at DESC);

-- Demo seed data; safe to re-run.
INSERT INTO inventory (item_id, total_stock, reserved_stock)
VALUES ('item_4021', 100, 0), ('item_1001', 25, 0), ('item_2001', 5, 0)
ON CONFLICT (item_id) DO NOTHING;
