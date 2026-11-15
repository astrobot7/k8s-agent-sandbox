---
paths:
  - "**/*.go"
  - "config/**"
  - "deploy/**"
  - "**/*.yaml"
---

# Kubernetes code and manifests

## Go clients and controllers

- Use `sigs.k8s.io/controller-runtime` for controllers and the typed `client.Client` for CRUD. Drop to client-go only when controller-runtime lacks the API.
- Reconcilers are idempotent and level-triggered: read the current state, compute desired state, apply the difference. Never assume the previous reconcile ran.
- Return `ctrl.Result{RequeueAfter: d}` for expected waits. Return an error only for real failures.
- Write status through the status subresource (`r.Status().Update`/`Patch`). Use conditions (`metav1.Condition`) with `meta.SetStatusCondition`.
- Set owner references on objects you create so garbage collection cleans them up.
- Use server-side apply or `client.MergeFrom` patches. Avoid full `Update` on objects other controllers also write.
- Handle `apierrors.IsNotFound` and `IsConflict` explicitly.
- Keep RBAC markers (`+kubebuilder:rbac`) next to the reconciler and minimal.

## Sandboxes and gVisor

- Sandbox pods run with `runtimeClassName: gvisor`. Don't remove it to "fix" a workload; find out which syscall or feature gVisor doesn't support and record it in `docs/`.
- Pod security defaults: `runAsNonRoot: true`, `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true` where possible, drop all capabilities, `seccompProfile: RuntimeDefault`.
- Set CPU and memory requests and limits on every container.
- No `hostPath`, `hostNetwork`, `hostPID`, or privileged containers in sandbox workloads.
- Restrict sandbox egress with a NetworkPolicy. Only mcp-toolbox and explicitly allowed endpoints are reachable.

## Manifests

- Pin image tags to a version or digest, never `latest`.
- Keep secrets out of manifests. Reference `Secret` objects and document how to create them.
- Validate YAML with `kubectl apply --dry-run=server` against the kind cluster before calling it done.
