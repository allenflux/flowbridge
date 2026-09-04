#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${FLOWBRIDGE_BASE_URL:-http://127.0.0.1:7070}"
APIKEY="${FLOWBRIDGE_APIKEY:-}"
COUNT="${COUNT:-30}"
CONCURRENCY="${CONCURRENCY:-5}"
CURL_CONNECT_TIMEOUT="${CURL_CONNECT_TIMEOUT:-5}"
CURL_MAX_TIME="${CURL_MAX_TIME:-15}"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

echo "Health smoke test"
echo "BASE_URL=$BASE_URL COUNT=$COUNT CONCURRENCY=$CONCURRENCY"

curl_get() {
  local output="$1"
  local url="$2"
  if [[ -n "$APIKEY" ]]; then
    curl -sS --connect-timeout "$CURL_CONNECT_TIMEOUT" --max-time "$CURL_MAX_TIME" -o "$output" -w "%{http_code}" --header "Accept: application/json" --header "Apikey: $APIKEY" "$url"
  else
    curl -sS --connect-timeout "$CURL_CONNECT_TIMEOUT" --max-time "$CURL_MAX_TIME" -o "$output" -w "%{http_code}" --header "Accept: application/json" "$url"
  fi
}

run_one() {
  local index="$1"
  local code
  code="$(curl_get "$tmp_dir/health-$index.json" "$BASE_URL/healthz")"
  if [[ "$code" != "200" ]]; then
    echo "health request $index failed with HTTP $code"
    return 1
  fi

  code="$(curl_get "$tmp_dir/ready-$index.json" "$BASE_URL/readyz")"
  if [[ "$code" != "200" ]]; then
    echo "readiness request $index failed with HTTP $code"
    return 1
  fi

  code="$(curl_get "$tmp_dir/admin-$index.html" "$BASE_URL/admin/workflows")"
  if [[ "$code" != "200" ]]; then
    echo "admin request $index failed with HTTP $code"
    return 1
  fi
}

failures=0
pids=()
for i in $(seq 1 "$COUNT"); do
  (
    run_one "$i"
  ) &
  pids+=("$!")
  if (( ${#pids[@]} >= CONCURRENCY )); then
    for pid in "${pids[@]}"; do
      if ! wait "$pid"; then
        failures=$((failures + 1))
      fi
    done
    pids=()
  fi
done

if (( ${#pids[@]} > 0 )); then
  for pid in "${pids[@]}"; do
    if ! wait "$pid"; then
      failures=$((failures + 1))
    fi
  done
fi

if (( failures > 0 )); then
  echo "FAILED failures=$failures"
  exit 1
fi

echo "OK"
