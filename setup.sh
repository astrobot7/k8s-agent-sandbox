#!/usr/bin/env bash
# Sets up the local alert-triage environment. Every step checks the current
# state first and skips work that is already done, so the script can be re-run
# after a failure and it picks up where it stopped.
#
# Usage:
#   ./setup.sh                 run all steps
#   ./setup.sh cluster verify  run only the named steps, in the order given
#   ./setup.sh --list          list steps
#   ./setup.sh --recreate      delete the kind cluster first, then run the steps
#
# Env overrides: KIND_CLUSTER, GVISOR_RELEASE, FOUNDRY_VERSION, SIGNOZ_ADMIN_EMAIL,
# K8S_INFRA_CHART_VERSION, PROFILE (full|lean), LLM_MODE (anthropic|local), RETRIES, RETRY_DELAY.
set -euo pipefail

cd "$(dirname "$0")"

KIND_CLUSTER="${KIND_CLUSTER:-agent-sandbox}"
KIND_CONTEXT="kind-${KIND_CLUSTER}"
# PROFILE=full (default): 3 nodes, generous limits. PROFILE=lean: 1 node and
# tight limits, to fit on a laptop next to a local LLM (about 8 GB for Docker).
PROFILE="${PROFILE:-full}"
case "$PROFILE" in
  full)
    KIND_CONFIG="deploy/kind/cluster.yaml" EXPECTED_NODES=3 MIN_DOCKER_MEM_GIB=14
    K8S_INFRA_VALUES=(-f deploy/k8s-infra/values.yaml)
    ;;
  lean)
    KIND_CONFIG="deploy/kind/cluster-lean.yaml" EXPECTED_NODES=1 MIN_DOCKER_MEM_GIB=7
    K8S_INFRA_VALUES=(-f deploy/k8s-infra/values.yaml -f deploy/k8s-infra/values-lean.yaml)
    ;;
  *) echo "PROFILE must be full or lean" >&2; exit 1 ;;
esac
RETRIES="${RETRIES:-3}"
RETRY_DELAY="${RETRY_DELAY:-5}"

FOUNDRY_VERSION="${FOUNDRY_VERSION:-0.5.0}"  # foundryctl, installs SigNoz from deploy/signoz/casting.yaml
SIGNOZ_NS=signoz
SIGNOZ_RELEASE=main                          # metadata.name in casting.yaml; prefixes every resource
SIGNOZ_SVC="${SIGNOZ_RELEASE}-signoz"         # UI and API, port 8080
SIGNOZ_INGESTER="${SIGNOZ_RELEASE}-ingester"  # OTLP, ports 4317 and 4318
SIGNOZ_ADMIN_EMAIL="${SIGNOZ_ADMIN_EMAIL:-admin@agent-sandbox.local}"
SIGNOZ_SA_NAME=triage-mcp # SigNoz service account behind the MCP server's API key
MCP_NS=mcp-gateway
K8S_INFRA_CHART_VERSION="${K8S_INFRA_CHART_VERSION:-0.17.1}"
K8S_INFRA_NS=k8s-infra
AGENT_SANDBOX_VERSION="${AGENT_SANDBOX_VERSION:-v1.0.11}"
TRIAGE_NS=alert-triage
SANDBOX_NS=agent-sandboxes
LLM_NS=llm-proxy
LLM_SECRET=anthropic-api-key
EGRESS_NS=egress
CURL_IMAGE=curlimages/curl:8.22.0
LLM_MODEL=claude-sonnet-5-5
TOKEN_AUDIENCE=triage.k8s-agent-sandbox.io # audience of agent identity tokens
COORDINATOR_USER="system:serviceaccount:${TRIAGE_NS}:coordinator"
PROBE_SA=triage-setup-probe # setup's stand-in for a run identity (see probe_identity)
# Namespaces the remediation executor may act in (space-separated), and its kill
# switch: live performs actions, dry-run records them, off rejects them.
REMEDIATION_NAMESPACES="${REMEDIATION_NAMESPACES:-workloads}"
REMEDIATION_MODE="${REMEDIATION_MODE:-live}"
# LLM_MODE=anthropic (default) uses the Anthropic API key Secret. LLM_MODE=local
# uses an OpenAI-compatible server on this machine (e.g. mlx_lm.server) on LOCAL_LLM_PORT.
LLM_MODE="${LLM_MODE:-anthropic}"
LOCAL_LLM_PORT="${LOCAL_LLM_PORT:-8000}"
LOCAL_LLM_ALIAS="${LOCAL_LLM_ALIAS:-local-qwen}" # model name agents use and RBAC grants
# Small local models get only the tools the skills use (12 of ~54), keeping prompts short.
LOCAL_AGENT_TOOLS="${LOCAL_AGENT_TOOLS:-pods_get,pods_list_in_namespace,pods_log,events_list,resources_get,resources_list,signoz_search_logs,signoz_search_traces,signoz_get_trace_details,signoz_query_metrics,signoz_get_alert_history,signoz_list_alerts}"

CALICO_VERSION="${CALICO_VERSION:-v3.33.0}"

STEPS=(preflight gvisor cluster cni nodes runtimeclass verify signoz signoz-apikey signoz-mcp collectors
  agent-sandbox k8s-mcp skills images egress llm-secret llm-proxy triage coordinator remediation slack-sink signoz-alerts)
OPTIONAL_STEPS=(demo) # run only when named: ./setup.sh demo

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok() { printf '\033[1;32m  ✓\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m  !\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31m  ✗\033[0m %s\n' "$*" >&2; exit 1; }

kc() { kubectl --context "$KIND_CONTEXT" "$@"; }

# retry <cmd...>: run a command up to RETRIES times with linear backoff.
retry() {
  local n=1
  until "$@"; do
    if ((n >= RETRIES)); then
      warn "failed after ${n} attempts: $*"
      return 1
    fi
    warn "attempt ${n}/${RETRIES} failed, retrying in $((RETRY_DELAY * n))s: $*"
    sleep $((RETRY_DELAY * n))
    n=$((n + 1))
  done
}

cluster_exists() { kind get clusters 2>/dev/null | grep -qx "$KIND_CLUSTER"; }

