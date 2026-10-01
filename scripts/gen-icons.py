#!/usr/bin/env python3
"""Generate the placeholder PNG icons for the example profile.

Usage
-----
    python3 scripts/gen-icons.py [--out DIR] [--size N]

Defaults: writes 64x64 RGBA PNGs into ``profiles/development/icons/``. Pass
``--out`` to write somewhere else and ``--size`` to change the edge length.

What it writes
--------------
Six flat-colour placeholders, one per icon the canonical example profile is
likely to want::

    terminal.png  docker.png  git.png  obs.png  media.png  system.png

The default profile uses ``{"type": "emoji"}`` and ``{"type": "material"}``
icons, so these files are **not referenced by it**. They exist as the format
reference for the image icon type::

    "icon": { "type": "image", "value": "terminal.png" }

``value`` is a file name resolved inside the profile's own ``icons/``
directory, which is why the files live at ``profiles/development/icons/``
rather than anywhere else.

Implementation notes
--------------------
Standard library only, by design: the repository must build on a bare machine
with no package manager (ADR-0009), and a placeholder generator is not worth a
dependency. The PNG is written by hand with ``zlib`` and ``struct``: an 8-bit
RGBA truecolour image, one filter byte (0, "None") per scanline, a single
``IDAT`` chunk. That is the simplest valid PNG and every decoder accepts it.
"""

from __future__ import annotations

import argparse
import os
import struct
import sys
import zlib

# Flat RGBA colour per icon name. Distinct hues so a placeholder is
# recognisable at a glance on the deck.
ICONS: dict[str, tuple[int, int, int]] = {
    "terminal": (0x1C, 0x1C, 0x22),  # near-black, like a terminal
    "docker": (0x24, 0x8B, 0xE6),    # docker blue
    "git": (0xF0, 0x50, 0x32),       # git orange
    "obs": (0x30, 0x2E, 0x31),       # obs dark grey
    "media": (0x8B, 0x5C, 0xF6),     # violet
    "system": (0x22, 0xC5, 0x5E),    # green
}

ALPHA = 0xFF


def _png_chunk(kind: bytes, data: bytes) -> bytes:
    """Return one PNG chunk: length, type, data, CRC32 of type+data."""
    return (
        struct.pack(">I", len(data))
        + kind
        + data
        + struct.pack(">I", zlib.crc32(kind + data) & 0xFFFFFFFF)
    )


def make_png(width: int, height: int, rgb: tuple[int, int, int]) -> bytes:
    """Build a solid-colour 8-bit RGBA PNG of the given size."""
    r, g, b = rgb

    # IHDR: width, height, bit depth 8, colour type 6 (RGBA), no compression
    # method / filter method / interlace.
    ihdr = struct.pack(">IIBBBBB", width, height, 8, 6, 0, 0, 0)

    # Raw scanlines, each prefixed with filter byte 0 (None). A solid image
    # compresses to a few dozen bytes.
    pixel = bytes((r, g, b, ALPHA))
    row = b"\x00" + pixel * width
    raw = row * height

    return (
        b"\x89PNG\r\n\x1a\n"
        + _png_chunk(b"IHDR", ihdr)
        + _png_chunk(b"IDAT", zlib.compress(raw, 9))
        + _png_chunk(b"IEND", b"")
    )


def verify_png(data: bytes, width: int, height: int) -> None:
    """Cheap self-check: signature, IHDR geometry, and a decompressable IDAT.

    A generator that silently writes a corrupt file is worse than no generator,
    so the script proves its own output before declaring success.
    """
    if data[:8] != b"\x89PNG\r\n\x1a\n":
        raise ValueError("missing PNG signature")

    offset = 8
    seen_idat = False
    while offset < len(data):
        (length,) = struct.unpack(">I", data[offset : offset + 4])
        kind = data[offset + 4 : offset + 8]
        body = data[offset + 8 : offset + 8 + length]
        crc = struct.unpack(">I", data[offset + 8 + length : offset + 12 + length])[0]
        if crc != (zlib.crc32(kind + body) & 0xFFFFFFFF):
            raise ValueError(f"chunk {kind!r} has a bad CRC")
        if kind == b"IHDR":
            w, h = struct.unpack(">II", body[:8])
            if (w, h) != (width, height):
                raise ValueError(f"IHDR says {w}x{h}, expected {width}x{height}")
        elif kind == b"IDAT":
            seen_idat = True
            raw = zlib.decompress(body)
            expected = height * (1 + width * 4)
            if len(raw) != expected:
                raise ValueError(f"IDAT expands to {len(raw)} bytes, expected {expected}")
        offset += 12 + length

    if not seen_idat:
        raise ValueError("no IDAT chunk")


def main(argv: list[str] | None = None) -> int:
    repo_root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    default_out = os.path.join(repo_root, "profiles", "development", "icons")

    parser = argparse.ArgumentParser(
        description="Generate placeholder PNG icons for the example profile.",
    )
    parser.add_argument(
        "--out",
        default=default_out,
        help=f"output directory (default: {default_out})",
    )
    parser.add_argument(
        "--size",
        type=int,
        default=64,
        help="edge length in pixels (default: 64)",
    )
    args = parser.parse_args(argv)

    if args.size < 1 or args.size > 1024:
        parser.error("--size must be between 1 and 1024")

    os.makedirs(args.out, exist_ok=True)

    for name in sorted(ICONS):
        png = make_png(args.size, args.size, ICONS[name])
        verify_png(png, args.size, args.size)
        path = os.path.join(args.out, f"{name}.png")
        with open(path, "wb") as fh:
            fh.write(png)
        print(f"wrote {path} ({len(png)} bytes)")

    print(
        f"\n{len(ICONS)} placeholder icons in {args.out}.\n"
        "Reference one from a profile with:\n"
        '  "icon": { "type": "image", "value": "terminal.png" }',
        file=sys.stderr,
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
