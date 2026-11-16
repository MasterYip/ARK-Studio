"""Python tests (the legacy suite of the frozen Python server and the contract tools)."""
import os
import sys
from pathlib import Path

# The frozen Python server lives in ark_studio/python/ (import name arkstudio):
# importable here and in child processes (python -m arkstudio).
_ARK_PY = str(Path(__file__).resolve().parents[2] / "ark_studio" / "python")
if _ARK_PY not in sys.path:
    sys.path.insert(0, _ARK_PY)
if _ARK_PY not in os.environ.get("PYTHONPATH", "").split(os.pathsep):
    os.environ["PYTHONPATH"] = os.pathsep.join(p for p in (_ARK_PY, os.environ.get("PYTHONPATH", "")) if p)
