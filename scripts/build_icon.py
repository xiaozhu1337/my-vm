"""Render the project icon with the Python standard library."""
from pathlib import Path
import struct
import zlib

ROOT = Path(__file__).resolve().parents[1]
ASSETS = ROOT / 'assets'


def rounded(x, y, left, top, right, bottom, radius):
    dx = max(left + radius - x, 0, x - right + radius)
    dy = max(top + radius - y, 0, y - bottom + radius)
    return left <= x <= right and top <= y <= bottom and dx * dx + dy * dy <= radius * radius


def pixel(x, y):
    if not rounded(x, y, 12, 12, 244, 244, 52):
        return 0, 0, 0, 0
    t = (x + y) / 512
    color = (round(35 + 62 * t), round(104 - 49 * t), round(224 + 6 * t), 255)
    if rounded(x, y, 63, 48, 213, 164, 16):
        color = (116, 219, 247, 255)
    if rounded(x, y, 71, 56, 205, 156, 9):
        color = (47, 86, 181, 255)
    if rounded(x, y, 39, 82, 193, 198, 16):
        color = (242, 249, 255, 255)
    if rounded(x, y, 47, 90, 185, 176, 9):
        color = (21, 38, 79, 255)
    if abs(x - 116) / 35 + abs(y - 124) / 19 <= 1:
        color = (113, 229, 245, 255)
    if 124 < y <= 153 and abs(x - 116) / 35 + abs(y - 134) / 19 <= 1:
        color = (68, 183, 237, 255)
    if rounded(x, y, 100, 183, 132, 189, 3):
        color = (70, 108, 187, 255)
    return color


def png(size):
    # Four samples per axis keep the small taskbar sizes smooth.
    scale = 256 / size
    data = bytearray()
    for row in range(size):
        data.append(0)
        for col in range(size):
            samples = [pixel((col + (sx + .5) / 4) * scale, (row + (sy + .5) / 4) * scale)
                       for sy in range(4) for sx in range(4)]
            alpha = sum(p[3] for p in samples)
            rgb = [round(sum(p[channel] * p[3] for p in samples) / alpha) if alpha else 0
                   for channel in range(3)]
            data.extend((*rgb, round(alpha / 16)))

    def chunk(kind, body):
        return struct.pack('>I', len(body)) + kind + body + struct.pack('>I', zlib.crc32(kind + body))

    return (b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', struct.pack('>IIBBBBB', size, size, 8, 6, 0, 0, 0))
            + chunk(b'IDAT', zlib.compress(data, 9)) + chunk(b'IEND', b''))


def main():
    ASSETS.mkdir(exist_ok=True)
    sizes = (16, 24, 32, 48, 64, 128, 256)
    images = [png(size) for size in sizes]
    offset = 6 + 16 * len(sizes)
    entries = bytearray()
    for size, image in zip(sizes, images):
        entries.extend(struct.pack('<BBBBHHII', size % 256, size % 256, 0, 0, 1, 32, len(image), offset))
        offset += len(image)
    icon = struct.pack('<HHH', 0, 1, len(images)) + entries + b''.join(images)
    assert offset == len(icon) and images[-1].startswith(b'\x89PNG\r\n\x1a\n')
    assert pixel(0, 0)[3] == 0 and pixel(128, 128)[3] == 255
    (ASSETS / 'my-vm.png').write_bytes(images[-1])
    (ASSETS / 'my-vm.ico').write_bytes(icon)


if __name__ == '__main__':
    main()
