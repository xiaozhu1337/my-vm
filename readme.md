# My VM

Windows 原生虚拟机管理器。管理界面使用 [MyGo](https://github.com/egoist/mygo) 和 [Onevra](https://github.com/oneo-me/onevra)，虚拟机后端为 Hyper-V Host Compute Service（HCS），控制台使用 Windows 原生 RDP ActiveX。仅实现 Windows x64 主机版本，可运行 Windows 与 Linux 客户机。

当前版本已经从 QEMU 迁移至 HCS，只使用硬件虚拟化。旧 QEMU/qcow2 清单和磁盘会保留，但无法直接启动，也不会自动转换、覆盖或删除。

## 已实现

- 原生虚拟机列表、搜索、分页、资源编辑、深浅色主题及未保存修改提示。
- UEFI、动态 VHDX、安装光盘和客户机工具光盘；启停、暂停、继续、正常关机及强制断电。
- Hyper-V 默认交换机 NAT，或选择已创建的外部交换机进行桥接。
- 基础控制台用于固件和安装；增强会话提供窗口分辨率调整和剪贴板。
- 增强会话的文本、图片、文件复制粘贴；不会同时重定向宿主整盘。
- 选择主机文件夹映射，默认只读，可单独允许写入；Windows 自动分配空闲盘符，Linux 在文件管理器中挂载。
- 内存按需分配与运行中热调整；GPU 分区共享开关。
- 关机状态的完整 VHDX 磁盘快照，创建、恢复、删除及分页查看。
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
dist\my-vm-guest-tools.iso
```

不再需要安装 QEMU 或下载 virtio 驱动。工具 ISO 包含 Windows 和 Linux x64 客户机程序。重新构建前关闭正在使用旧 EXE/工具光盘的程序和虚拟机。

## 使用

1. 退出仍在运行的旧版本，以管理员身份打开 `dist\my-vm.exe`，新建虚拟机，选择客户机类型和安装 ISO。
2. 设置 CPU、内存、磁盘及网络，创建并启动。Windows 安装光盘出现按键启动提示时及时按键；错过后可重启虚拟机。
3. 完成系统安装后关机，卸载安装 ISO。HCS 使用 Hyper-V 自带的存储、网络和显示设备。
4. 需要共享目录时，在关机状态添加主机文件夹并选择只读/可写，点击“挂载客户机工具光盘”，保存并启动，在客户机安装工具。
5. 需要高清桌面和剪贴板时，准备客户机的增强会话服务，再点击“增强会话”。基础控制台仍可随时用于安装及故障恢复。

完整客户机准备步骤见 [guest-setup.md](docs/guest-setup.md)。GPU 的实际通过环境及可复现步骤见 [linux-gpu.md](docs/linux-gpu.md)，验证范围见 [validation.md](docs/validation.md)。

共享目录使用绑定到该虚拟机 RuntimeId 的 Hyper-V Socket 私有通道，宿主不开放 TCP 文件共享端口。客户机工具仅在本机 `127.0.0.1:18389` 提供 WebDAV 适配；不会转发任意主机网络地址。只读限制在宿主端执行，可写目录的操作会直接修改主机文件。

## 内存、GPU 与快照的边界

“内存按需分配”对应 HCS `AllowOvercommit`，同时允许在线修改目标内存。它不是自动按最小/最大值调整的控制器，实际增减及回收受客户机 Hyper-V 内存驱动影响。运行中的调整不修改下次启动的配置值。

GPU 开关请求 HCS 分配 GPU 分区，失败会报错。客户机驱动必须另行准备；原版 Ubuntu 不自带所需的 `dxgkrnl`。已在本文档所列 Ubuntu 环境验证真实 CUDA 运算，但不据此保证所有显卡、内核、Windows GPU 驱动或桌面 3D 应用兼容。

快照是关机后的完整磁盘文件副本，每台最多 16 个；不含 RAM、UEFI 变量或运行状态，也不是 Hyper-V 完整检查点。创建和恢复需要额外磁盘空间，恢复前会确认。系统安装或固件设置发生变化时，应保留相应安装记录和配置。

Windows WebClient 单文件上限约 4 GiB，工具会配置系统允许的最大值；如服务此前已启动，变更后可能需要重启客户机。为避免覆盖时丢失目标文件，服务拒绝覆盖式 COPY/MOVE、目录 COPY 和非空目录递归删除；普通文件 PUT 覆盖使用临时文件完整写入后再替换。不是完整 SMB 语义，也不适合作为数据库目录。

## 数据和日志

```text
%APPDATA%\my-vm\library.yaml
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

MyGo `ui.NewTester` 在内存中验证导航、搜索分页、表单、禁用状态、主题和最小布局；原生文件/确认对话框、RDP、GPU 及真实文件粘贴另以实际系统验证。当前不包含 macOS/Linux 主机版本、旧 qcow2 自动转换、内存快照、自动 OS 安装、自动 GPU 驱动安装或宿主交换机创建。

实现参考 [NanaBox](https://github.com/M2Team/NanaBox) 的 HCS、RDP、内存和 GPU 路径；未使用其图标资源。
