import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { runInNewContext } from "node:vm";

const source = (path) => readFileSync(new URL(path, import.meta.url), "utf8");

const helper = await import("./codex-oauth.ts").catch(() => ({}));
test("OAuth API methods attach existing app auth and never send credentials in URLs", async () => {
  const requests = [];
  const exports = {};
  const compiled = ts.transpileModule(source("./api.ts"), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText;
  runInNewContext(compiled, {
    exports,
    require: (name) =>
      name.endsWith("enabled") ? { MOCK: false } : { mockHandle: () => assert.fail("must use HTTP") },
    window: {},
    localStorage: { getItem: () => "app-session-fixture" },
    fetch: async (url, init) => {
      requests.push({ url, ...init });
      return { ok: true, status: 200, json: async () => ({ connected: false }) };
    },
  });
  await exports.api.startCodexOAuth();
  await exports.api.pollCodexOAuth();
  await exports.api.codexOAuthStatus();
  await exports.api.logoutCodexOAuth();
  assert.deepEqual(
    requests.map((r) => [r.url, r.method ?? "GET"]),
    [
      ["/api/llm/oauth/codex/start", "POST"],
      ["/api/llm/oauth/codex/poll", "POST"],
      ["/api/llm/oauth/codex/status", "GET"],
      ["/api/llm/oauth/codex", "DELETE"],
    ],
  );
  for (const request of requests) {
    assert.equal(request.headers.Authorization, "Bearer app-session-fixture");
    assert.equal(request.url.includes("?"), false);
  }
  assert.equal(requests[0].body, "{}");
  assert.equal(requests[1].body, "{}");
});

test("OAuth proxy normalization clears stale proxy overrides", () => {
  assert.equal(typeof helper.codexProxy, "function");
  assert.equal(helper.codexProxy("openai-codex", "http://old-proxy"), "");
  assert.equal(helper.codexProxy("openai", "http://old-proxy"), "http://old-proxy");
});

test("OAuth transport normalization strips stale credentials without altering key-based formats", () => {
  assert.equal(typeof helper.codexTransport, "function", "OAuth normalization helper is missing");
  assert.deepEqual(helper.codexTransport("openai-codex", "https://old", "secret", false), {
    base_url: "",
    api_key: "",
    streaming: true,
  });
  assert.deepEqual(helper.codexTransport("openai", "https://old", "secret", false), {
    base_url: "https://old",
    api_key: "secret",
    streaming: false,
  });
});

test("Device poller respects interval, expires without a request, and cancels late responses", async () => {
  assert.equal(typeof helper.pollCodexDevice, "function", "bounded device poller is missing");
  let now = 0;
  let queued;
  let calls = 0;
  const events = [];
  const options = {
    deadline: 12000,
    interval: 5000,
    now: () => now,
    schedule: (fn, ms) => {
      queued = { fn, ms };
      return 1;
    },
    unschedule: () => {
      queued = undefined;
    },
    poll: async () => {
      calls++;
      return { connected: false, pending: true };
    },
    onState: (state) => events.push(state),
    onError: (e) => events.push(e.message),
  };
  const stop = helper.pollCodexDevice(options);
  assert.equal(calls, 0);
  assert.equal(queued.ms, 5000);
  now = 5000;
  await queued.fn();
  assert.equal(calls, 1);
  now = 12000;
  await queued.fn();
  assert.equal(calls, 1);
  assert.deepEqual(events, ["expired"]);
  stop();
  let resolve;
  const stopLate = helper.pollCodexDevice({
    ...options,
    deadline: 30000,
    poll: () =>
      new Promise((r) => {
        resolve = r;
      }),
  });
  const pending = queued.fn();
  stopLate();
  resolve({ connected: true, pending: false });
  await pending;
  assert.deepEqual(events, ["expired"]);
  assert.equal(queued, undefined);
});

test("Device deadline expires even when a poll request never resolves", async () => {
  const timers = new Map();
  let id = 0;
  let now = 0;
  const events = [];
  const stop = helper.pollCodexDevice({
    deadline: 10000,
    interval: 5000,
    now: () => now,
    schedule: (fn, ms) => {
      timers.set(++id, { fn, ms });
      return id;
    },
    unschedule: (key) => timers.delete(key),
    poll: () =>
      new Promise(() => {
        /* Intentionally hung network request. */
      }),
    onState: (state) => events.push(state),
    onError: (e) => events.push(e.message),
  });
  const poll = [...timers.values()].find((timer) => timer.ms === 5000);
  now = 5000;
  void poll.fn();
  const expiry = [...timers.values()].find((timer) => timer.ms === 10000);
  assert.ok(expiry, "a separate deadline timer must bound in-flight requests");
  now = 10000;
  expiry.fn();
  assert.deepEqual(events, ["expired"]);
  stop();
  assert.equal(timers.size, 0);
});

test("Device poller stops on connection, cancellation and visible errors", async () => {
  for (const result of [
    { connected: true, pending: false },
    { connected: false, pending: false },
    new Error("denied"),
  ]) {
    let queued;
    const events = [];
    helper.pollCodexDevice({
      deadline: 10000,
      interval: 5000,
      now: () => 0,
      schedule: (fn) => {
        queued = fn;
        return 1;
      },
      unschedule: () => {
        queued = undefined;
      },
      poll: async () => {
        if (result instanceof Error) throw result;
        return result;
      },
      onState: (state) => events.push(state),
      onError: (e) => events.push(e.message),
    });
    await queued();
    const state = result.connected ? "connected" : "cancelled";
    assert.deepEqual(events, [result instanceof Error ? "denied" : state]);
    assert.equal(queued, undefined);
  }
});

test("Profile drawer integrates OAuth without key editing or implicit activation", () => {
  const page = source("../app/(main)/system/llm/page.tsx");
  assert.match(page, /<SelectItem value="openai-codex">/);
  assert.match(page, /<CodexOAuthPanel/);
  assert.match(page, /codexTransport\(format, baseUrl, apiKey, streaming\)/);
  assert.match(page, /base_url: transport.base_url/);
  assert.match(page, /api_key: transport.api_key/);
  assert.match(page, /streaming: transport.streaming/);
  assert.match(page, /!isCodex && \(\s*<div className="grid gap-2">\s*<Label htmlFor="p-api-key"/);
  const panel = source("../app/(main)/system/llm/_components/codex-oauth.tsx");
  assert.match(panel, /return \(\) =>/);
  assert.match(panel, /stopPolling.current\?\.\(\)/);
  assert.match(panel, /api.pollCodexOAuth/);
  assert.match(panel, /api.codexOAuthStatus/);
  assert.match(panel, /navigator.clipboard.writeText\(device.user_code\)/);
  assert.match(panel, /rel="noopener noreferrer"/);
  assert.doesNotMatch(panel, /window.open|activateLLMProfile|localStorage|access_token|refresh_token/);
  for (const locale of ["ko", "en", "zh", "es"]) {
    const messages = JSON.parse(source(`../../messages/${locale}.json`)).llmPage;
    assert.ok(messages.oauth.expired);
    assert.ok(messages.oauth.shared);
    assert.ok(messages.oauth.restart);
  }
});

test("Codex device auth uses protected JSON transport and explicit mutating verbs", () => {
  const api = source("./api.ts");
  assert.match(api, /startCodexOAuth:.*post<CodexOAuthStart>\("\/llm\/oauth\/codex\/start", \{\}\)/);
  assert.match(api, /pollCodexOAuth:.*post<CodexOAuthPoll>\("\/llm\/oauth\/codex\/poll", \{\}\)/);
  assert.match(api, /codexOAuthStatus:.*get<CodexOAuthStatus>\("\/llm\/oauth\/codex\/status"\)/);
  assert.match(api, /logoutCodexOAuth:.*del<CodexOAuthStatus>\("\/llm\/oauth\/codex"\)/);
  assert.match(source("./types.ts"), /"openai-codex"/);
});
