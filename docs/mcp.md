# MCP interface

aqsh exposes explicitly opted-in tasks through Streamable HTTP at `/mcp`, using the official MCP Go SDK. The interface is disabled by default. It is a generic task interface: database scripts and operation-specific policy belong to the project that supplies the tasks.

## Enable the interface

```sh
AQSH_MCP_ENABLED=true
AQSH_REQUIRE_IDENTITY=true
AQSH_EXECUTION_ACCESS=owner
# Optional, exact comma-separated browser origins; no wildcard or trailing slash:
AQSH_MCP_ALLOWED_ORIGINS=https://client.example
```

Startup rejects MCP without identity and owner enforcement. `AQSH_EXECUTION_ACCESS` defaults to `legacy`; it can also be set to `owner` without enabling MCP. Setting `AQSH_MCP_ENABLED=false` removes the MCP route and does not change the separately configured execution policy.

Authentication is delegated to the existing trusted gateway. The gateway must validate credentials and overwrite incoming identity/groups headers; clients must not be able to reach the backend directly or impersonate another caller with `X-Forwarded-User`. aqsh itself does not verify bearer tokens or provide OAuth discovery. Configure gateway CORS/preflight and OAuth discovery separately if the intended client requires them.

Identity and groups still use `AQSH_IDENTITY_HEADER` and `AQSH_GROUPS_HEADER`. Use a stable identity unique within the deployment's authentication domain. Accounts sharing the same service identity also share execution ownership.

Requests with an `Origin` header must match the allowlist exactly. With an empty allowlist, all requests carrying Origin are denied; machine clients without Origin are accepted subject to identity and authorization. Multiple Origin headers are rejected. The SDK also keeps its default localhost DNS rebinding protection.

## Opt in individual tasks

Enabling the endpoint does not expose every task. Add an `mcp` block only to reviewed tasks:

```yaml
tasks:
  inspect:
    script: inspect.sh
    description: Inspect an authorized target
    allowed_users: [alice]
    mcp:
      enabled: true
      name: example_inspect
      description: Return the inspection result for the selected target
      read_only: true
      result_mode: json
      max_result_bytes: 65536
    input:
      - name: target
        env: TARGET
        type: string
        required: true
        pattern: '^[a-z0-9-]{1,63}$'
        # Use values_url for caller-specific target authorization when needed.
  deploy:
    script: deploy.sh
    # No mcp block: remains REST-only.
```

| Setting | Behavior |
| --- | --- |
| `enabled` | Defaults to false; omitted/disabled tasks are hidden |
| `name` | Required when enabled; unique `[A-Za-z0-9_.-]{1,64}`; `aqsh_` is reserved |
| `description` | Optional override of the task description |
| `read_only` | Must explicitly be true in this release |
| `result_mode` | `omitted` (default) or `json` |
| `max_result_bytes` | Limit on the JSON result-file data; default 65536, maximum 1048576 |

Metadata is preserved through included task files. Invalid enabled metadata, duplicate tool names, or invalid schemas fail startup. Names are explicit because existing task names may contain slashes. Configuration is loaded at startup; restart API and worker deployments when changing task definitions.

`read_only` is a maintainer-reviewed declaration, not a sandbox or an authorization grant. Review the script, its helpers, and its result/failure paths before exposing it. Enabled write tasks are rejected in this release. Existing REST-only writes continue to use their configured permissions.

Task `allowed_users` OR `allowed_groups` remains the authorization rule. Only authorized tools appear in the caller's list; direct calls to hidden tools cannot enqueue work. An empty ACL allows every authenticated caller to submit that task. Input `values_url` checks are still required when task-level permission alone does not authorize every target. Syntax validation of a namespace or target is not access control.

## Tools and asynchronous execution

The endpoint supports the SDK's protocol lifecycle and `tools/list` / `tools/call`. HTTP mode is stateless with JSON responses, so requests can reach different API replicas without sticky sessions. The SDK negotiates supported protocol versions, including older initialized clients. GET/DELETE on `/mcp` return 405 in stateless mode; raw request bodies are capped at 1 MiB, including chunked bodies.

Each enabled task becomes one named tool. Its input schema comes from the existing task inputs:

- Strings, integers, numbers, booleans, enums, numeric bounds, descriptions, and portable patterns are represented in JSON Schema.
- Internal environment names and policy URLs are not exposed. Patterns using `(?...)` constructs are rejected for enabled MCP tasks.
- Unknown fields are rejected. Optional null follows the existing missing/default semantics.
- Defaults retain their declared type. A `type: string` flag with `default: "false"` is a string, not a boolean.
- Defaults are applied and validated server-side before `values_url` authorization, including omitted/null parameters. Missing optional dynamic inputs are checked as the empty string.
- Dynamic choices are cached separately by principal, groups, task, input, and substituted URL. Configured cache TTL determines how long authorization choices may remain cached; use no cache when immediate revocation is required.

A successful task call returns immediately:

