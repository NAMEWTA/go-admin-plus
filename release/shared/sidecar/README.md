# Desktop sidecar packaging

Tauri resolves `externalBin` by appending the Rust target triple. Build the matching Go binary before invoking Tauri:

```sh
node release/shared/sidecar/build.mjs --target aarch64-apple-darwin
node release/shared/sidecar/build.mjs --target x86_64-apple-darwin
node release/shared/sidecar/build.mjs --target x86_64-pc-windows-msvc
```

For local development, the desktop app exposes one command that detects an approved host triple, builds its sidecar, and starts Tauri:

```sh
pnpm --dir frontend/apps/admin-desktop dev:native
```

Generated binaries are staged atomically under `apps/admin-desktop/src-tauri/binaries/` and remain ignored by Git.
