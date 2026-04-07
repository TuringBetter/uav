# Repository Guidelines

## Project Structure & Module Organization
This repository is a Go module (`uav`) for a three-layer UAV communication stack.

- `cmd/node/`: runnable example node entry point.
- `node/transport/`: transport abstractions and UDP/TCP/codec implementations.
- `node/runtime/`: node lifecycle, routing, peer management, and timers.
- `node/algorithm/`: pluggable algorithms such as `gossip`, `raft`, and `weaknet`.
- `node/config/`: shared configuration types.
- `pkg/message/`: core message model and wire-format helpers.

Keep new packages focused and colocate tests with the code they exercise, for example `node/runtime/node_test.go`.

## Build, Test, and Development Commands
- `go run ./cmd/node --id=1 --addr=:9001 --peers=2=:9002 --algo=gossip`: run a local node.
- `go test ./...`: run the full test suite across algorithms, runtime, transport, and message packages.
- `go test ./node/runtime ./node/transport/...`: iterate on framework or transport changes only.
- `gofmt -w cmd node pkg`: format all tracked Go sources before review.

Use `go test ./...` as the default pre-PR check.

## Coding Style & Naming Conventions
Follow standard Go formatting and import ordering; `gofmt` is the source of truth. Use tabs for indentation, exported identifiers in `CamelCase`, and short lowercase package names like `runtime`, `udp`, and `message`. Prefer descriptive constructor-style names such as `NewNode`, `DefaultConfig`, and `WithSendQueueSize`. Keep comments brief and explain protocol or weak-network behavior when the code is not obvious.

## Testing Guidelines
Tests use Go’s built-in `testing` package. Name files `*_test.go` and prefer `TestXxx` functions that describe the behavior under test, such as deduplication, TTL expiry, timer firing, and priority ordering. Add unit tests for new protocol rules and for weak-network edge cases like delay, loss, duplicates, or out-of-order delivery.

## Commit & Pull Request Guidelines
The visible history is minimal (`init`), so use short imperative commit subjects and keep them specific, for example `runtime: drop expired messages earlier`. Pull requests should include:

- a concise summary of the behavior change,
- linked issue or research task when applicable,
- test evidence (`go test ./...`), and
- sample CLI output or logs if the change affects node behavior.
