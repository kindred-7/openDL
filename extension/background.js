// Native-message replies carry request IDs, allowing concurrent downloads and
// the initial handshake to share one port without consuming each other's reply.
// Classification tables are shared with the content scripts.
// Chrome loads background.js as a classic worker script where importScripts
// defines the shared tables; Firefox loads it as a module where importScripts
// is unavailable and the background entry loads these files first instead.
if (typeof importScripts === "function") importScripts("resources.js", "i18n.js");
const localize = globalThis.openDLI18n.t;
const { downloadable: DOWNLOAD_EXT, playlistMime, directMediaMime } = globalThis.openDLResourceTypes;
const HOST_NAME = "com.example.opendl";
let port = null;
let ready = false;
let supportsScheduledQueue = false;
let supportsIdempotentSubmission = false;
let supportsSiteRules = false;
let handshakeRulesValid = false;
let mediaCapabilities = new Set();
let siteRules = [];
let siteRulesRefresh = null;
let connecting = null;
let nextRequest = 0;
const pending = new Map();

// Explicit browser context-menu requests follow the same prompt path as the
// popup and never silently fall back when the desktop rejects a request.
let contextMenuSetup = null;
function ensureContextMenu() {
  if (contextMenuSetup) return contextMenuSetup;
  contextMenuSetup = new Promise((resolve) => {
    const id = "opendl-download-link";
    const options = { title: chrome.i18n.getMessage("m_context_download") || "使用 OpenDL 下载", contexts: ["link", "video", "audio"] };
    chrome.contextMenus.update(id, options, () => {
      if (!chrome.runtime.lastError) { resolve(); return; }
      chrome.contextMenus.create({ id, ...options }, () => {
        if (chrome.runtime.lastError) console.warn("OpenDL context menu:", chrome.runtime.lastError.message);
        resolve();
      });
    });
  }).finally(() => { contextMenuSetup = null; });
  return contextMenuSetup;
}
chrome.runtime.onInstalled.addListener(() => { void ensureContextMenu(); });
void ensureContextMenu();
chrome.contextMenus.onClicked.addListener((info, tab) => {
  if (info.menuItemId !== "opendl-download-link") return;
  const url = info.linkUrl || info.srcUrl || "";
  if (!/^https?:/i.test(url)) {
    void captureNotice(localize("仅支持 HTTP 和 HTTPS 链接"));
    return;
  }
  chrome.storage.local.get({ "opendl-use-browser-cookies": true }).then((stored) => sendToDesktop({ type: "prompt", url, pageUrl: info.pageUrl || tab?.url || "", title: tab?.title || "", filename: "", mediaMode: "", mediaQuality: "", queue: "" },
    { tabId: tab?.id, url, pageUrl: info.pageUrl || tab?.url || "", useBrowserCookies: stored["opendl-use-browser-cookies"] === true })).then((reply) => {
      if (!reply?.ok) captureNotice(reply?.reason || localize("桌面端或原生主机不可用"));
    }).catch(() => captureNotice(localize("桌面端或原生主机不可用")));
});

// Keep a stable logical request ID across popup/worker restarts. No URL,
// cookies or passwords are stored in this ledger; only the payload digest.
const submissions = new Map();
let submissionChanges = Promise.resolve();
const submissionStorageKey = (tabId) => `opendl-submissions:${tabId}`;
function changeSubmissions(action) {
  const result = submissionChanges.then(action);
  submissionChanges = result.catch(() => {});
  return result;
}
async function submissionHash(payload) {
  const address = new URL(payload.url);
  address.username = "";
  address.password = "";
  address.hash = "";
  const identity = [address.href, payload.filename, payload.mediaMode, payload.mediaQuality || "", payload.queue === "main" ? "" : payload.queue];
  if (payload.type === "prompt") identity.push("prompt");
  if (payload.submissionNonce) identity.push(payload.submissionNonce);
  if (payload.audioLanguage) identity.push(payload.audioLanguage);
  const data = new TextEncoder().encode(JSON.stringify(identity));
  return [...new Uint8Array(await crypto.subtle.digest("SHA-256", data))].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}
