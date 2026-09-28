CREATE INDEX IF NOT EXISTS idx_orders_status ON orders(status);
CREATE INDEX IF NOT EXISTS idx_orders_restaurant ON orders(restaurant_id);
CREATE INDEX IF NOT EXISTS idx_riders_status ON riders(status);
CREATE INDEX IF NOT EXISTS idx_assignments_order ON assignments(order_id);
CREATE INDEX IF NOT EXISTS idx_assignments_rider ON assignments(rider_id);
CREATE INDEX IF NOT EXISTS idx_assignments_status_expires ON assignments(status, expires_at);
CREATE INDEX IF NOT EXISTS idx_proposals_status ON proposals(status);
CREATE INDEX IF NOT EXISTS idx_events_order_created ON assignment_events(order_id, created_at);

-- At most one active assignment per order.
CREATE UNIQUE INDEX IF NOT EXISTS uq_active_assignment_per_order
  ON assignments(order_id)
  WHERE status IN ('offered','accepted');

-- Capacity 1: at most one active assignment per rider.
CREATE UNIQUE INDEX IF NOT EXISTS uq_active_assignment_per_rider
  ON assignments(rider_id)
  WHERE status IN ('offered','accepted');
