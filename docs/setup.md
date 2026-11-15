# Setup

Status: the SigNoz, collector, and triage steps are implemented in `setup.sh`; sections 4 and 7 still describe manual checks.

## Prerequisites

- Go (version from `go.mod`)
- Docker (or another runtime kind supports) with at least 6 CPUs and 12 GB of memory for the VM
- `kind`, `kubectl`, `helm`
- `golangci-lint` for linting

## 1. Cluster and gVisor

```sh
./setup.sh                 # all steps: preflight gvisor cluster nodes runtimeclass verify
./setup.sh cluster verify  # only the named steps
./setup.sh --recreate      # delete the kind cluster first (use when the cluster is broken or the config changed)
```

Each step checks the current state and skips work already done, so after a failure, fix the cause and re-run `./setup.sh`. Network-bound steps retry with backoff (`RETRIES`, `RETRY_DELAY`), and a failed `kind create` removes its half-made node containers before the next attempt. `make kind-up` and `make kind-verify` call the same script.

What this sets up:

- **Cluster** `agent-sandbox` (context `kind-agent-sandbox`), defined in `deploy/kind/cluster.yaml`: one control plane and two workers on `kindest/node:v1.37.0` (pinned by digest). The control plane keeps its `NoSchedule` taint, so workloads run on the two workers.
- **gVisor** release `20261109.0`, set in `hack/install-gvisor.sh` (override with `GVISOR_RELEASE`). The script downloads the release tarball for the Docker VM's architecture, checks its SHA-512, and extracts `runsc`, `containerd-shim-runsc-v1`, and `gvisor-bin/` into `.cache/gvisor/` (gitignored). The kind config mounts them read-only into every node at `/usr/local/bin/` and registers the `runsc` handler with containerd.
- **RuntimeClass** `gvisor` with handler `runsc` (`deploy/kind/runtimeclass.yaml`).

`make kind-verify` passes when the pod's log starts with `Starting gVisor...` instead of the host kernel log.

`runsc` needs its `gvisor-bin/` sidecars (including `gvisor_sentry`) next to it. If they're missing, it tries to download them at sandbox start, which fails on the read-only mount.

Tested on OrbStack (arm64, Linux 7.2) with kind v0.33.0.

## 2. SigNoz and the SigNoz MCP server

```sh
./setup.sh signoz signoz-apikey signoz-mcp   # also part of a plain ./setup.sh
```

