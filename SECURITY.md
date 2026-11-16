# Security Policy

## Supported versions

Security fixes are made on the `main` branch and included in the next release.
Older releases are not patched separately.

## Reporting a vulnerability

Please do not report security problems in public issues, discussions or pull
requests.

Report them privately through GitHub's
[private vulnerability reporting](https://github.com/MasterYip/ARK-Studio/security/advisories/new)
("Report a vulnerability" on the repository's **Security** tab). Include:

- the affected version or commit;
- a description of the problem and its impact;
- steps or a proof of concept that reproduce it;
- any suggested fix.

You should receive an acknowledgement within 7 days. We will keep you informed
while a fix is prepared and credit you in the advisory unless you prefer
otherwise.

## Scope

ARK Studio runs on your own machine or server. Reports are most useful when
they concern the ARK code in this repository: authentication and sessions,
project and file access boundaries, agent tokens, the mobile pairing flow, and
the handling of untrusted project content in the web UI. Problems in your own
deployment's network, operating system or reverse proxy are out of scope.
