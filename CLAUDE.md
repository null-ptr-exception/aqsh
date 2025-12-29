# aqsh - Async Queue for Shell Scripts

## Project Context

This is **aqsh** (v2), a complete rewrite of the job queue system.

**v1 (DJQWSC)** is located at: `/home/rophy/projects/djqwsc`

v1 was a fork of Fireworq + webhook. It worked but had issues:
- Complex codebase (~15k lines inherited from Fireworq)
- Two components (djqwsc + webhook sidecar)
- Primary/backup only, no horizontal worker scaling
- Fork divergence risk

aqsh is a clean-slate rewrite built on [Asynq](https://github.com/hibiken/asynq) + Redis.

## Design Document

The `README.md` contains the full design document. Read it first to understand:
- Architecture (API pods + Worker pods + Redis)
- API endpoints (`POST /tasks/{name}`, `GET /tasks/{id}`, etc.)
- Task configuration format (`tasks.yaml`)
- Log streaming via Redis Streams
- Input validation schema

## Key Design Decisions

1. **Single binary** - One Go binary with `--mode=api|worker|both`
2. **Redis backend** - Asynq uses Redis for queue, we also use Redis Streams for logs
3. **Explicit task config** - All inputs must be declared with validation rules
4. **Horizontal scaling** - Multiple worker pods all actively process jobs

## Development

### Prerequisites

- Go 1.21+
- Redis (or use Docker)
- Task scripts in `/tasks` directory

### Project Structure (Planned)

```
aqsh/
├── cmd/
│   └── aqsh/
│       └── main.go         # CLI entry point
├── internal/
│   ├── api/                # HTTP handlers
│   ├── config/             # Configuration loading
│   ├── tasks/              # Task config parsing & validation
│   ├── worker/             # Asynq task handler, shell execution
│   └── logs/               # Redis Streams log handling
├── tasks.yaml              # Task configuration
├── tasks/                  # Example task scripts
├── scripts/                # Build scripts (build.sh)
├── go.mod
├── go.sum
├── Dockerfile
└── README.md               # Design document
```

### Key Dependencies

```
github.com/hibiken/asynq     # Task queue
github.com/redis/go-redis/v9 # Redis client (for log streams)
github.com/go-chi/chi/v5     # HTTP router (or stdlib)
gopkg.in/yaml.v3             # YAML parsing
```

### Running Locally

```bash
# Start Redis
docker run -d --name redis -p 6379:6379 redis:7

# Run in "both" mode (API + worker)
go run ./cmd/aqsh --mode=both

# Or separately
go run ./cmd/aqsh --mode=api &
go run ./cmd/aqsh --mode=worker &
```

### Testing

```bash
# Submit a job
curl -X POST http://localhost:8080/jobs/hello \
  -H "Content-Type: application/json" \
  -d '{"name": "World"}'

# Check status
curl http://localhost:8080/jobs/{id}

# Stream logs
curl -N http://localhost:8080/jobs/{id}/logs
```

## Lessons from v1

### What Worked

- The simplified API (`POST /tasks/{name}` vs Fireworq's more complex API)
- Job status tracking and result storage
- The concept of named tasks mapping to shell scripts

### What Didn't Work

- **Forking Fireworq** - Too much code to maintain, diverged from upstream
- **webhook as sidecar** - Extra component, extra configuration, coupled deployment
- **MySQL/etcd storage** - More complex than needed for this use case
- **Primary/backup model** - Can't scale workers horizontally

### Keep in Mind

1. **Simplicity is the goal** - Don't over-engineer. ~1000 lines is the target.
2. **Asynq does the heavy lifting** - Don't reimplement queue mechanics
3. **Input validation is critical** - Prevent injection attacks via explicit schema
4. **Redis Streams for logs** - Enables cross-pod log streaming
5. **Test with real scripts** - Shell execution edge cases (signals, timeouts, etc.)

## Environment

User has Redis Sentinel in production. Design must support:
```go
asynq.RedisFailoverClientOpt{
    MasterName:    "mymaster",
    SentinelAddrs: []string{"..."},
}
```

## Quick Reference

| v1 (DJQWSC) | v2 (aqsh) |
|-------------|-----------|
| `/home/rophy/projects/djqwsc` | `/home/rophy/projects/aqsh` |
| Fireworq fork | Asynq wrapper |
| MySQL/etcd | Redis |
| `DJQWSC_*` env vars | `AQSH_*` env vars |
| webhook sidecar | Built-in executor |

## Getting Started

1. Read `README.md` (design document)
2. Set up Go module: `go mod init github.com/rophy/aqsh`
3. Start with minimal working version:
   - Config loading
   - Single task type
   - Submit task → execute script → return result
4. Add features incrementally:
   - Input validation
   - Log streaming
   - Multiple queues
   - Metrics
