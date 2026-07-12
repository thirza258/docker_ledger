# PRD — Centralized Logs Collector (OpenTelemetry → Loki → Grafana)

**Status:** Draft
**Owner:** Backend / Platform
**Date:** 2026-07-12
**Component:** DockerLedger (Go 1.26, stdlib `net/http`, GORM, Docker client), deployed via Docker Compose

---

## 1. Summary

Stand up a centralized logging pipeline so all container logs are collected, stored,
and queryable with dashboards and alerting — complementing the existing Postgres-backed
log store with a purpose-built observability stack.

Target pipeline:

```
DockerLedger containers (backend, postgres, frontend, + user workloads)
   │  stdout/stderr
   ▼
OpenTelemetry Collector        ← receives, parses, batches, labels, routes
   │  Loki push / OTLP
   ▼
Loki                           ← stores logs (indexed by labels, LogQL query engine)
   │
   ▼
Grafana                        ← visualize + query (LogQL), dashboards, alerts
```

---

## 2. Problem / Motivation

### 2.1 What DockerLedger already has

The project already ships a capable log pipeline:

- **Log collection** — `backend/internal/collector/collector.go` tails every running
  container via the Docker API (`FollowContainerLogs`), decodes the multiplexed
  stream, and writes batches to Postgres (`LogRepository.BatchInsertLogs`).
- **Live streaming** — WebSocket handlers in `backend/internal/websocket/` push
  real-time log lines to the React frontend.
- **Search** — `GET /logs/search?q=...` performs `ILIKE` queries against the
  `log_entries` table with optional time-range and container filters.
- **AI summaries** — `GET /logs/summarize` and `/logs/summarize/container` pipe
  recent logs through OpenRouter for natural-language summaries.
- **OTel tracing** — `backend/internal/telemetry/telemetry.go` already wires up an
  OTLP trace exporter to an OpenTelemetry Collector.

### 2.2 Gaps

| Gap | Detail |
|-----|--------|
| **No dashboard** | The frontend shows live logs and search results but has no aggregated views (error rate, volume by container, trends over time). |
| **No alerting** | Nobody is notified when a container crashes, error rate spikes, or a known-bad pattern appears. |
| **Postgres as log store** | Works today but `ILIKE` over a growing `log_entries` table will degrade; no built-in retention, compaction, or indexed label queries. |
| **Unstructured app logs** | The backend uses Go's stdlib `log` package — plain text, no consistent fields (`level`, `request_id`, `service`). |
| **Logs lost on rotation** | Docker's local JSON log files rotate; only what the collector has already ingested into Postgres survives. No secondary retention tier. |
| **No cross-container correlation** | A request that touches backend → postgres → user container cannot be traced across services. |

---

## 3. Goals

| # | Goal | Success criteria |
|---|------|------------------|
| G1 | Add Loki + Grafana to the Docker Compose stack | `docker compose up` brings up `loki` and `grafana`; Grafana reachable at `127.0.0.1:3000` with Loki datasource pre-provisioned |
| G2 | Ship container logs to Loki | All container stdout/stderr visible in Grafana Explore via LogQL |
| G3 | Structured, queryable app logs | Backend emits JSON logs with `level`, `service`, `request_id` fields |
| G4 | Request correlation | Each HTTP request gets a `request_id`; filterable across all log lines for that request |
| G5 | Dashboards + alerting | Starter dashboard (error rate, volume by container, recent errors) + alert rule on error spikes |
| G6 | Coexist with existing Postgres log store | The current collector, search, AI summaries, and WebSocket streaming continue to work unchanged |

---

## 4. Non-Goals

- **Replacing the existing log collector** — the Postgres-backed collector, search API,
  AI summaries, and WebSocket streaming are valuable and stay. Loki is additive.
- **Distributed tracing beyond the existing OTel setup** — the tracing skeleton in
  `telemetry.go` is kept; this PRD covers **logs only**.
- **Metrics (Prometheus/Mimir)** — out of scope for this phase.
- **Long-term/cold storage** — start with local filesystem retention on Loki;
  S3-backed storage is a future option.
- **Multi-tenant Loki or HA cluster** — single-binary mode, single host.
- **Replacing the frontend log viewer** — the React dashboard continues to use the
  existing WebSocket + search APIs; Grafana is an additional ops-facing tool.

---

## 5. Architecture

### 5.1 New Docker Compose services

All new services join the existing `dockerledger` bridge network so they can reach
the `backend` container and each other. Grafana is the only new port exposed to the
host (bound to `127.0.0.1`).

| Service | Image | Role | Exposed |
|---------|-------|------|---------|
| `otel-collector` | `otel/opentelemetry-collector-contrib` | Receive/parse/batch/route logs | internal only |
| `loki` | `grafana/loki` | Store + index logs, LogQL engine | internal only |
| `grafana` | `grafana/grafana` | Visualize + query + alert | `127.0.0.1:3000` |

