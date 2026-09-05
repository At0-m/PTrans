BEGIN;

CREATE TABLE IF NOT EXISTS refunds (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES app_users(user_id),
    payment_id TEXT NOT NULL,
    amount BIGINT NOT NULL CHECK (amount > 0),
    currency TEXT NOT NULL CHECK (length(currency) = 3),
    status TEXT NOT NULL CHECK (status IN ('PENDING', 'PROCESSING', 'SUCCEEDED', 'FAILED')),
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    request_hash TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    check_failures INTEGER NOT NULL DEFAULT 0 CHECK (check_failures >= 0),
    retryable BOOLEAN NOT NULL DEFAULT false,
    manual_review BOOLEAN NOT NULL DEFAULT false,
    last_error TEXT NOT NULL DEFAULT '',
    next_action_at TIMESTAMPTZ NOT NULL,
    processing_started_at TIMESTAMPTZ,
    lease_token TEXT NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (user_id, idempotency_key),
    CHECK (NOT retryable OR status = 'FAILED'),
    CHECK (NOT manual_review OR status = 'PROCESSING')
);

CREATE INDEX IF NOT EXISTS idx_refunds_submit ON refunds (next_action_at, created_at)
WHERE status = 'PENDING' OR (status = 'FAILED' AND retryable);
CREATE INDEX IF NOT EXISTS idx_refunds_reconcile ON refunds (next_action_at, created_at)
WHERE status = 'PROCESSING' AND NOT manual_review;
CREATE INDEX IF NOT EXISTS idx_refunds_payment ON refunds (user_id, payment_id);
CREATE INDEX IF NOT EXISTS idx_refunds_user_created ON refunds (user_id, created_at DESC, id);

CREATE TABLE IF NOT EXISTS reconciliation_results (
    id TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL REFERENCES refunds(id),
    local_status TEXT NOT NULL,
    external_status TEXT NOT NULL,
    result TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    checked_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_reconciliation_operation ON reconciliation_results (operation_id, checked_at DESC, id);

CREATE OR REPLACE FUNCTION guard_refund_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.status <> 'PENDING' THEN
            RAISE EXCEPTION 'refund must start in PENDING' USING ERRCODE = '23514';
        END IF;
        NEW.version := 1;
        RETURN NEW;
    END IF;
    IF ROW(NEW.id, NEW.user_id, NEW.payment_id, NEW.amount, NEW.currency, NEW.idempotency_key, NEW.request_hash, NEW.created_at)
       IS DISTINCT FROM ROW(OLD.id, OLD.user_id, OLD.payment_id, OLD.amount, OLD.currency, OLD.idempotency_key, OLD.request_hash, OLD.created_at) THEN
        RAISE EXCEPTION 'refund identity and amount are immutable' USING ERRCODE = '23514';
    END IF;
    IF OLD.status <> NEW.status AND NOT (
        (OLD.status = 'PENDING' AND NEW.status = 'PROCESSING') OR
        (OLD.status = 'PROCESSING' AND NEW.status IN ('SUCCEEDED', 'FAILED')) OR
        (OLD.status = 'FAILED' AND NEW.status = 'PROCESSING' AND OLD.retryable)
    ) THEN
        RAISE EXCEPTION 'invalid refund transition: % -> %', OLD.status, NEW.status USING ERRCODE = '23514';
    END IF;
    IF OLD.status = 'FAILED' AND NOT OLD.retryable AND NEW.retryable THEN
        RAISE EXCEPTION 'permanent failure cannot become retryable' USING ERRCODE = '23514';
    END IF;
    NEW.version := OLD.version + 1;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION emit_refund_outbox() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    kind TEXT;
BEGIN
    IF TG_OP = 'INSERT' THEN
        kind := 'refund.created';
    ELSIF OLD.status <> NEW.status THEN
        kind := 'refund.' || lower(NEW.status);
    ELSIF NOT OLD.manual_review AND NEW.manual_review THEN
        kind := 'refund.manual_review';
    ELSIF OLD.manual_review AND NOT NEW.manual_review THEN
        kind := 'refund.recheck_requested';
    ELSE
        RETURN NEW;
    END IF;
    INSERT INTO outbox_events (
        id, user_id, event_type, aggregate_type, aggregate_id, payload,
        status, attempts, next_attempt_at, created_at, updated_at
    ) VALUES (
        'evt_' || NEW.id || '_' || NEW.version, NEW.user_id, kind, 'refund', NEW.id,
        to_jsonb(NEW) - 'lease_token' - 'lease_until' - 'request_hash',
        'pending', 0, NEW.updated_at, NEW.updated_at, NEW.updated_at
    );
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS refunds_guard ON refunds;
CREATE TRIGGER refunds_guard BEFORE INSERT OR UPDATE ON refunds
FOR EACH ROW EXECUTE FUNCTION guard_refund_transition();
DROP TRIGGER IF EXISTS refunds_outbox ON refunds;
CREATE TRIGGER refunds_outbox AFTER INSERT OR UPDATE ON refunds
FOR EACH ROW EXECUTE FUNCTION emit_refund_outbox();

COMMIT;
