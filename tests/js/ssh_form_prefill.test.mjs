// Run: node --test tests/js/ssh_form_prefill.test.mjs
// The device SSH dialog (web/js/resource-open-model.js sshFormPrefill /
// sshFormBody): it opens with what ARK already knows about the machine
// (the shared record and the viewer's own entry, personal values first)
// and saves shared fields only for admins.
import assert from "node:assert/strict";
import { test } from "node:test";
import {
  sshFormPrefill, sshFormBody, gpuDeviceId, machineName, resolveAlias, remoteEditorUrl, sshArgs, sshCommand, remoteCommand,
  staleAliases,
} from "../../ark_studio/web/js/resource-open-model.js";
import { EN } from "../../ark_studio/web/js/i18n-en.js";
import { ZH } from "../../ark_studio/web/js/i18n-zh.js";

const ark = { id: "ark", kind: "ark", label: "deskhost7k2pq" };
const laptop = { id: "host:abc123abc123", kind: "host", label: "laptop" };
// GET /api/resources/connect on a server that runs on its own GPU server.
const data = {
  machines: { "gpu:gpu-lab": { name: "gpu-lab", host: "198.51.100.23", port: 2022, user: "user", source: "gpu-config", shared: false } },
  links: { ark: { machine: "gpu:gpu-lab", how: "server" }, "gpu:gpu-lab": { machine: "gpu:gpu-lab", how: "self" } },
  mine: {},
  suggest: {},
};

test("the ARK server's dialog opens with the linked GPU server's values", () => {
  const p = sshFormPrefill({ device: ark, data });
  assert.equal(p.machine, "gpu:gpu-lab");
  assert.equal(p.how, "server");
  assert.equal(p.own, "gpu:gpu-lab");
  assert.deepEqual(p.values, { name: "gpu-lab", host: "198.51.100.23", port: "2022", user: "user", alias: "", auth: "", keyFile: "" });
  assert.equal(p.aliasPlaceholder, "ark-gpu-lab");
  assert.deepEqual(p.options, [{ id: "gpu:gpu-lab", name: "gpu-lab" }]);
});

test("the managed alias is used when it exists, a key file and mode stay local", () => {
  // Desktop: ~/.ssh/config has Host ark-gpu-lab.
  let p = sshFormPrefill({ device: ark, data, matches: new Map([["ark-gpu-lab", "ark-gpu-lab"]]),
    localAuth: { "ark-gpu-lab": "key" }, localKeys: { "ark-gpu-lab": "C:\Users\me\.ssh\id_rsa_gpu_lab" } });
  assert.equal(p.values.alias, "ark-gpu-lab");
  assert.equal(p.values.auth, "key");
  assert.equal(p.values.keyFile, "C:\Users\me\.ssh\id_rsa_gpu_lab");
  // Browser: the entry was pasted earlier.
  p = sshFormPrefill({ device: ark, data, added: { "ark-gpu-lab": "user@198.51.100.23:2022#key" } });
  assert.equal(p.values.alias, "ark-gpu-lab");
  // Another Host for the same address.
  p = sshFormPrefill({ device: ark, data, matches: new Map([["198.51.100.23", "prodsrv"]]) });
  assert.equal(p.values.alias, "prodsrv");
  // Not there: only suggested.
  p = sshFormPrefill({ device: ark, data, matches: new Map() });
  assert.equal(p.values.alias, "");
});

test("personal values win over shared ones; an older device entry wins over the machine's", () => {
  const withMine = { ...data, mine: { "gpu:gpu-lab": { alias: "my-4090", user: "ana", auth: "password" }, ark: { port: 2200 } } };
  const p = sshFormPrefill({ device: ark, data: withMine, localAuth: { "my-4090": "key" } });
  assert.deepEqual(p.values, { name: "gpu-lab", host: "198.51.100.23", port: "2200", user: "ana", alias: "my-4090", auth: "password", keyFile: "" });
});

