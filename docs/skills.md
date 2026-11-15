# Triage skills

A triage skill is a playbook that teaches the agent how to investigate one kind of problem. Each skill is a `TriageSkill` custom resource (`triage.k8s-agent-sandbox.io/v1alpha1`, cluster-scoped). Skills are data, so adding or changing one needs no code change, image build, or redeploy.

## How the agent uses skills

1. When a sandboxed agent fetches its assignment, the triage controller lists every `TriageSkill` and sends them along with the alert. The sandbox itself has no Kubernetes credentials.
2. The agent's system prompt gets an index of all skills (name and description). Skills whose `match` fits the alert's labels are included in full (at most 3).
3. When the evidence points to another skill's problem, the agent calls the `read_skill` tool to load its instructions.

A new or edited skill applies from the next triage run.

## Writing a skill

Add a file to `skills/`, named after the skill:

```yaml
# skills/dns-failures.yaml
apiVersion: triage.k8s-agent-sandbox.io/v1alpha1
kind: TriageSkill
metadata:
  name: dns-failures
spec:
  description: Pods cannot resolve service or external names (no such host, SERVFAIL, i/o timeout on port 53).
  match:                          # optional
    alertname: ["DNS errors"]
  instructions: |
    1. Find the failing lookups in the pod's logs with `pods_log`, and the names involved.
    2. Check CoreDNS: `pods_list_in_namespace` for kube-system pods with `k8s-app=kube-dns`, their restarts, and their logs.
    3. ...
```

| Field | Rules |
|---|---|
| `metadata.name` | 1 to 63 characters: `a-z`, `0-9`, inner hyphens. No dots. |
| `spec.description` | Required, at most 1024 characters. The agent picks skills by description, so name the symptoms and event reasons it covers. |
| `spec.match` | Optional. Map of alert label to a list of values; globs like `Back*` work. All listed labels must match. Matching skills go straight into the prompt. |
| `spec.instructions` | Required, at most 32 KiB of Markdown. Name the tools to call. |

Tools the agent has: `pods_get`, `pods_list_in_namespace`, `pods_log`, `events_list`, `resources_get`, `resources_list`, `namespaces_list` (Kubernetes, read-only, no Secrets or ConfigMaps), and the `signoz_*` tools (logs, traces, metrics, alerts). To refer to another skill, write ``read the `skill-name` skill``.

Then apply it:

```sh
kubectl --context kind-agent-sandbox apply -f skills/dns-failures.yaml
# or everything: ./setup.sh skills
go test ./internal/skills/   # checks every file in skills/, including references between skills
```

The API server rejects invalid skills (bad name, missing fields, oversized instructions). The controller also skips any skill that fails validation and logs why.

## Who may write skills

Skills are trusted instructions to the agent. Anyone who can create or update `triageskills` can change how it investigates, so grant those verbs only to the people who own triage. The controller only has `get`, `list`, and `watch`.

## Shipped skills

| Skill | Covers |
|---|---|
| `k8s-warning-events` | Entry point for the `K8s warning events` alert (matched automatically); routes to the skills below |
| `crashloopbackoff` | Containers that keep exiting and restarting |
| `oomkilled` | Memory-limit kills and node OOM |
| `image-pull` | ErrImagePull and ImagePullBackOff |
| `pending-pod` | FailedScheduling |
| `probe-failures` | Failing liveness and readiness probes |
| `volume-mount` | FailedMount and FailedAttachVolume |
| `service-errors-latency` | Error-rate and latency alerts on a service, using traces |
