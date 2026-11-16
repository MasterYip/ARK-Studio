"""Dependency-free QR Code encoder (byte mode, versions 1-10, ECC level M).

ARK only needs QR codes for short pairing URLs, so this module keeps a small,
well-tested subset of ISO/IEC 18004 instead of pulling in a third-party package
or loading a QR library from a CDN.  The output is an SVG string that the
Settings view can insert directly (the app's CSP allows inline SVG markup; no
inline script is involved).

The implementation follows the standard reference algorithm:

* byte mode (UTF-8) with an 8-bit character count for versions 1-9 and a 16-bit
  count for version 10;
* Reed-Solomon error correction over GF(256) with the 0x11D primitive
  polynomial, ECC level M;
* all eight mask patterns with the four standard penalty rules;
* BCH format/version information.

Versions above 10 are rejected: a pairing URL fits comfortably in version 5 at
level M (84 bytes), and refusing larger payloads avoids silently producing a
code that is hard to scan on a budget phone camera.
"""
from __future__ import annotations

from html import escape

# (data codeword lengths per block, error-correction codewords per block) for
# each version at ECC level M.  The lists are taken from ISO/IEC 18004 table 9.
_BLOCKS_M = {
    1: ([16], 10),
    2: ([28], 16),
    3: ([44], 26),
    4: ([32, 32], 18),
    5: ([43, 43], 24),
    6: ([27, 27, 27, 27], 16),
    7: ([31, 31, 31, 31], 18),
    8: ([38, 38, 39, 39], 22),
    9: ([36, 36, 36, 37, 37], 22),
    10: ([43, 43, 43, 43, 44], 26),
}

# Alignment-pattern centres by version (ISO/IEC 18004 annex E).
_ALIGNMENT = {
    1: [],
    2: [6, 18],
    3: [6, 22],
    4: [6, 26],
    5: [6, 30],
    6: [6, 34],
    7: [6, 22, 38],
    8: [6, 24, 42],
    9: [6, 26, 46],
    10: [6, 28, 50],
}

# ECC level M is encoded as 00 in the format information.
_ECC_M_BITS = 0b00
_FORMAT_MASK = 0x5412
_FORMAT_POLY = 0x537
_VERSION_POLY = 0x1F25

_GF_EXP = [0] * 512
_GF_LOG = [0] * 256


def _init_gf() -> None:
    value = 1
    for index in range(255):
        _GF_EXP[index] = value
        _GF_LOG[value] = index
        value <<= 1
        if value & 0x100:
            value ^= 0x11D
    for index in range(255, 512):
        _GF_EXP[index] = _GF_EXP[index - 255]


_init_gf()


def _gf_mul(a: int, b: int) -> int:
    if a == 0 or b == 0:
        return 0
    return _GF_EXP[_GF_LOG[a] + _GF_LOG[b]]


def _rs_generator(degree: int) -> list[int]:
    polynomial = [1]
    for index in range(degree):
        factor = _GF_EXP[index]
        result = [0] * (len(polynomial) + 1)
        for position, coefficient in enumerate(polynomial):
            result[position] ^= coefficient
            result[position + 1] ^= _gf_mul(coefficient, factor)
        polynomial = result
    return polynomial


def _rs_remainder(data: list[int], degree: int) -> list[int]:
    generator = _rs_generator(degree)
    remainder = list(data) + [0] * degree
    for index in range(len(data)):
        factor = remainder[index]
        if factor == 0:
            continue
        for offset, coefficient in enumerate(generator):
            remainder[index + offset] ^= _gf_mul(coefficient, factor)
    return remainder[-degree:]


def _data_capacity(version: int) -> int:
    lengths, _ = _BLOCKS_M[version]
    count_bits = 8 if version <= 9 else 16
    return (sum(lengths) * 8 - 4 - count_bits) // 8


def _choose_version(payload: bytes) -> int:
    for version in range(1, 11):
        if len(payload) <= _data_capacity(version):
            return version
    raise ValueError("payload is too large for a version 1-10 QR code at ECC M")


def _encode_data(payload: bytes, version: int) -> list[int]:
    bits: list[int] = []

    def append(value: int, length: int) -> None:
        for shift in range(length - 1, -1, -1):
            bits.append((value >> shift) & 1)

    append(0b0100, 4)  # byte mode
    count_bits = 8 if version <= 9 else 16
    append(len(payload), count_bits)
    for byte in payload:
        append(byte, 8)
    data_codewords = sum(_BLOCKS_M[version][0])
    capacity_bits = data_codewords * 8
    # Terminator is at most four zero bits; the byte boundary may add more.
    append(0, min(4, capacity_bits - len(bits)))
    while len(bits) % 8:
        bits.append(0)
    codewords = [int("".join(str(bit) for bit in bits[index:index + 8]), 2)
                 for index in range(0, len(bits), 8)]
    pads = (0xEC, 0x11)
    pad_index = 0
    while len(codewords) < data_codewords:
        codewords.append(pads[pad_index % 2])
        pad_index += 1
    return codewords