test("unlinked devices: own record, server suggestion, picker", () => {
  let p = sshFormPrefill({ device: laptop, data });
  assert.equal(p.machine, "");
  assert.equal(p.own, laptop.id);
  assert.deepEqual(p.values, { name: "laptop", host: "", port: "", user: "", alias: "", auth: "", keyFile: "" });
  // No machine name: no ark-<hostname> alias is made up.
  assert.equal(p.aliasPlaceholder, "gpu-a");
  assert.equal(p.managedAlias, "");
  // Picking the GPU server in the dialog shows its values.
  p = sshFormPrefill({ device: laptop, data, link: "gpu:gpu-lab" });
  assert.equal(p.machine, "gpu:gpu-lab");
  assert.equal(p.values.host, "198.51.100.23");
  // Unlinking the ARK server; the public name is offered as the host.
  p = sshFormPrefill({ device: ark, data: { ...data, suggest: { ark: { host: "ark-main.example.com" } } }, link: "" });
  assert.equal(p.machine, "");
  assert.equal(p.values.host, "ark-main.example.com");
  assert.equal(p.values.name, "deskhost7k2pq");
});

test("save body: admins write the shared record, members only their differences", () => {
  const p = sshFormPrefill({ device: ark, data });
  const form = { ...p.values, alias: "ark-gpu-lab", auth: "key" };
  assert.deepEqual(sshFormBody(p, form, { admin: true }), {
    alias: "ark-gpu-lab", auth: "key", shared: { name: "gpu-lab", host: "198.51.100.23", port: "2022", user: "user" } });
  assert.deepEqual(sshFormBody(p, { ...form, user: "ana" }, { admin: false }), { alias: "ark-gpu-lab", auth: "key", user: "ana" });
  assert.deepEqual(sshFormBody(p, form, { admin: false, personal: false }), {});
  // A changed link is saved; linking to the device itself unlinks.
  const picked = sshFormPrefill({ device: laptop, data, link: "gpu:gpu-lab" });
  assert.equal(sshFormBody(picked, picked.values, { admin: true, linkChanged: true }).link, "gpu:gpu-lab");
  const own = sshFormPrefill({ device: laptop, data, link: "" });
  assert.equal(sshFormBody(own, { host: "10.0.0.9" }, { admin: true, linkChanged: true }).link, "");
});

test("GPU device ids follow the graph", () => {
  assert.equal(gpuDeviceId("gpu-lab"), "gpu:gpu-lab");
  assert.equal(gpuDeviceId(" Lab GPU #1 "), "gpu:lab-gpu-1");
  assert.equal(gpuDeviceId("--"), "");
});

test("dialog strings exist in both languages", () => {
  for (const k of ["title", "lead", "lead_member", "machine", "machine_own", "linked_server", "linked_hostname", "linked_address",
    "shared", "personal", "name", "alias", "host", "user", "port", "auth", "key", "save", "saved"]) {
    assert.ok(EN[`resources.ssh.${k}`], `en ${k}`);
    assert.ok(ZH[`resources.ssh.${k}`], `zh ${k}`);
  }
});

