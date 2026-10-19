"""Collect the validated Linux GPU-PV source and matching local Windows driver snapshot."""
import ast
import hashlib
import json
from pathlib import Path
import shutil
from urllib.request import ProxyHandler, build_opener

ROOT = Path(__file__).resolve().parents[2]
COMMIT = 'd504d40ab83839cb61942a812a99d183c42227b9'
DRIVER = Path('C:/Windows/System32/DriverStore/FileRepository/nvlesi.inf_amd64_33018540a16fd177')


def collect():
    target = ROOT / 'dist' / 'guest-payload' / 'gpu'
    target.mkdir(parents=True, exist_ok=True)
    records = []

    def save(name, data):
        path = target / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        records.append((name, hashlib.sha256(data).hexdigest()))

    proxy = 'http://192.168.1.99:7890'
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
    groups = [
        (Path('C:/Windows/System32/lxss/lib'), 'lib', ['libcuda.so', 'libcuda.so.1', 'libcuda.so.1.1', 'libnvidia-ml.so.1', 'nvidia-smi']),
        (Path('C:/Program Files/WSL/lib'), 'lib', ['libd3d12.so', 'libd3d12core.so', 'libdxcore.so']),
        (DRIVER, 'drivers/' + DRIVER.name, ['libcuda.so.1.1', 'libnvidia-ml.so.1', 'libnvdxgdmal.so.1',
                                          'libnvidia-ptxjitcompiler.so.1', 'libnvidia-gpucomp.so', 'nvcubins.bin']),
    ]
    for source, directory, names in groups:
        for name in names:
            save(directory + '/' + name, (source / name).read_bytes())
    save('verify.py', (ROOT / 'scripts' / 'verify_linux_gpu.py').read_bytes())
    inf = (DRIVER / 'nvlesi.inf').read_bytes()
    assert inf == Path('C:/Windows/INF/oem87.inf').read_bytes(), 'Current display driver changed; select its matching DriverStore first'
    lines = ['# Only this guest kernel and host driver snapshot have been validated.',
             'kernel: "7.3.0-9-generic"', 'version: "6.18-d504d40"', 'driver: ' + json.dumps(DRIVER.name),
             'host_inf: ' + json.dumps(str(DRIVER / 'nvlesi.inf')),
             'host_inf_sha256: ' + hashlib.sha256(inf).hexdigest(),
             '# Relative payload paths and their SHA-256 checksums.', 'files:']
    for name, digest in records:
        lines += ['  - name: ' + json.dumps(name), '    sha256: ' + digest]
    (target / 'manifest.yaml').write_text('\n'.join(lines) + '\n', encoding='utf-8')
    print(f'GPU-PV: {len(records)} files, host driver {DRIVER.name}', flush=True)


if __name__ == '__main__':
    collect()
