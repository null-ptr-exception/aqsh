# API Reference

## Task Definitions

### GET /tasks - List All Task Definitions

Returns task names and descriptions. Use `GET /tasks/{name}` for full details.

**Response (200 OK):**
```json
{
  "tasks": {
    "deploy": {
      "description": "Deploy application to environment"
    },
    "backup": {
      "description": "Backup database to S3"
    }
  }
}
```

### GET /tasks/{name} - Get Task Definition

Returns full task definition including inputs. When identity headers are provided, inputs with `values_url` resolve the remote URL and include the allowed values for that user.

**Response without identity (200 OK):**
```json
{
  "description": "Upgrade a database instance",
  "timeout": "10m",
  "max_retry": 0,
  "queue": "default",
  "input": [
    {
      "name": "instance",
      "env": "DB_INSTANCE",
      "required": true,
      "type": "string",
      "values_url": true
    }
  ]
}
```

**Response with identity header (200 OK):**
```json
{
  "description": "Upgrade a database instance",
  "timeout": "10m",
  "max_retry": 0,
  "queue": "default",
  "input": [
    {
      "name": "instance",
      "env": "DB_INSTANCE",
      "required": true,
      "type": "string",
      "values_url": true,
      "values": [
        {"name": "Production DB 001", "value": "prod-db-001"},
        {"name": "Production DB 002", "value": "prod-db-002"}
      ]
    }
  ]
}
```

**Errors:**

| Status | Reason |
|--------|--------|
| 404 | Unknown task |

### POST /tasks/{name} - Submit Task Execution

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

**Authorization:**

Tasks can restrict access using `allowed_users` and/or `allowed_groups` in their config. If either matches, the request is authorized (OR logic). `allowed_users` matches against the identity header; `allowed_groups` matches against the groups header (comma-separated). If neither is configured, the task is open to all.

Inputs with `values_url` perform additional per-parameter authorization: the remote URL is fetched with user context, and the submitted value must be in the returned list.

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
| 403 | Not authorized for this task (user/group check failed), or submitted value not in allowed values from `values_url` |
| 400 | Validation error (missing field, invalid pattern, etc.) |
| 404 | Unknown task |
| 502 | Remote values URL returned error |
| 504 | Remote values URL timed out |
| 503 | Redis unavailable |

## Executions

### GET /executions/{id} - Get Execution Status

**Response (200 OK):**
```json
{
  "id": "task_01HQXK5V7Z8Y9ABCDEF",
  "queue": "default",
  "status": "completed",
  "identity": "alice@example.com",
  "groups": "deploy-team,platform-team",
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

### GET /executions/{id}/logs - Stream Execution Logs

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
- For running executions: streams logs in real-time
- For completed executions: streams stored logs
- For pending executions: waits for execution to start, then streams
- Connection closed when execution completes or fails

**Query Parameters:**

| Param | Description | Default |
|-------|-------------|---------|
| `follow` | Keep connection open for running executions | `true` |

## System

### GET /health - Health Check

**Response (200 OK):**
```json
{
  "status": "healthy",
  "version": "0.1.0",
  "redis": "connected",
  "mode": "api"
}
```

### GET /metrics - Prometheus Metrics

Returns Prometheus-formatted metrics. See [Observability](../README.md#observability) for details.
