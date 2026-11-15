KIND_CLUSTER ?= agent-sandbox
KIND_CONTEXT := kind-$(KIND_CLUSTER)
KUBECTL := kubectl --context $(KIND_CONTEXT)

.PHONY: gvisor kind-up kind-verify kind-down

## gvisor: download pinned gVisor binaries into .cache/gvisor
gvisor:
	./hack/install-gvisor.sh

## kind-up: create the 1 control-plane + 2 worker cluster with gVisor (safe to re-run)
kind-up:
	KIND_CLUSTER=$(KIND_CLUSTER) ./setup.sh preflight gvisor cluster nodes runtimeclass

## kind-verify: run a pod under gVisor and check its boot log
kind-verify:
	KIND_CLUSTER=$(KIND_CLUSTER) ./setup.sh verify

## kind-down: delete the cluster
kind-down:
	kind delete cluster --name $(KIND_CLUSTER)
