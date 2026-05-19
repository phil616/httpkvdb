#!/usr/bin/env python3
"""
httpkvdb benchmark — measures what actually matters for a globally
serialised, snapshot-persisting KV store.

Reports latency distribution (P50/P95/P99/P99.9), dataset-size scaling,
concurrency scaling, mixed-workload tail latency, and value-size impact.

The script is deliberately conservative about how much it writes when
pointed at a public instance. Tune via --profile or individual flags.

Security: never logs APIKey, Authorization headers, or value bytes.
"""

from __future__ import annotations

import argparse
import json
import math
import os
import random
import statistics
import string
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Callable


# ---------- HTTP client ----------------------------------------------------

class Client:
    def __init__(self, base_url: str, api_key: str, userspace: str, timeout: float = 10.0):
        self.base_url = base_url.rstrip("/")
        self._api_key = api_key
        self.userspace = userspace
        self.timeout = timeout
        # urllib opens a fresh TCP connection per request; that's fine for
        # benchmarking — it stresses both the server's accept path and the
        # request path. If you want to amortise TCP/TLS handshakes, swap in
        # http.client.HTTPConnection here.

    def _headers(self, extra: dict | None = None) -> dict:
        h = {"APIKey": self._api_key, "Connection": "close"}
        if extra:
            h.update(extra)
        return h

    def _url(self, key: str) -> str:
        return f"{self.base_url}/api/v1/{urllib.parse.quote(self.userspace, safe='')}/{urllib.parse.quote(key, safe='')}"

    def put(self, key: str, value: bytes, content_type: str = "application/octet-stream") -> tuple[int, float]:
        req = urllib.request.Request(
            self._url(key),
            data=value,
            method="PUT",
            headers=self._headers({"Content-Type": content_type}),
        )
        return self._do(req)

    def get(self, key: str) -> tuple[int, float]:
        req = urllib.request.Request(self._url(key), method="GET", headers=self._headers())
        return self._do(req)

    def head(self, key: str) -> tuple[int, float]:
        req = urllib.request.Request(self._url(key), method="HEAD", headers=self._headers())
        return self._do(req)

    def delete(self, key: str) -> tuple[int, float]:
        req = urllib.request.Request(self._url(key), method="DELETE", headers=self._headers())
        return self._do(req)

    def _do(self, req) -> tuple[int, float]:
        t0 = time.perf_counter()
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                # drain body so the connection can be closed cleanly
                resp.read()
                status = resp.status
        except urllib.error.HTTPError as e:
            e.read()
            status = e.code
        except (urllib.error.URLError, TimeoutError, OSError):
            status = 0  # network failure
        elapsed = (time.perf_counter() - t0) * 1000.0  # ms
        return status, elapsed


# ---------- stats ----------------------------------------------------------

@dataclass
class Samples:
    """A bag of latency samples (ms) with op accounting."""
    label: str
    latencies_ms: list[float] = field(default_factory=list)
    ok: int = 0
    errors: int = 0
    wall_clock_s: float = 0.0

    def record(self, status: int, elapsed_ms: float) -> None:
        self.latencies_ms.append(elapsed_ms)
        if 200 <= status < 300:
            self.ok += 1
        else:
            self.errors += 1

    def percentile(self, p: float) -> float:
        if not self.latencies_ms:
            return float("nan")
        s = sorted(self.latencies_ms)
        k = max(0, min(len(s) - 1, int(math.ceil(p / 100.0 * len(s))) - 1))
        return s[k]

    def summary(self) -> dict:
        if not self.latencies_ms:
            return {"label": self.label, "ops": 0}
        n = len(self.latencies_ms)
        return {
            "label": self.label,
            "ops": n,
            "ok": self.ok,
            "errors": self.errors,
            "wall_s": round(self.wall_clock_s, 3),
            "throughput_ops_s": round(n / self.wall_clock_s, 1) if self.wall_clock_s > 0 else None,
            "min_ms": round(min(self.latencies_ms), 3),
            "p50_ms": round(self.percentile(50), 3),
            "p95_ms": round(self.percentile(95), 3),
            "p99_ms": round(self.percentile(99), 3),
            "p999_ms": round(self.percentile(99.9), 3),
            "max_ms": round(max(self.latencies_ms), 3),
            "mean_ms": round(statistics.fmean(self.latencies_ms), 3),
        }


