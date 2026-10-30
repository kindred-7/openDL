const localize = globalThis.openDLI18n.t;
// popup: lists what content.js sniffed for the active tab and sends each
// item to the desktop app through the service worker.

const MEDIA_KINDS = new Set(["video", "audio", "source", "network:media", "network:video", "network:playlist"]);
const MEDIA_EXT = globalThis.openDLResourceTypes.media;

const statusEl = document.getElementById("status");
const connectionEl = document.getElementById("connection");
const countEl = document.getElementById("count-value");
let sentTotal = 0;
// footer 只保留左侧一排时计数元素可能不存在，计数仅作内部统计。
const markSent = () => { sentTotal++; if (countEl) countEl.textContent = String(sentTotal); };
const connectionLabel = connectionEl.querySelector(".conn-text");
const setConnectionText = (text) => { connectionLabel.textContent = text; };
const themeStorageKey = "opendl-desktop-theme";
const darkScheme = matchMedia("(prefers-color-scheme: dark)");
let desktopTheme = null;
let receivedDesktopTheme = false;
function validTheme(theme) {
  return [theme?.light, theme?.dark].every((mode) => mode &&
    [mode.primary, mode.onPrimary, mode.selection, mode.focus].every((color) => typeof color === "string" && /^#[0-9a-f]{6}$/i.test(color)));
}
function applyDesktopTheme(theme) {
  if (!validTheme(theme)) return;
  desktopTheme = theme;
  const mode = theme.active === "dark" || theme.active === "light" ? theme.active : (darkScheme.matches ? "dark" : "light");
  const colors = theme[mode];
  document.documentElement.dataset.theme = mode;
  const style = document.documentElement.style;
  style.setProperty("--pri", colors.primary);
  style.setProperty("--onpri", colors.onPrimary);
  style.setProperty("--sel", colors.selection);
  style.setProperty("--focus", colors.focus);
}
chrome.storage.local.get(themeStorageKey).then((stored) => {
  if (!receivedDesktopTheme) applyDesktopTheme(stored[themeStorageKey]);
}).catch(() => {});
darkScheme.addEventListener("change", () => applyDesktopTheme(desktopTheme));
// 连接状态图标：页脚中间，不可选中；桌面端可用时资源才能发送。
let refreshingConnection = false;
async function refreshConnection() {
  if (refreshingConnection) return;
  refreshingConnection = true;
  const english = !chrome.i18n.getUILanguage().toLowerCase().startsWith("zh");
  try {
    const result = await chrome.runtime.sendMessage({ type: "hostStatus" });
    const connected = Boolean(result?.connected);
    if (connected && validTheme(result.theme)) {
      const changed = JSON.stringify(desktopTheme) !== JSON.stringify(result.theme);
      receivedDesktopTheme = true;
      applyDesktopTheme(result.theme);
      if (changed) chrome.storage.local.set({ [themeStorageKey]: result.theme }).catch(() => {});
    }
    const themeMissing = connected && !validTheme(result.theme);
    connectionEl.title = themeMissing
      ? (english ? "Theme sync failed" : "主题同步失败") : "";
    setConnectionText(connected ? (english ? "Connected" : localize("已连接")) : (english ? "Disconnected" : localize("未连接")));
    connectionEl.dataset.state = connected ? "connected" : "disconnected";
  } catch {
    setConnectionText(english ? "Disconnected" : localize("未连接"));
    connectionEl.dataset.state = "disconnected";
  } finally {
    refreshingConnection = false;
  }
}
refreshConnection();
setInterval(refreshConnection, 2000);
const listEl = document.getElementById("list");
const emptyEl = document.getElementById("empty");
const takeoverOption = document.getElementById("takeover-input");
const loginOption = document.getElementById("browser-login");
const settingsStatus = document.getElementById("settings-status");
const settingsRetry = document.getElementById("settings-retry");
const loginWarning = document.getElementById("login-warning");
const loginKey = "opendl-use-browser-cookies";
const hoverOption = document.getElementById("hover-download");
chrome.storage.local.get({ "opendl-hover-download": true }).then((stored) => { hoverOption.checked = stored["opendl-hover-download"] !== false; }).catch(() => { hoverOption.checked = true; });
hoverOption.addEventListener("change", async () => {
  try { await chrome.storage.local.set({ "opendl-hover-download": hoverOption.checked }); }
  catch { hoverOption.checked = !hoverOption.checked; settingsStatus.textContent = localize("无法保存悬浮按钮设置"); }
});
let useBrowserCookies = false;
let loginSettingReady = false;
let cookiePermissionVersion = 0;
const selected = new Set();
const sentURLs = new Set();
const uncertainURLs = new Set();
const sendingURLs = new Set();
const videoKey = (url, mode) => mode + "\n" + url;
let currentResources = [];
let displayedTabId = null;
let pageStale = false;
let batchBusy = false, singleBusy = 0, scanBusy = false, renderVersion = 0;
let refreshPending = false, refreshTimer = null;

function scheduleResourceRefresh() {
  refreshPending = true;
  if (refreshTimer !== null) return;
  refreshTimer = setTimeout(() => {
    refreshTimer = null;
    if (!refreshPending || scanBusy || batchBusy || singleBusy > 0 || listEl.contains(document.activeElement)) return;
    refreshPending = false;
    void render();
  }, 150);
}

function updateControls() {
  const blocked = scanBusy || batchBusy || singleBusy > 0 || !loginSettingReady;
  takeoverOption.disabled = batchBusy || singleBusy > 0;
  document.getElementById("rescan-footer").disabled = scanBusy || batchBusy || singleBusy > 0;
  document.getElementById("dl-all").disabled = blocked;
  const chosen = document.getElementById("dl-selected");
  chosen.disabled = blocked || pageStale || selected.size === 0;
  chosen.textContent = selected.size ? localize("下载所选 (%s)", selected.size) : localize("下载所选");
  for (const input of listEl.querySelectorAll("input")) input.disabled = blocked || pageStale || sentURLs.has(input.dataset.url) || uncertainURLs.has(input.dataset.url);
  for (const button of listEl.querySelectorAll("button")) {
    if (button.dataset.copy === "true") continue;
    button.disabled = pageStale || scanBusy || !loginSettingReady || batchBusy || sendingURLs.has(button.parentElement.dataset.url) || uncertainURLs.has(button.dataset.key) || button.dataset.sending === "true";
  }
  if (refreshPending && !scanBusy && !batchBusy && singleBusy === 0) scheduleResourceRefresh();
}

// The takeover setting drives the background's download interception and
// applies to downloads created after a change.
chrome.storage.local.get({ "opendl-takeover": true }).then(
  (stored) => {
    takeoverOption.checked = stored["opendl-takeover"] !== false;
  },
  () => {
    takeoverOption.checked = true;
  }
);
takeoverOption.addEventListener("change", async () => {
  try {
    await chrome.storage.local.set({ "opendl-takeover": takeoverOption.checked });
  } catch {
    statusEl.textContent = localize("无法保存接管设置");
    takeoverOption.checked = !takeoverOption.checked;
  }
});

const inlineFormats = document.getElementById("inline-formats");
const inlineStatus = document.getElementById("inline-formats-status");
const inlineRetry = document.getElementById("inline-formats-retry");
const inlineSaveRetry = document.getElementById("inline-formats-save-retry");
async function loadInlineFormats() {
  inlineRetry.disabled = true;
  try {
    const stored = await chrome.storage.local.get({ "opendl-inline-formats": globalThis.openDLResourceTypes.defaultInlineFormats });
    inlineFormats.value = stored["opendl-inline-formats"];
    inlineFormats.disabled = false;
    inlineStatus.textContent = "";
    inlineRetry.classList.add("hidden");
  } catch {
    inlineStatus.textContent = localize("无法读取在线文档格式");
    inlineRetry.classList.remove("hidden");
  } finally {
    inlineRetry.disabled = false;
  }
}
inlineRetry.addEventListener("click", loadInlineFormats);
loadInlineFormats();
async function saveInlineFormats() {
  const formats = [...new Set(inlineFormats.value.toLowerCase().split(/[\s,;]+/).map((x) => x.replace(/^\./, "")).filter(Boolean))];
  if (formats.some((x) => !/^[a-z0-9]{1,16}$/.test(x))) {
    inlineSaveRetry.classList.add("hidden");
    inlineFormats.setAttribute("aria-invalid", "true");
    inlineStatus.textContent = localize("请输入有效的文件扩展名");
    return;
  }
  inlineFormats.disabled = true;
  inlineSaveRetry.disabled = true;
  try {
    const value = formats.join(", ");
    await chrome.storage.local.set({ "opendl-inline-formats": value });
    inlineFormats.value = value;
    inlineFormats.removeAttribute("aria-invalid");
    inlineStatus.textContent = "";
    inlineSaveRetry.classList.add("hidden");
  } catch {
    inlineStatus.textContent = localize("无法保存在线文档格式");
    inlineSaveRetry.classList.remove("hidden");
  } finally {
    inlineFormats.disabled = false;
    inlineSaveRetry.disabled = false;
  }
}
inlineFormats.addEventListener("change", saveInlineFormats);
inlineSaveRetry.addEventListener("click", saveInlineFormats);

async function loadLoginSetting() {
  settingsRetry.disabled = true;
  const permissionVersion = cookiePermissionVersion;
  try {
    const [stored, permissionGranted] = await Promise.all([
      chrome.storage.local.get({ [loginKey]: true }),
      chrome.permissions.contains({ permissions: ["cookies"] })
    ]);
    const granted = permissionGranted && permissionVersion === cookiePermissionVersion;
    useBrowserCookies = stored[loginKey] === true && granted;
    loginOption.checked = useBrowserCookies;
    loginWarning.textContent = stored[loginKey] && !granted ? localize("浏览器登录权限已失效，请在设置中重新授权；当前下载不会使用登录状态。") : "";
    if (stored[loginKey] && !granted) settingsStatus.textContent = localize("未获得浏览器 Cookie 权限");
    loginOption.disabled = false;
    loginSettingReady = true;
    settingsRetry.classList.add("hidden");
    if (!(stored[loginKey] && !granted)) settingsStatus.textContent = "";
    updateControls();
  } catch {
    settingsStatus.textContent = localize("无法读取浏览器登录设置");
    loginWarning.textContent = localize("无法读取浏览器登录设置，请在设置中重试后再发送资源。");
    settingsRetry.classList.remove("hidden");
  } finally {
    settingsRetry.disabled = false;
  }
}
settingsRetry.addEventListener("click", loadLoginSetting);
loginOption.addEventListener("change", async () => {
  loginOption.disabled = true;
  loginSettingReady = false;
  updateControls();
  settingsStatus.textContent = "";
  const permissionVersion = cookiePermissionVersion;
  try {
    const enabled = loginOption.checked;
    if (enabled && !(await chrome.permissions.request({ permissions: ["cookies"] }))) {
      loginOption.checked = false;
      settingsStatus.textContent = localize("未获得浏览器 Cookie 权限");
      return;
    }
    await chrome.storage.local.set({ [loginKey]: enabled });
    if (enabled && permissionVersion !== cookiePermissionVersion) {
      useBrowserCookies = false;
      loginOption.checked = false;
      loginWarning.textContent = localize("浏览器登录权限已失效，请在设置中重新授权；当前下载不会使用登录状态。");
      settingsStatus.textContent = localize("未获得浏览器 Cookie 权限");
      return;
    }
    useBrowserCookies = enabled;
    loginWarning.textContent = "";
  } catch {
    loginOption.checked = useBrowserCookies;
    settingsStatus.textContent = localize("无法保存浏览器登录设置");
  } finally {
    loginOption.disabled = false;
    loginSettingReady = true;
    updateControls();
  }
});
chrome.permissions.onRemoved.addListener((removed) => {
  if (!removed.permissions?.includes("cookies")) return;
  cookiePermissionVersion++;
  if (!useBrowserCookies) return;
  useBrowserCookies = false;
  loginOption.checked = false;
  loginWarning.textContent = localize("浏览器登录权限已失效，请在设置中重新授权；当前下载不会使用登录状态。");
  settingsStatus.textContent = localize("未获得浏览器 Cookie 权限");
});
loadLoginSetting();

function markUncertain(key, button, selectionURL = key) {
  uncertainURLs.add(key);
  if (selectionURL) selected.delete(selectionURL);
  if (button) {
    button.textContent = localize("待确认");
    button.title = localize("请先检查桌面或浏览器下载列表，确认后可重新打开扩展操作");
    button.setAttribute("aria-label", button.title);
    button.dataset.uncertain = "true";
    button.dataset.finished = "true";
  }
}

async function activeTab() {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  return tab;
}

// A scan can span a navigation, including a reload to the same URL. Track
// top-level document and history changes so results from the old page are not
// presented as belonging to the new one.
const navigationRevisions = new Map();
const documentRevisions = new Map();
let displayedDocument = "";
function noteNavigation(details, replaced = false) {
  if (details.frameId !== 0) return;
  navigationRevisions.set(details.tabId, (navigationRevisions.get(details.tabId) || 0) + 1);
  if (replaced) documentRevisions.set(details.tabId, (documentRevisions.get(details.tabId) || 0) + 1);
  if (details.tabId === displayedTabId) {
    pageStale = true;
    selected.clear();
    statusEl.textContent = localize("页面已发生变化，请重新扫描");
    updateControls();
  }
}
chrome.webNavigation.onCommitted.addListener((details) => noteNavigation(details, true));
chrome.webNavigation.onHistoryStateUpdated.addListener((details) => noteNavigation(details));

async function withTimeout(promise, milliseconds) {
  let timer;
  try {
    return await Promise.race([
      promise,
      new Promise((_, reject) => { timer = setTimeout(() => reject(new Error("timeout")), milliseconds); })
    ]);
  } finally {
    clearTimeout(timer);
  }
}

async function sniff(forceRules = false, onProgress = null) {
  let tab;
  try {
    tab = await activeTab();
  } catch {
    return { resources: [], error: localize("无法读取当前标签页") };
  }
  if (!tab || !tab.id) {
    return { resources: [], error: localize("没有活动标签页") };
  }
  const navigationRevision = navigationRevisions.get(tab.id) || 0;
  let frames, limitedFrames = false;
  try {
    frames = await withTimeout(chrome.webNavigation.getAllFrames({ tabId: tab.id }), 5000);
  } catch {
    frames = null;
    limitedFrames = true;
  }
  if (!frames || !frames.length) {
    frames = [{ frameId: 0, url: tab.url || "" }];
    limitedFrames = true;
  }
  const resources = new Map();
  let responded = 0, inaccessible = 0, timedOut = 0, rulesStale = false;
  const report = () => {
    if (onProgress && (navigationRevisions.get(tab.id) || 0) === navigationRevision) {
      onProgress({ resources: [...resources.values()], tabId: tab.id });
    }
  };
  const handleFrame = (result, index) => {
    if (result.status !== "fulfilled" || !Array.isArray(result.value?.resources)) {
      if (/^https?:/i.test(frames[index].url || "")) inaccessible++;
      if (result.status === "rejected" && result.reason?.message === "timeout") timedOut++;
      return;
    }
    responded++;
    if (forceRules && result.value.rulesFresh !== true) rulesStale = true;
    for (const item of result.value.resources) {
      if (!item || typeof item.url !== "string" || !/^https?:/i.test(item.url)) continue;
      const old = resources.get(item.url);
      // Prefer the frame that directly exposes a media element or a useful
      // download name over another frame's incidental link or iframe URL.
      const rank = (r) => (MEDIA_KINDS.has(r.kind) ? 2 : 0) + (r.downloadAttr || r.text ? 1 : 0);
      const fromTab = { ...item, tabId: tab.id };
      if (!old) {
        resources.set(item.url, fromTab);
      } else {
        const preferred = rank(fromTab) > rank(old) ? fromTab : old;
        const other = preferred === fromTab ? old : fromTab;
        resources.set(item.url, {
          ...preferred,
          downloadAttr: preferred.downloadAttr || other.downloadAttr || "",
          text: preferred.text || other.text || ""
        });
      }
    }
    report();
  };
  const frameJobs = frames.map(async (frame, index) => {
    try {
      const value = await withTimeout(chrome.tabs.sendMessage(tab.id, { type: forceRules ? "rescan" : "getResources" }, { frameId: frame.frameId }), 7000);
      handleFrame({ status: "fulfilled", value }, index);
    } catch (reason) {
      handleFrame({ status: "rejected", reason }, index);
    }
  });
  const cachedJobs = forceRules ? [] : frames.map(async (frame, index) => {
    try {
      const value = await withTimeout(chrome.tabs.sendMessage(tab.id, { type: "cachedResources" }, { frameId: frame.frameId }), 500);
      if (Array.isArray(value?.resources)) {
        // Cached responses only contribute early display; they do not count
        // as completed scans or affect final frame diagnostics.
        const before = responded;
        handleFrame({ status: "fulfilled", value }, index);
        responded = before;
      }
    } catch {}
  });
  const observedJob = (async () => { try {
    const observed = await withTimeout(chrome.runtime.sendMessage({ type: "observedPlaylists", tabId: tab.id }), 5000);
    for (const item of observed?.resources || []) {
      if (!item || !/^https?:/i.test(item.url || "")) continue;
      const old = resources.get(item.url);
      if (old) {
        // A playlist MIME takes precedence; otherwise keep a known filename
        // suffix and use the MIME-derived type for extensionless resources.
        const ext = item.ext && (item.kind === "network:playlist" || !MEDIA_EXT.has(old.ext)) ? item.ext : old.ext;
        resources.set(item.url, { ...old, ext, kind: MEDIA_KINDS.has(old.kind) ? old.kind : item.kind });
      } else {
        const frame = frames.find((entry) => entry.frameId === item.frameId);
        resources.set(item.url, { ...item, pageUrl: item.pageUrl || frame?.url || tab.url || "", title: tab.title || "", tabId: tab.id });
      }
    }
    report();
  } catch {
    // Frame scanning remains usable if the background cache is unavailable.
  } })();
  await Promise.all([...frameJobs, ...cachedJobs, observedJob]);
  try {
    const current = await activeTab();
    if (!current || current.id !== tab.id || current.url !== tab.url || (navigationRevisions.get(tab.id) || 0) !== navigationRevision) {
      return { resources: [], error: localize("页面已发生变化，请重新扫描") };
    }
  } catch {
    return { resources: [], error: localize("无法读取当前标签页") };
  }
  if (!responded && !resources.size) return { resources: [], error: timedOut ? localize("页面框架响应超时，请稍后重新扫描") : localize("无法读取当前标签页") };
  return { resources: [...resources.values()], inaccessible, limitedFrames, rulesStale, tabId: tab.id };
}

function resourceName(item) {
  const candidate = (item.downloadAttr || item.text || "").trim();
  if (candidate && !/^(?:https?:|\/\/)/i.test(candidate)) return candidate;
  try {
    const part = new URL(item.url).pathname.split("/").filter(Boolean).pop();
    if (part) { try { return decodeURIComponent(part); } catch { return part; } }
  } catch {}
  return localize("未命名资源");
}

function row(item) {
  const div = document.createElement("div");
  div.className = "row";
  div.dataset.url = item.url;
  const checkbox = document.createElement("input");
  checkbox.type = "checkbox";
  checkbox.dataset.url = item.url;
  checkbox.checked = selected.has(item.url);
  checkbox.setAttribute("aria-label", localize("选择 ") + resourceName(item));
  checkbox.addEventListener("change", () => {
    if (checkbox.checked) selected.add(item.url);
    else selected.delete(item.url);
    updateControls();
  });
  const name = document.createElement("div");
  name.className = "name";
  name.textContent = resourceName(item);
  name.title = name.textContent;
  const ext = typeof item.ext === "string" ? item.ext.toLowerCase().replace(/[^a-z0-9]/g, "").slice(0, 8) : "";
  const kind = document.createElement("span");
  kind.className = "file-kind";
  kind.dataset.kind = ext === "pdf" ? "pdf" : (MEDIA_EXT.has(ext) ? "media" : "file");
  const kindIcon = document.createElement("img");
  kindIcon.src = "icons/file.svg";
  kindIcon.alt = "";
  const kindLabel = document.createElement("span");
  kindLabel.textContent = ext ? "." + ext : localize("文件");
  kind.append(kindIcon, kindLabel);
  const btn = document.createElement("button");
  btn.dataset.key = item.url;
  btn.dataset.mode = "file";
  const playlist = item.ext === "m3u8" || item.ext === "mpd";
  btn.textContent = sentURLs.has(item.url) ? localize("再次下载") : (playlist ? localize("下载清单") : localize("下载"));
  btn.setAttribute("aria-label", btn.textContent + " " + resourceName(item));
  btn.title = btn.textContent;
  if (sentURLs.has(item.url)) btn.dataset.finished = "true";
  if (uncertainURLs.has(item.url)) markUncertain(item.url, btn);
  const send = async (action, mediaMode = "") => {
    if (pageStale || scanBusy || !loginSettingReady || batchBusy || sendingURLs.has(item.url)) return;
    div.querySelector(".row-error")?.remove();
    sendingURLs.add(item.url);
    action.dataset.sending = "true";
    const key = mediaMode ? videoKey(item.url, mediaMode) : item.url;
    singleBusy++;
    updateControls();
    try {
      const r = await chrome.runtime.sendMessage({
        type: "prompt",
        url: item.url,
        pageUrl: item.pageUrl,
        title: item.title,
        filename: item.downloadAttr || "",
        tabId: item.tabId,
        mediaMode,
        submissionNonce: crypto.randomUUID(),
        useBrowserCookies,
        queue: ""
      });
      if (r && r.ok) {
        action.textContent = localize("再次下载");
        action.title = action.textContent;
        action.setAttribute("aria-label", action.textContent + " " + resourceName(item));
        action.dataset.finished = "true";
        sentURLs.add(key);
        markSent();
        if (!mediaMode) {
          selected.delete(item.url);
          checkbox.checked = false;
        }
      } else {
        if (!r || r.deliveryUnknown) {
          markUncertain(key, action, mediaMode ? "" : item.url);
          if (!mediaMode) checkbox.checked = false;
        } else {
          action.textContent = localize("重试");
          action.title = action.textContent;
          action.setAttribute("aria-label", action.textContent + " " + resourceName(item));
        }
        statusEl.textContent = (r && r.reason) || localize("无法确认任务是否已发送，请先检查下载列表");
        const error = document.createElement("div");
        error.className = "row-error";
        error.setAttribute("role", "alert");
        error.textContent = statusEl.textContent;
        div.appendChild(error);
      }
    } catch (e) {
      markUncertain(key, action, mediaMode ? "" : item.url);
      if (!mediaMode) checkbox.checked = false;
      statusEl.textContent = localize("无法确认任务是否已发送，请先检查下载列表：") + String(e);
      const error = document.createElement("div");
      error.className = "row-error";
      error.setAttribute("role", "alert");
      error.textContent = statusEl.textContent;
      div.appendChild(error);
    } finally {
      delete action.dataset.sending;
      sendingURLs.delete(item.url);
      singleBusy--;
      updateControls();
    }
  };
  btn.addEventListener("click", () => send(btn));
  div.appendChild(checkbox);
  div.appendChild(kind);
  div.appendChild(name);
  if (item.ext === "m3u8" || item.ext === "mpd") {
    const mode = item.ext === "mpd" ? "dash-fmp4" : "hls-ts";
    const video = document.createElement("button");
    video.dataset.key = videoKey(item.url, mode);
    video.dataset.mode = mode;
    video.textContent = sentURLs.has(video.dataset.key) ? localize("再次下载") : localize("下载视频");
    video.setAttribute("aria-label", video.textContent + " " + resourceName(item));
    if (sentURLs.has(video.dataset.key)) {
      video.dataset.finished = "true";
      video.title = video.textContent;
    } else {
      video.title = video.textContent;
    }
    if (uncertainURLs.has(video.dataset.key)) markUncertain(video.dataset.key, video, "");
    video.addEventListener("click", () => send(video, mode));
    div.appendChild(video);
  }
  div.appendChild(btn);
  const copy = document.createElement("button");
  copy.type = "button";
  copy.dataset.copy = "true";
  copy.textContent = localize("复制链接");
  copy.setAttribute("aria-label", localize("复制链接"));
  copy.title = localize("复制链接");
  copy.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(item.url);
      statusEl.textContent = localize("链接已复制");
    } catch { statusEl.textContent = localize("无法复制链接"); }
  });
  div.appendChild(copy);
  return div;
}

