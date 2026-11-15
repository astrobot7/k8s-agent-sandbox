# k8s-agent-sandbox

Kubernetes alert triage system. SigNoz runs in the kind cluster, collects logs, traces, and metrics (via the k8s-infra OTel collectors), and fires alerts to a triage controller webhook. The controller claims a [kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) sandbox running under gVisor (`runsc`). An AI agent inside it investigates through read-only MCP tools (the SigNoz MCP server for alerts, logs, and traces, and a read-only K8s MCP server for cluster state) and returns a triage report that the controller posts to on-call.

## Where things are

- `docs/`: design notes, setup guide, ADRs. Start at `docs/README.md`.
- `.claude/rules/`: Go and Kubernetes coding rules. They load automatically when you touch matching files.
- Go code follows `.claude/rules/go-project-layout.md`. Binaries in `cmd/`: `triage-controller` (webhook, TriageRuns and Remediations, agent API, remediation executor), `coordinator` (step order, per-step RBAC identities), `triage-agent` (runs in each sandbox; `AGENT_MODE=remediate` for remediation), `llm-proxy`, `slack-sink`. CRD types are in `api/v1alpha1`. Only `internal/remediation/actions` may hold workload write clients. Binaries are cobra commands built with `internal/cli`: add settings as flags (each falls back to its env var, e.g. `--run-timeout` ← `RUN_TIMEOUT`), keep credentials env-only.

## Commands

Fill these in as the tooling lands. Keep this list short and accurate.

```sh
./setup.sh         # idempotent setup, safe to re-run; ./setup.sh --list for steps
                   # cluster + gVisor, then SigNoz (ns signoz), viewer API key, SigNoz MCP (ns mcp-gateway),
                   # k8s-infra collectors (ns k8s-infra)
make kind-up       # 1 control plane + 2 workers, gVisor RuntimeClass "gvisor" (context kind-agent-sandbox)
make kind-verify   # smoke-test a pod under gVisor
make kind-down

go build ./...
go test -race ./...
go vet ./...
golangci-lint run ./...
go tool controller-gen object paths=./api/...                                      # after editing api/v1alpha1
go tool controller-gen crd paths=./api/... output:crd:artifacts:config=deploy/crds  # regenerates Incident/TriageRun CRDs
```

## Working agreements

- Read `docs/architecture.md` before changing how the sandbox, runtime class, or MCP wiring fit together.
- Record decisions that are hard to reverse as an ADR in `docs/adr/` (copy `0000-template.md`).
- Update the relevant doc in the same change as the code. Stale docs are worse than none.
- Never commit kubeconfigs, tokens, or `.env` files.

## Personal overrides

Machine-specific notes (local cluster names, paths, credentials hints) go in `CLAUDE.local.md`, which is gitignored.
