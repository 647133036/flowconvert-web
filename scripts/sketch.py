import sys
import numpy as np
from PIL import Image, ImageOps, ImageFilter

def color_dodge_blend(base, blend):
    """Simple color dodge: min(255, base * 255 / (255 - blend))"""
    base = base.astype(np.float32)
    blend = blend.astype(np.float32)
    # Avoid division by zero
    with np.errstate(divide='ignore', invalid='ignore'):
        result = np.where(blend == 255.0, 255.0,
                          np.minimum(255.0, (base * 255.0) / (255.0 - blend)))
    return np.clip(result, 0, 255).astype(np.uint8)


def make_sketch(image_path, out_path, sigma):
    with Image.open(image_path) as img:
        img = ImageOps.exif_transpose(img)
        img = img.convert("RGB")

        gray = ImageOps.grayscale(img)
        if sigma <= 0.1:
            sigma = 0.1
        blurred = np.array(gray.filter(ImageFilter.GaussianBlur(radius=sigma)))
        inverted = 255 - blurred
        sketch = color_dodge_blend(np.array(gray), inverted)
        sketch = Image.fromarray(sketch)
        sketch = ImageOps.autocontrast(sketch)
        sketch.save(out_path, "PNG")


if __name__ == "__main__":
    sigma = float(sys.argv[3]) if len(sys.argv) > 3 else 3.0
    make_sketch(sys.argv[2], sys.argv[4], sigma)
    print("OK")