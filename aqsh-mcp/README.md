# aqsh-mcp

Standalone, read-only MCP adapter for aqsh's REST API. It has no dependency on
aqsh's Go packages, Redis, queue implementation, or shell runner. Existing task
projects own the explicit tool allowlist and approve the JSON output they expose.

This directory is an independent Go module. Run the commands below from
`aqsh-mcp/`; the repository root's `go test ./...` does not include this module.

## Requirements

- Go 1.25+ (the toolchain is pinned for reproducible builds).
- aqsh with task RBAC enabled, `rbac_enabled` in `/health`, and the original task
  name in execution metadata. Structured REST JSON results (#14) are not required.
- An OAuth authorization server with discovery, a JWKS endpoint, and signed JWT
  **access tokens** for the exact public MCP URL as audience. First version
  supports RS256, ES256, PS256 and EdDSA; opaque-token introspection is deferred.

The authorization server owns login, consent, authorization code + PKCE (S256),
client registration (CIMD, pre-registration or DCR), and token refresh. The adapter
is the OAuth resource server: it advertises protected resource metadata and
validates signature, issuer, audience, expiry, not-before, subject and scopes.
No production authorization server or password store is implemented here.

## Run

1. Start aqsh with `AQSH_BIND=127.0.0.1:8080`, `AQSH_AUTH_MODE=trusted-header`,
   and explicit `roles` / `bindings` (see `examples/tasks.yaml`). The adapter
   requires `/health` to report `rbac_enabled: true` at startup and before every
   authenticated MCP request. Missing or disabled RBAC makes it refuse service.
2. Copy `examples/config.yaml` to `config.yaml`. Set the public MCP URL, issuer,
   required scopes, and explicit group mappings for your authorization service.
3. Select reviewed read-only tasks in `tools`. Tasks without an entry are hidden.
   Configure `result_mode: json` only when their result files are safe to expose;
   the default omits results. `read_only: true` is a reviewed declaration, not a
   script sandbox. All write tools are rejected in this version.
4. Run `go run ./cmd/aqsh-mcp -config config.yaml`.

An example task and script are in `examples/`. Copy them into aqsh's task project
to exercise the REST-to-MCP path with generic test data.

## Deployment and identity boundary

Deploy aqsh-mcp alongside aqsh in the same Pod/network namespace. `backend_url`
must use loopback; remote header delegation is deliberately unsupported in this
version. Expose only the adapter's `/mcp`, protected resource metadata and health
routes through the ingress. Terminate public TLS there. HTTP is permitted only
for loopback URLs during local development; issuer and JWKS endpoints otherwise
require HTTPS. No redirects are followed by backend or discovery HTTP clients.

The adapter constructs new REST requests and never forwards incoming identity
headers or OAuth tokens. The aqsh identity is `oauth:<issuer>#<URL-escaped-sub>`;
it remains stable across token refresh and includes the issuer. Token `scope`
and `scp` may contain a space-separated string or string array. `groups_claim`
selects a top-level array of strings; only configured `group_mapping` entries
become aqsh groups. Bind these groups or canonical identities through aqsh's RBAC
configuration. OAuth scopes permit MCP access; they do not grant aqsh's `run` or
`read` verbs. aqsh remains the final authority for discovery, submissions and
execution access. The adapter does not reproduce its policy evaluator.

Other REST entry points may retain native TokenReview or an authenticating
proxy. Every API instance sharing a queue must enforce the same RBAC policy;
otherwise a legacy instance can still expose execution records. Trusted-header
instances must remain private behind their authenticating entry point. Identity
headers must distinguish principals across all issuer and cluster domains. Do
not map all OAuth callers onto a shared ServiceAccount. Kubernetes TokenReview
does not validate these OAuth tokens; this adapter neither mints Kubernetes tokens
nor changes native authentication.

## MCP contract

- `tools/list` converts only explicitly exposed tasks that aqsh lets the caller
  discover into JSON Schema. Missing/denied definitions are hidden; backend
  failures return a generic error. Both `run` and `read` permit discovery in aqsh,
  but the latter alone cannot submit a task. Internal environment names, policy
  URLs and raw backend errors are not returned.
- Task tools return `{execution_id, status, poll_after_ms}` after submission.
  They do not report completion at enqueue time. `aqsh_get_execution` asks aqsh
  to authorize each read, then checks the original queued task name against the
  exposure allowlist. Current `run` permits reading one's own execution; `read`
  permits shared reads. Revocation applies on the next request. A retained
  execution can still be polled after its task definition is removed, while its
  task remains exposed and its queue remains configured in aqsh.
- Defaults are parsed into the declared types and submitted explicitly before
  aqsh's target authorization. Optional blank `values_url` inputs are explicit
  too. Fractional integers, unknown fields, unsupported schemas, and integers
  beyond JSON's exact ±(2^53-1) range are rejected.
- Result output is limited to status, timestamps, exit code and approved JSON
  parsed from the existing REST `result.data` string.
  Raw logs, errors, identity/groups and payload/environment are not exposed.
  Missing, invalid or oversized JSON is omitted with a reason, never truncated.
  JSON null and large numbers inside result JSON retain their wire representation.
- Submission POSTs are never automatically retried by the adapter. A transport
  failure can leave submission outcome uncertain; durable deduplication and
  mutation tools are deferred.

Native clients may omit `Origin`. Browser origins must be explicitly allowed.
OAuth discovery is public; `/mcp` always requires a valid token and required scopes.

## Validation

```sh
go test -race ./...
go vet ./...
```

The HTTP tests use the official MCP Go SDK, a local signing issuer, and REST
fixtures. The issuer is test-only: user login is simulated, while discovery,
authorization-code exchange, single-use codes, PKCE S256, resource/audience,
scopes and JWT verification run over HTTP.

For real Redis/aqsh/shell execution, build the two local binaries and run:

```sh
# First build aqsh from this checkout, including the REST metadata additions:
# (cd /path/to/aqsh && go build -o /tmp/aqsh-under-test ./cmd/aqsh)
go build -o /tmp/aqsh-mcp-under-test ./cmd/aqsh-mcp
AQSH_BINARY=/tmp/aqsh-under-test \
AQSH_MCP_BINARY=/tmp/aqsh-mcp-under-test \
go test -race ./... -count=1
```

`redis-server` must be on PATH. The integration test starts isolated loopback
services, uses temporary files, and shuts down its processes. It covers real
execution, legacy REST result compatibility, peer denial on status/logs,
shared-reader access without submission rights, revocation across backend restart,
default-target authorization, and hidden tool rejection. CI runs both the module
tests and this integration test; it also builds the separate adapter image.

The same worker integration can use a disposable Keycloak container instead of
the test signing issuer:

```sh
AQSH_BINARY=/tmp/aqsh-under-test \
AQSH_MCP_BINARY=/tmp/aqsh-mcp-under-test \
AQSH_KEYCLOAK_IMAGE=quay.io/keycloak/keycloak:26.8.2 \
go test -race ./internal/adapter -run '^TestRealQueueAndWorker$' -count=1 -timeout=180s
```

This requires Docker. It imports synthetic users and a pre-registered public
client, then uses the official Go MCP client's authorization-code handler with
Keycloak login and PKCE S256. The container is removed on completion. The client
must emit a stable `sub`, the MCP URL as audience, required scopes, and the mapped
groups; for existing Keycloak setups, retain its `basic` client scope or an
equivalent subject mapper. See the [Keycloak subject claim migration](https://www.keycloak.org/docs/26.2.5/upgrading/).

CI runs this acceptance fixture too. Company SSO configuration and
Codex/Claude/ChatGPT interoperability still require deployment-specific checks;
the fixtures do not establish compatibility with every service or client.

## Container

```sh
docker build -t aqsh-mcp:dev .
```

Mount configuration at runtime and set `bind: 0.0.0.0:8090` inside a Pod. The
container runs as non-root and contains no aqsh worker or database credentials.

## References

- [MCP authorization](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization)
- [Official MCP Go SDK](https://go.sdk.modelcontextprotocol.io/)
- [aqsh architecture review](https://github.com/null-ptr-exception/aqsh/pull/13#pullrequestreview-5456312995)