> **Note:** The project's `telemetry.go` already references `otel-collector:4317` as
> the default OTLP endpoint. Adding an `otel-collector` service to Compose makes the
> existing tracing path work end-to-end without config changes.

### 5.2 How logs get to the Collector

Two viable approaches — **Option A is recommended** for lowest change.

**Option A — Collector scrapes container stdout (recommended to start)**

- The OTel Collector uses the `filelog` receiver to tail Docker's JSON log files
  (`/var/lib/docker/containers/*/*-json.log` on the host).
- Mount `/var/lib/docker/containers` into the collector container (read-only).
- No app-code changes required. Captures logs from **all** containers
  (`dockerledger-backend`, `dockerledger-postgres`, `dockerledger-frontend`, and any
  user containers managed through the dashboard).
- The existing `LogCollector` in `backend/internal/collector/` continues to feed
  Postgres independently — two consumers of the same log stream, no conflict.

**Option B — App pushes via OTLP (OTel SDK)**

- Instrument the Go backend with the OpenTelemetry log SDK and an OTLP log exporter
  pointing at `otel-collector:4317` (gRPC) or `:4318` (HTTP).
- The project already imports `go.opentelemetry.io/otel/*` for tracing, so adding
  the log bridge is a small dependency bump.
- Richer attributes (request ID, user context) but requires wiring in `cmd/api/main.go`
  and every log call site.

> **Decision: Start with Option A** (scrape stdout). Adopt Option B later when
> structured log attributes and trace-log correlation are needed.

### 5.3 Collector responsibilities

1. **Receive** — `filelog` receiver (Option A) or `otlp` receiver (Option B).
2. **Process** — parse JSON, promote fields to attributes, set labels
   (`service`, `level`, `container`), batch, add resource attributes,
   drop noisy lines (e.g. Docker health-check probes, GORM `record not found`).
3. **Export** — `loki` exporter to `http://loki:3100`.

---

## 6. App-side changes (DockerLedger backend)

### 6.1 Structured logging

Replace ad-hoc stdlib `log` usage with a structured logger emitting JSON to stdout.
Go 1.26 ships `log/slog` in the stdlib — no new dependency required.

- Configure a package-level `slog.Logger` with `slog.NewJSONHandler(os.Stdout, ...)`.
- Standard fields on every line: `time`, `level`, `msg`, `service`, `request_id`.
- Migrate call sites incrementally. Key sites to convert:
  - `backend/cmd/api/main.go` — server start, shutdown, Docker client init, WakeProxy
    lifecycle, DB connection, log collector lifecycle
  - `backend/internal/database/postgres.go` — GORM connect logs
  - `backend/internal/collector/collector.go` — collector attach/detach/error events,
    batch insert failures
  - `backend/internal/wakeproxy/` — proxy start/stop, upstream errors
  - `backend/internal/handlers/` — request-level errors
- Keep `log.Fatal` for genuine startup-abort paths, but route through slog first so
  the fatal message is JSON-formatted.

### 6.2 Request ID middleware

Add a lightweight middleware (wrapping `http.DefaultServeMux` or applied per-handler)
that:

- Reads incoming `X-Request-ID` header or generates a UUID (or short random ID).
- Stores it in the request context.
- Sets it on the response header.
- Makes it available to handlers/services so log lines can include `request_id`.

Since DockerLedger uses stdlib `net/http` (not Gin), the middleware wraps `http.Handler`
with the standard `func(http.Handler) http.Handler` pattern.

Optionally add a small structured access-log middleware so HTTP access logs are JSON
with `request_id`, `status`, `latency_ms`, `method`, `path`.

### 6.3 GORM logging

Route GORM's logger through `slog` and set `IgnoreRecordNotFoundError: true` so the
benign `record not found` lines (e.g. first-time lookups in `log_repository.go` and
`container_repository.go`) stop polluting error-level logs.

---

## 7. Loki & Grafana configuration

- **Loki**: single-binary mode, filesystem storage, retention (e.g. 14–30 days).
  Indexed **labels** kept low-cardinality: `service`, `level`, `container`,
  `env`. High-cardinality values (`request_id`, `user_id`) live in the **log line**
  (JSON), filtered at query time — not as labels (Loki best practice).
- **Grafana**: provision Loki as a datasource automatically; ship one starter
  dashboard (error rate, log volume by container, recent errors table) and one alert
  rule (error rate threshold → notification channel).

### Example LogQL queries

```logql
# All backend errors in the last hour
{service="dockerledger-backend", level="error"}

# Every log line for one request (request_id lives in the JSON line)
{service="dockerledger-backend"} | json | request_id="abc-123"

# Postgres error logs
{container="dockerledger-postgres"} |= "ERROR"

# Error rate per minute (for alerting)
sum(rate({service="dockerledger-backend", level="error"} [1m]))

# Log volume by container (dashboard panel)
sum by (container) (rate({container=~".+"} [5m]))
```

---

## 8. Noise & cost controls

- Drop or downgrade known-benign lines at the Collector:
  - Docker health-check probes to `/health/docker`
  - GORM `record not found` (already handled correctly in the app, but double-filter
    at the collector)
  - Frontend nginx access logs for static assets (optional)
