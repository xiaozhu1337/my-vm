"""Collect the validated Linux GPU-PV source and matching local Windows driver snapshot."""
import argparse
import ast
import hashlib
import json
import os
from pathlib import Path
import shutil
from urllib.request import ProxyHandler, build_opener

ROOT = Path(__file__).resolve().parents[2]
COMMIT = 'd504d40ab83839cb61942a812a99d183c42227b9'


def collect(source_only=False):
    target = ROOT / 'dist' / 'guest-payload' / ('gpu-source' if source_only else 'gpu')
    target.mkdir(parents=True, exist_ok=True)
    records = []

    def save(name, data):
        path = target / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        records.append((name, hashlib.sha256(data).hexdigest()))

    proxy = os.environ.get('HTTPS_PROXY') or os.environ.get('HTTP_PROXY') or ''
    opener = build_opener(ProxyHandler({'http': proxy, 'https': proxy}))
    names = ['dxgmodule.c', 'hmgr.c', 'misc.c', 'dxgadapter.c', 'ioctl.c', 'dxgvmbus.c',
             'dxgprocess.c', 'dxgsyncfile.c', 'dxgkrnl.h', 'dxgvmbus.h', 'hmgr.h', 'misc.h', 'dxgsyncfile.h']
    sources = {'source/' + name: 'drivers/hv/dxgkrnl/' + name for name in names}
    sources['source/include/uapi/misc/d3dkmthk.h'] = 'include/uapi/misc/d3dkmthk.h'
    sources.update({'COPYING': 'COPYING', 'GPL-2.0': 'LICENSES/preferred/GPL-2.0'})
    for name, source in sources.items():
        with opener.open(f'https://raw.githubusercontent.com/microsoft/WSL2-Linux-Kernel/{COMMIT}/{source}', timeout=30) as response:
            save(name, response.read())
    # Preserve the exact three generated files used by the existing, validated preparation script.
    tree = ast.parse((ROOT / 'scripts' / 'prepare_linux_gpu.py').read_text(encoding='utf-8'))
    generated = {}
    for node in ast.walk(tree):
        if not isinstance(node, ast.Call) or not isinstance(node.func, ast.Attribute) or node.func.attr != 'write_text':
            continue
        destination = node.func.value
        if isinstance(destination, ast.BinOp) and isinstance(destination.left, ast.Name) and destination.left.id == 'root':
            generated[ast.literal_eval(destination.right)] = ast.literal_eval(node.args[0])
    assert set(generated) == {'gpu_p_guids.h', 'Makefile', 'dkms.conf'}
    for name, content in generated.items():
        save('source/' + name, content.encode('utf-8'))
    save('verify.py', (ROOT / 'scripts' / 'verify_linux_gpu.py').read_bytes())
    lines = ['# Supported guest kernel and dxgkrnl source version.',
             'kernel: "7.3.0-9-generic"', 'version: "6.18-d504d40"']
    if not source_only:
        from windows_gpu import installed_packages
        adapters = [a for a in installed_packages() if a[2].lower().startswith('pci\\ven_10de')]
        drivers = {a[3] for a in adapters}
        if len(drivers) != 1:
            raise RuntimeError('Exactly one installed NVIDIA driver package is required for Linux GPU-PV')
        driver = drivers.pop()
        system = Path(os.environ['SystemRoot']) / 'System32'
        groups = [
            (system / 'lxss/lib', 'lib', ['libcuda.so', 'libcuda.so.1', 'libcuda.so.1.1', 'libnvidia-ml.so.1', 'nvidia-smi']),
            (Path(os.environ['ProgramFiles']) / 'WSL/lib', 'lib', ['libd3d12.so', 'libd3d12core.so', 'libdxcore.so']),
            (driver, 'drivers/' + driver.name, ['libcuda.so.1.1', 'libnvidia-ml.so.1', 'libnvdxgdmal.so.1',
                                             'libnvidia-ptxjitcompiler.so.1', 'libnvidia-gpucomp.so', 'nvcubins.bin']),
        ]
        for source, directory, names in groups:
            for name in names:
                save(directory + '/' + name, (source / name).read_bytes())
        infs = list(driver.glob('*.inf'))
        if len(infs) != 1:
            raise RuntimeError('Installed driver package must contain exactly one INF')
        lines += ['driver: ' + json.dumps(driver.name), 'host_inf: ' + json.dumps(str(infs[0])),
                  'host_inf_sha256: ' + hashlib.sha256(infs[0].read_bytes()).hexdigest()]
    lines += ['# Relative payload paths and their SHA-256 checksums.', 'files:']
    for name, digest in records:
        lines += ['  - name: ' + json.dumps(name), '    sha256: ' + digest]
    (target / 'manifest.yaml').write_text('\n'.join(lines) + '\n', encoding='utf-8')
    print(f'GPU-PV: {len(records)} files, source_only={source_only}', flush=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source-only', action='store_true')
    collect(parser.parse_args().source_only)
