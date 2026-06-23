#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${FLOWBRIDGE_BASE_URL:-http://43.213.13.89:7070}"
APIKEY="${FLOWBRIDGE_APIKEY:-}"
SOURCE_PATH="${SOURCE_PATH:-http://allenflux.tech:8000/files/44e8e840819be8e0638087a2.jpg}"
SCENE_NAME="${SCENE_NAME:-goal_kick_portugal}"
TITLE="${TITLE:-auto generated curl}"
COUNT="${COUNT:-5}"
CONCURRENCY="${CONCURRENCY:-2}"
POLL_SECONDS="${POLL_SECONDS:-10}"
MAX_POLLS="${MAX_POLLS:-60}"

if [[ -z "$APIKEY" ]]; then
  echo "FLOWBRIDGE_APIKEY is required"
  exit 2
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT
ids_file="$tmp_dir/task_ids.txt"
touch "$ids_file"

echo "Workflow smoke test"
echo "BASE_URL=$BASE_URL COUNT=$COUNT CONCURRENCY=$CONCURRENCY SCENE_NAME=$SCENE_NAME"

curl_get() {
  local output="$1"
  local url="$2"
  curl -sS -o "$output" -w "%{http_code}" \
    --header "Accept: application/json" \
    --header "Apikey: $APIKEY" \
    "$url"
}

submit_one() {
  local index="$1"
  local response="$tmp_dir/submit-$index.json"
  local code
  code="$(curl -sS -o "$response" -w "%{http_code}" \
    --location "$BASE_URL/api/public/generate/undress/anime/video" \
    --header "Accept: application/json" \
    --header "Content-Type: application/x-www-form-urlencoded" \
    --header "Apikey: $APIKEY" \
    --data-urlencode "source_path=$SOURCE_PATH" \
    --data-urlencode "title=$TITLE" \
    --data-urlencode "fee=10" \
    --data-urlencode "incoming_prompt=" \
    --data-urlencode "scene_name=$SCENE_NAME" \
    --data-urlencode "output_format=video")"

  if [[ "$code" != "200" ]]; then
    echo "submit $index failed HTTP $code"
    cat "$response"
    return 1
  fi

  local task_id
  task_id="$(python3 - "$response" <<'PY'
import json, sys
with open(sys.argv[1], "r", encoding="utf-8") as f:
    data = json.load(f)
print(data.get("task_id", ""))
PY
)"
  if [[ -z "$task_id" ]]; then
    echo "submit $index did not return task_id"
    cat "$response"
    return 1
  fi
  echo "$task_id" >> "$ids_file"
  echo "submitted $index task_id=$task_id"
}

active=0
submit_failures=0
for i in $(seq 1 "$COUNT"); do
  (
    submit_one "$i"
  ) &
  active=$((active + 1))
  if (( active >= CONCURRENCY )); then
    if ! wait; then
      submit_failures=$((submit_failures + 1))
    fi
    active=0
  fi
done
if (( active > 0 )); then
  if ! wait; then
    submit_failures=$((submit_failures + 1))
  fi
fi

if (( submit_failures > 0 )); then
  echo "FAILED submit_failures=$submit_failures"
  exit 1
fi

echo "Polling tasks..."
for poll in $(seq 1 "$MAX_POLLS"); do
  pending=0
  failed=0
  success=0

  while IFS= read -r task_id; do
    [[ -z "$task_id" ]] && continue
    response="$tmp_dir/query-$task_id.json"
    code="$(curl_get "$response" "$BASE_URL/api/public/task?task_id=$task_id")"
    if [[ "$code" != "200" ]]; then
      echo "query $task_id failed HTTP $code"
      failed=$((failed + 1))
      continue
    fi
    status="$(python3 - "$response" <<'PY'
import json, sys
with open(sys.argv[1], "r", encoding="utf-8") as f:
    data = json.load(f)
print(data.get("status", ""))
PY
)"
    case "$status" in
      2) success=$((success + 1)) ;;
      -1) failed=$((failed + 1)) ;;
      *) pending=$((pending + 1)) ;;
    esac
  done < "$ids_file"

  echo "poll=$poll success=$success pending=$pending failed=$failed"
  if (( failed > 0 )); then
    echo "At least one workflow failed. Check admin page for detail."
    exit 1
  fi
  if (( pending == 0 )); then
    echo "OK all workflows succeeded"
    exit 0
  fi
  sleep "$POLL_SECONDS"
done

echo "Timed out waiting for completion. Service may still be healthy; check admin page."
exit 1