# ---------- progress bar ---------------------------------------------------

class ProgressBar:
    """Inline progress bar written to stderr. Stays on one line via \\r when
    stderr is a TTY; otherwise prints a line per tick interval. Designed to
    be cheap to call from many threads — updates throttled by time.
    """

    BAR_WIDTH = 28

    def __init__(self, label: str, total: int, min_interval_s: float = 0.1):
        self.label = label
        self.total = max(1, total)
        self.done = 0
        self.errors = 0
        self.lock = threading.Lock()
        self.start_t = time.perf_counter()
        self.last_render_t = 0.0
        self.min_interval_s = min_interval_s
        self.is_tty = sys.stderr.isatty()
        self._closed = False

    def tick(self, status: int = 200) -> None:
        with self.lock:
            self.done += 1
            if not (200 <= status < 300):
                self.errors += 1
            now = time.perf_counter()
            if self.done == self.total or now - self.last_render_t >= self.min_interval_s:
                self._render(now)
                self.last_render_t = now

    def _render(self, now: float) -> None:
        elapsed = max(1e-9, now - self.start_t)
        frac = self.done / self.total
        rate = self.done / elapsed
        eta = (self.total - self.done) / rate if rate > 0 else 0.0
        filled = int(self.BAR_WIDTH * frac)
        bar = "█" * filled + "░" * (self.BAR_WIDTH - filled)
        err_str = f" err={self.errors}" if self.errors else ""
        msg = (f"  {self.label:<28s} [{bar}] {self.done:>5d}/{self.total:<5d} "
               f"{frac*100:5.1f}%  {rate:6.1f} ops/s  eta {eta:5.1f}s{err_str}")
        if self.is_tty:
            sys.stderr.write("\r" + msg)
            sys.stderr.flush()
        else:
            sys.stderr.write(msg + "\n")

    def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        if self.is_tty:
            self._render(time.perf_counter())
            sys.stderr.write("\n")
            sys.stderr.flush()


def announce(msg: str) -> None:
    """Section header to stderr so reports written to stdout stay clean."""
    sys.stderr.write(f"\n>> {msg}\n")
    sys.stderr.flush()


def render_samples_table(rows: list[dict], extra_cols: list[str] | None = None) -> str:
    """Pretty-print one row per Samples.summary()."""
    if not rows:
        return "(no data)"
    base = ["label", "ops", "ok", "errors", "throughput_ops_s",
            "p50_ms", "p95_ms", "p99_ms", "p999_ms", "max_ms"]
    cols = (extra_cols or []) + base
    widths = {c: max(len(c), max(len(str(r.get(c, ""))) for r in rows)) for c in cols}
    header = "  ".join(c.ljust(widths[c]) for c in cols)
    sep = "  ".join("-" * widths[c] for c in cols)
    lines = [header, sep]
    for r in rows:
        lines.append("  ".join(str(r.get(c, "")).ljust(widths[c]) for c in cols))
    return "\n".join(lines)


def sparkline(values: list[float]) -> str:
    """ASCII sparkline so degradation curves are visible in plain text reports."""
    if not values:
        return ""
    bars = " ▁▂▃▄▅▆▇█"
    lo, hi = min(values), max(values)
    if hi <= lo:
        return bars[-1] * len(values)
    step = (hi - lo) / (len(bars) - 1)
    return "".join(bars[min(len(bars) - 1, int((v - lo) / step))] for v in values)


# ---------- workloads ------------------------------------------------------

def _rand_value(size: int) -> bytes:
    # Random bytes so JSON snapshot compression / pseudo-dedup tricks don't
    # accidentally help. base64-like alphabet to keep things printable.
    alphabet = (string.ascii_letters + string.digits).encode()
    return bytes(random.choices(alphabet, k=size))


