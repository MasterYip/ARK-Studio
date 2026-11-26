// Run: node --experimental-vm-modules --test tests/js/resource_open_model.test.mjs
// Open a resource location in VS Code / Cursor or over SSH
// (web/js/resource-open-model.js): which menu entries each kind of device
// and location gets, the editor links and SSH commands, and that bad input
// never reaches a command or a link.
import assert from "node:assert/strict";
import { test } from "node:test";
import {
  cleanConnection, remoteCommand, sshArgs, sshCommand, remoteAuthority, remoteFolder, localEditorUrl,
  remoteEditorUrl, effectiveConnection, configNames, openActions, isRemotePosixPath,
  managedAlias, identityForConfig, sshConfigEntry, sshConfigSnippet, configMatchFits, keySuggestion,
  authMode, validIdentity, keySetupCommands,
  MANAGED_BEGIN, MANAGED_END,
} from "../../ark_studio/web/js/resource-open-model.js";
import { EN } from "../../ark_studio/web/js/i18n-en.js";
import { ZH } from "../../ark_studio/web/js/i18n-zh.js";

const gpu = { id: "gpu:gpu-a", kind: "gpu-server", label: "gpu-a" };
const ark = { id: "ark", kind: "ark", label: "lab-gpu" };
const laptop = { id: "host:abc123abc123", kind: "host", label: "laptop" };
const gitHost = { id: "git:github.com", kind: "git-host", label: "github.com" };
const prod = { host: "203.0.113.7", user: "user", port: 2222 };
const ids = (list) => list.map((a) => a.id);

test("connections are checked", () => {
  assert.deepEqual(cleanConnection(prod), prod);
  assert.deepEqual(cleanConnection({ alias: " gpu-a ", port: "2222" }), { alias: "gpu-a", port: 2222 });
  assert.deepEqual(cleanConnection({ host: "fe80::1%eth0" }), { host: "fe80::1%eth0" });
  for (const bad of [null, {}, { user: "ana" }, { host: "-oProxyCommand=calc" }, { host: "a b" }, { host: "h;calc" },
    { host: "h\nx" }, { host: "h", user: "-l" }, { host: "h", user: "a@b" }, { host: "h", user: "$(id)" },
    { alias: "a*" }, { host: "h", port: 0 }, { host: "h", port: 70000 }, { host: "h", port: "22x" }, { host: "h", port: 1.5 }]) {
    assert.equal(cleanConnection(bad), null, JSON.stringify(bad));
  }
});

test("remote paths are quoted for the remote shell", () => {
  assert.equal(remoteCommand("/data/repo"), "cd -- '/data/repo' && exec ${SHELL:-/bin/sh} -l");
  assert.equal(remoteCommand("~/work/my repo"), "cd -- ~/'work/my repo' && exec ${SHELL:-/bin/sh} -l");
  assert.equal(remoteCommand("/it's/$(rm -rf ~)/`id`"), "cd -- '/it'\\''s/$(rm -rf ~)/`id`' && exec ${SHELL:-/bin/sh} -l");
  assert.equal(remoteCommand("~"), null);
  assert.equal(remoteCommand("~/"), null);
  for (const bad of ["relative", "C:\\work", "/a\nrm -rf ~", "/a\rb", "/a\u0000b", "~user/x", "/".repeat(1025)]) {
    assert.equal(isRemotePosixPath(bad), false, JSON.stringify(bad));
    assert.equal(remoteCommand(bad), null);
  }
});

test("ssh arguments match the desktop app's", () => {
  // Same cases as remote_open.rs ssh_arguments.
  assert.deepEqual(sshArgs(prod, "/data/repo"),
    ["-t", "-p", "2222", "--", "user@203.0.113.7", "cd -- '/data/repo' && exec ${SHELL:-/bin/sh} -l"]);
  assert.deepEqual(sshArgs({ host: "box", port: 22 }), ["-t", "--", "box"]);
  assert.deepEqual(sshArgs({ alias: "gpu-a", host: "10.0.0.5", user: "ana", port: 2222 }, "~"), ["-t", "--", "gpu-a"]);
  // A path with a newline is dropped, never passed on.
  assert.deepEqual(sshArgs({ alias: "gpu-a" }, "/x\nrm -rf ~"), ["-t", "--", "gpu-a"]);
  assert.equal(sshArgs({ host: "-oProxyCommand=calc" }, "/x"), null);
});

