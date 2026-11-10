#!/usr/bin/env python3

import platform
from pathlib import Path

CONFIG_BASE = Path(__file__).parent.parent
CONFIG_TARGET = CONFIG_BASE / "mpv.conf"

# # Keep the MPV window open after playback
# keep-open=yes
# screenshot-directory=W:/tmp/

with open(CONFIG_TARGET, "w", encoding="utf-8") as f:
    print("# Keep the MPV window open after playback", file=f)
    print("keep-open=yes\n", file=f)

    print("# Screenshot target for key `s`", file=f)
    if platform.system() == "Windows" and Path('W:/tmp/').is_dir():
        print("screenshot-directory=W:/tmp/\n", file=f)
    if platform.system() == "Linux":
        print("screenshot-directory=/tmp/\n", file=f)
