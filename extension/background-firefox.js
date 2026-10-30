// Firefox entry for the background service worker. Firefox runs background
// "scripts" as a module context, where importScripts is unavailable, so the
// shared tables and localization load here as ordered scripts before the
// worker logic in background.js.
import "./resources.js";
import "./i18n.js";
import "./background.js";
