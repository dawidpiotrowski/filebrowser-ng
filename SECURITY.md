# Security policy

The development branch and the latest filebrowser-ng release receive security fixes. The first fork container release is `2.64.0-ng.1`. Upgrade to the latest fork release for fixes; upstream File Browser releases and images are not supported by this fork.

For a new vulnerability, use **Report a vulnerability** in this repository's Security tab (private vulnerability reporting is enabled for this fork). If it is unavailable, open an issue requesting a private reporting channel without including an exploit or sensitive data. Do not send reports to the archived upstream repository.

Include the fork commit/version, configuration, affected endpoint, a minimal text reproduction, impact, and suggested fix. Check the [advisory inventory and audit](docs/security/audit-2026-10-21.md) first.

We aim to triage reports and ship regression tests with fixes. This is a volunteer-maintained fork; there is no guaranteed response SLA.

## Deployment boundary

Run unprivileged, behind TLS, with the database outside the served tree. Keep the runtime, Go toolchain, and container base images current. Proxy authentication trusts only explicitly configured TCP peer CIDRs and still requires the proxy to strip client-supplied identity headers. Do not enable external symlink following for untrusted trees. Use one writer process per storage tree. See [migration](docs/migration.md) for compatibility changes and the audit for unresolved limits.
