#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
test -s "${WEB_TOOLS_TOKEN_PATH:-/volume2/docker/ayden/shared-secrets/web_tools_token}"
docker network inspect nas-proxy >/dev/null
if ! docker network inspect aydens-web-tools >/dev/null 2>&1; then
  docker network create aydens-web-tools >/dev/null
fi
docker compose config --quiet
docker compose build --pull
docker compose up -d --wait --wait-timeout 90
docker compose ps
