# Webhook Catcher API (Go + Docker + Render)

API para captura e inspeção de webhooks, com rotas administrativas, endpoint público de captura e persistência SQL.

## Stack
- Go 1.24+
- `net/http` (API REST)
- SQLite via `modernc.org/sqlite` (padrão para deploy simples)
- Docker (deploy)
- Render (runtime)

## Rotas principais
- `POST /v1/inboxes`
- `GET /v1/inboxes`
- `GET /v1/inboxes/{id}`
- `PATCH /v1/inboxes/{id}`
- `DELETE /v1/inboxes/{id}`
- `ANY /hook/{token}`
- `GET /v1/inboxes/{id}/requests`
- `GET /v1/inboxes/{id}/requests/{request_id}`
- `GET /health`
- `GET /ready`
- `POST /internal/retention/run`

## Variáveis de ambiente
- `PORT` (default `8080`)
- `DATABASE_URL` (vazio = memória; ex: `file:/app/data/webhook_catcher.db?_pragma=busy_timeout(5000)`)
- `MIGRATIONS_DIR` (default `migrations`)
- `MIGRATIONS_AUTO_APPLY` (default `true`)
- `PUBLIC_BASE_URL`
- `MAX_PAYLOAD_BYTES` (default `1048576`)
- `READ_TIMEOUT_SECONDS` (default `10`)
- `WRITE_TIMEOUT_SECONDS` (default `10`)
- `DEFAULT_RETENTION_DAYS` (default `30`)
- `ADMIN_RATE_LIMIT_RPM` (default `120`)
- `REQUIRE_ACCESS` (default `true`)
- `ACCESS_AUDIENCE`
- `CRON_SECRET`
- `CORS_ALLOWED_ORIGINS`
- `CORS_ALLOWED_METHODS`
- `CORS_ALLOWED_HEADERS`
- `CORS_MAX_AGE_SECONDS`

## Executar localmente
Sem `DATABASE_URL`, roda com store em memória:

```bash
go run ./cmd/local
```

Com SQLite local:

```bash
DATABASE_URL="file:webhook_catcher.db?_pragma=busy_timeout(5000)" go run ./cmd/local
```

## Testes
```bash
go test ./...
```

## Docker
Build:

```bash
docker build -t webhook-catcher-api .
```

Run:

```bash
docker run --rm -p 10000:10000 \
  -e PORT=10000 \
  -e REQUIRE_ACCESS=false \
  -e DATABASE_URL="file:/app/data/webhook_catcher.db?_pragma=busy_timeout(5000)" \
  webhook-catcher-api
```

## Deploy no Render
- O projeto inclui `render.yaml` para deploy como Web Service Docker.
- No free tier, o filesystem é efêmero (SQLite não persiste entre recriações).
- Para persistência real, use PostgreSQL gerenciado ou plano com disk persistente.

## Migrations
- `migrations/0001_init.sql`
- `migrations/0002_indexes.sql`
