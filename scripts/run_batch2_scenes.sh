#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${FLOWBRIDGE_BASE_URL:-http://127.0.0.1:8080}"
BATCH2_URL="${FLOWBRIDGE_BATCH2_URL:-${BASE_URL%/}/api/public/generate/10eros/batch-2/image-to-video}"
APIKEY="${FLOWBRIDGE_APIKEY:-}"
SOURCE_PATH="${SOURCE_PATH:-}"
TARGET_PATH="${TARGET_PATH:-}"
TITLE_PREFIX="${TITLE_PREFIX:-10Eros batch-2}"
FEE="${FEE:-10}"
QWEN_INCOMING_PROMPT="${QWEN_INCOMING_PROMPT:-}"
WAN_INCOMING_PROMPT="${WAN_INCOMING_PROMPT:-}"
VIDEO_FORMAT="${VIDEO_FORMAT:-video/h264-mp4}"
AUDIO_ENABLED="${AUDIO_ENABLED:-true}"
IS_WATERMARK="${IS_WATERMARK:-true}"
IS_ENCRYPT="${IS_ENCRYPT:-false}"
CURL_CONNECT_TIMEOUT="${CURL_CONNECT_TIMEOUT:-10}"
CURL_MAX_TIME="${CURL_MAX_TIME:-60}"

if [[ -z "$APIKEY" ]]; then
  echo "FLOWBRIDGE_APIKEY is required" >&2
  exit 2
fi
if [[ -z "$SOURCE_PATH" ]]; then
  echo "SOURCE_PATH is required" >&2
  exit 2
fi
if [[ -z "$TARGET_PATH" ]]; then
  echo "TARGET_PATH is required because this run includes two-person scenes" >&2
  exit 2
fi

# The value after the separator is the scene's exact input-image requirement.
# Keep this list in sync with docs/10eros-batch2-image-to-video-api.md.
SCENES=(
  "gay_oral_cumshot_10eros|two"
  "lesbian_cunnilingus_10eros|two"
  "gay_bondage_10eros|two"
  "gay_crossdressing_10eros|one"
  "gay_butt_slap_10eros|one"
  "gay_kneeling_doggy_10eros|two"
  "lesbian_strap_on_10eros|two"
  "lesbian_doggy_10eros|two"
  "lesbian_cowgirl_10eros|two"
  "gay_bar_doggy_10ero|two"
)

failures=0
for entry in "${SCENES[@]}"; do
  IFS='|' read -r scene_name input_mode <<< "$entry"
  curl_args=(
    --fail-with-body
    --show-error
    --silent
    --connect-timeout "$CURL_CONNECT_TIMEOUT"
    --max-time "$CURL_MAX_TIME"
    --request POST
    --url "$BATCH2_URL"
    --header "Accept: application/json"
    --header "Content-Type: application/x-www-form-urlencoded"
    --header "Apikey: $APIKEY"
    --data-urlencode "source_path=$SOURCE_PATH"
    --data-urlencode "scene_name=$scene_name"
    --data-urlencode "title=$TITLE_PREFIX: $scene_name"
    --data-urlencode "fee=$FEE"
    --data-urlencode "qwen_incoming_prompt=$QWEN_INCOMING_PROMPT"
    --data-urlencode "wan_incoming_prompt=$WAN_INCOMING_PROMPT"
    --data-urlencode "video_format=$VIDEO_FORMAT"
    --data-urlencode "audio_enabled=$AUDIO_ENABLED"
    --data-urlencode "is_watermark=$IS_WATERMARK"
    --data-urlencode "is_encrypt=$IS_ENCRYPT"
  )

  case "$input_mode" in
    two)
      curl_args+=(--data-urlencode "target_path=$TARGET_PATH")
      ;;
    one)
      # Deliberately omit target_path for single-person scenes.
      ;;
    *)
      echo "Invalid input mode '$input_mode' for scene '$scene_name'" >&2
      exit 2
      ;;
  esac

  echo "===== $scene_name ($input_mode-person) ====="
  if ! curl "${curl_args[@]}"; then
    echo >&2
    echo "Submission failed: $scene_name" >&2
    failures=$((failures + 1))
  fi
  echo
done

if (( failures > 0 )); then
  echo "FAILED: $failures of ${#SCENES[@]} submissions failed" >&2
  exit 1
fi

echo "OK: submitted all ${#SCENES[@]} batch-2 scenes"
