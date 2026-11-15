#!/usr/bin/env bash
# Narrated demo of one incident going through the agents: alert -> Incident ->
# per-step identities -> triage report -> remediation agent (gVisor sandbox)
# -> executor checks -> rollout restart -> identities revoked.
#
# The triage report is injected: the demo is about remediation, and small
# local models often do not finish triage. Everything after that is live.
# Recorded with hack/demo/remediation-demo.tape (vhs). Needs
# ./setup.sh ... remediation and ./setup.sh demo.
set -euo pipefail

CTX="${KIND_CONTEXT:-kind-agent-sandbox}"
INC="demo$(date +%H%M%S)"
kc() { kubectl --context "$CTX" "$@"; }

bold=$'\033[1m' dim=$'\033[2m' cyan=$'\033[36m' yellow=$'\033[33m' reset=$'\033[0m'
say() { printf '\n%s▶ %s%s\n' "$bold$cyan" "$*" "$reset"; }
note() { printf '%s  %s%s\n' "$dim" "$*" "$reset"; }
# show prints a command, then runs it. Some commands are expected to fail
# (showing that an identity is gone), so a failure does not stop the demo.
show() { local c="$*"; printf '%s$ %s%s\n' "$yellow" "${c//kc /kubectl }" "$reset"; eval "$c" || true; }
pause() { sleep "${1:-2}"; }
wait_for() { # wait_for <seconds> <description> <command...>
  local n=$1 what=$2 i
  shift 2
  for ((i = 0; i < n; i++)); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  printf 'timed out waiting for %s\n' "$what" >&2
  exit 1
}
phase_is() { # phase_is <ns> <kind> <name> <phase...>
  local p
  p=$(kc -n "$1" get "$2" "$3" -o jsonpath='{.status.phase}' 2>/dev/null) || return 1
  shift 3
  local want
  for want in "$@"; do [[ "$p" == "$want" ]] && return 0; done
  return 1
}

printf '%s\n' "${bold}k8s-agent-sandbox: an alert, two sandboxed agents, one fix${reset}"
note "triage controller · coordinator · gVisor sandboxes · per-step RBAC identities"
pause 3

say "A workload is failing"
show "kc -n workloads get pods -l app.kubernetes.io/name=payments-api"
pause 3

say "1. SigNoz alert -> Incident"
note "The webhook becomes an Incident custom resource. For the demo, the triage report"
note "is injected in step 3: the local model is too small to finish triage reliably."
# Hold the triage sandbox back: its report is injected instead. Always bring
# the controller back, even if the demo stops early.
trap 'kc -n alert-triage scale deploy/triage-controller --replicas=1 >/dev/null' EXIT
kc -n alert-triage scale deploy/triage-controller --replicas=0 >/dev/null
kc -n alert-triage rollout status deploy/triage-controller --timeout=60s >/dev/null
pod=$(kc -n workloads get pods -l app.kubernetes.io/name=payments-api -o jsonpath='{.items[0].metadata.name}')
kc create -f - >/dev/null <<EOF
apiVersion: triage.k8s-agent-sandbox.io/v1alpha1
kind: Incident
metadata:
  name: ${INC}
  namespace: alert-triage
  labels: {triage.k8s-agent-sandbox.io/fingerprint: "$(printf '%s' "$INC" | shasum | cut -c1-16)"}
spec:
  alert:
    fingerprint: ${INC}
    labels: {alertname: "K8s warning events", k8s.namespace.name: workloads, k8s.object.kind: Pod, k8s.object.name: "${pod}"}
    annotations: {description: "BackOff: Back-off restarting failed container api"}
EOF
show "kc -n alert-triage get incident ${INC}"
pause 2

say "2. Coordinator: a ServiceAccount + Role just for this triage step"
wait_for 300 "triage to start" phase_is alert-triage incident "$INC" Triaging
show "kc -n agent-sandboxes get triagerun ${INC}"
show "kc -n agent-sandboxes get role triage-${INC} -o jsonpath='{range .rules[*]}{.resources} {.verbs} on {.resourceNames}{\"\\n\"}{end}'"
note "Pinned to this run and one model; deleted when the step ends."
pause 4

