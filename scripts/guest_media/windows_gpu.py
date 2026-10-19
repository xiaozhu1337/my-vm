"""Snapshot installed physical display-driver packages for Windows GPU-PV guests."""
import ctypes as c
from ctypes import wintypes as w
import hashlib
import json
import os
from pathlib import Path
import shutil
import winreg

ROOT = Path(__file__).resolve().parents[2]
CLASS = '{4d36e968-e325-11ce-bfc1-08002be10318}'


class GUID(c.Structure):
    _fields_ = [('data', c.c_ubyte * 16)]


class Device(c.Structure):
    _fields_ = [('size', w.DWORD), ('guid', GUID), ('instance', w.DWORD), ('reserved', c.c_void_p)]


def installed_packages():
    import uuid
    setup = c.WinDLL('setupapi', use_last_error=True)
    guid = GUID((c.c_ubyte * 16).from_buffer_copy(uuid.UUID(CLASS).bytes_le))
    setup.SetupDiGetClassDevsW.argtypes = [c.POINTER(GUID), w.LPCWSTR, w.HWND, w.DWORD]
    setup.SetupDiGetClassDevsW.restype = w.HANDLE
    setup.SetupDiEnumDeviceInfo.argtypes = [w.HANDLE, w.DWORD, c.POINTER(Device)]
    setup.SetupDiGetDeviceRegistryPropertyW.argtypes = [w.HANDLE, c.POINTER(Device), w.DWORD, c.POINTER(w.DWORD), c.c_void_p, w.DWORD, c.POINTER(w.DWORD)]
    setup.SetupDiDestroyDeviceInfoList.argtypes = [w.HANDLE]
    setup.SetupGetInfDriverStoreLocationW.argtypes = [w.LPCWSTR, c.c_void_p, w.LPCWSTR, w.LPWSTR, w.DWORD, c.POINTER(w.DWORD)]
    devices = setup.SetupDiGetClassDevsW(c.byref(guid), None, None, 2)
    if devices == c.c_void_p(-1).value:
        raise c.WinError(c.get_last_error())
    result = []
    try:
        index = 0
        while True:
            device = Device(size=c.sizeof(Device))
            if not setup.SetupDiEnumDeviceInfo(devices, index, c.byref(device)):
                if c.get_last_error() == 259:
                    break
                raise c.WinError(c.get_last_error())
            index += 1
            buffer = c.create_unicode_buffer(32768)
            required = w.DWORD()
            if not setup.SetupDiGetDeviceRegistryPropertyW(devices, c.byref(device), 9, None, buffer, c.sizeof(buffer), c.byref(required)):
                raise c.WinError(c.get_last_error())
            with winreg.OpenKey(winreg.HKEY_LOCAL_MACHINE, 'SYSTEM\\CurrentControlSet\\Control\\Class\\' + buffer.value) as key:
                def value(name):
                    return winreg.QueryValueEx(key, name)[0]
                hardware = value('MatchingDeviceId')
                if not hardware.lower().startswith('pci\\'):
                    continue
                name, version, inf = value('DriverDesc'), value('DriverVersion'), value('InfPath')
                copies = []
                for subkey, directory, overwrite in (
                    ('CopyToVmWhenNewer', 'System32', False),
                    ('CopyToVmWhenNewerWow64', 'SysWOW64', False),
                    ('CopyToVmOverwrite', 'System32', True),
                    ('CopyToVmOverwriteWow64', 'SysWOW64', True),
                ):
                    try:
                        child = winreg.OpenKey(key, subkey)
                    except FileNotFoundError:
                        continue
                    with child:
                        for item in range(winreg.QueryInfoKey(child)[1]):
                            _, data, kind = winreg.EnumValue(child, item)
                            parts = [data] if kind == winreg.REG_SZ else data
                            if kind not in (winreg.REG_SZ, winreg.REG_MULTI_SZ) or not 1 <= len(parts) <= 2:
                                raise ValueError('Invalid driver CopyToVm declaration')
                            source = parts[0].replace('\\', '/')
                            destination = parts[1] if len(parts) == 2 else source.rsplit('/', 1)[-1]
                            if ':' in source or source.startswith('/') or '..' in source.split('/') or any(ch in destination for ch in '/\\:'):
                                raise ValueError('Unsafe driver CopyToVm path')
                            copies.append((source, directory + '/' + destination, overwrite))
            if not setup.SetupGetInfDriverStoreLocationW(str(Path(os.environ['SystemRoot']) / 'INF' / inf), None, None, buffer, len(buffer), c.byref(required)):
                raise c.WinError(c.get_last_error())
            result.append((name, version, hardware, Path(buffer.value).parent, copies))
    finally:
        setup.SetupDiDestroyDeviceInfoList(devices)
    if not result:
        raise RuntimeError('No installed physical display-driver package found')
    return result


def collect():
    target = ROOT / 'dist/guest-payload/windows-gpu'
    target.mkdir(parents=True, exist_ok=True)
    adapters, records, seen = [], [], set()
    for name, version, hardware, source, copies in installed_packages():
        adapters.append((name, version, hardware, source.name, copies))
        if source.name.lower() in seen:
            continue
        seen.add(source.name.lower())
        for path in sorted(source.rglob('*')):
            if path.stat().st_file_attributes & 0x400:
                raise ValueError('Driver package contains a reparse point: ' + str(path))
            if not path.is_file():
                continue
            relative = Path('FileRepository') / source.name / path.relative_to(source)
            destination = target / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            with path.open('rb') as stream:
                digest = hashlib.file_digest(stream, 'sha256').hexdigest()
            existing = None
            if destination.is_file():
                with destination.open('rb') as stream:
                    existing = hashlib.file_digest(stream, 'sha256').hexdigest()
            if existing != digest:
                shutil.copy2(path, destination)
            else:
                shutil.copystat(path, destination)
            records.append((relative.as_posix(), digest))
    lines = ['# Host-matched Windows GPU-PV driver packages; regenerate after host driver updates.',
             'architecture: amd64', '# Physical adapters represented by these driver packages.', 'adapters:']
    for name, version, hardware, driver, copies in adapters:
        lines += ['  - device_name: ' + json.dumps(name), '    version: ' + json.dumps(version),
                  '    hardware_id: ' + json.dumps(hardware), '    driver: ' + json.dumps(driver), '    copies:']
        for source, destination, overwrite in copies:
            lines += ['      - source: ' + json.dumps('FileRepository/' + driver + '/' + source),
                      '        destination: ' + json.dumps(destination),
                      '        overwrite: ' + str(overwrite).lower()]
    lines += ['# Relative file paths and SHA-256 checksums.', 'files:']
    for name, digest in records:
        lines += ['  - name: ' + json.dumps(name), '    sha256: ' + digest]
    (target / 'manifest.yaml').write_text('\n'.join(lines) + '\n', encoding='utf-8')
    print(f'Windows GPU-PV: {len(adapters)} adapters, {len(records)} files', flush=True)


if __name__ == '__main__':
    collect()