async function loadSubmissions(tabId) {
  let entries = submissions.get(tabId);
  if (!entries) {
    const stored = await chrome.storage.session.get(submissionStorageKey(tabId));
    entries = new Map();
    for (const item of Array.isArray(stored[submissionStorageKey(tabId)]) ? stored[submissionStorageKey(tabId)] : []) {
      if (item && /^[a-f0-9]{64}$/.test(item.hash || "") && /^[a-zA-Z0-9_-]{1,128}$/.test(item.id || "") && Number.isFinite(item.created)) entries.set(item.hash, item);
    }
    submissions.set(tabId, entries);
  }
  return entries;
}
async function submissionID(payload, tabId) {
  if (!Number.isInteger(tabId) || tabId < 0) return crypto.randomUUID();
  const hash = await submissionHash(payload);
  return changeSubmissions(async () => {
    const storageKey = submissionStorageKey(tabId);
    const entries = await loadSubmissions(tabId);
    const now = Date.now();
    for (const [key, item] of entries) {
      if (item.attempted === false && now - item.created > 86400000) entries.delete(key);
    }
    let item = entries.get(hash);
    if (!item) {
      if (entries.size >= 256) throw new Error(localize("当前页面的发送记录已满，请关闭或重新加载页面后重试"));
      item = { hash, id: crypto.randomUUID(), created: now, attempted: false, revision: 0 };
      entries.set(hash, item);
    }
    // A failed write stops submission before it can reach the desktop.
    await chrome.storage.session.set({ [storageKey]: [...entries.values()] });
    return item.id;
  });
}
async function beginSubmissionAttempt(tabId, id) {
  if (!id || !Number.isInteger(tabId) || tabId < 0) return null;
  return changeSubmissions(async () => {
    const entries = await loadSubmissions(tabId);
    const item = [...entries.values()].find((entry) => entry.id === id);
    if (!item) throw new Error(localize("发送记录已失效"));
    const previouslyPossible = item.attempted !== false;
    item.revision = (Number.isSafeInteger(item.revision) ? item.revision : 0) + 1;
    item.attempted = true;
    await chrome.storage.session.set({ [submissionStorageKey(tabId)]: [...entries.values()] });
    return { tabId, id, revision: item.revision, previouslyPossible };
  });
}
async function finishSubmissionAttempt(ticket, reply) {
  if (!ticket) return;
  return changeSubmissions(async () => {
    const entries = await loadSubmissions(ticket.tabId);
    const item = [...entries.values()].find((entry) => entry.id === ticket.id);
    if (!item) return;
    if (reply?.ok) {
      item.attempted = true;
      item.accepted = true;
    } else if (reply && !reply.deliveryUnknown && !ticket.previouslyPossible && item.revision === ticket.revision && !item.accepted) {
      // A known rejection only clears this attempt, never an earlier or
      // concurrently sent request whose outcome may still be unknown.
      item.attempted = false;
    }
    await chrome.storage.session.set({ [submissionStorageKey(ticket.tabId)]: [...entries.values()] });
  });
}
async function guardBrowserFallback(reply, payload, tabId) {
  if (!reply?.unavailable || !Number.isInteger(tabId) || tabId < 0) return reply;
  try {
    const hash = await submissionHash(payload);
    const possible = await changeSubmissions(async () => {
      const item = (await loadSubmissions(tabId)).get(hash);
      return item && item.attempted !== false;
    });
    if (possible) return { ok: false, deliveryUnknown: true, reason: localize("此前请求可能已被桌面端接收，请先检查任务列表") };
    return reply;
  } catch {
    return { ok: false, reason: localize("无法读取上次发送状态，请先检查桌面任务列表") };
  }
}
async function sendTrackedNative(activePort, payload, tabId, logicalID = payload.clientRequestId) {
  let ticket;
  try {
    ticket = await beginSubmissionAttempt(tabId, logicalID);
  } catch {
    return { ok: false, reason: localize("无法记录发送状态，请稍后重试") };
  }
  const reply = await sendNative(activePort, payload, true, 20000);
  try { await finishSubmissionAttempt(ticket, reply); } catch {}
  return reply;
}
function clearSubmissions(tabId) {
  return changeSubmissions(async () => {
    submissions.delete(tabId);
    await chrome.storage.session.remove(submissionStorageKey(tabId));
  });
}