async function render(forceRules = false) {
  if (scanBusy || batchBusy || singleBusy > 0) return;
  scanBusy = true;
  statusEl.textContent = localize("正在读取当前标签页…");
  emptyEl.classList.add("hidden");
  updateControls();
  const version = ++renderVersion;
  let result;
  try {
    result = await sniff(forceRules, ({ resources, tabId }) => {
      if (version !== renderVersion || pageStale || batchBusy || !resources.length) return;
      const documentKey = `${tabId}:${documentRevisions.get(tabId) || 0}`;
      if (displayedDocument && displayedDocument !== documentKey) {
        selected.clear();
        sentURLs.clear();
        uncertainURLs.clear();
      }
      displayedDocument = documentKey;
      displayedTabId = tabId;
      currentResources = resources;
      emptyEl.classList.add("hidden");
      listEl.replaceChildren(...resources.map(row));
      updateControls();
    });
  } catch {
    result = { resources: [], error: localize("无法读取当前标签页") };
  } finally {
    scanBusy = false;
  }
  if (version !== renderVersion || batchBusy) { updateControls(); return; }
  const { resources, error, inaccessible, limitedFrames, rulesStale, tabId } = result;
  if (!error && Number.isInteger(tabId)) {
    const documentKey = `${tabId}:${documentRevisions.get(tabId) || 0}`;
    if (displayedDocument && displayedDocument !== documentKey) {
      selected.clear();
      sentURLs.clear();
      uncertainURLs.clear();
    }
    displayedDocument = documentKey;
  }
  displayedTabId = error ? null : tabId;
  pageStale = false;
  currentResources = resources;
  const visible = new Set(resources.map((item) => item.url));
  for (const url of selected) if (!visible.has(url)) selected.delete(url);
  listEl.textContent = "";
  const silentError = error === localize("无法读取当前标签页");
  const empty = (!error || silentError) && resources.length === 0;
  emptyEl.classList.toggle("hidden", !empty);
  statusEl.textContent = silentError ? "" : (error || "");
  for (const item of resources) listEl.appendChild(row(item));
  updateControls();
}

