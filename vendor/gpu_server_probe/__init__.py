"""Minimal, ARK-compatible GPU server probe adapter.

This package is a small, standard-library-only reimplementation of the
read-only parts of the internal ``gpu_server_probe`` utility: it lists a safe
server configuration, probes GPUs over SSH with ``nvidia-smi``, and prepares a
bounded dispatch request for review. It never launches jobs and never stores
credentials.

See ``NOTICE.md`` for attribution and ``README.md`` for the configuration
schema. ARK imports this package through ``arkstudio.gpu_probe``.
"""

__version__ = "0.1.0"

from . import config, dispatch, probe, queue, ssh  # noqa: E402,F401  (public submodules)

__all__ = ["config", "dispatch", "probe", "queue", "ssh"]
