import { useAuthStore } from "@/stores/auth";
import { baseURL, noAuth, logoutPage } from "./constants";
import { createSessionClient, type BrowserSession } from "./session";

let client: ReturnType<typeof createSessionClient> | undefined;
let channel: BroadcastChannel | undefined;
let navigationStarted = false;
let manualLogoutReason: string | undefined;
const noticeKey = `filebrowser-ng/session:${baseURL}`;

function sessionClient() {
  if (client) return client;
  // Never inspect or migrate a bearer credential into the new session.
  for (const storage of ["localStorage", "sessionStorage"] as const) {
    try {
      window[storage].removeItem("jwt");
    } catch {
      /* Storage may be disabled. */
    }
  }
  client = createSessionClient({
    baseURL,
    fetch: (...args) => fetch(...args),
    locks: navigator.locks,
    changed: applySession,
    notify() {
      if (channel) channel.postMessage("changed");
      else {
        try {
          localStorage.setItem(noticeKey, `${Date.now()}:${Math.random()}`);
        } catch {
          /* Focus and 401 also reconcile. */
        }
      }
    },
  });
  const sync = () => {
    void client!.synchronize().catch(() => {});
  };
  try {
    if (typeof BroadcastChannel !== "undefined") {
      channel = new BroadcastChannel(noticeKey);
      channel.onmessage = sync;
    }
  } catch {
    /* Some browser policies disable channels. */
  }
  if (!channel) {
    window.addEventListener("storage", (event) => {
      if (event.key === noticeKey) sync();
    });
  }
  window.addEventListener("focus", sync);
  window.addEventListener("pageshow", sync);
  document.addEventListener("visibilitychange", () => {
    if (!document.hidden) sync();
  });
  return client;
}

function loginLocation(reason?: string) {
  if (navigationStarted) return;
  navigationStarted = true;
  if (noAuth) window.location.reload();
  else if (logoutPage !== "/login") window.location.assign(logoutPage);
  else
    window.location.assign(
      `${baseURL}/login${reason ? `?logout-reason=${encodeURIComponent(reason)}` : ""}`
    );
}

function applySession(state: BrowserSession, reload: boolean) {
  const store = useAuthStore();
  if (store.logoutTimer !== null) clearTimeout(store.logoutTimer);
  store.setUser(state.user);
  store.sessionVersion = state.sessionVersion;
  store.csrfToken = state.csrfToken;
  store.logoutTimer = null;
  if (reload) {
    // Discard private resource/clipboard/editor state from the previous account.
    if (state.authenticated) {
      if (!navigationStarted) {
        navigationStarted = true;
        window.location.reload();
      }
    } else loginLocation(manualLogoutReason || "inactivity");
    return;
  }
  if (state.authenticated) {
    store.logoutTimer = window.setTimeout(
      () => {
        // A probe never prolongs idle expiry. Another active tab may have done so.
        void sessionClient()
          .synchronize()
          .catch(() => {});
      },
      Math.max(5000, state.expires * 1000 - Date.now())
    );
  }
}

export const validateLogin = () => sessionClient().bootstrap();
export const reconcileSession = () => sessionClient().synchronize();
export const sessionVersion = () =>
  sessionClient().current()?.sessionVersion || "";
export const mutationHeaders = (expectedVersion?: string) =>
  sessionClient().mutationHeaders(expectedVersion);
export const renew = () => sessionClient().renew();
export const login = (username: string, password: string, recaptcha: string) =>
  sessionClient().login({ username, password, recaptcha });
export const signup = (username: string, password: string) =>
  sessionClient().signup({ username, password });

export async function logout(reason?: string) {
  manualLogoutReason = typeof reason === "string" ? reason : undefined;
  const state = await sessionClient().logout();
  if (!state.authenticated) loginLocation(reason);
}

export function observeSessionResponse(response: Response, version: string) {
  const expires = response.headers.get("X-Session-Expires");
  if (expires)
    sessionClient().observeExpiry(version, Date.parse(expires) / 1000);
  if (
    response.status === 401 ||
    response.status === 403 ||
    response.headers.get("X-Renew-Token") === "true"
  ) {
    void reconcileSession().catch(() => {});
  }
}
