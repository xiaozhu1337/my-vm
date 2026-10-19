"""Import an official Ubuntu image without changing Docker's daemon proxy."""
import gzip
import hashlib
import io
import json
from pathlib import Path
import shutil
import subprocess
import tarfile
import urllib.parse
import urllib.request


def prepare_image(version, docker_prefix, temporary, proxy):
    image = 'my-vm-media:ubuntu-' + version
    if subprocess.run([*docker_prefix, 'image', 'inspect', image],
                      stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0:
        return image
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({'http': proxy, 'https': proxy}))
    token_url = 'https://auth.docker.io/token?' + urllib.parse.urlencode({
        'service': 'registry.docker.io', 'scope': 'repository:library/ubuntu:pull'})
    with opener.open(token_url, timeout=30) as response:
        token = json.load(response)['token']
    headers = {'Authorization': 'Bearer ' + token, 'Accept':
               'application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, '
               'application/vnd.docker.distribution.manifest.list.v2+json, '
               'application/vnd.docker.distribution.manifest.v2+json'}
    base = 'https://registry-1.docker.io/v2/library/ubuntu/'

    def fetch_manifest(reference):
        request = urllib.request.Request(base + 'manifests/' + reference, headers=headers)
        with opener.open(request, timeout=30) as response:
            raw = response.read()
        if reference.startswith('sha256:') and 'sha256:' + hashlib.sha256(raw).hexdigest() != reference:
            raise RuntimeError('Ubuntu image manifest checksum mismatch')
        return json.loads(raw)

    data = fetch_manifest(version)
    if 'manifests' in data:
        matches = [item for item in data['manifests']
                   if item.get('platform', {}).get('architecture') == 'amd64'
                   and item.get('platform', {}).get('os') == 'linux']
        if len(matches) != 1:
            raise RuntimeError('Ubuntu registry has no unique Linux amd64 image')
        data = fetch_manifest(matches[0]['digest'])
    archive = temporary / ('ubuntu-' + version + '-image.tar')
    blob = temporary / ('ubuntu-' + version + '-blob')
    layer = temporary / ('ubuntu-' + version + '-layer.tar')

    def download(descriptor):
        request = urllib.request.Request(base + 'blobs/' + descriptor['digest'], headers=headers)
        digest = hashlib.sha256()
        with opener.open(request, timeout=60) as source, blob.open('wb') as target:
            while block := source.read(1024 * 1024):
                digest.update(block)
                target.write(block)
        if 'sha256:' + digest.hexdigest() != descriptor['digest']:
            raise RuntimeError('Ubuntu image blob checksum mismatch')

    def text_entry(output, name, raw):
        info = tarfile.TarInfo(name)
        info.size = len(raw)
        output.addfile(info, io.BytesIO(raw))

    try:
        with tarfile.open(archive, 'w') as output:
            download(data['config'])
            config_name = data['config']['digest'].split(':')[1] + '.json'
            text_entry(output, config_name, blob.read_bytes())
            names = []
            for descriptor in data['layers']:
                download(descriptor)
                name = descriptor['digest'].split(':')[1] + '/layer.tar'
                if descriptor['mediaType'].endswith('gzip'):
                    with gzip.open(blob, 'rb') as source, layer.open('wb') as target:
                        shutil.copyfileobj(source, target)
                elif descriptor['mediaType'].endswith('.tar'):
                    shutil.copyfile(blob, layer)
                else:
                    raise RuntimeError('Unsupported Ubuntu layer compression')
                output.add(layer, arcname=name)
                names.append(name)
            text_entry(output, 'manifest.json', json.dumps([
                {'Config': config_name, 'RepoTags': [image], 'Layers': names}]).encode('utf-8'))
        with archive.open('rb') as source:
            subprocess.run([*docker_prefix, 'load'], stdin=source, check=True)
    finally:
        archive.unlink(missing_ok=True)
        blob.unlink(missing_ok=True)
        layer.unlink(missing_ok=True)
    return image
