# Backend tests

Every module that emits log lines has a test asserting those lines are actually
produced — at the right level, with the attributes Loki and the Grafana
dashboards query (`request_id`, `trace_id`, `container_id`, …).

## Running

```bash
cd backend
go test ./...            # everything that needs no external services
go test -race ./...      # same, with the race detector
```

The suite needs **no Docker daemon and no database**. Modules that talk to
Docker pin `DOCKER_HOST` in a `TestMain` — either to a closed port or to a fake
daemon served by `httptest` — so results do not change depending on whether
Docker happens to be running locally. Repository failure paths run against a
GORM handle pointed at a closed port (`internal/testsupport.DeadDB`).

### Tests that need a live Postgres

The success-path log line in `internal/storage` (`log retention cleanup`, which
only fires when rows were actually deleted) needs a real database. Those tests
skip unless `DL_TEST_DSN` is set:

```bash
docker run --rm -d -e POSTGRES_PASSWORD=testpw -e POSTGRES_DB=dockerledger \
  -p 55432:5432 --name dl-test-pg postgres:16-alpine

DL_TEST_DSN='host=localhost port=55432 user=postgres password=testpw dbname=dockerledger sslmode=disable' \
  go test ./internal/storage/ -count=1

docker rm -f dl-test-pg
```

They clean up the rows they insert.

## Known failure — one real bug the tests found

`go test ./...` exits non-zero for exactly one failing test:
`TestRecordActivityDoesNotDeadlock` in `internal/wakeproxy`.

`ManagedService.RecordActivity` takes `s.mu` and then calls `resetIdleTimer`,
which takes `s.mu` again. Go mutexes are not reentrant, so the goroutine blocks
forever, and `ProxyHandler.ServeHTTP` calls it on every proxied request once a
service is running. The fix is an unlocked `resetIdleTimerLocked` that
`RecordActivity` calls while already holding the lock.

It is latent rather than live: `config.Load()` never assigns `cfg.Wakeproxy` —
nothing parses `backend/wakeproxy.yaml` — so `main.go`'s `if cfg.Wakeproxy != nil`
guard means the proxy never starts today. It becomes a hang on every proxied
request the moment that config is wired up.

The test is left failing rather than skipped so the bug is not lost.

## Writing a new log assertion

`internal/telemetry/logtest` captures both log sinks — the `telemetry.Logger`
package variable and the `slog` default — at debug level, and decodes each JSON
line:

```go
func TestSomethingLogs(t *testing.T) {
    rec := logtest.Capture(t)      // do not call t.Parallel(): both sinks are global

    doTheThing()

    rec.RequireLevel("ERROR", "failed to do the thing")
    rec.RequireAttrs("failed to do the thing", map[string]any{
        "container_id": "abc123",
        "error":        nil,       // nil asserts presence only
    })
    rec.RequireAbsent("succeeded")
}
```

Capture installs the handler at **debug** level on purpose — the WebSocket path
handling and the GORM query tracer log at debug, and the production default of
info would drop them and read as "never emitted".

## Coverage by module

| Module | What is asserted | External deps |
|---|---|---|
| `telemetry` | `LOG_LEVEL` parsing, JSON envelope, `request_id` / `trace_id` / `span_id` correlation | none |
| `middleware` | one `http request` line per request with method/path/status/latency, request-id propagation, `Hijack`/`Flush` forwarding (WebSocket upgrades depend on it) | none |
| `database` | the GORM→slog adapter: levels, `gorm: ` prefix, record-not-found suppression, query tracing, request/trace id propagation | none |
| `handlers` | every error and warn line on the container, logs, search and AI endpoints; client-side rejections stay silent | fake Docker daemon |
| `services` | the log pipeline itself: Docker multiplexed-stream decoding, the JSON envelope of the live stream, AI prompt building | fake Docker daemon |
| `collector` | batch-insert failure, retention failure and stop, attach failures, event-stream failure, shutdown | none (Docker pinned to a closed port) |
| `storage` | failures surface as errors rather than log lines; the retention summary line | Postgres for success paths |
| `websocket` | invalid path, missing container id, upgrade failure, the debug trail, request-id propagation, chunk decoding | none |
| `wakeproxy` | unknown host, deactivated service, start failure, nil target, and all admin service lines | none |
| `config` | the missing-`.env` warning, defaults and overrides | none |

`cmd/api/main.go` is not covered: its ~20 log lines are startup and shutdown
lines that need the process running end to end.

Also uncovered, for want of a fake daemon in those packages: the collector's
`detected new container` / `log stream ended`, and the four container-startup
lines in `wakeproxy/service.go`. The collector's `invalid log message` line is
unreachable — `FollowContainerLogs` only ever emits JSON it marshalled itself.
