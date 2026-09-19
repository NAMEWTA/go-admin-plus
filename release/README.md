# Release Assets

This directory contains the platform release contracts and packaging helpers:

- `linux/`: Linux amd64/arm64 server service bundles and x64 desktop deb/AppImage, systemd units, and deployment docs.
- `macos/`: Apple Silicon arm64 / Intel x64 app, DMG, and install verification.
- `windows/`: x64 sidecar, Tauri 2 NSIS installer, install-path and persistence verification.
- `shared/`: shared Desktop sidecar packaging logic.
- `manifest/`: source, checksum, and artifact contracts used by local release preflight.

Regular CI runs reproducible builds and policy tests. Pushing an exact semver tag automatically builds
and publishes the Linux service/desktop, macOS arm64/x64, and Windows x64 artifacts through `.github/workflows/release.yml`.
This project intentionally does not sign or notarize its private self-use releases.

```bash
task release VERSION=0.0.3
task release:verify
```

Those commands validate the local source and packaging contracts; only the tag workflow publishes a
GitHub Release.
