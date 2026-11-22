#!/usr/bin/env bash
# Starts a fresh Forgejo 17.0.2 test instance and prints the environment for
# the integration tests. If FORGEJO_TEST_URL and FORGEJO_TEST_TOKEN are already
# set, an existing instance is reused and nothing is started.
#
#   eval "$(test/integration/bootstrap.sh)"
#
# Diagnostics go to stderr; stdout carries only the export line.
set -euo pipefail

if [[ -n "${FORGEJO_TEST_URL:-}" && -n "${FORGEJO_TEST_TOKEN:-}" ]]; then
  echo "bootstrap: reusing instance at ${FORGEJO_TEST_URL}" >&2
  printf 'export FORGEJO_TEST_URL=%q FORGEJO_TEST_TOKEN=%q\n' "$FORGEJO_TEST_URL" "$FORGEJO_TEST_TOKEN"
  exit 0
fi

cd "$(dirname "${BASH_SOURCE[0]}")"
export FORGEJO_TEST_PORT="${FORGEJO_TEST_PORT:-3000}"

docker compose up -d --wait >&2

# Random password, never printed or stored: the token is the only credential.
PASSWORD="$(head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 24)"
# -T: no TTY, so no stray CR ends up in the output.
docker compose exec -T -u git forgejo forgejo admin user create --admin \
  --username it-admin --password "$PASSWORD" \
  --email it-admin@example.invalid --must-change-password=false >&2
unset PASSWORD

TOKEN="$(docker compose exec -T -u git forgejo forgejo admin user generate-access-token \
  --username it-admin --scopes all --raw --token-name it | tr -d '\r\n')"
if [[ -z "$TOKEN" ]]; then
  echo "bootstrap: could not create an access token" >&2
  exit 1
fi

printf 'export FORGEJO_TEST_URL=%q FORGEJO_TEST_TOKEN=%q\n' "http://localhost:${FORGEJO_TEST_PORT}" "$TOKEN"
