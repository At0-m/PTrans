#!/usr/bin/env sh
set -eu
APP_URL=${1:-http://localhost:8080}
PAYMENT_ID=${2:?payment id is required}
TOKEN=${TOKEN:?set TOKEN to a JWT issued by ptrans-token}
curl -fsS -X POST "$APP_URL/v1/payments/$PAYMENT_ID/cancel" -H "Authorization: Bearer $TOKEN"
printf '\n'
