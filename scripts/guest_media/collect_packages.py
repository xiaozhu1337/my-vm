"""Resolve offline Ubuntu dependencies with APT and download their verified payloads in parallel."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
from pathlib import Path
import shlex
import subprocess
from urllib.parse import unquote, urlsplit
from urllib.request import ProxyHandler, build_opener

from registry import prepare_image

ROOT = Path(__file__).resolve().parents[2]
PROXY = os.environ.get('HTTPS_PROXY') or os.environ.get('HTTP_PROXY') or ''
PACKAGES = ['gnome-remote-desktop', 'krdp', 'gvfs-backends', 'gvfs-fuse',
            'libglib2.0-bin', 'libsecret-tools', 'kmod', 'pkexec', 'polkitd', 'gnome-keyring',
            'libkf6config-bin', 'kwallet6', 'xdg-desktop-portal', 'xdg-desktop-portal-kde']


def docker_prefix():
    return ['wsl.exe', '--exec', 'docker'] if os.name == 'nt' else ['docker']


def docker(*arguments, capture=False):
    result = subprocess.run([*docker_prefix(), *arguments], check=True, stdin=subprocess.DEVNULL,
                            stdout=subprocess.PIPE if capture else None)
    return result.stdout.decode('utf-8') if capture else None


def collect(version):
    temporary = ROOT / '__tmp'
    temporary.mkdir(exist_ok=True)
    destination = ROOT / 'dist' / 'guest-payload' / ('ubuntu-' + version)
    packages = destination / 'pool'
    packages.mkdir(parents=True, exist_ok=True)
    image = prepare_image(version, docker_prefix(), temporary, PROXY)
    requested = list(PACKAGES)
    if version == '26.10':
        requested += ['build-essential', 'dkms', 'libelf-dev', 'gcc-15',
                      'linux-headers-7.3.0-9', 'linux-headers-7.3.0-9-generic']
    base = ['run', '--rm', '--env', 'http_proxy=' + PROXY, '--env', 'https_proxy=' + PROXY,
            '--env', 'DEBIAN_FRONTEND=noninteractive', '--volume',
            'my-vm-guest-apt-' + version.replace('.', '-') + '-lists:/var/lib/apt/lists', image]
    docker(*base, 'apt-get', '-o', 'Dir::Etc::parts=-', '-o', 'Acquire::Retries=3', 'update')
    plan = docker(*base, 'apt-get', '--assume-yes', '--print-uris', '--download-only',
                  '--no-install-recommends', '-o', 'Dir::State::status=/dev/null', 'install', *requested, capture=True)
    package_names = sorted({shlex.split(line)[1].split('_', 1)[0]
                            for line in plan.splitlines() if line.startswith("'http")})
    catalog = docker(*base, 'apt-cache', 'show', '--all-versions', *package_names, capture=True)
    metadata = {}
    for stanza in catalog.split('\n\n'):
        fields = dict(line.split(': ', 1) for line in stanza.splitlines() if ': ' in line and not line.startswith(' '))
        if 'Filename' in fields and ('SHA256' in fields or 'SHA512' in fields):
            metadata[fields['Filename']] = (stanza, fields)
    selected = []
    for line in plan.splitlines():
        if not line.startswith("'http"):
            continue
        parts = shlex.split(line)
        package_path = 'pool/' + unquote(urlsplit(parts[0]).path).split('/pool/', 1)[1]
        stanza, fields = metadata[package_path]
        digest = fields.get('SHA256') or fields['SHA512']
        selected.append((parts[0], digest, int(fields['Size']), stanza))
    if not selected:
        raise RuntimeError('APT resolved no downloadable dependencies')

    def download(item):
        uri, digest, size, _ = item
        target = packages / (digest[:32] + '.deb')
        if target.is_file():
            with target.open('rb') as source:
                if hashlib.file_digest(source, 'sha512' if len(digest) == 128 else 'sha256').hexdigest() == digest:
                    return
        pending = target.with_suffix('.download')
        for attempt in range(3):
            try:
                opener = build_opener(ProxyHandler({'http': PROXY, 'https': PROXY}))
                with opener.open(uri, timeout=45) as response, pending.open('wb') as output:
                    while chunk := response.read(256*1024):
                        output.write(chunk)
                with pending.open('rb') as source:
                    valid = pending.stat().st_size == size and hashlib.file_digest(source, 'sha512' if len(digest) == 128 else 'sha256').hexdigest() == digest
                if not valid:
                    raise RuntimeError('Package digest mismatch: ' + uri)
                pending.replace(target)
                return
            except Exception:
                pending.unlink(missing_ok=True)
                if attempt == 2:
                    raise
    with ThreadPoolExecutor(max_workers=8) as pool:
        list(pool.map(download, selected))
    index = ''.join('\n'.join('Filename: pool/' + digest[:32] + '.deb'
                             if line.startswith('Filename: ') else line for line in stanza.splitlines()) + '\n\n'
                    for _, digest, _, stanza in selected).encode('utf-8')
    (destination / 'Packages').write_bytes(index)
    lines = ['# Exact guest distribution and architecture accepted by the offline installer.',
             'os: ubuntu', 'version: ' + json.dumps(version), 'architecture: amd64',
             '# SHA-256 of the local APT package index.', 'index_sha256: ' + hashlib.sha256(index).hexdigest(),
             '# Top-level capabilities; files includes the exact APT dependency closure.',
             'packages:', *['  - ' + name for name in requested],
             '# SHA-256 of each file, checked before installation.', 'files:']
    for _, digest, _, _ in selected:
        name = digest[:32] + '.deb'
        with (packages / name).open('rb') as source:
            sha256 = hashlib.file_digest(source, 'sha256').hexdigest()
        lines += ['  - name: ' + json.dumps(name), '    sha256: ' + sha256]
    (destination / 'manifest.yaml').write_text('\n'.join(lines) + '\n', encoding='utf-8')
    print(f'{version}: {len(selected)} packages, {sum(item[2] for item in selected):,} bytes', flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('versions', nargs='*', default=['26.10'])
    arguments = parser.parse_args()
    for version in arguments.versions:
        if version not in ('26.04', '26.10'):
            parser.error('Supported versions: 26.04, 26.10')
        collect(version)


if __name__ == '__main__':
    main()
