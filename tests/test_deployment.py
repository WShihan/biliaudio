"""Vercel 部署契约测试。

固化两个曾经踩过的坑：

1. 入口点必须在仓库根目录（`app.py`）。Vercel 的 Python 构建器按
   `app.py` → `index.py` → `server.py` → … 的顺序在根目录、`src/`、`app/`、`api/`
   中探测，根目录的 `app.py` 会被挂载到 `/`。
2. 不能用 rewrite 把请求转发到函数路径。rewrite 会改写 Lambda 收到的请求路径，
   而 WSGI 的 `PATH_INFO` 直接取自该路径，Flask 内部路由会全部失配。
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from app import app as entry_app

ROOT = Path(__file__).resolve().parent.parent
VERCEL_CONFIG = ROOT / "vercel.json"


@pytest.fixture(scope="module")
def vercel_config() -> dict:
    return json.loads(VERCEL_CONFIG.read_text(encoding="utf-8"))


class TestEntrypoint:
    def test_root_app_py_is_the_flask_entrypoint(self):
        assert (ROOT / "app.py").is_file()
        assert entry_app is not None
        assert entry_app.name == "biliaudio.app"

    @pytest.mark.parametrize(
        "path",
        ["/", "/healthz", "/static/css/style.css", "/static/js/player.js"],
    )
    def test_assets_and_pages_serve_from_root(self, path):
        """入口不依赖任何路径前缀，服务端直接以原始路径匹配路由。"""
        response = entry_app.test_client().get(path)
        assert response.status_code == 200, f"{path} 返回 {response.status_code}"

    def test_index_is_not_mounted_under_api_prefix(self):
        """回归保护：入口应挂在 `/`，而不是 `/api/index`。"""
        client = entry_app.test_client()
        assert client.get("/").status_code == 200
        assert client.get("/api/index").status_code == 404


class TestVercelConfig:
    def test_has_no_rewrites(self, vercel_config):
        """rewrite 会破坏 PATH_INFO，路由必须交给 Vercel 原生映射。"""
        assert "rewrites" not in vercel_config
        assert "routes" not in vercel_config

    def test_has_no_bogus_runtime(self, vercel_config):
        """`runtime` 必须是 npm 包标识（如 @vercel/python@4.3.0），不要手写 python3.12。"""
        function_config = vercel_config["functions"]["app.py"]
        assert "runtime" not in function_config
        assert function_config["maxDuration"] > 0

    def test_includes_non_python_assets(self, vercel_config):
        """模板与静态资源不是 .py，必须显式打包进函数。

        `includeFiles` 是 `functions` 的子属性（顶层没有这个字段，官方 schema 的
        `additionalProperties: false` 会直接报 "should NOT have additional property"），
        类型是单个 glob 字符串、长度上限 256。
        """
        includes = vercel_config["functions"]["app.py"]["includeFiles"]
        assert isinstance(includes, str)
        assert len(includes) <= 256
        for pattern in ("biliaudio/", "templates/", "static/"):
            assert pattern in includes
