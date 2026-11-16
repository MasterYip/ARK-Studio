# Contributing to ARK Studio

Thank you for helping improve ARK Studio. This file covers how to report
problems and send changes. The [development guide](docs/development/development.md)
describes the repository layout, local setup and the full test commands.

## Reporting bugs and requesting features

- Search the [existing issues](https://github.com/MasterYip/ARK-Studio/issues)
  before opening a new one.
- Use the issue templates. Include the ARK version or commit, your operating
  system and the steps that reproduce the problem.
- Remove private data from logs and screenshots: server addresses, user names,
  tokens, file paths and project content.
- Report security problems privately as described in [SECURITY.md](SECURITY.md),
  not in a public issue.

## Sending a change

1. Fork the repository and create a branch from `main`.
2. Keep each pull request focused on one change. Follow the style of the code
   around it; the server is the Go `ark-server` in `ark_studio/server/` and the
   web UI uses plain ES modules without a build step.
3. Add or update tests for the behaviour you change.
4. Run the backend gate from the repository root (Go, Python 3.10+ as the
   contract-suite runner, Node 18+):

   ```sh
   scripts/dev/check-backend.sh            # or scripts/dev/check-backend.ps1
   ```

5. Update the relevant guide under `docs/` when user-visible behaviour changes,
   and add a line to the `Unreleased` section of [CHANGELOG.md](CHANGELOG.md).
6. Open a pull request and fill in the template.

## Licence

By contributing you agree that your contribution is licensed under the
[MIT License](LICENSE) that covers this project.
