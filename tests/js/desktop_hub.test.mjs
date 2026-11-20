// Run: node --test --experimental-vm-modules tests/js/desktop_hub.test.mjs
// The ARK Studio desktop hub as the web app sees it (desktop-hub-model.js):
// detection, the page's role, the saved-server list, the new-project
// locations and the ?ark_open_project= / ?ark_new_project=1 deep links.
import assert from "node:assert/strict";
import test from "node:test";
import {
  applyDesktopQuery, canLinkHere, desktopHub, desktopQuery, hubContext, linkThisDesktop, locationOptions,
  normalizeServers, resetHubContext, serverProjectsState, serverState,
} from "../../ark_studio/web/js/desktop-hub-model.js";

const fakeHub = (role, extra = {}) => {
  const calls = [];
  return {
    calls,
    context: () => { calls.push("context"); return Promise.resolve({ role }); },
    goLocal: () => { calls.push("goLocal"); return Promise.resolve(); },
    ...extra,
  };
};

test("browsers and older desktop apps have no hub", () => {
  assert.equal(desktopHub(undefined), null);
  assert.equal(desktopHub({ shell: "tauri", window: {} }), null); // app without the hub
  assert.equal(desktopHub({ hub: { context: () => {} } }), null); // incomplete
  const hub = fakeHub("local");
  assert.equal(desktopHub({ hub }), hub);
});

test("the page role is asked once and failures read as no context", async () => {
  const hub = fakeHub("remote");
  assert.deepEqual(await hubContext(hub), { role: "remote" });
  await hubContext(hub);
  assert.deepEqual(hub.calls, ["context"]);
  const refused = { context: () => Promise.reject(new Error("not allowed")), goLocal: () => {} };
  assert.equal(await hubContext(refused), null);
  assert.equal(await hubContext(null), null);
});

test("saved servers: only http(s) entries, no duplicates, labels kept", () => {
  const list = normalizeServers({ servers: [
    { url: "https://a.example/", label: "a.example", origin: "https://a.example" },
    { url: "https://a.example/", label: "dup" },
    { url: "javascript:alert(1)", label: "x" },
    { url: "http://10.0.0.5:8890/team/" },
    null,
  ] });
  assert.deepEqual(list.map((s) => s.url), ["https://a.example/", "http://10.0.0.5:8890/team/"]);
  assert.equal(list[1].label, "http://10.0.0.5:8890/team/");
  assert.deepEqual([list[0].linked, list[0].user], [false, null]);
  assert.deepEqual(normalizeServers({ servers: [{ url: "https://l.example/", linked: true, user: "ana", token: "arkm_x" }] }),
    [{ url: "https://l.example/", label: "https://l.example/", origin: "", linked: true, user: "ana" }], "no token passes through");
  assert.deepEqual(normalizeServers(null), []);
});

test("server state: checking until known, then online or offline", () => {
  assert.equal(serverState(undefined), "checking");
  assert.equal(serverState({ reachable: true, ark: true }), "online");
  assert.equal(serverState({ reachable: false }), "offline");
});

test("new-project locations: this computer first, then each server", () => {
  const opts = locationOptions([{ url: "https://a.example/", label: "a.example" }], "This computer");
  assert.deepEqual(opts, [{ value: "local", label: "This computer" }, { value: "https://a.example/", label: "a.example" }]);
  assert.equal(locationOptions([], "Local").length, 1);
});

test("deep links are parsed and stripped from the address", () => {
  assert.equal(desktopQuery("?view=graph"), null);
  assert.equal(desktopQuery(""), null);
  assert.deepEqual(desktopQuery("?ark_open_project=prj_1&view=graph"),
    { openProject: "prj_1", newProject: false, linkDesktop: false, search: "?view=graph" });
  assert.deepEqual(desktopQuery("?ark_new_project=1"), { openProject: null, newProject: true, linkDesktop: false, search: "" });
  assert.deepEqual(desktopQuery("?ark_new_project=0"), { openProject: null, newProject: false, linkDesktop: false, search: "" });
  assert.deepEqual(desktopQuery("?ark_link_desktop=1&a=b"), { openProject: null, newProject: false, linkDesktop: true, search: "?a=b" });
  assert.equal(desktopQuery(`?ark_open_project=${"x".repeat(500)}`).openProject.length, 200);
});

