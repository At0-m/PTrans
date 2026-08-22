CREATE TABLE IF NOT EXISTS outbox_events (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    aggregate_type TEXT NOT NULL,
    aggregate_id TEXT NOT NULL,
    payload JSONB NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'delivered', 'dead_letter')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL,
    locked_until TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_outbox_events_due
ON outbox_events (next_attempt_at ASC, created_at ASC)
WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_outbox_events_user_created_at
ON outbox_events (user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_outbox_events_aggregate
ON outbox_events (aggregate_type, aggregate_id);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL REFERENCES outbox_events(id) ON DELETE CASCADE,
    subscription_id TEXT NOT NULL,
    target_url TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('succeeded', 'failed')),
    http_status INTEGER,
    error TEXT,
    attempt INTEGER NOT NULL CHECK (attempt >= 1),
    duration_ms BIGINT NOT NULL CHECK (duration_ms >= 0),
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_event_status
ON webhook_deliveries (event_id, status);

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_subscription_created_at
ON webhook_deliveries (subscription_id, created_at DESC);
