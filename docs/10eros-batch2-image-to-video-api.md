# 10Eros 第二批生图转视频组合接口

## 接口概览

```text
POST https://flowbridge.inaiai.com/api/public/generate/10eros/batch-2/image-to-video
Content-Type: application/x-www-form-urlencoded
Apikey: <用户 API Key>
```

该入口与第一批 `/api/public/generate/10eros/image-to-video` 完全隔离。每个任务按以下顺序执行：

1. `POST /api/public/generate/qwen/two-image`
2. 等待中间图成功，将其 URL 作为 `source_path` 提交到 `POST /api/public/generate/videos/scenes/8s/ltx`

两个后端步骤使用相同的 `scene_name`。中间图固定发送 `is_encrypt=false`、`is_watermark=false`；加密、水印、视频格式、音频和回调设置只应用到最终 LTX 视频。

## 场景与输入图片

| 场景 | 中文 | 输入 |
| --- | --- | --- |
| `gay_oral_cumshot_10eros` | 男同口内射精 | `source_path` |
| `lesbian_cunnilingus_10eros` | 女同舔逼 | `source_path` |
| `gay_bondage_10eros` | 男同捆绑式性虐 | `source_path` |
| `gay_crossdressing_10eros` | 女装大佬 | `source_path` |
| `gay_butt_slap_10eros` | 拍臀诱惑（男） | `source_path` |
| `gay_kneeling_doggy_10eros` | 男同跪姿后入式 | `source_path` |
| `lesbian_strap_on_10eros` | 双女假阳具 | `source_path` + `target_path` |
| `lesbian_doggy_10eros` | 双女后入式 | `source_path` + `target_path` |
| `lesbian_cowgirl_10eros` | 双女骑乘式 | `source_path` + `target_path` |
| `gay_bar_doggy_10eros` | 男同酒吧后入式 | `source_path` + `target_path` |

> `gay_bar_doggy_10eros` 结尾包含 `s`；两个后端步骤均使用这个完整场景名。

## 参数

| 参数 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `source_path` | string | 是 | - | 第一张输入图片 URL |
| `target_path` | string | 条件必填 | - | 仅上表后 4 个双图场景必填；前 6 个单图场景省略，误传也不会向 Qwen 转发 |
| `scene_name` | string | 是 | - | 仅支持上表 10 个值 |
| `video_scene_name` | string | 否 | 与 `scene_name` 相同 | 建议省略；如果传入必须等于 `scene_name` |
| `incoming_prompt` | string | 否 | 空 | 两个步骤共享的兜底提示词 |
| `qwen_incoming_prompt` | string | 否 | `incoming_prompt` | 第一步 Qwen 提示词 |
| `wan_incoming_prompt` | string | 否 | `incoming_prompt` | 第二步 LTX 提示词 |
| `audio_enabled` | boolean | 否 | `true` | 最终 LTX 视频音频开关 |
| `video_format` | string | 否 | `video/h264-mp4` | 支持 `video/h264-mp4`、`video/h265-mp4` |
| `is_watermark` | boolean | 否 | `true` | 仅控制最终视频；兼容 `watermark` 别名 |
| `is_encrypt` | boolean | 否 | `false` | 仅控制最终视频 |
| `bid` | string | 否 | 空 | 业务编号 |
| `app_id` | string | 否 | 空 | 应用编号 |
| `fee` | string | 否 | `10` | 费用参数 |
| `title` | string | 否 | 空 | 任务标题 |
| `hash_key` | string | 否 | 空 | 仅向最终 LTX 步骤透传 |
| `notify_url` | string | 否 | 空 | 仅发送给最终 LTX 步骤，避免重复回调 |
| `task_id` | string | 否 | 自动生成 | 建议传唯一值，便于幂等查询和故障排查 |

## 双图场景示例

```bash
curl --location 'https://flowbridge.inaiai.com/api/public/generate/10eros/batch-2/image-to-video' \
  --header 'Accept: application/json' \
  --header 'Content-Type: application/x-www-form-urlencoded' \
  --header 'Apikey: <用户 API Key>' \
  --data-urlencode 'source_path=https://example.com/person-1.jpg' \
  --data-urlencode 'target_path=https://example.com/person-2.jpg' \
  --data-urlencode 'scene_name=lesbian_strap_on_10eros' \
  --data-urlencode 'audio_enabled=true' \
  --data-urlencode 'video_format=video/h264-mp4' \
  --data-urlencode 'is_watermark=true' \
  --data-urlencode 'is_encrypt=false' \
  --data-urlencode 'task_id=batch2_lesbian_strap_on_001'
```

## 单图场景示例

```bash
curl --location 'https://flowbridge.inaiai.com/api/public/generate/10eros/batch-2/image-to-video' \
  --header 'Accept: application/json' \
  --header 'Content-Type: application/x-www-form-urlencoded' \
  --header 'Apikey: <用户 API Key>' \
  --data-urlencode 'source_path=https://example.com/person.jpg' \
  --data-urlencode 'scene_name=gay_oral_cumshot_10eros' \
  --data-urlencode 'audio_enabled=true' \
  --data-urlencode 'video_format=video/h264-mp4' \
  --data-urlencode 'is_watermark=true' \
  --data-urlencode 'is_encrypt=false' \
  --data-urlencode 'task_id=batch2_gay_oral_001'
```

提交成功返回 `task_type: "10eros_batch2_image_to_video"`。状态查询继续使用：

```text
GET /api/public/task?task_id=<task_id>
POST /api/public/task
POST /api/public/task/details
```

## HTTP 结果

- `200`：任务已持久化并进入队列，初始 `status=0`。
- `400`：缺少 `Apikey`/`source_path`、双图场景缺少 `target_path`、场景不属于第二批、`video_scene_name` 与 `scene_name` 不一致或视频格式非法。
- `409`：调用方指定的 `task_id` 已存在。
- `503`：持久任务积压达到上限，响应包含 `Retry-After`。

## 200 响应示例（单图场景）

```json
{
  "uuid": "batch2_gay_oral_001",
  "task_id": "batch2_gay_oral_001",
  "status": 0,
  "task_type": "10eros_batch2_image_to_video",
  "source_path": "https://example.com/person.jpg",
  "scene_name": "gay_oral_cumshot_10eros",
  "video_format": "video/h264-mp4",
  "audio_enabled": true,
  "is_watermark": true,
  "is_encrypt": false,
  "current_step": "anime_image",
  "created_at": "2026-09-22T08:36:54Z",
  "updated_at": "2026-09-22T08:36:54Z"
}
```
