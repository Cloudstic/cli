# Contributing to Cloudstic

Welcome! We appreciate your help in making Cloudstic better.

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
