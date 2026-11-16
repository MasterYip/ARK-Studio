import { readFile } from "node:fs/promises";
import vm from "node:vm";

export async function loadGraphCore(context) {
  const icons = new vm.SourceTextModule(await readFile(new URL("../../web/js/icons.js", import.meta.url), "utf8"), { context });
  await icons.link(() => { throw new Error("Icons must remain dependency-free"); });
  const arrows = new vm.SourceTextModule(await readFile(new URL("../../web/js/arrows.js", import.meta.url), "utf8"), { context });
  await arrows.link(() => { throw new Error("Arrow settings must remain dependency-free"); });
  const core = new vm.SourceTextModule(await readFile(new URL("../../web/js/graph-core.js", import.meta.url), "utf8"), { context });
  await core.link((specifier) => (specifier === "./arrows.js" ? arrows : icons));
  await core.evaluate();
  return core;
}

// The node-graph layout module is pure and dependency-free.
export async function loadNodeGraphLayout(context) {
  const layout = new vm.SourceTextModule(await readFile(new URL("../../web/js/node-graph-layout.js", import.meta.url), "utf8"), { context });
  await layout.link(() => { throw new Error("node-graph-layout.js must remain dependency-free"); });
  await layout.evaluate();
  return layout;
}

// Card sizing shared by Mind and node layouts (pure, dependency-free).
export async function loadNodeCard(context) {
  const card = new vm.SourceTextModule(await readFile(new URL("../../web/js/node-card.js", import.meta.url), "utf8"), { context });
  await card.link(() => { throw new Error("node-card.js must remain dependency-free"); });
  await card.evaluate();
  return card;
}

// Sprite cache for cards and labels (dependency-free).
export async function loadGraphSprites(context) {
  const sprites = new vm.SourceTextModule(await readFile(new URL("../../web/js/graph-sprites.js", import.meta.url), "utf8"), { context });
  await sprites.link(() => { throw new Error("graph-sprites.js must remain dependency-free"); });
  await sprites.evaluate();
  return sprites;
}

// Mind layout (imports node-card.js).
export async function loadMindLayout(context, nodeCard) {
  const layout = new vm.SourceTextModule(await readFile(new URL("../../web/js/mind-layout.js", import.meta.url), "utf8"), { context });
  await layout.link(() => nodeCard);
  await layout.evaluate();
  return layout;
}
