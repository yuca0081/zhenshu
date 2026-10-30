// Python 解释器解析（produce 跑 ai/ 脚本与 styleref 抽特征共用一份，
// 防两处实现漂移——styleref 曾裸 LookPath("python")，Windows 上踩商店 stub）。
package pipeline

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// PythonExe 解析可用的 Python 解释器。Windows 的 `python` 常是 Microsoft Store
// 的 WindowsApps 占位 stub（LookPath 找得到，一跑就退 9009），跳过它退到 py 启动器。
// Linux/容器里 Debian 只装 python3、不建 python 别名，所以 python3 必须一起找。
func PythonExe() string {
	for _, name := range []string{"python", "python3"} {
		if p, err := exec.LookPath(name); err == nil && !storeStub(p) {
			return p
		}
	}
	if p, err := exec.LookPath("py"); err == nil {
		return p
	}
	return "python"
}

// storeStub 识别 WindowsApps 商店占位假 python。真从 Store 装了 Python 的路径
// 同样含 WindowsApps——会被误跳过，但那是 py 启动器不存在，兜底仍回 `python`，不受影响。
func storeStub(p string) bool {
	return runtime.GOOS == "windows" &&
		strings.Contains(strings.ToLower(filepath.ToSlash(p)), "windowsapps")
}
