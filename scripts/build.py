"""Build the Windows manager and a read-only guest-tools ISO using mise Go."""
import os
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[1]
DIST = ROOT / 'dist'
STAGE = ROOT / '__tmp' / 'release-media'
IMAGE = ROOT / '__tmp' / 'release-guest-tools.iso'


def run(*args, env=None):
    subprocess.run(args, cwd=ROOT, env=env, check=True, stdin=subprocess.DEVNULL)


def main():
    DIST.mkdir(exist_ok=True)
    STAGE.mkdir(parents=True, exist_ok=True)
    names = ['my-vm-guest.exe', 'my-vm-guest-linux-amd64', 'README.txt']
    try:
        for platform, name in [('windows', names[0]), ('linux', names[1])]:
            environment = dict(os.environ, GOOS=platform, GOARCH='amd64', CGO_ENABLED='0')
            flags = ['-ldflags=-H=windowsgui'] if platform == 'windows' else []
            run('mise', 'exec', 'go@1.27.2', '--', 'go', 'build', '-trimpath', *flags,
                '-o', str(STAGE / name), './cmd/guest-tools', env=environment)
        (STAGE / names[2]).write_text(
            'MyVM Guest Tools\n\nWindows: run my-vm-guest.exe --install as the desktop user.\n'
            'Linux: copy my-vm-guest-linux-amd64 to a writable location, chmod +x, then run --install as the desktop user.\n'
            'See the project README and docs/guest-setup.md for prerequisites.\n', encoding='utf-8')
        run('mise', 'exec', 'go@1.27.2', '--', 'go', 'build', '-trimpath',
            '-ldflags=-H=windowsgui', '-o', str(DIST / 'my-vm.exe'), './cmd/my-vm',
            env=dict(os.environ, GOOS='windows', GOARCH='amd64', CGO_ENABLED='0'))
        if IMAGE.exists():
            IMAGE.unlink()
        run(str(DIST / 'my-vm.exe'), '--build-guest-media', str(STAGE), str(IMAGE))
        os.replace(IMAGE, DIST / 'my-vm-guest-tools.iso')
    finally:
        for name in names:
            (STAGE / name).unlink(missing_ok=True)
        STAGE.rmdir()
        IMAGE.unlink(missing_ok=True)
    print(DIST / 'my-vm.exe')
    print(DIST / 'my-vm-guest-tools.iso')


if __name__ == '__main__':
    main()
