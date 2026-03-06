# Contributing to Cloudstic

Welcome! We appreciate your help in making Cloudstic better.

## Testing

```bash
# Run all tests (unit + hermetic e2e)
go test -v -race -count=1 ./...

# Run a single test
go test -v -run TestName ./path/to/package

# Run the full check script (fmt + lint + test + coverage)
./scripts/check.sh
```

### E2E test modes

E2E tests live in `e2e/` and are controlled by the `CLOUDSTIC_E2E_MODE` environment variable:

- `hermetic` (default) — spins up local dependencies via Testcontainers (MinIO, SFTP). Requires Docker.
- `live` — runs against real cloud vendor APIs. Requires secrets to be configured.
- `all` — runs both hermetic and live.

Hermetic tests are automatically skipped if `/var/run/docker.sock` is not available, so they are safe to run in environments without Docker.

### What to test

- Add tests for any new public API methods on `Client`.
- Test both success and error paths.
- Use the mock store in `internal/engine/mock_test.go` to unit test engine logic without a real backend.
- If your change touches encryption or compression, add a round-trip test.

## Profiling

If you are a developer or need to troubleshoot performance issues, you can generate standard Go profiles using the following hidden flags on any command:

- `-cpuprofile <file>`: Writes a CPU profile to the specified file.
- `-memprofile <file>`: Writes a memory heap profile to the specified file.

Example:

```bash
cloudstic backup -source local -source-path ./data -cpuprofile cpu.prof -memprofile mem.prof
go tool pprof -http=:8080 cpu.prof
```

The CPU profile flag will also automatically generate a goroutine dump (`<file>.goroutine`), a block profile (`<file>.block`), and a mutex profile (`<file>.mutex`) to help identify concurrency bottlenecks.

## Debugging

You can enable verbose internal logging by appending the `-debug` flag to any command. This is extremely useful for tracing API calls, caching behaviors, and engine operations in real time.

Example:

```bash
cloudstic backup -source local -source-path ./data -debug
```

The output will include detailed timings for every `GET`, `PUT`, `LIST`, and `DELETE` operation to the underlying storage backend, as well as cache hits/misses and memory management decisions within the engine.