test("copied SSH commands for POSIX shells and PowerShell", () => {
  assert.equal(sshCommand(prod, "/data/repo"),
    "ssh -t -p 2222 -- user@203.0.113.7 'cd -- '\\''/data/repo'\\'' && exec ${SHELL:-/bin/sh} -l'");
  assert.equal(sshCommand(prod, "/data/repo", { shell: "powershell" }),
    "ssh -t -p 2222 -- user@203.0.113.7 'cd -- ''/data/repo'' && exec ${SHELL:-/bin/sh} -l'");
  assert.equal(sshCommand({ alias: "gpu-a" }, "/it's"),
    "ssh -t -- gpu-a 'cd -- '\\''/it'\\''\\'\\'''\\''s'\\'' && exec ${SHELL:-/bin/sh} -l'");
  assert.equal(sshCommand({ alias: "gpu-a" }), "ssh -t -- gpu-a");
  assert.equal(sshCommand({ host: "h;rm" }), null);
});

test("VS Code links: local folders and Remote-SSH", () => {
  assert.equal(localEditorUrl("E:\\repo\\My Repo"), "vscode://file/E:/repo/My%20Repo");
  assert.equal(localEditorUrl("/home/me/x#1", "cursor"), "cursor://file/home/me/x%231");
  assert.equal(localEditorUrl("~/x"), null);
  assert.equal(localEditorUrl("relative"), null);
  assert.equal(localEditorUrl("/a\nb"), null);
  assert.equal(remoteAuthority({ alias: "gpu-a", host: "h", port: 2 }), "gpu-a");
  assert.equal(remoteAuthority({ host: "box", user: "me" }), "me@box");
  const hex = remoteAuthority(prod);
  assert.equal(Buffer.from(hex, "hex").toString(), '{"hostName":"203.0.113.7","user":"user","port":2222}');
  assert.equal(remoteEditorUrl({ alias: "gpu-a" }, "/data/my repo"), "vscode://vscode-remote/ssh-remote+gpu-a/data/my%20repo");
  assert.equal(remoteEditorUrl(prod, "/data", "cursor"), `cursor://vscode-remote/ssh-remote+${hex}/data`);
  // ~ needs the user's home folder.
  assert.equal(remoteFolder("~/work", { host: "h", user: "ana" }), "/home/ana/work");
  assert.equal(remoteFolder("~", { host: "h", user: "root" }), "/root");
  assert.equal(remoteFolder("~/work", { alias: "gpu-a" }), null);
  assert.equal(remoteEditorUrl({ host: "-oX" }, "/data"), null);
});

test("~/.ssh/config aliases fill in or replace the address", () => {
  const matches = new Map([["gpu-a", "gpu-a"], ["203.0.113.7", "prod"]]);
  assert.deepEqual(effectiveConnection({ ...prod, source: "gpu-config" }, gpu, matches),
    { ...prod, alias: "prod", source: "gpu-config" });
  assert.deepEqual(effectiveConnection(null, gpu, matches), { alias: "gpu-a", source: "ssh-config" });
  assert.equal(effectiveConnection(null, laptop, matches), null);
  assert.equal(effectiveConnection({ host: "-oX" }, laptop, null), null);
  assert.deepEqual(configNames(gpu, prod), ["gpu-a", "203.0.113.7"]);
  assert.deepEqual(configNames({ label: "Ana's device" }, null), []);
});

