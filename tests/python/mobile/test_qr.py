"""No-dependency tests for arkstudio.qr.

The QR matrix is decoded again with a separate (test-only) reader that follows
the same ISO placement rules: format information, unmasking, zig-zag data walk,
block de-interleaving and Reed-Solomon verification.  Known standard vectors
(format bits, version-7 bits and a published Reed-Solomon example) catch encoder
regressions that a self-round-trip alone would miss.
"""
from __future__ import annotations

import base64
import unittest

from arkstudio import qr


def _read_format(modules, size):
    bits = 0
    for index in range(15):
        if index < 6:
            dark = modules[index][8]
        elif index < 8:
            dark = modules[index + 1][8]
        else:
            dark = modules[8][14 - index]
        if dark:
            bits |= 1 << index
    return bits


def _function_map(version):
    matrix = qr._Matrix(version)
    matrix.draw_patterns()
    return matrix.functions, matrix.size


def _unmask(encoded, version, mask):
    modules = [row[:] for row in encoded]
    functions, size = _function_map(version)
    for y in range(size):
        for x in range(size):
            if not functions[y][x] and qr._mask_bit(mask, x, y):
                modules[y][x] = not modules[y][x]
    return modules


def _read_codewords(modules, functions, size):
    bits = []
    right = size - 1
    while right >= 1:
        if right == 6:
            right = 5
        upward = ((right + 1) & 2) == 0
        for vertical in range(size):
            y = size - 1 - vertical if upward else vertical
            for offset in range(2):
                x = right - offset
                if functions[y][x]:
                    continue
                bits.append(1 if modules[y][x] else 0)
        right -= 2
    return [int("".join(str(bit) for bit in bits[index:index + 8]), 2)
            for index in range(0, (len(bits) // 8) * 8, 8)]


def _deinterleave(codewords, version):
    lengths, ec_length = qr._BLOCKS_M[version]
    blocks = [[] for _ in lengths]
    cursor = 0
    for index in range(max(lengths)):
        for block_index, length in enumerate(lengths):
            if index < length:
                blocks[block_index].append(codewords[cursor])
                cursor += 1
    ec_blocks = [[] for _ in lengths]
    for index in range(ec_length):
        for block_index in range(len(lengths)):
            ec_blocks[block_index].append(codewords[cursor])
            cursor += 1
    assert cursor == len(codewords)
    return blocks, ec_blocks


def decode(text):
    encoded = qr.encode(text)
    version = encoded["version"]
    size = encoded["size"]
    assert size == 17 + 4 * version
    format_bits = _read_format(encoded["modules"], size) ^ 0x5412
    data = format_bits >> 10
    assert data >> 3 == 0, "ECC level must be M"
    mask = data & 7
    assert mask == encoded["mask"], f"format says mask {mask}, encoded {encoded['mask']}"
    functions, _ = _function_map(version)
    modules = _unmask(encoded["modules"], version, mask)
    codewords = _read_codewords(modules, functions, size)
    lengths, ec_length = qr._BLOCKS_M[version]
    total = sum(lengths) + ec_length * len(lengths)
    blocks, ec_blocks = _deinterleave(codewords[:total], version)
    for block, ec in zip(blocks, ec_blocks):
        assert qr._rs_remainder(block, ec_length) == ec
    data_codewords = []
    for block in blocks:
        data_codewords.extend(block)
    bit_text = "".join(f"{byte:08b}" for byte in data_codewords)
    assert bit_text[:4] == "0100", "byte mode expected"
    count_bits = 8 if version <= 9 else 16
    count = int(bit_text[4:4 + count_bits], 2)
    start = 4 + count_bits
    payload = bytes(int(bit_text[start + index * 8:start + (index + 1) * 8], 2)
                    for index in range(count))
    return payload.decode("utf-8"), encoded


class QrTests(unittest.TestCase):
    def test_known_reed_solomon_vector(self):
        # ISO/IEC 18004 / Thonky "HELLO WORLD" v1-M example.
        data = [32, 91, 11, 120, 209, 114, 220, 77, 67, 64, 236, 17, 236, 17, 236, 17]
        self.assertEqual(qr._rs_remainder(data, 10),
                         [196, 35, 39, 119, 235, 215, 231, 226, 93, 23])

    def test_format_and_version_information(self):
        # The standard format mask alone for M/mask 0, and the published v7 bits.
        self.assertEqual(qr._format_bits(0), 0x5412)
        self.assertEqual(qr._format_bits(1), 0x5125)
        self.assertEqual(qr._version_bits(7), 0x07C94)
        self.assertEqual(qr._version_bits(10), 0x0A4D3)

    def test_matrix_finders_and_quiet_zone(self):
        encoded = qr.encode("https://ark.example.net/ark/#pair=abc")
        modules = encoded["modules"]
        size = encoded["size"]
        for origin in ((0, 0), (size - 7, 0), (0, size - 7)):
            x0, y0 = origin
            self.assertTrue(modules[y0][x0])
            self.assertFalse(modules[y0 + 1][x0 + 1])
            self.assertTrue(modules[y0 + 3][x0 + 3])
        self.assertGreaterEqual(encoded["version"], 1)
        self.assertLessEqual(encoded["version"], 10)

    def test_round_trip_for_pairing_urls(self):
        cases = [
            "https://ark.example.net/ark/#pair=" + "x" * 43,
            "http://192.168.1.20:8891/#pair=" + "a" * 40,
            "http://ark-laptop.local:8891/#pair=abc",
        ]
        for text in cases:
            with self.subTest(text=text):
                decoded, encoded = decode(text)
                self.assertEqual(decoded, text)
                self.assertIn(encoded["version"], range(1, 11))

    def test_round_trip_utf8(self):
        text = "https://ark.example/研究/#pair=abc"
        decoded, _ = decode(text)
        self.assertEqual(decoded, text)

    def test_version_selection_uses_smallest_capacity(self):
        short = qr.encode("https://a/#pair=b")
        long = qr.encode("https://ark.example.net/ark/#pair=" + "z" * 43)
        self.assertLessEqual(short["version"], long["version"])
        for version in range(1, 10):
            capacity = qr._data_capacity(version)
            payload = b"a" * capacity
            self.assertEqual(qr._choose_version(payload), version)

    def test_svg_is_local_and_escaped(self):
        svg = qr.to_svg("https://ark.example/#pair=abc", title='<script>alert("x")</script>')
        self.assertTrue(svg.startswith("<svg"))
        self.assertIn("shape-rendering=\"crispEdges\"", svg)
        self.assertIn("<title>&lt;script&gt;alert(&quot;x&quot;)&lt;/script&gt;</title>", svg)
        self.assertNotIn("<script>", svg)
        self.assertNotIn("<image", svg)
        self.assertNotIn("href=", svg)
        self.assertNotIn("localhost", svg)
        uri = qr.svg_data_uri("hello")
        self.assertTrue(uri.startswith("data:image/svg+xml;base64,"))
        self.assertIn(b"<svg", base64.b64decode(uri.split(",", 1)[1]))

    def test_rejects_empty_and_oversized_payloads(self):
        with self.assertRaises(ValueError):
            qr.encode("")
        with self.assertRaises(ValueError):
            qr.encode("x" * 5000)
        with self.assertRaises(ValueError):
            qr._mask_bit(8, 0, 0)


if __name__ == "__main__":
    unittest.main()
