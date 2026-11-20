// Copies the ARK brand assets the bundled pages use from ark_studio/web/brand (the single
// source) into ui/brand, and checks that the sidecar binary for the target
// triple exists (scripts/release/build-server.sh --sidecar src-tauri/binaries).
import { copyFileSync, existsSync, mkdirSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const desktop = join(dirname(fileURLToPath(import.meta.url)), "..");
const brand = join(desktop, "..", "web", "brand");
const out = join(desktop, "ui", "brand");
mkdirSync(out, { recursive: true });
for (const name of ["ark-mark.svg", "ark-lockup-corner.svg", "ark-lockup-corner-dark.svg", "ark-lockup-loading.svg", "ark-lockup-loading-dark.svg"]) {
  copyFileSync(join(brand, name), join(out, name));
}
const bins = join(desktop, "src-tauri", "binaries");
const sidecars = existsSync(bins) ? readdirSync(bins).filter((n) => n.startsWith("ark-server-")) : [];
if (!sidecars.length) {
  console.warn("warning: no ark-server sidecar in src-tauri/binaries; run scripts/release/build-server.sh --no-archive --sidecar ark_studio/desktop/src-tauri/binaries <goos/goarch>");
}
