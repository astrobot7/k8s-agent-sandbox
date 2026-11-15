# Plan: Coordinator for step order and per-step identity

**Status**: implemented (2026-11-15), including the remediation step (see remediation-operator.plan.md)
**Complexity**: High

## Summary

A third component, the coordinator, moves each `Incident` through its steps and gives every step its own ServiceAccount, Role, and RoleBinding, deleted when the step ends. It is the only component that creates RBAC objects. Run state moved from the triage controller's memory into custom resources.

## Decisions

| Topic | Decision |
|---|---|
| Process | Separate Deployment, same image recipe ([ADR 0001](../../docs/adr/0001-coordinator-separate-process.md)) |
| State | `Incident` (alert-triage) and `TriageRun` (agent-sandboxes) CRs, one writer per field ([ADR 0002](../../docs/adr/0002-custom-resources-as-run-state.md)) |
| Ordering | `TriageRun` created with `spec.suspend: true`; identity created; then resumed. The triage controller ignores suspended runs |
| Grantable ceiling | ClusterRole `coordinator-grantable`, bound only in agent-sandboxes; no `escalate`/`bind` |
| Guard | ValidatingAdmissionPolicy `coordinator-step-identities`, mirrored in Go by `Identity.Allowed` |
| Notifications | Stay in the triage controller |

## Progress

- [x] Phase 1: `api/v1alpha1` types, controller-gen deepcopy and CRDs (`go tool controller-gen`)
- [x] Phase 2: `internal/coordinator/identity.go` (`TriageIdentity`, `Allowed`), tests incl. remediation `RunRules`
- [x] Phase 3: `internal/coordinator/reconciler.go` with fake-client tests (ordering, crash mid-start, slots, terminal paths, deadline backstop, retention)
- [x] Phase 4: triage controller on CRs: webhook → Incident (dedup by fingerprint label), `RunReconciler`, `PodAuth` checks SA and run name, sandbox owned by its TriageRun
- [x] Phase 5: `deploy/coordinator/manifests.yaml`, `setup.sh coordinator` (RBAC, admission, run-scoped RBAC, token revocation probes), probe identity replaces the shared `triage-agent`
- [x] Phase 6: architecture, ADRs 0001/0002, README, setup.md, CLAUDE.md
- [x] Remediation as the coordinator's next step (remediation plan phases 1, 3, 4, 6)

## Validation

```sh
go test -race ./...
PROFILE=lean LLM_MODE=local ./setup.sh skills images llm-proxy triage coordinator
kubectl --context kind-agent-sandbox get incidents,triageruns -A
```
