---
paths:
  - "**/*_test.go"
  - "**/testdata/**"
---

# Go testing

- Write tests first for new behaviour and bug fixes. A bug fix starts with a test that fails.
- Default to table-driven tests with `t.Run(tc.name, ...)`. Name cases by behaviour, not by input.
- Use the standard `testing` package. `github.com/google/go-cmp/cmp` for struct diffs is fine. Avoid assertion frameworks unless the module already uses one.
- Mark helpers with `t.Helper()`. Use `t.Cleanup`, `t.TempDir`, `t.Setenv`, and `t.Context()` instead of manual teardown.
- Call `t.Parallel()` unless the test touches global state or env vars.
- Always run with `-race`: `go test -race ./...`.
- Fixtures go in `testdata/`. Golden files get an `-update` flag.
- Fake at the boundary you own. For Kubernetes use `sigs.k8s.io/controller-runtime/pkg/client/fake` or `envtest`; don't mock client-go internals.
- Integration tests that need a kind cluster use the `//go:build e2e` tag and live under `test/e2e/`. Unit tests must run without a cluster or network.
- No `time.Sleep` for synchronization. Poll with `wait.PollUntilContextTimeout` or Gomega's `Eventually`.
- Aim for 80%+ coverage on packages with logic. Check with `go test -cover ./...`.
