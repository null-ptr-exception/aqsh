# Input-Level RBAC via Remote Values URL

## Problem

aqsh supports per-task authorization via `allowed_users` and `allowed_groups`, but this is too coarse for many real-world scenarios. Consider a "upgrade DB" task where a user selects from 1000 database instances — creating a separate task definition per instance doesn't scale, and static enum lists can't enforce per-user access control.

We need a way to:
1. Dynamically populate allowed values for an input parameter from an external source.
2. Filter those values based on who is submitting the task.
3. Enforce that submitted values are within the user's authorized set.

## Prior Art: Rundeck Remote URL

Rundeck's [Remote URL for option values](https://docs.rundeck.com/docs/manual/jobs/job-options.html#text-option-type) provides a good model:

- A job option can specify a URL that returns allowed values.
- The URL receives context variables (user, other option values) via template substitution.
- The remote service decides which values to return per user — authorization is delegated.
- Response format: `[{"name": "Display Label", "value": "actual_value"}]`

## Design

### Configuration

Add `values_url` to input definitions in `tasks.yaml`:

```yaml
tasks:
  upgrade-db:
    script: upgrade-db.sh
    description: "Upgrade a database instance"
    allowed_groups: [dba-team]
    input:
      - name: instance
        env: DB_INSTANCE
        required: true
        type: string
        values_url: "http://authz.internal/db-instances?user=${identity}&groups=${groups}"
        description: "Target database instance"
```

### Template Variables

The following variables are substituted into `values_url` before the request is made:

| Variable | Source | Example |
|----------|--------|---------|
| `${identity}` | Identity header value | `alice@example.com` |
| `${groups}` | Groups header value (comma-separated) | `dba-team,platform` |
| `${task}` | Task name | `upgrade-db` |

### Remote URL Response Format

The remote service returns a JSON array. Three formats are accepted (following Rundeck's convention):

**Simple list:**
```json
["prod-db-001", "prod-db-002", "staging-db-001"]
```

**Name-value pairs (ordered, with display labels):**
```json
[
  {"name": "Production DB 001", "value": "prod-db-001"},
  {"name": "Production DB 002", "value": "prod-db-002"}
]
```

**Key-value object (unordered):**
```json
{"Production DB 001": "prod-db-001", "Production DB 002": "prod-db-002"}
```

### Validation Flow

On `POST /tasks/{name}`:

1. Task-level authorization runs first (`allowed_users` / `allowed_groups`).
2. For each input with `values_url`:
   a. Substitute template variables into the URL.
   b. Fetch the URL (with timeout).
   c. Parse the response into a set of allowed values.
   d. If the submitted value is not in the set, reject with **403 Forbidden**.
3. Standard input validation (type, pattern, enum) runs after.

On `GET /tasks`:

- If a request includes identity/groups headers, inputs with `values_url` include the fetched values in the response (so clients can render dropdowns).
- If no identity is provided, `values_url` inputs omit the values list and just indicate `"values_url": true` to signal dynamic values.

### Error Handling

| Scenario | Behavior |
|----------|----------|
| Remote URL returns non-200 | Reject task submission with 502 |
| Remote URL times out | Reject task submission with 504 |
| Remote URL returns invalid JSON | Reject task submission with 502 |
| Remote URL returns empty list | Submitted value will always fail validation (403) |
| `values_url` and `enum` both set | Config error at load time |

### Interaction with Existing Validation

- `values_url` and `enum` are mutually exclusive (both define allowed values; one static, one dynamic).
- `pattern`, `min`, `max`, `type` still apply after `values_url` validation.
- `required` and `default` work as before.

## Implementation Plan

### Phase 1: Core `values_url` validation on submit

Add `values_url` field to `Input` struct. On task submission, fetch the URL with substituted variables, parse the response, and validate the submitted value against the returned set. Reject with 403 if not in set, 502/504 on fetch errors.

Scope:
- `internal/tasks/tasks.go` — add `ValuesURL` field, mutual exclusion check with `enum`
- `internal/api/api.go` — fetch + validate logic in submit handler
- New `internal/api/values.go` — URL template substitution, HTTP fetch, response parsing
- Unit tests for URL substitution, response parsing, mutual exclusion
- Integration test with a mock HTTP server

### Phase 2: Expose values in `GET /tasks`

When `GET /tasks` is called with identity headers, fetch `values_url` for each input and include the allowed values in the response. This enables clients to render dynamic dropdowns.

Scope:
- `internal/api/api.go` — update `handleListTasks` to optionally fetch values
- Consider parallel fetching if multiple inputs have `values_url`

### Phase 3: Caching

Add a short-TTL in-memory cache (keyed by URL after substitution) to avoid hitting the remote service on every request. Configurable TTL via `values_cache` on the input or a global default.

```yaml
input:
  - name: instance
    values_url: "http://authz.internal/db-instances?user=${identity}"
    values_cache: 30s
```

Scope:
- Cache implementation (simple map with expiry, or `sync.Map` + TTL)
- Cache key: fully-substituted URL
- Default TTL: 0 (no cache), configurable per-input

### Phase 4: Cascading parameters

Allow one input's `values_url` to reference another input's submitted value, enabling dependent dropdowns (e.g., select region first, then instance list filters by region).

```yaml
input:
  - name: region
    env: REGION
    type: string
    enum: [us-east-1, eu-west-1]
  - name: instance
    env: DB_INSTANCE
    type: string
    values_url: "http://authz.internal/instances?region=${input.region}&user=${identity}"
```

This requires defining input evaluation order (top-to-bottom) and validating inputs sequentially when cascading is used.
