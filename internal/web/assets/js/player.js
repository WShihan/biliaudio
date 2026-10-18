/* 底部播放器 / 表单增强 / 下载状态
 *
 * 用原生 <audio> 实现，不依赖任何外部 CDN：静态资源要全部内嵌进二进制，
 * 引用外部脚本会破坏「单文件部署」这一点。
 */
(function () {
  'use strict';

  var STORE_KEY = 'biliaudio:downloaded';

  var bar = document.getElementById('player-bar');
  var player = document.getElementById('player');
  var titleNode = document.getElementById('player-title');
  var artistNode = document.getElementById('player-artist');
  var form = document.getElementById('parse-form');
  var results = document.getElementById('results');
  var toastTimer = 0;

  /* ---------------- 令牌处理 ----------------
   * 服务端会种 Cookie，但部分浏览器（http + 局域网 IP、隐私模式、部分 Safari）
   * 不会保存，因此这里再从页面里读一次令牌，显式附加到所有请求上。
   */
  var TOKEN = (function () {
    var meta = document.querySelector('meta[name="biliaudio-token"]');
    if (meta && meta.content) return meta.content;
    if (form && form.dataset.token) return form.dataset.token;
    return '';
  })();

  // withToken 给地址补上令牌参数；已有 token 参数则原样返回
  function withToken(url) {
    if (!TOKEN || !url) return url;
    if (url.indexOf('token=') !== -1) return url;
    return url + (url.indexOf('?') === -1 ? '?' : '&') + 'token=' + encodeURIComponent(TOKEN);
  }

  /* ---------------- 基础工具 ---------------- */

  function toast(message) {
    var node = document.createElement('div');
    node.className = 'toast';
    node.textContent = message;
    document.body.appendChild(node);
    requestAnimationFrame(function () {
      node.classList.add('is-show');
    });
    clearTimeout(toastTimer);
    toastTimer = window.setTimeout(function () {
      node.classList.remove('is-show');
      window.setTimeout(function () {
        node.remove();
      }, 250);
    }, 1800);
  }

  function readTracks() {
    var node = document.getElementById('audio-data');
    if (!node) return [];
    try {
      var parsed = JSON.parse(node.textContent || '[]');
      return Array.isArray(parsed) ? parsed : [];
    } catch (error) {
      return [];
    }
  }

  /* ---------------- 底部播放器 ---------------- */

  function syncBarVisibility() {
    if (!bar) return;
    var hasTracks = readTracks().length > 0;
    bar.classList.toggle('is-empty', !hasTracks);
    document.body.classList.toggle('has-player', hasTracks);
  }

  function loadTrack(url, name, artist) {
    if (!player || !url) return;
    var absolute = new URL(url, window.location.href).href;
    if (player.src !== absolute) {
      player.src = url;
    }
    if (titleNode) titleNode.textContent = name || '未知音频';
    if (artistNode) artistNode.textContent = artist || '';
    var playing = player.play();
    if (playing && typeof playing.catch === 'function') {
      playing.catch(function () {
        // 浏览器可能拦截自动播放，此时保留已就绪的 src 让用户手动点播放
        toast('浏览器阻止了自动播放，请点击播放按钮');
      });
    }
  }

  function highlight(url) {
    document.querySelectorAll('.js-play').forEach(function (button) {
      var row = button.closest('.part');
      button.classList.toggle('is-active', Boolean(url) && Boolean(row) && row.dataset.url === url);
    });
  }

  /* ---------------- 下载状态 ---------------- */

  function readStore() {
    try {
      return JSON.parse(localStorage.getItem(STORE_KEY) || '{}') || {};
    } catch (error) {
      return {};
    }
  }

  function writeStore(store) {
    try {
      localStorage.setItem(STORE_KEY, JSON.stringify(store));
    } catch (error) {
      /* 隐私模式下忽略 */
    }
  }

  function applyTokenToLinks() {
    if (!TOKEN) return;
    document.querySelectorAll('a.js-download').forEach(function (link) {
      link.href = withToken(link.getAttribute('href'));
    });
  }

  function restoreDownloadState() {
    var store = readStore();
    document.querySelectorAll('.part').forEach(function (row) {
      var key = (row.dataset.trackName || '') + '|' + (row.dataset.artist || '');
      if (!store[key]) return;
      var link = row.querySelector('.js-download');
      if (link) link.classList.add('is-done');
    });
  }

  /* ---------------- 事件绑定 ---------------- */

  function bindResults() {
    document.querySelectorAll('button.js-play').forEach(function (button) {
      button.addEventListener('click', function () {
        var row = button.closest('.part');
        if (!row) return;
        loadTrack(row.dataset.url, row.dataset.trackName, row.dataset.artist);
      });
    });

    document.querySelectorAll('a.js-download').forEach(function (link) {
      link.addEventListener('click', function () {
        var row = link.closest('.part');
        if (!row) return;
        link.classList.add('is-done');
        var store = readStore();
        store[(row.dataset.trackName || '') + '|' + (row.dataset.artist || '')] = Date.now();
        writeStore(store);
        toast('开始下载：' + (link.getAttribute('download') || '音频'));
      });
    });
  }

  function setLoading(loading) {
    var button = document.getElementById('submit-btn');
    if (!button) return;
    button.disabled = loading;
    button.classList.toggle('is-loading', loading);
    var label = button.querySelector('.btn-label');
    if (label) label.textContent = loading ? '解析中' : '解析';
  }

  function submitForm() {
    setLoading(true);
    var input = document.getElementById('input');
    var value = input ? input.value : '';
    // 用 URLSearchParams 构造表单体（与 FormData 对 input 字段等价），
    // 同时把值放进查询串：这样服务端与 /api/debug/echo 都能看到浏览器实际发送的内容，
    // 排查「收到的 input 为空」时不必依赖开发者工具。
    var body = new URLSearchParams();
    body.set('input', value);
    var target = withToken(form.getAttribute('action')) + '&input=' + encodeURIComponent(value);
    fetch(target, {
      method: 'POST',
      body: body,
      headers: {
        'X-Requested-With': 'fetch',
        'Content-Type': 'application/x-www-form-urlencoded;charset=UTF-8'
      }
    })
      .then(function (response) {
        if (!response.ok) throw new Error('HTTP ' + response.status);
        return response.text();
      })
      .then(function (html) {
        results.innerHTML = html;
        syncBarVisibility();
        bindResults();
        applyTokenToLinks();
        restoreDownloadState();
        window.history.replaceState(null, '', window.location.pathname);
        results.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
      })
      .catch(function () {
        toast('解析失败，请稍后重试');
      })
      .finally(function () {
        setLoading(false);
      });
  }

  function bindForm() {
    if (!form) return;
    form.addEventListener('submit', function (event) {
      event.preventDefault();
      var input = document.getElementById('input');
      var value = input ? input.value.trim() : '';
      if (!value) {
        var notice = document.getElementById('notice');
        if (notice) {
          notice.classList.remove('is-shake');
          void notice.offsetWidth;
          notice.classList.add('is-shake');
        }
        toast('请先粘贴链接或 BV 号');
        return;
      }
      submitForm();
    });

    var input = document.getElementById('input');
    if (input) {
      input.addEventListener('keydown', function (event) {
        if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
          event.preventDefault();
          if (form.requestSubmit) {
            form.requestSubmit();
          } else {
            form.dispatchEvent(new Event('submit'));
          }
        }
      });
    }
  }

  if (player) {
    player.addEventListener('play', function () {
      highlight(player.getAttribute('src'));
    });
    player.addEventListener('pause', function () {
      highlight('');
    });
    if ('disablePictureInPicture' in player) {
      player.disablePictureInPicture = true;
    }
  }

  syncBarVisibility();
  bindResults();
  applyTokenToLinks();
  restoreDownloadState();
  bindForm();
})();
