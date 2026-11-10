//go:build windows

package core

import (
	"os/exec"
	"syscall"
)

// createNoWindow 让子进程根本不分配控制台（CREATE_NO_WINDOW）。
// 光 HideWindow 只是把窗口藏起来，仍会闪一下黑框。
const createNoWindow = 0x08000000

// hideWindow 让子进程不弹黑框：GUI 程序调用 ffmpeg 时必须。
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}
