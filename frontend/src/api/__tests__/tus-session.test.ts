import { beforeEach, expect, it, vi } from "vitest";
import type { UploadOptions } from "tus-js-client";
const { mutationHeaders, uploads } = vi.hoisted(() => ({
  mutationHeaders: vi.fn(),
  uploads: [] as UploadOptions[],
}));
vi.mock("@/utils/auth", () => ({
  mutationHeaders,
  reconcileSession: vi.fn(async () => {}),
}));
vi.mock("@/utils/constants", () => ({
  baseURL: "",
  origin: "https://files.example.test",
  tusEndpoint: "/api/tus",
  tusSettings: { chunkSize: 1024, retryCount: 2 },
}));
vi.mock("@/api/utils", () => ({ removePrefix: () => "/file.txt" }));
vi.mock("tus-js-client", () => ({
  Upload: class {
    constructor(_content: Blob, options: UploadOptions) {
      uploads.push(options);
    }
    start() {}
  },
}));
import { upload } from "../tus";
beforeEach(() => {
  uploads.length = 0;
  mutationHeaders
    .mockReset()
    .mockResolvedValue({ "X-FB-CSRF": "proof", "X-FB-Session": "alice" });
});
it("binds all chunks to the original session, using CSRF instead of a bearer", async () => {
  const pending = upload(
    "/api/resources/file.txt",
    new Blob(["hello"]),
    false,
    undefined
  );
  await vi.waitFor(() => expect(uploads).toHaveLength(1));
  const options = uploads[0],
    headers = new Headers(options.headers);
  await options.onBeforeRequest?.({
    setHeader: (name, value) => headers.append(name, value),
  } as Parameters<NonNullable<UploadOptions["onBeforeRequest"]>>[0]);
  expect(mutationHeaders).toHaveBeenLastCalledWith("alice");
  expect(headers.get("X-FB-CSRF")).toBe("proof");
  expect(headers.has("X-Auth")).toBe(false);
  mutationHeaders.mockRejectedValueOnce(
    new Error("Session changed; reload before continuing")
  );
  await expect(
    options.onBeforeRequest?.({ setHeader: vi.fn() } as never)
  ).rejects.toThrow("Session changed");
  options.onSuccess?.(
    {} as Parameters<NonNullable<UploadOptions["onSuccess"]>>[0]
  );
  await pending;
});
it("never retries unauthorized chunks", async () => {
  const pending = upload("/file.txt", new Blob(["hello"]), false, undefined);
  await vi.waitFor(() => expect(uploads).toHaveLength(1));
  const options = uploads[0];
  for (const status of [401, 403, 409]) {
    expect(
      options.onShouldRetry?.(
        { originalResponse: { getStatus: () => status } } as never,
        0,
        {} as never
      )
    ).toBe(false);
  }
  options.onSuccess?.({} as never);
  await pending;
});
