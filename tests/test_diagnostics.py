"""错误分类与诊断测试。

线上跑在 Vercel 机房 IP 上，B 站可能返回 412 风控。以前的实现用
`response.raise_for_status()` 把它和「连不上」混成同一句话，日志里看不出任何
信息。这里锁定行为：不同失败必须给出不同提示，并且诊断信息要保留下来。
"""

from __future__ import annotations

import httpx
import pytest

from biliaudio.api import _get_json
from biliaudio.errors import BiliError
from biliaudio.probe import ProbeResult, describe, probe_http
from biliaudio.utils import encode_base64_url


def _probe(
    *,
    status: int | None = 200,
    body: str = '{"code":0,"message":"OK","data":{"ok":1}}',
    json_code: int | None = 0,
    json_message: str = "OK",
    exception: str = "",
    exception_class: str = "",
) -> ProbeResult:
    return ProbeResult(
        url="https://api.bilibili.com/x/web-interface/view",
        status=status,
        elapsed=0.1,
        body=body,
        json_code=json_code,
        json_message=json_message,
        exception=exception,
        exception_class=exception_class,
    )


class TestErrorClassification:
    def test_success_returns_data(self, monkeypatch):
        monkeypatch.setattr("biliaudio.api.probe_http", lambda *a, **k: _probe())
        assert _get_json("https://api.bilibili.com/x/web-interface/view", {}) == {"ok": 1}

    def test_412_risk_control_is_reported_and_keeps_detail(self, monkeypatch):
        monkeypatch.setattr(
            "biliaudio.api.probe_http",
            lambda *a, **k: _probe(
                status=412,
                body="<html>412</html>",
                json_code=None,
                json_message="",
            ),
        )
        with pytest.raises(BiliError) as err:
            _get_json("https://api.bilibili.com/x/web-interface/view", {})

        assert "412" in err.value.message
        assert "风控" in err.value.message
        # 诊断信息必须保留，否则线上日志看不到真实状态码
        assert "HTTP 412" in (err.value.detail or "")

    def test_403_is_not_confused_with_timeout(self, monkeypatch):
        monkeypatch.setattr(
            "biliaudio.api.probe_http",
            lambda *a, **k: _probe(status=403, json_code=None, json_message=""),
        )
        with pytest.raises(BiliError) as err:
            _get_json("https://api.bilibili.com/x/web-interface/view", {})
        assert "403" in err.value.message

    @pytest.mark.parametrize(
        "exc_class", ["ConnectTimeout", "ReadTimeout", "ConnectError", "RemoteProtocolError"]
    )
    def test_network_errors_are_classified(self, monkeypatch, exc_class):
        monkeypatch.setattr(
            "biliaudio.api.probe_http",
            lambda *a, **k: _probe(
                status=None,
                json_code=None,
                exception="boom",
                exception_class=exc_class,
            ),
        )
        with pytest.raises(BiliError) as err:
            _get_json("https://api.bilibili.com/x/web-interface/view", {})

        if "Timeout" in exc_class:
            assert "超时" in err.value.message
        else:
            assert "无法连接" in err.value.message
        assert exc_class in (err.value.detail or "")

    def test_business_code_uses_mapped_message(self, monkeypatch):
        monkeypatch.setattr(
            "biliaudio.api.probe_http",
            lambda *a, **k: _probe(json_code=-404, json_message="啥都木有"),
        )
        with pytest.raises(BiliError) as err:
            _get_json("https://api.bilibili.com/x/web-interface/view", {})
        assert err.value.message == "视频不存在或已被删除"

    def test_non_json_body_is_reported(self, monkeypatch):
        monkeypatch.setattr(
            "biliaudio.api.probe_http",
            lambda *a, **k: _probe(body="<html/>", json_code=None, json_message=""),
        )
        with pytest.raises(BiliError) as err:
            _get_json("https://api.bilibili.com/x/web-interface/view", {})
        assert "非预期" in err.value.message