def run_concurrent(
    op_fn: Callable[[int], tuple[int, float]],
    n_ops: int,
    concurrency: int,
    samples: Samples,
    progress_label: str | None = None,
) -> None:
    """Run op_fn(i) n_ops times across `concurrency` threads. op_fn must be
    thread-safe (a Client per thread is fine — urllib opens its own socket).
    """
    bar = ProgressBar(progress_label or samples.label, n_ops) if n_ops > 0 else None
    t0 = time.perf_counter()
    try:
        if concurrency == 1:
            for i in range(n_ops):
                status, elapsed = op_fn(i)
                samples.record(status, elapsed)
                if bar:
                    bar.tick(status)
        else:
            with ThreadPoolExecutor(max_workers=concurrency) as pool:
                futs = [pool.submit(op_fn, i) for i in range(n_ops)]
                for f in as_completed(futs):
                    status, elapsed = f.result()
                    samples.record(status, elapsed)
                    if bar:
                        bar.tick(status)
    finally:
        if bar:
            bar.close()
    samples.wall_clock_s = time.perf_counter() - t0


def warmup_keyspace(client: Client, prefix: str, n_keys: int, value_size: int, concurrency: int) -> list[str]:
    """Pre-populate n_keys keys. Returns the list of keys written so they
    can be reused for reads and cleaned up later.
    """
    keys = [f"{prefix}/k/{i:08d}" for i in range(n_keys)]
    value = _rand_value(value_size)

    def op(i: int) -> tuple[int, float]:
        return client.put(keys[i], value, content_type="application/octet-stream")

    s = Samples(f"warmup-{n_keys}")
    run_concurrent(op, n_keys, concurrency, s, progress_label=f"warmup keyspace ({n_keys})")
    return keys


def bench_writes(client: Client, prefix: str, n_ops: int, value_size: int, concurrency: int, label: str) -> Samples:
    value = _rand_value(value_size)

    def op(i: int) -> tuple[int, float]:
        return client.put(f"{prefix}/bench/{i:08d}", value, content_type="application/octet-stream")

    s = Samples(label)
    run_concurrent(op, n_ops, concurrency, s, progress_label=label)
    return s


def bench_reads(client: Client, keys: list[str], n_ops: int, concurrency: int, label: str) -> Samples:
    if not keys:
        raise ValueError("read benchmark needs a pre-populated keyspace")

    def op(i: int) -> tuple[int, float]:
        return client.get(keys[i % len(keys)])

    s = Samples(label)
    run_concurrent(op, n_ops, concurrency, s, progress_label=label)
    return s


def bench_mixed(
    client: Client,
    prefix: str,
    keys: list[str],
    n_ops: int,
    concurrency: int,
    read_ratio: float,
    value_size: int,
) -> tuple[Samples, Samples]:
    """Mixed workload. Track read and write latency separately so we can
    see how much writes drag the read tail.
    """
    value = _rand_value(value_size)
    reads = Samples(f"mixed-r{int(read_ratio*100)}-read")
    writes = Samples(f"mixed-r{int(read_ratio*100)}-write")
    lock = threading.Lock()

    rng = random.Random(0xC0FFEE)
    decisions = [rng.random() < read_ratio for _ in range(n_ops)]

    def op(i: int) -> tuple[int, float, bool]:
        if decisions[i]:
            status, elapsed = client.get(keys[i % len(keys)])
            return status, elapsed, True
        else:
            status, elapsed = client.put(f"{prefix}/mix/{i:08d}", value, content_type="application/octet-stream")
            return status, elapsed, False

    bar = ProgressBar(f"mixed r={int(read_ratio*100)}%", n_ops)
    t0 = time.perf_counter()
    try:
        with ThreadPoolExecutor(max_workers=concurrency) as pool:
            futs = [pool.submit(op, i) for i in range(n_ops)]
            for f in as_completed(futs):
                status, elapsed, is_read = f.result()
                with lock:
                    (reads if is_read else writes).record(status, elapsed)
                bar.tick(status)
    finally:
        bar.close()
    wall = time.perf_counter() - t0
    reads.wall_clock_s = wall
    writes.wall_clock_s = wall
    return reads, writes


# ---------- profiles -------------------------------------------------------

