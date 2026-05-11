#!/usr/bin/env python3
import argparse
import base64
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, field
from datetime import datetime, timezone


@dataclass
class Check:
    name: str
    passed: bool
    detail: str = ""
    duration_ms: int = 0


@dataclass
class Report:
    started_at: str
    binary: str
    base_url: str
    checks: list[Check] = field(default_factory=list)

    def add(self, name, started, passed, detail=""):
        self.checks.append(Check(name=name, passed=passed, detail=detail, duration_ms=int((time.time() - started) * 1000)))

    def summary(self):
        passed = sum(1 for c in self.checks if c.passed)
        return {"passed": passed, "failed": len(self.checks) - passed, "total": len(self.checks)}

    def to_dict(self):
        return {
            "started_at": self.started_at,
            "binary": self.binary,
            "base_url": self.base_url,
            "summary": self.summary(),
            "checks": [c.__dict__ for c in self.checks],
        }


class Client:
    def __init__(self, base_url, api_key):
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key

    def request(self, method, path, body=None, content_type=None, headers=None, expected=None, auth_header="Authorization"):
        if auth_header == "APIKey":
            hdrs = {"APIKey": self.api_key}
        elif auth_header == "X-API-Key":
            hdrs = {"X-API-Key": self.api_key}
        else:
            hdrs = {"Authorization": f"ApiKey {self.api_key}"}
        if headers:
            hdrs.update(headers)
        data = None
        if body is not None:
            data = body if isinstance(body, bytes) else json.dumps(body).encode()
            if content_type:
                hdrs["Content-Type"] = content_type
            elif not isinstance(body, bytes):
                hdrs["Content-Type"] = "application/json"
        req = urllib.request.Request(self.base_url + path, data=data, headers=hdrs, method=method)
        try:
            with urllib.request.urlopen(req, timeout=5) as resp:
                payload = resp.read()
                status = resp.status
                resp_headers = dict(resp.headers)
        except urllib.error.HTTPError as e:
            payload = e.read()
            status = e.code
            resp_headers = dict(e.headers)
        if expected is not None and status != expected:
            safe_payload = payload[:300].decode("utf-8", errors="replace")
            raise AssertionError(f"status {status}, expected {expected}, response={safe_payload}")
        return status, resp_headers, payload


def wait_ready(base_url):
    deadline = time.time() + 10
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(base_url + "/readyz", timeout=1) as resp:
                if resp.status == 200:
                    return
        except Exception:
            time.sleep(0.1)
    raise RuntimeError("server did not become ready")


def start_server(binary, data_dir, port, api_key):
    env = os.environ.copy()
    env.update(
        {
            "KVHTTP_ADDR": f"127.0.0.1:{port}",
            "KVHTTP_STORAGE_PATH": data_dir,
            "KVHTTP_BOOTSTRAP_USER_ID": "prod_admin",
            "KVHTTP_BOOTSTRAP_USERSPACE_ID": "prod_space",
            "KVHTTP_BOOTSTRAP_API_KEY": api_key,
            "KVHTTP_JWT_SECRET": "local-production-test-jwt-secret",
            "KVHTTP_TX_CLEAN_INTERVAL_MS": "100",
        }
    )
    log_path = os.path.join(data_dir, "server.log")
    log_file = open(log_path, "ab")
    proc = subprocess.Popen([binary], env=env, stdout=log_file, stderr=log_file)
    return proc, log_file


def stop_server(proc, log_file):
    if proc.poll() is None:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=5)
    log_file.close()


def run_check(report, name, fn):
    started = time.time()
    try:
        detail = fn() or ""
        report.add(name, started, True, detail)
    except Exception as exc:
        report.add(name, started, False, str(exc))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", required=True)
    parser.add_argument("--port", type=int, default=18080)
    parser.add_argument("--keep-data", action="store_true")
    args = parser.parse_args()

    binary = os.path.abspath(args.binary)
    if not os.path.exists(binary):
        raise SystemExit(f"binary does not exist: {binary}")

    api_key = "local-prod-test-key-20260429"
    data_dir = tempfile.mkdtemp(prefix="httpkvdb-prodtest-")
    base_url = f"http://127.0.0.1:{args.port}"
    report = Report(started_at=datetime.now(timezone.utc).isoformat(), binary=binary, base_url=base_url)

    proc, log_file = start_server(binary, data_dir, args.port, api_key)
    try:
        wait_ready(base_url)
        client = Client(base_url, api_key)

        run_check(report, "health and readiness", lambda: (
            client.request("GET", "/healthz", expected=200),
            client.request("GET", "/readyz", expected=200),
            "healthz=200 readyz=200",
        )[-1])

        run_check(report, "unauthenticated request rejected", lambda: unauthenticated_check(base_url))
        run_check(report, "json kv put/get/head/delete", lambda: kv_json_check(client))
        run_check(report, "userspace url api and file mirror", lambda: userspace_url_and_file_mirror_check(client, data_dir))
        run_check(report, "admin creates userspace api key", lambda: admin_create_userspace_check(client, base_url, data_dir))
        run_check(report, "invalid json rejected", lambda: invalid_json_check(client))
        run_check(report, "binary value round trip", lambda: binary_check(client))
        run_check(report, "transaction fragments not executed before commit", lambda: tx_visibility_check(client))
        run_check(report, "out-of-order transaction commits by seq", lambda: tx_order_check(client))
        run_check(report, "duplicate commit returns committed result", lambda: tx_duplicate_commit_check(client))
        run_check(report, "export/import replace and merge modes", lambda: import_export_check(client))
        run_check(report, "metrics endpoint", lambda: metrics_check(base_url))

        stop_server(proc, log_file)
        proc, log_file = start_server(binary, data_dir, args.port, api_key)
        wait_ready(base_url)
        client = Client(base_url, api_key)
        run_check(report, "persistent data survives restart", lambda: restart_persistence_check(client))
    finally:
        stop_server(proc, log_file)
        if not args.keep_data:
            shutil.rmtree(data_dir, ignore_errors=True)

    output = report.to_dict()
    print(json.dumps(output, indent=2, ensure_ascii=False))
    if output["summary"]["failed"]:
        sys.exit(1)