def _interleave(codewords: list[int], version: int) -> list[int]:
    lengths, ec_length = _BLOCKS_M[version]
    blocks: list[list[int]] = []
    offset = 0
    for length in lengths:
        blocks.append(codewords[offset:offset + length])
        offset += length
    ec_blocks = [_rs_remainder(block, ec_length) for block in blocks]
    result: list[int] = []
    for index in range(max(lengths)):
        for block in blocks:
            if index < len(block):
                result.append(block[index])
    for index in range(ec_length):
        for block in ec_blocks:
            result.append(block[index])
    return result


def _bch_remainder(value: int, polynomial: int) -> int:
    while value.bit_length() >= polynomial.bit_length():
        value ^= polynomial << (value.bit_length() - polynomial.bit_length())
    return value


def _format_bits(mask: int) -> int:
    data = (_ECC_M_BITS << 3) | mask
    remainder = _bch_remainder(data << 10, _FORMAT_POLY)
    return ((data << 10) | remainder) ^ _FORMAT_MASK


def _version_bits(version: int) -> int:
    remainder = _bch_remainder(version << 12, _VERSION_POLY)
    return (version << 12) | remainder


class _Matrix:
    def __init__(self, version: int):
        self.version = version
        self.size = version * 4 + 17
        self.modules = [[False] * self.size for _ in range(self.size)]
        self.functions = [[False] * self.size for _ in range(self.size)]

    def set(self, x: int, y: int, dark: bool) -> None:
        if 0 <= x < self.size and 0 <= y < self.size:
            self.modules[y][x] = bool(dark)
            self.functions[y][x] = True

    def draw_finder(self, cx: int, cy: int) -> None:
        for dy in range(-1, 8):
            for dx in range(-1, 8):
                x, y = cx + dx, cy + dy
                if not (0 <= x < self.size and 0 <= y < self.size):
                    continue
                inside = 0 <= dx <= 6 and 0 <= dy <= 6
                dark = inside and (dx in (0, 6) or dy in (0, 6)
                                   or (2 <= dx <= 4 and 2 <= dy <= 4))
                self.set(x, y, dark)

    def draw_alignment(self, cx: int, cy: int) -> None:
        for dy in range(-2, 3):
            for dx in range(-2, 3):
                dark = max(abs(dx), abs(dy)) != 1
                self.set(cx + dx, cy + dy, dark)

    def draw_patterns(self) -> None:
        size = self.size
        self.draw_finder(0, 0)
        self.draw_finder(size - 7, 0)
        self.draw_finder(0, size - 7)
        for index in range(8, size - 8):
            dark = index % 2 == 0
            self.set(6, index, dark)
            self.set(index, 6, dark)
        centres = _ALIGNMENT[self.version]
        for cy in centres:
            for cx in centres:
                # Skip the three finder corners.
                if ((cx <= 8 and cy <= 8)
                        or (cx >= size - 9 and cy <= 8)
                        or (cx <= 8 and cy >= size - 9)):
                    continue
                self.draw_alignment(cx, cy)
        # Reserve the format information modules.
        for index in range(9):
            if index != 6:
                self.set(8, index, False)
                self.set(index, 8, False)
        for index in range(8):
            self.set(size - 1 - index, 8, False)
            self.set(8, size - 1 - index, False)
        self.set(8, size - 8, True)  # dark module
        if self.version >= 7:
            for index in range(18):
                a = size - 11 + index % 3
                b = index // 3
                self.set(a, b, False)
                self.set(b, a, False)

    def place_data(self, bits: list[int]) -> None:
        size = self.size
        bit_index = 0
        right = size - 1
        while right >= 1:
            if right == 6:
                right = 5
            upward = ((right + 1) & 2) == 0
            for vertical in range(size):
                y = size - 1 - vertical if upward else vertical
                for offset in range(2):
                    x = right - offset
                    if self.functions[y][x]:
                        continue
                    dark = bit_index < len(bits) and bits[bit_index] == 1
                    self.modules[y][x] = dark
                    bit_index += 1
            right -= 2

    def apply_mask(self, mask: int) -> None:
        size = self.size
        for y in range(size):
            for x in range(size):
                if self.functions[y][x] or not _mask_bit(mask, x, y):
                    continue
                self.modules[y][x] = not self.modules[y][x]

    def draw_format(self, mask: int) -> None:
        size = self.size
        bits = _format_bits(mask)
        for index in range(15):
            dark = (bits >> index) & 1 == 1
            if index < 6:
                self.set(8, index, dark)
            elif index < 8:
                self.set(8, index + 1, dark)
            else:
                self.set(14 - index, 8, dark)
            if index < 8:
                self.set(size - 1 - index, 8, dark)
            else:
                self.set(8, size - 15 + index, dark)
        self.set(8, size - 8, True)

    def draw_version(self) -> None:
        if self.version < 7:
            return
        bits = _version_bits(self.version)
        size = self.size
        for index in range(18):
            dark = (bits >> index) & 1 == 1
            a = size - 11 + index % 3
            b = index // 3
            self.set(a, b, dark)
            self.set(b, a, dark)

    def penalty(self) -> int:
        size = self.size
        score = 0
        # Rule 1: runs of five or more same-coloured modules in rows/columns.
        for line in (self.modules, [list(column) for column in zip(*self.modules)]):
            for row in line:
                run_colour = row[0]
                run = 1
                for value in row[1:]:
                    if value == run_colour:
                        run += 1
                    else:
                        if run >= 5:
                            score += 3 + (run - 5)
                        run_colour = value
                        run = 1
                if run >= 5:
                    score += 3 + (run - 5)
        # Rule 2: 2x2 blocks of the same colour.
        for y in range(size - 1):
            for x in range(size - 1):
                value = self.modules[y][x]
                if (self.modules[y][x + 1] == value
                        and self.modules[y + 1][x] == value
                        and self.modules[y + 1][x + 1] == value):
                    score += 3
        # Rule 3: finder-like 1:1:3:1:1 patterns with four light modules.
        pattern = (True, False, True, True, True, False, True)
        light = [False] * 4
        for line in (self.modules, [list(column) for column in zip(*self.modules)]):
            for row in line:
                for start in range(size - 6):
                    if tuple(row[start:start + 7]) != pattern:
                        continue
                    before = row[max(0, start - 4):start]
                    after = row[start + 7:start + 11]
                    if len(before) == 4 and before == light:
                        score += 40
                    if len(after) == 4 and after == light:
                        score += 40
        # Rule 4: deviation from a 50% dark ratio.
        dark = sum(1 for row in self.modules for value in row if value)
        percent = dark * 100 // (size * size)
        score += 10 * (abs(percent - 50) // 5)
        return score


def _mask_bit(mask: int, x: int, y: int) -> bool:
    if mask == 0:
        return (x + y) % 2 == 0
    if mask == 1:
        return y % 2 == 0
    if mask == 2:
        return x % 3 == 0
    if mask == 3:
        return (x + y) % 3 == 0
    if mask == 4:
        return (x // 3 + y // 2) % 2 == 0
    if mask == 5:
        return (x * y) % 2 + (x * y) % 3 == 0
    if mask == 6:
        return ((x * y) % 2 + (x * y) % 3) % 2 == 0
    if mask == 7:
        return ((x + y) % 2 + (x * y) % 3) % 2 == 0
    raise ValueError("mask must be between 0 and 7")


def encode(data: str) -> dict:
    """Encode ``data`` and return a matrix plus the chosen version/mask.

    The payload is always encoded in byte mode as UTF-8, which keeps the URL
    construction and the scanner side simple and unambiguous.
    """
    if not isinstance(data, str) or not data:
        raise ValueError("QR payload must be a non-empty string")
    payload = data.encode("utf-8")
    version = _choose_version(payload)
    data_codewords = _encode_data(payload, version)
    codewords = _interleave(data_codewords, version)
    bits: list[int] = []
    for codeword in codewords:
        for shift in range(7, -1, -1):
            bits.append((codeword >> shift) & 1)
    if len(bits) != len(codewords) * 8:
        raise AssertionError("bit stream length mismatch")

    best = None
    for mask in range(8):
        matrix = _Matrix(version)
        matrix.draw_patterns()
        matrix.place_data(bits)
        matrix.apply_mask(mask)
        matrix.draw_format(mask)
        matrix.draw_version()
        score = matrix.penalty()
        if best is None or score < best[0]:
            best = (score, matrix, mask)
    assert best is not None
    return {
        "version": version,
        "mask": best[2],
        "size": best[1].size,
        "modules": best[1].modules,
        "payload": data,
    }


def to_svg(data: str, *, border: int = 4, dark: str = "#0f141c",
           light: str = "#ffffff", title: str = "ARK mobile pairing QR code") -> str:
    """Render ``data`` as a self-contained SVG string (no external assets)."""
    if border < 0 or border > 16:
        raise ValueError("border must be between 0 and 16")
    encoded = encode(data)
    modules = encoded["modules"]
    size = encoded["size"]
    dimension = size + border * 2
    parts = [
        f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {dimension} {dimension}" '
        f'width="{dimension}" height="{dimension}" role="img" aria-label="{escape(title)}" '
        'shape-rendering="crispEdges">',
        f"<title>{escape(title)}</title>",
        f'<rect width="{dimension}" height="{dimension}" fill="{escape(light)}"/>',
    ]
    # Emit one path per row run instead of one element per module.
    for y, row in enumerate(modules):
        x = 0
        while x < size:
            if not row[x]:
                x += 1
                continue
            start = x
            while x < size and row[x]:
                x += 1
            parts.append(
                f'<path d="M{start + border} {y + border}h{x - start}v1h{start - x}z" '
                f'fill="{escape(dark)}"/>')
    parts.append("</svg>")
    return "".join(parts)


def svg_data_uri(data: str, **kwargs) -> str:
    """Convenience helper for callers that need an ``<img>``-safe data URI."""
    from base64 import b64encode

    svg = to_svg(data, **kwargs).encode("utf-8")
    return "data:image/svg+xml;base64," + b64encode(svg).decode("ascii")
