package produce

// segcheck.go —— 段级流水线的「本段 check」（production-stage-design §4.2 written→checked）：
//
//	hyperframes check 没有 -c 单段选择，junction 子工程顶上：.hf-check/<seg>/
//	放该段的单段工程（.hf-seg/<seg>.html 的拷贝作 index.html），assets/audio/
//	compositions 三个目录用 NTFS junction（Unix 用 symlink）共享回项目根——
//	check/snapshot 在子工程上跑，诊断天然只含该段（已实测）。
//	段内含 canvas 的段追加水回放门禁（P1-1 语义不变：两轮截帧像素一致），
//	同样在子工程上做，时刻为段内坐标。
//
// 同文件放句子→时刻换算（TTS 词级时间戳是锚点帧的坐标基础，§3）。

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/review"
)

// segCheckDir 段级 check 子工程目录。
func segCheckDir(p *pipeline.Project, segID string) string {
	return p.Artifact(".hf-check/" + segID)
}

// dirLinks 子工程需要共享回项目根的目录。caps 在 compositions 里，无需单列。
// compositions 不在此列（2026-10-17 事故）：checker 会扫尽整棵共享目录，别的段的
// error 级违规让本段 check 连坐失败，修复轮各自修一个不在自己文件里的错——永不
// 收敛。本段只带它引用的两个文件（见 ensureSegCheckDir）。
var dirLinks = []string{"assets", "audio"}

// ensureSegCheckDir 准备（或刷新）子工程：index.html 每次重拷（check 输入必须
// 是最新段工程），junction 只在缺失时创建（绝不 RemoveAll 整个目录——Windows 上
// RemoveAll 穿透 junction 会把正本目录删掉，只逐个 os.Remove）。
func ensureSegCheckDir(p *pipeline.Project, segID string) error {
	dir := segCheckDir(p, segID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	src := p.Artifact(".hf-seg/" + segID + ".html")
	b, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("缺单段工程 %s（先跑 assemble）: %w", src, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), b, 0o644); err != nil {
		return err
	}
	for _, name := range []string{"package.json", "meta.json"} {
		if pb, err := os.ReadFile(p.Artifact(name)); err == nil {
			_ = os.WriteFile(filepath.Join(dir, name), pb, 0o644)
		}
	}
	for _, name := range dirLinks {
		link := filepath.Join(dir, name)
		if _, err := os.Stat(link); err == nil {
			continue // 已通（junction 或真目录都能解析）
		}
		_ = os.Remove(link)
		if err := createDirLink(link, p.Artifact(name)); err != nil {
			return fmt.Errorf("建立 %s 链接失败: %w", name, err)
		}
	}
	// compositions 只带本段被引用的两个文件（帧 + 字幕 cap），每次检查前重拷：
	// 隔离别的段（见 dirLinks 注释），同时拷贝件天然跟随源刷新。老版本留下的
	// junction 在这里摘链接（os.Remove 对 junction 只摘链接不动正本）。
	compDir := filepath.Join(dir, "compositions")
	_ = os.Remove(compDir)
	for _, rel := range []string{filepath.Join("frames", segID+".html"), filepath.Join("caps", "cap-"+segID+".html")} {
		b, err := os.ReadFile(p.Artifact(filepath.Join("compositions", rel)))
		if err != nil {
			continue // 无字幕段没有 cap；index 没引用到就不需要
		}
		dst := filepath.Join(compDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// createDirLink 目录链接：Windows 用 junction（mklink /J 无需特权；目标给绝对
// 路径，相对目标按 CWD 解析会链错地方——实测踩过）；Unix 用相对 symlink。
func createDirLink(link, target string) error {
	if runtime.GOOS == "windows" {
		cmd := exec.Command("cmd", "/c", "mklink", "/J", link, target)
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, &buf
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%w: %s", err, pipeline.TailLines(buf.String(), 3))
		}
		return nil
	}
	return os.Symlink(relPathSoft(link, target), link)
}

// relPathSoft 相对路径（Unix symlink 用）；失败退绝对路径。
func relPathSoft(link, target string) string {
	rel, err := filepath.Rel(filepath.Dir(link), target)
	if err != nil {
		return target
	}
	return rel
}

// removeSegCheckDir 收尾清理：先逐个摘链接再删文件与目录（顺序铁律：绝不对含
// junction 的目录用 RemoveAll——穿透删除会动到正本）。compositions 可能是老版本
// junction（先摘链接）或本版本真目录（拷贝件，RemoveAll 安全）。best-effort。
func removeSegCheckDir(p *pipeline.Project, segID string) {
	dir := segCheckDir(p, segID)
	for _, name := range dirLinks {
		_ = os.Remove(filepath.Join(dir, name)) // junction/symlink 摘除不动正本
	}
	_ = os.Remove(filepath.Join(dir, "compositions")) // junction：只摘链接；真目录：非空删不掉，交给下面
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(dir, e.Name())) // 剩余是普通文件与本段拷贝目录
	}
	_ = os.Remove(dir)
}

