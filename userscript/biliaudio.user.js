// ==UserScript==
// @name         Bilibili 视频音频下载
// @name:zh-CN   B站视频音频下载
// @namespace    https://github.com/WShihan/biliaudio
// @version      1.0.0
// @description  在 B 站视频页一键下载当前分 P 的音频，自动识别视频与分 P，无需本地服务
// @description:zh-CN  在 B 站视频页一键下载当前分 P 的音频，自动识别视频与分 P，无需本地服务
// @author       WShihan
// @license      MIT
// @homepageURL  https://github.com/WShihan/biliaudio
// @supportURL   https://github.com/WShihan/biliaudio/issues
// @match        https://www.bilibili.com/video/*
// @match        https://www.bilibili.com/list/*
// @grant        GM_xmlhttpRequest
// @grant        unsafeWindow
// @connect      api.bilibili.com
// @connect      bilibili.com
// @connect      bilivideo.com
// @connect      bilivideo.cn
// @connect      hdslb.com
// @connect      akamaized.net
// @connect      szbdyd.com
// @run-at       document-idle
// @noframes
// ==/UserScript==

(function () {
  'use strict';

  const BUTTON_ID = 'biliaudio-download';
  const STYLE_ID = 'biliaudio-style';
  const TOAST_ID = 'biliaudio-toast';
  const WRAP_CLASS = 'biliaudio-item-wrap';
  const DEFAULT_TEXT = '下载音频';
  const DEFAULT_TITLE = '下载当前视频的音频（自动识别当前分 P）';
  const API_REFERER = 'https://www.bilibili.com/';
  const API_HEADERS = {
    Accept: 'application/json, text/plain, */*',
    Referer: API_REFERER,
  };
  const MEDIA_HEADERS = {
    Accept: '*/*',
    Referer: API_REFERER,
  };
  const PLAYURL_API = 'https://api.bilibili.com/x/player/playurl';
  const VIEW_API = 'https://api.bilibili.com/x/web-interface/view';

  // 当前按钮状态。工具栏被 B 站重新渲染后，新按钮会从这里恢复显示。
  let status = { kind: 'idle', text: DEFAULT_TEXT, title: DEFAULT_TITLE, progress: 0 };
  let lastHref = location.href;

  // ---------------------------------------------------------------- 小工具

  function pageWindow() {
    try {
      return typeof unsafeWindow !== 'undefined' && unsafeWindow ? unsafeWindow : window;
    } catch (error) {
      return window;
    }
  }

  function cleanText(value) {
    return String(value == null ? '' : value)
      .replace(/\s+/g, ' ')
      .trim();
  }

  function normalizeBvid(value) {
    const text = cleanText(value);
    const match = text.match(/^bv([0-9a-z]{10})$/i);
    return match ? 'BV' + match[1] : text;
  }

  function formatBytes(bytes) {
    const value = Number(bytes);
    if (!Number.isFinite(value) || value <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB'];
    let number = value;
    let index = 0;
    while (number >= 1024 && index < units.length - 1) {
      number /= 1024;
      index += 1;
    }
    return (number >= 100 ? number.toFixed(0) : number.toFixed(1)) + ' ' + units[index];
  }

  function sanitizeFilename(value) {
    let name = String(value == null ? '' : value)
      .replace(/[\\/:*?"<>|\u0000-\u001f\u007f]/g, ' ')
      .replace(/\s+/g, ' ')
      .replace(/^[.\s]+|[.\s]+$/g, '')
      .trim();
    if (!name) name = 'bilibili-audio';
    // Windows 保留名（CON、NUL、COM1…）不能直接作为文件名
    if (/^(con|prn|aux|nul|com[1-9]|lpt[1-9])(\.|$)/i.test(name)) name = '_' + name;
    const chars = Array.from(name);
    if (chars.length > 120) {
      name = chars.slice(0, 120).join('').replace(/[.\s]+$/g, '').trim();
    }
    return name || 'bilibili-audio';
  }

  function formatError(error) {
    const message = error && error.message ? error.message : String(error || '未知错误');
    if (/接口错误\s*-(352|412)/.test(message)) {
      return message + '（可能触发了 B 站风控，请确认已登录后稍后再试）';
    }
    if (/接口错误\s*-404/.test(message)) {
      return message + '（视频不存在或已被删除）';
    }
    if (/HTTP\s*403/.test(message)) {
      return message + '（音频直链可能已过期，请刷新页面后重试）';
    }
    return message;
  }

  // -------------------------------------------------------- 页面信息读取

  function getInitialState() {
    try {
      const state = pageWindow().__INITIAL_STATE__;
      return state && typeof state === 'object' ? state : null;
    } catch (error) {
      return null;
    }
  }

  function readDocumentTitle() {
    return cleanText(document.title)
      .replace(/[_\-|]\s*哔哩哔哩.*$/i, '')
      .replace(/\s*-\s*哔哩哔哩.*$/i, '')
      .trim();
  }

  function readText(selector) {
    const element = document.querySelector(selector);
    return element ? cleanText(element.textContent) : '';
  }

  function readAttribute(selector, attribute) {
    const element = document.querySelector(selector);
    return element ? cleanText(element.getAttribute(attribute)) : '';
  }

  /**
   * 从当前 URL 和页面 __INITIAL_STATE__ 里读出视频信息。
   * 只做同步读取；cid 缺失时由 resolveContext() 再调一次 view 接口补齐。
   */
  function readContext() {
    const url = new URL(location.href);
    const params = url.searchParams;

    let bvid = '';
    let aid = 0;
    const pathMatch = url.pathname.match(/\/video\/(BV[0-9A-Za-z]{10}|av\d+)/i);
    if (pathMatch) {
      const token = pathMatch[1];
      if (/^av/i.test(token)) aid = Number(token.slice(2)) || 0;
      else bvid = normalizeBvid(token);
    }
    if (!bvid && !aid) {
      const queryBvid = params.get('bvid');
      if (queryBvid) bvid = normalizeBvid(queryBvid);
      const queryAid = params.get('aid') || params.get('avid');
      if (queryAid) aid = Number(queryAid) || 0;
    }

    const state = getInitialState();
    const videoData = (state && state.videoData) || {};
    const stateBvid = normalizeBvid(videoData.bvid || (state && state.bvid) || '');
    const stateAid = Number(videoData.aid || (state && state.aid) || 0) || 0;
    if (!bvid && stateBvid) bvid = stateBvid;
    // SPA 切换视频时 __INITIAL_STATE__ 可能还是上一个视频的，必须比对 ID。
    const stateMatches =
      (!bvid || !stateBvid || stateBvid === bvid) && (!aid || !stateAid || aid === stateAid);
    if (!aid && stateMatches) aid = stateAid;

    const rawPage = params.get('p') || (stateMatches && state && state.p) || 1;
    let page = Math.max(1, parseInt(rawPage, 10) || 1);
    const pages = stateMatches && Array.isArray(videoData.pages) ? videoData.pages : [];

    let cid = Number(params.get('cid') || 0) || 0;
    if (cid && pages.length) {
      const byCid = pages.find((item) => Number(item.cid) === cid);
      if (byCid) page = Number(byCid.page) || page;
    }
    if (!cid) {
      const pageData = pages.find((item) => Number(item.page) === page);
      if (pageData) cid = Number(pageData.cid) || 0;
    }
    if (!cid && stateMatches) {
      cid = Number(videoData.cid || (state && state.cid) || 0) || 0;
    }

    let title = stateMatches ? cleanText(videoData.title) : '';
    if (!title) {
      title = readText('h1.video-title') || readAttribute('h1.video-title', 'title') || readDocumentTitle();
    }

    let author = stateMatches ? cleanText(videoData.owner && videoData.owner.name) : '';
    if (!author) {
      author = readText('.up-name') || readText('.up-info--name') || readText('.username');
    }

    let partTitle = '';
    if (pages.length) {
      const current =
        pages.find((item) => Number(item.cid) === cid) ||
        pages.find((item) => Number(item.page) === page);
      if (current) partTitle = cleanText(current.part);
    }

    return { bvid, aid, cid, page, title, author, partTitle, pages };
  }

  function contextFromView(data, base) {
    if (!data) throw new Error('B 站接口没有返回视频信息');
    const pages = Array.isArray(data.pages) ? data.pages : [];
    let page = Number(base.page) || 1;
    let cid = Number(base.cid) || 0;

    if (!cid) {
      const pageData = pages.find((item) => Number(item.page) === page);
      if (pageData) cid = Number(pageData.cid) || 0;
    }
    if (!cid) cid = Number(data.cid) || 0;
    if (!cid) throw new Error('无法获取当前视频的音频 cid');
    if (pages.length) {
      const byCid = pages.find((item) => Number(item.cid) === cid);
      if (byCid) page = Number(byCid.page) || page;
    }

    let partTitle = cleanText(base.partTitle);
    if (!partTitle && pages.length) {
      const current =
        pages.find((item) => Number(item.cid) === cid) ||
        pages.find((item) => Number(item.page) === page);
      if (current) partTitle = cleanText(current.part);
    }

    return {
      bvid: normalizeBvid(data.bvid) || base.bvid,
      aid: Number(data.aid) || base.aid,
      cid,
      page,
      title: cleanText(data.title) || base.title,
      author: cleanText(data.owner && data.owner.name) || base.author,
      partTitle,
      pages,
    };
  }

  async function resolveContext() {
    const context = readContext();
    if (context.bvid && context.cid) return context;
    if (!context.bvid && !context.aid) {
      throw new Error('无法识别当前视频，请刷新页面后重试');
    }
    const params = context.bvid ? { bvid: context.bvid } : { aid: String(context.aid) };
    const data = await apiGet(VIEW_API, params);
    return contextFromView(data, context);
  }

  // ------------------------------------------------------------ B 站接口

  function gmRequest(options) {
    return new Promise((resolve, reject) => {
      const request = typeof GM_xmlhttpRequest === 'function' ? GM_xmlhttpRequest : null;
      if (!request) {
        reject(new Error('当前脚本管理器不支持 GM_xmlhttpRequest，请使用 Tampermonkey 或 Violentmonkey'));
        return;
      }
      request(
        Object.assign(
          {
            timeout: 30000,
            anonymous: false,
          },
          options,
          {
            onload: resolve,
            onerror: () => reject(new Error('网络请求失败')),
            ontimeout: () => reject(new Error('网络请求超时')),
            onabort: () => reject(new Error('网络请求已取消')),
          }
        )
      );
    });
  }

  function buildApiUrl(endpoint, params) {
    const url = new URL(endpoint);
    if (params) {
      Object.keys(params).forEach((key) => {
        const value = params[key];
        if (value !== undefined && value !== null && value !== '') {
          url.searchParams.set(key, String(value));
        }
      });
    }
    return url.href;
  }

  function unwrapPayload(payload) {
    if (!payload || payload.code !== 0) {
      const code = payload && payload.code !== undefined ? payload.code : 'unknown';
      const message = payload && payload.message ? payload.message : '未知错误';
      throw new Error('B 站接口错误 ' + code + '：' + message);
    }
    return payload.data;
  }

  /**
   * 优先用页面自己的 fetch 请求接口：Origin/Referer/Cookie 都与播放器一致，
   * 不会因为扩展的 Origin 被 B 站风控（HTTP 412）。
   */
  async function apiGetViaPage(url) {
    const page = pageWindow();
    const fetchFn = page && typeof page.fetch === 'function' ? page.fetch : null;
    if (!fetchFn) throw new Error('页面 fetch 不可用');

    const response = await fetchFn.call(page, url, {
      method: 'GET',
      credentials: 'include',
      headers: { Accept: 'application/json, text/plain, */*' },
      cache: 'no-store',
    });
    if (!response || response.status < 200 || response.status >= 300) {
      throw new Error('B 站接口 HTTP ' + (response ? response.status : 'unknown'));
    }

    let payload;
    try {
      payload = await response.json();
    } catch (error) {
      throw new Error('B 站接口返回了无法解析的数据');
    }
    return unwrapPayload(payload);
  }

  async function apiGetViaGm(url) {
    const response = await gmRequest({
      method: 'GET',
      url,
      headers: API_HEADERS,
      timeout: 20000,
    });
    if (response.status !== 200) {
      throw new Error('B 站接口 HTTP ' + response.status);
    }

    let payload;
    try {
      payload = JSON.parse(response.responseText);
    } catch (error) {
      throw new Error('B 站接口返回了无法解析的数据');
    }
    return unwrapPayload(payload);
  }

  async function apiGet(endpoint, params) {
    const url = buildApiUrl(endpoint, params);
    try {
      return await apiGetViaPage(url);
    } catch (pageError) {
      // 页面 fetch 被 CSP/CORS 拦截时，退回脚本管理器的跨域请求
      try {
        return await apiGetViaGm(url);
      } catch (gmError) {
        throw pageError;
      }
    }
  }

  function fetchPlayData(context) {
    const params = {
      cid: String(context.cid),
      fnval: '4048',
      fnver: '0',
      fourk: '1',
    };
    if (context.bvid) params.bvid = context.bvid;
    else params.avid = String(context.aid);
    return apiGet(PLAYURL_API, params);
  }

  /**
   * 页面里的 __playinfo__ 是播放器已经拿到的数据，可作为接口失败时的兜底。
   * 只在 cid 对得上时使用，避免用户切换分 P 后拿到上一个 P 的音频。
   */
  function readCachedPlayData(context) {
    try {
      const info = pageWindow().__playinfo__;
      if (!info || !info.data || !info.data.dash) return null;
      if (info.code !== undefined && Number(info.code) !== 0) return null;
      const cachedCid = Number(info.data.cid || info.data.last_play_cid || 0);
      if (cachedCid && context.cid && cachedCid !== Number(context.cid)) return null;
      return info.data;
    } catch (error) {
      return null;
    }
  }

  async function loadPlayData(context) {
    try {
      return await fetchPlayData(context);
    } catch (error) {
      const cached = readCachedPlayData(context);
      if (cached) return cached;
      throw error;
    }
  }

  // ------------------------------------------------------------ 音频选择

  function normalizeMediaUrl(raw) {
    const value = String(raw == null ? '' : raw).trim();
    if (!value) return '';
    if (value.startsWith('//')) return 'https:' + value;
    return value;
  }

  function candidateUrls(stream) {
    const urls = [];
    const add = (value) => {
      const url = normalizeMediaUrl(value);
      if (url && urls.indexOf(url) === -1) urls.push(url);
    };
    add(stream.baseUrl);
    add(stream.base_url);
    const backups = stream.backupUrl || stream.backup_url;
    if (Array.isArray(backups)) backups.forEach(add);
    return urls;
  }

  /**
   * 与 CLI 保持一致：优先从 dash.audio 里挑 bandwidth 最大的一档（m4a，兼容性最好）。
   * 如果接口只返回了杜比/无损轨（dash.audio 为空），再回退到那些音轨。
   */
  function pickBestAudio(data) {
    const dash = data && data.dash;
    if (!dash) return null;

    const primary = Array.isArray(dash.audio) ? dash.audio : dash.audio ? [dash.audio] : [];
    const fallback = [];
    const collect = (value) => {
      if (!value) return;
      if (Array.isArray(value)) value.forEach(collect);
      else if (typeof value === 'object') fallback.push(value);
    };
    collect(dash.dolby && dash.dolby.audio);
    collect(dash.flac && dash.flac.audio);
    if (
      dash.flac &&
      typeof dash.flac === 'object' &&
      !dash.flac.audio &&
      (dash.flac.baseUrl || dash.flac.base_url)
    ) {
      collect(dash.flac);
    }

    const pools = primary.length ? [primary, fallback] : [fallback];
    for (const pool of pools) {
      let best = null;
      for (const stream of pool) {
        if (!stream || !candidateUrls(stream).length) continue;
        const bandwidth = Number(stream.bandwidth) || 0;
        if (!best || bandwidth > best.bandwidth) best = { stream, bandwidth };
      }
      if (best) return best.stream;
    }
    return null;
  }

  function extensionFor(stream) {
    const mime = String((stream && (stream.mimeType || stream.mime_type)) || '').toLowerCase();
    const codecs = String((stream && stream.codecs) || '').toLowerCase();
    if (mime.indexOf('flac') !== -1 || codecs.indexOf('flac') !== -1) return 'flac';
    if (mime.indexOf('mpeg') !== -1 || codecs.startsWith('mp3')) return 'mp3';
    if (mime.indexOf('ogg') !== -1 || codecs.indexOf('opus') !== -1) return 'ogg';
    if (mime.indexOf('wav') !== -1) return 'wav';
    return 'm4a';
  }

  function buildFilename(context, stream) {
    const extension = extensionFor(stream);
    const pieces = [];
    const title = cleanText(context.title);
    if (title) pieces.push(title);

    if (Array.isArray(context.pages) && context.pages.length > 1) {
      const part = cleanText(context.partTitle);
      if (part && title.indexOf(part) === -1) pieces.push('P' + context.page + ' ' + part);
      else pieces.push('P' + context.page);
    }

    return sanitizeFilename(pieces.join(' - ')) + '.' + extension;
  }

  // ------------------------------------------------------------ 下载与保存

  function fetchBlobOnce(url, onProgress, withHeaders) {
    const details = {
      method: 'GET',
      url,
      responseType: 'blob',
      timeout: 0,
      anonymous: false,
      onprogress: (event) => {
        if (!event) return;
        onProgress(Number(event.loaded) || 0, Number(event.total) || 0);
      },
    };
    if (withHeaders) details.headers = MEDIA_HEADERS;

    return gmRequest(details).then((response) => {
      if (response.status < 200 || response.status >= 300) {
        const error = new Error('音频下载失败：HTTP ' + response.status);
        error.status = response.status;
        throw error;
      }
      const body = response.response;
      if (!body) throw new Error('音频下载失败：返回内容为空');
      // 用结构化判断而不是 instanceof，避免脚本管理器与页面处于不同 realm
      if (typeof body === 'object' && typeof body.size === 'number' && typeof body.slice === 'function') {
        return body;
      }
      // 少数脚本管理器即使指定了 responseType: 'blob' 也可能返回 ArrayBuffer
      if (typeof body === 'object' && typeof body.byteLength === 'number') {
        return new Blob([body], { type: 'application/octet-stream' });
      }
      throw new Error('音频下载失败：返回内容不是二进制数据');
    });
  }

  async function fetchBlob(url, onProgress) {
    try {
      return await fetchBlobOnce(url, onProgress, true);
    } catch (error) {
      // 极少数环境下脚本管理器不允许改写 Referer，去掉它再试一次
      if (error && (error.status === 401 || error.status === 403)) {
        return fetchBlobOnce(url, onProgress, false);
      }
      throw error;
    }
  }

  function sleep(ms) {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }

  async function downloadStream(stream, onProgress) {
    const urls = candidateUrls(stream);
    if (!urls.length) throw new Error('音频直链为空');

    let lastError = null;
    for (let index = 0; index < urls.length; index += 1) {
      try {
        return await fetchBlob(urls[index], onProgress);
      } catch (error) {
        lastError = error;
        onProgress(0, 0);
        if (index < urls.length - 1) await sleep(400);
      }
    }
    throw lastError || new Error('音频下载失败');
  }

  function saveBlob(blob, filename) {
    const objectUrl = URL.createObjectURL(blob);
    const anchor = document.createElement('a');
    anchor.href = objectUrl;
    anchor.download = filename;
    anchor.rel = 'noopener';
    anchor.style.display = 'none';
    (document.body || document.documentElement).appendChild(anchor);
    anchor.click();
    anchor.remove();
    // 给浏览器留出读取 Blob 的时间，再释放 object URL
    setTimeout(() => URL.revokeObjectURL(objectUrl), 60000);
  }

  // ------------------------------------------------------------ 按钮界面

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const style = document.createElement('style');
    style.id = STYLE_ID;
    style.textContent = [
      '.biliaudio-item-wrap{display:inline-flex;align-items:center;}',
      '#biliaudio-download{position:relative;width:auto;min-width:92px;cursor:pointer;user-select:none;padding-bottom:2px;}',
      '#biliaudio-download:hover{color:#00aeec;}',
      '#biliaudio-download[data-state="resolving"],#biliaudio-download[data-state="downloading"]{cursor:progress;opacity:.8;}',
      '#biliaudio-download .biliaudio-text{white-space:nowrap;}',
      '#biliaudio-download .biliaudio-progress{position:absolute;left:0;right:0;bottom:0;height:2px;',
      'background:rgba(0,174,236,.15);border-radius:1px;display:none;overflow:hidden;}',
      '#biliaudio-download[data-state="resolving"] .biliaudio-progress,',
      '#biliaudio-download[data-state="downloading"] .biliaudio-progress{display:block;}',
      '#biliaudio-download .biliaudio-progress-bar{display:block;height:100%;width:0;background:#00aeec;',
      'transition:width .2s ease;}',
      '#biliaudio-toast{position:fixed;left:50%;bottom:36px;transform:translateX(-50%) translateY(8px);',
      'z-index:2147483647;max-width:min(90vw,560px);padding:10px 16px;border-radius:8px;',
      'background:rgba(20,20,20,.92);color:#fff;font-size:13px;line-height:1.5;text-align:center;',
      'box-shadow:0 8px 30px rgba(0,0,0,.25);opacity:0;pointer-events:none;word-break:break-word;',
      'transition:opacity .2s ease,transform .2s ease;}',
      '#biliaudio-toast.biliaudio-toast-show{opacity:1;transform:translateX(-50%) translateY(0);}',
      '#biliaudio-toast.biliaudio-toast-error{background:rgba(203,45,62,.95);}',
    ].join('');
    (document.head || document.documentElement).appendChild(style);
  }

  function getButton() {
    return document.getElementById(BUTTON_ID);
  }

  function applyStatus(button) {
    if (!button) return;
    const text = button.querySelector('.biliaudio-text');
    if (text) text.textContent = status.text;
    button.title = status.title;
    button.dataset.state = status.kind;
    button.setAttribute('aria-busy', status.kind === 'resolving' || status.kind === 'downloading' ? 'true' : 'false');
    const bar = button.querySelector('.biliaudio-progress-bar');
    if (bar) bar.style.width = Math.max(0, Math.min(100, status.progress)) + '%';
  }

  function setStatus(patch) {
    status = Object.assign({}, status, patch);
    applyStatus(getButton());
  }

  function resetStatus() {
    status = { kind: 'idle', text: DEFAULT_TEXT, title: DEFAULT_TITLE, progress: 0 };
    applyStatus(getButton());
  }

  let toastTimer = 0;

  function showToast(message, isError) {
    let toast = document.getElementById(TOAST_ID);
    if (!toast) {
      toast = document.createElement('div');
      toast.id = TOAST_ID;
      toast.setAttribute('role', 'status');
      toast.setAttribute('aria-live', 'polite');
      (document.body || document.documentElement).appendChild(toast);
    }
    toast.textContent = message;
    toast.classList.toggle('biliaudio-toast-error', Boolean(isError));
    toast.classList.add('biliaudio-toast-show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(
      () => toast.classList.remove('biliaudio-toast-show'),
      isError ? 6000 : 3000
    );
  }

  function createButton() {
    const wrap = document.createElement('div');
    wrap.className = 'toolbar-left-item-wrap ' + WRAP_CLASS;

    const button = document.createElement('div');
    button.id = BUTTON_ID;
    button.className = 'video-toolbar-left-item biliaudio-toolbar-item';
    button.setAttribute('role', 'button');
    button.setAttribute('tabindex', '0');
    button.title = DEFAULT_TITLE;
    button.innerHTML = [
      '<svg class="video-toolbar-item-icon" width="28" height="28" viewBox="0 0 28 28"',
      ' xmlns="http://www.w3.org/2000/svg" aria-hidden="true">',
      '<path fill-rule="evenodd" clip-rule="evenodd" fill="currentColor"',
      ' d="M14 3.5a1.25 1.25 0 0 1 1.25 1.25v11.19l3.72-3.72a1.25 1.25 0 1 1 1.77 1.77l-5.86 5.86a1.25 1.25 0 0 1-1.77 0l-5.86-5.86a1.25 1.25 0 1 1 1.77-1.77l3.73 3.72V4.75A1.25 1.25 0 0 1 14 3.5Zm-8.75 18.25a1.25 1.25 0 0 0 0 2.5h17.5a1.25 1.25 0 0 0 0-2.5H5.25Z"/>',
      '</svg>',
      '<span class="video-toolbar-item-text biliaudio-text">' + DEFAULT_TEXT + '</span>',
      '<span class="biliaudio-progress" aria-hidden="true"><span class="biliaudio-progress-bar"></span></span>',
    ].join('');

    button.addEventListener('click', handleClick);
    button.addEventListener('keydown', (event) => {
      if (event.key === 'Enter' || event.key === ' ' || event.key === 'Spacebar') {
        event.preventDefault();
        event.stopPropagation();
        handleClick(event);
      }
    });

    wrap.appendChild(button);
    applyStatus(button);
    return wrap;
  }

  function findToolbarContainer() {
    const toolbar = document.querySelector('.video-toolbar-left');
    if (!toolbar) return null;
    // 真正的按钮在 .video-toolbar-left-main 里；保持同样的 DOM 层级，
    // 这样按钮的间距、对齐方式会和点赞/投币等按钮一致。
    return toolbar.querySelector('.video-toolbar-left-main') || toolbar;
  }

  function injectButton() {
    ensureStyle();
    const container = findToolbarContainer();
    if (!container) return false;

    const existing = getButton();
    if (existing && container.contains(existing)) {
      applyStatus(existing);
      return true;
    }
    if (existing) {
      const oldWrap = existing.closest('.' + WRAP_CLASS);
      (oldWrap || existing).remove();
    }

    container.appendChild(createButton());
    return true;
  }

  let injectTimer = 0;

  function scheduleInject() {
    if (injectTimer) return;
    injectTimer = setTimeout(() => {
      injectTimer = 0;
      injectButton();
    }, 150);
  }

  // ------------------------------------------------------------ 下载流程

  async function handleClick(event) {
    event.preventDefault();
    event.stopPropagation();
    if (status.kind === 'resolving' || status.kind === 'downloading') return;

    setStatus({ kind: 'resolving', text: '解析中…', title: '正在获取音频地址…', progress: 0 });
    try {
      const context = await resolveContext();
      const data = await loadPlayData(context);
      const stream = pickBestAudio(data);
      if (!stream) {
        throw new Error('该视频没有可用的音频流（可能是旧视频、付费内容或需要登录）');
      }

      const filename = buildFilename(context, stream);
      setStatus({
        kind: 'downloading',
        text: '下载中 0%',
        title: '正在下载：' + filename,
        progress: 0,
      });

      const blob = await downloadStream(stream, (loaded, total) => {
        const percent = total > 0 ? Math.min(99, Math.floor((loaded / total) * 100)) : 0;
        const text = total > 0 ? '下载中 ' + percent + '%' : '下载中 ' + formatBytes(loaded);
        const title =
          total > 0
            ? '已下载 ' + formatBytes(loaded) + ' / ' + formatBytes(total)
            : '已下载 ' + formatBytes(loaded);
        setStatus({ kind: 'downloading', text, title, progress: percent });
      });

      saveBlob(blob, filename);
      setStatus({ kind: 'done', text: '已保存', title: '已保存：' + filename, progress: 100 });
      showToast('音频已开始下载：' + filename, false);
      setTimeout(() => {
        if (status.kind === 'done') resetStatus();
      }, 3000);
    } catch (error) {
      const message = formatError(error);
      console.error('[biliaudio]', error);
      setStatus({ kind: 'error', text: '下载失败', title: message, progress: 0 });
      showToast(message, true);
      setTimeout(() => {
        if (status.kind === 'error') resetStatus();
      }, 6000);
    }
  }

  // ------------------------------------------------------------ 初始化

  function handleUrlChange() {
    if (location.href === lastHref) return;
    lastHref = location.href;
    if (status.kind !== 'resolving' && status.kind !== 'downloading') resetStatus();
    scheduleInject();
  }

  function init() {
    ensureStyle();
    injectButton();

    const observer = new MutationObserver(scheduleInject);
    observer.observe(document.documentElement, { childList: true, subtree: true });

    window.addEventListener('popstate', handleUrlChange);
    window.addEventListener('hashchange', handleUrlChange);
    setInterval(() => {
      handleUrlChange();
      if (!getButton()) injectButton();
    }, 1000);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init, { once: true });
  } else {
    init();
  }
})();
