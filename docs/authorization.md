# Authorization

Authentication establishes the caller identity and groups; authorization decides
which tasks and executions that caller may access. Both native TokenReview and
trusted-header deployments use the same policy. In trusted-header mode, only an
authenticating proxy that overwrites caller headers may reach the API.

## Configuration

RBAC is enabled by the presence of either top-level `roles` or `bindings` in the
main `tasks.yaml`. Existing `include` files may define tasks, not roles/bindings.

```yaml
tasks:
  deploy:
    script: deploy.sh

roles:
  runner:
    - tasks: [deploy]
      verbs: [run]
  reader:
    - tasks: [deploy]
      verbs: [read]
  operator:
    - tasks: ["*"]
      verbs: [read, run]

bindings:
  runner:
    users: [system:serviceaccount:ci:deployer]
    groups: [system:serviceaccounts:dev-a]
  reader:
    users: [system:serviceaccount:ops:auditor]
  operator:
    groups: [system:serviceaccounts:ops]
```

Binding keys reference role names. A user or any group match grants the role;
multiple roles add permissions. Subjects match exactly, with no case folding or
wildcards. Native mode uses verified groups as separate values, not caller-supplied
headers. When cluster qualification is enabled, use the qualified user/group
strings, for example `cluster-a/system:serviceaccount:ci:deployer`; unqualified
subjects are not aliases. A shared SA is a shared owner identity.

Rules accept exact task names or `*` for all current and future tasks. They may
reference retired task names to authorize retained history. Verbs are only `read`
and `run`; neither role inheritance nor deny rules are supported. Empty roles or
bindings grant nothing. Unknown roles, verbs, YAML fields, null policy sections
and multiple YAML documents fail configuration loading. Validation also applies
to workers loading the shared task configuration.

## API behavior

| Request | Required permission in RBAC mode |
|---|---|
| `GET /tasks` | Nonblank identity; returns only tasks with `run` or `read` |
| `GET /tasks/{name}` | `run` or `read` for that task |
| `POST /tasks/{name}` | `run` for that task |
| `GET /executions/{id}` | `read`, or exact submitter identity plus current `run`, for the queued task name |
| `GET /executions/{id}/logs` | Same check as execution status, before SSE headers/data |

Missing identity returns 401, even when `AQSH_REQUIRE_IDENTITY` is false. Denied
submissions return 403 before input validation or remote values lookup. Definitions
without grants, inaccessible executions and missing records return 404. Execution
Redis lookup failures return 503 without backend details. Exact public health and
metrics routes keep their existing behavior. RBAC does not grant credentials to
scripts or replace the worker's database, filesystem or cluster permissions.

`read` covers metadata, results and logs together. A run-only caller cannot read a
peer's execution merely because they share a group. Owner access requires current
`run`; a caller who loses `run` can still read if they separately retain `read`.
Queued groups are audit metadata, not permission grants. Ownerless legacy records
with a valid task name are readable via `read`, never via ownership. Invalid or
foreign records cannot be authorized, even by a wildcard role.

## Lifecycle and retention

Policy is loaded at startup. Restart every API replica with consistent policy to
change grants; there is no live policy reload. Each new request uses its API's
current policy and caller identity/groups. Authentication caches can delay identity
or membership changes as described in [authentication](authentication.md).

Revocation does not cancel accepted, queued or retrying work, change the worker
identity, or terminate an already-open log stream. Reconnects are checked again.
Task deletion or renaming does not rewrite an execution's original task name.
Retained executions continue to use that name for authorization, without resolving
the current task definition or calling its `values_url`.

Execution lookup searches `default`, `AQSH_WORKER_QUEUES`, the default task queue
and current task queues, checking each distinct queue once. Queue discovery does
not resolve timeouts or retry settings. Keep retired queue names in the API's
`AQSH_WORKER_QUEUES` while their retained executions still need status or log
access, even after removing or renaming the task. A retained record is not
discoverable once its queue is no longer configured. Once an execution record
expires or is deleted, its logs are inaccessible in RBAC mode even if the log
stream still exists. Legacy log reads remain independent of execution records.

## Migration

With neither policy section present, legacy submission ACLs and execution reads
remain unchanged. This is upgrade compatibility, not automatic ownership isolation.

Upgrade in this order:

1. Deploy the new binary to **all API and worker replicas**, keeping the existing
   `allowed_users`/`allowed_groups` configuration unchanged.
2. After confirming every replica runs the new binary, replace legacy ACLs with
   `roles`/`bindings`, then restart all replicas with the consistent new policy.
   RBAC enforcement is complete only after every API replica loads that policy.

Older binaries ignore `roles`/`bindings`. Deploying the RBAC configuration first
removes the legacy ACLs while those binaries still serve requests, opening tasks
to everyone. Do not run an older binary against an RBAC configuration.

For example, replace this legacy task ACL:

```yaml
tasks:
  deploy:
    script: deploy.sh
    allowed_users: [system:serviceaccount:ci:deployer]
    allowed_groups: [system:serviceaccounts:dev-a]
```

with a task definition without the ACL, a role granting `run` for `deploy`, and a
binding carrying those same users/groups. Add `read` only where sharing execution
access is intended. Tasks previously open to everyone also require explicit grants
in RBAC mode. Nonempty `allowed_users`/`allowed_groups` anywhere in the loaded task
set are rejected when RBAC is enabled, including ACLs in included files.

Before downgrading the binary, restore the intended legacy ACLs, remove both
policy sections and restart all replicas while still running the new binary.
Only then deploy the older binary. Rolling back to no policy also restores the
broader legacy execution-read behavior. Explicit `roles: {}` or
`bindings: {}` is an enabled policy with no grants, not a rollback. Delete the
sections entirely to select legacy mode; `null` is invalid.

## Target-level authorization

These roles authorize tasks, not environments, projects, databases or other input
targets. `values_url` remains a separate submission-time input check and is not
consulted when reading historical results. Giving `read` for a shared task permits
reading its executions for every target. Target-level execution isolation needs a
separate design; it is not implied by task RBAC or cluster-qualified identities.
