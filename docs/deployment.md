# Deployment

Use the published GHCR image with the supplied Compose file (see [Docker and NAS](docker.md)). It listens on loopback port 8080 by default, runs as UID/GID 65532, drops Linux capabilities, and uses a read-only runtime filesystem. The served data and database need writable volumes; keep the database outside the served tree. For local builds, add the `compose.build.yaml` override.

For remote access, put the service behind a TLS-terminating reverse proxy. Forward requests to the loopback listener, set `X-Forwarded-Proto: https`, preserve the host, and configure request-size, connection, and rate limits appropriate to your users. Set the app's `baseURL` if serving it below a path prefix. Do not expose a proxy-auth backend directly to clients.

With proxy authentication, configure the proxy's actual peer CIDRs through `--auth.trustedProxies` and require it to authenticate every request and overwrite the identity header. A header supplied by an untrusted TCP peer is rejected. Built-in peer rate limits are shared by users behind the same proxy.

Use filesystem quotas and container memory/CPU limits for a multi-user deployment. The application does not implement per-user storage quotas. Mount only the intended data tree and keep external symlink following disabled. Use one writer process per tree; Redis-backed multi-writer uploads are disabled. Do not modify served paths concurrently from privileged host automation when relying on path-based deny rules.

Back up the database and data together, verify restore procedures, and read the [migration guide](migration.md) before replacing an existing instance. The archived upstream images, S6 image, install scripts, and Homebrew packages are not filebrowser-ng distribution channels.
