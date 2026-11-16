"""Regenerate vectors.json from CPython: python -B gen_vectors.py"""
import json
from pathlib import Path
from urllib.parse import parse_qs, quote, unquote, urlparse

targets = ["/", "/api/units?x=1#f", "//evil/x?y", "/a;b/c;d?e", "/a/b;c", "http://h:1/p?q", "/x:y/z",
           "/api/units/%E4%B8%AD", "/%zz%4", "/%ff%fe", "/%e4%b8", "/a%2Fb", "/api/units/abc%0A",
           "mailto:x", "/?a=1&a=2&b=&c&d=%20+x&=v&e=%E4%B8%AD;f=1", "/p?q=a+b%2Bc", "*", ""]
out = []
for t in targets:
    p = urlparse(t)
    out.append({"in": t, "scheme": p.scheme, "netloc": p.netloc, "path": p.path, "params": p.params,
                "query": p.query, "fragment": p.fragment, "unquoted": unquote(p.path),
                "qs": parse_qs(p.query), "quote": quote(t, safe="")})
Path(__file__).with_name("vectors.json").write_text(json.dumps(out, ensure_ascii=True), encoding="utf-8")