def unauthenticated_check(base_url):
    req = urllib.request.Request(base_url + "/v1/kv/nope", method="GET")
    try:
        urllib.request.urlopen(req, timeout=5)
    except urllib.error.HTTPError as e:
        if e.code == 401:
            return "401 returned without credentials"
        raise
    raise AssertionError("request unexpectedly succeeded")


def kv_json_check(client):
    key = urllib.parse.quote("profile/primary", safe="")
    client.request("PUT", f"/v1/kv/{key}", {"name": "Alice", "n": 1}, expected=200)
    status, headers, body = client.request("GET", f"/v1/kv/{key}", expected=200)
    parsed = json.loads(body)
    assert parsed["n"] == 1, "JSON payload mismatch"
    header_keys = {k.lower() for k in headers}
    assert "x-kv-version" in header_keys, "X-KV-Version header missing"
    assert "x-kv-checksum" in header_keys, "X-KV-Checksum header missing"
    client.request("HEAD", f"/v1/kv/{key}", expected=200)
    client.request("DELETE", f"/v1/kv/{key}", expected=204)
    client.request("GET", f"/v1/kv/{key}", expected=404)
    return "json CRUD, metadata headers, strict delete verified"


def userspace_url_and_file_mirror_check(client, data_dir):
    client.request("PUT", "/api/v1/prod_space/profile", b"Alice", content_type="text/plain", expected=200, auth_header="APIKey")
    _, _, body = client.request("GET", "/api/v1/prod_space/profile", expected=200, auth_header="APIKey")
    assert body == b"Alice", "userspace URL returned wrong value"
    client.request("GET", "/api/v1/other_space/profile", expected=403, auth_header="APIKey")
    text_path = os.path.join(data_dir, "userspaces", "prod_space", "profile.txt")
    with open(text_path, "rb") as f:
        assert f.read() == b"Alice", "text mirror mismatch"
    client.request("PUT", "/api/v1/prod_space/profile", {"name": "Alice"}, expected=200, auth_header="APIKey")
    assert not os.path.exists(text_path), "old text mirror still exists after JSON overwrite"
    json_path = os.path.join(data_dir, "userspaces", "prod_space", "profile.json")
    with open(json_path, "rb") as f:
        assert json.loads(f.read())["name"] == "Alice", "json mirror mismatch"
    return "APIKey header, userspace route isolation, and value file mirror verified"


def admin_create_userspace_check(admin_client, base_url, data_dir):
    _, _, payload = admin_client.request(
        "POST",
        "/v1/admin/userspaces",
        {"userspace_id": "bob", "user_id": "bob"},
        expected=201,
        auth_header="APIKey",
    )
    created = json.loads(payload)
    assert created["userspace_id"] == "bob", "created userspace mismatch"
    assert created["api_key"], "generated api key missing"
    bob = Client(base_url, created["api_key"])
    bob.request("PUT", "/api/v1/bob/profile", b"Bob", content_type="text/plain", expected=200, auth_header="APIKey")
    _, _, body = bob.request("GET", "/api/v1/bob/profile", expected=200, auth_header="APIKey")
    assert body == b"Bob", "created userspace API key failed"
    bob.request("GET", "/api/v1/prod_space/profile", expected=403, auth_header="APIKey")
    admin_client.request("POST", "/v1/admin/userspaces", {"userspace_id": "bob", "user_id": "bob"}, expected=409, auth_header="APIKey")
    with open(os.path.join(data_dir, "userspaces", "bob", "profile.txt"), "rb") as f:
        assert f.read() == b"Bob", "created userspace file mirror mismatch"
    return "admin-created userspace API key and duplicate conflict verified"