// Regression (2026-11-24): "Open in VS Code" on a repository of gpu-lab opened
// ssh-remote+ark-deskhost7k2pq, an alias made from the device's hostname
// that the person's ~/.ssh/config never had (it has Host ark-gpu-lab).
test("the alias comes from the linked machine and must exist", () => {
  const conn = { host: "198.51.100.23", port: 2022, user: "user" };
  const name = machineName(ark, data);
  assert.equal(name, "gpu-lab");
  // Desktop: ~/.ssh/config has Host ark-gpu-lab for this address.
  const found = [{ name: "ark-gpu-lab", alias: "ark-gpu-lab", hostname: "198.51.100.23", port: 2022, user: "user" },
    { name: "198.51.100.23", alias: "ark-gpu-lab", hostname: "198.51.100.23", port: 2022, user: "user" }];
  assert.deepEqual(resolveAlias({ name, conn, found }), { alias: "ark-gpu-lab", known: true });
  assert.equal(remoteEditorUrl({ ...conn, alias: "ark-gpu-lab" }, "/data/RoboKit"),
    "vscode://vscode-remote/ssh-remote+ark-gpu-lab/data/RoboKit");
  // Found only by address (another Host name): that one is used.
  assert.deepEqual(resolveAlias({ name, conn, found: [{ ...found[1], alias: "prodsrv" }] }), { alias: "prodsrv", known: true });
  // Browser: the entry was pasted before.
  assert.deepEqual(resolveAlias({ name, conn, added: { "ark-gpu-lab": "user@198.51.100.23:2022#key" } }),
    { alias: "ark-gpu-lab", known: true });
  // Not there yet: the alias to write first, not known.
  assert.deepEqual(resolveAlias({ name, conn, found: [] }), { alias: "ark-gpu-lab", known: false });
  // An entry for another port does not count.
  assert.equal(resolveAlias({ name, conn, found: [{ ...found[0], port: 22 }] }).known, false);
  // No machine linked: no alias at all (user@host:port), never ark-deskhost7k2pq.
  const unlinked = { ...data, links: {} };
  assert.equal(machineName(ark, unlinked), "");
  assert.deepEqual(resolveAlias({ name: machineName(ark, unlinked), conn, found: [] }), { alias: null, known: false });
  for (const d of [data, unlinked]) {
    const p = sshFormPrefill({ device: ark, data: d });
    assert.notEqual(p.aliasPlaceholder, "ark-deskhost7k2pq");
    assert.notEqual(p.values.alias, "ark-deskhost7k2pq");
  }
  assert.match(remoteEditorUrl(conn, "/data/RoboKit"), /^vscode:\/\/vscode-remote\/ssh-remote\+[0-9a-f]+\/data\/RoboKit$/);
  // A saved alias is checked like any other: in ~/.ssh/config, or pasted / confirmed in this browser.
  assert.deepEqual(resolveAlias({ name, conn: { ...conn, alias: "my-4090" }, found: [{ ...found[0], name: "my-4090", alias: "my-4090" }] }),
    { alias: "my-4090", known: true });
  assert.deepEqual(resolveAlias({ name, conn: { ...conn, alias: "my-4090" }, added: { "my-4090": "user@198.51.100.23:2022#key" } }),
    { alias: "my-4090", known: true });
  assert.deepEqual(resolveAlias({ name, conn: { ...conn, alias: "my-4090" } }), { alias: "ark-gpu-lab", known: false });
  // An alias-only connection has nothing to fall back to.
  assert.deepEqual(resolveAlias({ name, conn: { alias: "my-4090" } }), { alias: "my-4090", known: true });
});

// One shell word back to its text: POSIX sh ('...' and \x) or PowerShell ('...' with '' for ').
function unquote(word, shell) {
  let out = "";
  let i = 0;
  while (i < word.length) {
    const ch = word[i];
    if (ch === "'") {
      i += 1;
      for (;;) {
        if (i >= word.length) throw new Error(`unterminated: ${word}`);
        if (word[i] === "'") {
          if (shell === "powershell" && word[i + 1] === "'") { out += "'"; i += 2; continue; }
          i += 1;
          break;
        }
        out += word[i];
        i += 1;
      }
    } else if (ch === "\\" && shell === "posix") {
      out += word[i + 1];
      i += 2;
    } else if (/\s/.test(ch)) {
      throw new Error(`unquoted space in ${word}`);
    } else {
      out += ch;
      i += 1;
    }
  }
  return out;
}

