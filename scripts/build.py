"""Build the manager and portable Windows/Linux enhancement discs with mise Go."""
import os
from pathlib import Path
import shutil
import subprocess

from build_icon import main as build_icon
from guest_media.validate import verified_files

ROOT = Path(__file__).resolve().parents[1]
DIST = ROOT / 'dist'


def run(*args, env=None):
    subprocess.run(args, cwd=ROOT, env=env, check=True, stdin=subprocess.DEVNULL,
                   creationflags=subprocess.CREATE_NO_WINDOW if os.name == 'nt' else 0)


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
        run('mise', 'exec', 'go@1.27.3', '--', 'go', 'build', '-trimpath', *flags,
            '-o', str(target), './cmd/guest-tools', env=environment)
        staged.append(target)
        if platform == 'windows':
            link(stage / 'install.exe', stage / 'my-vm-guest.exe')
            text('autorun.inf', '[autorun]\nopen=install.exe\naction=Install MyVM Guest Enhancements\n')
            text('README.txt',
                 'MyVM Windows 离线客户机增强盘\n\n'
                 'Windows 10/11 Pro: 双击 install.exe 或 my-vm-guest.exe，默认安装全部适用增强功能，无需参数。\n'
                 '请使用当前桌面用户运行并完成 UAC 授权。\n'
                 '安装共享目录、远程桌面服务和登录自启动；不访问互联网仓库。\n'
                 'GPU-PV 首次准备或驱动升级请在基础控制台运行；已有驱动匹配且设备正常时可在增强会话重复安装。\n'
                 '安装后在主机点击“打开画面”，已就绪时自动使用增强；账户需设置登录密码。\n'
                 'GPU 设备代码正常不代表硬件图形/计算已通过，需按实际驱动和硬件验证。\n')
        else:
            launcher = stage / 'install.py'
            shutil.copyfile(ROOT / 'scripts' / 'guest_media' / 'install.py', launcher)
            staged.append(launcher)
            text('install.desktop', '[Desktop Entry]\nType=Application\nName=Install MyVM Guest Enhancements\n'
                 'Name[zh_CN]=安装 MyVM 客户机增强\nTerminal=true\n'
                 'Exec=python3 -c "import runpy,sys,urllib.parse,pathlib; '
                 "runpy.run_path(str(pathlib.Path(urllib.parse.unquote(urllib.parse.urlparse(sys.argv[1]).path)).with_name('install.py')), run_name='__main__')\" %k\n")
            text('README.txt',
                 'MyVM Linux 客户机增强盘（联网安装依赖）\n\n'
                 'Ubuntu / Kubuntu 26.04、26.10 amd64: 在已登录的 GNOME/KDE Wayland 桌面终端运行：\n'
                 'python3 "/media/实际光盘路径/install.py"\n'
                 '默认安装全部适用增强功能，无需 --install；请勿使用 sudo，在授权窗口确认管理员权限。\n'
                 '也可在文件管理器允许启动 install.desktop；若桌面阻止光盘启动器，请使用上述终端命令。\n'
                 '请保持客户机联网，安装器从系统 APT 软件源下载当前桌面所需依赖，不捆绑 Ubuntu 软件包。\n'
                 '网络或软件源错误会中止安装并明确报错；修复后可重新运行安装器。\n'
                 '安装共享目录、远程桌面证书和服务、自启动；保留系统软件源与签名校验。\n'
                 'GNOME/KDE 自动生成独立连接凭据，分别存入密钥环/KWallet，并按虚拟机回传保存到宿主凭据管理器。\n'
                 '无需输入 Linux 系统密码；KDE 首次可能需解锁钱包并允许 KRDP 访问。\n'
                 '请先由新版管理器启动客户机；宿主保存失败时会报错，可修正后重新安装。\n'
                 'GNOME 增强连接临时使用远程虚拟屏作为唯一主屏，分辨率随窗口调整；断开后恢复原显示布局。\n'
                 'KDE 当前版本只适配画面，不动态调整客户机分辨率。\n'
                 '安装后关闭安装用窗口，在主机点击“打开画面”；无需首次手输连接密码，仍需核对未知证书。\n'
                 '保留 TLS 与认证，桌面需保持已登录；剪贴板与显示调整由原生服务协商。\n'
                 'GPU-PV 仅在已验证的内核和宿主驱动组合下自动准备并运行 CUDA 自检。\n')
            source = DIST / 'guest-payload' / 'gpu-source'
            payload(source, payloads['gpu-source'], stage / 'gpu')
        text('GPU-HOST-DRIVERS.txt',
             'Host-specific GPU drivers are collected by My VM when installing enhancements with GPU sharing enabled.\n'
             'The original release image remains unchanged; collection failures are reported before mounting.\n'
             'Linux GPU-PV retains its supported NVIDIA and guest-kernel requirements.\n')
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
    payloads = {}
    source = DIST / 'guest-payload' / 'gpu-source'
    if not (source / 'manifest.yaml').is_file():
        run('python', '-X', 'utf8', str(ROOT / 'scripts' / 'guest_media' / 'gpu.py'), '--source-only')
    payloads['gpu-source'] = verified_files(source)
    build_icon()
    run('mise', 'exec', 'go@1.27.3', '--', 'go', 'run',
        'github.com/tc-hib/go-winres@v0.3.3', 'simply', '--arch', 'amd64',
        '--manifest', 'none', '--icon', str(ROOT / 'assets' / 'my-vm.ico'),
        '--out', str(ROOT / 'cmd' / 'my-vm' / 'rsrc'))
    run('mise', 'exec', 'go@1.27.3', '--', 'go', 'build', '-trimpath',
        '-o', str(DIST / 'my-vm-cli.exe'), './cmd/my-vm-cli',
        env=dict(os.environ, GOOS='windows', GOARCH='amd64', CGO_ENABLED='0'))
    run('mise', 'exec', 'go@1.27.3', '--', 'go', 'build', '-trimpath',
        '-ldflags=-H=windowsgui', '-o', str(DIST / 'my-vm.exe'), './cmd/my-vm',
        env=dict(os.environ, GOOS='windows', GOARCH='amd64', CGO_ENABLED='0'))
    for platform in ('windows', 'linux'):
        print(build_disc(platform, payloads))
    print(DIST / 'my-vm.exe')
    print(DIST / 'my-vm-cli.exe')


if __name__ == '__main__':
    main()
