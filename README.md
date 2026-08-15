# DockerLedger

DockerLedger is a container operations dashboard built around Docker, Postgres, Go, and React. It lets you inspect running containers, view stats and logs, search historical logs, generate AI summaries of log activity, and manage WakeProxy-style services that can start containers on demand.

## What It Does

- Lists Docker containers and shows their state, image, ID, and basic stats.
- Streams live logs over WebSockets and keeps a searchable log history in Postgres.
- Generates AI summaries of recent logs through OpenRouter.
- Exposes WakeProxy administration for host-based reverse proxy services.
- Ships as a Docker Compose stack with a Go API, React frontend, and Postgres database.

## Stack

- Backend: Go 1.26.3, GORM, Docker client, Gorilla WebSocket
- Frontend: React 19, Vite, Tailwind CSS 4
- Database: Postgres 16
- Runtime: Docker Compose

## Repository Layout

- `backend/` - Go API, Docker integration, storage, telemetry, and WakeProxy code.
- `frontend/` - React app for the dashboard UI.
- `docker-compose.yml` - Local multi-container stack.

## Requirements

- Docker and Docker Compose
- Access to the Docker daemon socket from the backend
- Postgres 16 if you run the backend without Compose
- An OpenRouter API key if you want AI summaries

## Quick Start

1. Set up your environment variables.
2. Start the stack with Docker Compose.
3. Open the frontend in your browser.

```bash
docker compose up --build
```

By default:

- Frontend: `http://localhost:3000`
- Backend API: `http://localhost:8080`
- Postgres: `localhost:5432`

## Local Development

### Backend

```bash
cd backend
go run ./cmd/api
```

The backend expects:

- `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME`, `DB_SSLMODE`
- `SERVER_PORT`
- `DOCKER_HOST`
- `OPENROUTER_API_KEY` for AI summaries
- `OPENROUTER_MODEL` if you want to override the default model

### Frontend

```bash
cd frontend
npm install
npm run dev
```

The Vite dev server proxies `/api` and `/ws` to `http://localhost:8080`, so the frontend can talk to the backend without a separate reverse proxy.

## Docker Compose

The Compose file starts:

- `postgres` on port `5432`
- `backend` on port `8080`
- `frontend` on port `3000`
- `otel-collector` - internal only, receives traces and scrapes container logs
- `loki` - internal only, stores logs
- `grafana` on `127.0.0.1:3030`

The backend container mounts `/var/run/docker.sock` so it can inspect containers, collect logs, and support WakeProxy-managed services when enabled.

## Observability

The stack ships with an OpenTelemetry pipeline alongside the Postgres log store. The two are independent: the Postgres collector still feeds live streaming, search, and AI summaries.

**Traces.** The backend exports OTLP/gRPC spans to `otel-collector:4317`. Instrumented out of the box:

- Inbound HTTP requests (`otelhttp`), excluding health probes and WebSocket streams
- Every SQL statement (`gorm.io/plugin/opentelemetry`), as a child of the request span
- Outbound Docker API calls

Tracing degrades gracefully: if the collector is unreachable the API still serves, and spans are dropped.

**Logs.** The collector tails Docker's JSON log files, parses the backend's structured output, and pushes to Loki with `container_id`, `level`, and `service_name` as labels. Grafana is pre-provisioned with the Loki datasource, a starter dashboard, and error-rate alert rules.

**Correlation.** Every backend log line emitted inside a request carries `request_id`, `trace_id`, and `span_id`, so one request can be followed across log lines and matched to its trace:

```logql
{service_name="dockerledger-backend"} | json | request_id="abc123"
```

Point `OTEL_EXPORTER_OTLP_ENDPOINT` at a tracing backend (Tempo, Jaeger) — or add one as a collector exporter — to view traces; the collector currently logs trace batches rather than storing them.

## Environment Variables

### Database

- `DB_HOST` - database host, default `localhost`
- `DB_PORT` - database port, default `5432`
- `DB_USER` - database user, default `postgres`
- `DB_PASSWORD` - database password
- `DB_NAME` - database name, default `dockerledger`
- `DB_SSLMODE` - Postgres SSL mode, default `disable`

### Server

- `SERVER_PORT` - backend HTTP port, default `8080`
- `DOCKER_HOST` - Docker daemon endpoint, default `unix:///var/run/docker.sock`
- `LOG_LEVEL` - `debug`, `info`, `warn`, or `error`, default `info`

### Observability

- `OTEL_SERVICE_NAME` - service name on spans and logs, default `dockerledger-backend`
- `OTEL_EXPORTER_OTLP_ENDPOINT` - collector endpoint, default `otel-collector:4317`
- `ENV` - environment attribute on spans, default `dev`
- `GRAFANA_PORT` - host port for Grafana, default `3030`
- `GRAFANA_USER` / `GRAFANA_PASSWORD` - Grafana admin credentials, default `admin`/`admin`

### AI Summary

- `OPENROUTER_API_KEY` - required for AI summaries
- `OPENROUTER_MODEL` - model name, default `google/gemma-4-26b-a4b-it`


## API Surface

### Containers

- `GET /health/docker` - Docker daemon health check
- `GET /containers` - list containers
- `GET /containers/{id}` - inspect a container
- `GET /containers/{id}/stats` - fetch container stats
- `GET /containers/{id}/logs?tail=100` - fetch recent logs

### Log Search and AI

- `GET /logs/search?q=...` - search historical logs
- `GET /logs/summarize?hours_back=24&limit=2000` - summarize recent logs
- `GET /logs/summarize/container?container_name=...` - summarize one container

### WakeProxy

- `GET /wakeproxy/services`
- `POST /wakeproxy/services`
- `POST /wakeproxy/services/{name}/activate`
- `POST /wakeproxy/services/{name}/deactivate`

### WebSockets

- `GET /ws/containers/{id}/logs/live?tail=100` - live log stream

## WakeProxy Notes

The backend includes a WakeProxy manager that:

- Maps hostnames to containers.
- Starts a container when traffic arrives for its host.
- Builds a reverse proxy to the container IP and port.
- Stops idle services after the configured timeout.

`backend/wakeproxy.yaml` shows the expected configuration shape for WakeProxy services.

## Behavior Notes

- The backend auto-migrates database tables on startup.
- The log collector follows running containers and stores logs in Postgres.
- The UI has separate views for container management and WakeProxy service management.
- AI summaries are generated from recent logs and depend on OpenRouter being configured.

