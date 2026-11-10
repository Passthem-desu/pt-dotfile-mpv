#!/usr/bin/env python3

import argparse
import ctypes
import json
import subprocess
import sys
import tkinter.messagebox
from pathlib import Path


def enable_high_dpi():
    if sys.platform == "win32":
        try:
            ctypes.windll.user32.SetProcessDpiAwarenessContext(ctypes.c_void_p(-4))
        except Exception:  # noqa: BLE001
            try:
                ctypes.windll.shcore.SetProcessDpiAwareness(2)
            except Exception:  # noqa: BLE001
                ctypes.windll.user32.SetProcessDPIAware()


def editor(file: Path, inPoint: float):
    try:
        data = probe(file)
    except (KeyError, IndexError):
        tkinter.messagebox.showerror("错误", f"文件 {file} 可能不是视频")
        sys.exit(1)

    print(data)

    root = tkinter.Tk()
    dpi = root.winfo_fpixels("1i")
    if not dpi or dpi <= 0:
        dpi = 96.0
    root.tk.call("tk", "scaling", dpi / 72.0)


def probe(file: Path):
    out = subprocess.check_output([
        'ffprobe', '-v', 'error', '-select_streams', 'v:0',
        '-show_entries', 'stream=width,height,r_frame_rate,duration',
        '-of', 'json', file,
    ])
    s = json.loads(out)['streams'][0]
    num, den = map(int, s.get('r_frame_rate', '0/1').split('/'))
    fps = num / den if den else 0.0
    dur = float(s.get('duration', 0) or 0)
    return {
        'width'   : s['width'],
        'height'  : s['height'],
        'fps'     : fps,
        'duration': dur,
    }


if __name__ == "__main__":
    enable_high_dpi()

    parser = argparse.ArgumentParser(
        prog="PCutter",
        description="小帕的视频帧小助手",
    )
    parser.add_argument("filename")
    parser.add_argument("-t", "--time")

    args = parser.parse_args()
    path = Path(args.filename)
    _inPoint = args.time
    if not path.exists():
        tkinter.messagebox.showerror("错误", f"文件 {args.filename} 不存在")
        sys.exit(1)

    try:
        if _inPoint is None:
            inPoint = 0.0
        else:
            inPoint = float(_inPoint)
    except (TypeError, ValueError):
        tkinter.messagebox.showerror("错误", f"参数 -t {_inPoint} 不是数字")
        sys.exit(1)
    editor(path, inPoint)
