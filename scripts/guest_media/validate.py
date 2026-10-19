"""Validate the canonical YAML manifests emitted by this package's collectors."""
import hashlib
import json
from pathlib import Path
import re


def verified_files(directory):
    directory = Path(directory)
    text = (directory / 'manifest.yaml').read_text(encoding='utf-8')
    records = re.findall(r'^  - name: ("[^\n]+")\n    sha256: ([a-f0-9]{64})$', text, re.MULTILINE)
    if not records or len(records) != text.count('  - name: '):
        raise ValueError('Incomplete generated manifest: ' + str(directory))
    names = set()
    result = []
    for encoded, expected in records:
        name = json.loads(encoded)
        relative = Path(name)
        if relative.is_absolute() or '..' in relative.parts or name in names:
            raise ValueError('Unsafe or duplicate payload path: ' + name)
        names.add(name)
        path = directory / ('pool' if directory.name.startswith('ubuntu-') else '') / relative
        if path.is_symlink() or not path.is_file():
            raise ValueError('Missing regular payload: ' + str(path))
        with path.open('rb') as source:
            actual = hashlib.file_digest(source, 'sha256').hexdigest()
        if actual != expected:
            raise ValueError('Payload checksum mismatch: ' + str(path))
        result.append(path)
    if directory.name.startswith('ubuntu-'):
        expected = re.search(r'^index_sha256: ([a-f0-9]{64})$', text, re.MULTILINE)
        if expected is None or hashlib.sha256((directory / 'Packages').read_bytes()).hexdigest() != expected[1]:
            raise ValueError('Package index checksum mismatch: ' + str(directory))
    return result


def self_check():
    import tempfile
    temporary = Path(__file__).resolve().parents[2] / '__tmp'
    temporary.mkdir(exist_ok=True)
    root = Path(tempfile.mkdtemp(prefix='manifest-check-', dir=temporary))
    try:
        data = root / 'test.bin'
        data.write_bytes(b'verified')
        digest = hashlib.sha256(b'verified').hexdigest()
        manifest = root / 'manifest.yaml'
        try:
            manifest.write_text('files:\n  - name: "test.bin"\n    sha256: ' + digest + '\n', encoding='utf-8')
            assert verified_files(root) == [data]
            data.write_bytes(b'corrupted')
            try:
                verified_files(root)
            except ValueError:
                pass
            else:
                raise AssertionError('Corrupted payload accepted')
        finally:
            data.unlink(missing_ok=True)
            manifest.unlink(missing_ok=True)
    finally:
        root.rmdir()


if __name__ == '__main__':
    self_check()
