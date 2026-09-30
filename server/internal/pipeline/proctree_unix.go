//go:build !windows

package pipeline

import (
	"os/exec"
	"syscall"
)

// setSysProcAttr 新进程组：取消时 kill(-pgid) 整组击杀（npx→node→chromium 孙进程树）。
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessTree(pid int) {
	// 负 pid = 进程组；进程已退出时 ESRCH 忽略
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
