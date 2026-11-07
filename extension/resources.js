// Shared classification for the page scanner and the popup's media actions.
(() => {
  const media = new Set([
    "mp4", "m4v", "webm", "flv", "mkv", "mov", "avi", "wmv", "ts",
    "mp3", "m4a", "aac", "wav", "ogg", "opus", "flac", "m3u8", "mpd"
  ]);
  const downloadable = new Set([
    ...media, "iso", "img", "dmg", "apk", "zip", "rar", "7z",
    "tar", "gz", "exe", "msi", "pdf", "doc", "docx", "xls", "xlsx"
  ]);
  // Discover text files in the resource list without changing which browser
  // downloads are automatically intercepted by the desktop application.
  const discoverable = new Set([...downloadable, "txt", "log", "csv", "json"]);
  // Response Content-Type tables for capture-time classification; the values
  // are portable filename suffixes. The service worker loads this file with
  // importScripts so every capture path shares one table.
  const playlistMime = new Map([
    ["application/vnd.apple.mpegurl", "m3u8"],
    ["application/mpegurl", "m3u8"],
    ["application/x-mpegurl", "m3u8"],
    ["audio/mpegurl", "m3u8"],
    ["audio/x-mpegurl", "m3u8"],
    ["application/dash+xml", "mpd"]
  ]);
  const directMediaMime = new Map([
    ["video/mp4", "mp4"], ["video/webm", "webm"], ["video/quicktime", "mov"],
    ["video/x-matroska", "mkv"], ["video/x-msvideo", "avi"], ["video/x-ms-wmv", "wmv"],
    ["audio/mpeg", "mp3"], ["audio/mp4", "m4a"], ["audio/aac", "aac"],
    ["audio/ogg", "ogg"], ["audio/opus", "opus"], ["audio/flac", "flac"],
    ["audio/wav", "wav"], ["audio/x-wav", "wav"], ["audio/webm", "webm"]
  ]);
  const defaultInlineFormats = "pdf, doc, docx, xls, txt, json, png, jpg, xml";

  function asFileSize(value) {
    return Number.isSafeInteger(value) && value >= 0 ? value : undefined;
  }

  function normalizeDownloadURL(value) {
    try {
      const url = new URL(value);
      return /^https?:$/.test(url.protocol) ? url.href : "";
    } catch {}
    return "";
  }

  function isCoremailAttachmentDownload(value) {
    if (typeof value !== "string" || !/^https?:\/\/[^/?#]+\/(?:js\d+|coremail)\/s\?/.test(value)) return false;
    try {
      const params = new URL(value).searchParams;
      return params.get("func") === "mbox:getMessageData" &&
        params.get("mode") === "download" && Boolean(params.get("sid"));
    } catch {
      return false;
    }
  }

  // Keep the former helper export available to already loaded content scripts.
  globalThis.openDLResourceTypes = Object.freeze({ media, downloadable, discoverable, playlistMime, directMediaMime, defaultInlineFormats, asFileSize, normalizeDownloadURL, resolveBlenderDownload: normalizeDownloadURL, isCoremailAttachmentDownload });
})();