# port_forward <ns> <svc> <port>: start a background port-forward on a free local
# port and set PF_URL. Stopped automatically when the script exits.
PF_PIDS=()
stop_port_forwards() { ((${#PF_PIDS[@]} == 0)) || kill "${PF_PIDS[@]}" 2>/dev/null || true; }
trap stop_port_forwards EXIT
port_forward() {
  local ns=$1 svc=$2 port=$3 out local_port i
  out=$(mktemp)
  kc -n "$ns" port-forward "svc/${svc}" ":${port}" >"$out" 2>&1 &
  PF_PIDS+=($!)
  for ((i = 0; i < 30; i++)); do
    local_port=$(sed -nE 's/^Forwarding from 127\.0\.0\.1:([0-9]+) .*/\1/p' "$out" | head -1)
    [[ -n "$local_port" ]] && break
    sleep 0.5
  done
  rm -f "$out"
  [[ -n "$local_port" ]] || return 1
  PF_URL="http://127.0.0.1:${local_port}"
}

secret_value() { kc -n "$1" get secret "$2" -o "jsonpath={.data.$3}" 2>/dev/null | base64 -d; }

# apply_secret <ns> <name> <key=value>...: create or update a generic Secret.
apply_secret() {
  local ns=$1 name=$2
  shift 2
  local args=()
  for kv in "$@"; do args+=(--from-literal="$kv"); done
  kc -n "$ns" create secret generic "$name" "${args[@]}" --dry-run=client -o yaml | kc apply -f - >/dev/null
}

# --- steps -------------------------------------------------------------------

step_preflight() {
  log "preflight: tools and Docker"
  local missing=()
  for t in docker kind kubectl helm curl jq openssl shasum tar; do
    command -v "$t" >/dev/null || missing+=("$t")
  done
  ((${#missing[@]} == 0)) || die "missing tools: ${missing[*]}"

  if ! docker info >/dev/null 2>&1; then
    if command -v orb >/dev/null; then
      warn "Docker is not reachable; starting OrbStack"
      orb start
    fi
    retry docker info >/dev/null 2>&1 || die "Docker is not reachable; start your Docker runtime and re-run"
  fi

  local mem_gib
  mem_gib=$(($(docker info --format '{{.MemTotal}}') / 1024 / 1024 / 1024))
  if ((mem_gib < MIN_DOCKER_MEM_GIB)); then
    warn "Docker VM has ${mem_gib} GiB memory; SigNoz needs about ${MIN_DOCKER_MEM_GIB} GiB in total"
  fi
  ok "Docker $(docker info --format '{{.Architecture}}'), ${mem_gib} GiB"
}

step_gvisor() {
  log "gvisor: download pinned release"
  retry ./hack/install-gvisor.sh
  for f in runsc containerd-shim-runsc-v1 gvisor-bin/gvisor_sentry; do
    [[ -x ".cache/gvisor/${f}" ]] || die ".cache/gvisor/${f} missing; delete .cache/gvisor and re-run"
  done
  ok "binaries in .cache/gvisor"
}

create_cluster() {
  # No --wait: nodes stay NotReady until the cni step installs Calico.
  kind create cluster --config "$KIND_CONFIG" && return 0
  # A failed create can leave half-made node containers; remove them before the next attempt.
  kind delete cluster --name "$KIND_CLUSTER" >/dev/null 2>&1 || true
  return 1
}

step_cluster() {
  log "cluster: kind cluster ${KIND_CLUSTER}"
  if cluster_exists; then
    kc get --raw /readyz >/dev/null 2>&1 ||
      die "cluster ${KIND_CLUSTER} exists but its API server is not responding; re-run with --recreate"
    ok "already exists"
    return
  fi
  retry create_cluster
  ok "created"
}

step_cni() {
  log "cni: Calico ${CALICO_VERSION} (enforces ingress and egress NetworkPolicies)"
  local base="https://raw.githubusercontent.com/projectcalico/calico/${CALICO_VERSION}/manifests"
  retry kc apply --server-side --force-conflicts -f "${base}/operator-crds.yaml" >/dev/null
  retry kc apply --server-side --force-conflicts -f "${base}/tigera-operator.yaml" >/dev/null
  retry kc -n tigera-operator rollout status deployment/tigera-operator --timeout=180s >/dev/null
  retry kc apply -f deploy/calico/installation.yaml >/dev/null
  # The operator creates tigerastatus/calico shortly after it sees the Installation.
  RETRIES=30 RETRY_DELAY=5 retry kc get tigerastatus calico >/dev/null 2>&1 || die "Calico status never appeared"
  retry kc wait --for=condition=Available tigerastatus/calico --timeout=300s >/dev/null
  retry kc wait --for=condition=Ready nodes --all --timeout=180s >/dev/null
  if ((EXPECTED_NODES == 1)); then # lean: the probe below must schedule on the control plane
    kc taint nodes --all node-role.kubernetes.io/control-plane:NoSchedule- >/dev/null 2>&1 || true
  fi

  # Fail fast if egress policies are not enforced: everything else depends on it.
  local ns=cni-egress-check out
  kc create namespace "$ns" --dry-run=client -o yaml | kc apply -f - >/dev/null
  kc apply -f - >/dev/null <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: deny-egress-except-dns
  namespace: ${ns}
spec:
  podSelector: {}
  policyTypes: [Egress]
  egress:
    - ports:
        - { protocol: UDP, port: 53 }
        - { protocol: TCP, port: 53 }
EOF
  out=$(probe "$ns" app=check - https://example.com/)
  kc delete namespace "$ns" --wait=false >/dev/null
  expect_blocked "egress from a pod whose policy allows only DNS" "$out"
  ok "Calico ready; egress NetworkPolicies are enforced"
}

step_nodes() {
  log "nodes: wait for ${EXPECTED_NODES} Ready nodes"
  local count node
  count=$(kc get nodes --no-headers | wc -l | tr -d ' ')
  ((count == EXPECTED_NODES)) ||
    die "expected ${EXPECTED_NODES} nodes, found ${count}; did ${KIND_CONFIG} change? re-run with --recreate"
  retry kc wait --for=condition=Ready nodes --all --timeout=120s >/dev/null
  if ((EXPECTED_NODES == 1)); then
    # Single-node (lean) cluster: the control plane must run workloads too.
    kc taint nodes --all node-role.kubernetes.io/control-plane:NoSchedule- >/dev/null 2>&1 || true
  fi

  for node in $(kc get nodes -o jsonpath='{.items[*].metadata.name}'); do
    docker exec "$node" test -x /usr/local/bin/runsc ||
      die "${node} has no runsc; the gVisor mount is missing, re-run with --recreate"
  done
  ok "all nodes Ready, runsc present on every node"
}

step_runtimeclass() {
  log "runtimeclass: gvisor"
  retry kc apply -f deploy/kind/runtimeclass.yaml >/dev/null
  ok "gvisor -> runsc"
}

SMOKE_POD=gvisor-smoke-test
SMOKE_MANIFEST=deploy/kind/smoke-test.yaml

run_smoke() {
  kc delete -f "$SMOKE_MANIFEST" --ignore-not-found >/dev/null
  kc apply -f "$SMOKE_MANIFEST" >/dev/null
  kc wait --for=jsonpath='{.status.phase}'=Succeeded "pod/${SMOKE_POD}" --timeout=120s >/dev/null
}

gvisor_boot_logged() { kc logs "$SMOKE_POD" 2>/dev/null | grep -q 'Starting gVisor'; }

cleanup_smoke() { kc delete -f "$SMOKE_MANIFEST" --ignore-not-found --wait=false >/dev/null 2>&1 || true; }

step_verify() {
  log "verify: run a pod under gVisor"
  trap cleanup_smoke EXIT
  if ! retry run_smoke; then
    kc describe "pod/${SMOKE_POD}" 2>/dev/null | tail -20 >&2 || true
    die "smoke test pod did not succeed"
  fi
  # Logs can lag a moment behind the Succeeded phase on a fresh cluster.
  RETRIES=5 RETRY_DELAY=2 retry gvisor_boot_logged ||
    die "pod ran, but not under gVisor (no gVisor boot log)"
  cleanup_smoke
  trap stop_port_forwards EXIT
  ok "pod ran under gVisor"
}

step_signoz() {
  log "signoz: Foundry ${FOUNDRY_VERSION}, installation '${SIGNOZ_RELEASE}' (ClickHouse Keeper) in namespace ${SIGNOZ_NS}"
  FOUNDRY_VERSION="$FOUNDRY_VERSION" retry ./hack/install-foundry.sh >/dev/null
  local work=.cache/foundry/work pours
  mkdir -p "$work"
  cp deploy/signoz/casting.yaml "$work/casting.yaml"
  # Foundry writes Kubernetes manifests (flavor: kustomize) under pours/.
  (cd "$work" && ../bin/foundryctl forge -f casting.yaml >forge.log 2>&1) ||
    die "foundryctl forge failed; see ${work}/forge.log (re-run with --debug for details)"
  pours="${work}/pours/deployment"

  # 1. The ClickHouse operator and its CRDs (pinned URLs in the generated kustomization).
  retry kc apply --server-side --force-conflicts -k "${pours}/operators/clickhouse-operator" >/dev/null
  kc wait --for=condition=Established --timeout=120s \
    crd/clickhouseinstallations.clickhouse.altinity.com \
    crd/clickhousekeeperinstallations.clickhouse-keeper.altinity.com >/dev/null
  retry kc -n "$SIGNOZ_NS" rollout status "deployment/${SIGNOZ_RELEASE}-telemetrystore-clickhouse-operator" --timeout=300s >/dev/null

  # 2. ClickHouse Keeper, ClickHouse, the schema migrator, SigNoz, and the ingester.
  retry kc apply --server-side --force-conflicts -k "$pours" >/dev/null
  # The migrator finishes only once ClickHouse and Keeper are up, so it gates the rest.
  kc -n "$SIGNOZ_NS" wait --for=condition=complete "job/${SIGNOZ_RELEASE}-telemetrystore-migrator" --timeout=15m >/dev/null ||
    die "schema migration did not complete; check: kubectl --context ${KIND_CONTEXT} -n ${SIGNOZ_NS} get pods"
  retry kc -n "$SIGNOZ_NS" rollout status "statefulset/${SIGNOZ_SVC}" --timeout=300s >/dev/null
  retry kc -n "$SIGNOZ_NS" rollout status "deployment/${SIGNOZ_INGESTER}" --timeout=300s >/dev/null
  ok "SigNoz ready (UI: kubectl --context ${KIND_CONTEXT} -n ${SIGNOZ_NS} port-forward svc/${SIGNOZ_SVC} 8080:8080)"
}

# signoz_api <method> <path> [json-body]: call the SigNoz API with $SIGNOZ_TOKEN; prints the body.
signoz_api() {
  local args=(-sS -X "$1" "${PF_URL}$2" -H "Authorization: Bearer ${SIGNOZ_TOKEN}" -H 'Content-Type: application/json')
  [[ $# -ge 3 ]] && args+=(-d "$3")
  curl "${args[@]}"
}

# signoz_admin_login: register the first admin if SigNoz has none, then log in
# and set SIGNOZ_TOKEN. Needs PF_URL (a port-forward to SigNoz). The admin's
# credentials live in Secret signoz/signoz-admin so re-runs (and you) can log in.
signoz_admin_login() {
  local password exists
  password=$(secret_value "$SIGNOZ_NS" signoz-admin password || true)
  exists=$(curl -fsS "${PF_URL}/api/v2/sessions/context?email=${SIGNOZ_ADMIN_EMAIL}&ref=http://localhost:8080" | jq -r '.data.exists')
  if [[ "$exists" != "true" ]]; then
    [[ -n "$password" ]] || password="$(openssl rand -base64 18 | tr -d '/+=')Aa1!"
    apply_secret "$SIGNOZ_NS" signoz-admin "email=${SIGNOZ_ADMIN_EMAIL}" "password=${password}"
    local body
    body=$(jq -nc --arg e "$SIGNOZ_ADMIN_EMAIL" --arg p "$password" \
      '{email: $e, name: "admin", orgDisplayName: "agent-sandbox", password: $p}')
    curl -fsS -X POST "${PF_URL}/api/v1/register" -H 'Content-Type: application/json' -d "$body" >/dev/null ||
      die "registering the SigNoz admin user failed"
    ok "registered admin ${SIGNOZ_ADMIN_EMAIL} (password in Secret ${SIGNOZ_NS}/signoz-admin)"
  elif [[ -z "$password" ]]; then
    die "SigNoz already has user ${SIGNOZ_ADMIN_EMAIL} but Secret ${SIGNOZ_NS}/signoz-admin is missing; create it with that user's email/password and re-run"
  fi

  local org_id login
  org_id=$(curl -fsS "${PF_URL}/api/v2/sessions/context?email=${SIGNOZ_ADMIN_EMAIL}&ref=http://localhost:8080" | jq -r '.data.orgs[0].id')
  login=$(jq -nc --arg e "$SIGNOZ_ADMIN_EMAIL" --arg p "$password" --arg o "$org_id" '{email: $e, password: $p, orgId: $o}')
  SIGNOZ_TOKEN=$(curl -fsS -X POST "${PF_URL}/api/v2/sessions/email_password" -H 'Content-Type: application/json' -d "$login" |
    jq -r '.data.accessToken // empty')
  [[ -n "$SIGNOZ_TOKEN" ]] || die "SigNoz login failed for ${SIGNOZ_ADMIN_EMAIL}; check Secret ${SIGNOZ_NS}/signoz-admin"
}

step_signoz_apikey() {
  log "signoz-apikey: admin user, '${SIGNOZ_SA_NAME}' service account (signoz-viewer), API key Secret"
  retry port_forward "$SIGNOZ_NS" "$SIGNOZ_SVC" 8080 || die "cannot port-forward to SigNoz"
  retry curl -fsS "${PF_URL}/api/v2/healthz" >/dev/null || die "SigNoz API is not healthy"

  # 1. Reuse the stored key if SigNoz still accepts it.
  local key
  key=$(secret_value "$MCP_NS" signoz-api-key SIGNOZ_API_KEY || true)
  if [[ -n "$key" ]] && curl -fsS -o /dev/null "${PF_URL}/api/v1/service_accounts/me" -H "SIGNOZ-API-KEY: ${key}"; then
    ok "existing API key in ${MCP_NS}/signoz-api-key is valid"
    return
  fi

  # 2. Log in as the admin (registering it on first run).
  signoz_admin_login

  # 3. Service account with only the signoz-viewer role.
  local sa_id role_id
  sa_id=$(signoz_api GET /api/v1/service_accounts | jq -r --arg n "$SIGNOZ_SA_NAME" '.data[] | select(.name == $n) | .id' | head -1)
  if [[ -z "$sa_id" ]]; then
    sa_id=$(signoz_api POST /api/v1/service_accounts "$(jq -nc --arg n "$SIGNOZ_SA_NAME" '{name: $n}')" | jq -r '.data.id // empty')
    [[ -n "$sa_id" ]] || die "creating service account ${SIGNOZ_SA_NAME} failed"
  fi
  role_id=$(signoz_api GET /api/v1/roles | jq -r '.data[] | select(.name == "signoz-viewer") | .id')
  [[ -n "$role_id" ]] || die "role signoz-viewer not found in SigNoz"
  if ! signoz_api GET "/api/v1/service_accounts/${sa_id}/roles" | jq -e '.data[]? | select(.name == "signoz-viewer")' >/dev/null; then
    signoz_api POST /api/v1/service_account_roles \
      "$(jq -nc --arg s "$sa_id" --arg r "$role_id" '{serviceAccountId: $s, roleId: $r}')" |
      jq -e '.status == "success"' >/dev/null || die "assigning signoz-viewer to ${SIGNOZ_SA_NAME} failed"
  fi

  # 4. New key (the value is only returned at creation), stored as a Secret for the MCP server.
  key=$(signoz_api POST "/api/v1/service_accounts/${sa_id}/keys" \
    "$(jq -nc --arg n "mcp-$(date +%Y%m%d%H%M%S)" '{name: $n, expiresAt: 0}')" | jq -r '.data.key // empty')
  [[ -n "$key" ]] || die "creating an API key for ${SIGNOZ_SA_NAME} failed"
  curl -fsS -o /dev/null "${PF_URL}/api/v1/service_accounts/me" -H "SIGNOZ-API-KEY: ${key}" ||
    die "the new API key was rejected by SigNoz"

  kc create namespace "$MCP_NS" --dry-run=client -o yaml | kc apply -f - >/dev/null
  apply_secret "$MCP_NS" signoz-api-key "SIGNOZ_API_KEY=${key}"
  # Pick up the new key if the MCP server is already running.
  kc -n "$MCP_NS" get deployment signoz-mcp >/dev/null 2>&1 &&
    kc -n "$MCP_NS" rollout restart deployment/signoz-mcp >/dev/null
  ok "API key stored in Secret ${MCP_NS}/signoz-api-key"
}

# mcp_call <json-rpc>: POST one JSON-RPC message to the MCP server at $PF_URL/mcp.
mcp_call() {
  curl -fsS -X POST "${PF_URL}/mcp" -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' -d "$1" | sed -n 's/^data: //p;/^{/p'
}

mcp_list_alerts_ok() {
  MCP_RESULT=$(mcp_call '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"signoz_list_alerts","arguments":{}}}')
  jq -e '.result and (.result.isError | not)' >/dev/null <<<"$MCP_RESULT"
}

step_signoz_mcp() {
  log "signoz-mcp: MCP server in namespace ${MCP_NS}"
  kc -n "$MCP_NS" get secret signoz-api-key >/dev/null 2>&1 || die "Secret ${MCP_NS}/signoz-api-key missing; run ./setup.sh signoz-apikey"
  retry kc apply -f deploy/signoz-mcp/manifests.yaml >/dev/null
  retry kc -n "$MCP_NS" rollout status deployment/signoz-mcp --timeout=180s >/dev/null

  retry port_forward "$MCP_NS" signoz-mcp 8000 || die "cannot port-forward to the MCP server"
  local init tools
  init='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"setup.sh","version":"0"}}}'
  retry mcp_call "$init" >/dev/null || die "MCP initialize failed"
  tools=$(mcp_call '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' | jq -r '.result.tools | length')
  ((tools > 0)) || die "MCP server lists no tools"
  # A real call through the API key proves the MCP -> SigNoz path, not just the HTTP server.
  # On a new SigNoz org the alertmanager appears a minute or so after registration.
  RETRIES=24 RETRY_DELAY=5 retry mcp_list_alerts_ok ||
    die "signoz_list_alerts failed through MCP: $(jq -c '.error // .result.content' <<<"$MCP_RESULT" | head -c 400)"
  ok "${tools} tools; signoz_list_alerts works (in-cluster URL: http://signoz-mcp.${MCP_NS}.svc.cluster.local:8000/mcp)"
}

# count_k8s_logs: print how many logs with Kubernetes attributes SigNoz received
# in the last 10 minutes, queried with the viewer API key at $PF_URL.
count_k8s_logs() {
  local key now body
  key=$(secret_value "$MCP_NS" signoz-api-key SIGNOZ_API_KEY)
  now=$(($(date +%s) * 1000))
  body=$(jq -nc --argjson s $((now - 600000)) --argjson e "$now" '{
    start: $s, end: $e, requestType: "scalar",
    compositeQuery: {queries: [{type: "builder_query", spec: {
      name: "A", signal: "logs",
      aggregations: [{expression: "count()"}],
      filter: {expression: "k8s.pod.name EXISTS AND k8s.cluster.name = '\''kind-agent-sandbox'\''"}
    }}]}}')
  curl -fsS -X POST "${PF_URL}/api/v5/query_range" -H 'Content-Type: application/json' \
    -H "SIGNOZ-API-KEY: ${key}" -d "$body" |
    jq -r '.data.data.results[0].data[0][0] // 0'
}

wait_for_k8s_logs() {
  local n
  n=$(count_k8s_logs) || return 1
  ((n > 0)) || return 1
  LOG_COUNT=$n
}

step_collectors() {
  log "collectors: k8s-infra chart ${K8S_INFRA_CHART_VERSION} in namespace ${K8S_INFRA_NS}"
  retry helm repo add signoz https://charts.signoz.io --force-update >/dev/null
  retry helm --kube-context "$KIND_CONTEXT" upgrade --install k8s-infra signoz/k8s-infra \
    --version "$K8S_INFRA_CHART_VERSION" --namespace "$K8S_INFRA_NS" --create-namespace \
    "${K8S_INFRA_VALUES[@]}" --wait --timeout 5m >/dev/null
  local ds deploy
  ds=$(kc -n "$K8S_INFRA_NS" get daemonset -l app.kubernetes.io/component=otel-agent -o name | head -1)
  deploy=$(kc -n "$K8S_INFRA_NS" get deployment -l app.kubernetes.io/component=otel-deployment -o name | head -1)
  [[ -n "$ds" && -n "$deploy" ]] || die "k8s-infra otel-agent DaemonSet or otel-deployment not found"
  retry kc -n "$K8S_INFRA_NS" rollout status "$ds" --timeout=180s >/dev/null
  retry kc -n "$K8S_INFRA_NS" rollout status "$deploy" --timeout=180s >/dev/null

  # Prove data reaches SigNoz, not just that the pods run. The first batch takes a minute or so.
  retry port_forward "$SIGNOZ_NS" "$SIGNOZ_SVC" 8080 || die "cannot port-forward to SigNoz"
  RETRIES=12 RETRY_DELAY=5 retry wait_for_k8s_logs ||
    die "no Kubernetes logs in SigNoz yet; check: kubectl --context ${KIND_CONTEXT} -n ${K8S_INFRA_NS} logs ${ds}"
  ok "collectors running; SigNoz has ${LOG_COUNT} k8s logs from the last 10 minutes"
}

step_agent_sandbox() {
  log "agent-sandbox: controller ${AGENT_SANDBOX_VERSION} with extensions"
  local url="https://github.com/kubernetes-sigs/agent-sandbox/releases/download/${AGENT_SANDBOX_VERSION}/sandbox-with-extensions.yaml"
  # Server-side apply: the CRDs are too large for the client-side last-applied annotation.
  retry kc apply --server-side --force-conflicts -f "$url" >/dev/null
  retry kc -n agent-sandbox-system rollout status deployment/agent-sandbox-controller --timeout=180s >/dev/null
  kc wait --for=condition=Established crd/sandboxes.agents.x-k8s.io --timeout=60s >/dev/null
  ok "agent-sandbox controller ready"
}

step_k8s_mcp() {
  log "k8s-mcp: read-only Kubernetes MCP server in ${MCP_NS}"
  kc create namespace "$MCP_NS" --dry-run=client -o yaml | kc apply -f - >/dev/null
  retry kc apply -f deploy/k8s-mcp/manifests.yaml >/dev/null
  retry kc -n "$MCP_NS" rollout status deployment/k8s-mcp --timeout=180s >/dev/null

  # The ServiceAccount must not be able to read Secrets or write anything.
  local sa="system:serviceaccount:${MCP_NS}:k8s-mcp" check
  for check in "get secrets" "list configmaps" "delete pods" "create pods/exec" "patch deployments"; do
    # shellcheck disable=SC2086 # $check is "verb resource"
    if [[ "$(kc auth can-i $check --as "$sa" -A 2>/dev/null)" == "yes" ]]; then
      die "k8s-mcp ServiceAccount can '${check}'; it must be read-only without Secrets"
    fi
  done
  [[ "$(kc auth can-i list pods --as "$sa" -A)" == "yes" ]] || die "k8s-mcp ServiceAccount cannot list pods"
  ok "running; RBAC is read-only and excludes Secrets and ConfigMaps"
}

step_skills() {
  log "skills: the project's CRDs and the skills in skills/"
  # All CRDs, before the images step restarts controllers that watch them.
  retry kc apply --server-side -f deploy/crds/ >/dev/null
  kc wait --for=condition=Established crd/triageskills.triage.k8s-agent-sandbox.io \
    crd/incidents.triage.k8s-agent-sandbox.io crd/triageruns.triage.k8s-agent-sandbox.io \
    crd/remediations.triage.k8s-agent-sandbox.io crd/remediationskills.triage.k8s-agent-sandbox.io --timeout=60s >/dev/null

  # The API server must reject a bad skill (CEL name rule, size limits).
  local bad
  bad=$(kc apply --dry-run=server -f - 2>&1 <<'EOF' || true
apiVersion: triage.k8s-agent-sandbox.io/v1alpha1
kind: TriageSkill
metadata:
  name: bad.name
spec:
  description: ""
  instructions: x
EOF
)
  [[ "$bad" == *"Invalid value"* || "$bad" == *"invalid"* ]] || die "CRD validation did not reject an invalid skill: ${bad:0:300}"

  retry kc apply -f skills/ >/dev/null
  local want got
  want=$(find skills -maxdepth 1 -name '*.yaml' | wc -l | tr -d ' ')
  got=$(kc get triageskills -o name | wc -l | tr -d ' ')
  ((got >= want)) || die "expected at least ${want} TriageSkills, found ${got}"
  ok "${got} skills installed (kubectl get triageskills); new or edited skills apply to the next triage run"
}

step_images() {
  log "images: build triage-agent, triage-controller, coordinator, llm-proxy, slack-sink; load into kind"
  local images=(triage-agent triage-controller coordinator llm-proxy slack-sink) img
  for img in "${images[@]}"; do
    retry docker build -q --build-arg "CMD=${img}" -t "${img}:dev" . >/dev/null
  done
  retry kind load docker-image "${images[@]/%/:dev}" --name "$KIND_CLUSTER" >/dev/null 2>&1
  # Same tag, new image: restart running deployments so they use the new build.
  # (triage-agent runs in fresh sandboxes, so it picks up the new build on its own.)
  local dep
  for dep in "$TRIAGE_NS/triage-controller" "$TRIAGE_NS/coordinator" "$TRIAGE_NS/slack-sink" "$LLM_NS/llm-proxy"; do
    if kc -n "${dep%/*}" get deployment "${dep#*/}" >/dev/null 2>&1; then
      kc -n "${dep%/*}" rollout restart deployment "${dep#*/}" >/dev/null
    fi
  done
  ok "${images[*]} loaded (tag :dev)"
}

# probe <namespace> <labels> <runtimeClass|-> <curl args...>: run curl from a
# throwaway pod with the given identity, so NetworkPolicies apply as they would
# to the real workload. Prints "<http_code> <connect_code> rc=<curl exit>" and
# then the response body.
probe() {
  local ns=$1 labels=$2 rc_class=$3
  shift 3
  local overrides='{"spec":{"automountServiceAccountToken":false}}'
  [[ "$rc_class" != - ]] && overrides="{\"spec\":{\"automountServiceAccountToken\":false,\"runtimeClassName\":\"${rc_class}\"}}"
  local args
  args=$(printf '%q ' "$@")
  kc -n "$ns" run "probe-$RANDOM$RANDOM" --rm -i --restart=Never --quiet \
    --image="$CURL_IMAGE" --labels="$labels" --overrides="$overrides" --command -- \
    sh -c "sleep 2; curl -sS -k -m 8 -o /tmp/body -w '%{http_code} %{http_connect}' ${args} 2>/dev/null; echo \" rc=\$?\"; cat /tmp/body 2>/dev/null" \
    2>/dev/null || true
}

# probe_as <namespace> <labels> <serviceaccount> <curl args...>: like probe, but
# the pod runs under gVisor as the ServiceAccount, with a projected identity
# token (audience TOKEN_AUDIENCE) sent as the bearer token: the same credential
# a triage sandbox has.
probe_as() {
  local ns=$1 labels=$2 sa=$3
  shift 3
  local name="probe-$RANDOM$RANDOM" args script overrides
  args=$(printf '%q ' "$@")
  script="sleep 2; curl -sS -k -m 8 -o /tmp/body -w '%{http_code} %{http_connect}' -H \"Authorization: Bearer \$(cat /var/run/secrets/triage/token)\" ${args} 2>/dev/null; echo \" rc=\$?\"; cat /tmp/body 2>/dev/null"
  overrides=$(jq -nc --arg n "$name" --arg sa "$sa" --arg img "$CURL_IMAGE" --arg s "$script" --arg aud "$TOKEN_AUDIENCE" '{spec: {
    serviceAccountName: $sa, automountServiceAccountToken: false, runtimeClassName: "gvisor",
    volumes: [{name: "identity", projected: {sources: [{serviceAccountToken: {audience: $aud, expirationSeconds: 600, path: "token"}}]}}],
    containers: [{name: $n, image: $img, command: ["sh", "-c", $s],
      volumeMounts: [{name: "identity", mountPath: "/var/run/secrets/triage", readOnly: true}]}]}}')
  kc -n "$ns" run "$name" --rm -i --restart=Never --quiet --image="$CURL_IMAGE" --labels="$labels" \
    --overrides="$overrides" 2>/dev/null || true
}

# expect_status <code> <description> <probe output>
expect_status() { [[ "$3" == "$1 "* ]] || die "$2: expected HTTP $1, got: ${3%%$'\n'*}"; }

# expect_open / expect_blocked / expect_denied <description> <probe output>
expect_open() { [[ "$2" == *" rc=0"* ]] || die "$1: expected a connection, got: ${2%%$'\n'*}"; }
expect_blocked() {
  [[ "$2" != *" rc=0"* && "$2" != *" 403 "* ]] || die "$1: expected the network to block it, got: ${2%%$'\n'*}"
}
expect_denied() { [[ "$2" == *" 403 "* ]] || die "$1: expected the egress gateway to answer 403, got: ${2%%$'\n'*}"; }

step_egress() {
  log "egress: Envoy egress gateway in ${EGRESS_NS} (CONNECT allowlist)"
  retry kc apply -f deploy/egress/manifests.yaml >/dev/null
  retry kc -n "$EGRESS_NS" rollout status deployment/egress-gateway --timeout=180s >/dev/null

  # Probe as an llm-proxy pod: the gateway's only intended client besides the controller.
  local id="app.kubernetes.io/name=llm-proxy" gw="http://egress-gateway.${EGRESS_NS}.svc.cluster.local:3128" out
  kc create namespace "$LLM_NS" --dry-run=client -o yaml | kc apply -f - >/dev/null
  retry kc apply -f deploy/llm-proxy/manifests.yaml >/dev/null # its NetworkPolicy defines the client's limits
  out=$(probe "$LLM_NS" "$id" - -x "$gw" https://api.anthropic.com/)
  expect_open "CONNECT api.anthropic.com via gateway" "$out"
  out=$(probe "$LLM_NS" "$id" - -x "$gw" https://example.com/)
  expect_denied "CONNECT example.com via gateway" "$out"
  out=$(probe "$LLM_NS" "$id" - https://api.anthropic.com/)
  expect_blocked "direct HTTPS from llm-proxy (bypassing the gateway)" "$out"
  ok "allowlisted host reachable through the gateway; other hosts get 403; direct egress blocked"
}

llm_secret_ok() {
  [[ -n "$(secret_value "$LLM_NS" "$LLM_SECRET" ANTHROPIC_API_KEY 2>/dev/null)" ]]
}

# apply_local_llm_mode: when LLM_MODE=local, point llm-proxy at the local
# OpenAI-compatible server through the egress gateway, open the gateway to the
# host's port, and make the local model alias the one the coordinator grants
# each run (and the one setup's probe identity uses).
apply_local_llm_mode() {
  [[ "$LLM_MODE" == local ]] || return 0
  local model host_ip
  model=$(curl -fsS -m 5 "http://127.0.0.1:${LOCAL_LLM_PORT}/v1/models" | jq -r '.data[0].id // empty') ||
    die "no OpenAI-compatible server on localhost:${LOCAL_LLM_PORT}; start it or set LOCAL_LLM_PORT"
  [[ -n "$model" ]] || die "localhost:${LOCAL_LLM_PORT}/v1/models lists no models"
  host_ip=$(kc -n default run "resolve-$RANDOM" --rm -i --restart=Never --quiet --image="$CURL_IMAGE" \
    --command -- sh -c 'sleep 2; getent hosts host.docker.internal | cut -d" " -f1' 2>/dev/null | head -1 | tr -d '[:space:]')
  [[ -n "$host_ip" ]] || die "host.docker.internal does not resolve inside the cluster"

  kc apply -f - >/dev/null <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: egress-gateway-local-llm
  namespace: ${EGRESS_NS}
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: egress-gateway
  policyTypes: [Egress]
  egress:
    - to:
        - ipBlock: { cidr: ${host_ip}/32 }
      ports:
        - { protocol: TCP, port: ${LOCAL_LLM_PORT} }
EOF
  kc -n "$SANDBOX_NS" delete role,rolebinding triage-agent-local-llm --ignore-not-found >/dev/null # pre-coordinator leftovers
  kc -n "$LLM_NS" set env deployment/llm-proxy UPSTREAM_API=openai \
    "UPSTREAM_URL=http://host.docker.internal:${LOCAL_LLM_PORT}" "MODEL_MAP=${LOCAL_LLM_ALIAS}=${model}" \
    "HTTP_PROXY=http://egress-gateway.${EGRESS_NS}.svc.cluster.local:3128" >/dev/null
  kc -n "$TRIAGE_NS" get deployment triage-controller >/dev/null 2>&1 &&
    kc -n "$TRIAGE_NS" set env deployment/triage-controller "AGENT_TOOLS=${LOCAL_AGENT_TOOLS}" >/dev/null
  kc -n "$TRIAGE_NS" get deployment coordinator >/dev/null 2>&1 &&
    kc -n "$TRIAGE_NS" set env deployment/coordinator "AGENT_MODEL=${LOCAL_LLM_ALIAS}" \
      MAX_CONCURRENT_RUNS=1 RUN_TIMEOUT=30m REMEDIATION_TIMEOUT=30m >/dev/null # one run at a time; slow local models need longer
  LLM_MODEL=$LOCAL_LLM_ALIAS
  ok "local LLM: ${model} at host ${host_ip}:${LOCAL_LLM_PORT}, used by agents as '${LOCAL_LLM_ALIAS}'"
}

step_llm_secret() {
  log "llm-secret: Anthropic API key for the llm-proxy"
  if [[ "$LLM_MODE" == local ]]; then
    ok "skipped (LLM_MODE=local uses the local LLM, no key needed)"
    return
  fi
  kc create namespace "$LLM_NS" --dry-run=client -o yaml | kc apply -f - >/dev/null
  if llm_secret_ok; then
    ok "Secret ${LLM_NS}/${LLM_SECRET} exists"
    return
  fi
  cat >&2 <<EOF

  The llm-proxy needs an Anthropic API key. Create the Secret in another terminal:

    read -rs ANTHROPIC_API_KEY && \\
    kubectl --context ${KIND_CONTEXT} -n ${LLM_NS} create secret generic ${LLM_SECRET} \\
      --from-literal=ANTHROPIC_API_KEY="\$ANTHROPIC_API_KEY"

  (read -rs keeps the key out of your shell history. Only the llm-proxy reads it;
  sandboxes never see it.)

EOF
  [[ -t 0 ]] || die "Secret ${LLM_NS}/${LLM_SECRET} is missing; create it as shown above and re-run"
  local attempt
  for attempt in 1 2 3; do
    read -rp "  Press Enter once the Secret exists... " _
    if llm_secret_ok; then
      ok "Secret ${LLM_NS}/${LLM_SECRET} found"
      return
    fi
    warn "still missing (attempt ${attempt}/3)"
  done
  die "Secret ${LLM_NS}/${LLM_SECRET} is missing; create it and re-run ./setup.sh llm-secret"
}

step_llm_proxy() {
  log "llm-proxy: policy-enforcing Anthropic proxy in ${LLM_NS}"
  [[ "$LLM_MODE" == local ]] || llm_secret_ok || die "Secret ${LLM_NS}/${LLM_SECRET} is missing; run ./setup.sh llm-secret (or use LLM_MODE=local)"
  retry kc apply -f deploy/triage/manifests.yaml >/dev/null # namespaces and the agent NetworkPolicy
  retry kc apply -f deploy/egress/manifests.yaml >/dev/null
  retry kc apply -f deploy/llm-proxy/manifests.yaml >/dev/null
  apply_apiserver_policy # llm-proxy calls TokenReview/SubjectAccessReview
  apply_local_llm_mode
  retry kc -n "$EGRESS_NS" rollout status deployment/egress-gateway --timeout=180s >/dev/null
  retry kc -n "$LLM_NS" rollout status deployment/llm-proxy --timeout=180s >/dev/null
  probe_identity

  local id="app.kubernetes.io/name=triage-agent" url="http://llm-proxy.${LLM_NS}.svc.cluster.local:8080" out
  local count="{\"model\":\"${LLM_MODEL}\",\"messages\":[{\"role\":\"user\",\"content\":\"ping\"}]}"
  local post=(-X POST -H 'content-type: application/json')

  # No identity: rejected before anything else.
  out=$(probe "$SANDBOX_NS" "$id" gvisor "${post[@]}" -d "$count" "${url}/v1/messages/count_tokens")
  expect_status 401 "llm-proxy without an identity token" "$out"
  # A valid identity whose RBAC does not grant the model.
  out=$(probe_as "$SANDBOX_NS" "$id" "$PROBE_SA" "${post[@]}" -d '{"model":"claude-opus-5-5","max_tokens":1,"messages":[]}' "${url}/v1/messages")
  expect_status 403 "llm-proxy with a model RBAC does not grant" "$out"
  # A different ServiceAccount, with no RBAC at all.
  out=$(probe_as "$SANDBOX_NS" "$id" default "${post[@]}" -d "$count" "${url}/v1/messages/count_tokens")
  expect_status 403 "llm-proxy as the default ServiceAccount" "$out"
  # The agent identity and a granted model, end to end through the gateway.
  if [[ "$LLM_MODE" == local ]]; then
    # A tiny real completion from the local model (count_tokens is answered by the proxy itself).
    out=$(probe_as "$SANDBOX_NS" "$id" "$PROBE_SA" "${post[@]}" -m 120 \
      -d "{\"model\":\"${LLM_MODEL}\",\"max_tokens\":8,\"messages\":[{\"role\":\"user\",\"content\":\"Say ok.\"}]}" "${url}/v1/messages")
    [[ "$out" == "200 "* && "$out" == *'"type": "message"'* || "$out" == "200 "* && "$out" == *'"type":"message"'* ]] ||
      die "local LLM call through llm-proxy failed (kubectl --context ${KIND_CONTEXT} -n ${LLM_NS} logs deploy/llm-proxy): ${out:0:300}"
    ok "callers authenticated by ServiceAccount token; models authorized by RBAC; local LLM reachable via gateway"
    return
  fi
  # count_tokens is free and proves identity, RBAC, key, proxy, gateway, and Anthropic.
  out=$(probe_as "$SANDBOX_NS" "$id" "$PROBE_SA" "${post[@]}" -d "$count" "${url}/v1/messages/count_tokens")
  [[ "$out" == "200 "* && "$out" == *input_tokens* ]] ||
    die "count_tokens through llm-proxy failed (check the API key and: kubectl --context ${KIND_CONTEXT} -n ${LLM_NS} logs deploy/llm-proxy): ${out%%$'\n'*}"
  ok "callers authenticated by ServiceAccount token; models authorized by RBAC; Anthropic reachable"
}

# apply_apiserver_policy: allow k8s-mcp and the triage controller to reach the
# Kubernetes API server. NetworkPolicy needs IPs, so they are read from the cluster.
apply_apiserver_policy() {
  local svc_ip ep_port ep_ips ip blocks=""
  svc_ip=$(kc -n default get service kubernetes -o jsonpath='{.spec.clusterIP}')
  ep_port=$(kc -n default get endpointslices -l kubernetes.io/service-name=kubernetes -o jsonpath='{.items[0].ports[0].port}')
  ep_ips=$(kc -n default get endpointslices -l kubernetes.io/service-name=kubernetes -o jsonpath='{.items[*].endpoints[*].addresses[*]}')
  [[ -n "$svc_ip" && -n "$ep_port" && -n "$ep_ips" ]] || die "could not read the API server addresses"
  for ip in $ep_ips; do
    blocks+="        - ipBlock: { cidr: ${ip}/32 }"$'\n'
  done
  local ns app policy entry
  # ns:app:policy-name (the first three predate per-app names; kept to avoid duplicates)
  for entry in "${MCP_NS}:k8s-mcp:allow-kube-apiserver" "${TRIAGE_NS}:triage-controller:allow-kube-apiserver" \
    "${LLM_NS}:llm-proxy:allow-kube-apiserver" "${TRIAGE_NS}:coordinator:allow-kube-apiserver-coordinator"; do
    IFS=: read -r ns app policy <<<"$entry"
    kc get namespace "$ns" >/dev/null 2>&1 || continue
    kc apply -f - >/dev/null <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ${policy}
  namespace: ${ns}
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: ${app}
  policyTypes: [Egress]
  egress:
    - to:
        - ipBlock: { cidr: ${svc_ip}/32 }
      ports:
        - { protocol: TCP, port: 443 }
    - to:
${blocks}      ports:
        - { protocol: TCP, port: ${ep_port} }
EOF
  done
}

# mcp_probe_call <url> <tool>: JSON-RPC tools/call from an agent-shaped pod.
mcp_probe_call() {
  probe "$SANDBOX_NS" "app.kubernetes.io/name=triage-agent" gvisor -X POST \
    -H 'content-type: application/json' -H 'accept: application/json, text/event-stream' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"$2\",\"arguments\":{}}}" "$1"
}

step_triage() {
  log "triage: controller in ${TRIAGE_NS}, sandbox namespace ${SANDBOX_NS}, egress limits"
  kc -n "$MCP_NS" get secret signoz-api-key >/dev/null 2>&1 || die "run ./setup.sh signoz-apikey first"
  kc -n "$LLM_NS" get deployment llm-proxy >/dev/null 2>&1 || die "run ./setup.sh llm-proxy first"
  # The controller watches TriageRuns; its HTTP server starts only once that cache syncs.
  retry kc apply --server-side -f deploy/crds/triage.k8s-agent-sandbox.io_incidents.yaml \
    -f deploy/crds/triage.k8s-agent-sandbox.io_triageruns.yaml >/dev/null
  kc wait --for=condition=Established crd/incidents.triage.k8s-agent-sandbox.io \
    crd/triageruns.triage.k8s-agent-sandbox.io --timeout=60s >/dev/null
  retry kc apply -f deploy/triage/manifests.yaml >/dev/null
  # Before the coordinator, every sandbox shared one identity. Remove it.
  kc -n "$SANDBOX_NS" delete serviceaccount,role,rolebinding triage-agent --ignore-not-found >/dev/null
  retry kc apply -f deploy/egress/policies.yaml >/dev/null
  apply_apiserver_policy
  apply_local_llm_mode # re-applying the manifests resets the controller's AGENT_TOOLS
  probe_identity
  retry kc -n "$TRIAGE_NS" rollout status deployment/triage-controller --timeout=180s >/dev/null
  # Kubelet probes must still pass with the ingress policies in place.
  retry kc -n "$MCP_NS" rollout status deployment/signoz-mcp --timeout=120s >/dev/null
  retry kc -n "$MCP_NS" rollout status deployment/k8s-mcp --timeout=120s >/dev/null

  # Prove the agent's network box from inside an agent-shaped gVisor pod.
  local id="app.kubernetes.io/name=triage-agent" u out
  for u in "http://triage-controller.${TRIAGE_NS}.svc.cluster.local:8080/healthz" \
    "http://llm-proxy.${LLM_NS}.svc.cluster.local:8080/healthz"; do
    expect_open "sandbox -> ${u}" "$(probe "$SANDBOX_NS" "$id" gvisor "$u")"
  done
  for u in "http://${SIGNOZ_SVC}.${SIGNOZ_NS}.svc.cluster.local:8080/api/v2/healthz" \
    "http://${SIGNOZ_RELEASE}-clickhouse.${SIGNOZ_NS}.svc.cluster.local:8123/ping" \
    "http://keeper-${SIGNOZ_RELEASE}-clickhouse-keeper.${SIGNOZ_NS}.svc.cluster.local:2181/" \
    "https://kubernetes.default.svc.cluster.local/healthz" \
    "http://egress-gateway.${EGRESS_NS}.svc.cluster.local:3128/" \
    "https://api.anthropic.com/" "https://example.com/"; do
    expect_blocked "sandbox -> ${u}" "$(probe "$SANDBOX_NS" "$id" gvisor "$u")"
  done

  # The controller's agent API: identity required, and only a triage run's pod gets an assignment.
  local asg="http://triage-controller.${TRIAGE_NS}.svc.cluster.local:8080/v1/assignment"
  expect_status 401 "assignment without an identity token" "$(probe "$SANDBOX_NS" "$id" gvisor "$asg")"
  expect_status 403 "assignment for a pod that is not a triage run" "$(probe_as "$SANDBOX_NS" "$id" "$PROBE_SA" "$asg")"

  # MCP servers must still reach their own backends under their egress limits.
  out=$(mcp_probe_call "http://k8s-mcp.${MCP_NS}.svc.cluster.local:8080/mcp" namespaces_list)
  [[ "$out" == *kube-system* ]] || die "k8s-mcp cannot reach the API server: ${out:0:300}"
  out=$(mcp_probe_call "http://signoz-mcp.${MCP_NS}.svc.cluster.local:8000/mcp" signoz_list_alerts)
  [[ "$out" == "200 "* && "$out" != *'"isError":true'* ]] || die "signoz-mcp cannot reach SigNoz: ${out:0:300}"
  ok "sandbox reaches only DNS, controller, MCP servers, and llm-proxy; MCP servers reach only their backends"
}

# probe_identity: (re)create setup's stand-in for a run identity, shaped the way
# the coordinator builds one: models/use pinned to LLM_MODEL and triageruns
# pinned to a run named setup-probe that never exists. Probes act as an agent
# with it. Call after apply_local_llm_mode, which may change LLM_MODEL.
probe_identity() {
  kc apply -f - >/dev/null <<EOF
apiVersion: v1
kind: ServiceAccount
metadata: { name: ${PROBE_SA}, namespace: ${SANDBOX_NS} }
automountServiceAccountToken: false
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: { name: ${PROBE_SA}, namespace: ${SANDBOX_NS} }
rules:
  - apiGroups: [triage.k8s-agent-sandbox.io]
    resources: [triageruns]
    resourceNames: [setup-probe]
    verbs: [get, update]
  - apiGroups: [triage.k8s-agent-sandbox.io]
    resources: [models]
    resourceNames: [${LLM_MODEL}]
    verbs: [use]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: { name: ${PROBE_SA}, namespace: ${SANDBOX_NS} }
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: Role, name: ${PROBE_SA} }
subjects:
  - { kind: ServiceAccount, name: ${PROBE_SA}, namespace: ${SANDBOX_NS} }
EOF
}

# can_i <user> <args...>: "yes" or "no" from kubectl auth can-i as <user>.
can_i() {
  local user=$1
  shift
  kc auth can-i "$@" --as="$user" 2>/dev/null || true
}

# sar_allowed <sa> <resource> <name> <verb>: "true" if RBAC lets ServiceAccount
# <sa> in SANDBOX_NS do <verb> on <resource>/<name> in the triage API group,
# asked the way our services ask (SubjectAccessReview). kubectl auth can-i
# cannot: these resources exist only as RBAC names, not in discovery.
sar_allowed() {
  kc create -o jsonpath='{.status.allowed}' -f - 2>/dev/null <<EOF || true
apiVersion: authorization.k8s.io/v1
kind: SubjectAccessReview
spec:
  user: system:serviceaccount:${SANDBOX_NS}:$1
  groups: [system:serviceaccounts, "system:serviceaccounts:${SANDBOX_NS}", system:authenticated]
  resourceAttributes: {namespace: ${SANDBOX_NS}, group: triage.k8s-agent-sandbox.io, resource: "$2", name: "$3", verb: "$4"}
EOF
}

# step_role_as_coordinator <name> <rules-yaml>: server-side dry-run of creating
# a Role as the coordinator; prints the API server's error, or nothing.
step_role_as_coordinator() {
  kc create --dry-run=server --as="$COORDINATOR_USER" -f - 2>&1 >/dev/null <<EOF || true
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: { name: $1, namespace: ${SANDBOX_NS} }
rules:
$2
EOF
}

# token_authenticated <token>: "true" if a TokenReview for TOKEN_AUDIENCE accepts it.
token_authenticated() {
  kc create -o jsonpath='{.status.authenticated}' -f - 2>/dev/null <<EOF || true
apiVersion: authentication.k8s.io/v1
kind: TokenReview
spec: { token: "$1", audiences: ["${TOKEN_AUDIENCE}"] }
EOF
}

step_coordinator() {
  log "coordinator: per-step identities and step order, in ${TRIAGE_NS}"
  kc get crd triageruns.triage.k8s-agent-sandbox.io >/dev/null 2>&1 || die "run ./setup.sh triage first"
  retry kc apply -f deploy/coordinator/manifests.yaml >/dev/null
  apply_apiserver_policy
  apply_local_llm_mode # sets the coordinator's AGENT_MODEL in local mode
  retry kc -n "$TRIAGE_NS" rollout status deployment/coordinator --timeout=180s >/dev/null

  # RBAC: identities only, only in the sandbox namespace, and nothing it could
  # use to reach real resources.
  local verdict check
  for check in "yes:create roles -n ${SANDBOX_NS}" "yes:create rolebindings -n ${SANDBOX_NS}" \
    "yes:create serviceaccounts -n ${SANDBOX_NS}" "yes:create triageruns.triage.k8s-agent-sandbox.io -n ${SANDBOX_NS}" \
    "yes:create remediations.triage.k8s-agent-sandbox.io -n ${SANDBOX_NS}" "no:create remediations.triage.k8s-agent-sandbox.io -n default" \
    "no:create roles -n default" "no:create clusterroles" "no:update roles -n ${SANDBOX_NS}" \
    "no:escalate roles -n ${SANDBOX_NS}" "no:bind roles -n ${SANDBOX_NS}" "no:list roles -n ${SANDBOX_NS}" \
    "no:create pods -n ${SANDBOX_NS}" "no:get secrets -n ${SANDBOX_NS}" "no:create serviceaccounts --subresource=token -n ${SANDBOX_NS}" \
    "no:create triageruns.triage.k8s-agent-sandbox.io -n default"; do
    # shellcheck disable=SC2086 # the check is a word list on purpose
    verdict=$(can_i "$COORDINATOR_USER" ${check#*:})
    [[ "$verdict" == "${check%%:*}" ]] || die "coordinator RBAC: can-i ${check#*:} = ${verdict:-error}, want ${check%%:*}"
  done

  # Admission policy: a step-shaped Role passes; broader ones fail even where
  # RBAC alone would allow them.
  local pinned unpinned core err
  pinned='  - { apiGroups: [triage.k8s-agent-sandbox.io], resources: [triageruns], resourceNames: [x], verbs: [get] }'
  unpinned='  - { apiGroups: [triage.k8s-agent-sandbox.io], resources: [triageruns], verbs: [get] }'
  core='  - { apiGroups: [""], resources: [pods], resourceNames: [x], verbs: [delete] }'
  err=$(step_role_as_coordinator triage-setupcheck "$pinned")
  [[ -z "$err" ]] || die "coordinator cannot create a step-shaped Role: ${err}"
  err=$(step_role_as_coordinator triage-setupcheck "$unpinned")
  [[ "$err" == *"pinned by resourceNames"* ]] || die "unpinned Role not denied by the admission policy: ${err:-allowed}"
  err=$(step_role_as_coordinator setupcheck "$pinned")
  [[ "$err" == *"named triage-"* ]] || die "Role without a step name not denied by the admission policy: ${err:-allowed}"
  err=$(step_role_as_coordinator triage-setupcheck "$core")
  [[ "$err" == *forbidden* ]] || die "Role on pods not denied: ${err:-allowed}"

  # A run's identity may act on its own run only.
  local probe_user="system:serviceaccount:${SANDBOX_NS}:${PROBE_SA}"
  probe_identity
  [[ $(can_i "$probe_user" get triageruns.triage.k8s-agent-sandbox.io/setup-probe -n "$SANDBOX_NS") == yes ]] ||
    die "probe identity cannot get its own run"
  [[ $(can_i "$probe_user" get triageruns.triage.k8s-agent-sandbox.io/another-run -n "$SANDBOX_NS") == no ]] ||
    die "probe identity can get another run"

  # Revocation: deleting a step's ServiceAccount kills its tokens at once.
  local sa=triage-setup-revoke tok state="" i
  kc -n "$SANDBOX_NS" create serviceaccount "$sa" --dry-run=client -o yaml | kc apply -f - >/dev/null
  tok=$(kc -n "$SANDBOX_NS" create token "$sa" --audience "$TOKEN_AUDIENCE" --duration 10m)
  [[ $(token_authenticated "$tok") == true ]] || die "a fresh step token was not accepted"
  kc -n "$SANDBOX_NS" delete serviceaccount "$sa" --wait >/dev/null
  for i in 1 2 3 4 5 6 7 8 9 10; do
    state=$(token_authenticated "$tok")
    [[ "$state" == true ]] || break
    sleep 1
  done
  [[ "$state" != true ]] || die "a step token still authenticates after its ServiceAccount was deleted"
  ok "coordinator creates only step-shaped identities in ${SANDBOX_NS}; run RBAC is pinned per run; deleting an identity revokes its token"
}

# step_remediation: open REMEDIATION_NAMESPACES to remediation, install the
# starter RemediationSkill, and prove the boundaries: the executor may write
# only there, a remediation identity may propose only on its own target.
step_remediation() {
  log "remediation: executor RBAC in ${REMEDIATION_NAMESPACES}, RemediationSkills, identity checks"
  kc -n "$TRIAGE_NS" get deployment coordinator >/dev/null 2>&1 || die "run ./setup.sh coordinator first"
  kc get crd remediations.triage.k8s-agent-sandbox.io >/dev/null 2>&1 || die "run ./setup.sh skills first (it installs the CRDs)"
  local ns
  for ns in ${REMEDIATION_NAMESPACES}; do
    kc create namespace "$ns" --dry-run=client -o yaml | kc apply -f - >/dev/null
    sed "s/__NAMESPACE__/${ns}/g" deploy/remediation/namespace.yaml | retry kc apply -f - >/dev/null
  done
  retry kc apply -f deploy/remediation/skills.yaml >/dev/null
  # Older manifests had no remediation settings; set them so the step works
  # without re-running triage and coordinator. No-ops when already set.
  kc -n "$TRIAGE_NS" set env deployment/triage-controller REMEDIATION_ENABLED=true \
    "REMEDIATION_MODE=${REMEDIATION_MODE}" >/dev/null
  kc -n "$TRIAGE_NS" set env deployment/coordinator REMEDIATION_ENABLED=true >/dev/null
  retry kc -n "$TRIAGE_NS" rollout status deployment/triage-controller --timeout=180s >/dev/null
  retry kc -n "$TRIAGE_NS" rollout status deployment/coordinator --timeout=180s >/dev/null

  # Executor (the triage controller) writes workloads only where opened, and
  # only with the verbs its drivers use.
  local controller="system:serviceaccount:${TRIAGE_NS}:triage-controller" first=${REMEDIATION_NAMESPACES%% *} check verdict
  for check in "yes:delete pods -n ${first}" "yes:patch deployments.apps -n ${first}" "yes:update deployments.apps --subresource=scale -n ${first}" \
    "no:create pods -n ${first}" "no:get secrets -n ${first}" "no:delete deployments.apps -n ${first}" \
    "no:delete pods -n default" "no:patch deployments.apps -n kube-system" "no:delete pods -n ${SANDBOX_NS}"; do
    # shellcheck disable=SC2086 # the check is a word list on purpose
    verdict=$(can_i "$controller" ${check#*:})
    [[ "$verdict" == "${check%%:*}" ]] || die "executor RBAC: can-i ${check#*:} = ${verdict:-error}, want ${check%%:*}"
  done
  for check in "yes:get pods -n ${first}" "yes:get replicasets.apps -n ${first}" "no:list pods -n ${first}" \
    "no:get pods -n default" "no:get secrets -n ${first}"; do
    # shellcheck disable=SC2086
    verdict=$(can_i "$COORDINATOR_USER" ${check#*:})
    [[ "$verdict" == "${check%%:*}" ]] || die "coordinator target RBAC: can-i ${check#*:} = ${verdict:-error}, want ${check%%:*}"
  done

  # A remediation identity as the coordinator builds it (remediation.RunRules):
  # one verb per allowed action, pinned to <namespace>.<name> of the target.
  local pinned err
  pinned="  - { apiGroups: [triage.k8s-agent-sandbox.io], resources: [deployments], resourceNames: [${first}.setup-probe], verbs: [rollout-restart] }"
  err=$(step_role_as_coordinator remediate-setupcheck "$pinned")
  [[ -z "$err" ]] || die "coordinator cannot create a remediation-shaped Role: ${err}"
  kc apply -f - >/dev/null <<EOF
apiVersion: v1
kind: ServiceAccount
metadata: { name: remediate-setup-probe, namespace: ${SANDBOX_NS} }
automountServiceAccountToken: false
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: { name: remediate-setup-probe, namespace: ${SANDBOX_NS} }
rules:
${pinned}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: { name: remediate-setup-probe, namespace: ${SANDBOX_NS} }
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: Role, name: remediate-setup-probe }
subjects:
  - { kind: ServiceAccount, name: remediate-setup-probe, namespace: ${SANDBOX_NS} }
EOF
  local args
  for check in "true:deployments ${first}.setup-probe rollout-restart" "false:deployments ${first}.other rollout-restart" \
    "false:deployments kube-system.setup-probe rollout-restart" "false:deployments ${first}.setup-probe scale" \
    "false:pods ${first}.setup-probe restart-pod"; do
    read -ra args <<<"${check#*:}"
    verdict=$(sar_allowed remediate-setup-probe "${args[@]}")
    [[ "$verdict" == "${check%%:*}" ]] || die "remediation identity: ${args[2]} ${args[0]}/${args[1]} allowed=${verdict:-error}, want ${check%%:*}"
  done
  [[ $(can_i "system:serviceaccount:${SANDBOX_NS}:remediate-setup-probe" patch deployments.apps -n "$first") == no ]] ||
    die "a remediation identity can patch Deployments directly"
  kc -n "$SANDBOX_NS" delete serviceaccount,role,rolebinding remediate-setup-probe --ignore-not-found >/dev/null

  ok "remediation open in: ${REMEDIATION_NAMESPACES} (mode ${REMEDIATION_MODE}); $(kc get remediationskills -o name | wc -l | tr -d ' ') RemediationSkill(s); identities propose only on their own target"
}

step_slack_sink() {
  log "slack-sink: local Slack stand-in for triage reports"
  local url="http://slack-sink.${TRIAGE_NS}.svc.cluster.local:8080/services/T000/B000/LOCAL" current
  retry kc apply -f deploy/slack-sink/manifests.yaml >/dev/null
  retry kc -n "$TRIAGE_NS" rollout status deployment/slack-sink --timeout=120s >/dev/null
  current=$(secret_value "$TRIAGE_NS" slack-webhook url || true)
  if [[ -n "$current" && "$current" != "$url" ]]; then
    # A URL someone set by hand (real Slack): keep it, the sink stays deployed but unused.
    warn "alert-triage/slack-webhook points elsewhere; leaving it (delete the Secret to use slack-sink)"
  elif [[ "$current" != "$url" ]]; then
    apply_secret "$TRIAGE_NS" slack-webhook "url=${url}"
    # The controller reads SLACK_WEBHOOK_URL at startup.
    kc -n "$TRIAGE_NS" rollout restart deployment/triage-controller >/dev/null
    retry kc -n "$TRIAGE_NS" rollout status deployment/triage-controller --timeout=120s >/dev/null
  fi
  # Only the controller may post: prove both sides.
  local post=(-X POST -H 'content-type: application/json' -d '{"text":"setup.sh: slack-sink check"}' "$url")
  expect_open "controller -> slack-sink" "$(probe "$TRIAGE_NS" app.kubernetes.io/name=triage-controller - "${post[@]}")"
  expect_blocked "other pod -> slack-sink" "$(probe "$TRIAGE_NS" app.kubernetes.io/name=intruder - "${post[@]}")"
  ok "reports go to slack-sink; view: kubectl --context ${KIND_CONTEXT} -n ${TRIAGE_NS} port-forward svc/slack-sink 8090:8080, then http://localhost:8090"
}

step_signoz_alerts() {
  log "signoz-alerts: webhook channel and Kubernetes warning-event rule"
  retry port_forward "$SIGNOZ_NS" "$SIGNOZ_SVC" 8080 || die "cannot port-forward to SigNoz"
  signoz_admin_login

  local channel=triage-webhook
  local hook="http://triage-controller.${TRIAGE_NS}.svc.cluster.local:8080/webhook/signoz"
  if ! signoz_api GET /api/v1/channels | jq -e --arg n "$channel" '.data[]? | select(.name == $n)' >/dev/null; then
    signoz_api POST /api/v1/channels "$(jq -nc --arg n "$channel" --arg u "$hook" \
      '{name: $n, webhook_configs: [{url: $u, send_resolved: false}]}')" |
      jq -e '.status == "success"' >/dev/null || die "creating SigNoz channel ${channel} failed"
  fi

  local rule="K8s warning events"
  # Always written from here (create or update), so fixes reach an existing rule.
  local rule_id filter body resp excluded
  rule_id=$(signoz_api GET '/api/v3/rules?limit=200' | jq -r --arg n "$rule" '[.data.rules[]? | select(.alert == $n) | .id][0] // empty')
  # The triage system must never alert on itself (that would start triage runs about triage).
  excluded="'${SANDBOX_NS}', '${TRIAGE_NS}', '${MCP_NS}', '${LLM_NS}', '${EGRESS_NS}', '${K8S_INFRA_NS}', '${SIGNOZ_NS}', 'agent-sandbox-system'"
  filter="k8s.event.reason IN ('BackOff', 'Unhealthy', 'Failed', 'FailedScheduling', 'FailedMount', 'OOMKilling')"
  # Explicit attribute context plus EXISTS: a bare NOT IN can resolve to the resource
  # field, which events lack, and a negative filter matches missing fields.
  filter+=" AND attribute.k8s.namespace.name EXISTS AND attribute.k8s.namespace.name NOT IN (${excluded})"
  {
    body=$(jq -nc --arg name "$rule" --arg filter "$filter" --arg ch "$channel" '{
      schemaVersion: "v2alpha1",
      alert: $name,
      alertType: "LOGS_BASED_ALERT",
      ruleType: "threshold_rule",
      description: "Kubernetes warning events per object. Each firing alert starts a sandboxed triage run.",
      labels: {source: "k8s-triage"},
      annotations: {summary: "Kubernetes warning events"},
      condition: {
        compositeQuery: {queryType: "builder", panelType: "graph", queries: [{type: "builder_query", spec: {
          name: "A", signal: "logs", stepInterval: 60,
          aggregations: [{expression: "count()"}],
          filter: {expression: $filter},
          groupBy: [
            {name: "k8s.namespace.name", fieldContext: "attribute"},
            {name: "k8s.object.kind", fieldContext: "resource"},
            {name: "k8s.object.name", fieldContext: "resource"}]}}]},
        selectedQueryName: "A",
        thresholds: {kind: "basic", spec: [{name: "warning", target: 0, op: "above", matchType: "at_least_once", channels: [$ch]}]}},
      evaluation: {kind: "rolling", spec: {evalWindow: "5m", frequency: "1m"}},
      notificationSettings: {renotify: {enabled: false}, usePolicy: false}}')
    local method=POST path=/api/v2/rules code
    [[ -n "$rule_id" ]] && method=PUT path="/api/v2/rules/${rule_id}"
    # PUT answers 204 with no body, so judge by HTTP status, not a JSON "status".
    resp=$(curl -sS -X "$method" "${PF_URL}${path}" -H "Authorization: Bearer ${SIGNOZ_TOKEN}" \
      -H 'Content-Type: application/json' -d "$body" -w '\n%{http_code}')
    code=${resp##*$'\n'}
    [[ "$code" == 2* ]] || die "writing rule '${rule}' failed (HTTP ${code}): $(head -c 600 <<<"${resp%$'\n'*}")"
    [[ -n "$rule_id" ]] || rule_id=$(jq -r '.data.id // empty' <<<"${resp%$'\n'*}")
    # Read it back: the live rule must carry the namespace exclusion.
    signoz_api GET "/api/v2/rules/${rule_id}" | grep -q 'attribute.k8s.namespace.name EXISTS' ||
      die "rule '${rule}' does not carry the namespace exclusion after writing it"
  }
  ok "channel '${channel}' -> controller webhook; rule '${rule}' $([[ -n "$rule_id" ]] && echo updated || echo created)"
}

step_demo() {
  log "demo: crashlooping workload -> SigNoz alert -> sandboxed triage (uses the Anthropic API)"
  retry kc apply -f deploy/demo/crashloop.yaml >/dev/null
  ok "deployed workloads/payments-api (fails to resolve its database host)"
  retry port_forward "$TRIAGE_NS" triage-controller 8080 || die "cannot port-forward to the triage controller"

  local deadline=$((SECONDS + 1200)) runs status
  log "waiting for the alert and the triage run (usually 3-8 minutes)"
  while ((SECONDS < deadline)); do
    runs=$(curl -fsS "${PF_URL}/v1/runs" 2>/dev/null || echo '[]')
    status=$(jq -r '[.[] | select(.alert.labels["k8s.namespace.name"] == "workloads")][0].status // "none"' <<<"$runs")
    case "$status" in
      done | failed) break ;;
      none) printf '.' ;;
      *) printf '%s ' "$status" ;;
    esac
    sleep 15
  done
  echo
  local run
  run=$(jq '[.[] | select(.alert.labels["k8s.namespace.name"] == "workloads")][0]' <<<"$runs")
  case "$status" in
    done)
      ok "triage run $(jq -r .id <<<"$run") finished"
      jq -r '.report | "\nSummary: \(.summary)\nLikely cause (\(.confidence)): \(.likely_cause)\n\nEvidence:\n\(.evidence | map("  - [\(.source)] \(.detail)") | join("\n"))\n\nNext steps:\n\(.next_steps | to_entries | map("  \(.key + 1). \(.value)") | join("\n"))"' <<<"$run"
      ;;
    failed) die "triage run failed: $(jq -r .error <<<"$run")" ;;
    *) die "no finished triage run within 20 minutes; check: kubectl --context ${KIND_CONTEXT} -n ${TRIAGE_NS} logs deploy/triage-controller" ;;
  esac
  log "clean up the demo with: kubectl --context ${KIND_CONTEXT} delete namespace workloads"
}

# --- main --------------------------------------------------------------------

recreate=false
selected=()
for arg in "$@"; do
  case "$arg" in
    --list) printf '%s\n' "${STEPS[@]}"; printf '%s (optional)\n' "${OPTIONAL_STEPS[@]}"; exit 0 ;;
    --recreate) recreate=true ;;
    -h | --help) sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*) die "unknown flag: $arg" ;;
    *)
      [[ " ${STEPS[*]} ${OPTIONAL_STEPS[*]} " == *" $arg "* ]] || die "unknown step: $arg (see --list)"
      selected+=("$arg")
      ;;
  esac
done
((${#selected[@]} > 0)) || selected=("${STEPS[@]}")

if $recreate && cluster_exists; then
  log "recreate: deleting cluster ${KIND_CLUSTER}"
  kind delete cluster --name "$KIND_CLUSTER"
fi

for s in "${selected[@]}"; do
  "step_${s//-/_}"
done

log "done. kubectl context: ${KIND_CONTEXT}"
