#!/usr/bin/env python3
"""Exercise a disposable non-root image and, optionally, an ng.3 database upgrade."""
import argparse
import http.cookiejar
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import uuid


def docker(*args):
    return subprocess.check_output(["docker", *args], text=True, stderr=subprocess.PIPE).strip()


class Client:
    def __init__(self, url, browser=False):
        self.url = url
        self.opener = urllib.request.build_opener(
            *([urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar())] if browser else [])
        )
        self.state = None

    def request(self, method, path, body=None, headers=None, status=200):
        if isinstance(body, dict):
            body = json.dumps(body).encode()
            headers = {"Content-Type": "application/json", **(headers or {})}
        elif isinstance(body, str):
            body = body.encode()
        request = urllib.request.Request(self.url + path, body, headers or {}, method=method)
        try:
            response = self.opener.open(request, timeout=15)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            payload = response.read()
            assert response.status == status, f"{method} {path}: {response.status}, expected {status}"
            return response.headers, payload

    def login(self):
        _, payload = self.request("GET", "/api/session")
        assert not json.loads(payload)["authenticated"]
        proof = json.loads(payload)["loginCSRF"]
        headers, payload = self.request("POST", "/api/login", {"username": "release-user", "password": PASSWORD}, {"X-FB-CSRF": proof})
        self.state = json.loads(payload)
        assert self.state["authenticated"] and self.state["user"]["username"] == "release-user"
        assert headers["Cache-Control"] == "no-store"
        cookies = headers.get_all("Set-Cookie", [])
        assert any("fb_session_" in c and "HttpOnly" in c and "SameSite=Strict" in c for c in cookies)

    def proof(self):
        return {"X-FB-CSRF": self.state["csrfToken"], "X-FB-Session": self.state["sessionVersion"]}


PASSWORD = "Disposable-Release-Password!"


def smoke(args, uid):
    containers = []
    with tempfile.TemporaryDirectory(prefix="filebrowser-ng-release-") as temporary:
        root = Path(temporary)
        os.chmod(root, 0o777)
        for child in ["files", "database"]:
            (root / child).mkdir(mode=0o777)
            os.chmod(root / child, 0o777)
        platform = ["--platform", args.platform] if args.platform else []
        mounts = ["-v", f"{root / 'files'}:/srv", "-v", f"{root / 'database'}:/database"]
        flags = [*platform, "--user", uid, *mounts]
        initial = args.from_image or args.image

        def cli(*command):
            return docker("run", "--rm", *flags, initial, "--database=/database/filebrowser.db", *command)

        def start(image):
            name = "fb-ng-release-" + uuid.uuid4().hex[:12]
            container = docker("run", "-d", "--name", name, *flags, "--read-only", "--tmpfs", "/tmp:size=16m,mode=1777", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "-p", "127.0.0.1::8080", image)
            containers.append(container)
            address = docker("port", container, "8080/tcp").splitlines()[0]
            url = "http://" + address
            client = Client(url)
            for _ in range(60):
                try:
                    client.request("GET", "/health")
                    return container, url
                except (OSError, AssertionError):
                    time.sleep(0.25)
            raise AssertionError("Disposable container did not become healthy")

        try:
            cli("config", "init", "--root=/srv", "--disableThumbnails", "--fileMode=0o644")
            cli("users", "add", "release-user", PASSWORD, "--perm.admin")
            legacy_token = None
            share = None
            if args.from_image:
                old, url = start(initial)
                client = Client(url)
                _, payload = client.request("POST", "/api/login", {"username": "release-user", "password": PASSWORD}, {"X-FB-Client": "api"})
                legacy_token = payload.decode()
                client.request("POST", "/api/resources/upgrade.txt", "original from ng.3", {"X-Auth": legacy_token})
                _, payload = client.request("POST", "/api/share/upgrade.txt", {}, {"X-Auth": legacy_token})
                share = json.loads(payload)["hash"]
                docker("stop", old)
                # Copy through the daemon: the database is 0600 and owned by the
                # service UID, which need not equal the CI runner's UID.
                docker("cp", f"{old}:/database/filebrowser.db", str(root / "cold-backup.db"))
                assert (root / "cold-backup.db").stat().st_size > 0
            current, url = start(args.image)
            browser = Client(url, browser=True)
            browser.request("GET", "/api/resources/", status=401)
            if legacy_token:
                browser.request("GET", "/api/resources/", headers={"X-Auth": legacy_token}, status=401)
            browser.login()
            if share:
                _, payload = browser.request("GET", "/api/raw/upgrade.txt")
                assert payload == b"original from ng.3"
                _, payload = Client(url).request("GET", f"/api/public/dl/{share}")
                assert payload == b"original from ng.3"
            browser.request("POST", "/api/resources/smoke.txt", "initial", status=403)
            browser.request("POST", "/api/resources/smoke.txt", "initial", browser.proof())
            headers, payload = browser.request("GET", "/api/resources/smoke.txt", headers={"X-Encoding": "true"})
            etag = headers["ETag"]
            browser.request("PUT", "/api/resources/smoke.txt", "complete", {**browser.proof(), "If-Match": etag})
            browser.request("PUT", "/api/resources/smoke.txt", "stale", {**browser.proof(), "If-Match": etag}, status=412)
            _, payload = browser.request("GET", "/api/raw/smoke.txt")
            assert payload == b"complete"
            headers, _ = browser.request("POST", "/api/tus/partial.txt", b"", {**browser.proof(), "Upload-Length": "10"}, status=201)
            location = headers["Location"]
            browser.request("PATCH", location, "hello", {**browser.proof(), "Upload-Offset": "0", "Content-Type": "application/offset+octet-stream"}, status=204)
            browser.request("GET", "/api/raw/partial.txt", status=404)
            _, payload = browser.request("POST", "/api/renew", b"", browser.proof())
            assert json.loads(payload)["sessionVersion"] == browser.state["sessionVersion"]
            browser.request("PATCH", location, "world", {**browser.proof(), "Upload-Offset": "5", "Content-Type": "application/offset+octet-stream"}, status=204)
            _, payload = browser.request("GET", "/api/raw/partial.txt")
            assert payload == b"helloworld"
            docker("restart", current)
            # Docker may allocate a different host port after restarting a
            # container published with an ephemeral port. Cookies remain scoped
            # to the same loopback host, independent of that port.
            url = "http://" + docker("port", current, "8080/tcp").splitlines()[0]
            browser.url = url
            for _ in range(60):
                try:
                    browser.request("GET", "/health")
                    break
                except (OSError, AssertionError):
                    time.sleep(0.25)
            else:
                raise AssertionError("Disposable container did not restart")
            # The exact cookie and CSRF proof survive restart, without re-login.
            browser.request("GET", "/api/resources/")
            browser.request("POST", "/api/logout", b"", browser.proof(), status=204)
            browser.request("GET", "/api/resources/", status=401)
            browser.request("POST", "/api/renew", b"", browser.proof(), status=401)
            if share:
                _, payload = Client(url).request("GET", f"/api/public/dl/{share}")
                assert payload == b"original from ng.3"
            print(f"PASS {args.platform or 'native'} UID/GID {uid}: {'ng.3 upgrade, ' if share else ''}HttpOnly, CSRF, ETag, atomic TUS, download, restart, logout")
        finally:
            for container in reversed(containers):
                subprocess.run(["docker", "rm", "-f", container], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    parser.add_argument("--from-image")
    parser.add_argument("--platform")
    args = parser.parse_args()
    for uid in ["65532:65532", "1000:1000"]:
        smoke(args, uid)
