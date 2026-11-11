# API Reference

Authentication modes are described in [authentication.md](authentication.md).
Native Kubernetes mode requires a bearer token on every route except exact
`GET /health` and `GET /metrics`; forwarded identity headers are ignored.
Optional [role-based authorization](authorization.md) restricts task discovery,
submission and execution reads. Without a policy, legacy behavior is retained.

## Task Definitions

### GET /tasks - List All Task Definitions

Returns task names and descriptions. Use `GET /tasks/{name}` for full details.
With RBAC enabled, requires identity and lists only tasks with `run` or `read`.

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
With RBAC enabled, `run` or `read` is required before remote values are fetched;
unauthorized definitions return 404.

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
| 401 | Missing identity in RBAC mode, or failed native authentication |
| 404 | Unknown task or definition hidden by RBAC |

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

Tasks can restrict access using `allowed_users` and/or `allowed_groups` in their config. If either matches, the request is authorized (OR logic). In trusted-header mode, `allowed_users` matches the configured identity header and `allowed_groups` matches the groups header (comma-separated). In native mode, both use the verified TokenReview identity and groups; optional cluster qualification prefixes both the user identity and every group as described in [authentication](authentication.md#identity-and-task-authorization). If neither is configured, the task is open to all.

When top-level `roles` or `bindings` is configured, RBAC replaces these legacy
ACLs: nonblank identity and `run` for the task are required. Nonempty legacy ACLs
cannot be mixed with RBAC. A `read` grant alone does not allow submission.

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
| 401 | Missing identity header when `AQSH_REQUIRE_IDENTITY=true`, missing or blank identity when RBAC is enabled (even if `AQSH_REQUIRE_IDENTITY=false`), or failed native authentication |
| 403 | Not authorized for this task (user/group check failed), or submitted value not in allowed values from `values_url` |
| 400 | Validation error (missing field, invalid pattern, etc.) |
| 404 | Unknown task |
| 502 | Remote values URL returned error |
| 504 | Remote values URL timed out |
| 503 | Redis unavailable |

## Executions

### GET /executions/{id} - Get Execution Status

With RBAC enabled, requires `read` for the original queued task name, or its exact
submitter identity with current `run` for that name. The current task definition
need not exist. Inaccessible/missing records return the same 404; lookup backend
failures return 503. Without RBAC, existing execution visibility is retained.

Execution lookup searches the configured queues. Keep retired queue names in the
API's `AQSH_WORKER_QUEUES` while their retained executions still need to be read;
the task definition itself need not remain. See [lifecycle and retention](authorization.md#lifecycle-and-retention).

**Response (200 OK):**
```json
{
  "id": "task_01HQXK5V7Z8Y9ABCDEF",
  "queue": "default",
  "status": "completed",
  "identity": "alice@example.com",
  "task": "deploy",
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

With RBAC enabled, applies the execution-status permission check before opening
SSE, including requests with `Last-Event-ID` or `follow=false`. Deleted/expired
execution records make retained logs inaccessible. An already-open stream is not
interrupted by policy changes; each reconnect is authorized separately.

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
  "mode": "api",
  "rbac_enabled": false
}
```

`rbac_enabled` reports whether an explicit roles/bindings policy is loaded,
including an empty policy that denies all access. It does not describe any
particular caller's grants.

### GET /metrics - Prometheus Metrics

Returns Prometheus-formatted metrics. See [Observability](../README.md#observability) for details.
