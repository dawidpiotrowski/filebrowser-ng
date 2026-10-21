import { beforeEach, expect, it, vi } from "vitest";
import type { UploadOptions } from "tus-js-client";

const { store, uploads } = vi.hoisted(() => ({
  store: { jwt: "old" },
  uploads: [] as UploadOptions[],
}));
vi.mock("@/stores/auth", () => ({ useAuthStore: () => store }));
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
  store.jwt = "old";
  uploads.length = 0;
});

it("uses the current session for each subsequent TUS request", async () => {
  const pending = upload(
    "/api/resources/file.txt",
    new Blob(["hello"]),
    false,
    undefined
  );
  const options = uploads[0];
  store.jwt = "renewed";
  const headers = new Headers(options.headers);
  await options.onBeforeRequest?.({
    setHeader: (name, value) => headers.set(name, value),
  } as Parameters<NonNullable<UploadOptions["onBeforeRequest"]>>[0]);
  expect(headers.get("X-Auth")).toBe("renewed");
  options.onSuccess?.(
    {} as Parameters<NonNullable<UploadOptions["onSuccess"]>>[0]
  );
  await pending;
});