test("applyDesktopQuery opens the project or the new-project dialog once", async () => {
  const done = [];
  const history = { state: null, replaceState: (_s, _t, url) => done.push(["replace", url]) };
  const ctx = {
    currentProjectId: () => "prj_home",
    openProjectById: async (id) => done.push(["open", id]),
    openNewProject: () => done.push(["new"]),
  };
  ctx.linkDesktop = () => done.push(["link"]);
  const location = { pathname: "/ark/", search: "?ark_open_project=prj_2&ark_new_project=1&ark_link_desktop=1&x=1", hash: "#home" };
  await applyDesktopQuery(ctx, { location, history });
  assert.deepEqual(done, [["replace", "/ark/?x=1#home"], ["open", "prj_2"], ["new"], ["link"]]);

  done.length = 0;
  await applyDesktopQuery(ctx, { location: { pathname: "/", search: "?ark_open_project=prj_home", hash: "" }, history });
  assert.deepEqual(done, [["replace", "/"]], "already on that project");

  done.length = 0;
  assert.equal(await applyDesktopQuery(ctx, { location: { pathname: "/", search: "", hash: "" }, history }), null);
  assert.deepEqual(done, []);
});

test("a failed open is reported, not thrown", async () => {
  const toasts = [];
  const ctx = {
    currentProjectId: () => "",
    openProjectById: async () => { throw new Error("Project not found or not accessible."); },
    toast: (msg, kind) => toasts.push([msg, kind]),
  };
  await applyDesktopQuery(ctx, { location: { pathname: "/", search: "?ark_open_project=gone", hash: "" }, history: null });
  assert.deepEqual(toasts, [["Project not found or not accessible.", "err"]]);
});

test("a linked server's project list reads as one state", () => {
  assert.equal(serverProjectsState(null).kind, "error");
  assert.equal(serverProjectsState({ linked: false }).kind, "not_linked");
  assert.equal(serverProjectsState({ linked: false, expired: true }).kind, "expired");
  assert.equal(serverProjectsState({ linked: true, reachable: false, error: "timeout" }).kind, "offline");
  assert.equal(serverProjectsState({ linked: true, reachable: true, unsupported: true }).kind, "unsupported");
  assert.deepEqual(serverProjectsState({ linked: true, reachable: true, error: "boom" }), { kind: "error", projects: [], error: "boom" });
  const ok = serverProjectsState({ linked: true, reachable: true, canCreate: true, user: "ana", projects: [
    { id: "prj_1", name: "One", description: "d", permission: "admin" }, { id: "", name: "bad" }, null, { id: "prj_2" },
  ] });
  assert.equal(ok.kind, "projects");
  assert.deepEqual(ok.projects.map((p) => [p.id, p.name]), [["prj_1", "One"], ["prj_2", "prj_2"]]);
  assert.deepEqual([ok.canCreate, ok.user], [true, "ana"]);
});

test("Link this desktop is offered on saved, unlinked servers of a linking app", () => {
  assert.equal(canLinkHere({ role: "remote", server: "https://a/", link: 1, linked: false }), true);
  assert.equal(canLinkHere({ role: "remote", server: "https://a/", link: 1, linked: true }), false);
  assert.equal(canLinkHere({ role: "remote", server: null, link: 1, linked: false }), false, "not saved");
  assert.equal(canLinkHere({ role: "remote", server: "https://a/", linked: false }), false, "older app");
  assert.equal(canLinkHere({ role: "local", link: 1, linked: false }), false);
  assert.equal(canLinkHere(null), false);
});