PROFILES = {
    # conservative — for a public instance you don't want to abuse
    "public": {
        "value_size": 256,
        "concurrency_levels": [1, 4, 16, 32],
        "ops_per_concurrency": 200,
        "read_ops": 500,
        "mixed_ops": 400,
        "mixed_ratios": [0.95, 0.80, 0.50],
        "scaling_steps": [100, 500, 1500, 3000, 5000],
        "scaling_probe_ops": 50,
        "value_size_sweep": [64, 1024, 16 * 1024],
        "value_size_probe_ops": 50,
        "keyspace_for_reads": 500,
    },
    # full — for local / LAN runs
    "local": {
        "value_size": 256,
        "concurrency_levels": [1, 4, 16, 64, 128],
        "ops_per_concurrency": 2000,
        "read_ops": 5000,
        "mixed_ops": 4000,
        "mixed_ratios": [0.95, 0.80, 0.50],
        "scaling_steps": [100, 1000, 5000, 10000, 20000],
        "scaling_probe_ops": 200,
        "value_size_sweep": [64, 1024, 16 * 1024, 128 * 1024],
        "value_size_probe_ops": 200,
        "keyspace_for_reads": 2000,
    },
}


# ---------- main scenarios -------------------------------------------------

def scenario_concurrency_scaling(client: Client, prefix: str, cfg: dict) -> dict:
    """Vary client concurrency, measure write throughput. The global server
    lock should cap throughput; this finds the knee.
    """
    rows = []
    for c in cfg["concurrency_levels"]:
        s = bench_writes(client, f"{prefix}/conc/c{c}", cfg["ops_per_concurrency"], cfg["value_size"], c,
                         label=f"writes c={c}")
        rows.append({**s.summary(), "concurrency": c})
    return {"rows": rows, "extra_cols": ["concurrency"]}


def scenario_dataset_scaling(client: Client, prefix: str, cfg: dict) -> dict:
    """This is the headline test for this database. Grow the dataset in
    steps and, at each step, measure the latency of a small batch of fresh
    writes. We expect the curve to grow roughly linearly with keys.
    """
    rows = []
    growth_curve_p99 = []
    growth_curve_p50 = []
    populated = 0
    for target in cfg["scaling_steps"]:
        to_add = target - populated
        if to_add > 0:
            # bulk-populate up to `target` keys with low concurrency (we
            # don't want to fight the server lock during prep)
            value = _rand_value(cfg["value_size"])

            def add(i: int, base=populated) -> tuple[int, float]:
                return client.put(f"{prefix}/scale/seed/{base + i:08d}", value,
                                  content_type="application/octet-stream")
            seed = Samples(f"seed→{target}")
            run_concurrent(add, to_add, 4, seed, progress_label=f"seed→{target} keys (+{to_add})")
            populated = target

        # probe: fresh writes at this dataset size
        probe = bench_writes(client, f"{prefix}/scale/probe/d{target}",
                             cfg["scaling_probe_ops"], cfg["value_size"], 1,
                             label=f"probe @ {target} keys")
        summary = probe.summary()
        summary["dataset_keys"] = target
        rows.append(summary)
        growth_curve_p50.append(summary["p50_ms"])
        growth_curve_p99.append(summary["p99_ms"])

    return {
        "rows": rows,
        "extra_cols": ["dataset_keys"],
        "spark_p50": sparkline(growth_curve_p50),
        "spark_p99": sparkline(growth_curve_p99),
        "steps": cfg["scaling_steps"],
        "p50_series": growth_curve_p50,
        "p99_series": growth_curve_p99,
    }


def scenario_value_size(client: Client, prefix: str, cfg: dict) -> dict:
    rows = []
    for size in cfg["value_size_sweep"]:
        s = bench_writes(client, f"{prefix}/vs/s{size}",
                         cfg["value_size_probe_ops"], size, 1,
                         label=f"value={size}B")
        rows.append({**s.summary(), "value_bytes": size})
    return {"rows": rows, "extra_cols": ["value_bytes"]}


def scenario_read_latency(client: Client, keys: list[str], cfg: dict) -> dict:
    rows = []
    for c in cfg["concurrency_levels"]:
        s = bench_reads(client, keys, cfg["read_ops"], c, label=f"reads c={c}")
        rows.append({**s.summary(), "concurrency": c})
    return {"rows": rows, "extra_cols": ["concurrency"]}


def scenario_mixed(client: Client, prefix: str, keys: list[str], cfg: dict) -> dict:
    rows = []
    for ratio in cfg["mixed_ratios"]:
        reads, writes = bench_mixed(client, f"{prefix}/mix/r{int(ratio*100)}", keys,
                                    cfg["mixed_ops"], 16, ratio, cfg["value_size"])
        rows.append({**reads.summary(), "read_pct": int(ratio * 100)})
        rows.append({**writes.summary(), "read_pct": int(ratio * 100)})
    return {"rows": rows, "extra_cols": ["read_pct"]}


