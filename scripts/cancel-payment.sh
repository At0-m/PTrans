#!/usr/bin/env sh
set -eu

APP_URL="${1:-http://localhost:8080}"
USER_ID="${2:-alice}"
PAYMENT_ID="${3:?payment id is required}"

curl -fsS -X POST "$APP_URL/v1/payments/$PAYMENT_ID/cancel" -H "Content-Type: application/json" -H "X-User-ID: $USER_ID"
printf '\n'
