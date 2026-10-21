# filebrowser-ng

A maintained fork of [File Browser](https://github.com/filebrowser/filebrowser), starting from its final v2.63.23 release (commit `833d908884d5c801f30f5c098d7977177eb3a36b`). Upload, browse, preview, edit, and share files from a directory on your server.

This fork starts with a security hardening pass. Read the [audit and advisory inventory](docs/security/audit-2026-10-21.md) and [migration guide](docs/migration.md) before replacing an existing instance.

The [follow-up code review](docs/security/code-review-2026-10-21.md) documents additional fixes in 2.64.0-ng.3. The [security roadmap](docs/security/roadmap.md) covers browser sessions, safe file commits, CSP, MFA and resource limits.

## Docker / NAS

Public images for Linux `amd64` and `arm64` are published to [GHCR](https://github.com/dawidpiotrowski/filebrowser-ng/pkgs/container/filebrowser-ng):

```sh
docker pull ghcr.io/dawidpiotrowski/filebrowser-ng:2.64.0-ng.3
docker compose up -d
```

The supplied Compose file uses persistent named volumes and binds to `127.0.0.1:8080`. On a NAS, use [the migration instructions](docs/docker.md) and [compose.nas.yaml](compose.nas.yaml) to keep your existing files, accounts, and database. The container listens on **8080** and runs as UID/GID **65532:65532** by default; set `user:` to your existing NAS UID/GID for bind mounts. It has no shell, S6, or `PUID`/`PGID` initialization.

Version tags are recommended for upgrades. `latest` tracks the most recently published release; each build also has a full commit `sha-…` tag and an immutable registry digest. Publication requires passing CI, including native container smoke tests on both architectures.

## Build and run

Requires Go 1.27.3 or later, Node.js 24 or later, and pnpm 10.33.4.

```sh
git clone https://github.com/dawidpiotrowski/filebrowser-ng.git
cd filebrowser-ng
cd frontend
pnpm install --frozen-lockfile
pnpm run build
cd ..
go build -trimpath -o filebrowser-ng .
./filebrowser-ng --root /path/to/files --database ./filebrowser.db --address 127.0.0.1 --port 8080
```

The first startup creates the database and prints a random administrator password. Existing upstream databases remain readable. Back up your database and configuration first; all existing login sessions must log in again.

Alternatively, build the non-root container locally:

```sh
docker compose -f compose.yaml -f compose.build.yaml up --build -d
```

The runtime image contains only the static Go executable and CA certificates, drops capabilities, and uses a read-only root filesystem.

## Security changes

- Persistent, revocable sessions: logout revokes the session, password changes invalidate old credentials, and renewal atomically consumes the previous token.
- Command execution, event hooks, and hook authentication are removed. They cannot be re-enabled through an old database or flag.
- Upload failures never recursively delete a destination. TUS validates lengths before touching files and serializes writes to one upload.
- Access rules cover symlink targets. OS filesystem operations use traversal-resistant roots; archives skip special files and cycles.
- Share revocation resolves the owner's actual scope when deleting or moving files. Password-protected shares use short-lived HttpOnly cookies instead of permanent URL tokens.
- Subtitle conversion and text previews have bounded reads. Password work and login attempts are bounded; proxy identities require explicit trusted peer CIDRs.
- Updated dependencies, security regression tests, CI, and Dependabot configuration.

Use one writer process per storage tree. Redis upload coordination is rejected until writes can be fenced across processes. Put remote deployments behind TLS and a correctly configured reverse proxy. Keep the database outside the served directory; mount only data the service should access. Filesystem path rules do not protect against hard links or hostile host administrators.

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md), [documentation](docs/README.md), and [SECURITY.md](SECURITY.md). No audit guarantees that a program is free of vulnerabilities; the report records scope and remaining limitations.

## License and provenance

[Apache License 2.0](LICENSE). Original copyright and notices remain intact. File Browser contributors retain credit for the upstream code and fixes; the fork's changes are recorded in Git history. This project is independent of the upstream maintainers.
