# ARK Studio Documentation

Start with the [project README](../README.md) for installation and a workspace
overview. The guides below cover configuration, integrations and development.

## Running ARK

| Guide | Contents |
| --- | --- |
| [Cross-platform operation](operations/cross-platform.md) | Windows, Linux and macOS setup, processes and external applications |
| [Install ARK Studio](guide/install.md) | Desktop app downloads per OS, first run, unsigned-build warnings, data folders, uninstall; ark-server packages |
| [Desktop app](guide/desktop.md) | One window for this computer and your servers, tray, settings, desktop bridge |
| [Desktop links](guide/desktop-link.md) | A server's projects in ARK Studio: Link this desktop, token scope, revoking |
| [Mobile and remote access](guide/mobile.md) | Android builds, APKs, PWA, pairing, HTTPS and tunnels |
| [Accounts and access](operations/accounts-and-access.md) | Organization roles, project permissions and personal configuration |
| [Server operations](operations/server-operations.md) | Native persistent configuration, systemd, restart, backup and restore |
| [Shared project setup](operations/multi-user-setup.md) | Register repositories, environments, servers and storage locations |
| [Home workspace](guide/home-overview.md) | Research pipeline, activity and decision workflow |
| [View plugins](guide/plugins.md) | Built-in views as plugins; import, permissions and the sandbox of user plugins |
| [Appearance and text](guide/appearance.md) | Paired light/dark palettes, high contrast and text scaling |
| [Topics](guide/topics.md) | Nodes that hold a curated subgraph: rules, narrative order, chosen links and API |
| [Markdown bundles](guide/markdown-bundles.md) | Export selected nodes with media as linked Markdown for VS Code and Obsidian |
| [Session context](guide/session-context.md) | Read, continue and manage internal and uploaded agent sessions in Runs |
| [Project export](guide/project-export.md) | Download a restorable project ZIP with its database, text history and internal attachments |
| [Node text history](guide/node-history.md) | Opt-in Git versions of selected nodes, text diffs, restore and API |

## Agents and Resources

| Guide | Contents |
| --- | --- |
| [Providers and worktrees](agents/providers.md) | CLI discovery, models, authentication and isolated checkouts |
| [DeepSeek Harness](agents/dsh.md) | DSH installation and adapter configuration |
| [External agents](agents/external-agent.md) | Agent access, project context and rich media |
| [Recommended agent working mode](agents/working-mode.md) | Plan in a task node, progress lines, review nodes with media, decision nodes |
| [Session context management](agents/session-context.md) | Session ids, opt-in JSONL uploads, watcher, resume and API |
| [Agent interface](agents/agent-interface.md) | Versioned task and protocol contract |
| [Skills](agents/skills.md) | Skill discovery, configuration and API |
| [Skill attribution](agents/skills-attribution.md) | Upstream sources and license notices |
| [GPU servers and queue](operations/gpu.md) | SSH probes, execution requests and resource policies |
| [Resources graph](operations/resources-graph.md) | Devices, resources, connections, status and personal-device privacy (read-only) |

## Development

> The backend is the Go server `ark-server` (`ark_studio/server/`); all backend changes are
> made in Go.

- [Go server (ark-server)](development/go-port/README.md): architecture, build, tests, adding routes and [switch-over/rollback](development/go-port/switch-over.md).
- [Releasing](development/release.md): versioning, the release workflow, signing and the updater key.
- [Development guide](development/development.md): building and testing ark-server, adding a route, the backend gate (`scripts/dev/check-backend.*`), contributions.
- [Design notes](development/design.md): data and UI architecture.
- [Plugin API](development/plugin-api.md): view plugin manifest, lifecycle, the sandbox bridge and its security model.
- [Client project cache](development/client-cache.md): offline cache, deltas, push sync and its server contract.
- [Agent messaging](development/agent-messaging.md): the update feed agents watch, experimental messages to agents, and the options for agents ARK did not start.
