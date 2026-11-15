# 0001: The coordinator runs apart from the triage controller

- Status: accepted
- Date: 2026-11-15

## Context

Each triage step (and, later, each remediation step) needs its own short-lived identity: a ServiceAccount, a Role pinned to that step's objects, and a RoleBinding. Something has to create and delete those, which takes permission to create RBAC objects. The remediation plan had chosen a modular monolith: one controller binary with triage and remediation as internal modules.

The triage controller accepts webhooks from SigNoz and requests from sandboxes, the two least trusted callers in the system.

## Decision

A third component, the coordinator, owns step identities and step order. It is a separate Deployment with its own ServiceAccount, built from the same image recipe (`cmd/coordinator`, `internal/coordinator`). It has no ingress, egress only to the API server, and RBAC limited to:

- ServiceAccounts, Roles, and RoleBindings in `agent-sandboxes` (create, get, delete; no update, list, `escalate`, or `bind`),
- the triage-group permissions it may grant (`coordinator-grantable`), and
- Incidents and TriageRuns.

A ValidatingAdmissionPolicy restricts what it creates to step-shaped identities.

The code stays a modular monolith: one Go module, shared packages, interfaces between them. Only the process boundary is new.

## Alternatives considered

- **A module inside the triage controller.** Simpler to deploy, but a bug or compromise in the webhook or agent API would hold RBAC-write permission. Rejected.
- **`escalate` or `bind` on the coordinator, so it can grant anything.** Removes the need for `coordinator-grantable`, but any compromise could then grant cluster permissions. Rejected.
- **One shared agent identity (what we had).** Every agent could get and update every run, and its permissions outlived each run. Rejected.

## Consequences

- One more pod (about 40 MiB) and one more image.
- Granting agents a new kind of permission means adding it to `coordinator-grantable`. That list is the ceiling for every step identity.
- Identity objects are created by the coordinator only. If you need to debug a run's RBAC, `kubectl auth can-i --as=system:serviceaccount:agent-sandboxes:triage-<run>`.