test("menu entries per device kind and location", () => {
  const conn = { ...prod, source: "gpu-config" };
  // Remote repository with a connection, desktop app.
  assert.deepEqual(ids(openActions({ device: gpu, path: "/data/RoboKit", conn, terminal: true, canSave: true })),
    ["vscode", "cursor", "terminal", "copy-ssh", "copy-path", "ssh-setup"]);
  // Browser: "Open in terminal" copies the command; signed out: no "SSH connection…".
  const browser = openActions({ device: gpu, path: "/data/RoboKit", conn });
  assert.deepEqual(ids(browser), ["vscode", "cursor", "terminal", "copy-path"]);
  assert.equal(browser[2].copy, true);
  // The device itself opens its home folder (needs the user for VS Code).
  assert.deepEqual(ids(openActions({ device: ark, conn, terminal: true })), ["vscode", "cursor", "terminal", "copy-ssh"]);
  assert.deepEqual(ids(openActions({ device: ark, conn: { alias: "prod" } })), ["terminal"]);
  // On this computer: local editor links and the path, no SSH.
  const local = openActions({ device: laptop, path: "E:\\work\\repo", local: true, terminal: true });
  assert.deepEqual(ids(local), ["vscode", "cursor", "copy-path"]);
  assert.equal(local[0].url, "vscode://file/E:/work/repo");
  // The desktop app opens a terminal in a local folder.
  const localTerm = openActions({ device: laptop, path: "E:\\work\\repo", local: true, localTerminal: true });
  assert.deepEqual(ids(localTerm), ["vscode", "cursor", "terminal", "copy-path"]);
  assert.deepEqual([localTerm[2].local, localTerm[2].path], [true, "E:\\work\\repo"]);
  assert.deepEqual(ids(openActions({ device: laptop, path: "~/x", local: true, localTerminal: true })), ["copy-path"]);
  // Unknown connection: "Open in VS Code…" asks for it, then opens the folder.
  const unknown = openActions({ device: laptop, path: "/home/ana/x", canSave: true });
  assert.deepEqual(ids(unknown), ["vscode-setup", "copy-path"]);
  assert.equal(unknown[0].folder, "/home/ana/x");
  assert.deepEqual(openActions({ device: ark, canSave: true }).map((a) => [a.id, a.folder]), [["vscode-setup", "~"]]);
  // Signed out: shown, but disabled by the menu (canSave false); a Windows path cannot open remotely.
  assert.equal(openActions({ device: laptop, path: "/home/ana/x" })[0].canSave, false);
  assert.deepEqual(ids(openActions({ device: laptop, path: "D:\\data", canSave: true })), ["copy-path"]);
  // A Windows path on another computer: SSH without cd, no Remote-SSH folder.
  assert.deepEqual(ids(openActions({ device: laptop, path: "D:\\data", conn: { alias: "laptop" } })),
    ["terminal", "copy-path"]);
  // Git hosts, phones and the like: nothing to SSH into; URLs are not paths.
  assert.deepEqual(ids(openActions({ device: gitHost, path: "git@github.com:a/b.git" })), []);
  assert.deepEqual(ids(openActions({ device: { id: "phone:1", kind: "phone" } })), []);
  // A path with a newline is never offered (nor replaced by the home folder).
  assert.deepEqual(ids(openActions({ device: gpu, path: "/a\nb", conn })), ["terminal"]);
});

test("every menu string exists in both languages", () => {
  const keys = ["vscode", "cursor", "terminal", "copy_ssh", "copy_path", "copied_ssh", "copied_path", "copy_failed",
    "vscode_setup", "ssh_edit", "ssh_sign_in", "more", "none"].map((k) => `resources.open.${k}`)
    .concat(["title", "lead", "alias", "alias_ph", "host", "host_ph", "user", "user_ph", "port", "save",
      "clear", "saved", "invalid", "need_host"].map((k) => `resources.ssh.${k}`));
  for (const key of keys) {
    assert.ok(EN[key], `en ${key}`);
    assert.ok(ZH[key], `zh ${key}`);
  }
  assert.equal(ZH["resources.open.vscode"], "在 VS Code 中打开");
  assert.equal(ZH["resources.open.terminal"], "在终端中打开");
});

// The GPU tab's SSH / VS Code buttons (2026-11-23): VS Code asked for a
// password because ssh-remote+user@host:port carries no key; the entry
// below gives the alias a HostName, Port, User and IdentityFile.
const server = { host: "203.0.113.7", user: "user", port: 2222 };

test("managed aliases", () => {
  assert.equal(managedAlias("lab-1"), "ark-lab-1");
  assert.equal(managedAlias("GPU Box #2"), "ark-gpu-box-2");
  assert.equal(managedAlias("ark-lab"), "ark-lab");
  assert.equal(managedAlias("-x-"), "ark-x");
  for (const bad of ["", null, "  ", "###"]) assert.equal(managedAlias(bad), null, String(bad));
});

test("ssh config entry for a server", () => {
  const entry = sshConfigEntry({ alias: "ark-lab-1", ...server, identityFile: "C:\\Users\\me\\.ssh\\id_rsa_lab_1" });
  assert.equal(entry, [
    "Host ark-lab-1 203.0.113.7", "  HostName 203.0.113.7", "  Port 2222", "  User user",
    "  IdentityFile C:/Users/me/.ssh/id_rsa_lab_1", "  IdentitiesOnly yes",
  ].join("\n"));
  // No key: the agent / default keys, no IdentitiesOnly.
  assert.equal(sshConfigEntry({ alias: "ark-a", host: "gpu-a" }), "Host ark-a gpu-a\n  HostName gpu-a");
  // The snippet is the same block the desktop app writes (remote_open.rs).
  assert.equal(sshConfigSnippet([{ alias: "ark-a", host: "gpu-a", port: 2222 }]),
    `${MANAGED_BEGIN}\nHost ark-a gpu-a\n  HostName gpu-a\n  Port 2222\n${MANAGED_END}\n`);
  assert.equal(MANAGED_BEGIN, "# >>> ARK Studio managed hosts (edits inside are replaced) >>>");
  assert.equal(sshConfigSnippet([]), null);
});

