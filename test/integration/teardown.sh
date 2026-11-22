#!/usr/bin/env bash
# Removes the test instance, its volumes, the network and a leftover runner
# container. Only the fmcp-it compose project and the runner container are touched.
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"
docker rm -f fmcp-it-runner >/dev/null 2>&1 || true
docker compose down -v --remove-orphans >&2 || true
docker network rm fmcp-it >/dev/null 2>&1 || true
exit 0
