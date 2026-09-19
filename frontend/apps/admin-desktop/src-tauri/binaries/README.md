# Sidecar staging

`release/shared/sidecar/build.mjs` writes one of the approved target-triple suffixed Go binaries here immediately before a Tauri build. The builder stages and atomically publishes each binary; generated binaries are not source artifacts.
