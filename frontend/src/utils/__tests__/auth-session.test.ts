import { beforeEach, describe, expect, it, vi } from "vitest";

const { store, push } = vi.hoisted(() => ({
  store: {
    jwt: "old",
    logoutTimer: null as number | null,
    setUser: vi.fn(),
    setLogoutTimer: vi.fn(),
    clearUser: vi.fn(),
  },
  push: vi.fn(),
}));
vi.mock("@/stores/auth", () => ({ useAuthStore: () => store }));
vi.mock("@/router", () => ({ default: { push } }));
vi.mock("jwt-decode", () => ({
  jwtDecode: () => ({ user: {}, exp: 2000000000 }),
}));
vi.mock("@/utils/constants", () => ({
  baseURL: "",
  noAuth: false,
  authMethod: "json",
  logoutPage: "/login",
}));
vi.mock("@/api/utils", () => ({
  StatusError: class extends Error {},
  setSafeTimeout: () => 1,
}));
import { logout, renew } from "../auth";

describe("server-side session lifecycle", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    store.jwt = "old";
    vi.stubGlobal("localStorage", {
      setItem: vi.fn(),
      getItem: vi.fn(() => "old"),
    });
  });
  it("serializes concurrent renewal requests", async () => {
    const fetch = vi
      .fn()
      .mockResolvedValue({ status: 200, text: async () => "new" });
    vi.stubGlobal("fetch", fetch);
    await Promise.all([renew("old"), renew("old")]);
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(store.jwt).toBe("new");
  });
  it("waits for server revocation before clearing local state", async () => {
    let finish!: (value: { ok: boolean; status: number }) => void;
    const fetch = vi.fn(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        })
    );
    vi.stubGlobal("fetch", fetch);
    const pending = logout();
    expect(store.clearUser).not.toHaveBeenCalled();
    finish({ ok: true, status: 204 });
    await pending;
    expect(fetch).toHaveBeenCalledWith(
      "/api/logout",
      expect.objectContaining({ method: "POST" })
    );
    expect(store.clearUser).toHaveBeenCalledTimes(1);
  });
  it("retains local state when server revocation fails", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("offline")));
    await expect(logout()).rejects.toThrow("offline");
    expect(store.clearUser).not.toHaveBeenCalled();
  });
});
