"""Regenerate names.txt.gz from CPython's unicodedata (Unicode 13.0 in 3.10):
python -B gen_names.py.  CJK unified ideographs are algorithmic (names.go)."""
import gzip
import unicodedata
from pathlib import Path

names = []
for cp in range(0x110000):
    n = unicodedata.name(chr(cp), None)
    if n and not n.startswith("CJK UNIFIED IDEOGRAPH-"):
        names.append("%X;%s" % (cp, n))
Path(__file__).with_name("names.txt.gz").write_bytes(gzip.compress("\n".join(names).encode(), 9, mtime=0))
