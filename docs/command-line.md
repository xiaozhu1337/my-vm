# 命令行工具

`my-vm-cli.exe` 可以独立运行，不需要打开 GUI。GUI 和 CLI 都调用 `my-vm/library` 的公共接口，通过当前 Windows 用户专属的命名管道连接同一个核心服务。首次调用自动在后台启动同目录的 `my-vm-cli.exe serve`；单独复制 CLI 也可以管理虚拟机。增强安装另需对应的客户机工具 ISO。请使用管理员权限。

核心服务持有 HCS、共享通道及运行状态，持续运行到被明确停止或 Windows 会话结束。GUI 退出不会结束核心服务；GUI 正常退出仍保留原有的全部关机确认流程。更新程序前应正常关闭全部虚拟机并退出旧核心服务。服务异常退出或被强行终止会断开其 HCS 句柄，等同客户机断电。架构和库调用示例见 [architecture.md](architecture.md)。

## 输出与退出码

选项放在命令前。默认输出 JSON，支持 `--format json|yaml|text`。业务成功统一返回：

```json
{"ok":true,"data":{"total":0,"items":[]}}
```

失败时输出 `{"ok":false,"error":"具体错误","exit_code":2}` 并返回非零退出码。参数错误使用 2，业务或连接错误通常使用 1；客户机程序退出码在 0 到 255 之间时原样传播。`exec` 的 JSON/YAML 输出包含 `exit_code`、文本 `stdout`、文本 `stderr`、`truncated` 和 `error`，非零客户机退出码会令 `ok` 为 false。`--format text exec` 原样输出客户机的标准输出和错误输出；其他命令的 text 模式为缩进 JSON，错误写入 stderr。

虚拟机可用 ID 或唯一名称指定；名称不区分大小写。自动化建议使用返回的 ID。列表使用 `--offset` 和 `--limit` 分页，默认 20 条，最多 100 条。`data.total` 是总数，`data.items` 是当前页；列表只包含名称、ID、状态、客户机类型、运行内存及已有任务的进度，不返回完整配置。用 `get` 读取单台完整配置。

`--timeout` 为 1 到 3600 秒，默认 60 秒。`--wait` 等待启停、暂停和继续达到目标状态；默认只等待相应请求完成。CLI 没有交互确认框，调用方应明确决定是否恢复快照、强制断电、移除登记或删除网络。

## 创建、编辑与复制

```text
my-vm-cli.exe --format yaml template > "C:\Work\machine.yaml"
my-vm-cli.exe create "C:\Work\machine.yaml"
my-vm-cli.exe list
my-vm-cli.exe --format yaml get "Test VM" > "C:\Work\export.yaml"
my-vm-cli.exe update "Test VM" "C:\Work\export.yaml"
my-vm-cli.exe --timeout 3600 clone "Test VM" "Test VM Copy"
my-vm-cli.exe remove "Test VM Copy"
```

编辑模板中的名称、客户机系统、CPU、内存、磁盘、ISO、网卡、共享目录和串口等配置后创建。模板使用随机 ID，重复使用同一模板会被拒绝；创建另一台时重新生成模板。`create` 接受裸虚拟机 YAML，也接受 `template`、`get` 输出的 `ok/data` 包装，JSON 导出同样可导入。文件必须是单份文档，最多 1 MiB；未知字段会报错。

`update` 会先读取当前配置，再应用文件中提供的字段并通过核心验证；适合只包含需要调整字段的 YAML。例如：

```yaml
# 显示名称，1 到 80 个字符；同一清单内不能重名。
name: Test VM
# 虚拟 CPU 数量，1 到 256。
cpus: 4
# 下次启动的内存，单位 MiB；修改硬件须先关机。
memory_mib: 4096
# 主网卡模式：nat、bridge 或 none；bridge 需要 switch_id。
network: none
# 映射到客户机的宿主目录列表，最多 16 项。
shares:
  # 共享名：英文字母开头，只含字母、数字、下划线及连字符。
  - name: Work
    # 宿主目录绝对路径。
    path: 'C:\Work'
    # 默认只读；false 允许客户机修改宿主文件。
    read_only: true
```

创建、更新和复制共用核心库中的设备及配置验证。更新不得改变 ID、磁盘容量或后端；硬件变更需先关机。保存时若其他客户端已更改当前版本，会拒绝覆盖，应重新读取后再修改。文件自身长期保存的旧内容仍可能覆盖调用前已存在的修改，自动化应在编辑前重新导出。

复制使用独立磁盘和虚拟机标识，不继承快照，默认断网并清除宿主共享目录；客户机内部身份仍需自行处理。`remove` 只移除清单登记，磁盘和快照保留，返回保留磁盘的位置。

## 生命周期、内存与增强

