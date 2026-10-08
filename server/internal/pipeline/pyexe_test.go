package pipeline

import (
	"os/exec"
	"runtime"
	"testing"
)

// WindowsApps 商店占位 stub 判定：路径含 windowsapps 即跳过（大小写/斜杠不敏感）。
func TestIsStoreStub(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only 判定")
	}
	stub := []string{
		`C:\Users\x\AppData\Local\Microsoft\WindowsApps\python.exe`,
		`C:\USERS\X\AppData\Local\MicroSoft/WindowsApps/python3.exe`,
	}
	for _, p := range stub {
		if !storeStub(p) {
			t.Errorf("storeStub(%q) = false, want true", p)
		}
	}
	real := []string{`C:\Python313\python.exe`, `/usr/bin/python3`}
	for _, p := range real {
		if storeStub(p) {
			t.Errorf("storeStub(%q) = true, want false", p)
		}
	}
}

// PythonExe 至少解析出一个候选；且当 PATH 上的 python 是 stub 时绝不返回它。
func TestPythonExeSkipsStoreStub(t *testing.T) {
	got := PythonExe()
	if got == "" {
		t.Fatal("PythonExe() 为空")
	}
	if p, err := exec.LookPath("python"); err == nil && storeStub(p) && got == p {
		t.Fatalf("PythonExe() = %q（WindowsApps stub）", got)
	}
}