// segCheck 单段门禁（§4.2 checked 位）：hyperframes check 于子工程，诊断只含
// 本段；廉价机械修复先行，LLM 修复轮回喂 sceneGen（≤3 轮，与全局 checkWithRepair
// 同构）；含 canvas 的段加两轮回放门禁。全部通过 = checked。
// 同签名刹车（2026-10-20 成本诊断 P0-4）：修复轮报同类错误（签名=归一化诊断）
// 连续两轮，说明重生成在原地打转（seg07 实测：「Cannot access 'Ph' before
// initialization」连挂三轮，每轮重掷几万 token 换个变量名再死一次）——回退上一
// 版过检画面重检收场，不再给重掷机会。
func (r *Runner) segCheck(ctx context.Context, p *pipeline.Project, segID string, dur float64) error {
	id := p.ID
	prevSig := ""
	rechecks := 0 // 幽灵 TDZ 本地复检次数（竞态骰子，重掷不花钱）
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		r.emit(id, "stage", "check", fmt.Sprintf("%s 第 %d 次（单段）", segID, attempt))
		r.Manifest(p, "stage.start", "check/"+segID)
		var err, blocking error
		if err = ensureSegCheckDir(p, segID); err == nil {
			var out string
			out, err = pipeline.RunCLI(p, "npx", pipeline.HyperframesPkg, "check", ".hf-check/"+segID, "--no-browser-gpu")
			if err == nil {
				// check 过了还差回放门禁（canvas 段）与段间接缝亮度闸（D5，全段）
				if rerr := r.segReplayCheck(ctx, p, segID, dur); rerr != nil {
					err, blocking = rerr, rerr
				} else if serr := r.segSeamCheck(ctx, p, segID, dur); serr != nil {
					err, blocking = serr, serr
				}
			} else {
				blocking = fmt.Errorf("%s", blockingCheckErrors(out))
				if blocking.Error() == "" {
					blocking = fmt.Errorf("%s", pipeline.TailLines(out, 12)) // 浏览器崩溃等非诊断类失败也要有回喂材料
				}
			}
		}
		if err == nil {
			stashLastGoodScene(p, segID) // 过检画面入备份：同签名刹车回退时的底牌
			r.emit(id, "stage", "check", segID+" done")
			r.Manifest(p, "stage.finish", "check/"+segID)
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.Manifest(p, "stage.finish", "check/"+segID)
		// 幽灵 TDZ 复检（2026-10-20）：仅一条「Cannot access」运行时崩溃、无任何
		// 版面诊断 → 先本地重掷（fresh browser 重跑竞态，~10s 零 token），连挂
		// 2 次才进修复轮——把 checker 内伤从「烧 1.7 万修复轮」降成免费骰子。
		if blocking != nil && rechecks < 2 && isLoneRuntimeCrash(blocking.Error()) {
			rechecks++
			r.emit(id, "stage", "check", segID+" 运行时崩溃复检（checker 竞态，本地重跑）")
			r.Manifest(p, "check.tdz_recheck", segID+": "+pipeline.FirstLine(blocking.Error(), 120))
			attempt-- // 复检不消耗修复轮次
			continue
		}
		if attempt >= 3 {
			// 三轮全死在 ensureSegCheckDir（缺段工程/assemble 未跑等）时 blocking
			// 为 nil——裸 blocking.Error() 会 panic，真因被吞成空指针崩溃（中-12）
			if blocking != nil {
				return fmt.Errorf("%s %w:\n%s", segID, errCheckNotConverged, pipeline.TailLines(blocking.Error(), 25))
			}
			return fmt.Errorf("%s check 三轮未过（环境类失败，无诊断输出——查 ensureSegCheckDir）: %w", segID, err)
		}
		if blocking == nil {
			// 环境类失败（ensure 死在 attempt 1/2）：没有可回喂的诊断，机械修复与
			// LLM 修复轮都无从下手，直接如实上抛（下方 blocking.Error() 会 panic）
			return fmt.Errorf("%s check 环境类失败: %w", segID, err)
		}
		// 廉价修复先行：checker 给了对比度建议色 / opacity 冲突就机械回填再检，
		// 不烧 LLM 修复轮（与全局 checkWithRepair 同一经验：LLM 修这两类从不收敛）
		fixN := applyContrastFixes(p, blocking.Error()) + applyGsapOpacityFixes(p, blocking.Error())
		if fixN > 0 {
			r.emit(id, "stage", "check", fmt.Sprintf("%s cheap-fix：机械回填 %d 处", segID, fixN))
			r.Manifest(p, "check.contrast_fix", fmt.Sprintf("%s %d 处", segID, fixN))
			if aerr := r.assemble(ctx, p); aerr == nil {
				continue // 回填后重检（attempt 消耗一次，语义同全局路径）
			}
		}
		// 同签名刹车：上一轮刚报过同类错误，重生成修不掉——回退上一版过检画面
		// 再检一轮（上一版过过检，大概率直接放行）。没有备份（首写就顽固失败）
		// 时退回原路重掷，行为同旧版。
		if sig := checkSig(blocking.Error()); sig == prevSig && restoreLastGoodScene(ctx, r, p, segID) {
			r.emit(id, "stage", "check", segID+" 修复轮同签名未收敛，回退上一版过检画面")
			r.Manifest(p, "check.signature_fallback", segID+": "+pipeline.FirstLine(blocking.Error(), 120))
			prevSig = "" // 回退版本下轮见分晓；别连续两轮回退自己
			continue
		} else if sig != prevSig {
			prevSig = sig
		}
		feedback := "上一版画面被自动检查拒绝，报错摘要（✗ 行逐项都要修）：\n" + pipeline.TailLines(blocking.Error(), 25) +
			"\n修复规则：\n" + checkRepairRules + "逐项修完再输出。"
		r.emit(id, "stage", "check", fmt.Sprintf("%s repair：重生成画面", segID))
		r.Manifest(p, "check.repair", segID+": "+pipeline.FirstLine(blocking.Error(), 160))
		if gerr := r.sceneGen(ctx, p, "", feedback, segID); gerr != nil {
			return gerr
		}
		r.clearFrames(p, segID)
		if err := segCompositions(p, segID); err != nil {
			return err
		}
		pipeline.ClampSegmentFonts(p, segID)
		pipeline.FixupSegmentFrame(p, segID)
		pipeline.ClampSegmentFrame(p, segID)
		if err := r.assemble(ctx, p); err != nil {
			return fmt.Errorf("组装失败: %w", err)
		}
	}
	return nil
}

