# aqsh - AI Assistant Instructions

## Quick Reference

- `README.md` - Design document and configuration
- `docs/api.md` - API reference
- `CONTRIBUTING.md` - Development and testing instructions

## Code Navigation

| Directory | Purpose |
|-----------|---------|
| `cmd/aqsh/` | CLI entry point |
| `internal/api/` | HTTP handlers |
| `internal/config/` | Configuration loading |
| `internal/tasks/` | Task config parsing & validation |
| `internal/worker/` | Asynq handler, shell execution |
| `internal/logs/` | Redis Streams log handling |

## Guidelines

1. Keep it simple - target ~1000 lines of Go
2. Let Asynq handle queue mechanics
3. Input validation prevents injection attacks
4. Test with real shell scripts for edge cases
