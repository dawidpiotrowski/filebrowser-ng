# Authentication

JSON username/password authentication is the default:

```sh
./filebrowser-ng --database ./filebrowser.db config set --auth.method=json
```

Sessions are persisted in the database, have a two-hour default lifetime, rotate on renewal, and can be revoked by `POST /api/logout` using `X-Auth`. Password changes invalidate existing sessions even if made through the CLI. Authentication responses are not cacheable. Login, signup, and share-password checks have bounded password work; peer rate limits allow an initial burst of 20 and refill one attempt per second. Behind a proxy this budget is shared by requests from that peer, so also configure appropriate rate limits at the edge.

Proxy authentication requires an explicit allowlist of the proxy's TCP peer addresses:

```sh
./filebrowser-ng --database ./filebrowser.db config set \
  --auth.method=proxy --auth.header=X-Authenticated-User \
  --auth.trustedProxies=127.0.0.1/32,::1/128
```

The configured proxy must authenticate the visitor and overwrite identity headers. CIDRs refer to `RemoteAddr`, never `X-Forwarded-For`. Configure the actual proxy network for container deployments. An empty allowlist rejects authentication. The proxy must assert the same identity on every authenticated API request. JWT expiry remains enforced with a custom logout URL.

Hook authentication is removed. No-auth mode remains an intentional administrator option for isolated environments; it provides no user authentication.

Password-protected public shares exchange the password for a 15-minute, share-scoped HttpOnly cookie. The cookie's lifetime is capped by the share expiry. The legacy permanent `?token=` bypass is no longer accepted. Existing non-password public links retain their access semantics.