def invalid_json_check(client):
    client.request("PUT", "/v1/kv/bad-json", b'{"bad"', content_type="application/json", expected=422)
    return "invalid JSON returned 422"


def binary_check(client):
    payload = bytes([0, 1, 2, 3, 250, 255]) + os.urandom(64)
    client.request("PUT", "/v1/kv/blob", payload, content_type="application/octet-stream", expected=200)
    _, _, got = client.request("GET", "/v1/kv/blob", expected=200)
    assert got == payload
    return f"binary round trip size={len(payload)}"


def tx_visibility_check(client):
    client.request("POST", "/v1/tx", {"tx_id": "prod-tx-visibility", "total_ops": 1, "timeout_ms": 5000}, expected=201)
    client.request(
        "POST",
        "/v1/tx/prod-tx-visibility/ops/1",
        b"committed later",
        content_type="text/plain",
        headers={"X-KV-Op": "PUT", "X-KV-Key": "tx-visible", "X-KV-Op-Id": "op-visibility-1"},
        expected=202,
    )
    client.request("GET", "/v1/kv/tx-visible", expected=404)
    client.request("POST", "/v1/tx/prod-tx-visibility/commit", {"total_ops": 1}, expected=200)
    _, _, got = client.request("GET", "/v1/kv/tx-visible", expected=200)
    assert got == b"committed later"
    return "operation body invisible before commit, visible after commit"


def tx_order_check(client):
    client.request("POST", "/v1/tx", {"tx_id": "prod-tx-order", "total_ops": 3, "timeout_ms": 5000}, expected=201)
    client.request("POST", "/v1/tx/prod-tx-order/ops/3", None, headers={"X-KV-Op": "GET", "X-KV-Key": "ordered", "X-KV-Op-Id": "op3"}, expected=202)
    client.request("POST", "/v1/tx/prod-tx-order/ops/1", b"first", content_type="text/plain", headers={"X-KV-Op": "PUT", "X-KV-Key": "ordered", "X-KV-Op-Id": "op1"}, expected=202)
    client.request("POST", "/v1/tx/prod-tx-order/ops/2", b"second", content_type="text/plain", headers={"X-KV-Op": "PUT", "X-KV-Key": "ordered", "X-KV-Op-Id": "op2"}, expected=202)
    _, _, body = client.request("POST", "/v1/tx/prod-tx-order/commit", {"total_ops": 3}, expected=200)
    result = json.loads(body)
    assert result["status"] == "committed"
    assert base64.b64decode(result["results"][2]["value_base64"]) == b"second"
    return "out-of-order fragments executed as seq 1..3"


def tx_duplicate_commit_check(client):
    _, _, first = client.request("POST", "/v1/tx/prod-tx-order/commit", {"total_ops": 3}, expected=200)
    _, _, second = client.request("POST", "/v1/tx/prod-tx-order/commit", {"total_ops": 3}, expected=200)
    assert json.loads(first) == json.loads(second)
    return "duplicate commit returned identical committed result"


def import_export_check(client):
    client.request("PUT", "/v1/kv/imported", b"original", content_type="text/plain", expected=200)
    _, _, exported = client.request("GET", "/v1/export", expected=200)
    client.request("DELETE", "/v1/kv/imported", expected=204)
    client.request("POST", "/v1/import", exported, content_type="application/octet-stream", headers={"X-KV-Import-Mode": "replace"}, expected=200)
    _, _, restored = client.request("GET", "/v1/kv/imported", expected=200)
    assert restored == b"original"
    client.request("PUT", "/v1/kv/imported", b"local", content_type="text/plain", expected=200)
    client.request("POST", "/v1/import", exported, content_type="application/octet-stream", headers={"X-KV-Import-Mode": "merge-skip"}, expected=200)
    _, _, skipped = client.request("GET", "/v1/kv/imported", expected=200)
    assert skipped == b"local"
    client.request("POST", "/v1/import", exported, content_type="application/octet-stream", headers={"X-KV-Import-Mode": "merge-overwrite"}, expected=200)
    _, _, overwritten = client.request("GET", "/v1/kv/imported", expected=200)
    assert overwritten == b"original"
    return f"binary export size={len(exported)}, replace/skip/overwrite verified"


def metrics_check(base_url):
    with urllib.request.urlopen(base_url + "/metrics", timeout=5) as resp:
        text = resp.read().decode()
    assert "http_requests_total" in text
    assert "kv_put_total" in text
    return "prometheus-style metrics exposed"


def restart_persistence_check(client):
    _, _, got = client.request("GET", "/v1/kv/imported", expected=200)
    assert got == b"original"
    _, _, got2 = client.request("GET", "/v1/kv/ordered", expected=200)
    assert got2 == b"second"
    return "committed KV data available after process restart"


if __name__ == "__main__":
    main()
