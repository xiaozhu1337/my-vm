"""Verify complete dependency installation in disposable Ubuntu containers without networking."""
import argparse
from pathlib import Path
import os
import shlex
import subprocess

from collect_packages import docker_prefix
from validate import verified_files

ROOT = Path(__file__).resolve().parents[2]


def verify(version):
    payload = ROOT / 'dist' / 'guest-payload' / ('ubuntu-' + version)
    verified_files(payload)
    manifest = (payload / 'manifest.yaml').read_text(encoding='utf-8')
    requested = [line[4:] for line in manifest.split('packages:\n', 1)[1].split('files:', 1)[0].splitlines()
                 if line.startswith('  - ')]
    assert requested and all('/' not in name and not name.startswith('-') for name in requested)
    source = payload.as_posix()
    if os.name == 'nt':
        source = '/mnt/' + source[0].lower() + source[2:]
    script = '''
mkdir -p /var/lib/my-vm-guest/apt/lists/partial /var/lib/my-vm-guest/apt/archives/partial
printf '%s\\n' 'deb [trusted=yes] file:/media/my-vm ./' > /var/lib/my-vm-guest/apt/sources.list
apt-get -o Dir::Etc::sourcelist=/var/lib/my-vm-guest/apt/sources.list -o Dir::Etc::sourceparts=- -o Dir::State::lists=/var/lib/my-vm-guest/apt/lists -o APT::Sandbox::User=root update
apt-get -o Dir::Etc::sourcelist=/var/lib/my-vm-guest/apt/sources.list -o Dir::Etc::sourceparts=- -o Dir::State::lists=/var/lib/my-vm-guest/apt/lists -o Dir::Cache::archives=/var/lib/my-vm-guest/apt/archives -o APT::Sandbox::User=root --no-remove --no-install-recommends --assume-yes install ''' + shlex.join(requested)
    subprocess.run([*docker_prefix(), 'run', '--rm', '--network', 'none', '--env', 'DEBIAN_FRONTEND=noninteractive',
                    '--mount', 'type=bind,source=' + source + ',target=/media/my-vm,readonly',
                    'my-vm-media:ubuntu-' + version, 'sh', '-ec', script], check=True, stdin=subprocess.DEVNULL)
    print('Offline installation passed: Ubuntu ' + version, flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('versions', nargs='*', default=['26.04', '26.10'], choices=['26.04', '26.10'])
    for version in parser.parse_args().versions:
        verify(version)


if __name__ == '__main__':
    main()
