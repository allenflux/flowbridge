#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${FLOWBRIDGE_BASE_URL:-http://127.0.0.1:7070}"
COUNT="${COUNT:-30}"
CONCURRENCY="${CONCURRENCY:-5}"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

echo "Health smoke test"
echo "BASE_URL=$BASE_URL COUNT=$COUNT CONCURRENCY=$CONCURRENCY"

run_one() {
  local index="$1"
  local code
  code="$(curl -sS -o "$tmp_dir/health-$index.json" -w "%{http_code}" "$BASE_URL/healthz")"
  if [[ "$code" != "200" ]]; then
    echo "health request $index failed with HTTP $code"
    return 1
  fi

  code="$(curl -sS -o "$tmp_dir/admin-$index.html" -w "%{http_code}" "$BASE_URL/admin/workflows")"
  if [[ "$code" != "200" ]]; then
    echo "admin request $index failed with HTTP $code"
    return 1
  fi
}

active=0
failures=0
for i in $(seq 1 "$COUNT"); do
  run_one "$i" || failures=$((failures + 1)) &
  active=$((active + 1))
  if (( active >= CONCURRENCY )); then
    wait -n || failures=$((failures + 1))
    active=$((active - 1))
  fi
done

wait || failures=$((failures + 1))

if (( failures > 0 )); then
  echo "FAILED failures=$failures"
  exit 1
fi

echo "OK"
