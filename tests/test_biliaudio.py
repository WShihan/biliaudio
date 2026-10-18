"""离线单元测试：不发起任何网络请求。"""

from __future__ import annotations

import pytest

from biliaudio import utils
from biliaudio.api import build_video_info, parse_videos, pick_best_audio
from biliaudio.errors import BiliError
from biliaudio.parser import extract_ids
from biliaudio.proxy import ensure_allowed, target_from_token


class TestParser:
    def test_extracts_bv_and_av(self):
        ids = extract_ids("看这个 BV1GJ411x7h7 还有 av80433022", resolve_short=False)
        assert ids == ["BV1GJ411x7h7", "av80433022"]

    def test_bv_case_is_preserved_and_deduplicated(self):
        ids = extract_ids("BV1GJ411x7h7 BV1GJ411x7h7", resolve_short=False)
        assert ids == ["BV1GJ411x7h7"]

    def test_lowercase_bv_prefix_is_normalized(self):
        ids = extract_ids("bv1GJ411x7h7", resolve_short=False)
        assert ids == ["BV1GJ411x7h7"]

    def test_reads_ids_from_full_url(self):
        ids = extract_ids(
            "https://www.bilibili.com/video/BV1GJ411x7h7?spm_id_from=333.999",
            resolve_short=False,
        )
        assert ids == ["BV1GJ411x7h7"]

    def test_returns_empty_for_plain_text(self):
        assert extract_ids("这里没有任何视频信息", resolve_short=False) == []


class TestPlayUrl:
    def test_picks_highest_bandwidth(self):
        data = {
            "dash": {
                "audio": [
                    {"bandwidth": 64000, "baseUrl": "low.m4s"},
                    {"bandwidth": 192000, "baseUrl": "high.m4s"},
                    {"bandwidth": 132000, "baseUrl": "mid.m4s"},
                ]
            }
        }
        assert pick_best_audio(data) == "high.m4s"

    def test_falls_back_to_backup_url(self):
        data = {"dash": {"audio": [{"bandwidth": 1000, "backup_url": ["backup.m4s"]}]}}
        assert pick_best_audio(data) == "backup.m4s"

    def test_returns_none_without_dash_audio(self):
        assert pick_best_audio({"dash": {"audio": []}}) is None
        assert pick_best_audio({}) is None


class TestVideoInfo:
    VIEW_DATA = {
        "bvid": "BV1GJ411x7h7",
        "cid": 137649199,
        "title": "测试视频",
        "pic": "http://i0.hdslb.com/cover.jpg",
        "duration": 3661,
        "pubdate": 1577835803,
        "desc": "简介",
        "owner": {"name": "UP 主"},
        "stat": {"view": 12345, "like": 678, "coin": 9, "favorite": 1, "share": 2, "danmaku": 3},
    }

    def test_single_page_uses_full_audio(self, monkeypatch):
        monkeypatch.setattr("biliaudio.api.fetch_audio_url", lambda bvid, cid: "https://x.m4s")
        video = build_video_info(self.VIEW_DATA)
        assert video.duration == "1:01:01"
        assert video.views == "1.2万"
        assert video.cover.startswith("https://")
        assert [part.name for part in video.parts] == ["完整音频"]
        assert video.parts[0].filename == "测试视频"

    def test_multi_page_skips_failed_part(self, monkeypatch):
        def fake(bvid, cid):
            if cid == 2:
                raise BiliError("没有可用音频")
            return "https://x.m4s"

        monkeypatch.setattr("biliaudio.api.fetch_audio_url", fake)
        data = dict(self.VIEW_DATA)
        data.pop("cid")
        data["pages"] = [
            {"cid": 1, "page": 1, "part": "第一话", "duration": 60},
            {"cid": 2, "page": 2, "part": "第二话", "duration": 60},
        ]
        video = build_video_info(data)
        assert [part.name for part in video.parts] == ["P1 · 第一话"]

    def test_parse_videos_reports_failure(self, monkeypatch):
        def boom(_):
            raise BiliError("视频不存在或已被删除")

        monkeypatch.setattr("biliaudio.api.fetch_video_info", boom)
        videos, failures = parse_videos("BV1GJ411x7h7")
        assert videos == []
        assert "视频不存在或已被删除" in failures[0]


class TestProxyGuard:
    @pytest.mark.parametrize(
        "url",
        [
            "https://xy116x196x140x37xy.mcdn.bilivideo.cn:8082/v1/resource/a.m4s",
            "https://upos-sz-mirrorcos.bilivideo.com/a.m4s",
            "https://i0.hdslb.com/a.m4s",
        ],
    )
    def test_allows_bilibili_cdn(self, url):
        ensure_allowed(url)

    @pytest.mark.parametrize(
        "url",
        [
            "http://127.0.0.1:8000/admin",
            "http://169.254.169.254/latest/meta-data/",
            "http://evil-bilivideo.com.attacker.net/a.m4s",
            "file:///etc/passwd",
        ],
    )
    def test_rejects_other_hosts(self, url):
        with pytest.raises(BiliError):
            ensure_allowed(url)

    def test_round_trip_token(self):
        url = "https://upos-sz-mirrorcos.bilivideo.com/a.m4s?token=1"
        target = target_from_token(utils.encode_base64_url(url), "歌名")
        assert target.url == url
        assert target.download_name == "歌名"

    def test_rejects_tampered_token(self):
        with pytest.raises(BiliError):
            target_from_token("not-base64!!", "x")


class TestUtils:
    def test_format_count(self):
        assert utils.format_count(999) == "999"
        assert utils.format_count(12_000) == "1.2万"
        assert utils.format_count(150_000_000) == "1.5亿"

    def test_sanitize_filename(self):
        assert utils.sanitize_filename("你好/世界?*") == "你好_世界"
        assert utils.sanitize_filename("a  b___c") == "a b_c"
        assert utils.sanitize_filename("///").startswith("bilibili_audio_")

    def test_ascii_fallback(self):
        assert utils.ascii_fallback("纯中文标题") == "bilibili_audio"
        assert utils.ascii_fallback("Song 歌名") == "Song"

    def test_truncate(self):
        assert utils.truncate("abcdef", 3) == "abc..."
        assert utils.truncate("abc", 3) == "abc"
