#!/usr/bin/env python3

import platform
from pathlib import Path

CONFIG_BASE = Path(__file__).parent.parent
CONFIG_TARGET = CONFIG_BASE / "mpv.conf"

with open(CONFIG_TARGET, "w", encoding="utf-8") as f:
    print("# Keep the MPV window open after playback", file=f)
    print("keep-open=yes", file=f)
    print(file=f)

    print("# Screenshot target for key `s`", file=f)
    if platform.system() == "Windows" and Path("W:/tmp/").is_dir():
        print("screenshot-directory=W:/tmp/", file=f)
    if platform.system() == "Linux":
        print("screenshot-directory=/tmp/", file=f)
    print(file=f)

    print("loop-file=inf", file=f)
    print("loop-playlist=inf", file=f)


def mpv_path(p: Path) -> str:
    """给 mpv 的路径：正斜杠，并把 WSL 的 /mnt/c/xxx 映射回 C:/xxx。"""
    s = p.as_posix()
    if s.startswith("/mnt/") and len(s) > 6 and s[6] == "/":
        s = s[5].upper() + ":" + s[6:]
    return s


PCUTTER = mpv_path(CONFIG_BASE / "tools" / "pcutter" / "pcutter.exe")

with open(CONFIG_BASE / "input.conf", "w", encoding="utf-8") as f:
    print("# 旋转视频", file=f)
    print("r cycle_values video-rotate 90 180 270 0", file=f)
    print(file=f)
    print("# 用 pcutter 打开当前文件，并从当前播放位置开始", file=f)
    print("# 注意：大写 C 在 mpv 里等于 Shift+c，所以两个都绑上", file=f)
    for key in ("Alt+c", "Alt+C"):
        print(f'{key} run "{PCUTTER}" "${{path}}" "-t" "${{=time-pos:0}}"', file=f)
