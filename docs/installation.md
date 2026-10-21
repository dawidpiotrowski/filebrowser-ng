# Installation

Build this fork from source using the [README](../README.md#build-and-run), or run `docker compose up --build -d` from the repository root. There are no published fork packages or container images yet. Upstream package-manager entries and install scripts still install the archived project.

The server creates an initial database and prints a random administrator password once. Save it securely. To recover an existing account, use the CLI user update command after stopping the server and backing up the database; do not delete a database containing accounts and shares.

The container runs as UID/GID `65532:65532` and listens on port `8080`. Bind-mounted directories must be writable by this user. Named volumes in the supplied Compose file receive the image's ownership automatically. The runtime has no shell or S6 init system.

For upgrades, read [migration.md](migration.md). For network exposure, read [deployment.md](deployment.md).