test("linking hands the server's one-time code to the app and refreshes the context", async () => {
  const calls = [];
  let linked = false;
  const hub = {
    context: async () => ({ role: "remote", server: "https://a/", link: 1, linked }),
    goLocal: () => {},
    linkServer: async (code) => { calls.push(["link", code]); linked = true; return { linked: true, user: "ana" }; },
  };
  assert.equal(canLinkHere(await hubContext(hub)), true);
  const res = await linkThisDesktop(hub, async (body) => { calls.push(["code", body]); return { code: "arkp_abc" }; },
    { device: "PC" });
  assert.deepEqual(res, { linked: true, user: "ana" });
  assert.equal(calls.length, 2);
  assert.equal(calls[0][1].device, "PC");
  assert.ok(calls[0][1].signal instanceof AbortSignal, "the code request can be aborted");
  assert.deepEqual(calls[1], ["link", "arkp_abc"]);
  assert.equal(canLinkHere(await hubContext(hub)), false, "context re-read after linking");
  await assert.rejects(linkThisDesktop(hub, async () => ({})), /desktop\.link\.no_code/);
  await assert.rejects(linkThisDesktop({ context: () => {}, goLocal: () => {} }, async () => ({ code: "x" })),
    /desktop\.link\.unavailable/);
  resetHubContext(null);
});

// A fake timer: run() fires the pending timeout at once.
const fakeTimers = () => {
  const pending = new Map();
  let next = 1;
  return {
    pending,
    setTimer: (fn, ms) => { const id = next++; pending.set(id, { fn, ms }); return id; },
    clearTimer: (id) => pending.delete(id),
    run: () => { for (const [id, { fn }] of [...pending]) { pending.delete(id); fn(); } },
  };
};
const remoteHub = (linkServer) => ({
  context: async () => ({ role: "remote", server: "https://a/", link: 1, linked: false }), goLocal: () => {}, linkServer,
});

test("linking times out with a clear error when the server never answers, and aborts the request", async () => {
  const timers = fakeTimers();
  let signal = null;
  const hanging = linkThisDesktop(remoteHub(async () => ({ linked: true })), (body) => {
    signal = body.signal;
    return new Promise(() => {}); // a request that never completes
  }, { timeoutMs: 15000, ...timers });
  assert.equal([...timers.pending.values()][0].ms, 15000);
  timers.run();
  await assert.rejects(hanging, /desktop\.link\.timeout/);
  assert.equal(signal.aborted, true, "the code request is aborted");
});

test("linking times out when the app never answers the claim", async () => {
  const timers = fakeTimers();
  const p = linkThisDesktop(remoteHub(() => new Promise(() => {})), async () => ({ code: "arkp_x" }), timers);
  await new Promise((r) => setTimeout(r, 0));
  timers.run();
  await assert.rejects(p, /desktop\.link\.timeout/);
});

test("Cancel stops linking: no claim after the code arrives, and an AbortError", async () => {
  const timers = fakeTimers();
  const controller = new AbortController();
  let release;
  let claimed = false;
  const p = linkThisDesktop(remoteHub(async () => { claimed = true; return { linked: true }; }),
    () => new Promise((r) => { release = r; }), { signal: controller.signal, ...timers });
  controller.abort();
  await assert.rejects(p, (err) => err.name === "AbortError");
  release({ code: "arkp_late" });
  await new Promise((r) => setTimeout(r, 0));
  assert.equal(claimed, false, "a code that arrives after Cancel is not claimed");
  assert.equal(timers.pending.size, 0, "the timeout is cleared");
  const already = new AbortController();
  already.abort();
  await assert.rejects(linkThisDesktop(remoteHub(async () => ({})), async () => ({ code: "x" }), { signal: already.signal }),
    (err) => err.name === "AbortError");
});

test("a successful link clears its timeout; errors pass through", async () => {
  const timers = fakeTimers();
  const res = await linkThisDesktop(remoteHub(async () => ({ linked: true, user: "bo" })), async () => ({ code: "arkp_y" }), timers);
  assert.deepEqual(res, { linked: true, user: "bo" });
  assert.equal(timers.pending.size, 0);
  await assert.rejects(linkThisDesktop(remoteHub(async () => { throw new Error("refused"); }), async () => ({ code: "c" }), timers),
    /refused/);
  assert.equal(timers.pending.size, 0);
});
