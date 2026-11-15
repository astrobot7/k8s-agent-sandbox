---
paths:
  - "**/*.go"
  - "go.mod"
---

# Go style

## Formatting and tooling

- Code must pass `gofmt`, `goimports`, `go vet`, and `golangci-lint run`. Run them before calling a change done.
- Group imports: standard library, third party, then this module. `goimports -local <module>` does it.
- Target the Go version in `go.mod`. Use modern stdlib (`slices`, `maps`, `min`/`max`, `log/slog`, range-over-int) instead of hand-rolled helpers.

## Naming

- Short, lower-case package names with no underscores. No `util`, `common`, `helpers`.
- Don't stutter: `sandbox.Client`, not `sandbox.SandboxClient`.
- Initialisms keep one case: `ID`, `URL`, `HTTP`, `MCP`, `apiURL`.
- Receivers are one or two letters and consistent across methods. Never `this` or `self`.
- Getters drop `Get`: `Name()`, not `GetName()` (generated k8s code excepted).

## Design

- Accept interfaces, return concrete types. Define interfaces in the package that consumes them, and keep them small.
- Pass `context.Context` as the first parameter of anything that does I/O or may block. Never store it in a struct.
- Make the zero value useful where possible. Otherwise provide a `New...` constructor and validate there.
- Use functional options only when a constructor has several optional settings. A plain config struct is fine otherwise.
- No package-level mutable state and no `init()` side effects beyond registering schemes.
- Prefer early returns over nested `else`.

## Errors

- Wrap with context: `fmt.Errorf("create sandbox %q: %w", name, err)`. Lower-case, no trailing punctuation, no "failed to".
- Check with `errors.Is` / `errors.As`, never by comparing strings.
- Handle an error once: log it or return it, not both.
- Declare sentinel errors as `var ErrX = errors.New(...)` when callers need to branch on them.
- `panic` only for programmer bugs during init. Never in request or reconcile paths.

## Logging

- Use `log/slog` (or the controller-runtime `logr` logger in controllers) with structured key/value pairs.
- Never log secrets, tokens, kubeconfigs, or full request bodies.

## Comments

- Every exported identifier gets a doc comment that starts with its name.
- Comments explain why. Delete comments that restate the code.
