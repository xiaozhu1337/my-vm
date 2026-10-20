[English](README.md) · [下载](https://github.com/xiaozhu1337/my-vm/releases/latest)

# My VM

<img src="assets/my-vm.png" alt="My VM 图标" width="72">

基于 **Windows Hyper-V Host Compute Service（HCS）** 的原生虚拟机管理器，在同一应用中管理 Windows 和 Linux 客户机，提供硬件虚拟化、运行中磁盘快照和 Linux 桌面增强。

## 项目优势

- **原生 Hyper-V 性能。** 直接使用 Windows 虚拟化栈及其 CPU、内存、存储与网络设备，不需要 QEMU 运行时。
- **运行中快照与分支。** 客户机继续运行时创建 VHDX 差分快照，后台显示进度；恢复旧节点后可创建新分支，无需复制整个系统盘。
- **Linux 增强桌面。** 连接现有 GNOME/KDE Plasma Wayland 桌面，GNOME 支持动态分辨率，KDE 适配窗口画面，无需更换桌面或安装 Xfce。
- **统一管理 Windows 与 Linux。** 集中管理虚拟硬件、NAT/桥接网络、安装光盘、独立副本以及基础/增强控制台。
- **按虚拟机隔离的目录共享。** 只共享选定的主机目录，默认只读，使用 Hyper-V Socket 私有通道，不向宿主网络开放 TCP 文件共享端口。
- **命令行自动化。** 独立 CLI 支持虚拟机启停、快照、客户机命令执行及单文件双向传输。
- **后台操作保持界面可用。** 控制台渲染采用独立进程；后台作业只占用对应虚拟机，其他虚拟机仍可管理。
- **轻量增强盘。** Windows/Linux 使用各自的增强盘；Linux 支持 Ubuntu/Kubuntu 26.04、26.10 amd64，安装时从客户机已配置的 APT 软件源下载所需依赖。

## 界面截图

以下图片由应用的真实界面渲染，使用示例虚拟机配置与快照数据。当前应用界面为简体中文。

**虚拟机库与硬件概览**

![虚拟机库与硬件概览](assets/screenshots/overview.png)

**差分快照分支**

![使用示例数据的快照树](assets/screenshots/snapshots.png)

**每台虚拟机独立的设备管理器**

![虚拟机设备管理器](assets/screenshots/devices.png)

## 下载与运行

从 [Releases](https://github.com/xiaozhu1337/my-vm/releases/latest) 下载 **`my-vm-windows-amd64.zip`**，一个压缩包包含：

```text
my-vm/
  my-vm.exe
  my-vm-cli.exe
  my-vm-guest-tools-windows.iso
  my-vm-guest-tools-linux.iso
  README.md
  README.zh-CN.md
  THIRD-PARTY-NOTICES.txt
  licenses/
  SHA256SUMS.txt
```

完整解压到可写目录，保持两个增强 ISO 与程序位于同一目录，**以管理员身份运行 `my-vm.exe`**。下载页同时提供 ZIP 的 SHA-256 校验文件。

### 主机要求

- Windows x64，支持 HCS schema 2.5 或更高版本，例如 Windows 11 专业版/企业版、Windows Server 2022 及以上。
- 固件启用 CPU 虚拟化，安装完整 Hyper-V 功能并重启。
- `vmcompute`、`hns` 服务可用；自动 NAT 使用 Hyper-V 的 **Default Switch**。
- 在线快照还需要宿主支持原生磁盘分叉 API。不支持时会明确报错，不会自动退回全盘复制。

### 创建第一台虚拟机

1. 新建虚拟机，选择 Windows 或 Linux 安装 ISO，设置 CPU、内存、磁盘和网络。
2. 右键虚拟机启动，通过基础控制台正常安装操作系统。
3. 从右键菜单选择“安装增强能力”，挂载对应系统的增强盘。
4. Windows：用已登录桌面用户运行光盘内的 `install.exe`，完成管理员授权。增强会话需要支持 RDP 服务端的客户机版本，以及设置了登录密码的账户。
5. Ubuntu/Kubuntu：先确保客户机能够联网，在已登录的 GNOME/KDE Wayland 桌面终端运行 `python3 "/实际光盘路径/install.py"`。使用当前桌面用户运行，不加 `sudo`，在授权提示中确认权限。安装沿用客户机的 APT 软件源和签名校验；需要代理时请配置客户机的系统 APT 代理。下载或安装失败会中止并报错，修复网络或软件包问题后可重新运行。
6. 重新打开画面。自动模式先显示基础控制台，再按可用状态尝试增强连接；也可通过工具栏手动切换。

GNOME/KDE 工具会生成独立连接凭据，通过虚拟机私有通道交换。密钥环/钱包授权、未知证书确认、客户机登录与解锁仍可能需要用户操作。剪贴板支持的格式由客户机桌面服务决定。

启用 GPU 共享后，选择“安装增强能力”会先从**当前宿主机复制驱动**，生成本地专用增强盘再挂载。Release 不包含某台宿主的 GPU 驱动二进制。缺少文件、复制或校验失败、准备期间宿主驱动变化，都会停止并明确报错，保留原光盘；暂存文件和新 ISO 需要额外磁盘空间。Linux GPU-PV 当前限于计算能力 8.0 以上的 NVIDIA 显卡及受支持的 `7.3.0-9-generic` 客户机内核，自动收集驱动不代表所有硬件都兼容。

Windows 专用盘会排除 Linux 库、仅宿主使用的内核驱动、安装器、缓存和日志；保留厂商运行库与数据文件，因此仍可能占用数 GiB。管理器只自动回收本次运行中生成且已无引用的光盘，之前运行留下的已有光盘会保留。

关闭控制台不会关闭虚拟机。关闭管理器时，可确认正常关闭全部运行中的虚拟机，管理器会等待实际关机后退出。

## 快照保存什么？

快照保存**磁盘状态**，不包含 RAM、UEFI 变量或程序运行现场。运行中创建得到崩溃一致性的磁盘状态，不等于数据库应用一致性备份。

- 支持运行中创建和删除；创建时将后续写入切换到新差分盘。
- 恢复会重启正在运行的客户机；暂停的客户机恢复后仍暂停，关机的客户机保持关机。确认恢复后，当前分支尚未保存为快照的修改会被放弃。
- 每台最多 16 个命名恢复点。删除名称后，仍被其他分支或活动磁盘依赖的层会保留，不保证立即释放空间。
- 快照依赖父盘，应完整保存磁盘链和 `storage.yaml`；独立备份应另行复制。目前未实现在线磁盘链合并。

## 命令行

保持管理器运行，在解压目录使用 CLI。虚拟机名称或 ID 可通过 `list` 查询：

```text
my-vm-cli.exe list
my-vm-cli.exe start "Ubuntu Desktop"
my-vm-cli.exe snapshot create "Ubuntu Desktop" before-update
my-vm-cli.exe snapshot list "Ubuntu Desktop"
my-vm-cli.exe snapshot restore "Ubuntu Desktop" before-update
my-vm-cli.exe snapshot delete "Ubuntu Desktop" before-update
my-vm-cli.exe exec "Ubuntu Desktop" /usr/bin/uname -a
my-vm-cli.exe copy-to "Ubuntu Desktop" "C:\Work\hello.txt" /home/user/hello.txt
my-vm-cli.exe --help
```

客户机命令和文件传输需要已安装并运行增强工具；快照与普通启停操作由宿主后端完成。

## 从源码构建

开发工具使用 [mise](https://mise.jdx.dev/) 管理，在 Windows x64 上执行：

```text
mise install go@1.27.2
mise install python@3.12
mise exec go@1.27.2 -- go build -trimpath -ldflags=-H=windowsgui -o "dist/my-vm.exe" ./cmd/my-vm
mise exec go@1.27.2 -- go build -trimpath -o "dist/my-vm-cli.exe" ./cmd/my-vm-cli
```

需要同时构建通用增强盘时：

```text
mise exec python@3.12 -- python -X utf8 "scripts/build.py"
```

首次制作光盘会下载通用 GPU 模块源码和许可文本，之后校验并复用 `dist/guest-payload` 缓存，不再收集 Ubuntu 软件包，也不需要 Docker/WSL。构建遵循 `HTTP_PROXY`/`HTTPS_PROXY` 环境设置。Linux 客户机安装增强时需要联网；GPU-PV 另需兼容客户机内核和与宿主匹配的驱动，支持范围取决于实际硬件。

```text
mise exec go@1.27.2 -- go test ./...
mise exec go@1.27.2 -- go vet ./...
```

## 数据与适用范围

YAML 配置保存在 `%APPDATA%\my-vm`；错误异步写入程序同目录的 `logs\my-vm.log`。从列表移除虚拟机保留其磁盘和快照。

当前只支持 **Windows x64 宿主机**，暂不提供内存快照、无人值守系统安装、qcow2 自动转换或通用 GPU 兼容保证。管理器异常退出会终止其 HCS 虚拟机，因此应正常关闭客户机后再退出宿主会话。

## 致谢

界面使用 [MyGo](https://github.com/egoist/mygo) 与 [Onevra](https://github.com/oneo-me/onevra)；HCS、RDP、内存与 GPU 实现参考 [NanaBox](https://github.com/M2Team/NanaBox)。项目图标由仓库内的脚本生成。
