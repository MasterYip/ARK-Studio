"""CPython vectors for pyval.go / iso.go / probe.go helpers (float(), round(x, 1),
the queue's since filter, probe.parse_nvidia_smi / parse_system)."""
import json
import re
import sys
from datetime import datetime, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[4] / "python" / "vendor"))
from gpu_server_probe import probe  # noqa: E402

floats = ["1", " 2.5 ", "1_000", "1__0", "_1", "1_", "1e3", "1E-2", ".5", "5.", ".", "e3", "+-1", "-inf", "INFINITY",
          "nan", "-NaN", "１２", "１.５", "0x10", "1e400", "-1e400", "1e-400", "\u20031\u2003", "1 2", "", "  ",
          "1.5e+3", "1_000.000_1", "١٢٣", "²", "in f", "+.5e-1", "1e", "1e+", "--1", "0", "-0", "00012", "1.0_0"]
float_out = []
for text in floats:
    try:
        value = float(text)
        float_out.append({"in": text, "out": repr(value)})
    except ValueError as exc:
        float_out.append({"in": text, "err": str(exc)})
rounds = [0.05, 0.15, 0.25, 0.35, 2.675, 1000.15, 70.25, 61.125, -0.05, 1e16, 123456789.25, 0.95, 99.95, 5e-324]
round_out = [{"in": x, "out": repr(round(x, 1))} for x in rounds]


def since(value):
    text = value.replace("Z", "+00:00")
    text = re.sub(r"\.(\d{1,6})(?=[+-]\d{2}:\d{2}$)", lambda m: "." + m[1].ljust(6, "0"), text)
    try:
        stamp = datetime.fromisoformat(text)
        if stamp.tzinfo is None:
            return None
        return stamp.astimezone(timezone.utc).isoformat()
    except (ValueError, TypeError):
        return None


sinces = ["2000-01-01T00:00:00Z", "2000-01-01", "2000-01-01T00:00:00", "2000-01-01T00:00:00.5+00:00",
          "2000-01-01T00:00:00.123+05:30", "2000-01-01T24:00:00Z", "2000-01-01T12+00:00", "2000-01-01T12:30x",
          "2000-01-01 00:00:00-23:59", "2000-02-30T00:00:00Z", "2000-02-29T23:59:59.999999-00:00:01",
          "2001-02-29T00:00:00Z", "0001-01-01T00:00:00+00:00", "2000-01-01T00:00:00+24:00", "2000-01-01T00:00+01:00",
          "2000-01-01X00:00:00Z", "2000-01-01\u00e900:00:00Z", "2000-01-01T00:00:00.1234567Z", "2000-1-01T00:00:00Z",
          "2000-01-01T00:00:00+0100", "2000-01-01T00:00:00+01:00:30.5", "2000-01-01T00:00:00.123456+01:00:30.000001",
          "2000-01-01T00Z", "2000-01-01T", "garbage", "2000-01-01T00:00:00Z\n", "2000-01-01T00:00:00.5Z\n"]
since_out = [{"in": s, "out": since(s)} for s in sinces]
smi = ["0, A, 100, 50, 10, 1, 2, 3", "index, name\nNo devices were found\n x , y, 1, 1, 1\n1.5, h, 1, 1, 1",
       "3, n, [N/A], 1, 1\r\n4, m, 1e3, 2e3, -5\r5, , inf, 1, nan", "\n".join(["bad"] * 22), "6, u, 1_0, ２, 0.15"]
smi_out = [{"in": t, "out": json.dumps(probe.parse_nvidia_smi(t), ensure_ascii=False)} for t in smi]
systems = ["__ARK_LOAD__\n1 2 3\n__ARK_NPROC__\n4\n__ARK_CPU__\n%Cpu(s): 1.0 us, 90.5 id\n",
           "__ARK_LOAD__\nx 2 3\n", "__ARK_NPROC__\nabc\n", "__ARK_MEM__\n1 x 3 4\n",
           "__ARK_BUS__\n0, b0\n1, b1\nz, b2\n__ARK_PROC__\n9, b0, 100\n8, b1, [N/A]\n7, b9, 1\n__ARK_PMON__\n# x\n 0 9 C 50\n"
           " 1 8 C -\n__ARK_ENV__\n9 gpu_x\n"]
sys_out = [{"in": t, "out": json.dumps(probe.parse_system(t), ensure_ascii=False)} for t in systems]
json.dump({"float": float_out, "round": round_out, "since": since_out, "smi": smi_out, "system": sys_out},
          open(Path(__file__).with_name("pyval_vectors.json"), "w", encoding="utf-8"), ensure_ascii=False, indent=1)
