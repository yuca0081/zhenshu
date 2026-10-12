package produce

import (
	"bytes"
	"encoding/json"
	"os"
)

// jsonlTailWindow 单次尾读的字节上限。manifest 随制作轮次/自动重跑记录无限增长，
// activity 轮询与 LastTerminal 门禁此前每次全量读+全行解析，I/O 随历史线性上涨；
// 尾部字节窗口把成本压到常数级。2MB 约容纳上千条事件行，常规项目等于全量。
const jsonlTailWindow = 2 << 20

// ReadJSONLTail 读取 jsonl 尾部至多 maxLines 条完整行，按时间正序返回原始行字节。
// 只返回完整行（末行被截断时丢弃）；窗口内行数不足 maxLines 时按实际返回。
// 兜底：文件大于窗口且窗口内一条可解析行都没有时回退全量读——宁可慢不可错。
func ReadJSONLTail(path string, maxLines int) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()
	window := int64(jsonlTailWindow)
	if size < window {
		window = size
	}
	buf := make([]byte, window)
	if _, err := f.ReadAt(buf, size-window); err != nil {
		return nil, err
	}
	if window < size { // 窗口起点可能切进行中间，丢弃首半个残行
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		} else {
			buf = nil
		}
	}
	lines := bytes.Split(bytes.TrimSuffix(buf, []byte("\n")), []byte("\n"))
	if len(lines) == 1 && len(lines[0]) == 0 {
		lines = nil
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	if len(lines) == 0 && size > window {
		// 回退全量：窗口里一条完整行都没有（超长单行/异常文件）时不静默降级为空
		full, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		lines = bytes.Split(bytes.TrimSuffix(full, []byte("\n")), []byte("\n"))
		if len(lines) > maxLines {
			lines = lines[len(lines)-maxLines:]
		}
	}
	return lines, nil
}

// unmarshalJSONL 逐行解析为 T；坏行（部分写入的撕裂行）跳过不报错——
// manifest 追加写进程被杀时可能留下半行，审计口径历来按"坏行忽略"处理。
func unmarshalJSONL[T any](lines [][]byte) []T {
	out := make([]T, 0, len(lines))
	for _, line := range lines {
		var v T
		if json.Unmarshal(line, &v) != nil {
			continue
		}
		out = append(out, v)
	}
	return out
}
