import sys
import json
import os
import io

import vtracer


def convert_image_to_svg(image_path, out_path, params):
    vtracer.convert_image_to_svg_py(
        image_path,
        out_path,
        colormode=params.get("colormode", "color"),
        mode=params.get("mode", "spline"),
        filter_speckle=int(params.get("filter_speckle", 4)),
        color_precision=int(params.get("color_precision", 6)),
        corner_threshold=int(float(params.get("corner_threshold", 60))),
        path_precision=params.get("path_precision", 8),
    )


def build_sketch_file(svg_path, out_path):
    """Wrap an SVG into a minimal .sketch container (zip of JSON)."""
    import zipfile

    with open(svg_path, "r", encoding="utf-8") as f:
        svg = f.read()

    document = {
        "_class": "document",
        "do_objectID": "00000000000000000000000000000000",
        "pages": [
            {
                "_class": "page",
                "do_objectID": "11111111111111111111111111111111",
                "name": "Page 1",
                "layers": [
                    {
                        "_class": "group",
                        "do_objectID": "22222222222222222222222222222222",
                        "name": "Artboard",
                        "layers": [],
                    }
                ],
            }
        ],
    }
    meta = {
        "commit": "d9b9e58d4b2d1b2a4b2b4b2b4b2b4b2b4b2b4b2",
        "pagesAndArtboards": {
            "11111111111111111111111111111111": {
                "name": "Page 1",
                "artboards": {},
            }
        },
        "version": 127,
        "fonts": [],
    }
    user = {"document": {"artboards": {}}, "meta": {}}

    payload = {
        "svg_content": svg,
        "note": "Exported from FlowConvert as SVG wrapped in Sketch container. Open with a tool that supports SVG for best fidelity.",
    }

    with zipfile.ZipFile(out_path, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr("document.json", json.dumps(document))
        z.writestr("meta.json", json.dumps(meta))
        z.writestr("user.json", json.dumps(user))
        z.writestr("svg.svg", svg)
        z.writestr("payload.json", json.dumps(payload))


def build_fig_file(svg_path, out_path):
    """Wrap an SVG into a minimal .fig-like structure.

    Figma .fig is a protobuf-based container; we emit a documented JSON
    sibling + the SVG so the asset remains recoverable for tooling.
    """
    with open(svg_path, "r", encoding="utf-8") as f:
        svg = f.read()

    payload = {
        "type": "svg-export",
        "generator": "FlowConvert",
        "format_hint": "No public SVG->FIG converter in this environment; embedded SVG for import.",
        "svg": svg,
    }
    with open(out_path, "w", encoding="utf-8") as f:
        json.dump(payload, f)


def to_png(image_path, out_path):
    """Convert any supported image to RGB PNG."""
    from PIL import Image, ImageOps
    with Image.open(image_path) as img:
        img = ImageOps.exif_transpose(img)
        img = img.convert("RGB")
        img.save(out_path, "PNG")


def to_pbm(image_path, out_path):
    """Convert to 1-bit PBM for potrace."""
    from PIL import Image, ImageOps
    with Image.open(image_path) as img:
        img = ImageOps.exif_transpose(img)
        gray = ImageOps.grayscale(img).point(lambda p: 255 if p > 150 else 0)
        gray.save(out_path, "BMP")


def is_grayscale(image_path):
    """Return true if image is effectively grayscale (for black-white engine choice)."""
    from PIL import Image
    with Image.open(image_path) as img:
        if img.mode in ("L", "1"):
            return True
        rgb = img.convert("RGB")
        w, h = rgb.size
        # sample pixels
        sample = rgb.resize((min(w, 80), min(h, 80)))
        px = list(sample.getdata())
        for r, g, b in px[::7]:
            if abs(r - g) > 12 or abs(g - b) > 12:
                return False
        return True


if __name__ == "__main__":
    cmd = sys.argv[1]
    if cmd == "svg":
        convert_image_to_svg(sys.argv[2], sys.argv[3], json.loads(sys.argv[4]))
    elif cmd == "sketch":
        build_sketch_file(sys.argv[2], sys.argv[3])
    elif cmd == "fig":
        build_fig_file(sys.argv[2], sys.argv[3])
    elif cmd == "topng":
        to_png(sys.argv[2], sys.argv[3])
    elif cmd == "topbm":
        to_pbm(sys.argv[2], sys.argv[3])
    elif cmd == "gray":
        print("GRAY" if is_grayscale(sys.argv[2]) else "COLOR")
    elif cmd == "pdf_import_ok":
        from PIL import Image
        Image.open(sys.argv[2]).verify()
        print("OK")
    print("OK")