"""路由级测试：不访问外网。"""

from __future__ import annotations

from urllib.parse import unquote

import pytest

from biliaudio import utils
from biliaudio.api import AudioPart, VideoInfo
from biliaudio.app import create_app


@pytest.fixture()
def client(monkeypatch):
    video = VideoInfo(
        bvid="BV1GJ411x7h7",
        title="测试视频",
        author="UP 主",
        cover="https://i0.hdslb.com/cover.jpg",
        duration="1:00",
        views="1.2万",
        likes="678",
        coins="9",
        favorites="1",
        shares="2",
        danmaku="3",
        description="简介",
        pubdate="2020-01-01",
        parts=[
            AudioPart(
                name="完整音频",
                cid=1,
                duration="1:00",
                filename="测试视频",
                stream_url="https://upos-sz-mirrorcos.bilivideo.com/a.m4s",
            )
        ],
    )

    def fake_parse(text: str):
        return {
            "videos": [video],
            "failures": [],
            "error": "",
            "tracks": [],
            "input": text,
        }

    monkeypatch.setattr("biliaudio.app._parse", fake_parse)
    app = create_app()
    app.config.update(TESTING=True)
    with app.test_client() as test_client:
        yield test_client


def test_index_renders_empty_state(client):
    response = client.get("/")
    assert response.status_code == 200
    assert "哔哩哔哩音频解析" in response.get_data(as_text=True)


def test_form_post_returns_full_page(client):
    response = client.post("/api/parse", data={"input": "BV1GJ411x7h7"})
    body = response.get_data(as_text=True)
    assert response.status_code == 200
    assert "测试视频" in body
    assert body.lstrip().lower().startswith("<!doctype html>")


def test_fetch_post_returns_fragment(client):
    response = client.post(
        "/api/parse",
        data={"input": "BV1GJ411x7h7"},
        headers={"X-Requested-With": "fetch"},
    )
    body = response.get_data(as_text=True)
    assert response.status_code == 200
    assert "测试视频" in body
    assert "<!DOCTYPE html>" not in body


def test_empty_input_shows_error(client):
    response = client.post("/api/parse", data={"input": "   "})
    assert "请输入视频链接" in response.get_data(as_text=True)


def test_json_api(client):
    response = client.get("/api/parse", query_string={"input": "BV1GJ411x7h7"})
    payload = response.get_json()
    assert response.status_code == 200
    assert payload["videos"][0]["title"] == "测试视频"
    assert "stream_url" not in payload["videos"][0]["parts"][0]


def test_json_api_requires_input(client):
    response = client.get("/api/parse")
    assert response.status_code == 400


def test_proxy_rejects_bad_token(client):
    response = client.get("/api/proxy/bm90LWJhc2U2NA")
    assert response.status_code == 400


def test_healthz(client):
    assert client.get("/healthz").get_json() == {"ok": True}


class _FakeUpstream:
    """模拟 httpx 的流式响应。"""

    status_code = 206
    headers = {
        "content-type": "audio/mp4",
        "content-range": "bytes 0-10/100",
        "accept-ranges": "bytes",
    }

    def iter_bytes(self, chunk_size):
        yield b"audio-"
        yield b"bytes"

    def close(self):
        pass


class _FakeOctetStreamUpstream(_FakeUpstream):
    """部分 CDN 会把 m4s 标成 octet-stream，需要被纠正为 audio/mp4。"""

    headers = {
        "content-type": "application/octet-stream",
        "content-length": "11",
    }


class _FakeClient:
    """记录请求头，避免真实网络访问。"""

    def __init__(self, upstream):
        self.upstream = upstream
        self.sent_headers: dict[str, str] = {}
        self.closed = False

    def build_request(self, method, url, headers):
        self.sent_headers = dict(headers)
        return {"method": method, "url": url}

    def send(self, request, stream=True):
        return self.upstream

    def close(self):
        self.closed = True


@pytest.fixture()
def proxy_client(monkeypatch):
    """返回 (test_client, fake_client)，并把上游替换为内存响应。"""
    fake = _FakeClient(_FakeUpstream())
    monkeypatch.setattr("biliaudio.proxy.httpx.Client", lambda **kwargs: fake)
    app = create_app()
    app.config.update(TESTING=True)
    with app.test_client() as test_client:
        yield test_client, fake


def _proxy_path(name: str = "歌名") -> str:
    token = utils.encode_base64_url("https://upos-sz-mirrorcos.bilivideo.com/audio.m4s")
    return f"/api/proxy/{token}?filename={name}"


class TestProxyResponse:
    def test_streams_body_and_headers(self, proxy_client):
        test_client, fake = proxy_client
        response = test_client.get(_proxy_path())

        assert response.status_code == 206
        assert response.get_data() == b"audio-bytes"
        assert response.headers["Content-Range"] == "bytes 0-10/100"
        assert response.headers["Accept-Ranges"] == "bytes"
        assert response.headers["Content-Type"] == "audio/mp4"
        assert response.headers["X-Content-Type-Options"] == "nosniff"
        assert response.headers["Cache-Control"] == "private, max-age=600"

        disposition = response.headers["Content-Disposition"]
        assert disposition.startswith("inline; ")
        assert 'filename="bilibili_audio.m4a"' in disposition
        assert unquote(disposition.split("filename*=UTF-8''")[1]) == "歌名.m4a"
        assert fake.closed is True

    def test_forwards_range_and_referer(self, proxy_client):
        test_client, fake = proxy_client
        test_client.get(_proxy_path(), headers={"Range": "bytes=100-199"})

        assert fake.sent_headers["Range"] == "bytes=100-199"
        assert fake.sent_headers["Referer"] == "https://www.bilibili.com/"
        assert "User-Agent" in fake.sent_headers

    def test_corrects_octet_stream_content_type(self, monkeypatch):
        fake = _FakeClient(_FakeOctetStreamUpstream())
        monkeypatch.setattr("biliaudio.proxy.httpx.Client", lambda **kwargs: fake)
        app = create_app()
        app.config.update(TESTING=True)

        with app.test_client() as test_client:
            response = test_client.get(_proxy_path())

        assert response.headers["Content-Type"] == "audio/mp4"