// isLoneRuntimeCrash 幽灵 TDZ 指纹：blocking 里仅一条 ✗ 且是「Cannot access
// 'X' before initialization」。这是 checker 注入运行时自身的初始化竞态（其压缩
// 代码里 Ph 是巨型 let 链的一节，被提前的 seek 抢跑即 TDZ）——2026-10-19 实测
// 三案（seg06/seg11/seg07）内容各不相同、报错永远是同一个 'Ph'，场景代码怎么
// 重生成都治不了。版面/内容类诊断并行出现时不在此列（那是真要修的）。
func isLoneRuntimeCrash(blocking string) bool {
	if !strings.Contains(blocking, "check_runtime_failure: Cannot access") {
		return false
	}
	n := 0
	for _, ln := range strings.Split(blocking, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "✗") {
			n++
		}
	}
	return n == 1
}

// checkSig 检查诊断的归一化签名：变量名/数字/引号内容剥掉（重掷一轮只换个变量名
// 是常态），剩下的错误类别一致即同签名。返回 sha1 前 8 位。
func checkSig(s string) string {
	var b strings.Builder
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(strings.ToLower(ln))
		if ln == "" {
			continue
		}
		b.WriteString(stripQuotedAndDigits(ln))
		b.WriteByte('\n')
	}
	sum := sha1.Sum([]byte(b.String()))
	return hex.EncodeToString(sum[:4])
}

