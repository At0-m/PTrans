#!/usr/bin/env sh
set -eu
url=${1:-http://localhost:8080}
i=0
while [ "$i" -lt 60 ]; do
    if curl -fsS --max-time 2 "$url/readyz" >/dev/null 2>&1; then
        printf '%s\n' 'API is ready'
        exit 0
    fi
    i=$((i+1))
    sleep 1
done
printf '%s\n' 'API did not become ready; check docker compose logs app migrate' >&2
exit 1
