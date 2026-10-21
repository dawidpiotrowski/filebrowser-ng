# Validation — 2026-10-21

Local host: macOS arm64; compiler Go 1.27.3. Frontend package manager: pnpm 10.33.4. Runtime image built and exercised on Linux arm64 through Docker Desktop.

| Check | Result |
| --- | --- |
| `go test -race -timeout 180s ./...` | PASS, all packages |
| `go test -json ./...` | PASS, 222 tests/subtests passed, 0 skipped |
| `go vet ./...` | PASS |
| `govulncheck@v1.9.0 ./...` | Zero reachable vulnerabilities and zero vulnerable imported packages; one unused module package advisory (`x/crypto/openpgp`) |
| `pnpm run lint` | PASS, no warnings |
| `pnpm run test` | PASS, 35 tests in 7 files |
| `pnpm run build` | PASS, typecheck and production bundle |
| `pnpm audit --audit-level=low` | PASS; one reported GHSA is locally patched and narrowly excepted, no unpatched findings |
| Gosec 2.29.0 with `x/tools@v0.52.0` | Completed without package errors; 19 reviewed findings: 0 high, 13 medium, 6 low; see audit classifications |
| Native `go build -trimpath -o filebrowser-ng .` | PASS |
| `docker build --tag filebrowser-ng:local .` | PASS, including Linux Go tests in build stage |
| Runtime container smoke | PASS with UID 65532, read-only root, capabilities dropped, no-new-privileges |
| End-to-end API smoke | PASS: health, login, HttpOnly cookie, upload, text preview, share creation/public download, token rotation, rejection of old token, logout, rejection of revoked token |
| Browser/static smoke | PASS: SPA JSON bootstrap is an object, malicious branding escapes script context, login page renders, logo loads |
| `git diff --check` | PASS |

The frontend build emits expected notices for server-templated reCAPTCHA/custom CSS URLs and bundler timing; these are not build failures. The image has no shell and no upstream publishing destinations.

The CI workflow additionally targets Linux, macOS, and Windows. Local results do not substitute for remote CI results; inspect the repository Actions tab for the pushed commit. Cross-platform runtime behavior and media/preview flows outside these tests still need deployment-specific verification.

Repository setup: issue tracking, private vulnerability reporting, vulnerability alerts, and automated dependency security-fix PRs enabled. No release was tagged or pushed to a container registry, and no existing production deployment was changed.
