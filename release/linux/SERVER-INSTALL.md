# Go Admin Plus Linux service installation

This archive is a self-contained Linux server release for `amd64` or `arm64`. It contains the
`go-admin-plus-server` binary, both typed profile examples, and systemd units. It does not include
Docker Compose files or images; use the repository's `deploy/compose/` definitions for container
deployment. Run the commands below
as root on a machine you control; keep the bootstrap password and PostgreSQL DSN outside the archive.

## Install

```bash
sudo useradd --system --home /var/lib/go-admin-plus --shell /usr/sbin/nologin go-admin-plus
sudo install -d -o go-admin-plus -g go-admin-plus -m 0750 /opt/go-admin-plus /etc/go-admin-plus /var/lib/go-admin-plus
sudo install -m 0755 bin/go-admin-plus-server /opt/go-admin-plus/go-admin-plus-server
sudo install -m 0640 config/server-sqlite.yaml /etc/go-admin-plus/server-sqlite.yaml
sudo install -m 0640 config/server-postgres.yaml /etc/go-admin-plus/server-postgres.yaml
sudo install -m 0644 systemd/go-admin-plus-server.service /etc/systemd/system/
sudo install -m 0644 systemd/go-admin-plus-server-postgres.service /etc/systemd/system/
```

Choose one profile. For SQLite, use the supplied `server-sqlite.yaml`; for PostgreSQL, create a
root-readable `/etc/go-admin-plus/server.env` containing `GO_ADMIN_DATABASE_DSN_FILE` that points to
a mode `0600` DSN file. Set `GO_ADMIN_LOG_LEVEL` in the same environment file when needed.

Initialize the database before starting the unit:

```bash
sudo -u go-admin-plus /opt/go-admin-plus/go-admin-plus-server migrate \
  --profile server-sqlite --config /etc/go-admin-plus/server-sqlite.yaml \
  --data-root /var/lib/go-admin-plus
sudo -u go-admin-plus /opt/go-admin-plus/go-admin-plus-server bootstrap \
  --profile server-sqlite --config /etc/go-admin-plus/server-sqlite.yaml \
  --data-root /var/lib/go-admin-plus --username admin \
  --display-name Administrator --email admin@example.invalid --secret-file /run/backend/bootstrap.secret
```

Use `server-postgres` and the PostgreSQL profile for a PostgreSQL deployment. Run `doctor` after
bootstrap, then enable exactly one unit:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now go-admin-plus-server.service
sudo systemctl status go-admin-plus-server.service
```

The PostgreSQL unit is `go-admin-plus-server-postgres.service`. Back up `/var/lib/go-admin-plus`
before upgrades. A normal service stop does not delete database or product files. To upgrade, stop
the unit, replace only the binary, run the forward migration, run `doctor`, and start the same unit.