test("key paths and hosts that could change the config are refused", () => {
  assert.equal(identityForConfig("~/.ssh/my key"), '"~/.ssh/my key"');
  assert.equal(identityForConfig("/home/me/.ssh/id_ed25519"), "/home/me/.ssh/id_ed25519");
  for (const bad of ["relative/key", "id_rsa", 'C:/k"ey', "/k\n  ProxyCommand calc", "//server/share/k", "", "/a\u0000b"]) {
    assert.equal(identityForConfig(bad), null, JSON.stringify(bad));
    assert.equal(sshConfigEntry({ alias: "ark-a", host: "h", identityFile: bad || "x" }), null, JSON.stringify(bad));
  }
  for (const bad of [
    { alias: "gpu-a", host: "h" }, { alias: "ark-a b", host: "h" }, { alias: "ark-a\nHost *", host: "h" },
    { alias: "ark-a", host: "a b" }, { alias: "ark-a", host: '"h"' }, { alias: "ark-a", host: "h\n  ProxyCommand calc" },
    { alias: "ark-a", host: "-oProxyCommand=calc" }, { alias: "ark-a", host: "h", user: "u x" },
    { alias: "ark-a", host: "h", user: "u'x" }, { alias: "ark-a", host: "h", port: 0 },
  ]) {
    assert.equal(sshConfigEntry(bad), null, JSON.stringify(bad));
  }
  assert.equal(sshConfigSnippet([{ alias: "ark-a", host: "h" }, { alias: "ark-b", host: "a b" }]), null);
});

test("an existing Host only counts for the same port and user", () => {
  assert.ok(configMatchFits({ alias: "prod", hostname: "203.0.113.7", port: 2222, user: "user" }, server));
  assert.ok(configMatchFits({ alias: "prod", port: 2222 }, server));
  assert.ok(!configMatchFits({ alias: "prod", hostname: "203.0.113.7" }, server));
  assert.ok(!configMatchFits({ alias: "prod", port: 2222, user: "root" }, server));
  assert.ok(configMatchFits({ alias: "box" }, { host: "box" }));
  assert.ok(!configMatchFits({ alias: "a*", port: 2222 }, server));
});

test("the key named after the server is suggested", () => {
  const keys = ["C:\\Users\\me\\.ssh\\id_rsa", "C:\\Users\\me\\.ssh\\id_rsa_lab_1"];
  assert.equal(keySuggestion(keys, "lab-1"), keys[1]);
  assert.equal(keySuggestion(keys, "ark-lab-1"), keys[1]);
  assert.equal(keySuggestion(keys, "gpu"), "");
  assert.equal(keySuggestion(keys, ""), "");
});

test("the remote editor action keeps its folder for the alias step", () => {
  const [vscode] = openActions({ device: gpu, path: "/data/x", conn: server });
  assert.equal(vscode.folder, "/data/x");
  assert.deepEqual(vscode.conn, server);
  // With the managed alias the link names only the alias.
  assert.equal(remoteEditorUrl({ ...server, alias: "ark-lab-1" }, "~"), "vscode://vscode-remote/ssh-remote+ark-lab-1/home/user");
});

test("SSH config dialog strings exist in both languages", () => {
  for (const k of ["title", "lead_desktop", "lead_browser", "key", "key_ph", "key_hint", "key_invalid", "browse", "copy",
    "copied", "where", "without", "write_and", "added_and", "no_address", "terminal_opened", "copied_ssh"]) {
    assert.ok(EN[`resources.sshcfg.${k}`], `en ${k}`);
    assert.ok(ZH[`resources.sshcfg.${k}`], `zh ${k}`);
  }
});

// With or without a password (2026-11-23): key login writes IdentityFile +
// IdentitiesOnly; password login writes neither, so ssh / VS Code prompt.
test("auth mode: explicit choice, else detected from a key path", () => {
  assert.equal(authMode({ identityFile: "C:\\k" }), "key");
  assert.equal(authMode({}), "password");
  assert.equal(authMode({ auth: "password", identityFile: "C:\\k" }), "password");
  assert.equal(authMode({ auth: "key" }), "key");
  assert.equal(authMode({ auth: "token" }), "password");
  // A recorded mode survives cleanConnection; nothing else is added.
  assert.deepEqual(cleanConnection({ ...server, auth: "password" }), { ...server, auth: "password" });
  assert.deepEqual(cleanConnection({ ...server, auth: "x", identityFile: "C:\\k" }), server);
});

