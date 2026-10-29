import { StatusError } from "@/api/errors";

// Only presentation data and a session-bound CSRF proof enter JavaScript.
// Authentication is exclusively the browser's HttpOnly cookie.
export interface BrowserSession {
  authenticated: boolean;
  user: IUser | null;
  csrfToken: string;
  loginCSRF: string;
  sessionVersion: string;
  expires: number;
  absoluteExpires: number;
}

interface Options {
  baseURL: string;
  fetch: typeof fetch;
  locks?: Pick<LockManager, "request">;
  changed: (state: BrowserSession, reload: boolean) => void;
  notify: () => void;
}

export function createSessionClient(options: Options) {
  let state: BrowserSession | null = null;
  let revision = 0;
  let seenVersion = "";
  let requiresReload = false;
  let pending: { revision: number; promise: Promise<BrowserSession> } | null =
    null;
  let tail: Promise<unknown> = Promise.resolve();
  let renewal: Promise<BrowserSession> | null = null;

  async function request(path: string, init: RequestInit = {}) {
    const res = await options.fetch(`${options.baseURL}/api/${path}`, {
      ...init,
      credentials: "same-origin",
      cache: "no-store",
      signal: AbortSignal.timeout(10000),
    });
    if (!res.ok) {
      throw new StatusError((await res.text()) || res.statusText, res.status);
    }
    return res;
  }

  function apply(next: BrowserSession) {
    if (
      (seenVersion &&
        (!next.authenticated || seenVersion !== next.sessionVersion)) ||
      (state !== null && !state.authenticated && next.authenticated)
    ) {
      // A cancelled beforeunload must not let an old editor write as a new user.
      requiresReload = true;
    }
    if (next.authenticated) seenVersion = next.sessionVersion;
    state = next;
    options.changed(next, requiresReload);
    return next;
  }

  function bootstrap(force = false): Promise<BrowserSession> {
    if (!force && state) return Promise.resolve(state);
    if (pending?.revision === revision) return pending.promise;
    const started = revision;
    const promise = request("session")
      .then((res) => res.json() as Promise<BrowserSession>)
      .then((next) => (started === revision ? apply(next) : bootstrap(true)))
      .finally(() => {
        if (pending?.promise === promise) pending = null;
      });
    pending = { revision: started, promise };
    return promise;
  }

  async function synchronize() {
    revision++;
    return bootstrap(true);
  }

  async function mutationHeaders(expectedVersion?: string) {
    const current = await bootstrap();
    if (
      requiresReload ||
      !current.authenticated ||
      !current.csrfToken ||
      (expectedVersion !== undefined &&
        expectedVersion !== current.sessionVersion)
    ) {
      throw new StatusError("Session changed; reload before continuing", 401);
    }
    return {
      "X-FB-CSRF": current.csrfToken,
      "X-FB-Session": current.sessionVersion,
    };
  }

  function mutate(
    action: "login" | "logout" | "renew" | "signup",
    data?: unknown
  ) {
    const run = async () => {
      // Invalidate bootstrap responses issued before this mutation. Refresh the
      // shared cookie's state inside the lock, never trust a channel payload.
      revision++;
      const current = await bootstrap(true);
      if (action === "logout" && !current.authenticated) return current;
      const headers =
        action === "login" || action === "signup"
          ? { "X-FB-CSRF": current.loginCSRF }
          : await mutationHeaders(current.sessionVersion);
      try {
        await request(action, {
          method: "POST",
          headers: { "Content-Type": "application/json", ...headers },
          body: data === undefined ? undefined : JSON.stringify(data),
        });
      } catch (error) {
        // Reconciliation is read-only: a delayed 401/403 never logs out a new
        // session. Network failure must not pretend revocation succeeded.
        await synchronize().catch(() => {});
        throw error;
      }
      options.notify();
      return synchronize();
    };
    const locked = async (): Promise<BrowserSession> =>
      options.locks
        ? await options.locks.request(
            `filebrowser-ng/session:${options.baseURL}`,
            {
              signal: AbortSignal.timeout(15000),
            },
            run
          )
        : run();
    const result = tail.then(locked);
    tail = result.catch(() => {});
    return result;
  }

  return {
    bootstrap,
    synchronize,
    mutationHeaders,
    current: () => state,
    login: (data: unknown) => mutate("login", data),
    signup: (data: unknown) => mutate("signup", data),
    logout: () => mutate("logout"),
    renew: () => {
      if (!renewal)
        renewal = mutate("renew").finally(() => {
          renewal = null;
        });
      return renewal;
    },
    observeExpiry(version: string, expires: number) {
      if (
        state?.authenticated &&
        state.sessionVersion === version &&
        Number.isFinite(expires) &&
        expires > state.expires
      ) {
        apply({ ...state, expires: Math.min(expires, state.absoluteExpires) });
      }
    },
  };
}