# ---------- cleanup --------------------------------------------------------

def cleanup_keys(client: Client, all_keys: set[str], concurrency: int = 8) -> tuple[int, int]:
    deleted = 0
    failed = 0
    lock = threading.Lock()
    keys = sorted(all_keys)
    bar = ProgressBar("cleanup", len(keys)) if keys else None

    def op(i: int) -> int:
        status, _ = client.delete(keys[i])
        # 204 on delete, 404 if it never existed
        return status

    try:
        with ThreadPoolExecutor(max_workers=concurrency) as pool:
            futs = [pool.submit(op, i) for i in range(len(keys))]
            for f in as_completed(futs):
                status = f.result()
                with lock:
                    if status in (200, 204, 404):
                        deleted += 1
                    else:
                        failed += 1
                if bar:
                    # 404 is fine for cleanup
                    bar.tick(204 if status in (200, 204, 404) else status)
    finally:
        if bar:
            bar.close()
    return deleted, failed


# ---------- key tracking ---------------------------------------------------

class KeyTracker:
    """Record every key written so we can clean up. We rebuild the key
    list deterministically from prefixes to avoid storing N strings per op.
    """
    def __init__(self):
        self.entries: list[tuple[str, int]] = []  # (prefix, count)

    def add_range(self, prefix: str, count: int) -> None:
        if count > 0:
            self.entries.append((prefix, count))

    def all_keys(self) -> set[str]:
        out: set[str] = set()
        for prefix, count in self.entries:
            for i in range(count):
                out.add(f"{prefix}/{i:08d}")
        return out


# ---------- report ---------------------------------------------------------

def emit_report(
    cfg_meta: dict,
    sections: dict[str, dict],
    out_text: bool,
    out_json_path: str | None,
) -> None:
    if out_text:
        print("=" * 72)
        print("httpkvdb benchmark report")
        print("=" * 72)
        for k, v in cfg_meta.items():
            print(f"  {k:18s} {v}")
        print()
        for name, section in sections.items():
            print(f"## {name}")
            print(render_samples_table(section["rows"], section.get("extra_cols")))
            if "spark_p50" in section:
                steps_str = "  ".join(f"{s:>6d}" for s in section["steps"])
                p50_str = "  ".join(f"{v:>6.2f}" for v in section["p50_series"])
                p99_str = "  ".join(f"{v:>6.2f}" for v in section["p99_series"])
                print()
                print(f"  dataset keys : {steps_str}")
                print(f"  P50 (ms)     : {p50_str}   {section['spark_p50']}")
                print(f"  P99 (ms)     : {p99_str}   {section['spark_p99']}")
            print()

    if out_json_path:
        payload = {
            "meta": cfg_meta,
            "sections": {name: {k: v for k, v in s.items() if k != "extra_cols"} for name, s in sections.items()},
        }
        with open(out_json_path, "w") as f:
            json.dump(payload, f, indent=2, default=str)
        print(f"[report] wrote {out_json_path}")


# ---------- entrypoint -----------------------------------------------------

