# Authentication

aqsh supports two explicit authentication modes. Existing deployments keep
`AQSH_AUTH_MODE=trusted-header` by default. In that mode, an authenticating proxy
must be the only caller able to reach the API; aqsh trusts its configured identity
and groups headers. `AQSH_REQUIRE_IDENTITY` retains its existing behavior.

`AQSH_AUTH_MODE=kubernetes` uses a small native TokenReview client. The same client
can call a Kubernetes API or an external kube-federated-auth service. aqsh owns
request authentication, verified identity mapping and task authorization. Cluster
routing, JWKS, reviewer credential renewal and Secret persistence remain in the
external service. aqsh does not embed either auth service or cache TokenReviews.

## Native Kubernetes authentication

Run the API with `AQSH_MODE=api`, `AQSH_AUTH_MODE=kubernetes`, and
`AQSH_AUTH_CONFIG=/etc/aqsh/auth.yaml`:

```yaml
audiences: [aqsh]
request_timeout: 5s
include_cluster_in_identity: false
clusters:
  production-a:
    api_server: https://kubernetes.default.svc
    ca_file: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt
    token_file: /var/run/secrets/kubernetes.io/serviceaccount/token
```

The configuration keeps the existing `clusters` shape but accepts exactly one
TokenReview endpoint. `api_server` is its base URL; aqsh appends
`/apis/authentication.k8s.io/v1/tokenreviews`. For a direct Kubernetes API,
`production-a` is the configured fallback cluster name. An external endpoint can
return identities from multiple clusters through that same endpoint.

Configuration is loaded at startup; changes require an API restart. Unknown
fields, invalid endpoints, missing audiences and unreadable credentials cause
startup to fail. HTTPS is required unless `allow_http: true` is explicitly set on
that endpoint. Omitting `ca_file` uses system roots. HTTPS certificate verification
cannot be disabled, and redirects are never followed, including HTTPS-to-HTTP
redirects. Requests have the configured timeout (default 5 seconds).

The caller sends `Authorization: Bearer <token>` issued for an audience in the
configured list. aqsh submits a TokenReview with those audiences using its
**reviewer** credential. For a direct Kubernetes API, the reviewer needs `create`
on `tokenreviews.authentication.k8s.io`. Its token must target the Kubernetes API
audience, which may differ from the caller's `aqsh` audience.

The response must authenticate the caller, include a username and UID, and return
an audience intersecting the request. Authentication denial returns 401.
Connection failures, timeouts, non-success HTTP responses (including reviewer
credential rejection and HTTP 5xx), response-body decode/size failures, or malformed
cluster metadata return 503 without exposing backend details. A successful HTTP response reporting
`authenticated: false` is treated as authentication denial, not classified by
matching its error text. There is no fallback to forwarded identity headers.

All API endpoints require authentication except exact `GET /health` and
`GET /metrics`. Limit access to these operational endpoints at the gateway or
network boundary. The coverage debug endpoint also requires authentication.

## Using kube-federated-auth

Point the same client at the external service:

```yaml
audiences: [aqsh]
request_timeout: 5s
include_cluster_in_identity: false
clusters:
  fallback-cluster:
    api_server: http://kube-federated-auth.auth.svc:8080
    allow_http: true
    token_file: /etc/aqsh/auth/reviewer-token
```

`allow_http` is an explicit operator trust decision for a trusted in-cluster
connection. aqsh does not infer network boundaries or automatically trust a
`.svc` hostname. Plain HTTP sends both the reviewer credential and caller token
without transport encryption; use HTTPS for endpoints outside that trusted
network, or expose the service through a TLS endpoint and omit `allow_http`.

The reviewer credential must be accepted by kube-federated-auth's caller
authentication policy, including `authorized_clients` when configured. The
external service owns the credentials and TokenReview permissions needed in each
source cluster; those credentials are not mounted in aqsh. aqsh sends requests
only to its configured endpoint and does not inspect token issuers or fetch JWKS.

For an authenticated response, aqsh reads the cluster name from
`status.user.extra["authentication.kubernetes.io/cluster-name"]`. A single valid
value takes precedence over the configured fallback. If the key is absent, aqsh
uses the configured name, preserving direct Kubernetes API support. If the key
is present but empty, contains multiple values, or has an invalid value, aqsh
returns 503 instead of silently using the fallback. Cluster names must be
nonempty and contain no whitespace, control characters, `/` or `\`.

Each authenticated request causes a TokenReview call. Any caching and its effect
on revocation timing belong to the configured endpoint; aqsh adds no second
cache layer.

## Identity and task authorization

By default, task identity is the verified username returned by TokenReview.
For example, `system:serviceaccount:team:caller` matches the same `allowed_users`
entry across trusted clusters. The same namespace/service account therefore
identifies one workload for task authorization across those clusters. Cluster
and UID remain recorded separately in the Principal for provenance.

Set `include_cluster_in_identity: true` to use `<cluster>/<username>` instead:

```yaml
allowed_users:
  - cluster-a/system:serviceaccount:team:caller
```

This changes `allowed_users` matching, `${identity}` in values URLs,
`AQSH_IDENTITY`, the queued `identity` and the access-log identity. Update matching
ACLs and downstream identity consumers when enabling it. `principal.username`
always retains the original TokenReview username; `principal.identity` is added
when qualification is enabled. Groups are unchanged, so this option does not
scope `allowed_groups` to a cluster.

The middleware discards forwarded identity, groups and cluster headers, strips
the bearer token, and passes a verified Principal through request context.
`allowed_groups`, `${groups}` and `AQSH_GROUPS` keep their existing group
semantics. Group names must be representable as CSV without commas, line breaks
or surrounding whitespace; ambiguous names fail authentication. Bearer and
reviewer tokens are never queued or passed to scripts.

This integration authenticates requests. It preserves the existing execution
read policy: authenticated callers who know an execution ID can read its status,
result and logs. Cluster-qualified identity does not add execution ownership or
tenant isolation. Trust only endpoints and source clusters whose authenticated
users may access this aqsh instance.

## Separate API and worker credentials

Native authentication rejects `AQSH_MODE=both`. Deploy a separate worker Pod
using `AQSH_MODE=worker`, with the same Redis/task configuration but without the
auth configuration, reviewer token mounts, or TokenReview RBAC. Workers ignore
the API authentication configuration.

Runbooks execute shell commands with access to their process environment and
filesystem. Use separate Pods and service accounts so the worker does not share
the API's reviewer credentials, and grant workers only the database/cluster
permissions their tasks need.

Projected reviewer tokens are read from `token_file` for every TokenReview so
rotation is picked up without restarting the API. CA files are loaded at startup;
restart the API after CA changes. aqsh does not renew tokens or persist them in
Secrets.

## Migrating from kube-auth-proxy

1. Create a dedicated API service account and mount its reviewer credential and
   auth configuration only in the API Pod. Grant direct TokenReview permission
   or authorize that caller in the external federated-auth service as appropriate.
2. Run a separate worker Pod and verify its task permissions and queue settings.
3. Point the gateway/Service directly at the native API port. Preserve the
   caller's Authorization header; do not substitute the gateway's identity.
4. Verify allowed and denied task submissions before removing aqsh's proxy
   sidecar. Keep kube-federated-auth and other auth services used by applications.

To roll back, restore the authenticating proxy route and explicitly select
`trusted-header`. Ensure that callers cannot directly reach the header-trusting
backend. Existing task definitions and shell scripts keep their identity behavior
unless cluster qualification is explicitly enabled.
