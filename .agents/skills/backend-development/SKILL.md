---
name: backend-development
description: Develop or review the Go Admin Plus backend, including product composition, capability modules, OpenAPI transport, dual-dialect migrations, workers, security and tests.
---

# Backend Development

Use this skill for changes under `backend/`, `contracts/openapi/`, backend tests and backend
release/deployment integration. Read the repository `AGENTS.md`, the Speculo workspace/config files,
and [repository architecture](../../../docs/repository-architecture.md) before making a structural
change.

## Current topology

```text
backend/
  cmd/
    server/             Server and bootstrap/recovery CLI
    desktop-sidecar/     Tauri-managed local SQLite process
  internal/
    app/adapters/         Product-to-module ports and adapters
    app/product/          The only product composition root
    application/          Module contract and lifecycle orchestration
    contracts/            Generated transport contracts and capability types
    host/                 Server/Desktop process hosts and readiness
    modules/              iam, audit, scheduler and files
    platform/             config, database, migrations, logging, outbox and coordination
  test/                   Cross-module, dialect, E2E harness and reliability tests
```

## Call hierarchy and boundaries

The normal request path is:

```text
CLI/host -> product.Build or BuildPrepared
  -> application.Build and module lifecycle
  -> HTTP transport / session adapter
  -> module service/use case
  -> repository and platform ports
  -> SQLite or PostgreSQL
```

- `internal/app/product` constructs services, adapters, routes, workers and migration providers.
- `internal/application` owns generic module registration and start/stop lifecycle; it must not know
  business details.
- A module owns its domain behavior, repository, HTTP transport, capabilities and migrations. Modules
  may use public ports from another module, but never another module's private tables or implementation.
- `internal/platform` supplies technical capabilities. It must not become a business feature bucket.
- OpenAPI YAML under `contracts/openapi/` is the HTTP source of truth. Generated Go transport and
  frontend clients are outputs; do not hand-edit them.

## Implementation rules

- Keep the composition-root, Ports and Adapters and vertical-slice design. Do not add `common`,
  `system`, `other` or compatibility aliases to bypass module ownership.
- Author migrations inside the owning module, provide equivalent PostgreSQL and SQLite SQL, and
  register them in `product.NewMigrationRunner`. Do not create schema from a service or repository.
- Re-authorize every write in the service transaction. Enforce data scope in backend queries, not in UI
  filters. Use revision/conflict semantics for mutable records.
- Use generated strict HTTP transport and the shared session/CSRF/problem adapters. Keep user-facing
  messages, machine error categories, audit facts and operational logs separate.
- Pass `context.Context` through I/O, return wrapped/sentinel errors at boundaries, and make worker
  ownership, lease, retry and cancellation explicit.
- Logs must be structured and redacted. Never record passwords, secret-file contents, DSNs, session or
  CSRF tokens, request bodies or raw credentials.
- Server profiles are `server-sqlite` and `server-postgres`; Desktop is `desktop-sqlite`. Server migration is an explicit offline operation for both dialects. Local Desktop backs up and migrates SQLite; remote Desktop connects by HTTPS without a sidecar. Readiness rejects schema mismatch.
- Public server configuration is unified YAML; optional Redis is display cache only, and files support local/S3 providers.

## Tests and gates

Add focused unit, service, HTTP, migration and dialect tests with every behavior change. Run the
smallest relevant package tests first, then the root gates:

```text
task test
task lint
task contract:lint
task generate:check
task architecture:check
task compatibility:zero
task governance:check
task docs:check
task task:contract
```

For release-impacting work also run `task build TARGET=all PROFILE=server-sqlite` and
`task release:verify`. A backend change is complete only when the product registry, route module,
capability registration, migrations, generated transport and tests agree.
