#!/usr/bin/env python3
"""Generate E2 identity review exports from selected masters and draft tokens."""

import argparse
import copy
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import xml.etree.ElementTree as ET


SVG = "http://www.w3.org/2000/svg"
ET.register_namespace("", SVG)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def write_svg(root, path):
    ET.ElementTree(root).write(path, encoding="unicode")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output", type=Path)
    parser.add_argument("--overwrite", action="store_true")
    args = parser.parse_args()
    for command in ("rsvg-convert", "inkscape", "fc-match"):
        if shutil.which(command) is None:
            parser.error(f"{command} is required")
    source = Path(__file__).resolve().parent
    paths = [source / "mark.svg", source / "lockup.svg", source.parent / "tokens.json"]
    mark = ET.parse(paths[0]).getroot()
    lockup = ET.parse(paths[1]).getroot()
    reference = ET.parse(source.parent / "marks/switch-s-split.svg").getroot()
    selected_path = mark.find(f"{{{SVG}}}path").get("d")
    if selected_path != reference.find(f"{{{SVG}}}path").get("d"):
        parser.error("selected master differs from E2 reference geometry")
    if selected_path != lockup.find(f".//{{{SVG}}}path").get("d"):
        parser.error("lockup differs from selected master geometry")
    wordmark_text = lockup.find(f"{{{SVG}}}text")
    family = wordmark_text.get("font-family").split(",")[0].strip()
    style = {"500": "Medium", "600": "SemiBold"}[wordmark_text.get("font-weight")]
    font_result = subprocess.check_output([
        "fc-match", "-f", "%{family}|%{style}|%{file}", f"{family}:style={style}",
    ], text=True).split("|")
    if len(font_result) != 3 or family not in font_result[0].split(",") or style not in font_result[1].split(","):
        parser.error(f"{family} {style} is required; refusing a font substitution")
    font = Path(font_result[2])
    colors = json.loads(paths[2].read_text())["color"]
    treatments = {
        "dark": (colors["dark"]["action"], colors["dark"]["text"]),
        "light": (colors["light"]["action"], colors["light"]["text"]),
        "ivory": (colors["dark"]["text"], colors["dark"]["text"]),
        "ink": (colors["light"]["text"], colors["light"]["text"]),
    }
    names = ["identity-review.svg", "identity-review.png", "manifest.json"]
    for name in treatments:
        names.extend([f"mark-{name}.svg", f"mark-{name}.png",
                      f"lockup-{name}.svg", f"lockup-{name}-outlined.svg", f"lockup-{name}.png"])
    if not args.overwrite and any((args.output / name).exists() for name in names):
        parser.error("review output exists; choose another directory or pass --overwrite")
    args.output.mkdir(parents=True, exist_ok=True)

    def raster(svg_path, png_path, width):
        subprocess.run(["rsvg-convert", str(svg_path), "-w", str(width), "-o", str(png_path)], check=True)

    outlined = {}
    for name, (accent, ink) in treatments.items():
        symbol = copy.deepcopy(mark)
        symbol.set("color", accent)
        mark_path = args.output / f"mark-{name}.svg"
        write_svg(symbol, mark_path)
        raster(mark_path, args.output / f"mark-{name}.png", 512)
        wordmark = copy.deepcopy(lockup)
        wordmark.set("color", ink)
        wordmark.find(f"{{{SVG}}}g").set("color", accent)
        wordmark_path = args.output / f"lockup-{name}.svg"
        write_svg(wordmark, wordmark_path)
        outlined_path = args.output / f"lockup-{name}-outlined.svg"
        subprocess.run(["inkscape", str(wordmark_path), "--export-text-to-path",
                        "--export-plain-svg", f"--export-filename={outlined_path}"], check=True)
        outlined[name] = ET.parse(outlined_path).getroot()
        if outlined[name].find(f".//{{{SVG}}}text") is not None:
            raise RuntimeError("outlined lockup still contains live text")
        raster(outlined_path, args.output / f"lockup-{name}.png", 768)

    board = ET.Element(f"{{{SVG}}}svg", {
        "width": "1200", "height": "920", "viewBox": "0 0 1200 920", "role": "img",
        "aria-label": "Switchyard E2 selected mark and Sora 600 wordmark with proposed color treatments",
    })

    def element(tag, attrs, text=None, parent=None):
        node = ET.SubElement(board if parent is None else parent, f"{{{SVG}}}{tag}", attrs)
        if text is not None:
            node.text = text
        return node

    def label(x, y, text, size=15, ink=None):
        element("text", {"x": str(x), "y": str(y), "fill": ink or colors["dark"]["textSecondary"],
                        "font-family": family, "font-size": str(size)}, text)

    def place(master, x, y, width, height, color=None):
        node = copy.deepcopy(master)
        node.attrib.update({"x": str(x), "y": str(y), "width": str(width), "height": str(height)})
        if color is not None:
            node.set("color", color)
        for descendant in node.iter():
            for key in ("id", "aria-labelledby"):
                descendant.attrib.pop(key, None)
        board.append(node)

    element("rect", {"width": "1200", "height": "920", "fill": colors["dark"]["canvas"]})
    label(40, 44, "SIGNAL YARD / SWITCHYARD / SELECTED MARK E2", 13, colors["dark"]["action"])
    label(40, 94, "Split plates. Clear signal.", 34, colors["dark"]["text"])
    label(40, 127, "Selected E2 + Sora 600 / draft spacing and color treatments", 15)
    label(40, 174, "DARK / VIOLET + IVORY", 12)
    place(outlined["dark"], 85, 192, 1000, 188)
    element("rect", {"x": "40", "y": "395", "width": "1120", "height": "215", "fill": colors["light"]["canvas"]})
    label(64, 428, "LIGHT / DEEP VIOLET + INK", 12, colors["light"]["textSecondary"])
    place(outlined["light"], 85, 433, 1000, 188)
    label(40, 660, "SIZE CHECK / 16 · 24 · 32 · 64 PX", 12)
    for index, size in enumerate((16, 24, 32, 64)):
        place(mark, 44 + index * 85, 685, size, size, colors["dark"]["text"])
    label(470, 660, "PRODUCT ICON", 12)
    element("rect", {"x": "470", "y": "678", "width": "112", "height": "112", "rx": "8", "fill": colors["dark"]["surface"]})
    place(mark, 478, 686, 96, 96, colors["dark"]["action"])
    label(680, 660, "MONOCHROME", 12)
    place(outlined["ivory"], 675, 684, 450, 84)
    element("rect", {"x": "680", "y": "779", "width": "445", "height": "84", "fill": colors["light"]["canvas"]})
    place(outlined["ink"], 675, 779, 450, 84)
    label(40, 885, "The robot avatars identify the crew; E2 identifies Switchyard.", 15)
    board_path = args.output / "identity-review.svg"
    write_svg(board, board_path)
    raster(board_path, args.output / "identity-review.png", 1200)
    manifest = {
        "mark": "E2 / Split plates", "geometryStatus": "owner-selected",
        "wordmarkStatus": "owner-selected", "lockupStatus": "proposed-spacing", "sources": {str(p.relative_to(source.parent)): digest(p) for p in paths},
        "font": {"family": font_result[0], "style": font_result[1], "sha256": digest(font)},
        "exports": {name: digest(args.output / name) for name in names if name != "manifest.json"},
    }
    (args.output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print(args.output / "identity-review.png")


if __name__ == "__main__":
    main()
