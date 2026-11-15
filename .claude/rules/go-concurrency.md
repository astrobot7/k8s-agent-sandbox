---
paths:
  - "**/*.go"
---

# Go concurrency

- Every goroutine has an owner who knows how it stops. Tie its lifetime to a `context.Context` and wait for it on shutdown.
- Prefer `golang.org/x/sync/errgroup` over raw `sync.WaitGroup` plus error channels.
- The sender closes a channel, never the receiver. Don't close a channel that has more than one sender.
- Protect shared state with a `sync.Mutex` next to the fields it guards, and say which fields in a comment. Don't copy structs that contain a mutex.
- Use buffered channels only with a stated reason for the size.
- Every blocking `select` includes a `case <-ctx.Done():`.
- Set timeouts on outbound calls (`http.Client.Timeout`, `context.WithTimeout`). Never use `http.DefaultClient` for remote calls.
- Use `time.NewTicker`/`time.NewTimer` and stop them. Avoid `time.After` inside loops.
