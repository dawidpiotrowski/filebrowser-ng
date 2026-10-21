# Migration from File Browser v2.63.23

1. Stop the upstream process. Back up its database, configuration, and data. Keep the backup outside the served tree.
2. Pull the fork image (see [Docker and NAS](docker.md)) or build filebrowser-ng and point it at the same database and root. The database schema retains upstream users, permissions, settings, and shares, and adds a session bucket. Test with a copied database and data first.
3. If the old instance used hook authentication, switch it to JSON or proxy authentication using the CLI before starting the new server. Command execution and hooks no longer run; remove `--disable-exec=false`.
4. For proxy authentication, configure `--auth.trustedProxies` with the actual proxy peer CIDRs. Restrict backend access to that proxy and overwrite identity headers. Use TLS; configure `X-Forwarded-Proto` correctly so cookies are marked Secure behind TLS termination.
5. Remove the Redis upload-cache URL and run one writer process per tree. Redis did not fence concurrent filesystem writes. Expired incomplete uploads remain on disk rather than risking deletion of a subsequently replaced file; clean abandoned files during a maintenance window.
6. If signup is enabled, enable `createUserDir`. Self-registration into a shared default/root scope is rejected. Verify existing users' scopes; migration cannot infer which historical accounts were provisioned unsafely.
7. All users must sign in again. Old JWTs are rejected because they have no server-side session. Renewal consumes the old token; API clients must retain the replacement and serialize renewal. Logout uses `POST /api/logout` with `X-Auth`.
8. Old password-share download URLs containing `?token=` no longer bypass the password. Recipients use the ordinary share page and enter its password; subsequent downloads use an HttpOnly cookie valid for at most 15 minutes. Test media playback, EPUBs, CSVs, and browser integrations before rollout.

Subtitle conversion is capped at 2 MiB; VTT streams directly. Text previews are capped at 10 MiB even if a file grows after stat. Archive trees are capped at 100,000 entries and 128 levels, and skip special files and cyclic directory aliases.

The new container is published to GHCR, contains no shell, runs as UID/GID 65532, and listens on 8080. Match `user:` to your existing NAS UID/GID rather than changing ownership of an entire data tree. The supplied Compose file binds to loopback; [compose.nas.yaml](../compose.nas.yaml) makes paths, ownership, and the bind address explicit. The upstream S6 image and upstream install scripts are not fork distribution channels.

After a successful test migration, take a fresh backup and switch the deployment. Rolling back to upstream reintroduces its known vulnerabilities; do not treat rollback as a security fix.
