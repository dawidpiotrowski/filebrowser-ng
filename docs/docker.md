# Docker images and NAS migration

Registry: [ghcr.io/dawidpiotrowski/filebrowser-ng](https://github.com/dawidpiotrowski/filebrowser-ng/pkgs/container/filebrowser-ng). Public pulls require no login. Images support Linux `amd64` (Intel/AMD NAS) and `arm64` (64-bit ARM NAS); Docker selects the matching platform. 32-bit ARM is not currently published.

```sh
docker pull ghcr.io/dawidpiotrowski/filebrowser-ng:2.64.0-ng.3
```

Use a numbered version in production. `latest` follows the most recently published release. The workflow also publishes `sha-<full-git-commit>` and records an immutable multi-platform digest in its summary. To pin a digest, use `ghcr.io/dawidpiotrowski/filebrowser-ng@sha256:…`. Image labels identify the version, source, and commit; BuildKit publishes provenance and SBOM attestations alongside the image.

## Fresh installation

```sh
docker compose up -d
docker compose logs filebrowser-ng
```

The initial random admin password appears once in the log. The supplied Compose file binds to loopback (`127.0.0.1:8080`) and creates named volumes for files and the database. For access from another machine, set the bind address to the NAS LAN address or put a TLS reverse proxy in front of it. See [deployment](deployment.md).

## Move an existing NAS installation

1. Record the existing container's data mount, database path, config path, UID/GID, port, command, and environment. `docker inspect <old-container>` shows these. If the old image has a shell, `docker exec <old-container> id` shows its process user (S6 may run the service under a different user; check its `PUID`/`PGID`). Keep the same in-container data root so existing user scopes resolve correctly.
2. Stop the old container. Back up the database and configuration while it is stopped, and snapshot or back up the files. Test against copies first. Keep backups outside the served tree. Run only one instance against any database/data tree.
3. Copy [`.env.nas.example`](../.env.nas.example) to `.env.nas`. Set the existing UID/GID, absolute paths, bind address, and host port. The database directory must already exist and contain your existing `filebrowser.db`. If your database has another name, set `--database` explicitly. The example refuses missing host paths so a typo cannot silently create an empty installation.
4. Use [compose.nas.yaml](../compose.nas.yaml). For the standard `/srv` root and `/database/filebrowser.db` database:

   ```sh
   docker compose --env-file .env.nas -f compose.nas.yaml up -d
   docker compose --env-file .env.nas -f compose.nas.yaml logs filebrowser-ng
   ```

5. Check existing accounts, root/scopes, upload/download, permissions, previews, and shares. Everyone must sign in again. If the log says it created a new database/admin, stop and check the database mount/path; an existing database should retain your users. Read the [migration changes](migration.md), especially proxy auth, hooks, and Redis, before switching traffic.

The database directory and served files must be writable by the selected UID/GID; the database needs directory write access as well as file write access. Use the existing service user to retain ownership. The image defaults to `65532:65532`, does not implement `PUID`/`PGID`, and has no S6, shell, or startup `chown`. In Compose use `user: "1000:1000"` (replace with your actual IDs), or `--user 1000:1000` with `docker run`.

### Adapt an existing Compose or NAS container UI

Change the image to `ghcr.io/dawidpiotrowski/filebrowser-ng:2.64.0-ng.3` and map your current host port to container port **8080**. Preserve the existing file/database mounts and set the UID/GID. A port mapping that previously ended in `:80` must end in `:8080`.

The default command is:

```yaml
command:
  - --root=/srv
  - --database=/database/filebrowser.db
  - --address=0.0.0.0
  - --port=8080
```

If your previous setup loaded `/config/settings.json`, mount that config directory read-only and add `--config=/config/settings.json` to the command above. Adapt `--root` and `--database` to match the previous installation. These explicit flags take precedence over config and environment; review `FB_*` variables and the old command rather than copying them blindly. The database must remain outside the served root. A mount at `/config` alone does not load that file automatically.

For a read-only container root filesystem, retain writable mounts for the database and files and a writable `/tmp` (the examples use tmpfs). No `privileged` mode or added Linux capabilities are needed.

## Upgrade

Stop the service, back up the database and data together, update the image version in Compose, then run `docker compose pull` and `docker compose up -d`. Use `--env-file .env.nas -f compose.nas.yaml` with the NAS example. Keep the backup until you have verified the upgrade. Do not use `docker compose down -v` when upgrading: it deletes named volumes.

## Publish a release

Push a tag such as `v2.64.0-ng.3`. The `Publish container` workflow first runs the reusable CI workflow (Go race tests on Linux, frontend checks, vulnerability checks, and native container startup tests on both architectures), then cross-compiles and publishes both platforms to GHCR. Its registry credential is the repository's short-lived `GITHUB_TOKEN`; no personal registry token is required. The full branch CI also tests macOS and Windows independently. To retry or publish an existing tag using the current workflow, dispatch it from the default branch with the `tag` input; both verification and image building check out the resolved tagged commit, not the default branch. A new GHCR package initially needs its visibility set to Public by its owner; verify an anonymous pull before announcing it.
