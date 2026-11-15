# 0003: The remediation executor runs in the triage controller

- Status: accepted
- Date: 2026-11-15

## Context

Remediation needs three things the triage step does not: a sandboxed agent that proposes an action, a component that checks the proposal and waits for approval, and something with write access to workloads that performs it. The remediation plan chose a modular monolith: triage and remediation as modules of one controller binary. [ADR 0001](0001-coordinator-separate-process.md) already moved RBAC-write out of that binary into the coordinator.

The step identity's Role lives in `agent-sandboxes`, but the target lives in a workload namespace. A Role pinned by `resourceNames: [payments-api]` would match `payments-api` in every namespace.

## Decision

- The executor is a module of the triage controller (`internal/remediation/executor`), enabled by `REMEDIATION_ENABLED`. It runs `Remediation` resources the way `RunReconciler` runs `TriageRun`s, using the same sandbox launcher and agent authentication (`internal/sandbox`).
- Only `internal/remediation/actions` holds the workload write client. Its RBAC is a Role per namespace opened to remediation (`deploy/remediation/namespace.yaml`): delete pods, patch workloads, update scale. No cluster-wide write.
- The coordinator chooses the skill and resolves the target before the step starts, so the step identity can be pinned to that target. It reads pods and workloads only in opened namespaces.
- Remediation RBAC names a target as `<namespace>.<name>` in `resourceNames`. Namespaces cannot contain dots, so the name is unambiguous, and the target's namespace is part of what the step may touch.
- `REMEDIATION_MODE` (`live`, `dry-run`, `off`) is the kill switch.

## Alternatives considered

- **A separate executor Deployment.** Keeps workload write out of the process that faces webhooks and agents. It costs another pod and another agent API endpoint, and the triage controller already holds the agent API and sandbox lifecycle remediation needs. The write Role is namespace-scoped and its verbs match the drivers exactly, so a compromise of the controller reaches only opened namespaces. Revisit if remediation is opened to many namespaces or gets more powerful actions.
- **The executor in the coordinator.** It already holds RBAC-write; adding workload write would make it the one component whose compromise does the most damage. Rejected.
- **Target name only in `resourceNames`.** Matches the same name in every namespace. Rejected.

## Consequences

- Opening a namespace to remediation is a deliberate step: add it to `REMEDIATION_NAMESPACES` and run `./setup.sh remediation`.
- `kubectl auth can-i` cannot check remediation verbs (the resources exist only as RBAC names, not in discovery). Use a SubjectAccessReview; `setup.sh` has `sar_allowed`.
- Setting `REMEDIATION_ENABLED=false` on both deployments returns to triage only.
