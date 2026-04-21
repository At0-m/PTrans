CREATE TABLE IF NOT EXISTS app_users (
    user_id TEXT PRIMARY KEY,
    payments_table_name TEXT NOT NULL UNIQUE,
    webhook_subscriptions_table_name TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS idempotency_keys (
    user_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    payment_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_idempotency_keys_payment_id ON idempotency_keys(payment_id);

CREATE TABLE IF NOT EXISTS rate_limit_windows (
    scope_key TEXT NOT NULL,
    window_start TIMESTAMPTZ NOT NULL,
    hits INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (scope_key, window_start)
);
