# Authentication

JSON username/password authentication is the default:

```sh
./filebrowser-ng --database ./filebrowser.db config set --auth.method=json
```

## Browser sessions

The browser uses an opaque, host-only `HttpOnly; SameSite=Strict` cookie. No login bearer token is returned to JavaScript, stored in localStorage/sessionStorage or added to download URLs. Startup deletes the old `jwt` storage entries without reading them; users must sign in again. Other local UI preferences are retained.

`GET /api/session` returns sanitized user information, a CSRF proof, a non-authenticating session version and expiry times. Responses are `Cache-Control: no-store`. The session secret is generated from 32 random bytes; only its SHA-256 identifier is persisted in the database. Password/username changes invalidate existing sessions, including changes through the CLI. Session creation, refresh and revocation persist across server restarts.

The default idle lifetime is two hours, configured with `tokenExpirationTime`, capped by a fixed 24-hour absolute lifetime. Authenticated application/media activity extends idle expiry when less than half remains. Background bootstrap probes do not extend it. Browser renewal keeps the same cookie and CSRF proof; simultaneous renewals and a lost renewal response cannot consume another tab's credential. Login creates a new credential and revokes the preceding browser session; logins sharing a challenge also revoke earlier responses that might have been lost.

Every unsafe cookie-authenticated request requires `X-FB-CSRF` and `X-FB-Session` from the bootstrap response. Login/signup use `loginCSRF` instead. These proofs are bound to the session/challenge and BaseURL; they cannot authenticate without the HttpOnly cookie. Origin/Referer and Fetch Metadata reject foreign origins, including sibling subdomains. Clients without browser metadata still need the proofs. Public share downloads retain their separate access rules.

Tabs serialize login/logout/renewal through Web Locks where available and broadcast only a change notification. The receiver reads the authoritative shared cookie state from the server. Without Web Locks, stable renewal remains safe; without BroadcastChannel, a non-secret localStorage notification is used. Focus, visibility and authentication failures also reconcile state. Old 401 responses never issue logout. Account/session changes trigger a full reload to discard private cached resources and clipboard/editor state; writes and queued TUS chunks are blocked if reload is cancelled.

Logout revokes the server record. Its response deliberately does not clear or replace the browser credential cookie: a delayed response must not erase a subsequent login. The remaining opaque cookie is unusable after revocation and is replaced at the next login or expires at the absolute deadline. Failed revocation reports an error instead of pretending the session ended.

## HTTPS proxies and LAN HTTP

Direct TLS uses `Secure` cookies with the `__Host-` prefix and `Path=/`. Cookie names include a BaseURL namespace so installations under different prefixes do not collide. Plain LAN HTTP uses unprefixed cookies scoped to the BaseURL path; it has no transport confidentiality.

When TLS ends at a reverse proxy, explicitly trust its actual TCP peer CIDRs:

```sh
./filebrowser-ng --database ./filebrowser.db config set \
  --trustedProxies=127.0.0.1/32,::1/128
```

The proxy must preserve the external `Host` and overwrite `X-Forwarded-Proto` with `https`. Only peers in `trustedProxies` may assert the TLS scheme; forwarded client IP/host headers are not used for this decision. Set the actual proxy/container network, not these example loopback addresses. An invalid CIDR prevents startup. The `auth.trustedProxies` allowlist also supplies this trust when proxy authentication is configured. With JSON authentication, configure the separate `trustedProxies` option before a future HTTPS-proxy rollout, otherwise the origin check rejects mismatched HTTPS requests. This local change does not alter any deployed proxy or NAS.

## Machine API clients

Machine clients explicitly send `X-FB-Client: api` when posting credentials to `/api/login`, without cookies, Origin, Referer or Fetch Metadata headers. The response body is a short-lived JWT for `X-Auth`; no browser cookie is issued. It has the API-specific audience `filebrowser-ng/api` and a persisted API session. Existing tokens without this audience/kind must be replaced by logging in again.

API renewal consumes the old JWT; retain the replacement and serialize renewals. Logout is `POST /api/logout` with `X-Auth`. Supplying both a browser session cookie and `X-Auth` is rejected. A browser cannot exchange its HttpOnly session for a readable machine bearer. API tokens currently retain the account's permissions; separately scoped credentials and a session-management UI remain planned.

Login, signup and share-password checks bound password work. Peer rate limits allow an initial burst of 20 and refill one attempt per second. Behind a proxy this budget is shared by its visitors; configure suitable edge limits as well.

## Proxy authentication and shares

Proxy authentication requires an explicit identity-proxy allowlist:

```sh
./filebrowser-ng --database ./filebrowser.db config set \
  --auth.method=proxy --auth.header=X-Authenticated-User \
  --auth.trustedProxies=127.0.0.1/32,::1/128
```

The proxy must authenticate visitors and overwrite identity headers. CIDRs refer to `RemoteAddr`, never `X-Forwarded-For`; an empty allowlist rejects authentication. The same identity must be asserted on every authenticated request, for browser and machine sessions. Idle and absolute browser expiry remain enforced with a custom logout URL.

Hook authentication is removed. No-auth mode remains an intentional administrator option for isolated environments; it provides no user authentication.

Password-protected public shares exchange the password for a 15-minute, share-scoped HttpOnly cookie, capped by share expiry. The legacy permanent `?token=` bypass is no longer accepted. Non-password public links retain their access semantics.
