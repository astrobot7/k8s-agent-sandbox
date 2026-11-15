# Plan: Remediation for triaged alerts

**Status**: implemented (2026-11-15); see "Implemented" below

## Progress

- [x] Phase 1 policy core: `internal/remediation/remediation.go` (Skill, Proposal, Target, `Skill.Validate`, `Skill.Check`, `Skill.ApprovalFor`, protected namespaces) with tests
- [x] Phase 1 CRDs: `RemediationSkill` (cluster) and `Remediation` (namespaced) in `api/v1alpha1`, generated with controller-gen
- [x] Phase 2: per-step identity `remediate-<incident>` from `RunRules`, created by the coordinator (`coordinator.RemediationIdentity`); RBAC names targets as `<namespace>.<name>`
- [x] Phase 3: `AGENT_MODE=remediate` in `cmd/triage-agent`, `agent.Remediate` with `propose_action` / `no_action`; refused proposals go back to the model
- [x] Phase 4: executor in the triage controller (`internal/remediation/executor`): agent API `/v1/remediation/{assignment,decision}`, `Skill.Check` + SubjectAccessReview, approval via `spec.approval`, approval timeout, per-target cooldown, at-most-once execution, kill switch `REMEDIATION_MODE`, notifications; drivers in `internal/remediation/actions`
- [x] Phase 5: the coordinator's next step after a successful TriageRun (`startRemediation`): resolves the target workload, picks a skill, revokes the triage identity, provisions the remediation identity
- [x] Phase 6: starter skill `deploy/remediation/skills.yaml`, per-namespace RBAC `deploy/remediation/namespace.yaml`, `setup.sh remediation` probes, docs (architecture "Remediation", ADR 0003, README, setup.md)
- [ ] Later: `open-pr` driver (dry-run text only today); approval UI or chat button

## Implemented

Validated on kind (2026-11-15) with injected triage reports and the local Qwen model: a configuration cause led to `NoAction`; a transient cause led to an auto-approved `rollout-restart` that patched `workloads/payments-api`. Identities were revoked after each step.

**Complexity**: Large

## Summary

After triage produces a report, a remediation agent (same gVisor sandbox and workload identity) proposes one action from what its RemediationSkill allows. A trusted executor validates the proposal, checks RBAC for the run's own per-run ServiceAccount, records it as a `Remediation` custom resource, and executes it after approval. The agent never holds write credentials.

## Decisions

| Topic | Decision |
|---|---|
| Architecture | Modular monolith in code; the coordinator runs as its own process because it holds RBAC-write ([ADR 0001](../../docs/adr/0001-coordinator-separate-process.md)). Remediation is a step the coordinator starts, enabled by config (`REMEDIATION_ENABLED`). |
| Approval | Default per action type: `restart-pod` and `rollout-restart` auto; `scale` and `open-pr` require human approval. Each RemediationSkill can override (`approval: auto|required`). |
| PRs | Deferred. `open-pr` stays a configurable action type; implement a `dry-run` driver first, a GitHub driver later (repo, token type, and paths configurable). |
| Least privilege | Per remediation run: ServiceAccount + Role generated from the skill's allowed actions, `resourceNames` pinned to the alert's target. Executor checks SubjectAccessReview for that ServiceAccount before acting with its own namespace-scoped RBAC. `kube-system` and triage namespaces always denied in code. |

## Module layout (modular monolith)

```
cmd/triage-controller        one binary; wires modules from config
internal/triage              intake: webhook, runs, assignment, reports (exists)
internal/remediation         proposals, Remediation CRs, approval, executor loop
internal/remediation/actions typed drivers: restart-pod, rollout-restart, scale, dry-run (open-pr later)
internal/identity            shared: TokenReview / SubjectAccessReview (exists)
internal/skills              shared: skill model; add RemediationSkill (exists)
internal/agent               agent loop; add MODE=remediate + propose_action tool (exists)
```

Rules: modules talk through interfaces defined by the consumer; `internal/triage` must not import `internal/remediation` (it publishes "run finished" events through an interface); only `internal/remediation/actions` holds write clients.

## Patterns to mirror

| Category | Source |
|---|---|
| Sandbox launch | `internal/triage/sandbox.go` (`SandboxConfig.Object`) |
| Agent identity | `internal/triage/auth.go` (`PodAuth.RunID`), `internal/identity/identity.go` (`Verifier.Check`) |
| Skills as CRDs | `deploy/crds/triageskill.yaml`, `internal/triage/skillsource.go` (`CRDSkills`) |
| Local agent tools | `internal/agent/agent.go` (`SubmitReportTool`, `ReadSkillTool`) |
| Errors | `identity.ErrForbidden` / `ErrUnauthenticated`, `identity.HTTPStatus` |
| Tests | `internal/triage/server_test.go`, `internal/triage/auth_test.go` (fake clientset reactors) |
| Setup checks | `setup.sh` `probe_as` allow/deny probes |

## Phases

1. **CRDs and types**: `RemediationSkill` (cluster), `Remediation` (namespaced, with status phases Proposed/Approved/Executing/Succeeded/Failed/Rejected; approval via `spec.approved`, RBAC controls who may set it).
2. **Per-run identity**: controller creates `remediate-<run>` ServiceAccount, Role, RoleBinding (owner refs to the Sandbox); `PodAuth` checks the ServiceAccount belongs to the run.
3. **Agent mode**: `MODE=remediate`; assignment carries report, alert, matching RemediationSkills; tools = read-only MCP + `propose_action` + `submit_summary`; at most one proposal per run.
4. **Executor**: `POST /v1/remediations`; validate against skill constraints and target; SubjectAccessReview as the run's ServiceAccount; create `Remediation`; execute approved ones with typed drivers; kill switch, per-target cooldown, rate limit, idempotency, notifications.
5. **Wiring**: triage module publishes "report done"; remediation module subscribes. Executor RBAC: Role per allowlisted namespace only.
6. **Skills, setup, docs**: starter skills (`crashloop-restart` auto, `scale-up-on-pending` approval); `setup.sh remediation` step with probes (own target allowed; other pods, namespaces, kube-system denied); demo with dry-run; `docs/remediation.md`.

## Risks

| Risk | Mitigation |
|---|---|
| Action hits the wrong object | Target comes from alert labels, not the agent; Role pinned by resourceNames; executor re-checks |
| Prompt injection steers the proposal | Agent chooses only among skill-allowed actions on its own target; risky actions need approval |
| Restart loops worsen an outage | Per-target cooldown, actions-per-hour cap, kill switch |
| Write credentials in the shared binary | Only `internal/remediation/actions` holds write clients; namespace-scoped Roles; module disabled by default |
| Memory on the lean cluster | Reuse the agent image; one sandbox at a time locally |

## Acceptance

- [x] A run's ServiceAccount can act only on its own target (setup probes prove allow and deny)
- [x] Auto actions execute; approval-required actions wait for `spec.approval` (approval path unit-tested; not yet exercised on kind)
- [x] Remediation module can be disabled by config with no effect on triage (`REMEDIATION_ENABLED=false`)
- [x] `go test -race ./...` passes; kind run showed NoAction and an executed rollout-restart
