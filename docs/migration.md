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

## Changes in 2.64.0-ng.3

- Complete user replacements (`which` missing/null/empty or `all`) and changes to passwords, identities, permissions, scope, password locks, commands and rules require the current password under JSON authentication. Preference-only updates still work without it. API clients must send editable field names; ID/runtime filesystem fields and unknown fields are rejected before any writes.
- The last administrator cannot be deleted or demoted through API/CLI, including concurrent operations. Invalid regex rules are rejected before saving. Existing malformed rules deny file access; fix global rules in settings or repair user rules through an administrator.
- Failed rename operations fall back to copy/delete only for cross-device moves. Other filesystem errors are returned instead of being bypassed. Failed copies retain the source/partial destination for recovery and never unconditionally delete an existing destination. Interrupted overwrite atomicity remains planned work.
- Application pages now restrict embedding to the same origin and set no-referrer, navigation and browser-permission headers. Same-origin PDF previews remain allowed. Integrations embedding the UI on another origin need an explicit policy design. HSTS remains a reverse-proxy/TLS endpoint setting.
- Thumbnail cache identity now includes finer timestamps and size. Existing cache entries are ignored, not migrated; caches may be cleared separately during maintenance. The database schema and UID/GID/container paths do not change.

See the [review](security/code-review-2026-10-21.md) for evidence and remaining boundaries, and the [roadmap](security/roadmap.md) for upcoming security features.

## Unreleased local changes

These changes follow `v2.64.0-ng.3` and are not yet available in a published image. No database schema, UID/GID, container path or deployment configuration change is required by this local patch set.

- Copy requests without `override=true` return 409 if another operation creates a destination file before its writable open, including inside recursive copies. Modify permission alone does not opt into overwriting. Copying a file onto itself through a hardlink/symlink returns an error and preserves source contents. A failed Sync/Close during a cross-volume move retains the source; partial destination data may remain for recovery. Atomic rename and overwrite are still planned work.
- TUS HEAD/PATCH/DELETE require the upload creator's account, in addition to existing permissions. A different account cannot take over an upload merely because it shares the directory; use a new upload or the ordinary permitted file operations. A replacement filesystem object at the same path causes 409. The creator can resume after session renewal. Adapters whose root lacks stable filesystem identity reject TUS creation with 501 before mutation; use regular uploads with such adapters. The created handle must also supply stable identity. OS-backed deployments retain TUS. Cache expiry and server restart still lose resumability and retain incomplete files on disk.
- New share IDs are 22 URL-safe characters; clients must treat hashes as opaque strings of variable length. Existing short links continue to work. Explicit expiry must be positive, use seconds/minutes/hours/days (omitted unit means hours), and fit a Go duration. Invalid/overflowing values return 400 instead of being accepted or returning 500. Omitted expiry remains permanent. Passwords longer than bcrypt's 72-byte limit return 400.
- Browser requests associate renewal/logout with the token sent, and TUS refreshes its token for each request. The authentication wire protocol remains unchanged. Full HttpOnly session migration and cross-tab coordination are separate planned changes.

See the [local review](security/follow-up-review-2026-10-21.md) for evidence and limitations.
