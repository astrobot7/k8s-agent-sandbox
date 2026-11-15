---
paths:
  - "**/*.go"
  - "go.mod"
  - "Makefile"
---

# Project layout

Use this layout as code arrives. Don't create empty directories ahead of need.

```
cmd/<binary>/main.go   thin main: parse flags, build deps, call run(ctx)
internal/...           all non-exported packages (default home for code)
api/<version>/         CRD Go types, if we define any
config/                kustomize bases: CRDs, RBAC, manager
deploy/kind/           kind cluster config, gVisor RuntimeClass, setup manifests
test/e2e/              e2e tests against kind (build tag e2e)
hack/                  dev scripts
docs/                  see docs/README.md
```

- Put code in `internal/` unless another module needs to import it. Don't add `pkg/` speculatively.
- One module at the repo root. Don't add nested `go.mod` files without an ADR.
- `main` does wiring only. Business logic lives in `internal/` where it can be tested.
- Add a dependency only when the stdlib or existing deps can't do the job reasonably. Run `go mod tidy` after any change.
- Expose common tasks as `make` targets (`build`, `test`, `lint`, `kind-up`, `kind-down`) and list them in `CLAUDE.md`.