def main() -> int:
    ap = argparse.ArgumentParser(description="httpkvdb benchmark")
    ap.add_argument("--base-url", required=True, help="e.g. https://kv.example.com")
    ap.add_argument("--api-key", default=os.environ.get("KVHTTP_API_KEY"),
                    help="APIKey for the userspace (or set KVHTTP_API_KEY env)")
    ap.add_argument("--userspace", required=True, help="userspace id this APIKey belongs to")
    ap.add_argument("--profile", choices=PROFILES.keys(), default="public",
                    help="conservative ('public') vs full ('local') defaults")
    ap.add_argument("--scenarios", default="concurrency,reads,mixed,value-size,dataset-scaling",
                    help="comma-separated subset of scenarios to run")
    ap.add_argument("--prefix", default=None,
                    help="key prefix (default: bench-<timestamp>) so concurrent runs don't collide")
    ap.add_argument("--cleanup", action="store_true", default=True,
                    help="DELETE all keys written by the benchmark (default: on)")
    ap.add_argument("--no-cleanup", dest="cleanup", action="store_false")
    ap.add_argument("--json", dest="json_path", default=None,
                    help="also write a machine-readable report to this path")
    ap.add_argument("--timeout", type=float, default=10.0)
    args = ap.parse_args()

    if not args.api_key:
        print("error: --api-key or KVHTTP_API_KEY required", file=sys.stderr)
        return 2

    cfg = PROFILES[args.profile]
    prefix = args.prefix or f"bench-{int(time.time())}"
    client = Client(args.base_url, args.api_key, args.userspace, timeout=args.timeout)
    tracker = KeyTracker()

    # sanity check connectivity first — bail out quickly with a clear msg
    status, elapsed = client.put(f"{prefix}/ping", b"ok", content_type="text/plain")
    tracker.add_range(f"{prefix}/ping", 0)  # single key handled by direct add below
    if status != 200:
        print(f"error: ping PUT returned status={status} ({elapsed:.1f}ms). "
              f"Check base-url / api-key / userspace.", file=sys.stderr)
        return 3
    # track the ping key explicitly
    pings = {f"{prefix}/ping"}

    meta = {
        "started_at": datetime.now(timezone.utc).isoformat(),
        "base_url": args.base_url,
        "userspace": args.userspace,
        "profile": args.profile,
        "prefix": prefix,
        "value_size_bytes": cfg["value_size"],
        "concurrency_levels": cfg["concurrency_levels"],
    }

    requested = {s.strip() for s in args.scenarios.split(",") if s.strip()}
    sections: dict[str, dict] = {}

    # Build a stable read keyspace once, reused by reads and mixed
    read_keys: list[str] = []
    if requested & {"reads", "mixed"}:
        n = cfg["keyspace_for_reads"]
        read_keys = warmup_keyspace(client, f"{prefix}/rk", n, cfg["value_size"], 8)
        tracker.add_range(f"{prefix}/rk/k", n)

    try:
        if "concurrency" in requested:
            announce("scenario: concurrency scaling (writes)")
            sections["concurrency scaling (writes)"] = scenario_concurrency_scaling(client, prefix, cfg)
            for c in cfg["concurrency_levels"]:
                tracker.add_range(f"{prefix}/conc/c{c}/bench", cfg["ops_per_concurrency"])

        if "reads" in requested:
            announce("scenario: read latency vs concurrency")
            sections["read latency vs concurrency"] = scenario_read_latency(client, read_keys, cfg)

        if "mixed" in requested:
            announce("scenario: mixed workload (read tail vs write share)")
            sections["mixed workload (read tail vs write share)"] = scenario_mixed(client, prefix, read_keys, cfg)
            for ratio in cfg["mixed_ratios"]:
                tracker.add_range(f"{prefix}/mix/r{int(ratio*100)}/mix", cfg["mixed_ops"])

        if "value-size" in requested:
            announce("scenario: value size sweep")
            sections["value size sweep (1 client)"] = scenario_value_size(client, prefix, cfg)
            for size in cfg["value_size_sweep"]:
                tracker.add_range(f"{prefix}/vs/s{size}/bench", cfg["value_size_probe_ops"])

        if "dataset-scaling" in requested:
            announce("scenario: dataset size scaling — this is the headline test")
            sections["dataset size scaling (writes)"] = scenario_dataset_scaling(client, prefix, cfg)
            # both seeded keys and probe keys
            for target in cfg["scaling_steps"]:
                tracker.add_range(f"{prefix}/scale/seed", target)  # range is cumulative; dedup below
                tracker.add_range(f"{prefix}/scale/probe/d{target}/bench", cfg["scaling_probe_ops"])

    finally:
        emit_report(meta, sections, out_text=True, out_json_path=args.json_path)

        if args.cleanup:
            all_keys = tracker.all_keys() | pings
            print(f"[cleanup] deleting {len(all_keys)} keys ...")
            ok, fail = cleanup_keys(client, all_keys, concurrency=8)
            print(f"[cleanup] done. removed/absent={ok}, failed={fail}")
        else:
            print("[cleanup] skipped (--no-cleanup)")

    return 0


if __name__ == "__main__":
    sys.exit(main())
