#!/usr/bin/env sh
set -eu
APP_URL=${1:-http://localhost:8080}
TOKEN=${TOKEN:?set TOKEN to a JWT issued by ptrans-token}
TARGET_URL=${2:-http://mock-webhook:8090/hook}
body=$(python3 -c 'import json,sys;print(json.dumps({"url":sys.argv[1],"active":True}))' "$TARGET_URL")
curl -fsS -X POST "$APP_URL/v1/webhooks/subscriptions" \
    -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" -d "$body"
printf '\n'