say "3. Triage report (injected)"
now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
kc -n agent-sandboxes patch triagerun "$INC" --subresource=status --type=merge -p "$(cat <<EOF
{"status":{"phase":"Succeeded","finishedAt":"${now}","report":{
 "summary":"payments-api lost its database connections in a database failover and never reconnected; the database is healthy again.",
 "likely_cause":"The process keeps a broken connection pool after the failover; new connections to the database succeed now.",
 "confidence":"high",
 "evidence":[{"source":"pods_log","detail":"pool: all connections closed by peer; giving up after 5 retries"},
             {"source":"signoz_search_logs","detail":"database failover completed; other clients reconnected"}],
 "next_steps":["Restart payments-api so it opens a fresh pool; add reconnect logic to the pool."]}}}
EOF
)" >/dev/null
show "kc -n agent-sandboxes get triagerun ${INC} -o jsonpath='{.status.report.likely_cause}{\"\\n\"}'"
pause 3

say "4. Coordinator: triage identity revoked, remediation identity pinned to the workload"
wait_for 30 "remediation to start" phase_is alert-triage incident "$INC" Remediating
show "kc -n agent-sandboxes get serviceaccount triage-${INC} 2>&1 | tail -1"
show "kc -n agent-sandboxes get remediation ${INC} -o jsonpath='skill={.spec.skillName}  target={.spec.target.kind} {.spec.target.namespace}/{.spec.target.name}{\"\\n\"}'"
show "kc -n agent-sandboxes get role remediate-${INC} -o jsonpath='{range .rules[*]}{.resources} {.verbs} on {.resourceNames}{\"\\n\"}{end}'"
note "The alert named a Pod; the coordinator resolved it to its Deployment."
pause 4

say "5. Remediation agent in a gVisor sandbox (read-only tools, no write credentials)"
kc -n alert-triage scale deploy/triage-controller --replicas=1 >/dev/null
sel="triage.agent-sandbox/run=${INC},triage.agent-sandbox/step=remediate"
has_pod() { kc -n agent-sandboxes get pods -l "$sel" -o name | grep -q .; }
has_logs() { kc -n agent-sandboxes logs -l "$sel" 2>/dev/null | grep -q .; }
wait_for 120 "the sandbox pod" has_pod
show "kc -n agent-sandboxes get pods -l ${sel} -o custom-columns=POD:.metadata.name,RUNTIME:.spec.runtimeClassName,SERVICEACCOUNT:.spec.serviceAccountName"
wait_for 120 "the agent to start" has_logs
note "agent log:"
kc -n agent-sandboxes logs -f -l "$sel" 2>/dev/null |
  jq -r --unbuffered 'select(.msg) |
    if .msg == "model reply" then "  turn \(.turn): \((.calls // []) | join(", "))\(if (.text // "") != "" then "  \"" + (.text | .[0:90]) + "\"" else "" end)"
    elif .msg == "decision accepted" then "  decision accepted: \(.action // "no action")"
    elif .msg == "decision refused" then "  refused: \(.error)"
    elif (.msg | test("assignment|tools ready")) then "  \(.msg)"
    else empty end' || true

say "6. Executor: skill check, RBAC check as the step's identity, auto-approved restart"
wait_for 60 "the remediation to finish" phase_is agent-sandboxes remediation "$INC" Succeeded NoAction Rejected Failed
show "kc -n agent-sandboxes get remediation ${INC}"
show "kc -n agent-sandboxes get remediation ${INC} -o jsonpath='why: {.status.proposal.rationale}{\"\\n\"}result: {.status.result}{\"\\n\"}'"
show "kc -n workloads get deploy payments-api -o jsonpath='{.spec.template.metadata.annotations}{\"\\n\"}'"
pause 3

say "7. Done: identity revoked, outcome posted to on-call"
wait_for 30 "the incident to finish" phase_is alert-triage incident "$INC" Done Failed
show "kc -n agent-sandboxes get serviceaccount remediate-${INC} 2>&1 | tail -1"
show "kc -n alert-triage get incident ${INC}"
pause 5
note "end of demo"
