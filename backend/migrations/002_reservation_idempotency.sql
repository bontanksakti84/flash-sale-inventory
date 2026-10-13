ALTER TABLE reservations
    ADD COLUMN IF NOT EXISTS idempotency_key TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS
    idx_reservations_idempotency_key
ON reservations (idempotency_key)
WHERE idempotency_key IS NOT NULL;
