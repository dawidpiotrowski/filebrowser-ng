# Developing filebrowser-ng

The server is Go, and `frontend/` contains the Vue application. Build the frontend before building a distributable server; Go embeds `frontend/dist`.

Use Go 1.27.3 or later, Node.js 24 or later, and the pnpm version pinned by `frontend/package.json`.

```sh
cd frontend
pnpm install --frozen-lockfile
pnpm run lint
pnpm run test
pnpm run build
pnpm audit --audit-level=low
cd ..
go vet ./...
go test -race -timeout 5m ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
go build -trimpath -o filebrowser-ng .
```

`task build` is an alternative when Task is installed. The static runtime image is built from source with `docker build -t filebrowser-ng:local .`.

For a security fix, add a small regression that fails on the vulnerable behavior and confirms normal operations still work. Keep storage compatibility, user permissions, and cross-platform behavior in mind. Update `docs/security/` and the migration guide for security-related compatibility changes.

The frontend carries a bounded-nesting patch for `braces@3.0.3`. The single audit exception is tied to that exact patch and its regression tests; do not remove the patch or broaden the exception. Dependency overrides need a build and preview regression check when upgraded.

Release packaging in `.goreleaser.yml` generates fork-named binaries and checksums. No workflow publishes to the upstream Docker Hub repository or Homebrew tap. Configure release destinations explicitly before tagging a release.
