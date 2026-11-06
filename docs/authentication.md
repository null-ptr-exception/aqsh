# Authentication

`AQSH_AUTH_MODE=trusted-header` is the default. An authenticating proxy must be
the only caller able to reach the API; aqsh trusts its configured identity and
groups headers. `AQSH_REQUIRE_IDENTITY` retains its existing behavior.

`AQSH_AUTH_MODE=kubernetes` verifies bearer tokens through one TokenReview
endpoint: either the Kubernetes API or an external kube-federated-auth service.
Cluster routing, JWKS verification, credential renewal and Secret persistence
remain in kube-federated-auth.

## Configuration

Authentication uses environment variables, loaded at startup. Changes require an
API restart. Unknown `AQSH_AUTH_*` settings, invalid booleans/durations and invalid
combinations fail startup. Native settings require `AQSH_AUTH_MODE=kubernetes`.
There is no separate authentication YAML file.

| Variable | Meaning | Default |
|---|---|---|
| `AQSH_AUTH_MODE` | `trusted-header` or `kubernetes` | `trusted-header` |
| `AQSH_AUTH_URL` | TokenReview base URL; aqsh appends `/apis/authentication.k8s.io/v1/tokenreviews` | Required in native mode |
| `AQSH_AUTH_CA_FILE` | PEM CA file for the endpoint | System roots |
| `AQSH_AUTH_TOKEN_FILE` | Mounted reviewer credential | Required in native mode |
| `AQSH_AUTH_AUDIENCES` | Comma-separated, nonempty caller audiences without surrounding whitespace | Required in native mode |
| `AQSH_AUTH_ALLOW_HTTP` | Explicitly permit an unencrypted endpoint | `false` |
| `AQSH_AUTH_REQUEST_TIMEOUT` | Timeout including waiting for TokenReview capacity; 1ms–1m | `5s` |
| `AQSH_AUTH_CACHE_TTL` | Successful review lifetime; 0 disables caching, maximum 1m | `10s` |
| `AQSH_AUTH_INCLUDE_CLUSTER_IN_IDENTITY` | Prefix both identity and groups with the source cluster | `false` |
| `AQSH_AUTH_CLUSTER_NAME` | Local cluster name for a direct Kubernetes endpoint | Required for direct cluster qualification |
| `AQSH_AUTH_TRUST_CLUSTER_EXTRA` | Trust the cluster name supplied by a federated endpoint | `false` |

Direct Kubernetes example:

```sh
AQSH_MODE=api
AQSH_AUTH_MODE=kubernetes
AQSH_AUTH_URL=https://kubernetes.default.svc
AQSH_AUTH_CA_FILE=/var/run/secrets/kubernetes.io/serviceaccount/ca.crt
AQSH_AUTH_TOKEN_FILE=/var/run/secrets/kubernetes.io/serviceaccount/token
AQSH_AUTH_AUDIENCES=aqsh
```

The reviewer needs `create` on `tokenreviews.authentication.k8s.io`. Its credential
must target the Kubernetes API audience. Callers need tokens issued for an audience
in `AQSH_AUTH_AUDIENCES`; a token issued only for the Kubernetes API is rejected
when aqsh has a distinct configured audience.

HTTPS certificate verification is always enabled. URLs with credentials, query
strings or fragments are rejected; redirects are never followed. Reviewer tokens
are read from the file on each cache miss, so projected token rotation is picked
up without restart. CA files are loaded at startup.

## Federated authentication and cluster trust

For kube-federated-auth, use the same client with these settings:

```sh
AQSH_MODE=api
AQSH_AUTH_MODE=kubernetes
AQSH_AUTH_URL=http://kube-federated-auth.auth.svc:8080
AQSH_AUTH_ALLOW_HTTP=true
AQSH_AUTH_TOKEN_FILE=/etc/aqsh/reviewer-token
AQSH_AUTH_AUDIENCES=aqsh
AQSH_AUTH_TRUST_CLUSTER_EXTRA=true
```

The reviewer credential must satisfy kube-federated-auth's caller authentication
policy, including `authorized_clients` when configured. Credentials and permissions
for each source cluster stay in the external service.

`AQSH_AUTH_ALLOW_HTTP=true` is an operator trust decision: both reviewer and caller
tokens travel unencrypted. Use HTTPS outside a trusted network. A `.svc` hostname
does not automatically enable HTTP or trust cluster metadata.

