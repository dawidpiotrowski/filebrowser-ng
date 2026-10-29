// Build the frontend and binary first. Run with an installed playwright module:
// NODE_PATH=/path/to/node_modules node frontend/e2e/browser-sessions.cjs /path/to/filebrowser-ng
// This creates its own disposable database and loopback-only server.
const assert = require("node:assert/strict");
const { spawn, execFileSync } = require("node:child_process");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const net = require("node:net");
const { chromium } = require("playwright");

async function main() {
  const binary = path.resolve(process.argv[2]);
  const temp = await fs.mkdtemp(path.join(os.tmpdir(), "fb-ng-sessions-"));
  const root = path.join(temp, "files"),
    db = path.join(temp, "test.db");
  await fs.mkdir(root);
  await fs.writeFile(path.join(root, "hello.txt"), "private fixture");
  const cli = (args) =>
    execFileSync(binary, ["--database", db, ...args], { stdio: "pipe" });
  const password = "Disposable-Browser-Password!";
  cli([
    "config",
    "init",
    "--root",
    root,
    "--baseURL",
    "/browser",
    "--disableThumbnails",
    "--tus.chunkSize",
    "5",
  ]);
  cli(["users", "add", "alice", password, "--perm.admin"]);
  const socket = net.createServer();
  await new Promise((resolve) => socket.listen(0, "127.0.0.1", resolve));
  const port = socket.address().port;
  await new Promise((resolve) => socket.close(resolve));
  const url = `http://127.0.0.1:${port}/browser`;
  const server = spawn(
    binary,
    ["--database", db, "--address", "127.0.0.1", "--port", String(port)],
    { stdio: "ignore" }
  );
  let browser;
  try {
    for (let n = 0; n < 100; n++) {
      try {
        if ((await fetch(`${url}/api/session`)).ok) break;
      } catch {}
      if (server.exitCode !== null) throw new Error("Fixture server exited");
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
    browser = await chromium.launch({ headless: true });
    for (const fallback of [false, true]) {
      const context = await browser.newContext();
      await context.addInitScript((fallback) => {
        if (fallback) {
          Object.defineProperty(navigator, "locks", { value: undefined });
          Object.defineProperty(window, "BroadcastChannel", {
            value: undefined,
          });
        }
        if (!sessionStorage.getItem("seeded")) {
          localStorage.setItem("jwt", "legacy-credential-must-be-removed");
          sessionStorage.setItem("jwt", "legacy-credential-must-be-removed");
          sessionStorage.setItem("seeded", "yes");
        }
      }, fallback);
      const errors = [];
      const a = await context.newPage(),
        b = await context.newPage();
      for (const page of [a, b]) {
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("request", (r) => {
          assert.equal(r.headers()["x-auth"], undefined);
          assert.equal(new URL(r.url()).searchParams.has("auth"), false);
        });
        await page.goto(`${url}/login`);
        await page.locator('#login input[type="text"]').waitFor();
      }
      await a.locator('#login input[type="text"]').fill("alice");
      await a.locator('#login input[type="password"]').fill(password);
      await a.locator('#login input[type="submit"]').click();
      await Promise.all([
        a.waitForURL(`${url}/files/`),
        b.waitForURL(`${url}/files/`),
      ]);
      for (const page of [a, b]) {
        const visible = await page.evaluate(() => ({
          cookie: document.cookie,
          local: localStorage.getItem("jwt"),
          session: sessionStorage.getItem("jwt"),
        }));
        assert.equal(visible.local, null);
        assert.equal(visible.session, null);
        assert.equal(visible.cookie.includes("fb_session"), false);
        assert.equal(visible.cookie.includes("fb_login"), false);
      }
      const cookie = (await context.cookies()).find((c) =>
        c.name.includes("fb_session")
      );
      assert(cookie?.httpOnly);
      assert.equal(cookie.sameSite, "Strict");
      const renewal = (page) =>
        page.evaluate(async (url) => {
          const state = await (await fetch(`${url}/api/session`)).json();
          const res = await fetch(`${url}/api/renew`, {
            method: "POST",
            headers: {
              "X-FB-CSRF": state.csrfToken,
              "X-FB-Session": state.sessionVersion,
            },
          });
          return {
            status: res.status,
            version: (await res.json()).sessionVersion,
          };
        }, url);
      const [ra, rb] = await Promise.all([renewal(a), renewal(b)]);
      assert.equal(ra.status, 200);
      assert.equal(rb.status, 200);
      assert.equal(ra.version, rb.version);
      assert.equal(
        await a.evaluate(
          async (url) =>
            (
              await fetch(`${url}/api/resources/blocked.txt`, {
                method: "POST",
                body: "blocked",
              })
            ).status,
          url
        ),
        403
      );
      const name = `upload-${fallback ? "fallback" : "modern"}.txt`;
      const written = a.waitForResponse(
        (r) =>
          r.request().method() === "PATCH" &&
          r.url().includes(name) &&
          r.headers()["upload-offset"] === "17"
      );
      await a.locator("#upload-input").setInputFiles({
        name,
        mimeType: "text/plain",
        buffer: Buffer.from("browser tus bytes"),
      });
      await written;
      assert.equal(
        await a.evaluate(
          async ({ url, name }) =>
            (await fetch(`${url}/api/raw/${name}`)).text(),
          { url, name }
        ),
        "browser tus bytes"
      );
      assert.equal(
        await a.evaluate(
          async (url) => (await fetch(`${url}/api/raw/hello.txt`)).text(),
          url
        ),
        "private fixture"
      );
      // The real logout button exercises the client lock, channel and storage fallback.
      await a.locator("#logout").dispatchEvent("click");
      await Promise.all([a.waitForURL(/\/login/), b.waitForURL(/\/login/)]);
      assert.equal(
        await b.evaluate(
          async (url) => (await fetch(`${url}/api/resources/`)).status,
          url
        ),
        401
      );
      assert.deepEqual(errors, []);
      await context.close();
      console.log(
        `PASS two tabs: ${fallback ? "storage fallback, no Web Locks" : "BroadcastChannel + Web Locks"}; login, storage cleanup, HttpOnly, CSRF, renewal, TUS, download, logout`
      );
    }
  } finally {
    if (browser) await browser.close();
    server.kill("SIGTERM");
    await new Promise((resolve) => {
      if (server.exitCode !== null || server.signalCode) resolve();
      else server.once("exit", resolve);
    });
    await fs.rm(temp, { recursive: true, force: true });
  }
}
main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