// "Open in terminal": ssh -t into the machine, cd into the clicked folder.
test("terminal command quotes the folder for both shells", () => {
  const conn = { host: "198.51.100.23", port: 2022, user: "user" };
  // The command the user expects (PowerShell, copied in a browser).
  assert.equal(sshCommand(conn, "/srv/arkdata/PegasusMoDye", { shell: "powershell" }),
    "ssh -t -p 2022 -- user@198.51.100.23 'cd -- ''/srv/arkdata/PegasusMoDye'' && exec ${SHELL:-/bin/sh} -l'");
  // Desktop: an argument array, the remote command as one argument.
  assert.deepEqual(sshArgs(conn, "/data/my repo"), ["-t", "-p", "2022", "--", "user@198.51.100.23",
    "cd -- '/data/my repo' && exec ${SHELL:-/bin/sh} -l"]);
  // Key login without an alias: this key only.
  assert.deepEqual(sshArgs({ ...conn, auth: "key", identityFile: "C:/Users/me/.ssh/id_rsa_gpu_lab" }, "/data/x").slice(0, 7),
    ["-t", "-p", "2022", "-i", "C:/Users/me/.ssh/id_rsa_gpu_lab", "-o", "IdentitiesOnly=yes"]);
  // With an alias: the alias carries port, user and key.
  assert.deepEqual(sshArgs({ ...conn, alias: "ark-gpu-lab" }, "/data/x"), ["-t", "--", "ark-gpu-lab",
    "cd -- '/data/x' && exec ${SHELL:-/bin/sh} -l"]);
  // Single quotes, double quotes and spaces survive both quoting levels.
  for (const folder of ["/data/it's a \"repo\"", "/data/a  b/'c'", "/data/$HOME `x`"]) {
    const remote = remoteCommand(folder);
    const inner = remote.slice("cd -- ".length, remote.indexOf(" && exec "));
    assert.equal(unquote(inner, "posix"), folder, folder);
    for (const shell of ["posix", "powershell"]) {
      const cmd = sshCommand(conn, folder, { shell });
      const prefix = "ssh -t -p 2022 -- user@198.51.100.23 ";
      assert.ok(cmd.startsWith(prefix), cmd);
      assert.equal(unquote(cmd.slice(prefix.length), shell), remote, `${shell}: ${cmd}`);
    }
  }
  // ~ paths keep the home expansion; options never pass as a path.
  assert.equal(remoteCommand("~/work dir"), "cd -- ~/'work dir' && exec ${SHELL:-/bin/sh} -l");
  assert.deepEqual(sshArgs(conn, "-oProxyCommand=x"), ["-t", "-p", "2022", "--", "user@198.51.100.23"]);
});

// Regression (2026-11-24, 20:31): the fix above was not enough.  An earlier
// dialog had remembered ark-deskhost7k2pq (the ARK server's hostname) as a
// saved / pasted alias for 198.51.100.23:2022, and that stale value still
// won.  Now it must pass the same check, and stale marks are cleaned up.
test("a stale ark-<hostname> alias never reaches VS Code", () => {
  const conn = { host: "198.51.100.23", port: 2022, user: "user" };
  const name = machineName(ark, data);
  const stale = { ...conn, alias: "ark-deskhost7k2pq" };
  const config = [{ name: "ark-gpu-lab", alias: "ark-gpu-lab", hostname: "198.51.100.23", port: 2022, user: "user" }];
  // Desktop: not in ~/.ssh/config, so the machine's alias is used.
  assert.deepEqual(resolveAlias({ name, conn: stale, found: config }), { alias: "ark-gpu-lab", known: true });
  // Browser with the old "pasted" mark for the same address: ignored; the machine alias is asked for first.
  const pastedStale = { "ark-deskhost7k2pq": "user@198.51.100.23:2022#key" };
  assert.deepEqual(resolveAlias({ name, conn, added: pastedStale }), { alias: "ark-gpu-lab", known: false });
  assert.deepEqual(resolveAlias({ name, conn: { ...conn }, added: { ...pastedStale, "ark-gpu-lab": "user@198.51.100.23:2022#key" } }),
    { alias: "ark-gpu-lab", known: true });
  // Unlinked device with a stale saved alias: user@host:port, no alias.
  assert.deepEqual(resolveAlias({ name: "", conn: stale, found: [] }), { alias: null, known: false });
  // Which remembered aliases are stale, and where their key file moves.
  const laptop2 = { id: "host:709270ec09fc", kind: "host", label: "deskhost7k2pq" };
  const map = staleAliases([ark, laptop2, { id: "gpu:gpu-lab", kind: "gpu-server", label: "gpu-lab" }],
    { ...data, links: { ...data.links, [laptop2.id]: { machine: "gpu:gpu-lab", how: "server" } } });
  assert.deepEqual([...map], [["ark-deskhost7k2pq", "ark-gpu-lab"], ["ark-709270ec09fc", "ark-gpu-lab"]]);
  assert.ok(!map.has("ark-gpu-lab"));
});

