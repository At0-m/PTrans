#!/usr/bin/env sh
set -eu
APP_URL=${1:-http://localhost:8080}
TOKEN=${TOKEN:?set TOKEN to a JWT issued by ptrans-token}
IDEMPOTENCY_KEY=${2:-order-1}
curl -fsS -X POST "$APP_URL/v1/payments" \
    -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" \
    -H "Idempotency-Key: $IDEMPOTENCY_KEY" -d '{"amount":1500,"currency":"RUB"}'
printf '\n'
