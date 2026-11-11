// ==UserScript==
// @name         Bilibili 视频音频下载
// @name:zh-CN   B站视频音频下载
// @namespace    https://github.com/WShihan/biliaudio
// @version      1.2.3
// @description  在 B 站视频页一键下载当前分 P 的音频，自动识别视频与分 P，无需本地服务
// @description:zh-CN  在 B 站视频页一键下载当前分 P 的音频，自动识别视频与分 P，无需本地服务
// @author       WShihan
// @license      MIT
// @homepageURL  https://github.com/WShihan/biliaudio
// @supportURL   https://github.com/WShihan/biliaudio/issues
// @match        https://www.bilibili.com/video/*
// @match        https://www.bilibili.com/list/*
// @grant        unsafeWindow
// @run-at       document-idle
// @noframes
// ==/UserScript==

(function () {
  'use strict';

  // 站点限定：只在 bilibili.com 及其子域运行。
  // @match 已经限定了页面，这里再兜底一层，避免脚本被改到别的站点后误运行。
  if (!/(^|\.)bilibili\.com$/i.test(location.hostname)) return;

  const BUTTON_ID = 'biliaudio-download';
  const STYLE_ID = 'biliaudio-style';
  const WRAP_CLASS = 'biliaudio-item-wrap';
  const DEFAULT_TITLE = '下载音频（自动识别当前分 P）';
  const PLAYURL_API = 'https://api.bilibili.com/x/player/playurl';
  const VIEW_API = 'https://api.bilibili.com/x/web-interface/view';

  // 当前按钮状态。工具栏被 B 站重新渲染后，新按钮会从这里恢复显示。
  // 按钮只显示图标，因此所有状态都通过 title 提示和图标颜色/动画表达。
  let status = { kind: 'idle', title: DEFAULT_TITLE };
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
   * 用页面自己的 fetch 请求接口：Origin/Referer/Cookie 都与播放器一致，
   * 也不会触发脚本管理器的跨域授权弹窗。
   */
  async function apiGetViaPage(url) {
    const page = pageWindow();
    const fetchFn = page && typeof page.fetch === 'function' ? page.fetch : null;
    if (!fetchFn) throw new Error('页面 fetch 不可用');

    const response = await fetchFn.call(page, url, {
      method: 'GET',
      credentials: 'include',
      headers: { Accept: 'application/json, text/plain, */*' },
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

  async function apiGet(endpoint, params) {
    return apiGetViaPage(buildApiUrl(endpoint, params));
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

  /**
   * 用页面自身的 fetch 下载音频，避免触发脚本管理器的跨域授权弹窗。
   * B 站 CDN 的 CORS 是 `*`，所以这里要用 credentials: 'omit'；
   * 带 cookie 的 `*` 会被浏览器判定为非法 CORS 响应。
   */
  async function fetchBlobViaPage(url, onProgress) {
    const page = pageWindow();
    const fetchFn = page && typeof page.fetch === 'function' ? page.fetch : null;
    if (!fetchFn) throw new Error('页面 fetch 不可用');

    const response = await fetchFn.call(page, url, {
      method: 'GET',
      credentials: 'omit',
    });
    if (!response || response.status < 200 || response.status >= 300) {
      const error = new Error('音频下载失败：HTTP ' + (response ? response.status : 'unknown'));
      error.status = response ? response.status : 0;
      throw error;
    }

    const total = Number(response.headers.get('content-length')) || 0;
    const type = response.headers.get('content-type') || 'application/octet-stream';
    if (!response.body || typeof response.body.getReader !== 'function') {
      const blob = await response.blob();
      onProgress(blob.size, blob.size);
      return blob;
    }

    const reader = response.body.getReader();
    const chunks = [];
    let loaded = 0;
    for (;;) {
      const result = await reader.read();
      if (result.done) break;
      chunks.push(result.value);
      loaded += result.value.byteLength;
      onProgress(loaded, total);
    }
    onProgress(loaded, loaded || total);
    return new Blob(chunks, { type });
  }

  async function downloadStream(stream, onProgress) {
    const urls = candidateUrls(stream);
    if (!urls.length) throw new Error('音频直链为空');

    let lastError = null;
    for (const url of urls) {
      try {
        return await fetchBlobViaPage(url, onProgress);
      } catch (error) {
        lastError = error;
        onProgress(0, 0);
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
      '#biliaudio-download{position:relative;width:32px;height:32px;min-width:0;padding:0;margin:0 12px;display:flex;',
      'align-items:center;justify-content:center;border-radius:50%;cursor:pointer;user-select:none;',
      'color:var(--text2,#61666d);transition:background-color .2s ease,color .2s ease,opacity .2s ease;}',
      '#biliaudio-download:hover{color:#00aeec;background:rgba(0,174,236,.08);}',
      '#biliaudio-download[data-state="resolving"],#biliaudio-download[data-state="downloading"]{',
      'cursor:progress;animation:biliaudio-pulse 1s ease-in-out infinite;}',
      '#biliaudio-download[data-state="done"]{color:#00aeec;}',
      '#biliaudio-download[data-state="error"]{color:#f56c6c;}',
      '#biliaudio-download .biliaudio-icon{display:block;width:28px;height:28px;pointer-events:none;}',
      '@media(min-width:1681px){#biliaudio-download{width:40px;height:40px;}',
      '#biliaudio-download .biliaudio-icon{width:36px;height:36px;}}',
      '@keyframes biliaudio-pulse{0%,100%{opacity:1;}50%{opacity:.35;}}',
    ].join('');
    (document.head || document.documentElement).appendChild(style);
  }

  function getButton() {
    return document.getElementById(BUTTON_ID);
  }

  function applyStatus(button) {
    if (!button) return;
    // 只在值变化时写 DOM，避免 MutationObserver 每 150ms 触发一次无意义的属性变更。
    if (button.title !== status.title) button.title = status.title;
    if (button.getAttribute('aria-label') !== status.title) {
      button.setAttribute('aria-label', status.title);
    }
    if (button.dataset.state !== status.kind) button.dataset.state = status.kind;
    const busy =
      status.kind === 'resolving' || status.kind === 'downloading' ? 'true' : 'false';
    if (button.getAttribute('aria-busy') !== busy) button.setAttribute('aria-busy', busy);
  }

  function setStatus(patch) {
    status = Object.assign({}, status, patch);
    applyStatus(getButton());
  }

  function resetStatus() {
    status = { kind: 'idle', title: DEFAULT_TITLE };
    applyStatus(getButton());
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
    // 图标来自需求提供的 SVG；改 fill 为 currentColor，让 hover/完成/失败状态能换色。
    button.innerHTML = [
      '<svg class="biliaudio-icon" width="28" height="28" viewBox="0 0 100 100"',
      ' xmlns="http://www.w3.org/2000/svg" aria-hidden="true">',
      '<path fill="currentColor" fill-rule="evenodd" d="M14 36 Q14 31 19 31 H34 L64 13 Q70 9 70 17 V83 Q70 91 64 87 L34 69 H19 Q14 69 14 64 Z M42 35 Q42 32 45 32 H51 Q54 32 54 35 V49 H61 Q65 49 62 53 L50 65 Q48 67 46 65 L34 53 Q31 49 35 49 H42 Z"/>',
      '<path d="M79 31 Q91 50 79 69" fill="none" stroke="currentColor" stroke-width="8" stroke-linecap="round"/>',
      '</svg>',
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
    if (existing && container.contains(existing)) return true;
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

    setStatus({ kind: 'resolving', title: '解析中…' });
    try {
      const context = await resolveContext();
      const data = await loadPlayData(context);
      const stream = pickBestAudio(data);
      if (!stream) {
        throw new Error('该视频没有可用的音频流（可能是旧视频、付费内容或需要登录）');
      }

      const filename = buildFilename(context, stream);
      setStatus({ kind: 'downloading', title: '下载中 0%：' + filename });

      const blob = await downloadStream(stream, (loaded, total) => {
        const percent = total > 0 ? Math.min(99, Math.floor((loaded / total) * 100)) : 0;
        const progress = total > 0 ? percent + '%' : formatBytes(loaded);
        setStatus({ kind: 'downloading', title: '下载中 ' + progress + '：' + filename });
      });

      saveBlob(blob, filename);
      setStatus({ kind: 'done', title: '已保存：' + filename });
      setTimeout(() => {
        if (status.kind === 'done') resetStatus();
      }, 3000);
    } catch (error) {
      const message = formatError(error);
      console.error('[biliaudio]', error);
      setStatus({ kind: 'error', title: message });
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

  function isPageReady() {
    // 三个信号同时满足才认为客户端已完成挂载：
    // 1) B 站根组件已经挂上 Vue 实例；
    // 2) 顶栏已经从服务端骨架变成有高度的渲染结果；
    // 3) 播放器 DOM 已经由骨架替换成完整播放器。
    // 只看“顶栏有高度”不够：服务端 HTML 在 hydration 前也可能有高度。
    const app = document.querySelector('#app');
    if (!app || !app.__vue__) return false;
    const header = document.querySelector('.bili-header, #biliMainHeader');
    if (!header || header.getBoundingClientRect().height <= 0) return false;
    const player = document.querySelector('#bilibili-player');
    if (!player || player.innerHTML.length < 50000) return false;
    return true;
  }

  function startWatching() {
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

  function init() {
    let started = false;
    const start = () => {
      if (started) return;
      started = true;
      startWatching();
    };

    // 关键：不能在 B 站 Vue 挂载/注水完成前往工具栏里插节点，
    // 否则会破坏 hydration，导致顶栏等组件被卸载。
    if (isPageReady()) {
      setTimeout(start, 500);
      return;
    }

    const startedAt = Date.now();
    const timer = setInterval(() => {
      if (isPageReady()) {
        clearInterval(timer);
        setTimeout(start, 500);
        return;
      }
      // 兜底：页面结构变化导致识别不到信号时，等 8 秒再插入；
      // 此时 Vue 通常已经完成挂载。
      if (Date.now() - startedAt > 8000) {
        clearInterval(timer);
        start();
      }
    }, 100);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init, { once: true });
  } else {
    init();
  }
})();
