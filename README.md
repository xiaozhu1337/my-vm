[简体中文](README.zh-CN.md) · [Download](https://github.com/xiaozhu1337/my-vm/releases/latest)

# My VM

<img src="assets/my-vm.png" alt="My VM icon" width="72">

A native Windows virtual machine manager for Windows and Linux guests, built on **Hyper-V Host Compute Service (HCS)**. Hardware virtualization, live disk snapshots, and integrated Linux desktops in one application.

## Why My VM?

- **Native Hyper-V performance.** Uses the Windows virtualization stack directly, with hardware-backed CPU, memory, storage, and networking. No QEMU runtime is required.
- **Live snapshots with branches.** Create VHDX differencing snapshots while your guest keeps running. Progress appears in the background. Restore an earlier point and create a new branch without copying the entire system disk.
- **Linux enhanced sessions.** Connect to the existing GNOME or KDE Plasma Wayland desktop. GNOME supports dynamic resolution; KDE uses display fitting. No replacement desktop or Xfce installation is required.
- **Windows and Linux in one library.** Manage virtual hardware, NAT or bridged networking, installation media, clones, and basic or enhanced consoles from the same interface.
- **Private folder sharing.** Share selected folders through a VM-specific Hyper-V Socket channel. Folders default to read-only; the host does not expose a TCP file-sharing port.
- **Useful automation.** A separate CLI controls VMs and snapshots, executes guest commands, and transfers individual files in either direction.
- **Responsive management.** Console rendering runs in separate processes. Background jobs reserve the affected VM while other VMs remain manageable.
- **Small guest-tools discs.** Separate Windows and Linux discs install desktop integration and folder-sharing tools. Linux setup downloads only the required dependencies from the guest's configured APT repositories for Ubuntu/Kubuntu 26.04 and 26.10 amd64.

## Screenshots

The images below are rendered from the application's actual interface using example VM configurations and snapshot data. The current application UI is in Simplified Chinese.

**VM library and hardware overview**

![VM library and hardware overview](assets/screenshots/overview.png)

**Differencing snapshot branches**

![Snapshot tree with example branches](assets/screenshots/snapshots.png)

**Per-VM device manager**

![Virtual machine device manager](assets/screenshots/devices.png)

## Download and run

Download **`my-vm-windows-amd64.zip`** from [Releases](https://github.com/xiaozhu1337/my-vm/releases/latest). The ZIP contains:

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

Extract the complete archive into a writable folder, keep the enhancement ISOs next to the executable, and run **`my-vm.exe` as administrator**. A SHA-256 checksum for the ZIP is provided alongside the download.

### Host requirements

- An x64 Windows host with HCS schema 2.5 or later: Windows 11 Pro/Enterprise or Windows Server 2022 and later.
- Hardware virtualization enabled in firmware, the full Hyper-V feature enabled, and a restart after enabling it.
- The `vmcompute` and `hns` services available. Automatic NAT uses the Hyper-V **Default Switch**.
- Live snapshots additionally require the host's native backing-file fork API. Unsupported hosts report an error instead of silently copying the full disk.

### First VM

1. Create a VM, select a Windows or Linux installation ISO, and assign CPU, memory, disk, and networking.
2. Right-click the VM to start it and open the basic console. Install the operating system normally.
3. Choose **Install enhancements** from the VM's context menu to attach the appropriate guest-tools disc.
4. In Windows, run `install.exe` from the disc as the logged-in desktop user and approve elevation. Windows enhanced sessions require a guest edition with an RDP server and an account with a login password.
5. In Ubuntu/Kubuntu, connect the guest to the internet, then run `python3 "/path/to/mounted/disc/install.py"` from a logged-in GNOME/KDE Wayland desktop. Run it as your desktop user, without `sudo`; approve the permission prompt when requested. Setup uses the guest's existing APT repositories and signature checks; configure a system APT proxy if your network requires one. Download or installation failures stop setup with an error; fix the network or package issue and rerun it.
6. Reopen the console. Automatic mode starts with the basic console and tries an enhanced session when appropriate. The toolbar also lets you switch modes manually.

GNOME/KDE tools generate separate connection credentials and exchange them through the VM's private channel. Keyring/wallet approval, unknown-certificate confirmation, guest login, and unlocking may still need user interaction. Clipboard formats depend on the guest's desktop service.

With GPU sharing enabled, **Install enhancements** collects drivers from the current host and prepares a private, matching disc before mounting it. Release images contain no host-specific GPU driver binaries. Missing files, copy/checksum errors, or changed host drivers stop preparation and report the problem; the existing disc stays in place. Preparation needs extra disk space for temporary files and the new ISO. Linux GPU-PV currently targets NVIDIA GPUs with compute capability 8.0 or later and the supported `7.3.0-9-generic` guest kernel; automatic collection does not guarantee hardware compatibility.

Windows preparation excludes Linux libraries, host-only kernel drivers, installers, caches, and logs. Vendor runtime DLLs and data remain, so a private disc can still occupy several GiB. Only unused images generated during the current manager session are automatically removed; existing images from earlier sessions are retained.

Closing a console leaves its VM running. Closing the manager offers a normal shutdown of active VMs and waits for them to stop.

## Snapshots: what is saved?

Snapshots contain **disk state**, not RAM or UEFI variables. Live creation is crash-consistent; it does not save a running application's memory or guarantee an application-consistent database backup.

- Create and delete snapshots while the guest runs. Creation switches future writes to a new differencing disk.
- Restore restarts a running guest, keeps a paused guest paused, and leaves a powered-off guest off. Unsnapshotted changes on the current branch are discarded after confirmation.
- Up to 16 named restore points per VM. Deleting a name retains disk layers still needed by another branch or the active disk, so space may not be reclaimed immediately.
- Snapshots depend on their parent disks. Keep the entire disk chain and `storage.yaml` together; use an independent copy for backup. Online chain compaction is not implemented.

## Command line

Keep the manager open, then run the CLI from the extracted folder. Use a VM name or ID from `list`:

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

Guest commands and file transfer require installed, running guest tools. Snapshots and ordinary VM control use the host backend.

## Build from source

Development tools are managed with [mise](https://mise.jdx.dev/). On Windows x64:

```text
mise install go@1.27.2
mise install python@3.12
mise exec go@1.27.2 -- go build -trimpath -ldflags=-H=windowsgui -o "dist/my-vm.exe" ./cmd/my-vm
mise exec go@1.27.2 -- go build -trimpath -o "dist/my-vm-cli.exe" ./cmd/my-vm-cli
```

To also build the portable enhancement discs:

```text
mise exec python@3.12 -- python -X utf8 "scripts/build.py"
```

The first media build downloads portable GPU module source and license texts; later builds verify and reuse `dist/guest-payload`. It does not collect Ubuntu packages or require Docker/WSL. Standard `HTTP_PROXY`/`HTTPS_PROXY` settings are respected. Linux guests need network access during enhancement installation. GPU-PV requires compatible guest kernels and host-matched drivers; support is hardware-dependent.

```text
mise exec go@1.27.2 -- go test ./...
mise exec go@1.27.2 -- go vet ./...
```

## Data and scope

Configuration lives in `%APPDATA%\my-vm` as YAML. Errors are written asynchronously to `logs\my-vm.log` beside the executable. Removing a VM from the library keeps its disks and snapshots.

This project currently targets **Windows x64 hosts**. It does not provide memory snapshots, unattended OS installation, automatic qcow2 conversion, or universal GPU compatibility. An abnormal manager process exit terminates its HCS VMs; shut guests down normally before closing the host session.

## Acknowledgments

The interface uses [MyGo](https://github.com/egoist/mygo) and [Onevra](https://github.com/oneo-me/onevra). HCS, RDP, memory, and GPU implementation paths reference [NanaBox](https://github.com/M2Team/NanaBox). The project icon is generated by this repository's own script.