// Regression (2026-11-26 12:30): VS Code opened the direct form
// ssh-remote+{"hostName": <address>, "user": "user", "port": <port>} and got
// "Permission denied (publickey)": the person's key is not a default key name
// and ssh matches Host patterns against the typed name, so the Host
// ark-<machine> entry never applied to the bare address.  Some opens worked
// (with the alias), some did not (the direct form).
test("key login never opens the bare address without a confirmed entry", async () => {
  const { openPlan, isDefaultKey, sshConfigEntry } = await import("../../ark_studio/web/js/resource-open-model.js");
  const conn = { host: "198.51.100.23", port: 2022, user: "user" };
  // The entry names the address too, so the direct form picks up the key.
  const entry = sshConfigEntry({ alias: "ark-gpu-lab", ...conn, identityFile: "C:/Users/me/.ssh/id_rsa_gpu_lab", auth: "key" });
  assert.equal(entry.split("\n")[0], "Host ark-gpu-lab 198.51.100.23");
  assert.ok(entry.includes("IdentityFile C:/Users/me/.ssh/id_rsa_gpu_lab"));
  // The 12:30 case: browser, alias not confirmed, key login with a non-default key.
  const unconfirmed = resolveAlias({ name: "gpu-lab", conn, added: {} });
  assert.deepEqual(unconfirmed, { alias: "ark-gpu-lab", known: false });
  assert.equal(openPlan({ resolved: unconfirmed, auth: "key", keyFile: "C:/Users/me/.ssh/id_rsa_gpu_lab" }), "confirm");
  // Unknown login mode: treated as key login.
  assert.equal(openPlan({ resolved: unconfirmed }), "confirm");
  // Once the person confirmed the entry ("Added, open"): the alias, every time.
  const confirmed = resolveAlias({ name: "gpu-lab", conn, added: { "ark-gpu-lab": "user@198.51.100.23:2022#key" } });
  assert.equal(openPlan({ resolved: confirmed, auth: "key", keyFile: "C:/Users/me/.ssh/id_rsa_gpu_lab" }), "alias");
  // The bare address only for password login or a key ssh tries by itself.
  assert.equal(openPlan({ resolved: unconfirmed, auth: "password" }), "direct");
  assert.equal(openPlan({ resolved: unconfirmed, auth: "key", keyFile: "C:\\Users\\me\\.ssh\\id_ed25519" }), "direct");
  assert.ok(isDefaultKey("~/.ssh/id_rsa") && !isDefaultKey("~/.ssh/id_rsa_gpu_lab") && !isDefaultKey("id_rsa"));
  // Desktop app: write the entry (it asks the first time), then the alias.
  assert.equal(openPlan({ resolved: unconfirmed, desktop: true, auth: "key" }), "write");
  // No machine name: no alias to use.
  assert.equal(openPlan({ resolved: { alias: null, known: false }, auth: "key" }), "direct");
});
