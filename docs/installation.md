# Installation

Pull `ghcr.io/dawidpiotrowski/filebrowser-ng:2.64.0-ng.3` and run `docker compose up -d` from the repository root. Public images support Linux `amd64` and `arm64` without a registry login. See [Docker and NAS](docker.md) for existing installations. To build locally, use `docker compose -f compose.yaml -f compose.build.yaml up --build -d`, or build a binary using the [README](../README.md#build-and-run). Upstream package-manager entries and install scripts still install the archived project.

The server creates an initial database and prints a random administrator password once. Save it securely. To recover an existing account, use the CLI user update command after stopping the server and backing up the database; do not delete a database containing accounts and shares.

The container runs as UID/GID `65532:65532` and listens on port `8080`. Bind-mounted directories must be writable by the selected user; override `user:` to match your NAS ownership. Fresh named volumes in the supplied Compose file receive the image's ownership automatically. The runtime has no shell or S6 init system.

For upgrades, read [migration.md](migration.md). For network exposure, read [deployment.md](deployment.md).
