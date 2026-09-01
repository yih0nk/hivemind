"""A tiny, dependency-free Prometheus metrics registry.

Just enough to expose counters and gauges in the Prometheus text exposition
format without pulling in prometheus_client. Labels are a small dict; series are
keyed by (name, sorted labels). Thread-safe for the API server's threadpool.
"""

from __future__ import annotations

import threading
from typing import Callable


def _key(name: str, labels: dict[str, str]) -> tuple:
    return (name, tuple(sorted(labels.items())))


class Metrics:
    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._counters: dict[tuple, float] = {}
        self._help: dict[str, str] = {}
        self._gauges: dict[str, Callable[[], float]] = {}

    def counter(self, name: str, help_text: str) -> None:
        """Register a counter (no-op if already registered)."""
        self._help.setdefault(name, help_text)

    def inc(self, name: str, labels: dict[str, str] | None = None, by: float = 1.0) -> None:
        labels = labels or {}
        with self._lock:
            self._counters[_key(name, labels)] = self._counters.get(_key(name, labels), 0.0) + by

    def gauge(self, name: str, help_text: str, fn: Callable[[], float]) -> None:
        """Register a gauge backed by a callable sampled at render time."""
        self._help[name] = help_text
        self._gauges[name] = fn

    def render(self) -> str:
        """Render all series in Prometheus text exposition format."""
        lines: list[str] = []
        emitted: set[str] = set()

        def header(name: str, typ: str) -> None:
            if name in emitted:
                return
            emitted.add(name)
            if name in self._help:
                lines.append(f"# HELP {name} {self._help[name]}")
            lines.append(f"# TYPE {name} {typ}")

        with self._lock:
            counters = dict(self._counters)
            gauges = dict(self._gauges)

        for (name, label_items), value in sorted(counters.items()):
            header(name, "counter")
            label_str = ",".join(f'{k}="{v}"' for k, v in label_items)
            suffix = f"{{{label_str}}}" if label_str else ""
            lines.append(f"{name}{suffix} {_fmt(value)}")

        for name, fn in sorted(gauges.items()):
            header(name, "gauge")
            try:
                lines.append(f"{name} {_fmt(float(fn()))}")
            except Exception:  # a gauge sample must never break /metrics
                continue

        return "\n".join(lines) + "\n"


def _fmt(v: float) -> str:
    return str(int(v)) if v == int(v) else repr(v)
