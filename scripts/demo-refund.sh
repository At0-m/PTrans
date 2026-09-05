#!/usr/bin/env sh
set -eu
mode=${1:-ok}
case "$mode" in ok|timeout-after-success) ;; *) printf '%s\n' 'Supported modes: ok, timeout-after-success' >&2; exit 1 ;; esac
command -v python3 >/dev/null
if [ ! -f .env ]; then printf '%s\n' 'Run make init first' >&2; exit 1; fi
set -a
. ./.env
set +a
app=${APP_URL:-http://localhost:8080}
provider=${PROVIDER_URL:-http://localhost:8091}
user=${USER_ID:-alice}
./scripts/wait-ready.sh "$app"
before=$(curl -fsS "$provider/admin/stats" -H "Authorization: Bearer $PROVIDER_API_KEY")
previous=$(printf '%s' "$before" | python3 -c 'import json,sys;print(json.load(sys.stdin)["mode"])')
restore() {
    curl -fsS -X POST "$provider/admin/mode" -H "Authorization: Bearer $PROVIDER_API_KEY" \
        -H 'Content-Type: application/json' -d "{\"mode\":\"$previous\"}" >/dev/null || true
}
trap restore EXIT
trap 'exit 130' INT TERM
curl -fsS -X POST "$provider/admin/mode" -H "Authorization: Bearer $PROVIDER_API_KEY" \
    -H 'Content-Type: application/json' -d "{\"mode\":\"$mode\"}" >/dev/null
token=$(docker compose exec -T app ptrans-token -user "$user")
payment=$(docker compose exec -T -e ALLOW_DEMO_SEED=true app ptrans-seed -user "$user" \
    | python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])')
response=$(curl -fsS -X POST "$app/v1/refunds" -H "Authorization: Bearer $token" \
    -H 'Content-Type: application/json' -H "Idempotency-Key: refund-$payment" \
    -d "{\"payment_id\":\"$payment\",\"amount\":1500}")
refund=$(printf '%s' "$response" | python3 -c 'import json,sys;print(json.load(sys.stdin)["refund"]["id"])')
printf 'payment_id=%s\nrefund_id=%s\n' "$payment" "$refund"
i=0
while [ "$i" -lt 60 ]; do
    current=$(curl -fsS "$app/v1/refunds/$refund" -H "Authorization: Bearer $token")
    status=$(printf '%s' "$current" | python3 -c 'import json,sys;print(json.load(sys.stdin)["status"])')
    if [ "$status" = SUCCEEDED ]; then break; fi
    i=$((i+1))
    sleep 1
done
printf '%s\n' "$current"
if [ "$status" != SUCCEEDED ]; then printf '%s\n' 'Refund did not succeed; check refund-worker logs' >&2; exit 1; fi
curl -fsS "$app/v1/refunds/$refund/reconciliations" -H "Authorization: Bearer $token"
printf '\n'
printf 'Provider stats before: %s\n' "$before"
printf 'Provider stats after: '
curl -fsS "$provider/admin/stats" -H "Authorization: Bearer $PROVIDER_API_KEY"
printf '\n'
