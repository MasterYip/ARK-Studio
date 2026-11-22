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
    ["vscode", "cursor", "ssh-terminal", "copy-ssh", "copy-path", "ssh-setup"]);
  // Browser: no terminal, the command is copied instead; signed out: no "SSH connection…".
  assert.deepEqual(ids(openActions({ device: gpu, path: "/data/RoboKit", conn })), ["vscode", "cursor", "copy-ssh", "copy-path"]);
  // The device itself opens its home folder (needs the user for VS Code).
  assert.deepEqual(ids(openActions({ device: ark, conn, terminal: true })), ["vscode", "cursor", "ssh-terminal", "copy-ssh"]);
  assert.deepEqual(ids(openActions({ device: ark, conn: { alias: "prod" } })), ["copy-ssh"]);
  // On this computer: local editor links and the path, no SSH.
  const local = openActions({ device: laptop, path: "E:\\work\\repo", local: true, terminal: true });
  assert.deepEqual(ids(local), ["vscode", "cursor", "copy-path"]);
  assert.equal(local[0].url, "vscode://file/E:/work/repo");
  // Unknown connection: the path and a hint to set it up.
  const unknown = openActions({ device: laptop, path: "/home/ana/x", canSave: true });
  assert.deepEqual(ids(unknown), ["copy-path", "ssh-setup"]);
  assert.equal(unknown[1].known, false);
  // A Windows path on another computer: SSH without cd, no Remote-SSH folder.
  assert.deepEqual(ids(openActions({ device: laptop, path: "D:\\data", conn: { alias: "laptop" } })),
    ["copy-ssh", "copy-path"]);
  // Git hosts, phones and the like: nothing to SSH into; URLs are not paths.
  assert.deepEqual(ids(openActions({ device: gitHost, path: "git@github.com:a/b.git" })), []);
  assert.deepEqual(ids(openActions({ device: { id: "phone:1", kind: "phone" } })), []);
  // A path with a newline is never offered (nor replaced by the home folder).
  assert.deepEqual(ids(openActions({ device: gpu, path: "/a\nb", conn })), ["copy-ssh"]);
});

test("every menu string exists in both languages", () => {
  const keys = ["vscode", "cursor", "ssh_terminal", "copy_ssh", "copy_path", "copied_ssh", "copied_path", "copy_failed",
    "ssh_setup", "ssh_setup_hint", "ssh_edit", "ssh_sign_in", "more", "none"].map((k) => `resources.open.${k}`)
    .concat(["title", "lead", "from_config", "alias", "alias_ph", "host", "host_ph", "user", "user_ph", "port", "save",
      "clear", "saved", "invalid", "need_host"].map((k) => `resources.ssh.${k}`));
  for (const key of keys) {
    assert.ok(EN[key], `en ${key}`);
    assert.ok(ZH[key], `zh ${key}`);
  }
  assert.equal(ZH["resources.open.vscode"], "在 VS Code 中打开");
  assert.equal(ZH["resources.open.ssh_terminal"], "通过 SSH 打开终端");
});
