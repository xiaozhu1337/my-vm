"""Build the manager and a complete offline guest-enhancement disc with mise Go."""
import os
from pathlib import Path
import shutil
import subprocess

from build_icon import main as build_icon
from guest_media.validate import verified_files

ROOT = Path(__file__).resolve().parents[1]
DIST = ROOT / 'dist'
STAGE = ROOT / '__tmp' / 'release-media'
IMAGE = ROOT / '__tmp' / 'release-guest-tools.iso'
VERSIONS = ('26.04', '26.10')


def run(*args, env=None):
    subprocess.run(args, cwd=ROOT, env=env, check=True, stdin=subprocess.DEVNULL)


def main():
    DIST.mkdir(exist_ok=True)
    for version in VERSIONS:
        if not (DIST / 'guest-payload' / ('ubuntu-' + version) / 'manifest.yaml').is_file():
            run('python', '-X', 'utf8', str(ROOT / 'scripts' / 'guest_media' / 'collect_packages.py'), version)
    payloads = {version: verified_files(DIST / 'guest-payload' / ('ubuntu-' + version)) for version in VERSIONS}
    run('python', '-X', 'utf8', str(ROOT / 'scripts' / 'guest_media' / 'gpu.py'))
    gpu_source = DIST / 'guest-payload' / 'gpu'
    gpu_files = verified_files(gpu_source)
    build_icon()
    run('mise', 'exec', 'go@1.27.2', '--', 'go', 'run',
        'github.com/tc-hib/go-winres@v0.3.3', 'simply', '--arch', 'amd64',
        '--manifest', 'none', '--icon', str(ROOT / 'assets' / 'my-vm.ico'),
        '--out', str(ROOT / 'cmd' / 'my-vm' / 'rsrc'),
        env=dict(os.environ, HTTP_PROXY='http://192.168.1.99:7890', HTTPS_PROXY='http://192.168.1.99:7890'))
    STAGE.mkdir(parents=True, exist_ok=True)
    staged, directories = [], []

    def text(name, content):
        target = STAGE / name
        target.write_text(content, encoding='utf-8')
        staged.append(target)

    def link(source, target):
        os.link(source, target)
        staged.append(target)

    def directory(path):
        path.mkdir()
        directories.append(path)

    try:
        for platform, name in [('windows', 'install.exe'), ('linux', 'my-vm-guest-linux-amd64')]:
            environment = dict(os.environ, GOOS=platform, GOARCH='amd64', CGO_ENABLED='0')
            flags = ['-ldflags=-H=windowsgui'] if platform == 'windows' else []
            target = STAGE / name
            run('mise', 'exec', 'go@1.27.2', '--', 'go', 'build', '-trimpath', *flags,
                '-o', str(target), './cmd/guest-tools', env=environment)
            staged.append(target)
        link(STAGE / 'install.exe', STAGE / 'my-vm-guest.exe')
        launcher = STAGE / 'install.py'
        shutil.copyfile(ROOT / 'scripts' / 'guest_media' / 'install.py', launcher)
        staged.append(launcher)
        text('install.desktop', '[Desktop Entry]\nType=Application\nName=Install MyVM Guest Enhancements\n'
             'Name[zh_CN]=安装 MyVM 客户机增强\nTerminal=true\n'
             'Exec=python3 -c "import runpy,sys,urllib.parse,pathlib; '
             "runpy.run_path(str(pathlib.Path(urllib.parse.unquote(urllib.parse.urlparse(sys.argv[1]).path)).with_name('install.py')), run_name='__main__')\" %k\n")
        text('autorun.inf', '[autorun]\nopen=install.exe\naction=Install MyVM Guest Enhancements\n')
        text('README.txt',
             'MyVM 离线客户机增强盘\n\nWindows 10/11 Pro: 双击 install.exe，以当前桌面用户授权 UAC。\n'
             'Ubuntu / Kubuntu 26.04、26.10 amd64: 在已登录的 GNOME/KDE Wayland 桌面打开终端，运行：\n'
             'python3 "/media/实际光盘路径/install.py"\n'
             '也可在文件管理器允许启动 install.desktop；若桌面阻止光盘启动器，请使用上述终端命令。\n\n'
             '安装包含本地依赖、共享目录、原生远程桌面证书和服务、自启动；不访问互联网仓库。\n'
             'GNOME 会交互询问远程桌面专用凭据；KDE 使用当前系统账户密码。\n'
             '安装后在主机点击“增强会话”，确认客户机证书并登录。\n'
             '保留 TLS/NLA；客户机桌面需保持已登录。桌面剪贴板与显示调整由原生远程服务协商。\n'
             'GPU-PV 仅在已验证的内核和宿主驱动组合下自动准备并运行 CUDA 自检。\n')
        linux = STAGE / 'linux'
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
        gpu = STAGE / 'gpu'
        directory(gpu)
        link(gpu_source / 'manifest.yaml', gpu / 'manifest.yaml')
        for source in gpu_files:
            relative = source.relative_to(gpu_source)
            parent = gpu
            for part in relative.parts[:-1]:
                parent /= part
                if not parent.exists():
                    directory(parent)
            link(source, gpu / relative)
        run('mise', 'exec', 'go@1.27.2', '--', 'go', 'build', '-trimpath',
            '-o', str(DIST / 'my-vm-cli.exe'), './cmd/my-vm-cli',
            env=dict(os.environ, GOOS='windows', GOARCH='amd64', CGO_ENABLED='0'))
        run('mise', 'exec', 'go@1.27.2', '--', 'go', 'build', '-trimpath',
            '-ldflags=-H=windowsgui', '-o', str(DIST / 'my-vm.exe'), './cmd/my-vm',
            env=dict(os.environ, GOOS='windows', GOARCH='amd64', CGO_ENABLED='0'))
        IMAGE.unlink(missing_ok=True)
        run(str(DIST / 'my-vm.exe'), '--build-guest-media', str(STAGE), str(IMAGE))
        os.replace(IMAGE, DIST / 'my-vm-guest-tools.iso')
    finally:
        for path in reversed(staged):
            path.unlink(missing_ok=True)
        for path in reversed(directories):
            path.rmdir()
        STAGE.rmdir()
        IMAGE.unlink(missing_ok=True)
    print(DIST / 'my-vm.exe')
    print(DIST / 'my-vm-cli.exe')
    print(DIST / 'my-vm-guest-tools.iso')


if __name__ == '__main__':
    main()
