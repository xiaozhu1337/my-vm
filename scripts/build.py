"""Build the manager and separate Windows/Linux offline enhancement discs with mise Go."""
import os
from pathlib import Path
import shutil
import subprocess

from build_icon import main as build_icon
from guest_media.validate import verified_files

ROOT = Path(__file__).resolve().parents[1]
DIST = ROOT / 'dist'
VERSIONS = ('26.04', '26.10')


def run(*args, env=None):
    subprocess.run(args, cwd=ROOT, env=env, check=True, stdin=subprocess.DEVNULL)


def build_disc(platform, payloads):
    stage = ROOT / '__tmp' / ('release-media-' + platform)
    image = ROOT / '__tmp' / ('release-guest-tools-' + platform + '.iso')
    target_iso = DIST / ('my-vm-guest-tools-' + platform + '.iso')
    stage.mkdir(parents=True, exist_ok=True)
    staged, directories = [], []

    def text(name, content):
        target = stage / name
        target.write_text(content, encoding='utf-8')
        staged.append(target)

    def link(source, target):
        os.link(source, target)
        staged.append(target)

    def directory(path):
        path.mkdir()
        directories.append(path)

    def payload(source, files, target):
        directory(target)
        link(source / 'manifest.yaml', target / 'manifest.yaml')
        for file in files:
            relative = file.relative_to(source)
            parent = target
            for part in relative.parts[:-1]:
                parent /= part
                if not parent.exists():
                    directory(parent)
            link(file, target / relative)

    try:
        name = 'install.exe' if platform == 'windows' else 'my-vm-guest-linux-amd64'
        environment = dict(os.environ, GOOS=platform, GOARCH='amd64', CGO_ENABLED='0')
        flags = ['-ldflags=-H=windowsgui'] if platform == 'windows' else []
        target = stage / name
        run('mise', 'exec', 'go@1.27.2', '--', 'go', 'build', '-trimpath', *flags,
            '-o', str(target), './cmd/guest-tools', env=environment)
        staged.append(target)
        if platform == 'windows':
            link(stage / 'install.exe', stage / 'my-vm-guest.exe')
            text('autorun.inf', '[autorun]\nopen=install.exe\naction=Install MyVM Guest Enhancements\n')
            text('README.txt',
                 'MyVM Windows 离线客户机增强盘\n\n'
                 'Windows 10/11 Pro: 双击 install.exe，以当前桌面用户授权 UAC。\n'
                 '安装共享目录、远程桌面服务和登录自启动；不访问互联网仓库。\n'
                 'GPU-PV 首次准备或驱动升级请在基础控制台运行；已有驱动匹配且设备正常时可在增强会话重复安装。\n'
                 '安装后在主机点击“增强会话”；账户需设置登录密码。\n'
                 'GPU 设备代码正常不代表硬件图形/计算已通过，兼容性见项目验证记录。\n')
            source = DIST / 'guest-payload' / 'windows-gpu'
            payload(source, payloads['windows-gpu'], stage / 'windows-gpu')
        else:
            launcher = stage / 'install.py'
            shutil.copyfile(ROOT / 'scripts' / 'guest_media' / 'install.py', launcher)
            staged.append(launcher)
            text('install.desktop', '[Desktop Entry]\nType=Application\nName=Install MyVM Guest Enhancements\n'
                 'Name[zh_CN]=安装 MyVM 客户机增强\nTerminal=true\n'
                 'Exec=python3 -c "import runpy,sys,urllib.parse,pathlib; '
                 "runpy.run_path(str(pathlib.Path(urllib.parse.unquote(urllib.parse.urlparse(sys.argv[1]).path)).with_name('install.py')), run_name='__main__')\" %k\n")
            text('README.txt',
                 'MyVM Linux 离线客户机增强盘\n\n'
                 'Ubuntu / Kubuntu 26.04、26.10 amd64: 在已登录的 GNOME/KDE Wayland 桌面终端运行：\n'
                 'python3 "/media/实际光盘路径/install.py"\n'
                 '也可在文件管理器允许启动 install.desktop；若桌面阻止光盘启动器，请使用上述终端命令。\n'
                 '包含本地依赖、共享目录、远程桌面证书和服务、自启动；不访问互联网仓库。\n'
                 'GNOME 会询问远程桌面专用凭据；KDE 使用当前系统账户密码。\n'
                 '安装后在主机点击“增强会话”，确认客户机证书并登录。\n'
                 '保留 TLS/NLA，桌面需保持已登录；剪贴板与显示调整由原生服务协商。\n'
                 'GPU-PV 仅在已验证的内核和宿主驱动组合下自动准备并运行 CUDA 自检。\n')
            linux = stage / 'linux'
            directory(linux)
            for version in VERSIONS:
                source = DIST / 'guest-payload' / ('ubuntu-' + version)
                target = linux / source.name
                directory(target)
                directory(target / 'pool')
                for name in ('manifest.yaml', 'Packages'):
                    link(source / name, target / name)
                for package in payloads[version]:
                    link(package, target / 'pool' / package.name)
            source = DIST / 'guest-payload' / 'gpu'
            payload(source, payloads['gpu'], stage / 'gpu')
        image.unlink(missing_ok=True)
        run(str(DIST / 'my-vm.exe'), '--build-guest-media', str(stage), str(image))
        os.replace(image, target_iso)
    finally:
        for path in reversed(staged):
            path.unlink(missing_ok=True)
        for path in reversed(directories):
            path.rmdir()
        stage.rmdir()
        image.unlink(missing_ok=True)
    return target_iso


def main():
    DIST.mkdir(exist_ok=True)
    for version in VERSIONS:
        if not (DIST / 'guest-payload' / ('ubuntu-' + version) / 'manifest.yaml').is_file():
            run('python', '-X', 'utf8', str(ROOT / 'scripts' / 'guest_media' / 'collect_packages.py'), version)
    payloads = {version: verified_files(DIST / 'guest-payload' / ('ubuntu-' + version)) for version in VERSIONS}
    for name in ('gpu', 'windows_gpu'):
        run('python', '-X', 'utf8', str(ROOT / 'scripts' / 'guest_media' / (name + '.py')))
        folder = name.replace('_', '-')
        payloads[folder] = verified_files(DIST / 'guest-payload' / folder)
    build_icon()
    run('mise', 'exec', 'go@1.27.2', '--', 'go', 'run',
        'github.com/tc-hib/go-winres@v0.3.3', 'simply', '--arch', 'amd64',
        '--manifest', 'none', '--icon', str(ROOT / 'assets' / 'my-vm.ico'),
        '--out', str(ROOT / 'cmd' / 'my-vm' / 'rsrc'),
        env=dict(os.environ, HTTP_PROXY='http://192.168.1.99:7890', HTTPS_PROXY='http://192.168.1.99:7890'))
    run('mise', 'exec', 'go@1.27.2', '--', 'go', 'build', '-trimpath',
        '-o', str(DIST / 'my-vm-cli.exe'), './cmd/my-vm-cli',
        env=dict(os.environ, GOOS='windows', GOARCH='amd64', CGO_ENABLED='0'))
    run('mise', 'exec', 'go@1.27.2', '--', 'go', 'build', '-trimpath',
        '-ldflags=-H=windowsgui', '-o', str(DIST / 'my-vm.exe'), './cmd/my-vm',
        env=dict(os.environ, GOOS='windows', GOARCH='amd64', CGO_ENABLED='0'))
    for platform in ('windows', 'linux'):
        print(build_disc(platform, payloads))
    print(DIST / 'my-vm.exe')
    print(DIST / 'my-vm-cli.exe')


if __name__ == '__main__':
    main()
