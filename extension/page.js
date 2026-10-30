// opendl page-world sniffer: the addresses players really use.
//
// Content scripts run in an isolated JavaScript world and cannot see the
// page's fetch/XHR/media plumbing. This script runs in the page's own world
// before page scripts and reports what playback code requests: fetch and XHR
// responses (with their Content-Type), media element sources, and blobs the
// page promotes back into media sources. MediaSource element sources carry no
// address of their own, so the manifest the player fetched is re-reported when
// MSE playback starts.
//
// Everything sent to the content script is page-controlled data: it is
// validated and classified there, and a forged record can only pollute the
// resource list of the page that forged it.

(() => {
  if (globalThis.__opendl_page_init__) return;
  globalThis.__opendl_page_init__ = true;

  const PAGE_SOURCE = "opendl-page";
  const CS_SOURCE = "opendl-cs";
  const MAX_ITEMS = 256;
  const FLUSH_MS = 60;
  const HINT = /m3u8?|mpd|manifest|playlist|\.m3u\b/i;
  const PLAIN = new Set(["", "text/plain", "application/octet-stream"]);

  const log = new Map(); // url -> merged record, kept for late collectors
  let pending = []; // records not yet posted to the content script
  let flushTimer = 0;
  let lastPlaylist = null; // most recent manifest seen in this frame

  const post = (message) => {
    try {
      window.postMessage(message, "*");
    } catch {}
  };
  const flush = () => {
    if (flushTimer) {
      clearTimeout(flushTimer);
      flushTimer = 0;
    }
    if (!pending.length) return;
    const items = pending;
    pending = [];
    post({ source: PAGE_SOURCE, v: 1, type: "log", items });
  };
  const scheduleFlush = () => {
    if (flushTimer) return;
    flushTimer = setTimeout(flush, FLUSH_MS);
  };

  const absolute = (value) => {
    try {
      return new URL(String(value), location.href).href;
    } catch {
      return "";
    }
  };
  const mimeOf = (ct) => String(ct || "").split(";", 1)[0].trim().toLowerCase();
  const playlistMimeOf = (ct) => {
    const mime = mimeOf(ct);
    if (/mpegurl/.test(mime)) return "application/vnd.apple.mpegurl";
    if (/dash\+xml/.test(mime)) return "application/dash+xml";
    return "";
  };

  const note = (record) => {
    if (!record || typeof record.url !== "string") return;
    const url = record.url;
    if (url.length > 4096 || !/^https?:/i.test(url)) return;
    const item = { url, via: String(record.via || "").slice(0, 16) };
    if (typeof record.ct === "string") item.ct = record.ct.slice(0, 128);
    if (typeof record.kind === "string") item.kind = record.kind.slice(0, 32);
    if (typeof record.tag === "string") item.tag = record.tag.slice(0, 16);
    if (record.player === true) item.player = true;
    const stored = log.get(url);
    if (stored) {
      for (const field of ["via", "ct", "kind", "tag"]) {
        if (!stored[field] && item[field]) stored[field] = item[field];
      }
      if (item.player) stored.player = true;
    } else {
      if (log.size >= MAX_ITEMS) log.delete(log.keys().next().value);
      log.set(url, item);
    }
    if (pending.length >= MAX_ITEMS) pending.shift();
    pending.push(item);
    scheduleFlush();
  };

  // A manifest response tells us which stream the page is playing; MSE
  // playback re-points at it when the media element takes the MediaSource URL.
  const trackPlaylist = (url, ct) => {
    const mime = playlistMimeOf(ct);
    if (mime) lastPlaylist = { url, ct: mime };
    return mime;
  };

  // Blob promotion: players fetch a whole file, turn it into a blob URL and
  // hand that URL to a media element. Tag the blob with its origin address so
  // the media element can be reported under the real URL.
  const blobSources = new WeakMap(); // Blob -> origin address
  const nonReplayableResponses = new WeakSet(); // known non-GET or unsuccessful responses
  const blobURLSources = new Map(); // blob: URL -> origin address
  const mseURLs = new Set(); // blob: URL backed by a MediaSource

  const noteMediaElement = (element, value) => {
    const raw = String(value || "");
    if (!raw) return;
    const tag = String(element.tagName || "").toLowerCase().slice(0, 16);
    if (/^blob:/i.test(raw)) {
      const source = blobURLSources.get(raw);
      if (source) {
        note({ url: source, kind: tag, via: "media-blob" });
      } else if (mseURLs.has(raw) && lastPlaylist) {
        note({ url: lastPlaylist.url, ct: lastPlaylist.ct, kind: tag, via: "mse", player: true });
      }
      return;
    }
    const url = absolute(raw);
    note({ url, kind: tag, via: "media" });
  };

  // media element src assignments, including those made by page scripts.
  try {
    const descriptor = Object.getOwnPropertyDescriptor(HTMLMediaElement.prototype, "src");
    if (descriptor && descriptor.set) {
      Object.defineProperty(HTMLMediaElement.prototype, "src", {
        configurable: true,
        enumerable: descriptor.enumerable,
        get: descriptor.get,
        set(value) {
          try {
            noteMediaElement(this, value);
          } catch {}
          descriptor.set.call(this, value);
        }
      });
    }
  } catch {}

  // Text playlists served with a wrong Content-Type still start with a header
  // tag. Peek at the first chunk of suspicious responses only, then cancel.
  const peekPlaylist = (url, via, response) => {
    try {
      const length = Number(response.headers.get("content-length") || 0);
      if (length > 1048576) return;
      const body = response.clone().body;
      if (!body) return;
      const reader = body.getReader();
      reader.read().then(({ done, value }) => {
        reader.cancel().catch(() => {});
        if (done || !value) return;
        const head = new TextDecoder().decode(value.slice(0, 2048)).replace(/^\uFEFF/, "").trimStart();
        const ct = head.startsWith("#EXTM3U")
          ? "application/vnd.apple.mpegurl"
          : head.startsWith("<?xml") && /<MPD[\s>]/.test(head)
            ? "application/dash+xml"
            : "";
        if (!ct) return;
        const final = response.url || url;
        trackPlaylist(final, ct);
        note({ url: final, via, ct });
      }, () => {});
    } catch {}
  };
  const noteResponse = (url, via, ct, response) => {
    const final = (response && response.url) || url;
    const mime = mimeOf(ct);
    trackPlaylist(final, mime);
    note({ url: final, via, ct: mime });
    if (PLAIN.has(mime) && HINT.test(final)) peekPlaylist(final, via, response);
  };

  if (typeof window.fetch === "function") {
    const nativeFetch = window.fetch;
    window.fetch = function (input, init) {
      const request = nativeFetch.apply(this, arguments);
      let method = "";
      try { method = String(init?.method || input?.method || "GET").toUpperCase(); } catch {}
      return request.then(
        (response) => {
          try {
            if (method !== "GET" || !response.ok) nonReplayableResponses.add(response);
            if (method === "GET" && response.ok) noteResponse(absolute(input && input.url ? input.url : input), "fetch", response.headers.get("content-type"), response);
          } catch {}
          return response;
        },
        (error) => {
          throw error;
        }
      );
    };
  }

  if (typeof XMLHttpRequest !== "undefined") {
    const REQUEST = Symbol("opendl-request");
    const nativeOpen = XMLHttpRequest.prototype.open;
    XMLHttpRequest.prototype.open = function (method, url) {
      try {
        this[REQUEST] = { url: absolute(url), via: "xhr", method: String(method).toUpperCase() };
      } catch {}
      return nativeOpen.apply(this, arguments);
    };
    const nativeSend = XMLHttpRequest.prototype.send;
    XMLHttpRequest.prototype.send = function () {
      try {
        const request = this[REQUEST];
        if (request && !request.watching) {
          request.watching = true;
          this.addEventListener("loadend", () => {
            try {
              if (request.method !== "GET" || this.status < 200 || this.status >= 300) return;
              const url = this.responseURL || request.url;
              const ct = this.getResponseHeader("content-type") || "";
              if (this.responseType === "blob" && this.response) {
                try {
                  blobSources.set(this.response, url);
                } catch {}
              }
              const mime = mimeOf(ct);
              trackPlaylist(url, mime);
              note({ url, via: request.via, ct: mime });
              if (PLAIN.has(mime) && HINT.test(url)) {
                const text = typeof this.responseText === "string" && this.responseText.length <= 1048576 ? this.responseText : "";
                if (text) {
                  const head = text.replace(/^\uFEFF/, "").trimStart();
                  const sniffed = head.startsWith("#EXTM3U")
                    ? "application/vnd.apple.mpegurl"
                    : head.startsWith("<?xml") && /<MPD[\s>]/.test(head)
                      ? "application/dash+xml"
                      : "";
                  if (sniffed) {
                    trackPlaylist(url, sniffed);
                    note({ url, via: request.via, ct: sniffed });
                  }
                }
              }
            } catch {}
          }, { once: true });
        }
      } catch {}
      return nativeSend.apply(this, arguments);
    };
  }

  // fetch() consumers may turn a response into a blob and hand that blob to a
  // media element; remember where each blob came from.
  if (typeof Response !== "undefined" && typeof Response.prototype.clone === "function") {
    const nativeClone = Response.prototype.clone;
    Response.prototype.clone = function () {
      const clone = nativeClone.call(this);
      try { if (nonReplayableResponses.has(this)) nonReplayableResponses.add(clone); } catch {}
      return clone;
    };
  }
  if (typeof Response !== "undefined" && typeof Response.prototype.blob === "function") {
    const nativeBlob = Response.prototype.blob;
    Response.prototype.blob = function () {
      return nativeBlob.call(this).then((blob) => {
        try {
          if (!nonReplayableResponses.has(this)) blobSources.set(blob, this.url || "");
        } catch {}
        return blob;
      });
    };
  }

  try {
    const nativeCreateObjectURL = URL.createObjectURL;
    URL.createObjectURL = function (object) {
      const url = nativeCreateObjectURL.apply(this, arguments);
      try {
        if (object && typeof object === "object") {
          const source = blobSources.get(object);
          if (source) blobURLSources.set(url, source);
          else if (typeof MediaSource !== "undefined" && object instanceof MediaSource) mseURLs.add(url);
        }
      } catch {}
      return url;
    };
    const nativeRevokeObjectURL = URL.revokeObjectURL;
    URL.revokeObjectURL = function (url) {
      try {
        blobURLSources.delete(String(url));
        mseURLs.delete(String(url));
      } catch {}
      return nativeRevokeObjectURL.apply(this, arguments);
    };
  } catch {}

  // The content script collects whatever happened before it loaded, then
  // receives live batches. Only its collect messages are answered.
  window.addEventListener("message", (event) => {
    try {
      if (event.source !== window) return;
      const data = event.data;
      if (!data || data.source !== CS_SOURCE || data.v !== 1) return;
      if (data.type === "collect") {
        flush();
        post({
          source: PAGE_SOURCE,
          v: 1,
          type: "log",
          nonce: typeof data.nonce === "string" ? data.nonce.slice(0, 64) : "",
          items: [...log.values()]
        });
      }
    } catch {}
  });
})();
