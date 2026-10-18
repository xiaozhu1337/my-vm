# My VM

类似 VMware 布局的虚拟机管理器，采用 [MyGo](https://github.com/egoist/mygo) + [Onevra](https://github.com/oneo-me/onevra) 原生 UI，底层使用 QEMU 和平台硬件加速。当前首版仅实现 **Windows x64**。

## 已实现

- 虚拟机库：创建、搜索、分页、编辑，以及从列表移除；移除保留磁盘和快照。
- 配置 CPU、内存、qcow2 磁盘、安装 ISO、网络与加速方式。
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

创建虚拟机时选择 ISO 并保存，再点击“启动”。默认使用 2 个 CPU、4096 MiB 内存、64 GiB 按需分配磁盘、WHPX 和 NAT。使用标准 SATA 磁盘与 Intel E1000 网络设备。安装完成后关机，在“编辑配置”中卸载 ISO，后续从硬盘启动。

“正常关机”只发送 ACPI 请求，需要客户机系统响应；若客户机处于暂停状态，请先继续运行。强制断电可能丢失未保存的数据。

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

普通测试涵盖参数与路径处理、YAML 持久化、未知字段与多文档拒绝、QMP 事件、错误及超时后控制恢复、并发日志；PATH 中存在 `qemu-img` 时还会用临时 qcow2 磁盘执行快照创建、恢复和删除测试，包括数字名称的拒绝保护。

真实启动测试需设置环境变量 `MY_VM_INTEGRATION=1` 后执行：

```sh
mise exec go@1.27.2 -- go test ./internal/vm -run TestQEMULifecycle -v -count=1
```

该测试会短暂打开 TCG、WHPX QEMU 窗口，使用临时空磁盘验证 SDL 客户机窗口可见性、启动、重复启动保护、暂停、继续、关机请求、强制退出及运行中快照保护，不会安装操作系统或更改主机网络。WHPX 不可用时该项会失败。

## 首版范围

暂未实现 macOS/Linux、客户机画面嵌入、UEFI/TPM/Secure Boot、Windows 11 安装要求的自动配置、内存/在线快照、多磁盘、磁盘扩容、磁盘导入、安装器和 QEMU 自动下载。部分客户机需要其他设备或固件配置，当前不提供任意 QEMU 参数输入。