async function downloadBatch(resources, partialScan) {
  let sent = 0, fallback = 0, failed = 0, uncertain = 0, playlists = 0, lastError = "";
  const batchUsesBrowserCookies = useBrowserCookies;
  const rows = new Map([...listEl.children].map((element) => [element.dataset.url, element]));
  for (const item of resources) {
    try {
      const r = await chrome.runtime.sendMessage({ type: "prompt", url: item.url, pageUrl: item.pageUrl, title: item.title, filename: item.downloadAttr || "", tabId: item.tabId, queue: "", submissionNonce: crypto.randomUUID(), useBrowserCookies: batchUsesBrowserCookies });
      if (!r || r.deliveryUnknown) {
        uncertain++;
        markUncertain(item.url, rows.get(item.url)?.querySelector('button[data-mode="file"]'));
        lastError = (r && r.reason) || localize("无法确认任务是否已发送，请先检查下载列表");
      } else if (!r.ok) { failed++; lastError = r.reason || localize("发送失败"); }
      else {
        if (r.fallback) fallback++;
        else sent++;
        sentURLs.add(item.url);
        selected.delete(item.url);
        markSent();
        const button = rows.get(item.url)?.querySelector('button[data-mode="file"]');
        if (button) {
          button.textContent = localize("再次下载");
          button.title = button.textContent;
          button.setAttribute("aria-label", button.textContent + " " + resourceName(item));
          button.dataset.finished = "true";
        }
        if (item.ext === "m3u8" || item.ext === "mpd") playlists++;
      }
    } catch (e) {
      uncertain++;
      markUncertain(item.url, rows.get(item.url)?.querySelector('button[data-mode="file"]'));
      lastError = localize("无法确认任务是否已发送，请先检查下载列表：") + String(e);
    }
  }
  statusEl.textContent = localize("桌面 %s 项，浏览器 %s 项，失败 %s 项，待确认 %s 项", sent, fallback, failed, uncertain) +
    (playlists ? localize("，%s 项仅下载播放清单", playlists) : "") +
    (uncertain ? localize("；请先检查下载列表") : "") + (partialScan ? localize("；部分页面框架未扫描") : "") + (lastError ? `：${lastError}` : "");
  for (const input of listEl.querySelectorAll("input")) {
    input.checked = selected.has(input.dataset.url);
  }
}

