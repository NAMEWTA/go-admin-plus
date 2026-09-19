# Native desktop E2E

The harness starts the real Tauri host and supervised SQLite sidecar. On macOS, grant the terminal Accessibility permission, then run:

```sh
GO_ADMIN_DESKTOP_NATIVE_E2E=1 node frontend/tests/e2e/desktop/run.mjs
```

The required command fails when the opt-in is missing, when the host is not macOS, or when any native phase is skipped. A successful run ends with `DESKTOP_NATIVE_E2E_PASS runtime=tauri-native profile=sqlite skipped=0`. Source worktrees do not claim this required native check as passed.