- Batch + compress before export to Loki.
- Cap retention on Loki (start with 14 days); monitor disk usage of the Loki volume.
- Keep label cardinality low (see §7) to avoid Loki index blowup.
- The existing Postgres `log_entries` table can also get a retention policy (e.g.
  `DELETE FROM log_entries WHERE timestamp < NOW() - INTERVAL '7 days'` in a cron
  or background goroutine).

---

## 9. Implementation plan

| Phase | Work | Verify |
|-------|------|--------|
| P0 | Add `loki`, `grafana`, `otel-collector` to `docker-compose.yml` on the `dockerledger` network; provision Grafana datasource; mount Docker logs path into collector | `docker compose up`; Grafana at `127.0.0.1:3000`, Loki datasource green, Explore shows container logs |
| P1 | Collector `filelog` config: tail Docker JSON logs, parse, set `container`/`service` labels, export to Loki | `{container=~".+"}` returns lines from all running containers in Grafana Explore |
| P2 | App: introduce `slog` JSON logger in `backend/cmd/api/main.go`; migrate startup + collector + handler log sites | Backend logs appear as parsed JSON in Loki with `level`/`service` labels |
| P3 | Add request-ID middleware + structured access log | `{service="dockerledger-backend"} \| json \| request_id="X"` returns all lines for one request |
| P4 | Starter dashboard (error rate, volume by container, recent errors) + error-rate alert rule | Trigger a test error; alert fires; dashboard shows it |
| P5 | Noise filtering + retention tuning on both Loki and Postgres `log_entries` | Health-check lines suppressed; disk stable over several days |

Each phase is independently shippable; P0–P1 already deliver centralized logs in
Grafana with zero app-code change.

---

## 10. Risks & mitigations

| Risk | Mitigation |
|------|------------|
| Loki disk fills up | Retention limit + low-cardinality labels + disk alert in Grafana |
| Grafana exposed publicly | Bind to `127.0.0.1` only (same pattern as `backend`); access via SSH tunnel or reverse proxy with auth |
| Log volume on small host | Batch, filter noise, drop health-check lines at collector |
| Secrets leaking into logs | Never log passwords/tokens; scrub known fields at Collector; `.env` already holds `OPENROUTER_API_KEY` and DB creds — audit log sites to ensure these are never emitted |
| Collector scraping wrong paths | Pin Docker log path; validate with `docker compose up` on a dev machine first |
| Collector conflicts with existing `telemetry.go` | The existing tracer already points at `otel-collector:4317`; the new collector service simply needs an OTLP receiver on that port. Tracing and logging share the same collector process. |
| Breaking existing log features | The `LogCollector` → Postgres path, WebSocket streaming, search API, and AI summaries are untouched — Loki is an additional consumer, not a replacement |

---

## 11. Open questions

1. **Host resources** — is there headroom on the deploy host for Loki + Grafana +
   Collector alongside the existing `postgres` + `backend` + `frontend` containers?
2. **Retention** — how many days of logs in Loki? How many days in Postgres
   `log_entries`? Should they differ (Loki longer, Postgres shorter for recent
   search/summaries)?
3. **Access** — who needs Grafana access, and via what auth (built-in users / SSO /
   reverse proxy)?
4. **Env split** — one Loki for all environments (labeled by `env`) or separate
   instances for staging vs prod?
5. **Option A vs B** — is stdout scraping sufficient long-term, or do we want OTLP
   SDK instrumentation so trace IDs and log lines correlate automatically?
6. **Postgres log retention** — should the existing `log_entries` table get a TTL
   cleanup (background goroutine in the collector, or a Postgres cron)?

---

## 12. Appendix — how this fits the existing stack

DockerLedger already has a working log pipeline:

```
Containers ──► Docker API ──► LogCollector ──► Postgres (log_entries)
                         │
                         └──► WebSocket ──► React frontend (live view)
                         
Postgres ──► Search API ──► React frontend (search view)
Postgres ──► AI Summary ──► React frontend (summary view)
```

This PRD adds a parallel observability path:

```
Containers ──► Docker JSON log files ──► OTel Collector ──► Loki ──► Grafana
                                                                     │
                                                              Dashboards
                                                              Alerts
                                                              Explore / LogQL
```

The existing pipeline continues to serve the React dashboard (live logs, search,
AI summaries). The new pipeline serves ops use cases (dashboards, alerting, ad-hoc
LogQL exploration). Both consume the same container stdout/stderr — no conflict.

### Why OpenTelemetry in the middle

The Collector decouples the app from the backend. DockerLedger already has an OTel
tracing setup in `backend/internal/telemetry/telemetry.go` pointing at
`otel-collector:4317`. Adding the collector service to Compose makes the existing
tracing path functional, and the same collector instance can handle logs. If we
later swap Loki for Elasticsearch, Grafana Cloud, or add metrics, only the
Collector's exporter config changes.