```text
my-vm-cli.exe --wait start "Test VM"
my-vm-cli.exe state "Test VM"
my-vm-cli.exe --wait pause "Test VM"
my-vm-cli.exe --wait resume "Test VM"
my-vm-cli.exe memory "Test VM"
my-vm-cli.exe memory set "Test VM" 6144
my-vm-cli.exe enhancements "Test VM"
my-vm-cli.exe media mount "Test VM" "C:\Work\tools.iso"
my-vm-cli.exe --wait stop "Test VM"
my-vm-cli.exe --timeout 600 shutdown-all
```

`stop` 请求正常关机，`kill` 强制断电，`shutdown-all` 等待全部实际关机。运行内存调整不修改下次启动的配置。`enhancements` 按客户机系统准备、登记并在运行时插入增强盘，复用 GUI 的宿主驱动收集和校验流程；安装器仍需在客户机运行。`media mount` 临时更换运行中的客户机工具光盘，不修改已保存的启动配置。CLI 不提供 RDP 窗口或鼠标键盘控制，GUI 负责画面显示。

## 磁盘快照

```text
my-vm-cli.exe snapshot list "Test VM"
my-vm-cli.exe --timeout 600 snapshot create "Test VM" before_update
my-vm-cli.exe --timeout 600 snapshot restore "Test VM" before_update
my-vm-cli.exe snapshot delete "Test VM" before_update
```

每台最多 16 个命名恢复点。创建与删除支持运行中的虚拟机；恢复运行中的虚拟机会重启，暂停中的恢复后仍暂停，关机的保持关机。快照只保存磁盘，不保存 RAM、UEFI 变量或程序现场。恢复会丢弃当前分支未保存的修改。

快照列表分页，条目包含 `ParentID`、`ParentName`、`Depth`、`Current` 及旧格式的 `Legacy`。名称以英文字母开头，仅含字母、数字、下划线或连字符，最多 64 字符。运行中创建依赖宿主的原生磁盘分叉能力，不支持时明确报错。删除保留仍被引用的祖先层，不保证立即释放空间，目前不执行在线磁盘链合并。

超时或断连会请求取消，但不能回滚已完成的磁盘操作；原子切换阶段可能继续完成。结果不明确时先查询状态和快照，不要盲目重试或直接删除磁盘文件。GUI 会观察运行会话的变化，重新连接自己已打开的控制台。

## 网络和全局设置

```text
my-vm-cli.exe network list
my-vm-cli.exe adapters
my-vm-cli.exe network create DevNet 192.168.240.0/24
my-vm-cli.exe network delete "network-id"
my-vm-cli.exe settings
my-vm-cli.exe settings set side-channel-mitigations true
```

仅可删除本程序登记的自定义 NAT；被虚拟机引用的网络不能删除。系统或其他程序的网络保持只读。Default Switch 提供 DHCP，自定义 NAT 需在客户机配置静态地址、网关与 DNS。侧通道缓解设置只影响之后启动的虚拟机。

## 客户机命令和文件

先安装并运行新版客户机增强工具，登录代理所属的用户。命令以该用户权限运行，通过私有 Hyper-V Socket 通道连接，无需客户机网络或共享目录，也不自动提升权限。

```text
my-vm-cli.exe exec "Test VM" whoami.exe
my-vm-cli.exe exec ubuntu /usr/bin/id
my-vm-cli.exe --format text --cwd "C:\Users\vmuser" exec "Test VM" cmd.exe /c dir
my-vm-cli.exe copy-to "Test VM" "C:\Work\input.bin" "C:\Users\vmuser\input.bin"
my-vm-cli.exe copy-from ubuntu /home/vmuser/result.bin "C:\Work\result.bin"
my-vm-cli.exe --overwrite copy-to ubuntu "C:\Work\input.bin" /home/vmuser/input.bin
```

程序和参数直接传递，shell 语法需显式使用 `cmd.exe /c` 或 `/bin/sh -c`。每个输出流最多 4 MiB，超出会标记截断。超时或断连会终止 Windows Job Object 或 Linux 进程组中的派生进程；Linux 新会话进程可能脱离原组，应由客户机服务管理器控制。取消不回滚程序已修改的数据。

复制按流传输普通单文件，客户机路径须为绝对路径，目标目录须已存在。默认不覆盖已有文件，`--overwrite` 才允许替换；完整接收后才发布，中断保留原目标。Windows 覆盖保留目标原有 DACL，新文件仅当前用户和 SYSTEM 可访问。目录递归复制不在支持范围。

同一台虚拟机的冲突管理操作由核心串行处理，等待同样受命令超时约束；不同虚拟机的业务和状态查询可以并行。后台快照及复制进度可从 `list`/`status` 的 `progress` 查询。命名管道仅当前用户和 SYSTEM 可访问，客户端验证服务端属于同一用户并已提升权限。错误异步记录到程序目录的 `logs`；客户机输出及凭据不写入日志。
