#!/usr/bin/env python3
"""Create inspectable screenshot overlays and a small pixel-difference summary."""

from __future__ import annotations

import json
import sys
from pathlib import Path

from PIL import Image, ImageChops, ImageEnhance


def canvas(image: Image.Image, size: tuple[int, int]) -> Image.Image:
    output = Image.new("RGBA", size, "#080c0e")
    output.alpha_composite(image.convert("RGBA"))
    return output


def main() -> None:
    mock_path, app_path, output_prefix = map(Path, sys.argv[1:4])
    mock = Image.open(mock_path).convert("RGBA")
    app = Image.open(app_path).convert("RGBA")
    size = (max(mock.width, app.width), max(mock.height, app.height))
    mock_canvas = canvas(mock, size)
    app_canvas = canvas(app, size)

    overlay = Image.blend(mock_canvas, app_canvas, 0.5)
    overlay.save(f"{output_prefix}-overlay.png")

    difference = ImageChops.difference(mock_canvas, app_canvas)
    difference.save(f"{output_prefix}-diff.png")
    enhanced = ImageEnhance.Contrast(difference).enhance(3)
    enhanced.save(f"{output_prefix}-diff-enhanced.png")

    pixels = list(difference.getdata())
    changed = sum(1 for pixel in pixels if pixel[:3] != (0, 0, 0))
    mean = sum(sum(pixel[:3]) for pixel in pixels) / (len(pixels) * 3)
    print(json.dumps({"canvas": {"width": size[0], "height": size[1]}, "changedPixels": changed, "changedRatio": changed / len(pixels), "meanChannelDifference": mean}))


if __name__ == "__main__":
    main()