async function runBatch(getResources) {
  if (scanBusy || !loginSettingReady || batchBusy || singleBusy > 0) return;
  batchBusy = true;
  renderVersion++;
  updateControls();
  try {
    const { resources, error, inaccessible, limitedFrames } = await getResources();
    if (error) statusEl.textContent = error === localize("无法读取当前标签页") ? "" : error;
    else {
      const unsent = resources.filter((item) => !uncertainURLs.has(item.url));
      if (!unsent.length) statusEl.textContent = localize("没有可下载的资源") + (inaccessible || limitedFrames ? localize("；部分页面框架未扫描") : "");
      else await downloadBatch(unsent, Boolean(inaccessible || limitedFrames));
    }
  } catch (e) {
    statusEl.textContent = localize("发送失败：") + String(e);
  } finally {
    batchBusy = false;
    updateControls();
  }
}

document.getElementById("dl-selected").addEventListener("click", () => {
  runBatch(async () => ({ resources: currentResources.filter((item) => selected.has(item.url)) }));
});

document.getElementById("dl-all").addEventListener("click", () => {
  runBatch(async () => {
    const result = await sniff();
    return result;
  });
});


const panelResources = document.getElementById("panel-resources");
const panelSettings = document.getElementById("panel-settings");
const settingsButton = document.getElementById("open-settings");
function showResources() {
  panelSettings.classList.add("hidden");
  panelResources.classList.remove("hidden");
  settingsButton.setAttribute("aria-expanded", "false");
}

settingsButton.addEventListener("click", () => {
  if (!panelSettings.classList.contains("hidden")) {
    showResources();
    settingsButton.focus();
    return;
  }
  panelResources.classList.add("hidden");
  panelSettings.classList.remove("hidden");
  settingsButton.setAttribute("aria-expanded", "true");
  document.getElementById("settings-back").focus();
});
document.getElementById("settings-back").addEventListener("click", () => {
  showResources();
  settingsButton.focus();
});
panelSettings.addEventListener("keydown", (event) => {
  if (event.key !== "Escape") return;
  event.preventDefault();
  showResources();
  settingsButton.focus();
});
document.getElementById("rescan-footer").addEventListener("click", () => render(true));

chrome.runtime.onMessage.addListener((message, sender) => {
  const tabId = message?.type === "resourcesChanged" ? sender?.tab?.id :
    message?.type === "opendlResourcesUpdated" ? message.tabId : null;
  if (Number.isInteger(tabId) && tabId === displayedTabId && !pageStale) scheduleResourceRefresh();
});
listEl.addEventListener("focusout", () => {
  if (refreshPending) scheduleResourceRefresh();
});

render();
