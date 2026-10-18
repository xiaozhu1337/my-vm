# Run inside the guest: sudo python3 prepare_linux_gpu.py
from pathlib import Path
from urllib.request import ProxyHandler, build_opener

import os
import subprocess

# shortcut: this module is validated on one kernel; port and retest before expanding support.
if os.uname().release != "7.3.0-9-generic":
    raise SystemExit("Only Ubuntu kernel 7.3.0-9-generic has been validated")
if os.geteuid() != 0:
    raise SystemExit("Run this script with sudo inside the Linux guest")

commit = "d504d40ab83839cb61942a812a99d183c42227b9"
root = Path("/usr/src/dxgkrnl-6.18-d504d40")
root.mkdir()  # Refuse to overwrite an existing source directory.

opener = build_opener(ProxyHandler({
    "http": "http://192.168.1.99:7890",
    "https": "http://192.168.1.99:7890",
}))
base = f"https://raw.githubusercontent.com/microsoft/WSL2-Linux-Kernel/{commit}/"
names = [
    "dxgmodule.c", "hmgr.c", "misc.c", "dxgadapter.c",
    "ioctl.c", "dxgvmbus.c", "dxgprocess.c", "dxgsyncfile.c",
    "dxgkrnl.h", "dxgvmbus.h", "hmgr.h", "misc.h", "dxgsyncfile.h",
]
files = {
    name: "drivers/hv/dxgkrnl/" + name
    for name in names
}
files["include/uapi/misc/d3dkmthk.h"] = "include/uapi/misc/d3dkmthk.h"

for destination, source in files.items():
    with opener.open(base + source, timeout=30) as response:
        text = response.read().decode("utf-8")
    path = root / destination
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")

(root / "gpu_p_guids.h").write_text("""\
#include <linux/hyperv.h>
#ifndef HV_GPUP_DXGK_GLOBAL_GUID
#define HV_GPUP_DXGK_GLOBAL_GUID \\
 .guid = GUID_INIT(0xdde9cbc0, 0x5060, 0x4436, 0x94, 0x48, \\
                   0xea, 0x12, 0x54, 0xa5, 0xd1, 0x77)
#endif
#ifndef HV_GPUP_DXGK_VGPU_GUID
#define HV_GPUP_DXGK_VGPU_GUID \\
 .guid = GUID_INIT(0x6e382d18, 0x3336, 0x4f4b, 0xac, 0xc4, \\
                   0x2b, 0x77, 0x03, 0xd4, 0xdf, 0x4a)
#endif
""", encoding="utf-8")

(root / "Makefile").write_text("""\
obj-m += dxgkrnl.o
dxgkrnl-y := dxgmodule.o hmgr.o misc.o dxgadapter.o ioctl.o dxgvmbus.o dxgprocess.o dxgsyncfile.o
ccflags-y += -I$(src)/include -D_MAIN_KERNEL_ -include linux/vmalloc.h -include $(src)/gpu_p_guids.h
""", encoding="utf-8")

(root / "dkms.conf").write_text("""\
PACKAGE_NAME="dxgkrnl"
PACKAGE_VERSION="6.18-d504d40"
BUILT_MODULE_NAME[0]="dxgkrnl"
DEST_MODULE_LOCATION[0]="/updates/dkms"
AUTOINSTALL="no"
BUILD_EXCLUSIVE_KERNEL="^7[.]3[.]0-9-generic$"
MAKE[0]="make -C /lib/modules/${kernelver}/build M=${dkms_tree}/${PACKAGE_NAME}/${PACKAGE_VERSION}/build modules"
""", encoding="utf-8")

for step in ("add", "build", "install"):
    subprocess.run(["dkms", step, "-m", "dxgkrnl", "-v", "6.18-d504d40", "-k", os.uname().release], check=True)
subprocess.run(["modprobe", "dxgkrnl"], check=True)
print("MYVM_GPU_MODULE_READY", flush=True)
