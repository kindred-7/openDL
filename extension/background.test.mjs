import assert from "node:assert/strict";
import { randomUUID, webcrypto } from "node:crypto";
import { readFile } from "node:fs/promises";
import test from "node:test";
import vm from "node:vm";

// Exercise the real worker with browser APIs replaced by in-memory stubs.
// This file is not part of the extension manifest and performs no network I/O.
const source = await readFile(new URL("./background.js", import.meta.url), "utf8");
const shared = await readFile(new URL("./resources.js", import.meta.url), "utf8");
const localization = await readFile(new URL("./i18n.js", import.meta.url), "utf8");
const messages = JSON.parse(await readFile(new URL("./_locales/zh_CN/messages.json", import.meta.url), "utf8"));
const script = new vm.Script(source);
const payload = { url: "https://example.test/file", filename: "", mediaMode: "", queue: "", pageUrl: "https://example.test/", title: "Page" };

async function worker(storage = new Map()) {
  const event = () => ({ addListener() {} });
  const context = vm.createContext({
    URL, TextEncoder, setTimeout, clearTimeout, queueMicrotask,
    importScripts(...names) {
      for (const name of names) {
        const sources = { "resources.js": shared, "i18n.js": localization };
        if (!(name in sources)) throw new Error("unexpected importScripts: " + name);
        vm.runInContext(sources[name], context);
      }
    },
    crypto: { subtle: webcrypto.subtle, randomUUID },
    payload: { ...payload },
    chrome: {
      i18n: {
        getMessage(key, values = []) {
          return (messages[key]?.message || "").replace(/\$ARG(\d+)\$/g, (_, index) => String(values[Number(index) - 1] ?? ""));
        },
        getUILanguage() { return "zh-CN"; }
      },
      runtime: {
        onMessage: event(),
        connectNative() {
          let listener;
          return {
            onMessage: { addListener(fn) { listener = fn; } },
            onDisconnect: event(),
            disconnect() {},
            postMessage(msg) { queueMicrotask(() => listener({ ok: true, requestId: msg.requestId, capabilities: ["idempotent-submission"] })); }
          };
        }
      },
      storage: { session: {
        async get(key) { return { [key]: structuredClone(storage.get(key)) }; },
        async set(values) { for (const [key, value] of Object.entries(values)) storage.set(key, structuredClone(value)); },
        async remove(key) { storage.delete(key); }
      } },
      alarms: { create() {}, onAlarm: event() },
      downloads: { onCreated: event() },
      webRequest: { onHeadersReceived: event() },
      webNavigation: { onCommitted: event() },
      tabs: { onRemoved: event() }
    }
  });
  script.runInContext(context);
  await vm.runInContext("connecting", context);
  return {
    storage,
    async run(code, values = {}) { Object.assign(context, values); return vm.runInContext(code, context); }
  };
}

const unavailable = { ok: false, unavailable: true, reason: "disconnected" };

test("a known rejection can release the first attempt for browser fallback", async () => {
  const w = await worker();
  const id = await w.run("submissionID(payload, 1)");
  const ticket = await w.run("beginSubmissionAttempt(1, id)", { id });
  let reply = await w.run("guardBrowserFallback(reply, payload, 1)", { reply: unavailable });
  assert.equal(reply.deliveryUnknown, true);
  await w.run("finishSubmissionAttempt(ticket, reply)", { ticket, reply: { ok: false, reason: "rejected" } });
  reply = await w.run("guardBrowserFallback(reply, payload, 1)", { reply: unavailable });
  assert.equal(reply.unavailable, true);
  assert.equal(reply.deliveryUnknown, undefined);
});

test("a later rejection cannot clear an earlier uncertain submission", async () => {
  const w = await worker();
  const id = await w.run("submissionID(payload, 1)");
  const first = await w.run("beginSubmissionAttempt(1, id)", { id });
  await w.run("finishSubmissionAttempt(ticket, reply)", { ticket: first, reply: { ok: false, deliveryUnknown: true } });
  const second = await w.run("beginSubmissionAttempt(1, id)", { id });
  await w.run("finishSubmissionAttempt(ticket, reply)", { ticket: second, reply: { ok: false, reason: "connection refused" } });
  const reply = await w.run("guardBrowserFallback(reply, payload, 1)", { reply: unavailable });
  assert.equal(reply.deliveryUnknown, true);
  assert.equal(reply.unavailable, undefined);
});

test("accepted submission identity and fallback protection survive worker restart", async () => {
  const firstWorker = await worker();
  const id = await firstWorker.run("submissionID(payload, 1)");
  const ticket = await firstWorker.run("beginSubmissionAttempt(1, id)", { id });
  await firstWorker.run("finishSubmissionAttempt(ticket, reply)", { ticket, reply: { ok: true, id: "task" } });
  const restored = await worker(firstWorker.storage);
  assert.equal(await restored.run("submissionID(payload, 1)"), id);
  const reply = await restored.run("guardBrowserFallback(reply, payload, 1)", { reply: unavailable });
  assert.equal(reply.deliveryUnknown, true);
});

test("an earlier rejected attempt cannot erase a concurrent pending attempt", async () => {
  const w = await worker();
  const id = await w.run("submissionID(payload, 1)");
  const first = await w.run("beginSubmissionAttempt(1, id)", { id });
  await w.run("beginSubmissionAttempt(1, id)", { id });
  await w.run("finishSubmissionAttempt(ticket, reply)", { ticket: first, reply: { ok: false, reason: "rejected" } });
  const reply = await w.run("guardBrowserFallback(reply, payload, 1)", { reply: unavailable });
  assert.equal(reply.deliveryUnknown, true);
});

test("uncertain submissions do not age out while their page remains open", async () => {
  const firstWorker = await worker();
  const id = await firstWorker.run("submissionID(payload, 1)");
  await firstWorker.run("beginSubmissionAttempt(1, id)", { id });
  const entries = firstWorker.storage.get("opendl-submissions:1");
  entries[0].created = Date.now() - 2 * 86400000;
  const restored = await worker(firstWorker.storage);
  assert.equal(await restored.run("submissionID(payload, 1)"), id);
  const reply = await restored.run("guardBrowserFallback(reply, payload, 1)", { reply: unavailable });
  assert.equal(reply.deliveryUnknown, true);
});
