"""Tests for transient-LLM-error retry."""

from __future__ import annotations

import pytest

from hivemind_brain.llm import _is_transient, call_json, invoke_with_retry


class _RateLimitError(Exception):
    pass


class _BadRequestError(Exception):
    status_code = 400


class FlakyModel:
    """Raises a transient error `fail_times` times, then returns content."""

    def __init__(self, fail_times: int, exc: Exception):
        self.fail_times = fail_times
        self.exc = exc
        self.calls = 0

    def invoke(self, messages):
        self.calls += 1
        if self.calls <= self.fail_times:
            raise self.exc
        from langchain_core.messages import AIMessage

        return AIMessage(content='{"ok": true}')


def test_is_transient_by_name_and_code():
    assert _is_transient(_RateLimitError("429"))
    assert _is_transient(TimeoutError())

    class _E(Exception):
        status_code = 503

    assert _is_transient(_E())
    assert not _is_transient(_BadRequestError())
    assert not _is_transient(ValueError("nope"))


def test_retries_transient_then_succeeds():
    model = FlakyModel(fail_times=2, exc=_RateLimitError("slow down"))
    out = call_json(model, "sys", "user", max_retries=3, retry_base_s=0)
    assert out == {"ok": True}
    assert model.calls == 3  # 2 failures + 1 success


def test_gives_up_after_max_retries():
    model = FlakyModel(fail_times=5, exc=_RateLimitError("still slow"))
    with pytest.raises(_RateLimitError):
        invoke_with_retry(model, [], max_retries=2, retry_base_s=0)
    assert model.calls == 3  # initial + 2 retries


def test_non_transient_raises_immediately():
    model = FlakyModel(fail_times=5, exc=_BadRequestError())
    with pytest.raises(_BadRequestError):
        invoke_with_retry(model, [], max_retries=3, retry_base_s=0)
    assert model.calls == 1  # no retries on a 400