- **`signoz`**: installs SigNoz with [Foundry](https://github.com/SigNoz/foundry) from `deploy/signoz/casting.yaml`: installation `main` in namespace `signoz`, `signoz/signoz:v0.150.0`, collector `signoz-otel-collector:v0.144.15`, **ClickHouse Keeper** (no ZooKeeper, no JVM), and SQLite as the metadata store. `hack/install-foundry.sh` downloads `foundryctl` v0.5.0 (override with `FOUNDRY_VERSION`) into `.cache/foundry/` and checks its SHA-256. The step forges plain Kubernetes manifests (`flavor: kustomize`; Foundry's Helm flavor supports only ZooKeeper), applies the ClickHouse operator and its CRDs first, then the rest, and waits for the schema migration, `main-signoz`, and `main-ingester`. Resources are named after the installation: `main-signoz` (UI and API, 8080), `main-ingester` (OTLP, 4317 and 4318), `main-clickhouse`, `keeper-main-clickhouse-keeper`.
- **`signoz-apikey`**: through a temporary port-forward, it registers the first admin (`admin@agent-sandbox.local`, random password), creates the SigNoz service account `triage-mcp` with only the `signoz-viewer` role, creates a non-expiring API key, and stores it in Secret `mcp-gateway/signoz-api-key`. On re-runs it keeps the stored key as long as SigNoz still accepts it, and creates a new one (restarting the MCP server) if not.
- **`signoz-mcp`**: deploys `signoz/signoz-mcp-server:v0.19.0` (pinned by digest) from `deploy/signoz-mcp/manifests.yaml` in HTTP mode, then checks it end to end: MCP `initialize`, `tools/list`, and a real `signoz_list_alerts` call through the API key.

In-cluster endpoint for the agent: `http://signoz-mcp.mcp-gateway.svc.cluster.local:8000/mcp`.

Open the UI:

```sh
kubectl --context kind-agent-sandbox -n signoz port-forward svc/main-signoz 8080:8080
kubectl --context kind-agent-sandbox -n signoz get secret signoz-admin -o jsonpath='{.data.password}' | base64 -d
```

The MCP server's docs tools serve their embedded index only (scheduled refreshes from signoz.io are off), and it exports its own traces and metrics to the SigNoz collector as `service.name=signoz-mcp`, `service.namespace=alert-triage`.

## 3. Cluster telemetry (k8s-infra)

```sh
./setup.sh collectors   # also part of a plain ./setup.sh
```

Installs chart `signoz/k8s-infra` `0.17.1` (override with `K8S_INFRA_CHART_VERSION`) into namespace `k8s-infra` with `deploy/k8s-infra/values.yaml`:

- `k8s-infra-otel-agent` DaemonSet (one per node, including the control plane): container logs, kubelet and host metrics, Kubernetes attribute enrichment, and an OTLP receiver on hostPort 4317/4318 for app traces.
- `k8s-infra-otel-deployment`: cluster metrics and Kubernetes events as logs.
- Both export over OTLP/HTTP to `http://main-ingester.signoz.svc.cluster.local:4318`. The endpoint needs the `http://` scheme for the chart's default `otlphttp` exporter.
- Every record carries `k8s.cluster.name=kind-agent-sandbox` and `deployment.environment=dev`.

The step passes only when SigNoz returns a non-zero count of logs with `k8s.pod.name` for this cluster from the last 10 minutes, queried with the viewer API key. Log collection starts at the end of each file, so only lines written after install are collected.

Check in the UI: **Logs** shows container logs with `k8s.namespace.name` and `k8s.pod.name`, and **Infrastructure** shows the kind nodes and pods.

## 4. Traces from workloads

Point instrumented workloads at the node-local agent:

```yaml
env:
  - name: HOST_IP
    valueFrom:
      fieldRef:
        fieldPath: status.hostIP
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: http://$(HOST_IP):4317
  - name: OTEL_SERVICE_NAME
    value: checkout-api
  - name: OTEL_RESOURCE_ATTRIBUTES
    value: service.namespace=workloads,deployment.environment=dev
```

Deploy the demo workload into `workloads` and generate traffic. Check in SigNoz: **Services** lists the service and **Traces** shows its spans.

## 5. Alerts

```sh
./setup.sh signoz-alerts   # also part of a plain ./setup.sh
```

Creates the webhook channel `triage-webhook` (`http://triage-controller.alert-triage.svc.cluster.local:8080/webhook/signoz`) and upserts the rule `K8s warning events`: Kubernetes warning events (`BackOff`, `Failed`, `OOMKilling`, and similar) grouped by namespace and object. The filter requires `attribute.k8s.namespace.name EXISTS` and excludes the system's own namespaces, so a failing triage run cannot trigger another one. The step reads the rule back to verify it.

Rules on logs, traces, and metrics per service are not created yet. Add them in the UI, group them by `service.name` and `k8s.namespace.name`, and route them to `triage-webhook`.

## 6. Triage components

```sh
./setup.sh agent-sandbox k8s-mcp skills images egress llm-secret llm-proxy triage coordinator remediation slack-sink
```

These steps install the agent-sandbox controller, the read-only Kubernetes MCP server, the CRDs (`TriageSkill`, `Incident`, `TriageRun`, `Remediation`, `RemediationSkill`) and skills, the images, the egress gateway, the llm-proxy, the triage controller, the coordinator, remediation, and slack-sink.

The `coordinator` step checks, as the coordinator's ServiceAccount:

- RBAC lets it create ServiceAccounts, Roles, and RoleBindings in `agent-sandboxes` only. It cannot update, list, escalate, or bind them, and cannot touch pods, secrets, or tokens.
- The admission policy accepts a step-shaped Role and rejects one without `resourceNames`, one without a `triage-`/`remediate-` name, and one on core resources.
- A run identity can get its own `TriageRun` but not another run's.
- Deleting a ServiceAccount makes its token fail TokenReview within seconds. Each step probes the NetworkPolicies it depends on from short-lived pods, checking both allowed and blocked paths. With `LLM_MODE=local`, `llm-secret` is skipped and the proxy talks to the LLM server on this machine (see the README).

The `remediation` step opens each namespace in `REMEDIATION_NAMESPACES` (default `workloads`) to remediation, installs the starter `RemediationSkill`, sets `REMEDIATION_MODE` (default `live`; `dry-run` or `off`), and checks:

- The triage controller (the executor) can delete pods, patch workloads, and update scale in opened namespaces only. It cannot create pods, read Secrets, delete Deployments, or write in `default`, `kube-system`, or `agent-sandboxes`.
- The coordinator can get pods and workloads in opened namespaces only, and cannot list them.
- The admission policy accepts a remediation-shaped Role.
- A remediation identity's RBAC (checked with SubjectAccessReview, since `kubectl auth can-i` cannot see these resources) allows its action on its own target only: not another object, not the same name in another namespace, not another action, and not the Kubernetes API.

## 7. End-to-end check

1. `./setup.sh demo` deploys `workloads/payments-api`, which crashloops because its database host does not resolve.
2. Confirm the alert fires in SigNoz and the controller logs the webhook.
3. Confirm a sandbox is created, the report reaches slack-sink (or Slack), and the sandbox is deleted.
4. Check `kubectl get incidents,triageruns,remediations -A`. For the demo the remediation agent should decline (`NoAction`): the cause is configuration, and a restart would crash the same way.
5. In SigNoz, find the triage run's trace under `service.namespace=alert-triage`.

## Teardown

```sh
make kind-down
```
