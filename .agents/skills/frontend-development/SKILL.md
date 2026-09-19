---
name: frontend-development
description: Develop or review the Go Admin Plus Vue 3 frontend, including headless domains, Web/Desktop adapters, app-shell routing, controllers, accessibility and pnpm quality gates.
---

# Frontend Development

Use this skill for changes under `frontend/`, frontend tests and Web/Desktop composition.
Read the repository `AGENTS.md`, Speculo workspace/config files, and
[repository architecture](../../../docs/repository-architecture.md) before changing package
boundaries or runtime behavior.

## Current topology

```text
frontend/
  apps/
    admin-web/            Vite browser application
    admin-desktop/        Vite app plus Tauri 2/Rust host
  packages/
    platform/             Host-neutral ports and shared types
    api-client/           OpenAPI generated client and adapter primitives
    ui/                   Shared components, tokens and list/controller utilities
    adapters/browser/     Browser HTTP/session runtime
    adapters/desktop/     Tauri IPC/session runtime
    domains/<module>/     Headless domain types, validation, ports and tests
    web-domains/<module>/ Vue pages, web client mapping, controllers and tests
  tests/                  Shell, browser and native harness contracts
```

## Call hierarchy and dependency direction

```text
admin-web or admin-desktop
  -> runtime adapter
  -> app-shell ProductWorkspace and product router
  -> web-domain controller/client
  -> headless domain port and validation
  -> api-client generated OpenAPI transport or Tauri host command
```

- Apps select one adapter and mount the shared shell; they do not own business HTTP or IPC logic.
- `platform` is host-neutral. `domains/*` may depend on `api-client` but never on Vue, DOM, Element
  Plus, a host adapter or app-shell.
- `web-domains/*` owns Vue presentation and maps transport data to domain types. It may use `ui`, but
  must not deep-import another package's `src` files.
- `app-shell/src/product/manifest.ts` is the route/menu contract. `ProductWorkspace.vue` explicitly
  composes controllers and pages for both hosts.
- Capability checks hide routes and actions for usability only; the backend remains the authorization
  boundary.

## Implementation rules

- Use Vue 3 Composition API and `<script setup lang="ts">`; keep package exports explicit and workspace
  dependencies exact. Do not add a second router, store, component library or CSS theme.
- Keep generated OpenAPI files generated. Normalize and validate in the headless domain, map errors at
  the client boundary, and keep raw transport envelopes out of templates.
- Reuse the shared list/controller state machine. Preserve stale-request protection, last-successful
  projection, loading/submitting/delete recovery, conflict repair and post-delete refresh semantics.
- Route, menu, toolbar and row actions must use the current capability constants. Deletion confirms at
  the UI boundary and the server checks authorization again.
- Preserve labels, focus order, keyboard behavior, stable test identifiers and ARIA semantics. Use
  stable table widths and responsive constraints so dynamic text cannot shift controls.
- Use Element Plus and Lucide through existing shared patterns; keep tokens in the shared Sass theme.
  Do not encode credentials, private endpoints or host-specific assumptions in a page.

## Tests and gates

Cover domain normalization/validation, client error mapping, controller races and conflicts, permission
visibility, dialog reset, cancellation and successful mutation. Run:

```text
pnpm --dir frontend lint
pnpm --dir frontend typecheck
pnpm --dir frontend test
pnpm --dir frontend check:workspace
pnpm --dir frontend build
task architecture:check
task compatibility:zero
```

For release-impacting work also run `task build TARGET=all PROFILE=server-sqlite` and verify that the
production Desktop bundle contains no native-E2E controls. A page is complete only when its headless
domain, web domain, product manifest, shared shell composition and tests are all connected.
