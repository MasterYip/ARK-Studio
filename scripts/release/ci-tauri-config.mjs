#!/usr/bin/env node
// Writes the Tauri config overlay for a CI build from the signing settings
// that are present, and prints its path (for `tauri build --config <path>`).
// Every block is skipped when its secret/variable is absent, so forks and
// unsigned builds work unchanged.
//
//   ARK_UPDATER_PUBKEY            updater public key (repository variable)
//   TAURI_SIGNING_PRIVATE_KEY     updater private key (secret; read by the CLI)
//   WINDOWS_CERT_THUMBPRINT       thumbprint of the imported code-signing cert
//   AZURE_TS_ENDPOINT / AZURE_TS_ACCOUNT / AZURE_TS_PROFILE
//                                 Azure Trusted Signing (with AZURE_* client secrets)
//   APPLE_SIGNING_IDENTITY        Developer ID identity; without it macOS
//                                 bundles are ad-hoc signed ("-")
//
//   node scripts/release/ci-tauri-config.mjs <out.json> <rust target triple>
import { writeFileSync } from "node:fs";

export function overlay(env, target) {
  const conf = { bundle: {} };
  const has = (k) => typeof env[k] === "string" && env[k].trim() !== "";
  if (has("ARK_UPDATER_PUBKEY") && has("TAURI_SIGNING_PRIVATE_KEY")) {
    conf.bundle.createUpdaterArtifacts = true;
    conf.plugins = { updater: { pubkey: env.ARK_UPDATER_PUBKEY.trim() } };
  }
  if (target.includes("windows")) {
    if (has("AZURE_TS_ENDPOINT") && has("AZURE_TS_ACCOUNT") && has("AZURE_TS_PROFILE")) {
      conf.bundle.windows = {
        signCommand: `trusted-signing-cli -e ${env.AZURE_TS_ENDPOINT} -a ${env.AZURE_TS_ACCOUNT} -c ${env.AZURE_TS_PROFILE} -d "ARK Studio" %1`,
      };
    } else if (has("WINDOWS_CERT_THUMBPRINT")) {
      conf.bundle.windows = {
        certificateThumbprint: env.WINDOWS_CERT_THUMBPRINT.trim(),
        digestAlgorithm: "sha256",
        timestampUrl: env.WINDOWS_TIMESTAMP_URL || "http://timestamp.digicert.com",
      };
    }
  }
  if (target.includes("apple")) {
    conf.bundle.macOS = { signingIdentity: has("APPLE_SIGNING_IDENTITY") ? env.APPLE_SIGNING_IDENTITY.trim() : "-" };
  }
  return conf;
}

if (process.argv[1] && process.argv[1].endsWith("ci-tauri-config.mjs")) {
  const [out, target = ""] = process.argv.slice(2);
  if (!out) {
    console.error("usage: ci-tauri-config.mjs <out.json> <target triple>");
    process.exit(2);
  }
  const conf = overlay(process.env, target);
  writeFileSync(out, `${JSON.stringify(conf, null, 2)}\n`);
  const parts = [
    conf.bundle.createUpdaterArtifacts ? "updater artifacts on" : "updater off",
    conf.bundle.windows ? (conf.bundle.windows.signCommand ? "Azure Trusted Signing" : "certificate signing") : null,
    conf.bundle.macOS ? `macOS identity ${conf.bundle.macOS.signingIdentity === "-" ? "ad-hoc" : "Developer ID"}` : null,
  ].filter(Boolean);
  console.log(`${out}: ${parts.join(", ")}`);
}
