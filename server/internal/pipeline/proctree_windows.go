//go:build windows

package pipeline

import (
	"os/exec"
	"strconv"
	"syscall"
)

// setSysProcAttr 新进程组：让 killProcessTree 能按组定位（Windows 侧 taskkill /T 兜底树杀）。
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// killProcessTree taskkill /T 连带孙进程。CommandContext 默认只 TerminateProcess 直接
// 子进程——npx→node→hyperframes→chromium/ffmpeg 的孙进程会存活，继续烧 CPU、写 renders/。
func killProcessTree(pid int) {
	_ = exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
}
