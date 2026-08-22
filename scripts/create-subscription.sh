#!/usr/bin/env sh
set -eu

APP_URL="${1:-http://localhost:8080}"
USER_ID="${2:-alice}"
TARGET_URL="${3:-http://mock-webhook:8090/hook}"

curl -fsS -X POST "$APP_URL/v1/webhooks/subscriptions" -H "Content-Type: application/json" -H "X-User-ID: $USER_ID" -d "{\"url\": \"$TARGET_URL\", \"active\": true}"
printf '\n'
