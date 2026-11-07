# aqsh-mcp

Standalone, read-only MCP adapter for aqsh's REST API. It has no dependency on
aqsh's Go packages, Redis, queue implementation, or shell runner. Existing task
projects own the explicit tool allowlist and approve the JSON output they expose.

This directory is an independent Go module. Run the commands below from
`aqsh-mcp/`; the repository root's `go test ./...` does not include this module.

## Requirements

- Go 1.25+ (the toolchain is pinned for reproducible builds).
- aqsh with owner-only execution reads and structured JSON execution results.
- An OAuth authorization server with discovery, a JWKS endpoint, and signed JWT
  **access tokens** for the exact public MCP URL as audience. First version
  supports RS256, ES256, PS256 and EdDSA; opaque-token introspection is deferred.

The authorization server owns login, consent, authorization code + PKCE (S256),
client registration (CIMD, pre-registration or DCR), and token refresh. The adapter
is the OAuth resource server: it advertises protected resource metadata and
validates signature, issuer, audience, expiry, not-before, subject and scopes.
No production authorization server or password store is implemented here.

## Run

1. Start aqsh with the two API changes, `AQSH_BIND=127.0.0.1:8080`, and
   `AQSH_EXECUTION_OWNER_ONLY=true`. The adapter checks these capabilities at
   startup and before handling MCP requests, refusing an unprotected backend.
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
become aqsh groups. Use these mapped groups or canonical identities in aqsh's
`allowed_groups`, `allowed_users` and `values_url` policy. aqsh remains the final
authority for submissions and execution ownership.

Existing Kubernetes clients can retain their kube-auth-proxy REST path. Every
API instance sharing a queue must enforce owner-only reads and remain behind a
trusted proxy. Identity headers must distinguish principals across all issuer
and cluster domains; namespace/name alone is insufficient across clusters. Do
not map all OAuth callers onto a shared ServiceAccount. This adapter does not
change Kubernetes token validation or mint Kubernetes tokens.

## MCP contract

- `tools/list` converts only allowed task definitions into JSON Schema and
  filters them using current task user/group ACLs. Internal environment names,
  policy URLs and raw backend errors are not returned.
- Task tools return `{execution_id, status, poll_after_ms}` after submission.
  They do not report completion at enqueue time. `aqsh_get_execution` polls only
  the caller's executions of currently exposed, authorized tasks.
- Defaults are parsed into the declared types and submitted explicitly before
  aqsh's target authorization. Optional blank `values_url` inputs are explicit
  too. Fractional integers, unknown fields, unsupported schemas, and integers
  beyond JSON's exact ±(2^53-1) range are rejected.
- Result output is limited to status, timestamps, exit code and approved JSON.
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
# First build aqsh from a checkout containing both REST API changes:
# (cd /path/to/aqsh && go build -o /tmp/aqsh-under-test ./cmd/aqsh)
go build -o /tmp/aqsh-mcp-under-test ./cmd/aqsh-mcp
AQSH_BINARY=/tmp/aqsh-under-test \
AQSH_MCP_BINARY=/tmp/aqsh-mcp-under-test \
go test -race ./... -count=1
```

`redis-server` must be on PATH. The integration test starts isolated loopback
services, uses temporary files, and shuts down its processes. It covers real
execution, legacy REST result compatibility, owner isolation on status/logs,
default-target authorization, and hidden tool rejection.

Real Keycloak/Auth0/company SSO and Codex/Claude/ChatGPT interoperability remain
deployment acceptance checks. Passing the local issuer tests does not establish
compatibility with every authorization service or client.

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