// stripQuotedAndDigits 去引号包裹段（'Ph' / "seg07.html"）与数字。重掷一轮的
// 典型变化就是引号里换个变量名、路径里换个数字——剥掉后剩下的错误类别一致即同签名。
func stripQuotedAndDigits(s string) string {
	var out strings.Builder
	inQ := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQ != 0:
			if c == inQ {
				inQ = 0
			}
		case c == '\'' || c == '"' || c == '`':
			inQ = c
		case c >= '0' && c <= '9':
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

// lastGoodScenePath 上一版过检画面的备份路径（llm/comp-<seg>.lastgood.json）。
func lastGoodScenePath(p *pipeline.Project, segID string) string {
	return p.Artifact("llm/comp-" + segID + ".lastgood.json")
}

// stashLastGoodScene 把当前场景产物备份为「上一版过检画面」（过检后调用）。
func stashLastGoodScene(p *pipeline.Project, segID string) {
	b, err := os.ReadFile(p.Artifact("llm/comp-" + segID + ".json"))
	if err != nil {
		return
	}
	_ = pipeline.WriteFileAtomic(lastGoodScenePath(p, segID), b, 0o644)
}

// restoreLastGoodScene 同签名刹车收场：回退上一版过检画面并走完合成物落盘链
// （与修复轮重生成后的落盘步骤同序）。备份缺失/恢复链路失败返回 false（调用方
// 退回原路重掷）。指纹旁车不动：恢复版本仍是旧指纹，下轮制作照常再试重生成。
func restoreLastGoodScene(ctx context.Context, r *Runner, p *pipeline.Project, segID string) bool {
	b, err := os.ReadFile(lastGoodScenePath(p, segID))
	if err != nil {
		return false
	}
	if err := pipeline.WriteFileAtomic(p.Artifact("llm/comp-"+segID+".json"), b, 0o644); err != nil {
		return false
	}
	if err := segCompositions(p, segID); err != nil {
		return false
	}
	pipeline.ClampSegmentFonts(p, segID)
	pipeline.FixupSegmentFrame(p, segID)
	pipeline.ClampSegmentFrame(p, segID)
	return r.assemble(ctx, p) == nil
}

// segReplayCheck 段级确定性回放门禁（P1-1 语义不变，范围=本段 canvas）：子工程
// 上 3 个段内时刻两轮截帧，像素须一致。失败信息按 ✗ 行格式（与全局门禁同款文案）。
// 快照基础设施故障降级放行并记账（审查类故障不挡制作，哲学同 prereview）。
func (r *Runner) segReplayCheck(ctx context.Context, p *pipeline.Project, segID string, dur float64) error {
	if !specHasScene(p, segID) || dur <= 1 {
		return nil
	}
	var ats []float64
	for _, t := range []float64{0.5, dur / 2, dur - 0.5} {
		if t < 0.25 {
			continue
		}
		if t > dur-0.05 {
			t = dur - 0.05
		}
		ats = append(ats, round2(t))
	}
	if len(ats) == 0 {
		return nil
	}
	atArg := make([]string, len(ats))
	for i, t := range ats {
		atArg[i] = strconv.FormatFloat(t, 'f', 2, 64)
	}
	snap := func(round string) ([]string, error) {
		dir := p.Artifact("reports/replay/" + segID + "/" + round)
		_ = os.RemoveAll(dir) // 输出目录是我们自己的产物，无 junction 风险
		out, err := pipeline.RunCLI(p, "npx", pipeline.HyperframesPkg, "snapshot",
			".hf-check/"+segID, "--at", strings.Join(atArg, ","), "--no-end", "--no-browser-gpu", "-o", dir)
		if err != nil {
			return nil, fmt.Errorf("snapshot 失败: %s", pipeline.TailLines(out, 6))
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		var names []string
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "frame-") && strings.HasSuffix(e.Name(), ".png") {
				names = append(names, e.Name())
			}
		}
		return names, nil
	}
	n1, err := snap("round1")
	if err != nil {
		r.Manifest(p, "check.replay_degraded", segID+": "+pipeline.FirstLine(err.Error(), 120))
		return nil
	}
	n2, err := snap("round2")
	if err != nil {
		r.Manifest(p, "check.replay_degraded", segID+": "+pipeline.FirstLine(err.Error(), 120))
		return nil
	}
	if len(n1) == 0 || len(n1) != len(n2) {
		r.Manifest(p, "check.replay_degraded", fmt.Sprintf("%s: 两轮帧数异常（%d vs %d）", segID, len(n1), len(n2)))
		return nil
	}
	for i, name := range n1 {
		ia, ib, derr := decodePngPair(
			filepath.Join(p.Artifact("reports/replay/"+segID+"/round1"), name),
			filepath.Join(p.Artifact("reports/replay/"+segID+"/round2"), n2[i]))
		if derr != nil {
			return derr
		}
		maxD, nD, tot := diffStatsImgs(ia, ib)
		if maxD <= replayMaxChannelDiff && (tot == 0 || float64(nD) <= replayDiffRatioMax*float64(tot)) {
			continue
		}
		bm, bp := diffPerceptualImgs(ia, ib)
		if bm <= perceptualMeanMax && bp <= perceptualPixMax {
			continue // 纹理相位抖动（与全局门禁同一二级判据）
		}
		return fmt.Errorf("%s: scene 确定性回放未过（%s 两轮差异超感知容差：块均差 %.1f、块内峰值 %.1f——code 藏了非确定性：只用注入的 rng，运动必须写成 t 的函数，禁任何自管时间/真随机路径）",
			segID, name, bm, bp)
	}
	r.Manifest(p, "check.seg_replay", segID+": diff=0")
	return nil
}

