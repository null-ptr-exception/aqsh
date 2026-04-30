# aqsh Design Document

**Async Queue for Shell Scripts**

## Overview

aqsh is a simple, production-ready task queue for executing shell scripts asynchronously. Built on [Asynq](https://github.com/hibiken/asynq) and Redis.

### Goals

1. **Simple** - Minimal code, easy to understand and maintain
2. **Explicit** - Clear task configuration with input validation
3. **Scalable** - Horizontal worker scaling via Redis
4. **Observable** - Real-time log streaming, Prometheus metrics
5. **Secure** - Input validation prevents injection attacks

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                                Clients                                  │
│                                   │                                     │
│            POST /tasks/deploy     │     GET /tasks/{id}                 │
│            {"version": "1.2.3"}   │     GET /tasks/{id}/logs            │
│                                   │                                     │
└───────────────────────────────────┼─────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                          API Pods (stateless)                           │
│  ┌───────────────────────────────────────────────────────────────────┐  │
│  │  • HTTP API (submit tasks, query status, stream logs)             │  │
│  │  • Input validation against task config                           │  │
│  │  • Asynq Client (enqueue tasks)                                   │  │
│  │  • Asynq Inspector (query task status)                            │  │
│  └───────────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────┬───────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                         Redis (Sentinel)                                │
│  ┌───────────────────────────────────────────────────────────────────┐  │
│  │  • Asynq task queues (pending, active, completed, retry, etc.)    │  │
│  │  • Log streams (logs:{task_id}) via Redis Streams                 │  │
│  │  • Task results (stored with configurable retention)              │  │
│  └───────────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────┬───────────────────────────────────┘
                                      │
          ┌───────────────────────────┼───────────────────────────┐
          │                           │                           │
          ▼                           ▼                           ▼
┌─────────────────────┐   ┌─────────────────────┐   ┌─────────────────────┐
│   Worker Pod 1      │   │   Worker Pod 2      │   │   Worker Pod N      │
│  ┌───────────────┐  │   │  ┌───────────────┐  │   │  ┌───────────────┐  │
│  │ Asynq Server  │  │   │  │ Asynq Server  │  │   │  │ Asynq Server  │  │
│  │ (goroutines)  │  │   │  │ (goroutines)  │  │   │  │ (goroutines)  │  │
│  └───────┬───────┘  │   │  └───────┬───────┘  │   │  └───────┬───────┘  │
│          │          │   │          │          │   │          │          │
│          ▼          │   │          ▼          │   │          ▼          │
│  ┌───────────────┐  │   │  ┌───────────────┐  │   │  ┌───────────────┐  │
│  │  Shell Exec   │  │   │  │  Shell Exec   │  │   │  │  Shell Exec   │  │
│  │  os/exec.Cmd  │  │   │  │  os/exec.Cmd  │  │   │  │  os/exec.Cmd  │  │
│  └───────────────┘  │   │  └───────────────┘  │   │  └───────────────┘  │
│  /tasks/*           │   │  /tasks/*           │   │  /tasks/*           │
└─────────────────────┘   └─────────────────────┘   └─────────────────────┘
```

---

## Components

### 1. API Server

Stateless HTTP server that handles:

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/tasks/{name}` | POST | Submit a task |
| `/tasks/{id}` | GET | Get task status and result |
| `/tasks/{id}/logs` | GET | Stream task logs (SSE) |
| `/tasks` | GET | List available task types with schemas |
| `/health` | GET | Health check |
| `/metrics` | GET | Prometheus metrics |

**Deployment:** Can run multiple replicas behind a load balancer.

### 2. Worker Server

Pulls tasks from Redis and executes shell scripts:

- Runs Asynq Server with configurable concurrency
- Executes scripts via `os/exec`
- Streams output to Redis Streams
- Stores final result in task

**Deployment:** Scale horizontally by adding more worker pods.

### 3. Redis (Sentinel)

Shared state for:

- Task queues (managed by Asynq)
- Log streams (Redis Streams)
- Task results (with TTL)

**Deployment:** Redis Sentinel cluster for HA.

---

## API Reference

See [docs/api.md](docs/api.md) for full API documentation.

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/tasks/{name}` | POST | Submit a task |
| `/tasks/{id}` | GET | Get task status and result |
| `/tasks/{id}/logs` | GET | Stream task logs (SSE) |
| `/tasks` | GET | List available task types |
| `/health` | GET | Health check |
| `/metrics` | GET | Prometheus metrics |

---

## Task Configuration

### tasks.yaml

```yaml
defaults:
  timeout: 5m
  max_retry: 3
  retry_delay: 30s
  queue: default
  log_retention: 24h

tasks:
  deploy:
    script: /tasks/deploy.sh
    description: "Deploy application to environment"
    timeout: 10m
    max_retry: 2
    allowed_users: [system:serviceaccount:deploy:deployer]
    allowed_groups: [deploy-team, platform-team]

    input:
      - name: version
        env: VERSION
        required: true
        type: string
        pattern: '^v?\d+\.\d+\.\d+$'
        description: "Semantic version to deploy"

      - name: environment
        env: ENVIRONMENT
        required: true
        type: string
        enum: [dev, staging, prod]

      - name: dry_run
        env: DRY_RUN
        required: false
        type: bool
        default: "false"

  backup:
    script: /tasks/backup.sh
    description: "Backup database to S3"
    timeout: 30m
    max_retry: 1
    queue: long-running

    input:
      - name: database
        env: DATABASE
        required: true
        type: string
        pattern: '^[a-z][a-z0-9_]{2,30}$'

      - name: destination
        env: DESTINATION
        required: false
        type: string
        pattern: '^s3://[a-z0-9][a-z0-9.-]+/'
        default: "s3://backups/db/"

  cleanup:
    script: /tasks/cleanup.sh
    description: "Clean up old resources"

    input:
      - name: older_than_days
        env: OLDER_THAN_DAYS
        required: true
        type: int
        min: 1
        max: 365
```

### Input Field Specification

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | JSON payload field name |
| `env` | string | Environment variable name for script |
| `required` | bool | Whether field must be present |
| `type` | string | `string`, `int`, `float`, `bool` |
| `pattern` | string | Regex pattern (for strings) |
| `enum` | []string | Allowed values |
| `min` | number | Minimum value (for numbers) |
| `max` | number | Maximum value (for numbers) |
| `default` | string | Default value if not provided |
| `description` | string | Human-readable description |

### Script Results (AQSH_RESULT_FILE)

Scripts can optionally write structured results to the file specified by `$AQSH_RESULT_FILE`:

```bash
#!/bin/bash
set -e

echo "Processing..."  # Goes to logs (streamed real-time)
# ... do work ...

# Write structured result (stored with task)
cat > "$AQSH_RESULT_FILE" << EOF
{
  "processed": 42,
  "status": "success"
}
EOF
```

**Key points:**
- `$AQSH_RESULT_FILE` is a temp file path set by the worker
- Content is read after script exits and stored as a string with the task result
- Maximum result size: 1MB
- Logs (stdout/stderr) are streamed separately via Redis Streams
- Clients are responsible for parsing the result (e.g., as JSON)

**Result structure:**
```json
{
  "exit_code": 0,
  "data": "...",       // Contents of AQSH_RESULT_FILE as string (optional)
  "error": "..."       // Set on execution error
}
```

**Data field semantics:**
- Field omitted: Script did not write to `$AQSH_RESULT_FILE`
- `"data": ""`: Script wrote an empty file
- `"data": "..."`: Script wrote content to the file

### Security

Input validation prevents:

1. **Environment injection** - Unknown fields are rejected
2. **Command injection** - Pattern validation on inputs
3. **Type confusion** - Explicit type checking
4. **Unbounded values** - min/max constraints

---

## Log Streaming

### Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                              Worker Pod                                 │
│  ┌───────────────────────────────────────────────────────────────────┐  │
│  │  HandleTask(ctx, task) {                                          │  │
│  │      streamKey := "logs:" + task.ID                               │  │
│  │      cmd := exec.Command(script)                                  │  │
│  │                                                                   │  │
│  │      stdout := cmd.StdoutPipe()                                   │  │
│  │      cmd.Start()                                                  │  │
│  │                                                                   │  │
│  │      for line := range stdout {                                   │  │
│  │          redis.XADD(streamKey, "*", "line", line) ──────────────┐ │  │
│  │      }                                                          │ │  │
│  │                                                                 │ │  │
│  │      cmd.Wait()                                                 │ │  │
│  │      redis.XADD(streamKey, "*", "eof", "true") ─────────────────┤ │  │
│  │      redis.EXPIRE(streamKey, retention)                         │ │  │
│  │  }                                                              │ │  │
│  └─────────────────────────────────────────────────────────────────│─┘  │
└────────────────────────────────────────────────────────────────────│────┘
                                                                     │
                                                                     ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                              Redis                                      │
│  ┌───────────────────────────────────────────────────────────────────┐  │
│  │  Stream: logs:task_01HQXK5V7Z8Y9ABCDEF                            │  │
│  │                                                                   │  │
│  │  1703750000000-0  {"line": "Starting deployment..."}              │  │
│  │  1703750000100-0  {"line": "Pulling image v1.2.3..."}             │  │
│  │  1703750001500-0  {"line": "Scaling replicas..."}                 │  │
│  │  1703750002000-0  {"line": "Done.", "eof": "true"}                │  │
│  └───────────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────────┘
                                                                     │
                                                                     │
┌────────────────────────────────────────────────────────────────────│────┐
│                              API Pod                               │    │
│  ┌─────────────────────────────────────────────────────────────────│─┐  │
│  │  GetLogs(w, r) {                                                │ │  │
│  │      streamKey := "logs:" + taskID                              │ │  │
│  │      w.Header("Content-Type", "text/event-stream")              │ │  │
│  │                                                                 │ │  │
│  │      lastID := "0"                                              │ │  │
│  │      for {                                                      │ │  │
│  │          entries := redis.XREAD(BLOCK 5s, streamKey, lastID) ◄──┘ │  │
│  │          for entry := range entries {                             │  │
│  │              if entry["eof"] { return }                           │  │
│  │              fmt.Fprintf(w, "data: %s\n\n", entry["line"])        │  │
│  │              w.Flush()                                            │  │
│  │              lastID = entry.ID                                    │  │
│  │          }                                                        │  │
│  │      }                                                            │  │
│  │  }                                                                │  │
│  └───────────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────────┘
```

### Redis Streams Commands

| Command | Usage |
|---------|-------|
| `XADD logs:{id} * line "text"` | Append log line (worker) |
| `XREAD BLOCK 5000 STREAMS logs:{id} {lastID}` | Read new entries (API) |
| `XRANGE logs:{id} - +` | Read all entries (completed task) |
| `EXPIRE logs:{id} 86400` | Set 24h TTL |

### Client Reconnection

If client disconnects and reconnects:
1. Client sends `Last-Event-ID` header (SSE standard)
2. API uses this as `lastID` for XREAD
3. Streaming resumes from where it left off

---

## Configuration

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `AQSH_MODE` | `api`, `worker`, or `both` | `both` |
| `AQSH_BIND` | API listen address | `0.0.0.0:8080` |
| `AQSH_TASKS_CONFIG` | Path to tasks.yaml | `/etc/aqsh/tasks.yaml` |
| `AQSH_TASKS_DIR` | Tasks directory | `/tasks` |
| `AQSH_RESULTS_DIR` | Directory for temp result files | `/var/lib/aqsh/results` |
| `AQSH_REDIS_ADDR` | Redis address (standalone) | `localhost:6379` |
| `AQSH_REDIS_SENTINEL_ADDRS` | Sentinel addresses (comma-separated) | - |
| `AQSH_REDIS_SENTINEL_MASTER` | Sentinel master name | `mymaster` |
| `AQSH_REDIS_PASSWORD` | Redis password | - |
| `AQSH_WORKER_CONCURRENCY` | Concurrent tasks per worker | `10` |
| `AQSH_WORKER_QUEUES` | Queues to process (comma-separated) | `default` |
| `AQSH_IDENTITY_HEADER` | Header name for user identity | `X-Forwarded-User` |
| `AQSH_REQUIRE_IDENTITY` | Require identity header (401 if missing) | `false` |
| `AQSH_GROUPS_HEADER` | Header name for user groups | `X-Forwarded-Groups` |
| `AQSH_LOG_RETENTION` | Log stream retention | `24h` |
| `AQSH_RESULT_RETENTION` | Completed task retention | `72h` |

### Modes

| Mode | Description | Use Case |
|------|-------------|----------|
| `api` | HTTP API only (no task processing) | Dedicated API pods |
| `worker` | Task processing only (no HTTP) | Dedicated worker pods |
| `both` | API + worker in same process | Simple deployments |

---

## Kubernetes Deployment

See [k8s/](k8s/) for example manifests. Deploy with:

```bash
skaffold run
```

For production, consider separate API and Worker deployments for independent scaling:
- API pods (`--mode=api`): stateless, scale based on request load
- Worker pods (`--mode=worker`): scale based on queue depth

---

## Observability

- **Prometheus metrics** at `/metrics` - See [Asynq Monitoring](https://github.com/hibiken/asynq/wiki/Monitoring-and-Alerting)
- **Health check** at `/health` - Returns version and Redis connection status
- **Web UI** - Deploy [Asynqmon](https://github.com/hibiken/asynqmon) for task monitoring

Task lifecycle follows [Asynq's state machine](https://github.com/hibiken/asynq/wiki/Life-of-a-Task):
`pending` → `active` → `completed` (or `retry` → `archived`)

---

## Future Considerations

Not in scope for initial release, but possible future additions:

1. **Task dependencies** - Run task B after task A completes
2. **Scheduled tasks** - Cron-like scheduling (Asynq supports this)
3. **Task cancellation** - Cancel pending/running tasks
4. **Webhooks** - Notify external systems on task completion
5. **Multi-tenancy** - Namespace isolation

---

## Authorization

aqsh supports identity tracking and per-task group authorization via HTTP headers, designed for use with an authenticating reverse proxy (e.g., [kube-auth-proxy](https://github.com/rophy/kube-auth-proxy)).

### Identity Tracking

The proxy sets `X-Forwarded-User` (configurable via `AQSH_IDENTITY_HEADER`) on authenticated requests. aqsh records this with the task so you can see who submitted it.

Set `AQSH_REQUIRE_IDENTITY=true` to reject requests without this header (401).

### Group Authorization

Tasks can restrict access using `allowed_users` and/or `allowed_groups` in the task config:

```yaml
tasks:
  deploy:
    script: /tasks/deploy.sh
    allowed_users: [system:serviceaccount:deploy:deployer]
    allowed_groups: [deploy-team, platform-team]
    input: [...]
```

- `allowed_users` matches the identity header against the list (exact match).
- `allowed_groups` matches the groups header (comma-separated) against the list.
- If both are set, the request is authorized if **either** matches (OR logic). This mirrors Kubernetes RBAC `RoleBinding.subjects` semantics.
- Returns 403 if neither matches.

Tasks without `allowed_users` or `allowed_groups` are open to all users (or all authenticated users when `AQSH_REQUIRE_IDENTITY=true`).

---

## Open Questions

1. **Result size limits** - How large can script output be? (Redis memory)
2. **Log format** - Plain text vs structured (JSON lines)?
3. **Rate limiting** - Per-task or global rate limits?