```json
{
  "execution_id": "generated-execution-id",
  "status": "pending",
  "poll_after_ms": 1000
}
```

Call the built-in `aqsh_get_execution` with `{"execution_id":"generated-execution-id"}`. It returns `pending`, `running`, `scheduled`, `retrying`, `completed`, or `failed`. Poll with bounded backoff; do not immediately repeat the submission. Disconnecting or cancelling after enqueueing does not cancel the worker. A lost submission response may require repeating a read-only submission; submissions are not deduplicated in this release.

REST and MCP share the same validation, enqueue options, queue, worker, and stored ownership. The client cannot override the script, execution owner, or queue through protocol metadata or arguments. APIs search task-specific queues as well as configured worker queues.

## Ownership and results

Owner mode protects both `GET /executions/{id}` and `GET /executions/{id}/logs`, preventing a REST bypass of MCP restrictions. Task discovery through REST also requires identity and filters task ACLs in owner mode.

Reading requires the stored owner to match the current caller and current task/target permissions to allow access. Anonymous legacy executions, malformed ownership records, removed tasks, and inaccessible executions return the same 404 as unknown IDs. Logs are authorized before any SSE headers or lines are emitted. MCP additionally requires the task to remain opted in; it cannot retrieve results of an unrelated REST-only task.

MCP returns approved JSON from `AQSH_RESULT_FILE` only when `result_mode: json` is set. The existing worker's string-valued result data is parsed into structured JSON and mirrored in text content for clients. Example:

```json
{
  "execution_id": "generated-execution-id",
  "status": "completed",
  "retried": 0,
  "max_retry": 0,
  "exit_code": 0,
  "data": {"target": "demo", "message": "example inspection finished"}
}
```

An execution's completion and its domain result have different meanings: a successfully executed health check may report an unhealthy target. A successful query of a failed execution returns `status: failed`; it is not a failed tool call.

Unsafe/unavailable data is omitted with `result_omitted`: `not_enabled`, `unavailable`, `absent`, `empty`, `invalid_json`, or `too_large`. Oversized JSON is not truncated into an invalid document. MCP does not return raw logs, raw worker errors, `last_error`, caller groups, or the submitted environment. The task owner must sanitize approved JSON at its source; aqsh does not recognize or redact arbitrary secrets embedded in otherwise valid JSON.

Invalid arguments, permission failures, and unavailable policy/queue services produce `isError: true` with a safe error code/message. Malformed protocol messages and unknown tools follow SDK error conventions. Submission and authorized execution reads are audited by principal and execution ID without recording raw arguments.

## Local example

The [example task configuration](../examples/mcp/tasks.yaml) and [script](../examples/mcp/inspect.sh) contain no database operations. With a local Redis already running:

```sh
export AQSH_BIND=127.0.0.1:8080
export AQSH_REDIS_ADDR=localhost:6379
export AQSH_TASKS_DIR="$PWD/examples/mcp"
export AQSH_RESULTS_DIR=/tmp/aqsh-mcp-results
export AQSH_MCP_ENABLED=true
export AQSH_REQUIRE_IDENTITY=true
export AQSH_EXECUTION_ACCESS=owner
go run ./cmd/aqsh -tasks examples/mcp/tasks.yaml
```

For loopback development only, simulate the gateway's identity header. A protocol-2025 client can list tools with:

```sh
curl http://127.0.0.1:8080/mcp \
  -H 'X-Forwarded-User: alice' \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'Mcp-Protocol-Version: 2025-11-25' \
  --data '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}'
```

To submit, replace the body with:

```json
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"example_inspect","arguments":{"target":"demo"}}}
```

Then query using the returned execution ID:

```json
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"aqsh_get_execution","arguments":{"execution_id":"generated-execution-id"}}}
```

Use an SDK client for automatic negotiation/lifecycle handling. In production, call the authenticated gateway and supply its credentials rather than selecting a user identity yourself.

## Upgrade and verification

The official SDK v1.8.0 requires Go 1.25 or newer; the module declares that minimum and the toolchain/builder uses Go 1.26.8. Existing task scripts and worker payload formats are preserved.

The shared REST/MCP validator now rejects fractional/out-of-range integers, non-finite numbers, unsupported input types, invalid defaults, and defaults disallowed by target policy. REST submissions accept one JSON object with a 1 MiB body limit. These validation changes apply even when MCP is disabled; the default legacy execution-access behavior otherwise remains unchanged.

```sh
go test ./...
go test -race ./...
go vet ./...
```

`test/TestMCPRealWorkerSmoke` builds and starts aqsh, a temporary real Redis, and a real shell task, then queries its result through MCP and verifies REST ownership. It runs when `redis-server` is installed; otherwise it skips. Other tests use miniredis and the official SDK's HTTP client.

Dynamic-choice discovery as a separate MCP tool, write submission deduplication, native OAuth, human approval, team-shared executions, and database-specific pilot deployment are follow-up work. Existing submission-time `values_url` authorization remains supported.