class TestProbe:
    def test_probe_http_captures_transport_exception(self, monkeypatch):
        def boom(*args, **kwargs):
            raise httpx.ConnectError("dns failure")

        monkeypatch.setattr("biliaudio.probe.httpx.get", boom)
        result = probe_http("https://api.bilibili.com/x/web-interface/view")
        assert result.exception_class == "ConnectError"
        assert result.status is None
        assert "ConnectError" in describe(result)

    def test_probe_result_serialization_truncates_body(self):
        result = _probe(body="x" * 5000)
        payload = result.to_dict()
        assert len(payload["body"]) < 1000
        assert "timed_out" in payload

    def test_timeout_flag(self):
        assert _probe(exception_class="ReadTimeout").timed_out is True
        assert _probe(exception_class="ConnectError").timed_out is False


class TestDiagEndpoint:
    @pytest.fixture()
    def client(self, monkeypatch):
        from biliaudio.app import create_app

        monkeypatch.setattr("biliaudio.app.DEBUG_TOKEN", "s3cret")
        monkeypatch.setattr(
            "biliaudio.app.collect_diagnostics",
            lambda cookie="": {"client_ip": "203.0.113.7", "dns": {"ok": True}},
        )
        app = create_app()
        app.config.update(TESTING=True)
        with app.test_client() as test_client:
            yield test_client

    def test_hidden_without_token_config(self, monkeypatch):
        from biliaudio.app import create_app

        monkeypatch.setattr("biliaudio.app.DEBUG_TOKEN", "")
        app = create_app()
        app.config.update(TESTING=True)
        assert app.test_client().get("/api/diag").status_code == 404

    def test_requires_matching_token(self, client):
        assert client.get("/api/diag").status_code == 403
        assert client.get("/api/diag?token=wrong").status_code == 403

    def test_returns_diagnostics(self, client):
        response = client.get("/api/diag?token=s3cret")
        assert response.status_code == 200
        payload = response.get_json()
        assert payload["ok"] is True
        assert payload["diagnostics"]["client_ip"] == "203.0.113.7"


class TestProxyTokenStillWorks:
    def test_encoded_url_roundtrip(self):
        token = encode_base64_url("https://upos-sz-mirrorcos.bilivideo.com/a.m4s")
        assert "/" not in token and "+" not in token


class TestRiskControlRetry:
    """命中 412/403 且未配置登录态时，会自动带匿名设备指纹重试一次。"""

    def test_retry_with_device_cookie_succeeds(self, monkeypatch):
        from biliaudio import api

        monkeypatch.setattr(api, "COOKIE", "")
        calls: list[dict[str, str]] = []

        def fake(url, *, params=None, headers=None, timeout=10.0):
            calls.append(dict(headers or {}))
            if len(calls) == 1:
                return _probe(status=412, json_code=None, json_message="")
            return _probe()

        monkeypatch.setattr(api, "probe_http", fake)

        assert api._get_json("https://api.bilibili.com/x/web-interface/view", {}) == {"ok": 1}
        assert len(calls) == 2
        assert "Cookie" not in calls[0]
        assert "buvid3=" in calls[1]["Cookie"]
        assert "buvid4=" in calls[1]["Cookie"]

    def test_reports_both_attempts_when_retry_also_blocked(self, monkeypatch):
        from biliaudio import api

        monkeypatch.setattr(api, "COOKIE", "")
        monkeypatch.setattr(
            api,
            "probe_http",
            lambda *a, **k: _probe(status=412, json_code=None, json_message=""),
        )

        with pytest.raises(BiliError) as err:
            api._get_json("https://api.bilibili.com/x/web-interface/view", {})

        detail = err.value.detail or ""
        assert detail.count("HTTP 412") == 2
        assert "首次" in detail and "重试" in detail

    def test_no_retry_when_cookie_configured(self, monkeypatch):
        from biliaudio import api

        monkeypatch.setattr(api, "COOKIE", "SESSDATA=real")
        calls = []

        def fake(url, *, params=None, headers=None, timeout=10.0):
            calls.append(headers or {})
            return _probe(status=412, json_code=None, json_message="")

        monkeypatch.setattr(api, "probe_http", fake)

        with pytest.raises(BiliError):
            api._get_json("https://api.bilibili.com/x/web-interface/view", {})
        assert len(calls) == 1
