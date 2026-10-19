#!/usr/bin/env python3
"""Launch the Linux installer from read-only or noexec optical media."""
from pathlib import Path
import os
import shutil
import subprocess
import tempfile


def main():
    media = Path(__file__).resolve().parent
    descriptor, name = tempfile.mkstemp(prefix='my-vm-guest-')
    executable = Path(name)
    try:
        with os.fdopen(descriptor, 'wb') as target, (media / 'my-vm-guest-linux-amd64').open('rb') as source:
            shutil.copyfileobj(source, target)
        executable.chmod(0o700)
        result = subprocess.run([str(executable), '--media', str(media)], check=False)
        raise SystemExit(result.returncode)
    finally:
        executable.unlink(missing_ok=True)


if __name__ == '__main__':
    main()
