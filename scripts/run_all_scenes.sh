#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${FLOWBRIDGE_BASE_URL:-http://127.0.0.1:8080}"
TEN_EROS_URL="${FLOWBRIDGE_10EROS_URL:-${BASE_URL%/}/api/public/generate/10eros/image-to-video}"
LEGACY_URL="${FLOWBRIDGE_LEGACY_URL:-${BASE_URL%/}/api/public/generate/undress/anime/video}"
APIKEY="${FLOWBRIDGE_APIKEY:-}"
SOURCE_PATH="${SOURCE_PATH:-http://allenflux.tech:8000/files/44e8e840819be8e0638087a2.jpg}"
TARGET_PATH="${TARGET_PATH:-}"
TITLE="auto generated curl"
FEE="10"
OUTPUT_FORMAT="video"
VIDEO_FORMAT="${VIDEO_FORMAT:-video/h264-mp4}"
AUDIO_ENABLED="${AUDIO_ENABLED:-true}"
IS_ENCRYPT="${IS_ENCRYPT:-false}"
IS_WATERMARK="${IS_WATERMARK:-true}"
QWEN_INCOMING_PROMPT="${QWEN_INCOMING_PROMPT:-}"
WAN_INCOMING_PROMPT="${WAN_INCOMING_PROMPT:-}"

if [[ -z "$APIKEY" ]]; then
  echo "FLOWBRIDGE_APIKEY is required"
  exit 2
fi
if [[ -z "$TARGET_PATH" ]]; then
  echo "TARGET_PATH is required for gay_doggy_10eros and lesbian_kiss_10eros"
  exit 2
fi

SCENES=(
  "gay_doggy_10eros"
  "gay_cumshot_10eros"
  "gay_anal_creampie_10eros"
  "lesbian_kiss_10eros"
  "nicole_robin_real"
  "disney_real_anime_greet"
  "wishing_you_prosperity_in_2026_disney"
  "congratulations_on_getting_rich_3d_2026"
  "disney_video_wall"
  "tv_wall_with_chinese_style_3d"
  "tv_wall_studio_ghibli"
  "tv_wall_mainstream_anime_manga_style"
  "multiple_oral_sex_at_disney"
  "multiple_oral_sex_retro_style"
  "multiple_oral_sex_chinese_style_3d"
  "multiple_oral_sex_mainstream_anime_manga"
  "fox_nick_fury"
  "pregnancy_and_egg_laying_disney"
  "pregnancy_and_egg_laying_chinese_style_3d"
  "pregnancy_and_egg_laying_mainstream_anime_and_manga"
  "python_coiled_around_man"
  "python_coiled_around_woman"
  "male_rabbit_officer"
  "flying_horse_in_the_sky_retro_style"
  "flying_horse_soars_into_the_sky_mainstream_anime_manga"
  "new_years_party_disney"
  "new_year_party_chinese_style_3d"
  "new_years_party_mainstream_anime"
  "disney_sends_new_year_greetings_in_reallife_and_animated_form"
  "reallife_and_anime_new_year_greetings_retro_style"
  "liveaction_and_animated_new_year_greetings_in_a_traditional_chinese_style_3d"
  "new_year_greetings_from_real_people_and_anime_characters_mainstream_anime_culture"
  "smoothmix_finger_sex"
  "sakura_witch_video_version"
  "goal_kick_portugal"
  "goal_kick_argentina"
  "transition_challenge_portugal"
  "transition_challenge_argentina"
)

for item in "${SCENES[@]}"; do
  scene_name="$item"
  scene_target_path=""
  request_url="$LEGACY_URL"
  scene_format_args=(--data-urlencode "scene_name=$scene_name")
  if [[ "$scene_name" == "gay_doggy_10eros" || "$scene_name" == "gay_cumshot_10eros" || "$scene_name" == "gay_anal_creampie_10eros" || "$scene_name" == "lesbian_kiss_10eros" ]]; then
    request_url="$TEN_EROS_URL"
  else
    scene_format_args+=(--data-urlencode "output_format=$OUTPUT_FORMAT")
  fi
  if [[ "$scene_name" == "gay_doggy_10eros" || "$scene_name" == "lesbian_kiss_10eros" ]]; then
    scene_target_path="$TARGET_PATH"
  fi

  echo "===== ${scene_name} ====="
  curl --fail --show-error --location "$request_url" \
    --header "Accept: application/json" \
    --header "Content-Type: application/x-www-form-urlencoded" \
    --header "Apikey: $APIKEY" \
    --data-urlencode "source_path=$SOURCE_PATH" \
    --data-urlencode "target_path=$scene_target_path" \
    --data-urlencode "title=$TITLE" \
    --data-urlencode "fee=$FEE" \
    --data-urlencode "incoming_prompt=" \
    --data-urlencode "qwen_incoming_prompt=$QWEN_INCOMING_PROMPT" \
    --data-urlencode "wan_incoming_prompt=$WAN_INCOMING_PROMPT" \
    "${scene_format_args[@]}" \
    --data-urlencode "video_format=$VIDEO_FORMAT" \
    --data-urlencode "audio_enabled=$AUDIO_ENABLED" \
    --data-urlencode "is_watermark=$IS_WATERMARK" \
    --data-urlencode "is_encrypt=$IS_ENCRYPT"
  echo
done