// Collect recognized media and attachment URLs and their frame context, never request/response
// headers or bodies. Session storage survives a worker restart but not a browser
// restart. Serialize mutations so a navigation cannot race an earlier write.
const observedPlaylists = new Map();
let playlistChanges = Promise.resolve();
const playlistStorageKey = (tabId) => `opendl-playlists:${tabId}`;
const badgeTimers = new Map();
const badgeRevisions = new Map();
const notifyPopupResources = (tabId) => {
  try { chrome.runtime.sendMessage({ type: "opendlResourcesUpdated", tabId }, () => { void chrome.runtime.lastError; }); } catch {}
};
function scheduleBadge(tabId) {
  badgeRevisions.set(tabId, (badgeRevisions.get(tabId) || 0) + 1);
  if (badgeTimers.has(tabId)) return;
  badgeTimers.set(tabId, setTimeout(async () => {
    badgeTimers.delete(tabId);
    const revision = badgeRevisions.get(tabId);
    try {
      const frames = await chrome.webNavigation.getAllFrames({ tabId });
      const replies = await Promise.allSettled((frames || []).map((frame) => Promise.race([
        chrome.tabs.sendMessage(tabId, { type: "cachedResources" }, { frameId: frame.frameId }),
        new Promise((resolve) => setTimeout(() => resolve(null), 500))
      ])));
      const urls = new Set((await playlistsFor(tabId)).keys());
      for (const reply of replies) if (reply.status === "fulfilled") {
        for (const item of reply.value?.resources || []) if (item?.url) urls.add(item.url);
      }
      if (revision !== badgeRevisions.get(tabId)) return;
      await chrome.action.setBadgeText({ tabId, text: urls.size ? (urls.size > 99 ? "99+" : String(urls.size)) : "" });
      await chrome.action.setBadgeBackgroundColor({ tabId, color: "#3e63dd" });
    } catch {}
  }, 80));
}
function enqueuePlaylistChange(action) {
  const result = playlistChanges.then(action);
  playlistChanges = result.catch(() => {});
  return result;
}
async function playlistsFor(tabId) {
  if (observedPlaylists.has(tabId)) return observedPlaylists.get(tabId);
  let saved = [];
  try {
    const key = playlistStorageKey(tabId);
    const result = await chrome.storage.session.get(key);
    if (Array.isArray(result[key])) saved = result[key];
  } catch {}
  const entries = new Map();
  for (const item of saved.slice(-100)) {
    if (item && typeof item.url === "string" && /^https?:/i.test(item.url)) entries.set(item.url, item);
  }
  observedPlaylists.set(tabId, entries);
  return entries;
}
async function savePlaylists(tabId, entries) {
  try {
    const key = playlistStorageKey(tabId);
    if (entries.size) await chrome.storage.session.set({ [key]: [...entries.values()] });
    else await chrome.storage.session.remove(key);
  } catch {
    // Drop a stale snapshot when a new one cannot be stored; otherwise a
    // worker restart could resurrect entries already removed by navigation.
    try { await chrome.storage.session.remove(playlistStorageKey(tabId)); } catch {}
  }
}
const documentMimes = new Map([
  ["application/pdf", "pdf"], ["text/plain", "txt"],
  ["application/epub+zip", "epub"], ["text/csv", "csv"],
  ["application/json", "json"], ["text/markdown", "md"],
  ["image/png", "png"], ["image/jpeg", "jpg"],
  ["application/xml", "xml"], ["text/xml", "xml"],
  ["application/zip", "zip"], ["application/x-zip-compressed", "zip"],
  ["application/msword", "doc"],
  ["application/vnd.openxmlformats-officedocument.wordprocessingml.document", "docx"],
  ["application/vnd.ms-excel", "xls"],
  ["application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "xlsx"],
  ["application/vnd.ms-powerpoint", "ppt"],
  ["application/vnd.openxmlformats-officedocument.presentationml.presentation", "pptx"]
]);
// Serialize reservations across concurrent range requests and worker restarts.
let documentChanges = Promise.resolve();
function changeDocuments(action) {
  const result = documentChanges.then(action);
  documentChanges = result.catch(() => {});
  return result;
}
const documentKey = (tabId) => `opendl-documents:${tabId}`;
async function documentDigest(url) {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(url));
  return [...new Uint8Array(digest)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}
async function reserveDocument(tabId, url) {
  const hash = await documentDigest(url);
  return changeDocuments(async () => {
    const key = documentKey(tabId);
    const stored = await chrome.storage.session.get(key);
    const now = Date.now();
    const entries = (Array.isArray(stored[key]) ? stored[key] : []).filter((item) => item && item.time > now - 300000);
    if (entries.some((item) => item.hash === hash)) return false;
    entries.push({ hash, time: now });
    await chrome.storage.session.set({ [key]: entries.slice(-100) });
    return true;
  });
}
async function forgetDocument(tabId, url) {
  const hash = await documentDigest(url);
  return changeDocuments(async () => {
    const key = documentKey(tabId);
    const stored = await chrome.storage.session.get(key);
    const entries = Array.isArray(stored[key]) ? stored[key] : [];
    await chrome.storage.session.set({ [key]: entries.filter((item) => item.hash !== hash) });
  });
}
// Use only response metadata and configured formats; no speculative network replay.
function responseFilename(headers) {
  const disposition = headers?.find((h) => h.name.toLowerCase() === "content-disposition")?.value || "";
  const extended = disposition.match(/(?:^|;)\s*filename\*\s*=\s*(?:"([^"\r\n]*)"|([^;\r\n]*))/i);
  let name = "";
  if (extended) {
    const value = (extended[1] || extended[2] || "").trim();
    const utf8 = value.match(/^utf-8'[^']*'(.*)$/i);
    if (utf8) try { name = decodeURIComponent(utf8[1]); } catch {}
  }
  if (!name) {
    const plain = disposition.match(/(?:^|;)\s*filename\s*=\s*(?:"([^"\r\n]*)"|([^;\r\n]*))/i);
    name = (plain?.[1] || plain?.[2] || "").trim();
  }
  return fallbackFilename(name);
}
function filenameExtension(name) {
  return name.match(/\.([a-z0-9]{1,16})$/i)?.[1]?.toLowerCase() || "";
}
function documentCandidate(details, mime, formats, background) {
  const path = new URL(details.url).pathname;
  const filename = responseFilename(details.responseHeaders);
  const suffix = filenameExtension(filename) || filenameExtension(path);
  const segment = path.match(/\/content\/([a-z0-9]{1,16})\/?$/i)?.[1]?.toLowerCase() || "";
  const generic = !mime || mime === "application/octet-stream" || mime === "binary/octet-stream";
  // HTML and scripts are pages even if their address or suggested name ends
  // in a configured extension. Avoid treating JSON APIs as file previews.
  if (/^(?:text\/html|application\/xhtml\+xml|(?:text|application)\/(?:javascript|x-javascript))$/.test(mime)) return null;
  if (background && /^(?:application\/json|(?:application|text)\/xml|image\/(?:png|jpeg))$/.test(mime)) return null;
  const mimeExt = documentMimes.get(mime) || "";
  const namedExt = filenameExtension(filename);
  const ext = mime === "text/plain" && suffix ? suffix : (mimeExt || (generic ? (suffix || segment) : namedExt));
  if (!formats.has(ext)) return null;
  // Generic background responses require an explicit configured filename,
  // URL suffix, or /content/<format> endpoint, not an arbitrary binary API.
  if (background && generic && !suffix && !segment) return null;
  if (!generic && !mimeExt && !namedExt) return null;
  return { ext, filename: filename || (background ? `document.${ext}` : "") };
}
async function captureNotice(message) {
  try { await chrome.notifications.create({ type: "basic", iconUrl: "icon.png", title: "OpenDL", message }); } catch {}
}
async function promptInlineDocument(details, mime, background = false) {
  try {
    const stored = await chrome.storage.local.get({ "opendl-takeover": true, "opendl-inline-formats": globalThis.openDLResourceTypes.defaultInlineFormats });
    if (stored["opendl-takeover"] === false) return;
    const formats = new Set(String(stored["opendl-inline-formats"]).toLowerCase().split(/[\s,;]+/).map((x) => x.replace(/^\./, "")).filter((x) => /^[a-z0-9]{1,16}$/.test(x)));
    const candidate = documentCandidate(details, mime, formats, background);
    if (!candidate) return;
    if (background) {
      // Background requests can be ordinary page data. Offer them in the
      // resource list without starting a desktop prompt or reading cookies.
      const pageUrl = (details.documentUrl || details.initiator || "").slice(0, 4096);
      await enqueuePlaylistChange(async () => {
        const entries = await playlistsFor(details.tabId);
        if (entries.has(details.url)) return;
        entries.set(details.url, { url: details.url, ext: candidate.ext, kind: "network:document", pageUrl, frameId: details.frameId, downloadAttr: candidate.filename });
        if (entries.size > 100) entries.delete(entries.keys().next().value);
        scheduleBadge(details.tabId);
        notifyPopupResources(details.tabId);
        await savePlaylists(details.tabId, entries);
      });
      return;
    }
    const pageUrl = (details.documentUrl || details.initiator || "").slice(0, 4096);
    let cookie;
    try { cookie = await cookiesForTab(details.tabId, details.url); }
    catch {
      await captureNotice(localize("无法获取浏览器登录状态，请从扩展资源列表重试"));
      return;
    }
    const reply = await sendToDesktop({ type: "prompt", url: details.url, pageUrl, title: "", filename: candidate.filename, mediaMode: "", mediaQuality: "", queue: "", cookie },
      { tabId: details.tabId, url: details.url, pageUrl, queue: "", useBrowserCookies: false });
    if (!reply?.ok) await captureNotice(reply?.deliveryUnknown ? localize("无法确认桌面端是否收到任务，请先检查桌面任务列表") : ((reply && reply.reason) || localize("桌面端或原生主机不可用")));
  } catch {}
}

chrome.webRequest.onHeadersReceived.addListener((details) => {
  if (details.tabId < 0 || details.url.length > 4096 || !/^https?:/i.test(details.url)) return;
  const header = details.responseHeaders?.find((h) => h.name.toLowerCase() === "content-type");
  const mime = (header?.value || "").split(";", 1)[0].trim().toLowerCase();
  // Inline top-level documents do not create a browser download. Offer the
  // existing desktop prompt while leaving the browser viewer operational.
  if (details.type === "main_frame" && details.method === "GET" && details.statusCode === 200) {
    const disposition = details.responseHeaders?.find((h) => h.name.toLowerCase() === "content-disposition")?.value || "";
    if (!/^\s*attachment(?:\s*;|\s*$)/i.test(disposition)) void promptInlineDocument(details, mime);
  }
  if ((details.type === "xmlhttprequest" || details.type === "sub_frame" || details.type === "object" || details.type === "other") &&
      details.method === "GET" && (details.statusCode === 200 || details.statusCode === 206)) {
    const disposition = details.responseHeaders?.find((h) => h.name.toLowerCase() === "content-disposition")?.value || "";
    if (!/^\s*attachment(?:\s*;|\s*$)/i.test(disposition)) void promptInlineDocument(details, mime, true);
  }
  // The desktop fetches resources with GET. A POST/HEAD response cannot be
  // assumed to identify a replayable download at the same address.
  if (details.method !== "GET") return;
  const playlistExt = playlistMime.get(mime);
  const disposition = details.responseHeaders?.find((h) => h.name.toLowerCase() === "content-disposition")?.value || "";
  // Explicit attachments can use dynamic URLs with no useful suffix, such as
  // NAS download endpoints. Do not retain the response header or its filename.
  const attachment = /^\s*attachment(?:\s*;|\s*$)/i.test(disposition);
  // Media element requests are useful even without a suffix. Do not classify
  // arbitrary fetch/XHR video fragments as complete downloadable files.
  const ext = playlistExt || (details.type === "media" ? directMediaMime.get(mime) : "");
  if ((!ext && !attachment) || details.statusCode < 200 || details.statusCode >= 300) return;
  const pageUrl = (details.documentUrl || "").length <= 4096 && /^https?:/i.test(details.documentUrl || "") ? details.documentUrl : "";
  void enqueuePlaylistChange(async () => {
    const entries = await playlistsFor(details.tabId);
    entries.delete(details.url);
    entries.set(details.url, { url: details.url, ext: ext || "", kind: playlistExt ? "network:playlist" : ext ? "network:media" : "network:attachment", pageUrl, frameId: details.frameId });
    if (entries.size > 100) entries.delete(entries.keys().next().value);
    scheduleBadge(details.tabId);
    notifyPopupResources(details.tabId);
    await savePlaylists(details.tabId, entries);
  });
}, { urls: ["http://*/*", "https://*/*"] }, ["responseHeaders"]);

chrome.webNavigation.onCommitted.addListener((details) => {
  if (details.frameId === 0) {
    badgeRevisions.set(details.tabId, (badgeRevisions.get(details.tabId) || 0) + 1);
    clearTimeout(badgeTimers.get(details.tabId));
    badgeTimers.delete(details.tabId);
    void chrome.action.setBadgeText({ tabId: details.tabId, text: "" }).catch(() => {});
  }
  if (details.frameId === 0) {
    void clearSubmissions(details.tabId).catch(() => {});
    void changeDocuments(() => chrome.storage.session.remove(documentKey(details.tabId))).catch(() => {});
  }
  void enqueuePlaylistChange(async () => {
    const entries = await playlistsFor(details.tabId);
    if (details.frameId === 0) {
      entries.clear();
    } else {
      for (const [url, item] of entries) {
        if (item.frameId === details.frameId) entries.delete(url);
      }
    }
    await savePlaylists(details.tabId, entries);
    scheduleBadge(details.tabId);
  });
});
chrome.tabs.onRemoved.addListener((tabId) => {
  badgeRevisions.delete(tabId);
  clearTimeout(badgeTimers.get(tabId));
  badgeTimers.delete(tabId);
  void clearSubmissions(tabId).catch(() => {});
  void changeDocuments(() => chrome.storage.session.remove(documentKey(tabId))).catch(() => {});
  void enqueuePlaylistChange(async () => {
    observedPlaylists.delete(tabId);
    try { await chrome.storage.session.remove(playlistStorageKey(tabId)); } catch {}
  });
});

function finishPending(id, result) {
  const item = pending.get(id);
  if (!item) return;
  pending.delete(id);
  clearTimeout(item.timer);
  item.resolve(result);
}

function sendNative(activePort, payload, mayHaveBeenAccepted, timeout) {
  return new Promise((resolve) => {
    const id = String(++nextRequest);
    const timer = setTimeout(() => {
      finishPending(id, { ok: false, reason: localize("桌面端响应超时"), deliveryUnknown: mayHaveBeenAccepted });
    }, timeout);
    pending.set(id, { resolve, timer, port: activePort, mayHaveBeenAccepted });
    try {
      activePort.postMessage({ ...payload, requestId: id });
    } catch (e) {
      finishPending(id, { ok: false, reason: String(e), deliveryUnknown: false, unavailable: true });
    }
  });
}

function connect() {
  if (ready && port) return Promise.resolve(true);
  if (connecting) return connecting;
  connecting = (async () => {
    let activePort;
    try {
      activePort = chrome.runtime.connectNative(HOST_NAME);
    } catch {
      return false;
    }
    port = activePort;
    activePort.onMessage.addListener((msg) => {
      if (msg && !msg.ok) {
        const messages = {
          update_in_progress: "应用正在准备更新，请稍后再操作",
          journal_write_failed: "无法保存下载任务，请检查应用日志和存储空间",
          desktop_unauthorized: "桌面端认证失败，请重新启动扩展和桌面应用",
          desktop_unsupported: "桌面端版本不支持该下载请求，请更新桌面应用"
        };
        if (Object.prototype.hasOwnProperty.call(messages, msg.code)) msg.reason = localize(messages[msg.code]);
      }
      if (msg && msg.requestId) finishPending(msg.requestId, msg);
    });
    activePort.onDisconnect.addListener(() => {
      // Read lastError in the callback to avoid Chrome's unchecked-error warning.
      const reason = chrome.runtime.lastError?.message || localize("原生主机连接已断开");
      if (port === activePort) { port = null; ready = false; supportsScheduledQueue = false; supportsIdempotentSubmission = false; supportsSiteRules = false; mediaCapabilities = new Set(); }
      for (const [id, item] of pending) {
        if (item.port === activePort) {
          finishPending(id, { ok: false, reason, deliveryUnknown: item.mayHaveBeenAccepted });
        }
      }
    });
    const reply = await sendNative(activePort, { type: "ping" }, false, 5000);
    if (!reply.ok || port !== activePort) {
      if (port === activePort) { port = null; ready = false; supportsScheduledQueue = false; supportsIdempotentSubmission = false; supportsSiteRules = false; mediaCapabilities = new Set(); }
      try { activePort.disconnect(); } catch {}
      return false;
    }
    supportsScheduledQueue = Array.isArray(reply.capabilities) && reply.capabilities.includes("scheduled-queue");
    supportsIdempotentSubmission = Array.isArray(reply.capabilities) && reply.capabilities.includes("idempotent-submission");
    supportsSiteRules = Array.isArray(reply.capabilities) && reply.capabilities.includes("site-rules");
    mediaCapabilities = new Set(Array.isArray(reply.capabilities) ? reply.capabilities : []);
    handshakeRulesValid = supportsSiteRules && Array.isArray(reply.siteRules);
    if (handshakeRulesValid) siteRules = normalizeSiteRules(reply.siteRules);
    ready = true;
    return true;
  })().finally(() => { connecting = null; });
  return connecting;
}

connect();
chrome.alarms.create("opendl-keepalive", { periodInMinutes: 0.5 });
chrome.alarms.onAlarm.addListener((a) => {
  if (a.name === "opendl-keepalive" && !ready) connect();
});

async function browserCookies(msg) {
  if (!(await chrome.permissions.contains({ permissions: ["cookies"] }))) {
    throw new Error(localize("未获得浏览器 Cookie 权限"));
  }
  if (!Number.isInteger(msg.tabId)) throw new Error(localize("无法识别来源标签页"));
  const tab = await chrome.tabs.get(msg.tabId);
  const target = new URL(msg.url);
  const source = new URL(msg.pageUrl);
  const top = new URL(tab.url);
  // Cross-origin resources can still be sent, but must not receive cookies
  // from a different page or frame.
  if (!/^https?:$/.test(target.protocol) || target.origin !== source.origin || target.origin !== top.origin) return "";
  return cookiesForTab(msg.tabId, target.href);
}

async function cookiesForTab(tabId, url, cookieStoreId = "") {
  const target = new URL(url);
  if (!/^https?:$/.test(target.protocol)) throw new Error(localize("无法获取浏览器登录状态"));
  const stores = await chrome.cookies.getAllCookieStores();
  const store = cookieStoreId
    ? stores.find((item) => item.id === cookieStoreId && (!Number.isInteger(tabId) || tabId < 0 || item.tabIds.includes(tabId)))
    : stores.find((item) => item.tabIds.includes(tabId));
  if (!store) throw new Error(localize("无法识别当前标签页的 Cookie 存储区"));
  const cookies = await chrome.cookies.getAll({ url: target.href, storeId: store.id });
  const pairs = [];
  for (const cookie of cookies) {
    if (cookie.partitionKey) continue;
    if (!/^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/.test(cookie.name) || /[^\x20-\x7e]|[;]/.test(cookie.value)) {
      throw new Error(localize("浏览器 Cookie 包含无法安全传递的字符"));
    }
    pairs.push(cookie.name + "=" + cookie.value);
  }
  const header = pairs.join("; ");
  if (header.length > 8192) throw new Error(localize("浏览器 Cookie 过长，无法传递给桌面端"));
  return header;
}

async function sendToDesktop(payload, msg) {
  const unavailable = (reason) => guardBrowserFallback({ ok: false, unavailable: true, reason }, payload, msg.tabId);
  if (!(await connect()) || !port) return unavailable(localize("桌面端或原生主机不可用"));
  const activePort = port;
  if (payload.type === "prompt" && !mediaCapabilities.has("download-prompt")) {
    return { ok: false, reason: localize("桌面端版本不支持该下载请求，请更新桌面应用") };
  }
  if (msg.queue === "night" && !supportsScheduledQueue) {
    return { ok: false, reason: localize("原生主机版本不支持夜间队列，请更新原生主机") };
  }
  const mediaCapability = payload.mediaMode === "hls-ts" ? "media-hls" : payload.mediaMode === "dash-fmp4" ? "media-dash" : "";
  if ((payload.mediaMode && (!mediaCapability || !mediaCapabilities.has(mediaCapability))) ||
      (payload.mediaQuality && !mediaCapabilities.has("media-quality")) ||
      (payload.audioLanguage && !mediaCapabilities.has("media-audio-language"))) {
    return { ok: false, reason: localize("原生主机版本不支持媒体下载选项，请更新原生主机") };
  }
  if (msg.useBrowserCookies) {
    try {
      payload.cookie = await browserCookies(msg);
    } catch (e) {
      return { ok: false, reason: e.message || localize("无法获取浏览器登录状态") };
    }
  }
  if (!ready || port !== activePort) return unavailable(localize("原生主机连接已断开"));
  if (supportsIdempotentSubmission) {
    try {
      payload.clientRequestId = await submissionID(payload, msg.tabId);
    } catch {
      return { ok: false, reason: localize("无法保存发送记录，请检查扩展存储空间后重试") };
    }
  }
  if (!ready || port !== activePort) return unavailable(localize("原生主机连接已断开"));
  const reply = await sendTrackedNative(activePort, payload, msg.tabId);
  // This explicit rejection is returned before the host posts a task. Older
  // desktops can therefore keep the established submission behavior safely.
  if (reply?.unsupportedIdempotency && payload.clientRequestId) {
    supportsIdempotentSubmission = false;
    const logicalID = payload.clientRequestId;
    delete payload.clientRequestId;
    if (!ready || port !== activePort) return unavailable(localize("原生主机连接已断开"));
    const legacyReply = await sendTrackedNative(activePort, payload, msg.tabId, logicalID);
    return guardBrowserFallback(legacyReply, payload, msg.tabId);
  }
  return guardBrowserFallback(reply, payload, msg.tabId);
}

// Browser downloads accept relative paths; restrict page-provided suggestions
// to one portable filename, otherwise let the browser choose its own name.
function fallbackFilename(value) {
  if (typeof value !== "string" || !value || value !== value.trim()) return "";
  if (/[<>:"/\\|?*\x00-\x1f\x7f]/.test(value) || /[. ]$/.test(value)) return "";
  if (/^(con|prn|aux|nul|com[1-9¹²³]|lpt[1-9¹²³]) *(?:\.|$)/i.test(value)) return "";
  if (value.length > 240 || new TextEncoder().encode(value).length > 240) return "";
  return value;
}

// Site rules classify download paths without a file extension. They arrive
// from the desktop's rules file and stay data: bounds and shapes are enforced
// here before any pattern reaches a content script.
function normalizeSiteRules(value) {
  if (!Array.isArray(value)) return [];
  const out = [];
  for (const rule of value.slice(0, 256)) {
    if (!rule || typeof rule !== "object") continue;
    const path = typeof rule.path === "string" ? rule.path.slice(0, 512) : "";
    const host = typeof rule.host === "string" ? rule.host.slice(0, 512) : "";
    if (!path) continue;
    const exts = [];
    if (Array.isArray(rule.exts)) {
      for (const ext of rule.exts.slice(0, 32)) {
        if (typeof ext === "string" && ext.startsWith(".") && ext.length <= 16) exts.push(ext.toLowerCase());
      }
    }
    out.push({ tag: typeof rule.tag === "string" ? rule.tag.slice(0, 128) : "", host, path, exts });
  }
  return out;
}

// Browser click takeover: hand whitelisted browser downloads to the desktop
// app instead. A Shift-click in a page releases one URL to the browser, and
// the popup can switch takeover off entirely.
const releasedURLs = new Map();
const RELEASE_TTL = 20000;

async function takeoverEnabled() {
  try {
    const stored = await chrome.storage.local.get({ "opendl-takeover": true });
    return stored["opendl-takeover"] !== false;
  } catch {
    return true;
  }
}

// A running native host can answer ping even when the desktop app is closed.
// Probe the desktop before interrupting an ordinary browser download.
async function desktopAvailableForTakeover() {
  try {
    if (!(await connect()) || !port || !mediaCapabilities.has("download-prompt")) return false;
    const activePort = port;
    const reply = await sendNative(activePort, { type: "desktopStatus" }, false, 5000);
    return Boolean(reply?.ok && port === activePort);
  } catch {
    return false;
  }
}

async function startBrowserFallback(url, filename = "") {
  const options = { url, saveAs: false, conflictAction: "uniquify" };
  const safeName = fallbackFilename(filename);
  if (safeName) options.filename = safeName;
  const id = await chrome.downloads.download(options);
  return { ok: true, fallback: true, id };
}

function takeoverExt(url) {
  try {
    const path = new URL(url).pathname;
    const dot = path.lastIndexOf(".");
    const slash = path.lastIndexOf("/");
    const ext = dot > slash ? path.slice(dot + 1).toLowerCase() : "";
    return DOWNLOAD_EXT.has(ext) ? ext : "";
  } catch {
    return "";
  }
}

chrome.downloads.onCreated.addListener((item) => {
  void (async () => {
    try {
      // Never intercept this extension's own browser fallback downloads.
      if (!item || item.byExtensionId === chrome.runtime.id) return;
      const url = String(item.url || "");
      if (url.length > 4096 || !/^https?:/i.test(url)) return;
      const now = Date.now();
      for (const [released, expiry] of releasedURLs) {
        if (expiry <= now) releasedURLs.delete(released);
      }
      const release = releasedURLs.get(url);
      if (release && release > now) {
        releasedURLs.delete(url);
        return;
      }
      if (!(await takeoverEnabled())) return;
      // Pause immediately while checking the native host, preventing fast
      // transfers from completing during the asynchronous handshake.
      try { await chrome.downloads.pause(item.id); } catch { return; }
      if (!(await desktopAvailableForTakeover())) {
        try { await chrome.downloads.resume(item.id); } catch {}
        return;
      }
      let fresh;
      try { [fresh] = await chrome.downloads.search({ id: item.id }); } catch {}
      if (!fresh || fresh.state !== "in_progress") {
        try { await chrome.downloads.resume(item.id); } catch {}
        return;
      }
      const filename = fallbackFilename(String(fresh.filename || item.filename || "").split(/[\\/]/).pop() || "");
      let title = "", cookie = "";
      try { title = (await chrome.tabs.get(item.tabId))?.title || ""; } catch {}
      try { cookie = await cookiesForTab(item.tabId, url, fresh.cookieStoreId || item.cookieStoreId || ""); } catch {}
      // GitHub Actions artifacts require a signed-in browser session. If its
      // cookie store cannot be identified, keep the original browser download
      // rather than sending an unauthenticated request that returns 404.
      const source = new URL(url);
      if (!cookie && source.hostname.toLowerCase() === "github.com" &&
          /^\/[^/]+\/[^/]+\/actions\/runs\/\d+\/artifacts\/\d+\/?$/.test(source.pathname)) {
        try { await chrome.downloads.resume(item.id); } catch {}
        return;
      }
      try { await chrome.downloads.cancel(item.id); } catch {
        try { await chrome.downloads.resume(item.id); } catch {}
        return;
      }
      const reply = await sendToDesktop(
        {
          type: "prompt",
          url,
          pageUrl: String(item.referrer || "").slice(0, 4096),
          title,
          filename,
          cookie,
          mediaMode: "",
          mediaQuality: "",
          queue: ""
        },
        { tabId: item.tabId, queue: "", useBrowserCookies: false, url, pageUrl: item.referrer || "" }
      );
      if (reply?.ok) {
        // Keep the browser record until the desktop acknowledges the handoff.
        // Cleanup failure must not retry a task already accepted by OpenDL.
        try { await chrome.downloads.erase({ id: item.id }); } catch {}
        return;
      }
      // Keep the cancelled browser record if the handoff outcome is unknown;
      // starting a fallback here could duplicate an accepted desktop task.
      if (reply?.deliveryUnknown) return;
      // Hand the download back to the browser rather than losing it.
      try {
        await startBrowserFallback(url);
      } catch {}
    } catch {}
  })();
});

chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  if (msg?.type === "takeoverLink" && Number.isInteger(sender?.tab?.id) &&
      typeof msg.url === "string" && msg.url.length <= 4096 && /^https?:/i.test(msg.url)) {
    const tabId = sender.tab.id;
    (async () => {
      const payload = { type: "prompt", url: msg.url,
        pageUrl: String(msg.pageUrl || "").slice(0, 4096), title: String(msg.title || "").slice(0, 1024),
        filename: fallbackFilename(msg.filename), mediaMode: "", mediaQuality: "", queue: "" };
      if (!(await desktopAvailableForTakeover())) {
        const guarded = await guardBrowserFallback({ ok: false, unavailable: true }, payload, tabId);
        if (guarded?.deliveryUnknown || !guarded?.unavailable) return guarded;
        try { return await startBrowserFallback(msg.url, payload.filename); }
        catch (e) { return { ok: false, reason: String(e) }; }
      }
      let cookie = "";
      try { cookie = await cookiesForTab(tabId, msg.url); } catch {}
      payload.cookie = cookie;
      const reply = await sendToDesktop(payload,
        { tabId, url: msg.url, pageUrl: msg.pageUrl || "", queue: "", useBrowserCookies: false });
      if (reply?.unavailable && !reply.deliveryUnknown) {
        try { return await startBrowserFallback(msg.url, payload.filename); }
        catch (e) { return { ok: false, reason: String(e) }; }
      }
      return reply;
    })().then(sendResponse, () => sendResponse({ ok: false, deliveryUnknown: true, reason: localize("无法确认任务是否已发送，请检查下载列表") }));
    return true;
  }
  if (msg?.type === "resourcesChanged" && Number.isInteger(sender?.tab?.id)) {
    scheduleBadge(sender.tab.id);
    sendResponse({ ok: true });
    return false;
  }
  if (msg && msg.type === "releaseDownload" && typeof msg.url === "string" && msg.url.length <= 4096 && /^https?:/i.test(msg.url)) {
    // The marker only needs to outlive the click that set it; the download is
    // created milliseconds later and reads it back.
    const now = Date.now();
    for (const [url, expiry] of releasedURLs) {
      if (expiry <= now) releasedURLs.delete(url);
    }
    releasedURLs.set(msg.url, now + RELEASE_TTL);
    sendResponse({ ok: true });
    return false;
  }
  if (msg && msg.type === "siteRules") {
    // Content scripts cache the table; a forced request refreshes it from the
    // desktop so editing the rules file takes effect without restarts.
    const reply = (fresh = false) => sendResponse({ rules: siteRules, fresh });
    if (!msg.refresh) {
      reply();
      return false;
    }
    (async () => {
      const wasReady = ready && Boolean(port);
      if (!(await connect()) || !supportsSiteRules || !port) return false;
      // The handshake itself reads the rules when establishing a connection.
      if (!wasReady) return handshakeRulesValid;
      const activePort = port;
      if (!siteRulesRefresh || siteRulesRefresh.port !== activePort) {
        const request = { port: activePort, promise: null };
        request.promise = sendNative(activePort, { type: "siteRules" }, false, 5000).then((result) => {
          const fresh = Boolean(port === activePort && result && result.ok && Array.isArray(result.rules));
          if (fresh) siteRules = normalizeSiteRules(result.rules);
          return fresh;
        }, () => false).finally(() => {
          if (siteRulesRefresh === request) siteRulesRefresh = null;
        });
        siteRulesRefresh = request;
      }
      return siteRulesRefresh.promise;
    })().then(reply, () => reply());
    return true;
  }
  if (msg && msg.type === "observedPlaylists" && Number.isInteger(msg.tabId)) {
    enqueuePlaylistChange(async () => [...(await playlistsFor(msg.tabId)).values()])
      .then((resources) => sendResponse({ resources }), () => sendResponse({ resources: [] }));
    return true;
  }
  if (msg && msg.type === "hoverMediaLookup" && sender?.tab && typeof msg.url === "string" && msg.url.length <= 4096 && /^https?:/i.test(msg.url)) {
    // Only answer for the requesting frame; do not expose other frame URLs.
    enqueuePlaylistChange(async () => {
      const item = (await playlistsFor(sender.tab.id)).get(msg.url);
      return Boolean(item && item.frameId === sender.frameId && (item.kind === "network:media" || item.kind === "network:playlist"));
    }).then((media) => sendResponse({ media }), () => sendResponse({ media: false }));
    return true;
  }
  if (msg && (msg.type === "download" || msg.type === "prompt")) {
    // Page-side submissions carry no tabId of their own.
    if (!Number.isInteger(msg.tabId) && sender && sender.tab) msg.tabId = sender.tab.id;
    sendToDesktop({
      type: msg.type,
      url: msg.url,
      pageUrl: msg.pageUrl || (sender && sender.tab && sender.tab.url) || "",
      title: msg.title || (sender && sender.tab && sender.tab.title) || "",
      filename: msg.filename || "",
      mediaMode: msg.mediaMode || "",
      mediaQuality: msg.mediaQuality || "",
      audioLanguage: msg.audioLanguage || "",
      queue: msg.queue || "",
      submissionNonce: sender?.url?.split("#")[0].split("?")[0] === chrome.runtime.getURL("popup.html") && /^[a-f0-9-]{36}$/i.test(msg.submissionNonce || "") ? msg.submissionNonce : ""
    }, msg).then(async (reply) => {
      if (reply && reply.ok) {
        sendResponse({ ok: true, id: reply.id });
        return;
      }
      // A sent request with no reply may already have been accepted; silently
      // falling back would create a second download.
      if (reply && reply.deliveryUnknown) {
        sendResponse({ ok: false, deliveryUnknown: true, reason: localize("无法确认桌面端是否收到任务，请先检查桌面任务列表") });
        return;
      }
      if (!reply || !reply.unavailable) {
        sendResponse({ ok: false, reason: (reply && reply.reason) || localize("桌面端拒绝了下载请求") });
        return;
      }
      if (msg.type === "prompt") {
        sendResponse({ ok: false, reason: (reply && reply.reason) || localize("桌面端或原生主机不可用") });
        return;
      }
      if (msg.queue === "night") {
        sendResponse({ ok: false, reason: localize("桌面端不可用，无法加入夜间队列") });
        return;
      }
      if (msg.mediaMode) {
        sendResponse({ ok: false, reason: localize("桌面端不可用，无法合并媒体分片") });
        return;
      }
      try {
        const options = { url: msg.url, saveAs: false, conflictAction: "uniquify" };
        const filename = fallbackFilename(msg.filename);
        if (filename) options.filename = filename;
        const id = await chrome.downloads.download(options);
        sendResponse({ ok: true, fallback: true, id });
      } catch (e) {
        sendResponse({ ok: false, reason: String(e) });
      }
    }).catch((e) => sendResponse({ ok: false, deliveryUnknown: true, reason: localize("无法确认任务是否已发送，请检查下载列表：") + String(e) }));
    return true;
  }
  if (msg && msg.type === "hostStatus") {
    (async () => {
      if (!(await connect()) || !port) return { connected: false };
      const reply = await sendNative(port, { type: "desktopStatus" }, false, 5000);
      return { connected: Boolean(reply.ok), theme: reply.ok ? reply.theme : null };
    })().then(sendResponse, () => sendResponse({ connected: false }));
    return true;
  }
  return false;
});
