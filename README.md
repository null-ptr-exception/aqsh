# aqsh Design Document

**Async Queue for Shell Scripts**

## Overview

aqsh is a simple, production-ready job queue for executing shell scripts asynchronously. Built on [Asynq](https://github.com/hibiken/asynq) and Redis.

### Background

This project originated as [DJQWSC](https://github.com/rophy/djqwsc) (Distributed Job Queue With Shell Scripts), a fork of Fireworq + webhook. aqsh is a complete rewrite with a cleaner architecture.

### Why Rewrite?

| DJQWSC (v1) | aqsh |
|-------------|------|
| Forked from Fireworq (complex, diverging) | Clean codebase (~1000 lines) |
| Two components (djqwsc + webhook) | Single binary |
| MySQL/etcd storage | Redis (with Sentinel support) |
| Primary/backup only | True horizontal worker scaling |
| Custom implementation | Leverages battle-tested Asynq |

### Goals

1. **Simple** - Minimal code, easy to understand and maintain
2. **Explicit** - Clear hook configuration with input validation
3. **Scalable** - Horizontal worker scaling via Redis
4. **Observable** - Real-time log streaming, Prometheus metrics
5. **Secure** - Input validation prevents injection attacks

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                                Clients                                  │
│                                   │                                     │
│            POST /jobs/deploy      │     GET /jobs/{id}                  │
│            {"version": "1.2.3"}   │     GET /jobs/{id}/logs             │
│                                   │                                     │
└───────────────────────────────────┼─────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                          API Pods (stateless)                           │
│  ┌───────────────────────────────────────────────────────────────────┐  │
│  │  • HTTP API (submit jobs, query status, stream logs)              │  │
│  │  • Input validation against hook config                           │  │
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
| `/jobs/{hook}` | POST | Submit a job |
| `/jobs/{id}` | GET | Get job status and result |
| `/jobs/{id}/logs` | GET | Stream job logs (SSE) |
| `/hooks` | GET | List available hooks with schemas |
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

### POST /jobs/{hook} - Submit Job

**Request:**
```http
POST /jobs/deploy
Content-Type: application/json

{
  "version": "1.2.3",
  "environment": "prod"
}
```

**Response (202 Accepted):**
```json
{
  "id": "task_01HQXK5V7Z8Y9ABCDEF",
  "hook": "deploy",
  "queue": "default",
  "status": "pending"
}
```

**Errors:**
| Status | Reason |
|--------|--------|
| 400 | Validation error (missing field, invalid pattern, etc.) |
| 404 | Unknown hook |
| 503 | Redis unavailable |

### GET /jobs/{id} - Get Job Status

**Response (200 OK):**
```json
{
  "id": "task_01HQXK5V7Z8Y9ABCDEF",
  "hook": "deploy",
  "queue": "default",
  "status": "completed",
  "result": {
    "exit_code": 0,
    "data": "{\"status\":\"deployed\",\"version\":\"1.2.3\",\"environment\":\"prod\"}"
  },
  "created_at": "2025-12-28T10:00:00Z",
  "started_at": "2025-12-28T10:00:01Z",
  "completed_at": "2025-12-28T10:00:15Z",
  "retried": 0,
  "max_retry": 3
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

### GET /jobs/{id}/logs - Stream Logs

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
- For running jobs: streams logs in real-time
- For completed jobs: streams stored logs
- For pending jobs: waits for job to start, then streams
- Connection closed when job completes or fails

**Query Parameters:**
| Param | Description | Default |
|-------|-------------|---------|
| `follow` | Keep connection open for running jobs | `true` |
| `tail` | Number of lines from end (completed jobs) | all |

### GET /hooks - List Hooks

**Response (200 OK):**
```json
{
  "hooks": {
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

---

## Hook Configuration

### hooks.yaml

```yaml
defaults:
  timeout: 5m
  max_retry: 3
  retry_delay: 30s
  queue: default
  log_retention: 24h

hooks:
  deploy:
    script: /tasks/deploy.sh
    description: "Deploy application to environment"
    timeout: 10m
    max_retry: 2

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
| `XRANGE logs:{id} - +` | Read all entries (completed job) |
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
| `AQSH_HOOKS_CONFIG` | Path to hooks.yaml | `/etc/aqsh/hooks.yaml` |
| `AQSH_TASKS_DIR` | Tasks directory | `/tasks` |
| `AQSH_RESULTS_DIR` | Directory for temp result files | `/var/lib/aqsh/results` |
| `AQSH_REDIS_ADDR` | Redis address (standalone) | `localhost:6379` |
| `AQSH_REDIS_SENTINEL_ADDRS` | Sentinel addresses (comma-separated) | - |
| `AQSH_REDIS_SENTINEL_MASTER` | Sentinel master name | `mymaster` |
| `AQSH_REDIS_PASSWORD` | Redis password | - |
| `AQSH_WORKER_CONCURRENCY` | Concurrent jobs per worker | `10` |
| `AQSH_WORKER_QUEUES` | Queues to process (comma-separated) | `default` |
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

### Architecture

```yaml
# Separate API and Worker deployments for independent scaling
┌─────────────────────────────────────────────────────────────────────────┐
│                           Kubernetes Cluster                            │
│                                                                         │
│  ┌─────────────────────────────────────────────────────────────────┐   │
│  │  Deployment: aqsh-api (replicas: 2)                             │   │
│  │  ┌─────────────────┐  ┌─────────────────┐                       │   │
│  │  │  Pod: api-1     │  │  Pod: api-2     │                       │   │
│  │  │  mode: api      │  │  mode: api      │                       │   │
│  │  └────────┬────────┘  └────────┬────────┘                       │   │
│  │           └────────────┬───────┘                                │   │
│  │                        ▼                                        │   │
│  │              Service: aqsh-api                                  │   │
│  │                   :8080                                         │   │
│  └─────────────────────────────────────────────────────────────────┘   │
│                                                                         │
│  ┌─────────────────────────────────────────────────────────────────┐   │
│  │  Deployment: aqsh-worker (replicas: 3)                          │   │
│  │  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐  │   │
│  │  │  Pod: worker-1  │  │  Pod: worker-2  │  │  Pod: worker-3  │  │   │
│  │  │  mode: worker   │  │  mode: worker   │  │  mode: worker   │  │   │
│  │  │  concurrency:10 │  │  concurrency:10 │  │  concurrency:10 │  │   │
│  │  │  /tasks/*       │  │  /tasks/*       │  │  /tasks/*       │  │   │
│  │  └─────────────────┘  └─────────────────┘  └─────────────────┘  │   │
│  └─────────────────────────────────────────────────────────────────┘   │
│                                                                         │
│  ┌─────────────────────────────────────────────────────────────────┐   │
│  │  StatefulSet: redis-sentinel (replicas: 3)                      │   │
│  │  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐  │   │
│  │  │  redis-0        │  │  redis-1        │  │  redis-2        │  │   │
│  │  │  (master)       │  │  (replica)      │  │  (replica)      │  │   │
│  │  └─────────────────┘  └─────────────────┘  └─────────────────┘  │   │
│  └─────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────┘
```

### Example Manifests

```yaml
# ConfigMap for hooks
apiVersion: v1
kind: ConfigMap
metadata:
  name: aqsh-hooks
  namespace: aqsh
data:
  hooks.yaml: |
    defaults:
      timeout: 5m
      max_retry: 3
    hooks:
      deploy:
        script: /tasks/deploy.sh
        input:
          - name: version
            env: VERSION
            required: true
---
# ConfigMap for tasks
apiVersion: v1
kind: ConfigMap
metadata:
  name: aqsh-tasks
  namespace: aqsh
data:
  deploy.sh: |
    #!/bin/bash
    set -e
    echo "Deploying version $VERSION"
---
# API Deployment
apiVersion: apps/v1
kind: Deployment
metadata:
  name: aqsh-api
  namespace: aqsh
spec:
  replicas: 2
  selector:
    matchLabels:
      app: aqsh-api
  template:
    metadata:
      labels:
        app: aqsh-api
    spec:
      containers:
      - name: api
        image: aqsh:latest
        args: ["--mode=api"]
        ports:
        - containerPort: 8080
        env:
        - name: AQSH_REDIS_SENTINEL_ADDRS
          value: "redis-0:26379,redis-1:26379,redis-2:26379"
        - name: AQSH_REDIS_SENTINEL_MASTER
          value: "mymaster"
        volumeMounts:
        - name: hooks
          mountPath: /etc/aqsh
        livenessProbe:
          httpGet:
            path: /health
            port: 8080
        readinessProbe:
          httpGet:
            path: /health
            port: 8080
      volumes:
      - name: hooks
        configMap:
          name: aqsh-hooks
---
# Worker Deployment
apiVersion: apps/v1
kind: Deployment
metadata:
  name: aqsh-worker
  namespace: aqsh
spec:
  replicas: 3
  selector:
    matchLabels:
      app: aqsh-worker
  template:
    metadata:
      labels:
        app: aqsh-worker
    spec:
      containers:
      - name: worker
        image: aqsh:latest
        args: ["--mode=worker"]
        env:
        - name: AQSH_REDIS_SENTINEL_ADDRS
          value: "redis-0:26379,redis-1:26379,redis-2:26379"
        - name: AQSH_REDIS_SENTINEL_MASTER
          value: "mymaster"
        - name: AQSH_WORKER_CONCURRENCY
          value: "10"
        volumeMounts:
        - name: hooks
          mountPath: /etc/aqsh
        - name: tasks
          mountPath: /tasks
      volumes:
      - name: hooks
        configMap:
          name: aqsh-hooks
      - name: tasks
        configMap:
          name: aqsh-tasks
          defaultMode: 0755
---
# API Service
apiVersion: v1
kind: Service
metadata:
  name: aqsh-api
  namespace: aqsh
spec:
  selector:
    app: aqsh-api
  ports:
  - port: 8080
    targetPort: 8080
```

---

## Observability

### Prometheus Metrics

Exposed at `/metrics` using [asynq's x/metrics](https://github.com/hibiken/asynq/wiki/Monitoring-and-Alerting) package:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `asynq_queue_size` | Gauge | `queue` | Total tasks in queue |
| `asynq_queue_latency_seconds` | Gauge | `queue` | Oldest pending task wait time |
| `asynq_queue_memory_usage_approx_bytes` | Gauge | `queue` | Approximate memory usage |
| `asynq_queue_paused_total` | Gauge | `queue` | Whether queue is paused |
| `asynq_tasks_enqueued_total` | Gauge | `queue`, `state` | Task count by state (active, pending, retry, archived, completed, scheduled) |
| `asynq_tasks_processed_total` | Counter | `queue` | Total processed tasks |
| `asynq_tasks_failed_total` | Counter | `queue` | Total failed task attempts |

These metrics are read from Redis on each scrape, so they persist across aqsh restarts.

### Health Check

```http
GET /health

{
  "status": "healthy",
  "redis": "connected",
  "mode": "api"
}
```

### Asynqmon (Optional)

Deploy [Asynqmon](https://github.com/hibiken/asynqmon) for web UI:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: asynqmon
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: asynqmon
        image: hibiken/asynqmon
        args:
        - --redis-addr=redis:6379
        ports:
        - containerPort: 8080
```

---

## Job Lifecycle

```
┌──────────┐     ┌──────────┐     ┌──────────┐     ┌───────────┐
│ pending  │────▶│  active  │────▶│completed │     │           │
└──────────┘     └──────────┘     └──────────┘     │           │
      │                │                           │  deleted  │
      │                │ fail                      │  (after   │
      │                ▼                           │ retention)│
      │          ┌──────────┐     ┌──────────┐     │           │
      │          │  retry   │────▶│ archived │────▶│           │
      │          │(scheduled)│     │ (failed) │     └───────────┘
      │          └──────────┘     └──────────┘
      │                │
      │                │ retry
      │                ▼
      └────────────────┘
```

| State | Description | Retention |
|-------|-------------|-----------|
| `pending` | Queued, waiting for worker | Until processed |
| `active` | Worker executing script | Until done |
| `completed` | Script exited 0 | `AQSH_RESULT_RETENTION` |
| `retry` | Failed, waiting to retry | Until retry or archived |
| `archived` | Failed all retries | `AQSH_RESULT_RETENTION` |

---

## Comparison with DJQWSC

| Aspect | DJQWSC (Fireworq + webhook) | aqsh |
|--------|----------------------------|------|
| **Codebase** | ~15k lines (forked) | ~1k lines (new) |
| **Components** | 2 binaries | 1 binary |
| **Storage** | MySQL or etcd | Redis |
| **Worker scaling** | Primary/backup only | Horizontal |
| **Log streaming** | File-based, local only | Redis Streams, any pod |
| **Input validation** | None (webhook passthrough) | Explicit schema |
| **Metrics** | Custom | Prometheus (built-in) |
| **Web UI** | None | Asynqmon (optional) |
| **Maintenance** | Fork divergence risk | Thin wrapper |

---

## Migration from DJQWSC

aqsh is a complete rewrite with different storage backend. Migration options:

1. **Parallel deployment** - Run aqsh alongside DJQWSC, migrate hooks gradually
2. **Clean cutover** - Wait for DJQWSC queue to drain, switch to aqsh
3. **Fresh start** - aqsh uses new orphan branch, treat as separate project

Since aqsh uses Redis instead of MySQL/etcd, there's no data migration path. Jobs in DJQWSC queue should complete before switching.

### Hook Configuration Migration

DJQWSC (webhook `hooks.yaml`):
```yaml
- id: deploy
  execute-command: /tasks/run-job.sh
  pass-arguments-to-command:
    - source: string
      name: /tasks/deploy.sh
  pass-environment-to-command:
    - source: payload
      name: version
      envname: VERSION
```

aqsh (`hooks.yaml`):
```yaml
hooks:
  deploy:
    script: /tasks/deploy.sh
    input:
      - name: version
        env: VERSION
        required: true
```

---

## Future Considerations

Not in scope for initial release, but possible future additions:

1. **Job dependencies** - Run job B after job A completes
2. **Scheduled jobs** - Cron-like scheduling (Asynq supports this)
3. **Job cancellation** - Cancel pending/running jobs
4. **Webhooks** - Notify external systems on job completion
5. **Multi-tenancy** - Namespace isolation

---

## Open Questions

1. **Result size limits** - How large can script output be? (Redis memory)
2. **Log format** - Plain text vs structured (JSON lines)?
3. **Authentication** - API auth for multi-tenant scenarios?
4. **Rate limiting** - Per-hook or global rate limits?
