# My VM

类似 VMware 布局的虚拟机管理器，采用 [MyGo](https://github.com/egoist/mygo) + [Onevra](https://github.com/oneo-me/onevra) 原生 UI，底层使用 QEMU 和平台硬件加速。当前首版仅实现 **Windows x64**。

## 已实现

- 虚拟机库：创建、搜索、分页、编辑，以及从列表移除；移除保留磁盘和快照。
- 工作台布局：固定侧栏、全部/已启动筛选、概览/磁盘快照标签页、资源卡片、跟随系统的明暗配色与底部操作状态。
- 按运行状态突出启动、打开画面或继续运行；强制断电和移除集中在“更多操作”，离开未保存的编辑或设置时提示确认。
- 配置 CPU、内存、qcow2 磁盘、安装 ISO、独立驱动 ISO、网络与加速方式。
- 高清 virtio 显卡和标准 VGA 可选；Windows 安装客户机驱动及分辨率服务后可按窗口大小调整桌面分辨率。QEMU SDL 窗口启用每显示器 DPI 感知，避免宿主系统把整幅画面模糊放大。
- 启动、ACPI 正常关机、暂停、继续与确认后强制断电。
- 默认使用 WHPX 硬件加速；可明确选择 TCG 软件模拟。
- NAT 网络，以及使用现有 TAP-Windows 网卡的桥接网络。
- 关机状态下创建、列出、恢复和删除 qcow2 内部磁盘快照；快照名以英文字母开头，可含数字、下划线或连字符，最长 64 字符，避免 QEMU 数字 ID 与名称歧义。
- 带字段说明的 YAML 配置、损坏配置保护、异步文件日志。
- Windows Job Object 进程保护：管理器退出后，所属 QEMU 进程一并结束。

客户机画面显示在独立的 QEMU SDL 窗口，管理器负责配置与生命周期。程序运行期间禁止关闭管理窗口，直到当前操作完成且全部虚拟机已退出。

## 环境准备

需要 Windows 10/11 x64、Go 1.27.2 和包含 SDL 显示后端的 Windows QEMU。开发工具统一通过 [mise](https://mise.jdx.dev) 管理：

```sh
mise install go@1.27.2
```

QEMU 是外部运行时，需要提供**包含 `qemu-system-x86_64.exe` 和 `qemu-img.exe` 的安装目录**，不打包进管理器。当前实现复用本机已有的 QEMU 11.1.0 开发构建，尚未验证稳定发行版的安装流程；已有 QEMU 可直接复用，无需重复安装。

在 Windows“启用或关闭 Windows 功能”中开启 **Windows Hypervisor Platform**，并在 BIOS/UEFI 中开启 CPU 虚拟化；系统要求时重启。启动 WHPX 失败会直接显示错误，不会自动改用 TCG。

若开发环境访问外网需要代理，在终端为当前进程设置 `HTTP_PROXY`、`HTTPS_PROXY` 为 `http://192.168.1.99:7890` 后下载 Go 模块。程序不需要联网下载客户机镜像。

## 运行与构建

在仓库目录执行：

```sh
mise exec go@1.27.2 -- go run ./cmd/my-vm
mise exec go@1.27.2 -- go build -ldflags="-H=windowsgui" -o "dist/my-vm.exe" ./cmd/my-vm
```

构建后运行 `dist\my-vm.exe`，点击“QEMU 设置”，填写 QEMU 安装目录；两个 EXE 已在 PATH 中时可以留空。

创建虚拟机时选择 ISO 并保存，再点击“启动虚拟机”。默认使用 2 个 CPU、4096 MiB 内存、64 GiB 按需分配磁盘、WHPX、NAT 和标准 VGA 显卡。使用标准 SATA 磁盘与 Intel E1000 网络设备。安装完成后关机，在“编辑配置”中卸载 ISO，后续从硬盘启动。

运行时点击“打开画面”可恢复被最小化或隐藏的 QEMU 客户机窗口。暂停后主按钮变为“继续运行”，恢复后才能正常关机。进入“磁盘快照”标签页时会在关机状态下读取快照；也可以手动刷新。

“正常关机”只发送 ACPI 请求，需要客户机系统响应。强制断电可能丢失未保存的数据。

## Windows 高清显示

新建虚拟机默认保留 **标准 VGA**，先确保系统安装和驱动预装时有画面；未装驱动直接使用 virtio 时，部分 Windows 启动阶段可能显示 `Display output is not active`。旧版配置缺少 `display` 时也继续使用标准 VGA，不会自动替换已有客户机的硬件。完成下面的驱动预装后，再关机切换到 **高清 virtio 显卡**。

这条显示链路需要 QEMU SDL 将窗口尺寸通知 virtio GPU，再由客户机驱动和分辨率服务调整 Windows 桌面。**只有驱动、没有服务时，拖动窗口通常仍只是缩放现有画面。** 当前使用本机 QEMU 11.1.0 开发构建验证，旧版 Windows QEMU 的支持取决于其 SDL/virtio 实现，不能保证同样生效。

1. 从 [virtio-win 官方下载说明](https://github.com/virtio-win/virtio-win-pkg-scripts/blob/master/README.md#downloads)获取 stable 驱动 ISO。本次使用 [virtio-win 0.1.302-1](https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/archive-virtio/virtio-win-0.1.302-1/virtio-win-0.1.302.iso)。
2. 用标准 VGA 完成 Windows 安装，关机后在编辑页挂载“驱动光盘 ISO”；它是独立的第二张只读光盘，可以和 Windows 安装 ISO 同时使用。暂时保留标准 VGA，启动客户机后预装驱动和服务。
3. 在 **Windows 客户机的管理员 CMD** 中执行以下命令。示例假设驱动光盘为 `E:`，请按客户机中的实际盘符修改；Win10 x64 使用 `w10\amd64`，其他系统需选择对应目录。

```bat
pnputil /add-driver "E:\viogpudo\w10\amd64\viogpudo.inf" /install
copy /y "E:\viogpudo\w10\amd64\vgpusrv.exe" "C:\Windows\System32\vgpusrv.exe"
copy /y "E:\viogpudo\w10\amd64\viogpuap.exe" "C:\Windows\System32\viogpuap.exe"
"C:\Windows\System32\vgpusrv.exe" -i
sc query vgpusrv
tasklist /fi "IMAGENAME eq viogpuap.exe"
```

4. 标准 VGA 下没有匹配的 virtio 设备时，`pnputil` 将驱动暂存到驱动库；安装成功后关机，在编辑页选择“高清 virtio 显卡”，再启动并登录 Windows 桌面。确认 `vgpusrv` 为 `RUNNING`、桌面会话中有 `viogpuap.exe`，随后拖动 QEMU 窗口边缘，在客户机“显示设置”中检查分辨率是否变化。仅在登录界面测试、仅看画面是否被拉伸都不能证明自动分辨率工作。若无法显示，先关机切回标准 VGA，再检查驱动安装结果。

驱动安装和服务注册是两步；`vgpusrv.exe -i` 的退出码不足以证明服务已正常运行，应以上述查询结果为准。服务和代理复制到系统目录后，可关机卸载驱动 ISO。使用没有兼容驱动的旧系统时选择标准 VGA。高分辨率下文字太小时，在客户机自己的 Windows“显示设置”中调整缩放比例，例如 150% 或 200%。

本功能针对单显示器桌面清晰度与自动分辨率，不提供 Windows 3D 加速、剪贴板共享、文件共享或嵌入式控制台，也不需要 RDP 账户和端口。

## 网络

**NAT** 使用 QEMU 用户网络，无需管理员权限和额外网卡，可用于客户机访问外网。当前未提供端口转发和主机访问客户机服务的配置。

**桥接** 使用 Windows TAP 后端。需要先准备兼容的 TAP-Windows 网卡，在 Windows“网络连接”中把它和物理网卡建立网络桥，然后在虚拟机配置中填入 TAP 网卡的准确名称。管理器不安装驱动、不修改主机网络。Hyper-V 的 Default Switch、VMware VMnet 网卡不能直接当作 TAP 使用；无线网卡能否桥接取决于驱动及网络环境。

## 配置与数据

配置保存在 `%APPDATA%\my-vm\library.yaml`，保存时通过临时文件替换原文件，每个字段附有说明。损坏或不合法的配置会显示错误，并禁用修改操作，不覆盖原文件；修复后重启程序。

磁盘位于 `%APPDATA%\my-vm\machines\<id>\disk.qcow2`，内部快照保存在同一 qcow2 文件中。从列表移除只取消登记，仍可通过备份的 YAML 恢复登记；当前没有磁盘导入和永久删除功能。

错误与 QEMU 输出异步写入 **EXE 所在目录** 的 `logs\my-vm.log`；因此请把 EXE 放在可写目录。日志持续追加，当前不自动轮转。`go run` 的 EXE 位于 Go 的临时构建目录，日常使用建议运行构建后的 EXE 以保留日志。

Windows QEMU 构建对镜像文件的独占锁支持不一致。管理器会串行执行磁盘操作，并拒绝对运行中虚拟机执行快照操作；**不要在虚拟机运行时通过外部 QEMU、qemu-img 或其他工具修改同一磁盘**。管理器异常退出时 Job Object 会结束客户机，这相当于断电，仍可能造成客户机未保存的数据丢失。

## 验证

```sh
mise exec go@1.27.2 -- go test ./...
mise exec go@1.27.2 -- go vet ./...
```

普通测试涵盖参数与路径处理、两张只读 ISO、高清显卡及旧配置兼容、YAML 持久化、未知字段与多文档拒绝、QMP 事件、错误及超时后控制恢复、并发日志；PATH 中存在 `qemu-img` 时还会用临时 qcow2 磁盘执行快照创建、恢复和删除测试，包括数字名称的拒绝保护。

界面测试使用 MyGo 的 `ui.NewTester` 在内存中运行，**不会打开窗口或启动虚拟机**，验证页面切换、搜索、筛选、虚拟机与快照分页、名称编辑、显卡切换和驱动光盘输入/卸载、未保存修改检测、忙碌/暂停/运行中快照的操作禁用，以及明暗主题下的最小窗口布局。原生文件选择框、系统确认对话框和真实 QEMU 画面需另行进行实窗验证。仅运行界面与表单测试：

```sh
mise exec go@1.27.2 -- go test ./cmd/my-vm -v
```

真实启动测试需设置环境变量 `MY_VM_INTEGRATION=1` 后执行：

```sh
mise exec go@1.27.2 -- go test ./internal/vm -run TestQEMULifecycle -v -count=1
```

该测试会短暂打开 TCG、WHPX 与标准 VGA、virtio 两种显卡的四组 QEMU 窗口，使用临时空磁盘验证 SDL 客户机窗口可见性、每显示器 DPI 感知、隐藏后重新打开画面、启动、重复启动保护、暂停、继续、关机请求、强制退出及运行中快照保护，不会安装操作系统或更改主机网络。WHPX 不可用时该项会失败。程序关闭未使用的 HMP、串口和并口控制台，测试还会检查每个进程只有一个 SDL 客户机窗口，避免“打开画面”误打开隐藏的辅助窗口。

另已用 Windows 10 22H2 x64 安装镜像创建独立测试客户机，在 WHPX、QEMU 11.1.0、virtio-win 0.1.302-1 下完成驱动预装、关机切换显卡、服务运行和真实桌面分辨率验证。调整 SDL 客户区后，Windows WMI 当前显示模式和 QEMU `screendump` framebuffer 尺寸在 **1280×720、1600×900、1920×1080、2560×1440、3840×2119** 下均一致，包括缩小后恢复低分辨率；最后一项是本机工作区允许的实际窗口客户区，未将其当作标准 4K 全屏。此安装验证与上述空盘自动测试分开，未修改已有虚拟机的数据。尚未验证多显示器之间的 DPI 切换或其他 Windows/QEMU/驱动版本组合。

## 首版范围

暂未实现 macOS/Linux、客户机画面嵌入、UEFI/TPM/Secure Boot、Windows 11 安装要求的自动配置、内存/在线快照、多磁盘、磁盘扩容、磁盘导入、安装器和 QEMU 自动下载。部分客户机需要其他设备或固件配置，当前不提供任意 QEMU 参数输入。