// segSeamCheck 段间接缝亮度闸（D5，docs/cost-optimization-plan §八）——契约
// 「首帧即景/末帧定格」的程序化执法，零 token：子工程 snapshot 取开场两点、
// 段中参考、收尾两点共 5 帧，均值亮度判黑场开场/淡出收黑。白场空白开场 luma
// 偏高不在本闸射程（契约条款+终审兜底）。snapshot 基础设施故障降级放行并记账
// （审查类故障不挡制作，哲学同回放门禁）。
func (r *Runner) segSeamCheck(ctx context.Context, p *pipeline.Project, segID string, dur float64) error {
	if dur <= 1 {
		return nil
	}
	first := []float64{0.05, 0.15}
	last := []float64{round2(dur - 0.2), round2(dur - 0.1)}
	mid := round2(dur / 2)
	ats := append(append(append([]float64{}, first...), mid), last...)
	atArg := make([]string, len(ats))
	for i, t := range ats {
		atArg[i] = strconv.FormatFloat(t, 'f', 2, 64)
	}
	dir := p.Artifact("reports/seam/" + segID)
	_ = os.RemoveAll(dir) // 输出目录是我们自己的产物，无 junction 风险
	out, err := pipeline.RunCLI(p, "npx", pipeline.HyperframesPkg, "snapshot",
		".hf-check/"+segID, "--at", strings.Join(atArg, ","), "--no-end", "--no-browser-gpu", "-o", dir)
	if err != nil {
		r.Manifest(p, "check.seam_degraded", segID+": "+pipeline.FirstLine(out, 120))
		return nil
	}
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		r.Manifest(p, "check.seam_degraded", segID+": "+rerr.Error())
		return nil
	}
	lumas := map[float64]float64{}
	for _, e := range entries {
		t, ok := frameNameT(e.Name())
		if !ok {
			continue
		}
		img, derr := decodePng(filepath.Join(dir, e.Name()))
		if derr != nil {
			r.Manifest(p, "check.seam_degraded", segID+": "+derr.Error())
			return nil
		}
		lumas[t] = meanLuma(img)
	}
	var firstL, lastL []float64
	for _, t := range first {
		v, ok := lumas[t]
		if !ok {
			r.Manifest(p, "check.seam_degraded", segID+": 缺开场取样帧 t="+strconv.FormatFloat(t, 'f', 2, 64))
			return nil
		}
		firstL = append(firstL, v)
	}
	for _, t := range last {
		v, ok := lumas[t]
		if !ok {
			r.Manifest(p, "check.seam_degraded", segID+": 缺收尾取样帧 t="+strconv.FormatFloat(t, 'f', 2, 64))
			return nil
		}
		lastL = append(lastL, v)
	}
	probs := seamLumaVerdict(lumas[mid], firstL, lastL)
	if len(probs) > 0 {
		return fmt.Errorf("%s: 段间接缝亮度闸未过（修法见修复规则 seam 条）\n✗ %s", segID, strings.Join(probs, "\n✗ "))
	}
	r.Manifest(p, "check.seam", segID+": pass")
	return nil
}

