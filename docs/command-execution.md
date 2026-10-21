# Removed command execution

filebrowser-ng removes the command API, before/after event hooks, and hook authentication. These features executed unsandboxed host commands and had multiple published vulnerabilities. `/api/command` returns HTTP 410 for authenticated requests.

Existing command definitions remain readable in old databases for migration, but never execute. `--disable-exec=false` is rejected at startup. Hook authentication must be replaced with JSON authentication or a trusted identity proxy before starting the server. External automation should run as a separate, independently confined service.
