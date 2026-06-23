# FlowBridge

FlowBridge runs async workflow aggregation tasks and exposes bridge-owned task status.

## Run

Edit `config.json` first:

```json
{
  "addr": ":8080",
  "backend_base_url": "http://your-backend:8000",
  "db_path": "flowbridge.db",
  "worker_concurrency": 8,
  "worker_queue_size": 10000,
  "max_poll_errors": 10,
  "poll_interval": "3s",
  "task_timeout": "30m",
  "http_timeout": "30s"
}
```

Then start:

```bash
go run .
```

## Docker Compose

```bash
docker compose up -d --build
```

The service listens on:

```text
http://127.0.0.1:8080
```

SQLite data is persisted under:

```text
Docker volume: flowbridge-data
```

Do not use `docker compose down -v` unless you intentionally want to delete the DB volume.

## Smoke Tests

Lightweight service health test:

```bash
FLOWBRIDGE_BASE_URL=https://your-flowbridge-domain \
COUNT=30 \
CONCURRENCY=5 \
bash scripts/smoke_health.sh
```

Small real workflow test:

```bash
FLOWBRIDGE_BASE_URL=https://your-flowbridge-domain \
FLOWBRIDGE_APIKEY=your-user-api-key \
COUNT=5 \
CONCURRENCY=2 \
bash scripts/workflow_smoke.sh
```

Keep `COUNT` and `CONCURRENCY` small for production smoke tests. The workflow test submits real backend jobs.

Use another config file:

```bash
FLOWBRIDGE_CONFIG=/path/to/config.json go run .
```

Environment variables still override the config file when set.

## APIs

Submit anime image-to-video workflow:

```text
POST /api/public/generate/undress/anime/video
```

Pass the user's backend API key on each request:

```bash
curl --location 'http://127.0.0.1:8080/api/public/generate/undress/anime/video' \
  --header 'Accept: application/json' \
  --header 'Content-Type: application/x-www-form-urlencoded' \
  --header 'Apikey: your-user-api-key' \
  --data-urlencode 'source_path=http://allenflux.tech:8000/files/44e8e840819be8e0638087a2.jpg' \
  --data-urlencode 'title=auto generated curl' \
  --data-urlencode 'fee=10' \
  --data-urlencode 'incoming_prompt=' \
  --data-urlencode 'scene_name=venom_transform' \
  --data-urlencode 'video_scene_name=disney_real_anime_greet' \
  --data-urlencode 'output_format=video'
```

Query bridge task:

```text
GET /api/public/task?task_id=bridge_xxx
POST /api/public/task
```

Admin console:

```text
/admin/workflows
/admin/workflows/{task_id}
```

## Reliability Notes

- Submitted tasks are persisted to SQLite before worker execution.
- Worker queue overflow does not block requests; pending tasks are recovered from DB.
- Worker and HTTP handlers recover from panics and return/record failures.
- Backend `status=2` means success, `status=-1` means failed, and `status=3` keeps polling.
- Tune `FLOWBRIDGE_WORKERS` carefully because each worker can hold one backend workflow while polling.