// seamLumaVerdict 首/末帧亮度闸判定（纯函数，便于单测）。mid=段中帧均值亮度
// (0-255)，first/last=开场/收尾各取样点的均值亮度。任一开场取样过暗=黑场开场；
// 任一收尾取样过暗=淡出收黑。绝对黑线 20/255 + 相对塌陷 0.55×中段双条件；暗色
// 设计（中段 <40）绝对线失去意义，只看相对塌陷（阈 0.5）。取样取最暗点判——
// 快启动的淡入在 t=0.05 就现形，慢启动的在 t=0.15 现形。
func seamLumaVerdict(mid float64, first, last []float64) []string {
	floor, ratio := 20.0, 0.55
	if mid < 40 {
		floor, ratio = 0, 0.5
	}
	darkest := func(vs []float64) float64 {
		m := 255.0
		for _, v := range vs {
			if v < m {
				m = v
			}
		}
		return m
	}
	var out []string
	dark := func(when, what, fix string, vs []float64) {
		m := darkest(vs)
		if m < floor || (mid > 0 && m < ratio*mid) {
			out = append(out, fmt.Sprintf("%s：%s取样最暗 %.0f/255（段中 %.0f）——违反接缝契约：%s",
				when, what, m, mid, fix))
		}
	}
	dark("首帧黑场开场", "开场", "首帧即景——t=0 场景（背景/主体/字卡）必须已在画面，入场只做元素级位移/缩放/点亮，禁整页从黑场/纯色空白淡入", first)
	dark("末帧淡出收黑", "收尾", "末帧定格——结尾定格在内容帧，该退的元素提前退完、留下的停稳，禁整页淡出到黑/白", last)
	return out
}

// reFrameAt snapshot 输出帧名（frame-00-at-0.5s.png）→ 时刻。文件名里带时刻是
// 接缝闸对号入座的依据（字典序 ≠ 时刻序，不能按文件名排序配位）。
var reFrameAt = regexp.MustCompile(`^frame-\d+-at-([\d.]+)s\.png$`)

func frameNameT(name string) (float64, bool) {
	m := reFrameAt.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(m[1], 64)
	return f, err == nil
}

