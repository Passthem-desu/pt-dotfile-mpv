//go:build !windows

package core

import "os/exec"

// hideWindow 在非 Windows 上没有意义。
func hideWindow(cmd *exec.Cmd) {}
