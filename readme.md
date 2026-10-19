# My VM

<img src="assets/my-vm.png" alt="My VM 图标" width="72">

Windows 原生虚拟机管理器。管理界面使用 [MyGo](https://github.com/egoist/mygo) 和 [Onevra](https://github.com/oneo-me/onevra)，虚拟机后端为 Hyper-V Host Compute Service（HCS），控制台使用 Windows 原生 RDP ActiveX。仅实现 Windows x64 主机版本，可运行 Windows 与 Linux 客户机。

当前版本已经从 QEMU 迁移至 HCS，只使用硬件虚拟化。旧 QEMU/qcow2 清单和磁盘会保留，但无法直接启动，也不会自动转换、覆盖或删除。

## 已实现

- 原生虚拟机列表、搜索、分页、资源编辑、深浅色主题及未保存修改提示。右键虚拟机集中访问启停、画面、配置、复制、增强安装、快照和内存调整；概览只展示状态与设备。
- UEFI、动态 VHDX、安装光盘和客户机工具光盘；启停、暂停、继续、正常关机及强制断电。关闭管理器时可确认正常关闭全部虚拟机，待实际停机后自动退出。
- Hyper-V 默认交换机 NAT、桥接，或不连接网络的隔离模式；每台虚拟机最多 8 张网卡，可分别选择交换机，附加网卡可指定 MAC。
- 每台虚拟机独立的设备管理器，集中查看与配置 CPU、内存、磁盘、光盘、显示、GPU、网络和 COM1/COM2 命名管道串口。
- 全局设置提供运行环境、虚拟网络、主机网卡和数据存储；可创建和删除本程序管理的自定义 NAT 网络。
- 关机后复制已安装的 Windows/Linux，独立虚拟机和磁盘标识，默认断网并清除主机目录映射。
- Windows 10、Windows 11、Ubuntu 26 及其他 Windows/Linux 系统配置；已有清单保持兼容。
- 统一“打开画面”：先显示基础控制台，再在后台检查增强状态；仅在收到正面就绪信号后自动切换。右键“基础控制台（安装与维护）”保留基础模式。
- 客户机窗口顶部提供基础/增强切换、Ctrl+Alt+Del、全屏、暂停/继续和正常关机工具栏，关闭提示栏仍保留工具栏。
- 工具栏与 RDP 渲染分属不同进程，连接、分辨率同步和 COM 清理不在工具栏消息循环执行；增强连接期间保留已有基础画面。
- Linux 支持 GNOME 和 KDE Plasma 的 Wayland 当前桌面共享；旧 X11/xrdp 增强路径暂时停用，不安装 Xfce 或替换用户桌面；窗口画面自动缩放，客户机分辨率调整与剪贴板格式由原生服务决定。
- Windows 与 Linux 使用独立离线增强 ISO；直接运行安装器默认安装全部适用增强功能，无需 `--install`。Linux 盘内置 Ubuntu/Kubuntu 26.04、26.10 amd64 的依赖、证书与服务配置，Windows 盘包含安装器与宿主匹配 GPU-PV 驱动。虚拟机右键菜单的“安装增强能力”按客户机系统选择，支持运行中插入光盘。
- Windows 增强代理支持运行中升级和失败恢复，登录后显示托盘，可查看共享连接状态、开关自启动、打开日志或退出。
- 增强会话的文本、图片、文件复制粘贴能力取决于客户机服务；不会同时重定向宿主整盘。
- 选择主机文件夹映射，默认只读，可单独允许写入；Windows 自动分配空闲盘符，Linux 在文件管理器中挂载。
- 内存按需分配与运行中热调整；与 NanaBox 同路径的 HCS GPU-PV 共享开关。
- 独立命令行工具管理启停、快照、客户机命令执行和单文件双向复制。
- 关机状态的完整 VHDX 磁盘快照，创建、恢复、删除及分页查看；复制和快照显示后台进度，可继续管理其他虚拟机。
- YAML 配置及异步文件日志。

## 主机要求与构建

使用支持 HCS schema 2.5 或更高版本的 Windows x64 主机（Windows 11 专业版/企业版，或 Windows Server 2022 及以上），启用完整 Hyper-V 功能并重启；固件需开启 CPU 虚拟化。以管理员身份运行程序。`vmcompute` 和 `hns` 服务应可用；NAT 需要 Hyper-V 默认交换机。GPU 共享另需兼容 GPU、宿主和客户机驱动。

开发依赖使用 [mise](https://mise.jdx.dev/) 管理，构建脚本需要 Python 3：

```text
mise install go@1.27.2
mise install python@3.12
python -X utf8 "scripts/build.py"
```

输出：

```text
dist\my-vm.exe
dist\my-vm-cli.exe
dist\my-vm-guest-tools-windows.iso
dist\my-vm-guest-tools-linux.iso
```

不再需要安装 QEMU 或下载 virtio 驱动。Windows 和 Linux 工具 ISO 分别包含各自的客户机程序和离线载荷，不共用安装光盘。首次构建需联网收集 Ubuntu 软件包，使用 WSL 中的 Docker 解析闭合依赖；软件包和官方镜像由构建脚本通过代理下载，客户机安装只使用光盘内仓库。后续构建校验并复用 `dist\guest-payload` 缓存；不向客户机回退到联网安装。GPU-PV 载荷还需当前已验证宿主的匹配驱动文件，细节见 GPU 文档。

重新构建前关闭正在使用旧 EXE/工具光盘的程序和虚拟机。构建脚本使用 Go 工具 `go-winres` 生成 EXE 图标资源；图标源文件和无第三方 Python 依赖的生成脚本随项目保存，管理器与控制台窗口也使用同一图标。

## 使用

1. 退出仍在运行的旧版本，以管理员身份打开 `dist\my-vm.exe`，新建虚拟机，选择客户机类型和安装 ISO。
2. 设置 CPU、内存、磁盘及网络，创建后右键左侧虚拟机，选择“启动虚拟机”。Windows 安装光盘出现按键启动提示时及时按键；错过后可重启虚拟机。
3. 完成系统安装后关机，卸载安装 ISO。HCS 使用 Hyper-V 自带的存储、网络和显示设备。
4. 右键左侧虚拟机，选择“安装增强能力”，程序按客户机类型插入 Windows 或 Linux 增强盘。Windows 在光盘内双击 `install.exe`；Ubuntu/Kubuntu 在已登录的 GNOME/KDE Wayland 桌面运行光盘内 `install.py`。安装器完成离线依赖、远程桌面证书与服务、共享目录代理和用户自启动配置。
5. 需要主机文件夹时，在关机编辑页添加目录并选择只读/可写。普通“打开画面”先显示基础控制台，增强就绪后自动切换；工具栏也可手动选择模式。右键“基础控制台（安装与维护）”不自动升级，适合系统安装和维修。

客户机窗口顶部可直接切换基础/增强画面、发送 Ctrl+Alt+Del、全屏/还原、暂停/继续和正常关机。切换保留同一个宿主窗口和虚拟机；基础画面用于安装维护，增强画面支持桌面集成。暂停/继续会查询实际运行状态，关机需要确认，只发送正常关机请求，不自动强制断电。普通入口先显示基础画面，后台读取[客户机状态协议](docs/guest-protocol.md)，并在兼容路径确认增强已就绪后自动切换；未知、等待或不支持的状态继续保留基础。手动选择“基础”或维护入口会停用本窗口的自动升级；手动选择“增强”仍可尝试连接。认证、证书等待不会自动关窗。

左侧虚拟机和概览标题均可打开同一右键菜单；右键未选中的虚拟机时，操作作用于该项。聚焦列表项后也可按 `Shift+F10` 或菜单键。运行状态不允许的操作会灰显；“管理快照”和“调整运行内存”进入各自页面，强制断电与移除仍需确认。后台作业只占用对应虚拟机，其他项的菜单仍可使用。

关闭管理器时，如仍有运行或暂停的虚拟机，会先询问是否“正常关机并退出”。取消则保留应用；确认后后台同时发送正常关机请求，暂停的虚拟机先恢复。等待期间禁止新操作，全部实际关机并完成资源清理后自动退出。关机失败或五分钟等待期限到达时，应用保持打开并显示错误；不会自动强制断电。已有磁盘、配置或命令行操作须先完成。

旧清单已挂载的合并增强 ISO 保持原路径，升级时保留旧文件。需要切换时，重新点击“安装增强能力”或在关机编辑页挂载工具光盘，程序会选择对应系统的新光盘。

NAT 自动选网使用 Windows 保留标识对应的 `Default Switch`，不会把 WSL 的其他 ICS 网络当作默认交换机。Default Switch 提供 DHCP；找不到时明确报错。显式指定交换机时由该网络提供 DHCP 或客户机配置静态地址。若旧版本已接入 WSL 交换机，请正常关闭客户机并用新版重新启动，让网卡重新接入正确网络；无需重装系统。

左侧“全局设置”管理虚拟网络、查看宿主网卡与数据位置；虚拟机右键“设备管理器”只作用于该台。关机时可保存硬件变更，运行或暂停时只读查看；网络和 COM 配置在下次启动时生效。自定义 NAT 不提供 DHCP，需在客户机配置静态地址、网关和 DNS；Windows Default Switch 继续用于自动分配地址。详细操作与串口限制见 [device-management.md](docs/device-management.md)。

完整操作见 [guest-setup.md](docs/guest-setup.md)。Ubuntu 26 新配置默认使用 Wayland。GNOME/KDE 新安装均自动生成独立 RDP 凭据，分别存入密钥环或 KWallet，再由该 VM 的私有通道交给宿主管理器，按 VM 保存到 Windows 凭据管理器；无需首次输入 Linux 系统密码或手动勾选保存。旧 GNOME 凭据也可从当前密钥环复用。客户机需先由新版管理器启动，再运行新版 Linux 增强安装器；无需共享目录或互联网。密钥环/钱包授权、未知证书确认、客户机登录与解锁仍由用户完成。自动入口以当前代理、服务和桌面状态选择，探测失败使用基础控制台；旧 `guest_os: linux` 可识别当前 X11/Wayland，显式桌面选择仍受尊重。细分系统类型只记录配置，不会修改或重新安装现有客户机，也不会自动补装 Windows 11 TPM 等硬件要求。

命令行示例见 [command-line.md](docs/command-line.md)。Windows 10、Ubuntu 26 自动安装的可行方案及交付前条件见 [automatic-installation.md](docs/automatic-installation.md)，本轮尚未提供无人值守安装复选框。

GPU 的实际通过环境及可复现步骤见 [linux-gpu.md](docs/linux-gpu.md)，验证范围见 [validation.md](docs/validation.md)。

已有安装可以直接复用，不需要每轮测试重新安装。关机后从虚拟机右键菜单选择“复制虚拟机”，确认副本配置并创建：副本保留系统磁盘和 UEFI 固件状态，使用新的虚拟机 ID 和 VHDX 容器 ID，不继承快照及运行状态，也不再挂载原来的安装光盘。原机及其安装保留。

副本内部的账户、主机名、静态 IP、Windows SID 或 Linux machine-id 仍来自原机；复制不能替代客户机系统封装。默认关闭副本网络并清除主机共享目录，联网前按客户机用途准备系统身份，例如 Windows Sysprep 或 Linux machine-id/主机名配置。若在副本编辑页主动开启网络或添加共享目录，首次启动就会按所选配置连接。

共享目录使用绑定到该虚拟机 RuntimeId 的 Hyper-V Socket 私有通道，宿主不开放 TCP 文件共享端口。客户机工具仅在本机 `127.0.0.1:18389` 提供 WebDAV 适配；不会转发任意主机网络地址。只读限制在宿主端执行，可写目录的操作会直接修改主机文件。

Wayland 控制台通过临时的宿主 `127.0.0.1` 端口，将带 TLS 与认证的 RDP 流量送入该 VM 的 `vsock:3390`，再由工具连接客户机本机 GNOME 或 KDE 远程桌面端口；与文件夹映射共用一套工具。关闭控制台即关闭宿主转发器。GNOME 自身的 RDP 监听和防火墙策略由客户机管理。

## 内存、GPU 与快照的边界

“内存按需分配”对应 HCS `AllowOvercommit`，同时允许在线修改目标内存。它不是自动按最小/最大值调整的控制器，实际增减及回收受客户机 Hyper-V 内存驱动影响。运行中的调整不修改下次启动的配置值。

GPU 开关默认关闭，必须为每台虚拟机单独启用；已有清单按自身的 `gpu_shared` 值运行。GPU 开关沿用 NanaBox 同类的 HCS GPU-PV 路径，启动后更新 `VirtualMachine/ComputeTopology/Gpu`，使用 `AssignmentMode: Default`、`AllowVendorExtension: true` 分配默认 GPU；NanaBox 的 `Mirror` 则投射所有 GPU。本项目不使用 `List` 固定分区 ID，也不依赖 PowerShell 配置 GPU-P 或 DDA。失败会报错。原版 Ubuntu 不自带所需 `dxgkrnl`，增强盘仅对已验证的内核和宿主驱动组合自动准备，并运行真实 CUDA 自检；不满足条件时明确跳过 GPU，其余增强仍可安装。兼容范围见 [linux-gpu.md](docs/linux-gpu.md)。

Windows GPU-PV 还需客户机具备宿主匹配的用户态驱动包。新版增强盘会在基础控制台安装完整 HostDriverStore 和声明的补充文件，并重载设备；宿主驱动更新后需重新构建介质。现有 Win10 的 Code 43 已通过补齐驱动消除，但 616.92 组合的 D3D/CUDA 初始化仍失败，硬件加速尚未验收通过，见 [验证记录](docs/validation.md)。

快照是关机后的完整磁盘文件副本，每台最多 16 个；不含 RAM、UEFI 变量或运行状态，也不是 Hyper-V 完整检查点。创建和恢复需要额外磁盘空间，恢复前会确认。系统安装或固件设置发生变化时，应保留相应安装记录和配置。

复制和快照创建、恢复都需要读写整个 VHDX 文件，耗时随实际文件大小和存储速度增加。界面显示已复制容量和百分比，操作在后台进行；同一台虚拟机在作业完成前不能启动或修改磁盘，其他虚拟机仍可查看、编辑和启停。启动、关机、暂停、控制台、内存调整、快照读取及增强光盘操作均在后台执行，只占用对应虚拟机；期间侧栏、设置和其他虚拟机仍可操作。不同虚拟机可同时执行磁盘作业；复制会共享主机磁盘带宽。配置落盘通过单一后台队列合并最新清单，避免并发保存相互覆盖。

Windows WebClient 单文件上限约 4 GiB，工具会配置系统允许的最大值；如服务此前已启动，变更后可能需要重启客户机。为避免覆盖时丢失目标文件，服务拒绝覆盖式 COPY/MOVE、目录 COPY 和非空目录递归删除；普通文件 PUT 覆盖使用临时文件完整写入后再替换。不是完整 SMB 语义，也不适合作为数据库目录。

## 数据和日志

```text
%APPDATA%\my-vm\library.yaml
%APPDATA%\my-vm\networks.yaml
%APPDATA%\my-vm\machines\<id>\disk.vhdx
%APPDATA%\my-vm\machines\<id>\guest.vmgs
%APPDATA%\my-vm\machines\<id>\runtime.vmrs
%APPDATA%\my-vm\machines\<id>\snapshots\<name>\disk.vhdx
<程序目录>\logs\my-vm.log
```

客户机工具日志位于用户配置目录的 `MyVMGuestTools\logs`。Windows 为 `%APPDATA%`，Linux 为 `$XDG_CONFIG_HOME` 或 `~/.config`。配置损坏或含未知字段时阻止覆盖原清单。从列表移除只删除登记，保留磁盘和快照。

关闭控制台窗口不会关闭虚拟机；管理器要求先关闭虚拟机再退出。正常关机通过 Hyper-V 关机集成服务发送非强制请求，需要客户机的集成驱动/服务可用；不支持或被客户机拒绝时会报错，不会自动强制断电。管理器进程异常退出时，最后一个 HCS 句柄关闭会终止其虚拟机，效果等同断电。

## 测试

```text
mise exec go@1.27.2 -- go test ./...
mise exec go@1.27.2 -- go test -race ./...
mise exec go@1.27.2 -- go vet ./...
```

设置环境变量 `MY_VM_INTEGRATION=1` 后运行 `go test ./internal/vm -run TestHCSLifecycle -v -count=1`，会创建独立临时 VHDX，实际测试 HCS 启停、暂停、继续、共享通道注册及运行中快照拦截；需管理员权限和 Hyper-V。

MyGo `ui.NewTester` 在内存中验证导航、搜索分页、表单、禁用状态、主题和最小布局；原生文件/确认对话框、RDP、GPU 及真实文件粘贴另以实际系统验证。当前不包含 macOS/Linux 主机版本、旧 qcow2 自动转换、内存快照、自动 OS 安装、任意硬件与内核的 GPU 驱动安装或外部桥接交换机创建。

日常系统回归复用 `%APPDATA%\\my-vm\\library.yaml` 中已安装的 Windows、Ubuntu；完成后关机并保留安装，不删除重装。设置环境变量 `MY_VM_REUSE=1` 后运行 `go test ./internal/vm -run TestInstalledVMClones -v -count=1`，会读取现有的离线安装，在临时目录创建完整副本，验证磁盘新标识、固件状态、进度和原盘元数据未变；不修改原清单，不启动或删除原机。测试临时目录须与原盘位于同一卷，并预留至少最大单台 VHDX 大小的可用空间；执行前先正常关闭客户机。若同时设置 `MY_VM_INTEGRATION=1`，还会对临时副本验证 HCS 启动、暂停和继续，随后停止临时副本；原机始终不启动。普通单元测试仍使用小型临时 VHDX，不涉及系统重装。

实现参考 [NanaBox](https://github.com/M2Team/NanaBox) 的 HCS、RDP、内存和 GPU 路径；未使用其图标资源。
