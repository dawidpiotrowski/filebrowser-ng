import { describe, expect, it, vi } from "vitest";
import { createSessionClient, type BrowserSession } from "../session";

const anonymous = (): BrowserSession => ({
  authenticated: false,
  user: null,
  csrfToken: "",
  loginCSRF: "challenge-proof",
  sessionVersion: "",
  expires: 0,
  absoluteExpires: 0,
});
const authenticated = (version = "alice"): BrowserSession => ({
  ...anonymous(),
  authenticated: true,
  user: { username: version } as IUser,
  csrfToken: `csrf-${version}`,
  sessionVersion: version,
  expires: 2000000000,
  absoluteExpires: 2000086400,
});
function fixture() {
  let state = authenticated();
  const calls: { path: string; init: RequestInit }[] = [];
  const fetch = vi.fn(
    async (url: string | URL | Request, init: RequestInit = {}) => {
      const path = String(url).split("/").pop()!;
      calls.push({ path, init });
      if (path === "session") return Response.json(state);
      if (path === "logout") {
        state = anonymous();
        return new Response(null, { status: 204 });
      }
      if (path === "login") state = authenticated("bob");
      return Response.json(state);
    }
  );
  const makeClient = () =>
    createSessionClient({
      baseURL: "/browser",
      fetch,
      changed: vi.fn(),
      notify: vi.fn(),
    });
  return {
    makeClient,
    calls,
    fetch,
    setState(next: BrowserSession) {
      state = next;
    },
  };
}

describe("HttpOnly session coordination", () => {
  it("uses cookies and CSRF, never a bearer credential", async () => {
    const f = fixture(),
      client = f.makeClient();
    await client.renew();
    const renewal = f.calls.find((c) => c.path === "renew")!;
    expect(renewal.init.credentials).toBe("same-origin");
    expect(renewal.init.headers).toMatchObject({
      "X-FB-CSRF": "csrf-alice",
      "X-FB-Session": "alice",
    });
    expect(JSON.stringify(f.calls)).not.toContain("X-Auth");
  });
  it("deduplicates renewal in one tab", async () => {
    const f = fixture(),
      client = f.makeClient();
    await Promise.all([client.renew(), client.renew()]);
    expect(f.calls.filter((c) => c.path === "renew")).toHaveLength(1);
  });
  it("two tabs renew concurrently without invalidating proofs", async () => {
    const f = fixture(),
      a = f.makeClient(),
      b = f.makeClient();
    await Promise.all([a.bootstrap(), b.bootstrap()]);
    await Promise.all([a.renew(), b.renew()]);
    expect(await a.mutationHeaders()).toEqual(await b.mutationHeaders());
  });
  it("another tab reconciles logout using the shared cookie, not a message payload", async () => {
    const f = fixture(),
      a = f.makeClient(),
      b = f.makeClient();
    await Promise.all([a.bootstrap(), b.bootstrap()]);
    await a.logout();
    await b.synchronize();
    expect(b.current()?.authenticated).toBe(false);
    await expect(b.mutationHeaders()).rejects.toMatchObject({ status: 401 });
  });
  it("keeps state if revocation fails and does not announce success", async () => {
    const f = fixture(),
      client = f.makeClient();
    await client.bootstrap();
    const original = f.fetch.getMockImplementation()!;
    f.fetch.mockImplementation(async (url, init) => {
      if (String(url).endsWith("logout")) throw new Error("offline");
      return original(url, init);
    });
    await expect(client.logout()).rejects.toThrow("offline");
    expect(client.current()?.authenticated).toBe(true);
  });
  it("does not overwrite a new login with a delayed bootstrap response", async () => {
    const f = fixture(),
      client = f.makeClient();
    let finish!: (r: Response) => void;
    f.fetch.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        })
    );
    const old = client.bootstrap();
    f.setState(authenticated("bob"));
    await client.synchronize();
    finish(Response.json(authenticated("alice")));
    await old;
    expect(client.current()?.sessionVersion).toBe("bob");
  });
  it("does not clear a new login after a delayed logout response", async () => {
    const f = fixture(),
      client = f.makeClient();
    await client.bootstrap();
    let finish!: (r: Response) => void;
    const original = f.fetch.getMockImplementation()!;
    f.fetch.mockImplementation(async (url, init) => {
      if (String(url).endsWith("logout"))
        return new Promise((resolve) => {
          finish = resolve;
        });
      return original(url, init);
    });
    const pending = client.logout();
    await vi.waitFor(() => expect(finish).toBeTypeOf("function"));
    f.setState(authenticated("bob"));
    finish(new Response(null, { status: 204 }));
    await pending;
    expect(client.current()?.sessionVersion).toBe("bob");
    await expect(client.mutationHeaders()).rejects.toMatchObject({
      status: 401,
    });
  });
  it("blocks queued uploads after account switching even if reload is cancelled", async () => {
    const f = fixture(),
      client = f.makeClient();
    await client.bootstrap();
    f.setState(authenticated("bob"));
    await client.synchronize();
    await expect(client.mutationHeaders("alice")).rejects.toMatchObject({
      status: 401,
    });
    await expect(client.mutationHeaders()).rejects.toMatchObject({
      status: 401,
    });
  });
  it("serializes logout after in-flight renewal and revokes the shared session", async () => {
    const f = fixture(),
      client = f.makeClient();
    await Promise.all([client.renew(), client.logout()]);
    expect(
      f.calls.filter((c) => c.path !== "session").map((c) => c.path)
    ).toEqual(["renew", "logout"]);
    expect(client.current()?.authenticated).toBe(false);
  });
  it("uses Web Locks when available, with bounded waiting", async () => {
    const f = fixture();
    const request = vi.fn(async (_name, _options, run) => run());
    const client = createSessionClient({
      baseURL: "/b",
      fetch: f.fetch,
      locks: { request },
      changed: vi.fn(),
      notify: vi.fn(),
    });
    await client.renew();
    expect(request).toHaveBeenCalledWith(
      "filebrowser-ng/session:/b",
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
      expect.any(Function)
    );
  });
  it("does not extend idle expiry on background probes", async () => {
    const f = fixture(),
      client = f.makeClient();
    await client.synchronize();
    await client.synchronize();
    expect(f.calls.every((c) => c.path === "session")).toBe(true);
  });
  it("ignores expiry hints from another account and caps trusted hints", async () => {
    const f = fixture(),
      client = f.makeClient();
    await client.bootstrap();
    client.observeExpiry("bob", 3000000000);
    expect(client.current()?.expires).toBe(2000000000);
    client.observeExpiry("alice", 3000000000);
    expect(client.current()?.expires).toBe(2000086400);
  });
});
