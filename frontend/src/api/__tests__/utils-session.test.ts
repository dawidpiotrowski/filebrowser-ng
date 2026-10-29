import { beforeEach, describe, expect, it, vi } from "vitest";
const { mutationHeaders, observeSessionResponse, sessionVersion } = vi.hoisted(
  () => ({
    mutationHeaders: vi.fn(),
    observeSessionResponse: vi.fn(),
    sessionVersion: vi.fn(() => "alice"),
  })
);
vi.mock("@/utils/auth", () => ({
  mutationHeaders,
  observeSessionResponse,
  sessionVersion,
}));
vi.mock("@/utils/constants", () => ({ baseURL: "" }));
import { fetchURL } from "../utils";

describe("cookie API requests", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mutationHeaders.mockResolvedValue({
      "X-FB-CSRF": "proof",
      "X-FB-Session": "alice",
    });
  });
  it("protects writes without exposing an authentication header", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response("ok"));
    vi.stubGlobal("fetch", fetch);
    await fetchURL("/api/resources/a", {
      method: "PUT",
      headers: { "If-Match": '"v1"' },
    });
    const init = fetch.mock.calls[0][1];
    expect(init.credentials).toBe("same-origin");
    expect(init.headers.get("X-FB-CSRF")).toBe("proof");
    expect(init.headers.get("If-Match")).toBe('"v1"');
    expect(init.headers.has("X-Auth")).toBe(false);
  });
  it("associates a delayed response with its original public generation", async () => {
    let finish!: (r: Response) => void;
    vi.stubGlobal(
      "fetch",
      vi.fn(
        () =>
          new Promise((resolve) => {
            finish = resolve;
          })
      )
    );
    const pending = fetchURL("/api/resources/", {});
    const res = new Response("expired", { status: 401 });
    finish(res);
    await expect(pending).rejects.toMatchObject({ status: 401 });
    expect(observeSessionResponse).toHaveBeenCalledWith(res, "alice");
  });
  it("does not send session proofs or reconcile public share responses", async () => {
    const fetch = vi
      .fn()
      .mockResolvedValue(new Response("denied", { status: 403 }));
    vi.stubGlobal("fetch", fetch);
    await expect(fetchURL("/api/public/dl/a", {}, false)).rejects.toMatchObject(
      { status: 403 }
    );
    expect(mutationHeaders).not.toHaveBeenCalled();
    expect(observeSessionResponse).not.toHaveBeenCalled();
    expect([...fetch.mock.calls[0][1].headers]).toEqual([]);
  });
  it("blocks requests before transmission when an account change requires reload", async () => {
    mutationHeaders.mockRejectedValue(new Error("reload required"));
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    await expect(
      fetchURL("/api/resources/a", { method: "DELETE" })
    ).rejects.toThrow("reload required");
    expect(fetch).not.toHaveBeenCalled();
  });
});
