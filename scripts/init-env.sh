#!/usr/bin/env sh
set -eu
umask 077
if [ ! -f .env ]; then cp .env.example .env; fi
for key in JWT_SECRET PROVIDER_API_KEY; do
    value=$(sed -n "s/^$key=//p" .env | head -n 1)
    case "$value" in
        ''|replace-with-*)
            secret=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
            if grep -q "^$key=" .env; then
                sed "s/^$key=.*/$key=$secret/" .env > .env.tmp
                mv .env.tmp .env
            else
                printf '\n%s=%s\n' "$key" "$secret" >> .env
            fi
            ;;
    esac
done
chmod 600 .env
printf '%s\n' 'Local .env is ready; existing non-placeholder keys were preserved'
