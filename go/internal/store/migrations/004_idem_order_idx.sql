CREATE UNIQUE INDEX IF NOT EXISTS uq_orders_idempotency_key
  ON orders(idempotency_key) WHERE idempotency_key IS NOT NULL;
