#!/usr/bin/env sh
set -eu

APP_URL="${1:-http://localhost:8080}"
USER_ID="${2:-alice}"
IDEMPOTENCY_KEY="${3:-order-1}"

curl -fsS -X POST "$APP_URL/v1/payments" -H "Content-Type: application/json" -H "X-User-ID: $USER_ID"  -H "Idempotency-Key: $IDEMPOTENCY_KEY" -d '{"amount": 1500, "currency": "RUB"}'
printf '\n'