test("ssh config entry with a key and with a password", () => {
  const key = "C:\\Users\\me\\.ssh\\id_rsa_lab_1";
  const withKey = sshConfigEntry({ alias: "ark-lab-1", ...server, identityFile: key, auth: "key" });
  assert.ok(withKey.endsWith("\n  IdentityFile C:/Users/me/.ssh/id_rsa_lab_1\n  IdentitiesOnly yes"));
  const pwd = sshConfigEntry({ alias: "ark-lab-1", ...server, identityFile: key, auth: "password" });
  assert.equal(pwd, "Host ark-lab-1 203.0.113.7\n  HostName 203.0.113.7\n  Port 2222\n  User user");
  assert.equal(sshConfigEntry({ alias: "ark-lab-1", ...server }), pwd);
  // Key login without a path: agent / default keys, no IdentitiesOnly.
  assert.equal(sshConfigEntry({ alias: "ark-lab-1", ...server, auth: "key" }), pwd);
  // Never a password field in the entry.
  assert.ok(!/pass/i.test(withKey + pwd));
});

test("terminal command with -i or with a password prompt", () => {
  const key = "C:\\Users\\me\\.ssh\\id_rsa_lab_1";
  assert.deepEqual(sshArgs({ ...server, identityFile: key }),
    ["-t", "-p", "2222", "-i", key, "-o", "IdentitiesOnly=yes", "--", "user@203.0.113.7"]);
  assert.deepEqual(sshArgs({ ...server, identityFile: key, auth: "password" }), ["-t", "-p", "2222", "--", "user@203.0.113.7"]);
  // An alias carries its own key.
  assert.deepEqual(sshArgs({ ...server, alias: "ark-lab-1", identityFile: key }), ["-t", "--", "ark-lab-1"]);
  assert.equal(sshCommand({ ...server, identityFile: "C:\\Users\\me\\my keys\\k" }, null, { shell: "powershell" }),
    "ssh -t -p 2222 -i 'C:\\Users\\me\\my keys\\k' -o IdentitiesOnly=yes -- user@203.0.113.7");
  for (const bad of ["-oProxyCommand=calc", "relative/k", "/k\nx", "//srv/share/k", "\\\\srv\\share\\k"]) {
    assert.equal(sshArgs({ ...server, identityFile: bad }), null, JSON.stringify(bad));
    assert.equal(validIdentity(bad), false, JSON.stringify(bad));
  }
});

test("key setup commands for a password server", () => {
  assert.deepEqual(keySetupCommands(server, "ark-lab-1"), [
    "ssh-keygen -t ed25519 -f ~/.ssh/id_ed25519_ark-lab-1",
    "ssh-copy-id -i ~/.ssh/id_ed25519_ark-lab-1.pub -p 2222 user@203.0.113.7",
  ]);
  const ps = keySetupCommands(server, "ark-lab-1", { shell: "powershell" });
  assert.equal(ps[0], 'ssh-keygen -t ed25519 -f "$env:USERPROFILE\\.ssh\\id_ed25519_ark-lab-1"');
  assert.equal(ps[1], 'Get-Content "$env:USERPROFILE\\.ssh\\id_ed25519_ark-lab-1.pub" | ssh -p 2222 user@203.0.113.7 '
    + '"umask 077; mkdir -p ~/.ssh && cat >> ~/.ssh/authorized_keys"');
  assert.equal(keySetupCommands({ host: "a b" }, "ark-x"), null);
  assert.equal(keySetupCommands(server, "ark x"), null);
});

test("login strings exist in both languages", () => {
  for (const k of ["sshcfg.auth", "sshcfg.auth_key", "sshcfg.auth_password", "sshcfg.password_note", "sshcfg.key_setup",
    "sshcfg.key_setup_lead", "sshcfg.copy_setup", "sshcfg.copied_setup", "ssh.auth", "ssh.auth_auto", "ssh.auth_key",
    "ssh.auth_password"]) {
    assert.ok(EN[`resources.${k}`], `en ${k}`);
    assert.ok(ZH[`resources.${k}`], `zh ${k}`);
  }
});
