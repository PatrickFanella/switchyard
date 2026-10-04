#!/usr/bin/env python3
"""Render Signal Yard mark explorations from their SVG masters."""

import argparse
import copy
from pathlib import Path
import shutil
import subprocess
import xml.etree.ElementTree as ET


STUDIES = (
    ("D", "rail-s", "Rail S", "Paired tracks / letterform"),
    ("E", "switch-s", "Switch S", "Solid switch plates / letterform"),
    ("F", "point-blade", "Point Blade", "Turnout / compact silhouette"),
    ("G", "interlock", "Interlock", "Bent tracks / negative space"),
    ("H", "route-stamp", "Route Stamp", "Cut-out route / equipment label"),
    ("I", "three-lanes", "Three Lanes", "Staggered tracks / crossover"),
    ("J", "sy-monogram", "SY Monogram", "Industrial lettering / initials"),
    ("K", "switch-lever", "Switch Lever", "Mechanical lever / parallel rails"),
)

FOCUSED_STUDIES = (
    (
        ("D", "rail-s", "Original Rail S", "Reference / paired angular tracks"),
        ("D1", "rail-s-sharp", "Sharp points", "Narrower stance / crisp rail joints"),
        ("D2", "rail-s-open", "Open junction", "Separated bends / open center"),
        ("D3", "rail-s-soft", "Soft bends", "Continuous rails / rounded turns"),
    ),
    (
        ("E", "switch-s", "Original Switch S", "Reference / heavy angular plates"),
        ("E1", "switch-s-wide", "Wide cuts", "Larger slots / stronger negative space"),
        ("E2", "switch-s-split", "Split plates", "Two solid pieces / horizontal channel"),
        ("E3", "switch-s-continuous", "Continuous plate", "One solid route / open counters"),
    ),
    (
        ("F", "point-blade", "Original Point Blade", "Reference / rail, blade and footing"),
        ("F1", "point-blade-clean", "Clean turnout", "One connected silhouette / fewer parts"),
        ("F2", "point-blade-twin", "Twin route", "Two diagonal blades / parallel rhythm"),
        ("F3", "point-blade-split", "Split footing", "Independent upright / broad lower rail"),
    ),
)


def sheet(studies, number, total=2, round_number=2, heading=None):
    svg = ET.Element("svg", {
        "xmlns": "http://www.w3.org/2000/svg", "width": "1200",
        "height": "1100", "viewBox": "0 0 1200 1100", "role": "img",
        "aria-label": f"Signal Yard mark exploration, sheet {number}, draft monochrome constructions",
    })

    def element(tag, attributes, text=None, parent=None):
        node = ET.SubElement(svg if parent is None else parent, tag, attributes)
        if text is not None:
            node.text = text
        return node

    def label(x, y, text, size=16, ink="#B4ADBD"):
        element("text", {"x": str(x), "y": str(y), "fill": ink,
                        "font-family": "Space Grotesk, sans-serif",
                        "font-size": str(size)}, text)

    element("rect", {"width": "1200", "height": "1100", "fill": "#111017"})
    label(40, 44, f"SIGNAL YARD / SWITCHYARD / EXPLORATION {round_number:02d} / {number} OF {total}", 13, "#AD8AFF")
    label(40, 90, heading or "Track geometry. Letterforms. Mechanical signals.", 30, "#F2ECDE")
    label(40, 123, "Monochrome shape studies / all unselected", 15)
    source = Path(__file__).resolve().parent

    for index, (letter, slug, name, construction) in enumerate(studies):
        x = 40 + (index % 2) * 580
        y = 163 + (index // 2) * 430
        element("line", {"x1": str(x), "y1": str(y), "x2": str(x + 540),
                         "y2": str(y), "stroke": "#464050"})
        label(x, y + 34, f"{letter} / {name}", 23, "#F2ECDE")
        label(x, y + 62, construction, 15)
        master = ET.parse(source / f"{slug}.svg").getroot()

        def mark(mx, my, size, ink):
            attributes = {key: value for key, value in master.attrib.items()
                          if key in {"fill", "stroke", "stroke-width", "stroke-linecap", "stroke-linejoin"}}
            attributes.update({"transform": f"translate({mx} {my}) scale({size / 64})", "color": ink})
            group = element("g", attributes)
            for child in master:
                tag = child.tag.split("}")[-1]
                if tag in {"title", "desc"}:
                    continue
                node = copy.deepcopy(child)
                node.tag = tag
                group.append(node)

        mark(x + 176, y + 88, 180, "#F2ECDE")
        label(x, y + 300, "16 / 24 / 32 PX", 12)
        for i, size in enumerate((16, 24, 32)):
            mark(x + i * 50, y + 315, size, "#F2ECDE")
        element("rect", {"x": str(x + 190), "y": str(y + 305), "width": "180",
                         "height": "52", "fill": "#F2ECDE"})
        for i, size in enumerate((16, 24, 32)):
            mark(x + 205 + i * 50, y + 316, size, "#111017")
        mark(x + 4, y + 375, 30, "#AD8AFF")
        label(x + 48, y + 399, "switchyard", 27, "#F2ECDE")

    element("line", {"x1": "40", "y1": "1035", "x2": "1160", "y2": "1035", "stroke": "#464050"})
    label(40, 1071, "Draft vector masters / no finished logo selected", 14)
    return ET.ElementTree(svg)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output", type=Path)
    parser.add_argument("--overwrite", action="store_true")
    parser.add_argument("--round", type=int, choices=(2, 3), default=2,
                        help="2: broad exploration; 3: focused D/E/F variations")
    args = parser.parse_args()
    renderer = shutil.which("rsvg-convert")
    if renderer is None:
        parser.error("rsvg-convert is required")
    groups = [STUDIES[:4], STUDIES[4:]] if args.round == 2 else FOCUSED_STUDIES
    headings = [None, None] if args.round == 2 else [
        "D / Paired-track S variations.",
        "E / Solid switch-plate S variations.",
        "F / Compact turnout variations.",
    ]
    outputs = [(args.output / f"switchyard-marks-round-{args.round}-{n}.svg",
                args.output / f"switchyard-marks-round-{args.round}-{n}.png")
               for n in range(1, len(groups) + 1)]
    if not args.overwrite and any(path.exists() for pair in outputs for path in pair):
        parser.error("review output already exists; choose another directory or pass --overwrite")
    args.output.mkdir(parents=True, exist_ok=True)
    for number, (svg_path, png_path) in enumerate(outputs, 1):
        sheet(groups[number - 1], number, len(groups), args.round,
              headings[number - 1]).write(svg_path, encoding="unicode")
        subprocess.run([renderer, str(svg_path), "-o", str(png_path)], check=True)
        print(png_path)


if __name__ == "__main__":
    main()
