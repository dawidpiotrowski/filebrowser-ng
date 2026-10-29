# ng.4 release preparation — 2026-10-29

Target: **2.64.0-ng.4**, prepared locally, not published. The published baseline remains `v2.64.0-ng.3` at `12c883134fdb766667572905984f57627fe15b9e`; the application changes were committed through `a920e308`. See the [draft release notes](../releases/2.64.0-ng.4.md) and [upgrade instructions](../migration.md#upgrade-from-2640-ng3-to-2640-ng4-prepared).

The user performs environment validation. Further agent work is limited to repository changes and local commits. No push, release tag, registry publication or NAS deployment is included in this preparation.

## Repository changes

- Prepare ng.4 version defaults, a consolidated changelog and explicit authentication/filesystem migration notes. Published pull/Compose examples remain on ng.3 until publication is confirmed.
- Update x/net from v0.60.0 to v0.62.0 for GO-2026-6860 and GO-2026-6866. Its required x/crypto, x/sync, x/sys and x/text versions move together; no vulnerability-scan exception is added.
- Require Linux backend, browser and container checks before container publication. Linux/Docker/NAS use is the release priority. Windows/macOS checks remain in branch CI, without blocking Linux image publication; native Windows runtime validation is separate work.
- Add real browser-session tests to CI using isolated, pinned Playwright tooling. Add a disposable container upgrade test covering the published ng.3 digest, default and NAS UID/GID, CSRF, conditional writes, staged TUS, public links, restart persistence and logout.
- Limit binary archive targets to Linux, macOS and Windows. The BSD atomic-write adapters currently return unsupported; do not advertise functional BSD write support through an archive matrix.

## Checks completed before the scope correction

These results describe the working-tree candidate. They are not results from a published tag or from the user's NAS.

| Check | Result |
| --- | --- |
| Go vet and macOS arm64 race suite after dependency updates | Passed, 372 tests/subtests |
| Frontend lint and Vitest | Passed, 53 tests |
| Frontend production build and multi-platform image build | Passed locally for Linux amd64/arm64 |
| Real Chromium, two tabs | Passed with Web Locks/BroadcastChannel and with their storage fallback |
| Upgrade from the published ng.3 image, Linux arm64 | Passed for UID/GID 65532:65532 and 1000:1000 |
| Same container upgrade checks, Linux amd64 | Passed under local emulation for both UIDs; this does not establish native amd64 behavior |
| Fresh dependency scan | Zero reachable vulnerabilities and zero vulnerable imported packages after the update; GO-2026-5932 remains in an unimported OpenPGP package within required x/crypto |
| pnpm audit gate | Passed, zero reported advisories; existing patch/audit configuration unchanged |
| Workflow syntax, actionlint v1.7.7 | Passed |
| Windows files/fileutils/HTTP/Bolt test binaries | ARM64 cross-compilation passed; no native Windows tests were executed |

The initial new scan found two package advisories in x/net, with no identified calls to their affected functions. The update removes those findings. This does not claim that an exploitable application path was reproduced.

Detailed local logs are ignored under `.local/release-ng4-2026-10-29/`. Local test images use a working-tree revision label and are not published release artifacts. No immutable ng.4 registry digest exists yet.

## Publication and operator verification still pending

The user decides when to push the prepared source and run CI. The publish workflow must verify the exact tagged commit and pass its Linux release checks before any image is announced. Native Linux amd64/arm64 runner results remain pending; local emulation must not be substituted for them. Native Windows runtime checks are not a condition of this Linux image release.

The user also verifies the actual Ugreen proxy peer CIDRs, forwarded scheme/Host handling, ownership/ACLs, free space and representative filesystem operations using copied data. Take a cold database/configuration backup and matching data snapshot before a live upgrade. Storage power-loss behavior is not established by atomic-visibility tests.

After a future successful publication, record its source revision and registry digest, check anonymous pulls for both architectures, and update the versioned installation examples. Keep the OCI provenance/SBOM entries; their metadata manifests are separate from runnable platform images. The NAS deployment remains a separate user action.
