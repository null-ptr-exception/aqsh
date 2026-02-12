# API Reference

## POST /tasks/{name} - Submit Task

**Request:**
```http
POST /tasks/deploy
Content-Type: application/json
X-Forwarded-User: alice@example.com

{
  "version": "1.2.3",
  "environment": "prod"
}
```

**Identity Header:**

The identity header (default `X-Forwarded-User`, configurable via `AQSH_IDENTITY_HEADER`) identifies who submitted the task. When `AQSH_REQUIRE_IDENTITY=true`, requests without this header are rejected with 401.

**Response (202 Accepted):**
```json
{
  "id": "task_01HQXK5V7Z8Y9ABCDEF",
  "queue": "default",
  "status": "pending"
}
```

**Errors:**
| Status | Reason |
|--------|--------|
| 401 | Missing identity header (when `AQSH_REQUIRE_IDENTITY=true`) |
| 400 | Validation error (missing field, invalid pattern, etc.) |
| 404 | Unknown task |
| 503 | Redis unavailable |

## GET /tasks/{id} - Get Task Status

**Response (200 OK):**
```json
{
  "id": "task_01HQXK5V7Z8Y9ABCDEF",
  "queue": "default",
  "status": "completed",
  "identity": "alice@example.com",
  "created_at": "2025-12-28T10:00:00Z",
  "started_at": "2025-12-28T10:00:01Z",
  "completed_at": "2025-12-28T10:00:15Z",
  "retried": 0,
  "max_retry": 3,
  "result": {
    "exit_code": 0,
    "data": "{\"status\":\"deployed\",\"version\":\"1.2.3\",\"environment\":\"prod\"}"
  }
}
```

**Status Values:**
| Status | Description |
|--------|-------------|
| `pending` | Queued, waiting for worker |
| `running` | Currently executing |
| `completed` | Finished successfully (exit code 0) |
| `failed` | Failed after all retries |
| `retrying` | Failed, scheduled for retry |

## GET /tasks/{id}/logs - Stream Logs

**Response (200 OK, SSE):**
```http
Content-Type: text/event-stream
Cache-Control: no-cache
Connection: keep-alive

data: Starting deployment...

data: Pulling image v1.2.3...

data: Scaling replicas to 3...

data: Deployment complete.
```

**Behavior:**
- For running tasks: streams logs in real-time
- For completed tasks: streams stored logs
- For pending tasks: waits for task to start, then streams
- Connection closed when task completes or fails

**Query Parameters:**
| Param | Description | Default |
|-------|-------------|---------|
| `follow` | Keep connection open for running tasks | `true` |

## GET /tasks - List Task Types

**Response (200 OK):**
```json
{
  "tasks": {
    "deploy": {
      "description": "Deploy application to environment",
      "timeout": "10m",
      "max_retry": 2,
      "queue": "default",
      "input": [
        {
          "name": "version",
          "env": "VERSION",
          "required": true,
          "type": "string",
          "pattern": "^v?\\d+\\.\\d+\\.\\d+$",
          "description": "Semantic version to deploy"
        },
        {
          "name": "environment",
          "env": "ENVIRONMENT",
          "required": true,
          "type": "string",
          "enum": ["dev", "staging", "prod"]
        },
        {
          "name": "dry_run",
          "env": "DRY_RUN",
          "required": false,
          "type": "bool",
          "default": "false"
        }
      ]
    },
    "backup": {
      "description": "Backup database to S3",
      "timeout": "30m",
      "max_retry": 1,
      "queue": "long-running",
      "input": [
        {
          "name": "database",
          "env": "DATABASE",
          "required": true,
          "type": "string",
          "pattern": "^[a-z][a-z0-9_]{2,30}$"
        }
      ]
    }
  }
}
```

## GET /health - Health Check

**Response (200 OK):**
```json
{
  "status": "healthy",
  "redis": "connected",
  "mode": "api"
}
```

## GET /metrics - Prometheus Metrics

Returns Prometheus-formatted metrics. See [Observability](../README.md#observability) for details.
