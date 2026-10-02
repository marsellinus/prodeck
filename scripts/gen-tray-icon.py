#!/usr/bin/env python3
"""Generate the Windows tray icon.

Usage
-----
    python3 scripts/gen-tray-icon.py [--out PATH]

Defaults to ``host/internal/tray/mobiledeck.ico``, which is embedded into the
binary by ``host/internal/tray``.

Why a script rather than a checked-in drawing tool file
-------------------------------------------------------
The tray needs a Windows .ico, which is a BMP with an AND mask and a directory
of sizes. That is a format nobody should have to open an image editor to change:
the shape below is thirty lines of arithmetic, so the icon can be regenerated
from the repository with the standard library alone, exactly like
``scripts/gen-icons.py`` does for the profile placeholders.

The drawing is the product's own mark: a rounded key with four smaller keys on
it, in the accent colour the panel uses. It is written at 16, 32 and 48 pixels so
Windows can pick the size it needs instead of scaling one bitmap down.
"""

import argparse
import os
import struct

# The accent colour of the panel's dark theme (--accent in panel.html). Kept in
# step with it by hand: an icon that is the wrong shade of blue is a small thing
# that makes the whole product look unfinished.
ACCENT = (0x4C, 0x8D, 0xFF)


def rounded(x, y, w, h, r):
    """Return the coverage of a rounded rectangle at a point, 0.0 to 1.0."""
    if x < 0 or y < 0 or x >= w or y >= h:
        return 0.0
    # Distance outside the corner circles, measured from the corner centres.
    cx = min(max(x, r), w - 1 - r)
    cy = min(max(y, r), h - 1 - r)
    dx, dy = x - cx, y - cy
    d = (dx * dx + dy * dy) ** 0.5
    if d <= r - 0.5:
        return 1.0
    if d >= r + 0.5:
        return 0.0
    return r + 0.5 - d


def render(size):
    """Render one size as a list of (b, g, r, a) rows, top row first."""
    # A 4x4 key grid inside a rounded plate, with a margin that stays readable
    # at 16 pixels.
    margin = max(1.0, size * 0.09)
    plate = size - 2 * margin
    plate_r = plate * 0.22
    gap = plate * 0.075
    cell = (plate - 3 * gap) / 4.0
    cell_r = cell * 0.28

    rows = []
    for y in range(size):
        row = []
        for x in range(size):
            a = rounded(x - margin, y - margin, plate, plate, plate_r)
            # The four keys: (column, row) in the 4x4 grid.
            for gx, gy in ((0, 0), (1, 0), (2, 0), (3, 0),
                           (0, 1), (3, 1), (0, 2), (3, 2), (0, 3), (1, 3), (2, 3), (3, 3)):
                kx = margin + gx * (cell + gap)
                ky = margin + gy * (cell + gap)
                a = max(a, rounded(x - kx, y - ky, cell, cell, cell_r))
            alpha = int(round(max(0.0, min(1.0, a)) * 255))
            # White mark on the plate would vanish on a light taskbar, so the
            # plate is the accent colour and the keys are the background: the
            # icon reads as a solid coloured square with holes, which stays
            # visible on both light and dark themes.
            if alpha:
                row.append((ACCENT[2], ACCENT[1], ACCENT[0], alpha))
            else:
                row.append((0, 0, 0, 0))
        rows.append(row)
    return rows


def bmp(rows):
    """Encode rows as the BMP payload of one ICO directory entry."""
    size = len(rows)
    # BITMAPINFOHEADER with height doubled: the XOR bitmap and the AND mask are
    # stacked in one image.
    out = struct.pack("<IiiHHIIiiII", 40, size, size * 2, 1, 32, 0,
                      size * size * 4, 0, 0, 0, 0)
    for y in range(size - 1, -1, -1):  # bottom-up
        for (b, g, r, a) in rows[y]:
            out += bytes((b, g, r, a))
    # The AND mask is unused for a 32-bit image but must be present and padded
    # to a 4-byte boundary per row.
    stride = ((size + 31) // 32) * 4
    out += b"\x00" * (stride * size)
    return out


def main():
    ap = argparse.ArgumentParser(description="Generate the Windows tray icon.")
    here = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    ap.add_argument("--out", default=os.path.join(here, "host", "internal", "tray", "mobiledeck.ico"))
    args = ap.parse_args()

    sizes = (16, 32, 48)
    images = [(s, bmp(render(s))) for s in sizes]

    header = struct.pack("<HHH", 0, 1, len(images))
    offset = len(header) + 16 * len(images)
    directory = b""
    for s, data in images:
        # 0 means 256 in the width/height byte, which none of these sizes need.
        directory += struct.pack("<BBBBHHII", s % 256, s % 256, 0, 0, 1, 32, len(data), offset)
        offset += len(data)

    os.makedirs(os.path.dirname(args.out), exist_ok=True)
    with open(args.out, "wb") as fh:
        fh.write(header + directory + b"".join(d for _, d in images))
    print("wrote %s (%d bytes, sizes %s)" % (args.out, offset, ", ".join(str(s) for s in sizes)))


if __name__ == "__main__":
    main()
