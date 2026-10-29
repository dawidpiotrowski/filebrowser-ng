import {
  mutationHeaders,
  observeSessionResponse,
  sessionVersion,
} from "@/utils/auth";
import { baseURL } from "@/utils/constants";
import { encodePath } from "@/utils/url";

import { StatusError } from "./errors";
export { StatusError } from "./errors";

export async function fetchURL(
  url: string,
  opts: ApiOpts,
  auth = true
): Promise<Response> {
  opts = opts || {};
  opts.headers = opts.headers || {};

  const { headers, ...rest } = opts;
  const version = auth ? sessionVersion() : "";
  const requestHeaders = new Headers(headers as HeadersInit);
  if (
    auth &&
    !["GET", "HEAD", "OPTIONS"].includes((rest.method || "GET").toUpperCase())
  ) {
    const proof = await mutationHeaders(version || undefined);
    for (const [name, value] of Object.entries(proof))
      requestHeaders.set(name, value);
  }
  let res;
  try {
    res = await fetch(`${baseURL}${url}`, {
      headers: requestHeaders,
      ...rest,
      credentials: "same-origin",
    });
  } catch (e) {
    // Check if the error is an intentional cancellation
    if (e instanceof Error && e.name === "AbortError") {
      throw new StatusError("000 No connection", 0, true);
    }
    throw new StatusError("000 No connection", 0);
  }

  if (auth) observeSessionResponse(res, version);

  if (res.status < 200 || res.status > 299) {
    const body = await res.text();
    const error = new StatusError(
      body || `${res.status} ${res.statusText}`,
      res.status
    );

    throw error;
  }

  return res;
}

export async function fetchJSON<T>(url: string, opts?: any): Promise<T> {
  const res = await fetchURL(url, opts);

  if (res.status === 200) {
    return res.json() as Promise<T>;
  }

  throw new StatusError(`${res.status} ${res.statusText}`, res.status);
}

export function removePrefix(url: string): string {
  url = url.split("/").splice(2).join("/");

  if (url === "") url = "/";
  if (url[0] !== "/") url = "/" + url;
  return url;
}

export function createURL(endpoint: string, searchParams = {}): string {
  let prefix = baseURL;
  if (!prefix.endsWith("/")) {
    prefix = prefix + "/";
  }
  const url = new URL(prefix + encodePath(endpoint), origin);
  url.search = new URLSearchParams(searchParams).toString();

  return url.toString();
}