// segCompositions 单段合成物落盘（stageCompositions 的段级版，语义同源）：
// llm/comp-<seg>.json → frames html + 元素注册表。
func segCompositions(p *pipeline.Project, segID string) error {
	b, err := os.ReadFile(p.LLMOutput("comp-" + segID))
	if err != nil {
		return fmt.Errorf("缺 %s 场景产物: %w", segID, err)
	}
	var payload struct {
		HTML     string                  `json:"html"`
		Elements []contract.NamedElement `json:"elements"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		return fmt.Errorf("%s 解析失败: %w", segID, err)
	}
	if payload.HTML == "" || len(payload.Elements) == 0 {
		return fmt.Errorf("%s 场景产物缺 html/elements", segID)
	}
	if err := pipeline.WriteFileAtomic(p.Artifact("compositions/frames/"+segID+".html"), []byte(payload.HTML), 0o644); err != nil {
		return err
	}
	reg, _ := json.MarshalIndent(contract.ElementRegistry{Elements: payload.Elements}, "", "  ")
	return pipeline.WriteFileAtomic(p.Artifact("compositions/registry/"+segID+".json"), reg, 0o644)
}

// ── 句子 → 时刻换算（锚点帧坐标）─────────────────────────────────

// sentenceEndTimes 段内每句的结束时刻（词级时间戳累积匹配：词文本拼接（去标点
// 空白）与句文本拼接逐字对齐，句 i 的终点 = 跨过其累计长度的那个词的 end）。
// 匹配不上的句缺时刻（锚点帧少一张，不崩）。
func sentenceEndTimes(sents []contract.Sentence, words []wordTiming) map[string]float64 {
	out := map[string]float64{}
	if len(sents) == 0 || len(words) == 0 {
		return out
	}
	// 累计归一长度（句侧）：归一 = 去空白与标点终符/逗号
	norm := func(s string) string {
		var b strings.Builder
		for _, r := range s {
			switch r {
			case ' ', '\t', '\n', '\r', '，', '。', '！', '？', '；', '：', '、', '…', '—', '·', ',', '.', '!', '?', ';', ':':
			default:
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	walk := 0 // 词游标
	got := 0  // 已消费的归一字符数（词侧，跨句累计）
	want := 0 // 目标累计长度（句侧逐句累加）
	for _, sen := range sents {
		want += len([]rune(norm(sen.Text)))
		if want == 0 {
			continue
		}
		end := 0.0
		matched := false
		for walk < len(words) {
			got += len([]rune(norm(words[walk].Text)))
			end = words[walk].End
			walk++ // 消费过即前移：下一句从下一个词续计（got 不回退——跨句词整词归前句）
			if got >= want {
				matched = true
				break
			}
		}
		if !matched {
			return out // 词流提前耗尽：后面的句无从对齐，保守止步
		}
		out[sen.ID] = end
	}
	return out
}

// anchorFrames 段审锚点帧时刻表（§6.3）：首帧 + 句末帧 + 末帧，封顶 ~6 超出均匀
// 采样。返回（帧表, 各帧标注）。首帧/末帧永远保留（首末拍合规判据的证据）。
// anchorFrameCap 段审锚点帧封顶（首末帧含在内，超出均匀采样）。
const anchorFrameCap = 6

func anchorFrames(sents []contract.Sentence, ends map[string]float64, dur float64) []review.AnchorFrame {
	type cand struct {
		t     float64
		label string
		pin   bool // 首末帧不参与均匀采样淘汰
	}
	var cands []cand
	if len(ends) == 0 {
		// 无句时刻（词流对齐失败）：退化成首/中/末三点
		cands = append(cands,
			cand{t: 0.05, label: "首帧", pin: true},
			cand{t: dur * 0.5, label: "中段帧"},
			cand{t: dur - 0.1, label: "末帧", pin: true},
		)
	} else {
		cands = append(cands, cand{t: 0.05, label: "首帧", pin: true})
		for _, sen := range sents {
			if t, ok := ends[sen.ID]; ok && t > 0.1 && t < dur-0.05 {
				text := sen.Text
				if n := []rune(text); len(n) > 18 {
					text = string(n[:18]) + "…"
				}
				cands = append(cands, cand{t: t, label: fmt.Sprintf("句末帧·此刻刚念完 %s「%s」", sen.ID, text)})
			}
		}
		cands = append(cands, cand{t: dur - 0.1, label: "末帧", pin: true})
	}
	pinned := 0
	for _, c := range cands {
		if c.pin {
			pinned++
		}
	}
	if len(cands) > anchorFrameCap {
		// 均匀采样淘汰句末帧（首末帧保留）：按序号等距保留到配额
		keep := anchorFrameCap - pinned
		var mids []int
		for i, c := range cands {
			if !c.pin {
				mids = append(mids, i)
			}
		}
		keepSet := map[int]bool{}
		for i := 0; i < keep; i++ {
			idx := mids[i*len(mids)/keep]
			keepSet[idx] = true
		}
		var out []cand
		for i, c := range cands {
			if c.pin || keepSet[i] {
				out = append(out, c)
			}
		}
		cands = out
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].t < cands[j].t })
	frames := make([]review.AnchorFrame, 0, len(cands))
	for _, c := range cands {
		if c.t < 0 {
			c.t = 0
		}
		if c.t > dur {
			c.t = dur - 0.05
		}
		frames = append(frames, review.AnchorFrame{T: round2(c.t), Label: c.label})
	}
	return frames
}

// segSentences 段的句编号旁白：prep 分镜稿翻译时旁白 = 句文本按序拼接（无分隔），
// 逐句前缀匹配还原 N 编号；对不上（老分镜/手改稿）从断点截断。
func segSentences(script *contract.Script, seg contract.Segment) []contract.Sentence {
	if script == nil {
		return nil
	}
	var out []contract.Sentence
	rest := seg.Narration
	for _, sen := range script.Sentences {
		text := strings.TrimSpace(sen.Text)
		if text == "" {
			continue
		}
		if strings.HasPrefix(rest, text) {
			out = append(out, sen)
			rest = strings.TrimPrefix(rest, text)
			continue
		}
		break
	}
	return out
}

// segFacts 本段相关事实清单条目（§6.3）：claim 与旁白有 ≥4 字连续交集才算相关
// （滑窗匹配，宁缺勿滥——无关事实只会制造误报素材）。
func segFacts(script *contract.Script, seg contract.Segment) []string {
	if script == nil || len(script.FactSheet) == 0 {
		return nil
	}
	var out []string
	for _, f := range script.FactSheet {
		if factRelevant(f.Claim, seg.Narration) {
			line := f.Claim
			if f.URL != "" {
				line += "（来源 " + f.URL + "）"
			}
			out = append(out, line)
		}
	}
	return out
}

func factRelevant(claim, narration string) bool {
	r := []rune(stripSpaces(claim))
	n := stripSpaces(narration)
	const win = 4
	for i := 0; i+win <= len(r); i++ {
		if strings.Contains(n, string(r[i:i+win])) {
			return true
		}
	}
	return false
}

// stripSpaces 去全部空白（与 contract.containsIgnoreSpaces 同款口径）。
func stripSpaces(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r':
			return -1
		}
		return r
	}, s)
}

// reSegFrameSource 从 check 诊断的 source 路径提取段帧文件名（兼容两种形态：
// 全局 check 的 `compositions/frames/segNN.html` 与段级子工程的绝对路径
// `...\.hf-check\segNN\compositions\frames\segNN.html`——机械修复的落点判定两栖）。
var reSegFrameSource = regexp.MustCompile(`compositions[/\\]frames[/\\](seg\d{2,}\.html)`)

func segFrameFile(source string) (string, bool) {
	m := reSegFrameSource.FindStringSubmatch(source)
	if m == nil {
		return "", false
	}
	return "compositions/frames/" + m[1], true
}
