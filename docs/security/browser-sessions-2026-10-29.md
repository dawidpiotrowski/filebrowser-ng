# Browser sessions, CSRF and multiple tabs — local implementation

Date: 2026-10-29. Baseline: local `da4dbd2b`, above published ng.3. Implementation commit: `8ca3da75`. This work is local only: no push, tag, release, container publication or NAS deployment. Authentication protocol and operator migration details are in [authentication](../authentication.md) and [migration](../migration.md).

## Implemented boundary

Browser authentication is an opaque 256-bit secret in an HttpOnly, host-only, SameSite=Strict cookie. The server stores its SHA-256 identifier, user credential fingerprint, non-authenticating public generation, idle/absolute deadlines and login challenge family. Login returns sanitized JSON, never a JWT. The frontend removes the old `jwt` key from both storage APIs without reading it. UI preferences remain local. No authentication credential is sent through a broadcast, query string, X-Auth header or JavaScript response body in browser mode.

`GET /api/session` reads the current shared cookie without extending idle expiry. It returns the user, expiry, public generation and CSRF proofs with `Cache-Control: no-store`. Unsafe cookie requests require an HMAC-derived session proof and matching generation. Login/signup use a separate HttpOnly challenge and login proof. Origin/Referer and Fetch Metadata reject foreign origins, including sibling subdomains; absent metadata does not waive CSRF. Duplicate session/challenge cookies and mixed browser/API credentials fail closed.

Direct TLS and explicitly trusted HTTPS proxies use Secure, `__Host-` cookies. Their names include the BaseURL namespace and their required path is `/`. LAN HTTP uses unprefixed BaseURL-scoped cookies and retains its existing lack of transport confidentiality. Only configured TCP peers can assert `X-Forwarded-Proto`; Host must be preserved by the proxy. The old unvalidated forwarded-TLS decision is removed, including for password-share grants. Invalid proxy CIDRs reject startup.

The default idle deadline is two hours, configurable and capped by an absolute 24-hour deadline. Real application/media requests extend idle expiry after half the lifetime has elapsed; background probes do not. Transactional browser renewal preserves the credential and proof, never shortens expiry and never exceeds the absolute deadline or resurrects a revoked record. Therefore concurrent renewals and lost responses do not strand a tab. Explicit login creates a new credential, replaces the presented session and revokes earlier logins from the same challenge family, including responses not yet received by the browser.

Logout deletes the server record. Its response never writes the current session cookie; a delayed logout/renewal/anonymous response cannot erase a newer login. The revoked cookie remains inert until replacement/absolute expiry. Password/username changes invalidate the fingerprint; state and revocation persist in Bolt across restart. Already authorized in-flight requests are not cancelled retroactively by logout.

## Client convergence and API compatibility

A tab queues session mutations, deduplicates renewals and uses Web Locks when available. Each session request has a ten-second timeout and lock acquisition a fifteen-second timeout; closing a tab releases its browser lock. Other tabs receive only an invalidation notification through BroadcastChannel, or a non-secret storage event when channels are unavailable. They fetch the current server state. Focus, visibility, pageshow and rejected requests also reconcile it. No JavaScript lease or credential is stored to elect a renewal owner; the server's stable-renewal semantics also work without Web Locks.

Bootstrap responses are associated with a local revision. Responses from before a mutation/notification cannot replace newer state. A delayed 401/403 only probes the shared cookie; it never posts logout. A failed revocation cannot silently clear a still-valid local session. Account/session changes force a full reload to discard private editor/clipboard/resource state. If beforeunload cancels reload, unsafe requests and pending TUS chunks remain blocked. An old CSRF proof/generation also cannot write under a new cookie at the server.

Fetch and ordinary XHR writes send cookie-bound proofs. TUS checks its original session before every creation/chunk/termination request and sets each proof exactly once in the request hook. A real browser test exposed duplicate headers being appended by XMLHttpRequest when both static headers and the hook were used; this is fixed and reproduced in the unit test. Downloads and media continue to authenticate through the cookie without URL credentials. Public shares retain their distinct grant cookies and public-link semantics.

Machine login explicitly opts in with `X-FB-Client: api`, with no cookies or browser metadata. API JWTs use a separate audience and persisted kind; they are accepted only through X-Auth. Their renewal still consumes the old token. Existing pre-migration JWTs require a new login. This is an authentication-protocol change; it does not introduce scoped machine credentials or a session-management UI.

## Verification

| Check | Result |
| --- | --- |
| Go vet, build; macOS arm64 Go 1.27.3 race suite | PASS; 372 tests/subtests, no race reports |
| Linux arm64 Go 1.27.3 disposable toolchain container | PASS; 372 tests/subtests, one existing read-only-permission test skipped because the container runs as root |
| Windows amd64 HTTP and Bolt test binaries | Cross-compilation passes; native Windows runtime not tested |
| Frontend frozen install, lint, typecheck/build, Vitest | PASS; 53 tests |
| Chromium, two actual tabs with non-root BaseURL | PASS with Web Locks/BroadcastChannel and with both disabled, using storage notifications |
| Browser paths exercised | UI login in one tab updates the other; both storage JWTs removed; cookie inaccessible through document.cookie; concurrent renewal; missing-CSRF write rejected; real UI multichunk TUS upload; raw download; UI logout revokes both tabs |
| Protocol/storage regressions | HTTP/TLS cookie attributes; direct/trusted/forged forwarding; BaseURL binding; Origin/Referer/Fetch Metadata; duplicate cookies; browser/API separation; password-change revocation; idle/absolute deadlines; restart persistence; lost login response; stale generation; proxy identity and no-auth mode |
| Production dependency audit | pnpm audit --prod: zero reported advisories; existing project audit exceptions unchanged |

The initial browser-protocol tests failed against the old SPA fallback, before the endpoint was wired. The browser harness creates its own temporary database/files and a loopback-only server, then removes them. Run it after building the frontend and binary with an installed Playwright module:

```sh
NODE_PATH=/path/to/node_modules node frontend/e2e/browser-sessions.cjs /path/to/filebrowser-ng
```

Detailed local outputs are ignored under `.local/http-only-sessions-2026-10-29/`. No production session, user credential, database or file was used.

## Remaining work

Verify the actual Ugreen filesystem and HTTPS reverse-proxy peer configuration before any future rollout; this turn did not change them. Native Windows, Safari/Firefox, mobile browser behavior and external identity-provider logout remain additional rollout checks. HttpOnly reduces credential theft through JavaScript; it does not prevent same-origin malicious script from issuing authenticated actions. Effective application script CSP remains P0.3. Session inventory/revoke-all UI, scoped machine tokens, recent reauthentication and MFA remain P1; they are not implied by this implementation.

Cookie and CSRF choices were checked against primary [OWASP session guidance](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html) and [CSRF guidance](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html) on 2026-10-29. The stable-renewal, challenge-family and cross-tab protocol are project-specific design decisions, covered by the tests above.
