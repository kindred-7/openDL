// opendl content script: page resource sniffing.
//
// The URL tokenizer is the permissive one reconstructed from 参考下载器's content
// script (pathrec/extract.go documents it): paths with spaces, semicolons,
// tildes and non-ASCII text stay intact, which strict RFC parsers drop.

(() => {
  if (window.__opendl_init__) return;
  window.__opendl_init__ = true;

  // These are class contents, not bracketed classes: URL_RE supplies the
  // brackets when composing each part of the expression.
  const T = "\\w\\-.+*()$!,%:@& \\u0080-\\uFFFF";
  const H = "\\w\\u0080-\\uFFFF\\-";
  const Q = "[^\\s\\[\\]{}()\"'<>]";
  const URL_RE = new RegExp(
    `\\b\\w+://(?:[${T}]*(?::[${T}]*)?@)?[${H}.]+\\.[${H}]+(?::\\d+)?` +
      `(?:/(?:(?: +(?!\\w+:))?[${T}/~;])*)?(?:\\?${Q}*)?(?:#[${T}]*)?`,
    "gi"
  );

  const DOWNLOAD_EXT = globalThis.openDLResourceTypes.downloadable;
  const LINK_EXT = globalThis.openDLResourceTypes.discoverable;

  // A single isolated overlay avoids modifying a site's link or layout.
  let hoverEnabled = false, hoverLink = null, hoverHref = "", hoverTimer = 0, hoverHost = null, hoverControl = null, hoverRevision = 0, hoverSettingRevision = 0;
  const uncertainHoverURLs = new Set();
  const sendingHoverURLs = new Set();
  chrome.storage.local.get({ "opendl-hover-download": true }).then((s) => { if (!hoverSettingRevision) hoverEnabled = s["opendl-hover-download"] !== false; }).catch(() => { if (!hoverSettingRevision) hoverEnabled = true; });
  chrome.storage.onChanged.addListener((changes, area) => {
    if (area === "local" && changes["opendl-hover-download"]) {
      hoverSettingRevision++;
      hoverEnabled = changes["opendl-hover-download"].newValue !== false;
      if (!hoverEnabled && hoverHost) { hoverRevision++; hoverHost.hidden = true; }
    }
  });
  function resetHoverIcon(button) {
    const icon = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    icon.setAttribute("viewBox", "0 0 24 24");
    icon.setAttribute("width", "18"); icon.setAttribute("height", "18");
    icon.setAttribute("fill", "none"); icon.setAttribute("stroke", "currentColor");
    icon.setAttribute("stroke-width", "1.75"); icon.setAttribute("stroke-linecap", "round"); icon.setAttribute("stroke-linejoin", "round");
    for (const d of ["M12 3v12", "m7 10 5 5 5-5", "M5 17v2a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-2"]) {
      const path = document.createElementNS("http://www.w3.org/2000/svg", "path"); path.setAttribute("d", d); icon.appendChild(path);
    }
    button.replaceChildren(icon);
  }
  function hoverButton() {
    if (hoverHost) {
      if (!hoverHost.isConnected) document.documentElement.appendChild(hoverHost);
      return hoverHost;
    }
    hoverHost = document.createElement("div");
    hoverHost.style.cssText = "position:fixed;z-index:2147483647;";
    const shadow = hoverHost.attachShadow({ mode: "closed" });
    const button = document.createElement("button");
    hoverControl = button;
    button.type = "button";
    button.title = chrome.i18n.getMessage("m_context_download");
    button.setAttribute("aria-label", button.title);
    button.style.cssText = "width:32px;height:32px;display:grid;place-items:center;border:1px solid rgba(128,128,128,.25);border-radius:8px;background:rgba(30,30,32,.88);color:white;box-shadow:0 3px 12px #0004;cursor:pointer;font:18px system-ui;backdrop-filter:blur(8px)";
    const feedback = document.createElement("div");
    feedback.setAttribute("role", "status");
    feedback.setAttribute("aria-live", "polite");
    feedback.style.cssText = "position:absolute;right:0;top:38px;max-width:min(280px,90vw);min-width:140px;padding:8px 10px;border-radius:8px;background:rgba(30,30,32,.95);color:white;box-shadow:0 3px 12px #0004;font:12px/1.45 system-ui;overflow-wrap:anywhere;";
    feedback.hidden = true;
    resetHoverIcon(button);
    button.addEventListener("click", async (event) => {
      event.preventDefault(); event.stopPropagation();
      const link = hoverLink;
      if (!link || !link.isConnected || link.href !== hoverHref) { hoverHost.hidden = true; return; }
      if (sendingHoverURLs.has(link.href) || uncertainHoverURLs.has(link.href)) return;
      const revision = hoverRevision;
      const url = link.href, filename = link.download || "", pageUrl = location.href, title = document.title;
      sendingHoverURLs.add(url);
      button.disabled = true;
      feedback.hidden = true;
      let sent = false;
      try {
        const stored = await chrome.storage.local.get({ "opendl-use-browser-cookies": true });
        sent = true;
        const reply = await chrome.runtime.sendMessage({ type: "prompt", url, pageUrl, title, filename, useBrowserCookies: stored["opendl-use-browser-cookies"] === true });
        if (!reply || reply.deliveryUnknown) uncertainHoverURLs.add(url);
        if (revision !== hoverRevision || !link.isConnected || link.href !== url) return;
        if (!reply?.ok) { button.title = reply?.reason || chrome.i18n.getMessage(reply?.deliveryUnknown || !reply ? "m_hover_uncertain" : "m_hover_failed"); button.textContent = "!"; }
        else { button.title = chrome.i18n.getMessage("m_hover_sent"); button.textContent = "✓"; }
        button.setAttribute("aria-label", button.title);
        feedback.textContent = button.title;
        feedback.hidden = false;
      } catch {
        if (sent) uncertainHoverURLs.add(url);
        if (revision === hoverRevision && link.isConnected && link.href === url) { button.title = chrome.i18n.getMessage(sent ? "m_hover_uncertain" : "m_hover_failed"); button.setAttribute("aria-label", button.title); button.textContent = "!"; feedback.textContent = button.title; feedback.hidden = false; }
      }
      finally { sendingHoverURLs.delete(url); button.disabled = uncertainHoverURLs.has(hoverHref) || sendingHoverURLs.has(hoverHref); }
    });
    hoverHost.addEventListener("mouseenter", () => clearTimeout(hoverTimer));
    hoverHost.addEventListener("mouseleave", () => { hoverTimer = setTimeout(() => { hoverHost.hidden = true; }, 350); });
    shadow.append(button, feedback);
    document.documentElement.appendChild(hoverHost);
    return hoverHost;
  }
  function showHover(link) {
    if (!hoverEnabled || !link.isConnected || !link.matches(":hover")) return;
    const changed = hoverLink !== link || hoverHref !== link.href;
    hoverLink = link;
    hoverHref = link.href;
    clearTimeout(hoverTimer);
    const host = hoverButton(), bounds = link.getBoundingClientRect();
    if (changed) { hoverRevision++; resetHoverIcon(hoverControl); hoverControl.title = chrome.i18n.getMessage("m_context_download"); hoverControl.setAttribute("aria-label", hoverControl.title); hoverControl.nextElementSibling.hidden = true; }
    if (uncertainHoverURLs.has(hoverHref)) {
      hoverControl.disabled = true;
      hoverControl.title = chrome.i18n.getMessage("m_hover_uncertain");
      hoverControl.setAttribute("aria-label", hoverControl.title);
      hoverControl.textContent = "!";
      hoverControl.nextElementSibling.textContent = hoverControl.title;
      hoverControl.nextElementSibling.hidden = false;
    } else hoverControl.disabled = sendingHoverURLs.has(hoverHref);
    const x = Math.max(4, Math.min(innerWidth - 36, bounds.right - 32));
    const y = Math.max(4, Math.min(innerHeight - 36, bounds.top >= 34 ? bounds.top - 34 : bounds.bottom + 2));
    host.style.left = x + "px";
    host.style.top = y + "px";
    const feedback = hoverControl.nextElementSibling;
    feedback.style.right = innerWidth - x >= Math.min(280, innerWidth * 0.9) ? "auto" : "0";
    feedback.style.left = feedback.style.right === "auto" ? "0" : "auto";
    feedback.style.top = y > innerHeight - 150 ? "auto" : "38px";
    feedback.style.bottom = feedback.style.top === "auto" ? "38px" : "auto";
    host.hidden = false;
  }
  let pendingHoverLookup = null;
  document.addEventListener("mouseover", (event) => {
    if (!hoverEnabled || !(event.target instanceof Element)) return;
    const link = event.target.closest("a[href]");
    if (!link || !/^https?:/i.test(link.href)) return;
    const resource = resources.get(link.href);
    const media = globalThis.openDLResourceTypes.media;
    const suggestedExt = (link.download || "").match(/\.([a-z0-9]+)$/i)?.[1]?.toLowerCase();
    if (media.has(extOf(link.href)) || media.has(suggestedExt) || media.has(resource?.ext) ||
        ["video", "audio", "source", "network:media", "network:video", "network:playlist"].includes(resource?.kind)) {
      showHover(link);
      return;
    }
    const url = link.href;
    if (pendingHoverLookup?.link === link && pendingHoverLookup.url === url) return;
    const pending = { link, url };
    pendingHoverLookup = pending;
    chrome.runtime.sendMessage({ type: "hoverMediaLookup", url }).then((reply) => {
      if (pendingHoverLookup === pending && reply?.media && link.href === url) showHover(link);
    }).catch(() => {}).finally(() => { if (pendingHoverLookup === pending) pendingHoverLookup = null; });
  }, true);
  document.addEventListener("mouseout", (event) => {
    if (hoverLink && event.target instanceof Node && hoverLink.contains(event.target) && !hoverLink.contains(event.relatedTarget)) {
      hoverTimer = setTimeout(() => { if (hoverHost) hoverHost.hidden = true; }, 350);
    }
  }, true);
  window.addEventListener("scroll", () => { if (hoverHost) hoverHost.hidden = true; }, true);
  window.addEventListener("resize", () => { if (hoverHost) hoverHost.hidden = true; });
  document.addEventListener("keydown", (event) => { if (event.key === "Escape" && hoverHost) hoverHost.hidden = true; }, true);

  // ---- site rules ----------------------------------------------------------
  // Site rules recognize download paths that carry no file extension. They
  // come from the desktop's rules file through the background and follow the
  // expansion 参考下载器's content script uses: a "^/" pattern matches both the bare
  // path and the full address.
  let siteRulesCompiled = [];
  let siteRulesAt = 0;

  const expandPathPattern = (pattern, hostPattern) => {
    if (!pattern.includes("^/")) return pattern;
    let prefix = String(hostPattern || "").trim().replace(/^\^/, "").replace(/\$$/, "");
    prefix = prefix ? "https?://" + prefix : "https?://[^/]+";
    return pattern.split("^/").join("^(?:" + prefix + ")?/");
  };

  const compileSiteRules = (rules) => {
    const out = [];
    if (!Array.isArray(rules)) return out;
    for (const rule of rules.slice(0, 256)) {
      if (!rule || typeof rule !== "object") continue;
      const path = typeof rule.path === "string" ? rule.path : "";
      const host = typeof rule.host === "string" ? rule.host : "";
      if (!path || path.length > 512 || host.length > 512) continue;
      try {
        const compiled = {
          host: host ? new RegExp(host, "i") : null,
          path: new RegExp(expandPathPattern(path, host)),
          exts: null,
          ext: ""
        };
        if (Array.isArray(rule.exts) && rule.exts.length) {
          compiled.exts = new Set(rule.exts.map((value) => String(value).toLowerCase()).filter((value) => value.startsWith(".")));
          // A declared filter with no valid entry never matches; skip like the
          // desktop-side validation does.
          if (!compiled.exts.size) continue;
          compiled.ext = [...compiled.exts][0].slice(1);
        }
        out.push(compiled);
      } catch {}
    }
    return out;
  };

  const matchSiteRule = (url) => {
    for (const rule of siteRulesCompiled) {
      try {
        const u = new URL(url);
        if (rule.host && !rule.host.test(u.hostname)) continue;
        if (rule.exts && !rule.exts.has(extOf(url) ? "." + extOf(url) : "")) continue;
        if (rule.path.test(url)) return rule;
      } catch {}
    }
    return null;
  };

  // Rules are cached and refreshed when stale; a forced refresh re-reads the
  // desktop's rules file so edits apply without restarting anything.
  const refreshSiteRules = (force) => {
    if (!force && siteRulesAt && Date.now() - siteRulesAt < 300000) return Promise.resolve();
    return new Promise((resolve) => {
      let done = false;
      const finish = (rules, fresh = false) => {
        if (done) return;
        done = true;
        clearTimeout(timer);
        if (Array.isArray(rules)) {
          siteRulesAt = Date.now();
          siteRulesCompiled = compileSiteRules(rules);
        }
        resolve(fresh);
      };
      const timer = setTimeout(() => finish(null), force ? 6000 : 300);
      try {
        chrome.runtime.sendMessage({ type: "siteRules", refresh: Boolean(force) }, (reply) => {
          void chrome.runtime.lastError;
          finish(reply && reply.rules, Boolean(reply?.fresh));
        });
      } catch {
        finish(null);
      }
    });
  };

  const extOf = (url) => {
    try {
      // Match the desktop rule engine, which compares extensions against the
      // decoded URL path (for example, a percent-encoded dot in a filename).
      const encodedPath = new URL(url).pathname;
      let path;
      try {
        path = decodeURIComponent(encodedPath);
      } catch {
        // Legacy servers may percent-encode non-UTF-8 bytes. Preserve those
        // bytes while still decoding ASCII separators and extension characters.
        path = encodedPath.replace(/%([0-7][0-9a-f])/gi, (_, byte) => String.fromCharCode(parseInt(byte, 16)));
      }
      const dot = path.lastIndexOf(".");
      const slash = path.lastIndexOf("/");
      return dot > slash ? path.slice(dot + 1).toLowerCase() : "";
    } catch {
      return "";
    }
  };

  // One deduplicated resource table per frame.
  const resources = new Map();
  const ruleOnlyResources = new Set();
  let badgeTimer;
  const notifyResourcesChanged = () => {
    clearTimeout(badgeTimer);
    badgeTimer = setTimeout(() => {
      try { chrome.runtime.sendMessage({ type: "resourcesChanged" }, () => { void chrome.runtime.lastError; }); } catch {}
    }, 500);
  };
  // Several passes can see one address; the strongest classification wins.
  const MEDIA_KIND = new Set(["video", "audio", "source", "embed", "object", "network:media", "network:video"]);
  const rank = (kind) => {
    if (kind === "network:playlist") return 3;
    if (MEDIA_KIND.has(kind)) return 2;
    return 1;
  };
  const add = (url, kind, extra) => {
    if (!url || !/^https?:/i.test(url)) return;
    const { ruleOnly = false, ...metadata } = extra || {};
    const key = url;
    if (resources.has(key)) {
      // A media element may be seen before an anchor for the same URL.
      // Keep the stronger media classification while retaining its filename.
      const existing = resources.get(key);
      if (!ruleOnly) ruleOnlyResources.delete(key);
      if (metadata.downloadAttr && !existing.downloadAttr) existing.downloadAttr = metadata.downloadAttr;
      if (metadata.text && !existing.text) existing.text = metadata.text;
      if (metadata.player && !existing.player) existing.player = true;
      if (rank(kind) > rank(existing.kind)) {
        existing.kind = kind;
        if (metadata.ext) existing.ext = metadata.ext;
      } else if (metadata.ext && !existing.ext) {
        existing.ext = metadata.ext;
      }
      return;
    }
    const item = Object.assign(
      {
        url,
        kind,
        ext: extOf(url),
        pageUrl: location.href,
        title: document.title || "",
        frame: window !== window.top
      },
      metadata
    );
    resources.set(key, item);
    if (ruleOnly) ruleOnlyResources.add(key);
    notifyResourcesChanged();
  };

  // Permissive scan: pick "special" URLs out of any text the page carries.
  const scanText = (text) => {
    if (!text) return;
    URL_RE.lastIndex = 0;
    for (let m; (m = URL_RE.exec(text)); ) {
      let u = m[0];
      const tail = / +\w+:/.exec(u);
      if (tail) u = u.slice(0, tail.index);
      // Do not attach surrounding prose to a filename already ending in a
      // recognized extension, while preserving spaces within that filename.
      for (;;) {
        const suffix = / [^\s/]+$/.exec(u);
        if (!suffix || !LINK_EXT.has(extOf(u.slice(0, suffix.index)))) break;
        u = u.slice(0, suffix.index);
      }
      const ext = extOf(u);
      const rule = matchSiteRule(u);
      if (LINK_EXT.has(ext) || rule) add(u, "link", { via: "text", ruleOnly: !LINK_EXT.has(ext) });
    }
  };

  const scanDom = (root) => {
    const matching = (selector) => {
      const descendants = [...root.querySelectorAll(selector)];
      if (root.nodeType === 1 && root.matches(selector)) descendants.unshift(root);
      return descendants;
    };
    for (const el of matching("video,audio,source,embed,object")) {
      const extra = {
        width: el.videoWidth || el.clientWidth || 0,
        height: el.videoHeight || el.clientHeight || 0
      };
      // currentSrc is the browser-selected source, which may differ from src.
      if (el.currentSrc) add(el.currentSrc, el.tagName.toLowerCase(), extra);
      const url = el.src || el.data || "";
      if (url) add(url, el.tagName.toLowerCase(), extra);
    }
    for (const el of matching("iframe")) {
      if (el.src) add(el.src, "iframe");
    }
    for (const a of matching("a[href]")) {
      const ext = extOf(a.href);
      const rule = matchSiteRule(a.href);
      if (LINK_EXT.has(ext) || rule || a.hasAttribute("download")) {
        add(a.href, "link", {
          text: (a.innerText || a.title || "").trim().slice(0, 120),
          downloadAttr: a.getAttribute("download") || "",
          ruleOnly: !LINK_EXT.has(ext) && !a.hasAttribute("download"),
          ...(rule?.ext && !ext ? { ext: rule.ext } : {})
        });
      }
    }
    for (const el of matching("[srcset]")) {
      for (const part of (el.getAttribute("srcset") || "").split(",")) {
        const candidate = part.trim().split(/\s+/)[0];
        if (!candidate) continue;
        try {
          const url = new URL(candidate, document.baseURI).href;
          if (DOWNLOAD_EXT.has(extOf(url))) add(url, "srcset");
        } catch {}
      }
    }
  };

  // Resource timing exposes completed requests, including media and fetches.
  const addPerformanceEntry = (e) => {
    const ext = extOf(e.name);
    const rule = matchSiteRule(e.name);
    if (DOWNLOAD_EXT.has(ext) || rule || e.initiatorType === "media" || e.initiatorType === "video") {
      add(e.name, "network:" + e.initiatorType, {
        ruleOnly: !DOWNLOAD_EXT.has(ext) && e.initiatorType !== "media" && e.initiatorType !== "video",
        ...(rule?.ext && !ext ? { ext: rule.ext } : {})
      });
    }
  };
  const scanPerformance = () => {
    if (!performance.getEntriesByType) return;
    for (const e of performance.getEntriesByType("resource")) addPerformanceEntry(e);
  };

  // Listen for future resources as well as buffered entries; the browser's
  // resource timing buffer may fill on long-running dynamic pages. Never
  // clear or resize that shared buffer, as the page itself may use it.
  let resourceObserver = null;
  if (typeof PerformanceObserver === "function") {
    try {
      resourceObserver = new PerformanceObserver((list) => {
        for (const entry of list.getEntries()) addPerformanceEntry(entry);
      });
      resourceObserver.observe({ type: "resource", buffered: true });
    } catch {
      try {
        resourceObserver?.observe({ entryTypes: ["resource"] });
      } catch {
        resourceObserver = null;
      }
    }
  }

  const fullScan = () => {
    scanDom(document);
    scanPerformance();
    scanText(document.documentElement.innerHTML);
  };

  const pendingRoots = new Set();
  let scanTimer = null;
  let scanAll = false;
  const scheduleScan = (node) => {
    if (!node || node.nodeType !== 1) return;
    if (!scanAll) {
      pendingRoots.add(node);
      if (pendingRoots.size > 128) {
        pendingRoots.clear();
        scanAll = true;
      }
    }
    if (scanTimer !== null) return;
    scanTimer = setTimeout(() => {
      scanTimer = null;
      if (scanAll) {
        fullScan();
      } else {
        for (const root of pendingRoots) {
          scanDom(root);
          if (root.matches("script,template")) scanText(root.textContent);
          for (const el of root.querySelectorAll("script,template")) scanText(el.textContent);
        }
        scanPerformance();
      }
      pendingRoots.clear();
      scanAll = false;
    }, 200);
  };

  new MutationObserver((muts) => {
    for (const m of muts) {
      if (m.type === "attributes") {
        scheduleScan(m.target);
      } else if (m.type === "characterData") {
        if (m.target.parentElement?.matches("script,template")) scheduleScan(m.target.parentElement);
      } else {
        for (const n of m.addedNodes) {
          if (n.nodeType === 1) scheduleScan(n);
          else if (m.target.nodeType === 1 && m.target.matches("script,template")) scheduleScan(m.target);
        }
      }
    }
  }).observe(document.documentElement, {
    childList: true, subtree: true, characterData: true, attributes: true,
    attributeFilter: ["src", "href", "data", "srcset", "download"]
  });

  fullScan();

  // Keep this cached because the browser's default action must be prevented
  // synchronously during the click, before asynchronous messaging completes.
  let clickTakeoverEnabled = false;
  let takeoverRevision = 0;
  chrome.storage.local.get({ "opendl-takeover": true }).then((stored) => {
    if (!takeoverRevision) clickTakeoverEnabled = stored["opendl-takeover"] !== false;
  }).catch(() => {});
  chrome.storage.onChanged.addListener((changes, area) => {
    if (area === "local" && changes["opendl-takeover"]) {
      takeoverRevision++;
      clickTakeoverEnabled = changes["opendl-takeover"].newValue !== false;
    }
  });

  // Browser click takeover helpers: Alt+click forces a link through the
  // desktop app without ever starting a browser download, and Shift+click
  // releases one URL so the background keeps the browser download.
  document.addEventListener(
    "click",
    (event) => {
      try {
        if (event.button !== 0 || event.defaultPrevented) return;
        const anchor = event.target && event.target.closest ? event.target.closest("a[href]") : null;
        if (!anchor) return;
        const url = anchor.href;
        if (!/^https?:/i.test(url) || url.length > 4096) return;
        if (event.shiftKey) {
          chrome.runtime.sendMessage({ type: "releaseDownload", url });
          return;
        }
        if (!event.isTrusted || event.ctrlKey || event.metaKey) return;
        const matches = anchor.hasAttribute("download") || DOWNLOAD_EXT.has(extOf(url)) || Boolean(matchSiteRule(url));
        if (!event.altKey && (!clickTakeoverEnabled || !matches)) return;
        event.preventDefault();
        event.stopImmediatePropagation();
        chrome.runtime.sendMessage({
          type: "takeoverLink", url, pageUrl: location.href,
          title: document.title || "", filename: anchor.getAttribute("download") || ""
        }).then((reply) => {
          if (!reply?.ok) window.alert(reply?.reason || chrome.i18n.getMessage("m_hover_uncertain"));
        }).catch(() => window.alert(chrome.i18n.getMessage("m_hover_uncertain")));
      } catch {}
    },
    true
  );

  // ---- page-world sniffer bridge ------------------------------------------
  // extension/page.js runs in the page's own JavaScript world and reports the
  // addresses playback code uses: fetch/XHR responses with their Content-Type,
  // media element sources, and blobs promoted back into media sources. Every
  // record crossing worlds is untrusted page data: shapes are validated and
  // anything else is dropped. Forged records can only pollute the resource
  // list of the page that forged them.
  const PAGE_SOURCE = "opendl-page";
  const CS_SOURCE = "opendl-cs";
  const PAGE_VIAS = new Set(["fetch", "xhr", "media", "media-blob", "mse"]);
  const PLAYLIST_MIME = globalThis.openDLResourceTypes.playlistMime;
  const MEDIA_MIME = globalThis.openDLResourceTypes.directMediaMime;

  const acceptPageRecord = (record) => {
    if (!record || typeof record !== "object") return;
    const url = typeof record.url === "string" ? record.url : "";
    if (url.length > 4096 || !/^https?:/i.test(url)) return;
    const via = typeof record.via === "string" && PAGE_VIAS.has(record.via) ? record.via : "";
    if (!via) return;
    // Media elements carry their tag in "kind"; "tag" stays a fallback.
    const rawTag = typeof record.kind === "string" && /^(video|audio)$/.test(record.kind) ? record.kind : record.tag;
    const tag = typeof rawTag === "string" && /^(video|audio)$/.test(rawTag) ? rawTag : "";
    const ct = typeof record.ct === "string" ? record.ct.split(";", 1)[0].trim().toLowerCase() : "";
    const extra = { via };
    if (record.player === true) extra.player = true;
    const playlistExt = PLAYLIST_MIME.get(ct);
    if (playlistExt) {
      add(url, "network:playlist", { ...extra, ext: playlistExt });
      return;
    }
    const mediaExt = MEDIA_MIME.get(ct);
    // Media elements and blobs promoted into media sources describe whole
    // files. Raw fetch/XHR media responses are usually stream fragments and
    // stay unlisted, matching the background observer's policy.
    if (mediaExt && (tag || via === "media-blob")) {
      add(url, tag || "network:media", { ...extra, ext: mediaExt });
      return;
    }
    const ext = extOf(url);
    if (DOWNLOAD_EXT.has(ext)) {
      add(url, tag || (via === "media" || via === "media-blob" ? "network:media" : "link"), extra);
    }
  };

  let collectNonce = 0;
  const collectWaiters = new Map();
  // Ask the page hook for anything it recorded before this script loaded, and
  // let the newest batch land before popup/background queries are answered.
  const requestPageLog = () =>
    new Promise((resolve) => {
      const nonce = "c" + ++collectNonce;
      const timer = setTimeout(() => {
        collectWaiters.delete(nonce);
        resolve();
      }, 300);
      collectWaiters.set(nonce, () => {
        clearTimeout(timer);
        resolve();
      });
      try {
        window.postMessage({ source: CS_SOURCE, v: 1, type: "collect", nonce }, "*");
      } catch {
        clearTimeout(timer);
        collectWaiters.delete(nonce);
        resolve();
      }
    });

  window.addEventListener("message", (event) => {
    if (event.source !== window) return;
    const data = event.data;
    if (!data || data.source !== PAGE_SOURCE || data.v !== 1) return;
    if (data.type === "log" && Array.isArray(data.items)) {
      for (const record of data.items.slice(0, 512)) acceptPageRecord(record);
    }
    if (typeof data.nonce === "string" && collectWaiters.has(data.nonce)) {
      const done = collectWaiters.get(data.nonce);
      collectWaiters.delete(data.nonce);
      done();
    }
  });
  requestPageLog();
  refreshSiteRules(false);

  // popup/background queries. "download" only confirms the URL is on this
  // page; the actual transfer goes through the service worker to the host.
  chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
    if (msg.type === "getResources") {
      refreshSiteRules(false).then(() => {
        fullScan();
        return requestPageLog();
      }).then(() =>
        sendResponse({ resources: [...resources.values()], href: location.href, title: document.title })
      );
      return true;
    }
    if (msg.type === "rescan") {
      refreshSiteRules(true).then((rulesFresh) => {
        if (rulesFresh && ruleOnlyResources.size) {
          for (const url of ruleOnlyResources) resources.delete(url);
          ruleOnlyResources.clear();
          notifyResourcesChanged();
        }
        fullScan();
        return requestPageLog().then(() => rulesFresh);
      }).then((rulesFresh) => sendResponse({ count: resources.size, resources: [...resources.values()], href: location.href, title: document.title, rulesFresh }));
      return true;
    }
    return false;
  });
})();
