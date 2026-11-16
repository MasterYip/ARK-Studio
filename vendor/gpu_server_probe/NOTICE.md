# Attribution and licensing

This directory is a **minimal, independently written adapter**, not a copy of
the upstream `gpu_server_probe` utility.

## Source

The adapter implements the read-only contract documented for the internal
`gpu_server_probe` tool referenced in the ARK task notes
(`.../util_scripts/gpu_server_probe/`, with `cli.py`, `servers_rp.yaml`,
`job_configs/` and the `gpu_jobs/` package). That utility was not published
with a license file, repository URL or copyright notice in the material ARK
was given, so:

- no upstream source code is redistributed here;
- no upstream license is invented or implied;
- no remote repository URL is invented or configured as a git submodule.

The behaviour mirrored here is limited to the documented CLI surface: list the
server configuration, probe free GPUs with `nvidia-smi`, rank them by free
memory and utilization, and prepare a bounded job dispatch request. Job
execution, screen sessions, log tailing and the job registry are intentionally
**not** reimplemented.

## If the upstream project is published later

Replace this directory with a checked-out copy or a git submodule and keep the
upstream `LICENSE`/`NOTICE` files. Suggested command once a real public URL is
known:

```bash
git submodule add <upstream-url> vendor/gpu_server_probe_upstream
```

Do not add a submodule with a guessed URL.

## Original work in this directory

The Python modules (`config.py`, `ssh.py`, `probe.py`, `dispatch.py`, `cli.py`),
the safe example YAML and the ARK adapter in `arkstudio/gpu_probe.py` are part
of ARK Studio and follow the repository's license.
