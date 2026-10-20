"""Package existing portable Windows builds into one verified release ZIP."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import zipfile

ROOT = Path(__file__).resolve().parents[1]
DIST = ROOT / 'dist'
BASENAME = 'my-vm-windows-amd64.zip'


def run(*args):
    return subprocess.run(args, cwd=ROOT, check=True, stdin=subprocess.DEVNULL,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, encoding='utf-8',
                          creationflags=subprocess.CREATE_NO_WINDOW if os.name == 'nt' else 0).stdout


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def modules():
    data = run('mise', 'exec', 'go@1.27.2', '--', 'go', 'list', '-m', '-json', 'all')
    decoder = json.JSONDecoder()
    while data.strip():
        value, end = decoder.raw_decode(data.lstrip())
        yield value
        data = data.lstrip()[end:]


def package():
    files = {}
    for name in ('my-vm.exe', 'my-vm-cli.exe', 'my-vm-guest-tools-windows.iso', 'my-vm-guest-tools-linux.iso'):
        path = DIST / name
        if not path.is_file() or path.stat().st_size == 0:
            raise ValueError('Missing release artifact: ' + name)
        files[name] = path
    for name in ('README.md', 'README.zh-CN.md', 'assets/my-vm.png', 'assets/screenshots/overview.png',
                 'assets/screenshots/snapshots.png', 'assets/screenshots/devices.png',
                 'assets/screenshots/enhanced-sessions.png'):
        files[name] = ROOT / name
    for platform in ('windows', 'linux'):
        source = DIST / ('my-vm-guest-tools-' + platform + '.iso')
        names = run(str(Path(os.environ['SystemRoot']) / 'System32/tar.exe'), '-tf', str(source))
        if 'windows-gpu/' in names or 'gpu/drivers/' in names or 'gpu/lib/' in names:
            raise ValueError('Release ISO contains host-specific GPU binaries: ' + str(source))
        if platform == 'linux':
            if 'gpu/source/dxgmodule.c' not in names:
                raise ValueError('Linux release ISO is missing portable dxgkrnl source')
            if any(name.startswith('linux/') or name.endswith('.deb') for name in names.splitlines()):
                raise ValueError('Linux release ISO contains obsolete offline Ubuntu packages')
    descriptions = []
    for module in modules():
        if module.get('Main') or 'Dir' not in module:
            continue
        directory = Path(module['Dir'])
        licenses = sorted(path for path in directory.iterdir() if path.is_file() and
                          path.name.upper().split('.')[0] in ('LICENSE', 'COPYING', 'NOTICE', 'AUTHORS'))
        if not licenses:
            raise ValueError('No third-party license found: ' + module['Path'])
        folder = module['Path'].replace('/', '_') + '@' + module['Version']
        for license_file in licenses:
            files['licenses/' + folder + '/' + license_file.name] = license_file
        descriptions.append(module['Path'] + ' ' + module['Version'])
    goroot = Path(run('mise', 'exec', 'go@1.27.2', '--', 'go', 'env', 'GOROOT').strip())
    files['licenses/go/LICENSE'] = goroot / 'LICENSE'
    notices = ('My VM: bundled third-party components\n\n'
               'Go runtime and libraries: see licenses/go/LICENSE.\n'
               'Go dependencies and original notices are preserved under licenses/.\n\n' +
               '\n'.join(descriptions) + '\n\n'
               'The Linux enhancement ISO contains Microsoft WSL2-Linux-Kernel dxgkrnl source,\n'
               'based on commit d504d40ab83839cb61942a812a99d183c42227b9. Its complete bundled\n'
               'source and COPYING/GPL-2.0 license texts are in gpu/ on that ISO.\n'
               'Upstream: https://github.com/microsoft/WSL2-Linux-Kernel\n\n'
               'Linux desktop and build dependencies are downloaded during installation using\n'
               'the guest\'s configured APT repositories and signature verification. No Ubuntu\n'
               'binary packages are bundled in this ZIP.\n\n'
               'No host-specific GPU or WSL proprietary driver binaries are redistributed in\n'
               'this ZIP. With GPU sharing enabled, My VM prepares separate private media\n'
               'from the current host at the user\'s request.\n\n'
               'Acknowledgments: MyGo, Onevra, NanaBox; see the project README.\n').encode('utf-8')
    generated = {'THIRD-PARTY-NOTICES.txt': notices}
    checksums = [digest(path) + '  ' + name for name, path in sorted(files.items())]
    checksums += [hashlib.sha256(content).hexdigest() + '  ' + name for name, content in sorted(generated.items())]
    generated['SHA256SUMS.txt'] = ('\n'.join(sorted(checksums, key=lambda line: line[66:])) + '\n').encode('utf-8')
    partial = DIST / (BASENAME + '.partial')
    try:
        with zipfile.ZipFile(partial, 'w', compression=zipfile.ZIP_DEFLATED, compresslevel=6) as archive:
            for name, path in sorted(files.items()):
                archive.write(path, 'my-vm/' + name)
            for name, content in sorted(generated.items()):
                archive.writestr('my-vm/' + name, content)
        verify(partial)
        target = DIST / BASENAME
        os.replace(partial, target)
        checksum = digest(target)
        (DIST / (BASENAME + '.sha256')).write_text(checksum + '  ' + BASENAME + '\n', encoding='utf-8')
        print(f'{target}: {target.stat().st_size:,} bytes\nSHA256 {checksum}')
    finally:
        partial.unlink(missing_ok=True)


def verify(path):
    with zipfile.ZipFile(path) as archive:
        assert archive.testzip() is None, 'ZIP CRC verification failed'
        names = archive.namelist()
        assert len(names) == len(set(names)), 'Duplicate ZIP entry'
        assert all(name.startswith('my-vm/') and '..' not in Path(name).parts for name in names), 'Unsafe ZIP entry'
        assert not any('/docs/' in name or '/__tmp/' in name or name.endswith('.vhdx') for name in names), 'Local data included'
        checksums = archive.read('my-vm/SHA256SUMS.txt').decode('utf-8').splitlines()
        assert len(checksums) + 1 == len(names), 'Incomplete ZIP checksum manifest'
        for line in checksums:
            expected, name = line.split('  ', 1)
            with archive.open('my-vm/' + name) as stream:
                actual = hashlib.file_digest(stream, 'sha256').hexdigest()
            assert actual == expected, 'ZIP checksum mismatch: ' + name
        for name in ('README.md', 'README.zh-CN.md', 'THIRD-PARTY-NOTICES.txt'):
            content = archive.read('my-vm/' + name)
            assert not content.startswith(b'\xef\xbb\xbf'), 'Unexpected UTF-8 BOM'
            content.decode('utf-8')


if __name__ == '__main__':
    package()