With `AQSH_AUTH_TRUST_CLUSTER_EXTRA=false`, response cluster metadata is ignored,
including metadata supplied by Kubernetes authentication webhooks. Direct cluster
qualification uses only the configured `AQSH_AUTH_CLUSTER_NAME`.

With `AQSH_AUTH_TRUST_CLUSTER_EXTRA=true`, every successful review must include
exactly one valid value in
`status.user.extra["authentication.kubernetes.io/cluster-name"]`. Missing or
malformed values return 401. A static `AQSH_AUTH_CLUSTER_NAME` cannot be combined
with this option, so federation cannot fall back to a shared static cluster.
Cluster names exclude whitespace, control characters, commas, `/` and `\`.

## Identity and task authorization

By default, identity and groups are the verified values returned by TokenReview.
The same namespace/serviceaccount and group names therefore match the same task
ACLs across trusted clusters.

Set `AQSH_AUTH_INCLUDE_CLUSTER_IN_IDENTITY=true` to prefix **both** the username
and every group with `<cluster>/`. Direct Kubernetes also requires
`AQSH_AUTH_CLUSTER_NAME`; federated mode uses its trusted response metadata.

```yaml
allowed_users:
  - cluster-a/system:serviceaccount:team:caller
allowed_groups:
  - cluster-a/system:serviceaccounts:team
```

The existing user/group ACL rule is OR: either an allowed user or an allowed group
can submit. In qualified mode, unprefixed groups are not retained as aliases.
Update ACLs and downstream identity consumers when enabling qualification.
The same verified identity/groups flow into task authorization, `${identity}` and
`${groups}` in values URLs, queued `identity`/`groups`, `AQSH_IDENTITY`/`AQSH_GROUPS`
in scripts, execution responses and access logs.

Groups must be representable as CSV without commas, line breaks, NUL or surrounding
whitespace. Usernames must be nonempty and exclude line breaks and NUL. UID is
optional. The request context contains only the identity/groups needed for
authorization; no extra Principal is persisted in the queue. Forwarded identity,
groups and cluster headers are discarded, and bearer credentials are never passed
to task scripts or queued.

All application routes require authentication. Exact `GET /health` and
`GET /metrics` remain public for probes/scrapers; control their network access at
the gateway. Authentication denials, audience mismatches and invalid trusted
cluster metadata return 401. Transport failures, non-success endpoint responses
(including reviewer credential rejection), or invalid/missing response status
return 503 without exposing credentials or backend response details.

## Short-lived review cache

Each API process caches up to 256 successful reviews, keyed by a SHA-256 token
hash. Only identity/groups are cached. Entries expire at a fixed deadline measured
from the start of the review; cache hits do not extend it. Errors and denials are
not cached, expired entries are never reused on failure, and excess successful
reviews are served without caching when capacity is full. At most 32 TokenReview
requests run concurrently, with capacity wait included in the request timeout.

A live cache entry avoids repeated endpoint calls during polling or brief outages.
Token expiry, revocation or membership changes can take up to the configured TTL
to be reflected after a successful review. This delay is additional to any cache
in the configured endpoint. Set `AQSH_AUTH_CACHE_TTL=0s` to validate every request.
Caching does not replace task authorization, which runs on every submission.

## Deployment and migration

Native authentication requires `AQSH_MODE=api` and a separate worker Pod with
`AQSH_MODE=worker`. The worker must have no reviewer credential mounts or
TokenReview RBAC. Workers ignore API authentication settings. Give runbooks only
the filesystem, database and cluster permissions their tasks need.

To replace kube-auth-proxy:

1. Configure the separate API and worker, mounting the reviewer credential only
   in the API and granting its TokenReview permission or federated caller access.
2. Point the gateway/Service at the native API port and preserve the caller's
   Authorization header.
3. Verify accepted and denied task submissions, then remove the proxy sidecar.
   Keep the external kube-federated-auth service for multi-cluster routing.

To roll back, restore the authenticating proxy route and explicitly select
`trusted-header`, removing native `AQSH_AUTH_*` settings. Restrict backend access
to that proxy. Existing tasks retain their identity format unless qualification
is explicitly enabled.

Execution reads retain the existing policy: authenticated callers who know an
execution ID can read its status, result and logs. Cluster-qualified task ACLs do
not provide execution ownership isolation. Owner checks are a separate follow-up.
