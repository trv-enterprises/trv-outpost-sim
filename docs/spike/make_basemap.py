#!/usr/bin/env python3
"""
Generate a dark equirectangular world basemap with country boundary lines, for
use as `globe.baseTexture` in the echarts-gl attack globe.

Renders Natural Earth admin-0 country polygons (public domain) onto a 2:1
canvas: subtle filled land on a dark ocean, with brighter border lines. Output
is a plain image the dashboard's globe component can load as its texture.

Input GeoJSON (not committed):
  curl -sL https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson/ne_110m_admin_0_countries.geojson -o /tmp/ne_countries.geojson

Run (needs Pillow):
  python3 make_basemap.py   # reads /tmp/ne_countries.geojson, writes earth_dark.png
"""
import json
import os

from PIL import Image, ImageDraw

GEOJSON = os.environ.get("NE_GEOJSON", "/tmp/ne_countries.geojson")
OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "earth_dark.png")

W, H = 4096, 2048  # 2:1 equirectangular
OCEAN = (8, 12, 24)      # near-black blue
LAND = (20, 32, 52)      # subtle dark land fill
BORDER = (70, 110, 150)  # brighter boundary line


def lonlat_to_xy(lon, lat):
    x = (lon + 180.0) / 360.0 * W
    y = (90.0 - lat) / 180.0 * H
    return (x, y)


def iter_polygons(geom):
    """Yield each ring (list of [lon,lat]) from Polygon / MultiPolygon."""
    t = geom["type"]
    if t == "Polygon":
        for ring in geom["coordinates"]:
            yield ring
    elif t == "MultiPolygon":
        for poly in geom["coordinates"]:
            for ring in poly:
                yield ring


def main():
    with open(GEOJSON) as f:
        gj = json.load(f)

    img = Image.new("RGB", (W, H), OCEAN)
    draw = ImageDraw.Draw(img)

    # pass 1: fill land
    for feat in gj["features"]:
        for ring in iter_polygons(feat["geometry"]):
            pts = [lonlat_to_xy(lon, lat) for lon, lat in ring]
            if len(pts) >= 3:
                draw.polygon(pts, fill=LAND)

    # pass 2: draw borders on top (brighter, thin)
    for feat in gj["features"]:
        for ring in iter_polygons(feat["geometry"]):
            pts = [lonlat_to_xy(lon, lat) for lon, lat in ring]
            if len(pts) >= 2:
                draw.line(pts, fill=BORDER, width=2, joint="curve")

    img.save(OUT)
    print(f"wrote {OUT} ({W}x{H})")


if __name__ == "__main__":
    main()
