# Flash Sale Inventory System

Take-home assignment demo: Go + Gin + PostgreSQL backend and React + Vite + TypeScript dashboard.

## Features

- Atomic stock reservation using PostgreSQL row locks and conditional `UPDATE`.
- Five-minute reservations and a periodic expiry worker.
- Reservation confirmation and inventory decrement in one database transaction.
- Idempotent reservation requests using an idempotency key backed by a PostgreSQL unique partial index.
- Repeated requests with the same key and payload return the existing reservation; reusing a key with a different payload returns `409 Conflict`.
- Inventory status endpoint and consistent JSON errors.
- Request context timeouts, HTTP graceful shutdown, and configurable database connection pool limits.
- React dashboard with polling, reservation form, countdown, confirmation, and error states.
- Automated tests covering request validation, concurrent reservations, expiry, confirmation, and idempotency.

## Requirements

- Docker Desktop / Docker Engine with Docker Compose.
- Go 1.23+ (only for running or testing the backend outside Docker).
- Node.js 20+ and npm (only for running the frontend outside Docker).

## Run everything

From the project root:


docker compose up --build


- Dashboard: http://localhost:5173
- API: http://localhost:8080
- Health check: http://localhost:8080/healthz

Compose runs PostgreSQL, the Go backend, and the React + Vite development server. The SQL initialization scripts create the tables, apply the reservation idempotency migration, and seed `item_4021` with 100 units plus two other demo items.

PostgreSQL initialization scripts run only when the data directory is first initialized. Adding or changing an initialization script does not automatically apply it to an existing database volume. Apply subsequent schema changes through the appropriate migration procedure.

For production deployment, build the frontend static assets and serve them from a production web server or CDN instead of using the Vite development server.

### Database persistence and reset warning

PostgreSQL data persists in the Compose volume across container restarts.

**Do not run `docker compose down -v` unless you intentionally want to delete the local PostgreSQL volume and its data.** For a normal restart, use:


docker compose restart


To stop the services without deleting persisted database data:


docker compose down


## Run frontend separately (optional)

Start PostgreSQL and the backend first, then run:


cd frontend
npm install
npm run dev


The frontend uses `VITE_API_BASE_URL` to configure the API URL. For local development, the default is `http://localhost:8080`. Override it by creating `frontend/.env.local` if needed:

##env
VITE_API_BASE_URL=http://localhost:8080


Restart the Vite development server after changing environment variables.

## Run backend locally (optional)

Start PostgreSQL:


docker compose up -d postgres


Then run the backend from another terminal:


cd backend
go mod tidy

DATABASE_URL='postgres://inventory:inventory_dev_password@localhost:5432/inventory?sslmode=disable' \
APP_ADDR=':8080' \
go run ./cmd/server


Ensure the database schema migrations have been applied before starting the backend. The `DATABASE_URL` above targets the local development database, not the test database.

## API examples

### Reserve 2 units

Every reservation request must include an `idempotency_key`. Generate a new unique key for each logically new reservation, and reuse the same key when retrying that reservation after a timeout or an ambiguous network/commit outcome.


curl -i http://localhost:8080/api/v1/inventory/reserve \
  -H 'Content-Type: application/json' \
  -d '{
    "idempotency_key": "demo-reserve-001",
    "user_id": "usr_9981",
    "item_id": "item_4021",
    "quantity": 2
  }'


A new reservation returns `201 Created`. Retrying the same request with the same key and payload returns the existing reservation rather than allocating stock a second time. Reusing that key with a different payload returns `409 Conflict`.

The example key above is for demonstration. If you already used it with different data, choose a new key or reuse the original payload.

### Confirm a reservation

Replace the placeholder with the `reservation_id` returned by the reserve endpoint:


curl -i http://localhost:8080/api/v1/inventory/confirm \
  -H 'Content-Type: application/json' \
  -d '{"reservation_id":"res_REPLACE_WITH_ID"}'


A reservation can only be confirmed while active and unexpired. A repeated confirmation is not treated as a successful new confirmation; the API returns a conflict response.

### Check stock


curl -i 'http://localhost:8080/api/v1/inventory/stock?item_id=item_4021'


## Tests

Run backend tests from the backend directory:


cd backend
go test -race -count=1 -v ./...


Tests requiring PostgreSQL use `TEST_DATABASE_URL`. Configure it to point to a dedicated test database named `inventory_test`, with the required schema migrations applied.

For example, with the Compose PostgreSQL service running and the test database already created and migrated:


cd backend

TEST_DATABASE_URL='postgres://inventory:inventory_dev_password@localhost:5432/inventory_test?sslmode=disable' \
go test -race -count=1 -v ./cmd/server


The integration tests cover concurrent reservations, expiry cleanup, confirmation, reservation idempotency, and request validation. They must not be pointed at production data or the main development database.

The test database must exist before running the integration tests. If it has not been created, create `inventory_test` using your local PostgreSQL administration workflow, then apply the schema and idempotency migrations. Do not delete the existing Compose volume to create the test database.

A race-detector run helps identify Go data races, but does not by itself prove database-level concurrency correctness. The PostgreSQL integration tests exercise the actual transaction and locking behavior.

## Git history

Commit in real, incremental steps as you work. For example:

1. `chore: scaffold Go Gin and React Vite apps`
2. `feat: add PostgreSQL inventory schema and seed data`
3. `feat: implement atomic inventory reservation`
4. `feat: implement confirmation and expiration worker`
5. `test: add validation and concurrency coverage`
6. `feat: build inventory dashboard`
7. `docs: add architecture and setup guide`

Do not create artificial commits retroactively to misrepresent how work was performed.

## Demo limitations to disclose

- No authentication or authorization; intended for a local take-home demonstration.
- Idempotency is implemented for reservation creation, backed by a PostgreSQL unique partial index and automated tests. It is not a complete payment or distributed-workflow idempotency solution.
- Expiry cleanup uses a polling worker; reserve and confirm paths also expire stale reservations for the relevant item.
- Production deployment should add metrics, structured logging, rate limiting, authentication, managed migration execution, and a more comprehensive integration and stress-test suite.
- Database pool sizing and hot-item contention require load testing before production deployment.
