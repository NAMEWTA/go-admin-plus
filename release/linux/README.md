# Linux Server Release

The Linux release contains `go-admin-plus-server` service archives for `linux/amd64` and
`linux/arm64`. Each archive includes both SQLite and PostgreSQL profile examples, systemd units,
and `SERVER-INSTALL.md` with the full deployment procedure.

The service is a Go binary. Run the one-shot `migrate` command for either database before starting the API unit; the API and worker must
use the same data root and DSN configuration. Keep credentials in permission-restricted environment
or secret files, never in Git.

The release workflow builds both architectures with `CGO_ENABLED=0`, verifies the checksums, and
uploads the archives to the GitHub Release. The service archive does not contain Docker Compose
files or images. The repository's Compose definitions in `deploy/compose/` remain available for
container deployment; production Compose runs require immutable `image@sha256:<64-hex-digest>`
references for API, Web, and PostgreSQL images.

Linux x64 desktop deb/AppImage installation is described in [DESKTOP-INSTALL.md](DESKTOP-INSTALL.md). Runtime data is separate from program files.
