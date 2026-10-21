import { beforeEach, expect, it, vi } from "vitest";

const { fetchURL } = vi.hoisted(() => ({ fetchURL: vi.fn() }));
vi.mock("@/api/utils", () => ({
  fetchURL,
  removePrefix: (url: string) => url.replace("/files", ""),
  StatusError: class extends Error {},
}));
vi.mock("@/stores/auth", () => ({ useAuthStore: vi.fn() }));
vi.mock("@/stores/layout", () => ({ useLayoutStore: vi.fn() }));
vi.mock("@/utils/constants", () => ({ baseURL: "" }));
vi.mock("@/api/tus", () => ({ upload: vi.fn(), useTus: vi.fn() }));
vi.mock("@/utils/encodings", () => ({ isEncodableResponse: () => false }));
import { fetch as fetchFile, put } from "../files";

beforeEach(() => vi.clearAllMocks());

it("retains the server version with the loaded editor contents", async () => {
  fetchURL.mockResolvedValue(
    new Response(JSON.stringify({ isDir: false, content: "text" }), {
      headers: { "Content-Type": "application/json", ETag: '"version-1"' },
    })
  );
  const loaded = await fetchFile("/files/document.txt");
  expect(loaded.etag).toBe('"version-1"');
  expect(loaded.content).toBe("text");
});

it("sends a conditional save, including an intentionally empty file", async () => {
  fetchURL.mockResolvedValue(
    new Response("", { headers: { ETag: '"version-2"' } })
  );
  const response = await put("/files/document.txt", "", '"version-1"');
  expect(fetchURL).toHaveBeenCalledWith("/api/resources/document.txt", {
    method: "PUT",
    body: "",
    headers: { "If-Match": '"version-1"' },
  });
  expect(response.headers.get("ETag")).toBe('"version-2"');
});

it("propagates a conflict so the editor retains unsaved changes", async () => {
  fetchURL.mockRejectedValue({ status: 412 });
  await expect(
    put("/files/document.txt", "stale", '"version-1"')
  ).rejects.toMatchObject({ status: 412 });
});
