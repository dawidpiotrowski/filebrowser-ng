import { beforeEach, describe, expect, it, vi } from "vitest";

const { store, renew, logout } = vi.hoisted(() => ({
  store: { jwt: "old" },
  renew: vi.fn(),
  logout: vi.fn(),
}));
vi.mock("@/stores/auth", () => ({ useAuthStore: () => store }));
vi.mock("@/utils/auth", () => ({ renew, logout }));
vi.mock("@/utils/constants", () => ({ baseURL: "" }));
import { fetchURL } from "../utils";

describe("responses from an earlier browser session", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    logout.mockResolvedValue(undefined);
    store.jwt = "old";
  });

  it("preserves the HTTP error if automatic revocation fails", async () => {
    logout.mockRejectedValueOnce(new Error("offline"));
    const warning = vi.spyOn(console, "warn").mockImplementation(() => {});
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response("expired", { status: 401 }))
    );
    await expect(fetchURL("/api/resources/", {})).rejects.toMatchObject({
      status: 401,
    });
    expect(warning).toHaveBeenCalledOnce();
    warning.mockRestore();
  });

  it("associates a renewal signal with the token actually sent", async () => {
    let finish!: (response: Response) => void;
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise((resolve) => (finish = resolve)))
    );
    const pending = fetchURL("/api/resources/", {});
    store.jwt = "new";
    finish(new Response("{}", { headers: { "X-Renew-Token": "true" } }));
    await pending;
    expect(renew).toHaveBeenCalledWith("old");
  });

  it("limits automatic logout to the rejected request's session", async () => {
    let finish!: (response: Response) => void;
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise((resolve) => (finish = resolve)))
    );
    const pending = fetchURL("/api/resources/", {});
    store.jwt = "new";
    finish(new Response("expired", { status: 401 }));
    await expect(pending).rejects.toMatchObject({ status: 401 });
    expect(logout).toHaveBeenCalledWith(undefined, "old");
  });
});
