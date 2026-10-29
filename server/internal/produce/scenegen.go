// scenegen.go —— v2 场景生成：LLM 直写场景文件（模型当创意总监，平台当工厂）。
//
// 替换 v1 的「语义 spec → render_spec 翻译」中间层（plan-v2 §2.5）：每段一次
// LLM 调用直接产出符合 docs/contract.md 的自包含场景 HTML；契约 lint
// （ai/scene_lint.py）做 Tier 0 静态门禁，违规逐条回喂外科修复（≤2 轮）。
// 产物 = llm/comp-segNN.json {html, elements}——与 v1 render_spec 产物同构，
// compositions 落盘阶段 / assemble / check / render / stitch 下游零改动。
package produce

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/agentlog"
	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/prep"
)

// sceneFingerprint 段级场景文件缓存键（内容指纹）：分镜改稿后段 id 复用而
// 旁白/画面提示已换，只看「产物存在」跳过会把旧画面静默复用进新成片。
// 节拍表（导演定调）在场时一并入键：改节拍=改这一段的编排要求，必须过期重生成；
// 无节拍时保持旧拼接格式——老项目指纹不变，段缓存不被无谓冲掉。
func sceneFingerprint(seg contract.Segment) string {
	beats := ""
	if len(seg.Beats) > 0 {
		if b, err := json.Marshal(seg.Beats); err == nil {
			beats = "\x00" + string(b)
		}
	}
	h := sha256.Sum256([]byte(seg.Key + "\x00" + seg.Narration + "\x00" + seg.VisualBrief + "\x00" + strconv.Itoa(int(seg.DurationHint)) + beats))
	return hex.EncodeToString(h[:8])
}

// sceneSidecarPath 指纹旁车（llm/comp-segNN.scene.src）：场景文件落盘时的输入指纹。
func sceneSidecarPath(p *pipeline.Project, segID string) string {
	return p.Artifact("llm/comp-" + segID + ".scene.src")
}

// sceneSidecarFingerprint 段内容指纹 + 设计系统版本（笼子二版本戳）+ 导演统筹
// 版本（弧线/母题改动 → 全片段过期）+ 段落定位注（③重跑重分组 → 受影响段过期）：
// 设计一改（确认重出/字段变化）designFingerprint 变 → 按旧版画的段自动过期重生成；
// 设计未变时指纹不变，缓存照常命中。旧旁车（无冒号后缀）与新格式必然不等 → 首跑全量刷新一次。
// 无导演字段且无段落层（老项目）时后两项为空串，指纹与旧格式逐字节一致（老项目缓存不冲）。
func sceneSidecarFingerprint(p *pipeline.Project, sb *contract.Storyboard, seg contract.Segment) string {
	tail := paraNoteFingerprint(p, seg.ID)
	if p.UnitMode() {
		tail = unitPlanFingerprint(p, seg.ID) // 段落制：单元计划指纹（分组/格子变化→过期）
	}
	return sceneFingerprint(seg) + ":" + designFingerprint(p) + directorFingerprint(sb) + tail
}

// paraNote 段在本片的段落定位注（工件 C 段落层 → 生成侧注入，production-
// architecture-discussion §4 收益①）：“第 2/5 段落「…」·段内第 1/3 段”。
// 同段落相邻段是同一话题的画面延续，段落之间允许换舞台换气质——这是提示词行
// 的语义，注本身只带定位。draft 读不到或段落层为空（老项目）返回空串。
func paraNote(p *pipeline.Project, segID string) string {
	d, err := prep.LoadDraft(p)
	if err != nil {
		return ""
	}
	for pi, para := range d.Paragraphs {
		for si, id := range para.Segs {
			if id == segID {
				return fmt.Sprintf("第 %d/%d 段落「%s」·段内第 %d/%d 段",
					pi+1, len(d.Paragraphs), para.Title, si+1, len(para.Segs))
			}
		}
	}
	return ""
}

// paraNoteFingerprint 段落定位注指纹：段落主题/分组/段内位置任一变化 → 该段过期。
// 空注（老项目）返回空串，指纹与旧格式逐字节一致（同 directorFingerprint 纪律）。
func paraNoteFingerprint(p *pipeline.Project, segID string) string {
	note := paraNote(p, segID)
	if note == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(note))
	return ":" + hex.EncodeToString(sum[:4])
}

// designFingerprint 设计系统内容指纹（style/style_samples.json 文件字节哈希）。
// 文件缺失返回 "-"——样张落地后指纹变化，任何已生成段自然过期。
func designFingerprint(p *pipeline.Project) string {
	b, err := os.ReadFile(p.Artifact("style/style_samples.json"))
	if err != nil {
		return "-"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:4])
}

// sceneConcurrency 场景生成并发度（段间零依赖；默认 3，SCENE_CONCURRENCY/SPEC_CONCURRENCY 可调）。
func sceneConcurrency() int {
	for _, key := range []string{"SCENE_CONCURRENCY", "SPEC_CONCURRENCY"} {
		if n, err := strconv.Atoi(os.Getenv(key)); err == nil && n >= 1 && n <= 9 {
			return n
		}
	}
	return 3
}

// qStyleBook / qSegMaterials 视觉副驾细读的固定问法（2026-10-28 换脑）。同图同问 =
// 同请求体字节：风格画像全片一次、跨段跨修复轮靠磁盘缓存零成本重放——改措辞就是
// 废缓存，动前先想清楚。
const qStyleBook = "以下是本片已确认的风格样张截图与来源视频真迹帧——逐图细读四维画像：配色（底色/强调色/文字色，给近似色值）、质感（颗粒/描边/纹理）、画面密度与层次、字体气质。这是全片视觉的权威参照。"
const qSegMaterials = "以下是本段素材真图（公版检索所得）——逐图细读内容主体与构图，供决定用不用、怎么摆位。"

// sceneGen 每段一个场景文件。instruction = 用户改画要求（最高优先级位，修复轮必须
// 带上否则会丢）；feedback = lint/check/审查回喂（必须修正位）；only 非空只处理该段。
func (r *Runner) sceneGen(ctx context.Context, p *pipeline.Project, instruction, feedback, only string) error {
	sb, err := p.WorkStoryboard() // 段落制=单元表（写画面单位=段落），分镜制=原表
	if err != nil {
		return fmt.Errorf("先完成 storyboard: %w", err)
	}
	voices, err := loadAudioMeta(p)
	if err != nil {
		return err
	}
	// 段落制作业书（2026-10-29 换骨）：单元表在 → 该单元走 paraUnitPrompt
	// （连续时间轴编排+观感补丁）；分镜制走原 sceneUserPrompt。素材两段式仅分镜制。
	unitList := LoadUnits(p)
	unitOf := func(id string) (contract.RenderUnit, bool) {
		for _, u := range unitList {
			if u.ID == id {
				return u, true
			}
		}
		return contract.RenderUnit{}, false
	}
	// 主脑走文本主力档（2026-10-28 换脑 glm-5.3，无多模态）：写画面会话纯文本，
	// 看图活全走视觉副驾（llm.VisionDigest，flash 档）——样张/真迹细读成全片
	// 共享的「风格画像」，素材图段级细读。判卷链（frameQA/段审/终审）仍整段
	// flash 直判，不经这里。
	prov, perr := llm.FromEnv(llm.StationSceneGen)
	if perr != nil {
		return fmt.Errorf("场景生成需要 API 模式（LLM_API_KEY）: %w", perr)
	}
	cv := ProjectCanvas(p)
	golden := goldenExample(r.RootDir)
	// 样张截图（ai/snap_samples.py）→ 视觉副驾的细读原料（不再逐段内嵌进会话）。
	// 带指纹缓存：frameQA/preReview 修复轮对每个失败段各调一次 sceneGen，样张不变
	// 却重复截图（5 段×2 轮=10 次浏览器作业）纯属白付。
	r.snapSamples(ctx, p)
	sampleImgs := styleSampleImages(p)
	// 真迹关键帧（风格来源视频原片帧）：与样张截图一起进风格画像——课堂通道，
	// 与考卷（frameqa 对照真迹）配套；无媒体/压图失败静默缺省，退回纯样张画像。
	keyImgs := styleKeyframeImages(ctx, p)
	if len(keyImgs) > 0 {
		sampleImgs = append(sampleImgs, keyImgs...)
	}
	// 风格画像 styleBook（视觉副驾，每片一次）：样张截图+真迹细读成文字块，全段
	// 逐字共享进稳定前缀。同图同问字节一致——磁盘缓存跨段/跨修复轮零成本复用；
	// 会话变纯文本后，修复轮续写不再因丢图换形态断前缀（此前 seg.write 命中率
	// 22% 的主病灶，docs 见架构图 2026-10-28 版）。副驾失败降级空串：写的一端
	// 退回样张源码+设计系统文字口径，判卷链仍对照真图，闭环没断。
	styleBook := ""
	if shared := visURLs(sampleImgs); len(shared) > 0 {
		at := agentlog.Begin(p, "seg.stylebook", "风格画像细读（视觉副驾）")
		var u openai.Usage
		styleBook, u = llm.VisionDigest(ctx, shared, qStyleBook)
		at.Add(u)
		at.Done(nil)
	}
	// NoThink：画面是结构化 HTML 作业，开思考单段 5-13 分钟；关思考配合段间并发
	// 把全片压进分钟级（v1 spec 路径同款取舍）。
	// segUsage 单段写画面的用量账（首稿→素材二段式→修复轮全算任务总量，不再只记最后一次）。
	// cached 必须单独累计：Total 落账时手工拼 Usage 会丢 PromptTokensDetails，
	// 平台缓存命中就全账归零（2026-10-20 实测片写画面真实命中 21-99% 却记 0 的根因）。
	type segUsage struct {
		rec                   *llm.Transcript
		prompt, compl, cached int
		calls                 int
	}
	genCached := func(u openai.Usage) int {
		if u.PromptTokensDetails != nil {
			return u.PromptTokensDetails.CachedTokens
		}
		return 0
	}
	// digest 视觉副驾细读 + 用量并入段账（prompt/compl/cached/calls 全记——副驾
	// 花的钱是写画面成本的一部分，账面不能漏）。
	digest := func(acc *segUsage, images []visImg, question string) string {
		if len(images) == 0 {
			return ""
		}
		d, u := llm.VisionDigest(ctx, visURLs(images), question)
		acc.prompt += u.PromptTokens
		acc.compl += u.CompletionTokens
		if u.PromptTokensDetails != nil {
			acc.cached += u.PromptTokensDetails.CachedTokens
		}
		acc.calls++
		return d
	}
	gen := func(acc *segUsage, user string, out any) (openai.Usage, error) {
		if acc.rec != nil {
			if acc.calls == 0 { // 系统提示（设计系统/皮肤笼子）每任务记一次：留痕里看得见注入了什么约束
				acc.rec.Message(openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: sceneSystemFor()})
			}
			acc.rec.Message(openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: user})
		}
		// 纯文本主脑（2026-10-28 换脑）：图不入会话——样张/真迹走全片共享的风格
		// 画像（styleBook），素材走段级细读；NoThink 取舍不变（结构化 HTML 作业）。
		u, err := prov.GenerateJSONNoThink(ctx, sceneSystemFor(), user, out)
		acc.prompt += u.PromptTokens
		acc.compl += u.CompletionTokens
		acc.cached += genCached(u)
		acc.calls++
		if err != nil && acc.rec != nil {
			acc.rec.Note("模型调用失败: %v", err)
		}
		if err == nil && acc.rec != nil { // 模型回复落留痕（场景 JSON；超长由留痕侧截断）
			if b, merr := json.Marshal(out); merr == nil {
				acc.rec.Message(openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: string(b)})
			}
		}
		return u, err
	}
	// genCont 续写调用（D2）：完整消息数组出站（JSON 模式/NoThink 与 gen 同款）。
	// 留痕只记新追加的指令与回复——历史消息首轮已记过，重启恢复后的续写任务另起
	// 留痕，记一笔来源说明即可。
	genCont := func(acc *segUsage, msgs []openai.ChatCompletionMessage, out any) (openai.Usage, error) {
		if acc.rec != nil {
			acc.rec.Note("续写修复第 %d 轮（消息数组 %d 条，历史不重录）", acc.calls, len(msgs))
			acc.rec.Message(msgs[len(msgs)-1])
		}
		u, err := prov.GenerateJSONContinue(ctx, msgs, 0.7, llm.EffortDisabled, out)
		acc.prompt += u.PromptTokens
		acc.compl += u.CompletionTokens
		acc.cached += genCached(u)
		acc.calls++
		if err != nil && acc.rec != nil {
			acc.rec.Note("续写调用失败: %v", err)
		}
		if err == nil && acc.rec != nil {
			if b, merr := json.Marshal(out); merr == nil {
				acc.rec.Message(openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: string(b)})
			}
		}
		return u, err
	}
	// contRepair 续写修复一轮（D2/§3.3）：模型回 patch 改动单（程序确定性套用，
	// C3「改一处重抄全文」的正主）或整份 html；改动单打不中自动追加一条「改出
	// 整份」再试一次——宁回退整份重写，不出「改了一半」的产物。
	// 返回新场景与新消息数组（含本轮 user+assistant，验收后写回历史）。
	contRepair := func(acc *segUsage, msgs []openai.ChatCompletionMessage, cur scenePayload, userMsg string) (scenePayload, []openai.ChatCompletionMessage, error) {
		msgs = append(append([]openai.ChatCompletionMessage{}, msgs...),
			openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: userMsg})
		var fixed struct {
			Note     string                  `json:"note"`
			HTML     string                  `json:"html"`
			Patches  []scenePatch            `json:"patches"`
			Elements []contract.NamedElement `json:"elements"`
		}
		if _, err := genCont(acc, msgs, &fixed); err != nil {
			return cur, msgs, err
		}
		if fixed.HTML != "" { // 整份重写（结构/时序大改的合法通道）
			if len(fixed.Elements) == 0 {
				return cur, msgs, fmt.Errorf("整份重写缺 elements 注册表")
			}
			np := scenePayload{HTML: fixed.HTML, Elements: fixed.Elements}
			return np, append(msgs, openai.ChatCompletionMessage{
				Role: openai.ChatMessageRoleAssistant, Content: scenePayloadJSON(np.HTML, np.Elements)}), nil
		}
		if len(fixed.Patches) > 0 {
			newHTML, perr := applyScenePatches(cur.HTML, fixed.Patches)
			if perr == nil {
				np := scenePayload{HTML: newHTML, Elements: cur.Elements}
				return np, append(msgs, openai.ChatCompletionMessage{
					Role: openai.ChatMessageRoleAssistant, Content: scenePayloadJSON(np.HTML, np.Elements)}), nil
			}
			if acc.rec != nil {
				acc.rec.Note("改动单套用失败，本轮回退整份重写: %v", perr)
			}
			msgs = append(msgs, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: "改动单套用失败：" + perr.Error() + "\n改为输出整份文件：{\"note\":\"…\",\"html\":\"<template>…整份场景文件…</template>\",\"elements\":[{\"id\":\"…\",\"name\":\"…\"}]}"})
			var full struct {
				HTML     string                  `json:"html"`
				Elements []contract.NamedElement `json:"elements"`
			}
			if _, err := genCont(acc, msgs, &full); err != nil {
				return cur, msgs, err
			}
			if full.HTML == "" || len(full.Elements) == 0 {
				return cur, msgs, fmt.Errorf("补丁打不中且整份重写缺 html/elements")
			}
			np := scenePayload{HTML: full.HTML, Elements: full.Elements}
			return np, append(msgs, openai.ChatCompletionMessage{
				Role: openai.ChatMessageRoleAssistant, Content: scenePayloadJSON(np.HTML, np.Elements)}), nil
		}
		return cur, msgs, fmt.Errorf("修复输出既无 html 也无 patches")
	}
	// 样张源码（文字样式规范）全段同一份：循环外读一次盘
	sampleSrc := sampleStyleNote(p)
	// 设计系统共享主体（七刀#4）：全段逐字一致，循环外取一次（段指派在 work 内按段号取）
	designShared, _ := designNotesFor(p, 0)
	if feedback != "" && only == "" { // 全片重生成：先清旧产物（含会话历史——宁无历史不错历史）
		for _, seg := range sb.Segments {
			_ = os.Remove(p.Artifact("llm/comp-" + seg.ID + ".json"))
			removeSceneHistory(p, seg.ID)
		}
	}
	onlySet := map[string]bool{}
	for _, id := range strings.Split(only, ",") {
		if id = strings.TrimSpace(id); id != "" {
			onlySet[id] = true
		}
	}
	var todo []contract.Segment
	for _, seg := range sb.Segments {
		if len(onlySet) > 0 && !onlySet[seg.ID] {
			continue
		}
		out := p.Artifact("llm/comp-" + seg.ID + ".json")
		if _, err := os.Stat(out); err == nil && feedback == "" && instruction == "" {
			// 已有产物还要过指纹关：旁车指纹（段内容+设计版本+导演统筹）与当前一致才复用
			// （分镜改稿/设计改稿/弧线改动生效的关键闸）。
			if fp, ferr := os.ReadFile(sceneSidecarPath(p, seg.ID)); ferr == nil && string(fp) == sceneSidecarFingerprint(p, sb, seg) {
				continue
			}
		}
		todo = append(todo, seg)
	}
	sem := make(chan struct{}, sceneConcurrency())
	var wg sync.WaitGroup
	var errMu sync.Mutex
	firstErr := error(nil)
	fail := func(err error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
	}
	work := func(seg contract.Segment) {
		at := agentlog.Begin(p, "seg.write."+seg.ID, "写画面 "+seg.ID)
		acc := &segUsage{rec: at.Rec()}
		var werr error
		defer func() {
			at.Total(openai.Usage{
				PromptTokens:     acc.prompt,
				CompletionTokens: acc.compl,
				PromptTokensDetails: &openai.PromptTokensDetails{
					CachedTokens: acc.cached,
				},
			}, acc.calls)
			at.Done(werr)
		}()
		v := voices[seg.ID]
		pn := paraNote(p, seg.ID) // 段落定位注（工件 C 段落层；老项目空串不加行）
		sceneM, compM := methodsFor(r.RootDir, seg)
		// 稳定前缀布局（七刀#4）：跨段不变的（样张源码/真迹说明/设计系统主体/风格
		// 画像）连成整块进共享前缀；段变量（导演统筹/本段版式指派/素材细读）全部
		// 垫尾——导演统筹块先前置是可读性取舍，缓存计价下让位（语义由提示词行文自明）。
		styleShared := sampleSrc + keyframeNote(keyImgs) + designShared
		if styleBook != "" {
			styleShared += "\n## 风格画像（视觉副驾对样张截图与真迹帧的细读——全片共享，视觉权威参照）\n" + styleBook + "\n"
		}
		_, styleSeg := designNotesFor(p, seg.Idx)
		styleSeg = directorNoteFor(sb, seg) + styleSeg
		segMats := materialsForSeg(p, seg.ID)
		if len(segMats) > 0 {
			if d := digest(acc, materialVisImgs(p, segMats), qSegMaterials); d != "" {
				styleSeg += "\n- 素材真图细读（视觉副驾，公版检索所得——看清内容再决定用不用、怎么摆位）：\n" + d + "\n"
			} else {
				styleSeg += materialNote(len(segMats))
			}
			styleSeg += materialsNote(segMats)
		}
		promptFor := func(fb, prev string) string {
			if u, ok := unitOf(seg.ID); ok {
				return paraUnitPrompt(seg, v, u, unitList, instruction, fb, cv, styleShared, styleSeg, golden, sceneM, compM, prev)
			}
			return sceneUserPrompt(seg, v, instruction, fb, cv, styleShared, styleSeg, golden, sceneM, compM, len(segMats) == 0, prev, pn)
		}
		fp := sceneSidecarFingerprint(p, sb, seg)
		var payload struct {
			HTML          string                  `json:"html"`
			Elements      []contract.NamedElement `json:"elements"`
			ImageRequests []materialRequest       `json:"image_requests,omitempty"`
		}
		// 续写路径（D2，docs/cost-optimization-plan §3.2）：修复/rework 轮且有可用
		// 会话历史 → 修复指令追加在原对话上（模型带着首轮全上下文改自己的稿），
		// 不再重发全套注入包+prevHTML。历史缺失/指纹失配/超帽/续写失败 → 落回
		// 下方全新会话路径，行为同旧版。
		var hist *sceneHistory
		var contMsgs []openai.ChatCompletionMessage
		if instruction != "" || feedback != "" {
			if h := loadSceneHistory(p, seg.ID, fp); h != nil {
				var cur scenePayload
				if json.Unmarshal([]byte(h.Messages[len(h.Messages)-1].Content), &cur) == nil &&
					cur.HTML != "" && len(cur.Elements) > 0 {
					np, msgs2, cerr := contRepair(acc, h.Messages, cur, sceneContRepairMsg(instruction, feedback))
					if cerr == nil {
						payload.HTML, payload.Elements = np.HTML, np.Elements
						contMsgs = msgs2
						hist = h
					}
				}
			}
		}
		if hist == nil { // 全新会话（首写或续写不可用的修复兜底，行为同旧版）
			user := promptFor(feedback, prevSceneHTML(p, seg.ID))
			_, gerr := gen(acc, user, &payload)
			if gerr != nil {
				werr = fmt.Errorf("%s 场景生成失败: %w", seg.ID, gerr)
				fail(werr)
				return
			}
			// 素材申请单（两段式）：第一段式模型交了申请 → 检索公版图 → 带图出终稿。
			// 检索全落空就按无素材继续——第一段式的画面稿直接生效，不挡出片。
			if len(segMats) == 0 && len(payload.ImageRequests) > 0 {
				if mats := r.fulfillRequests(ctx, p, seg.ID, cleanRequests(payload.ImageRequests)); len(mats) > 0 {
					segMats = mats
					if d := digest(acc, materialVisImgs(p, segMats), qSegMaterials); d != "" {
						styleSeg += "\n- 素材真图细读（视觉副驾，公版检索所得——看清内容再决定用不用、怎么摆位）：\n" + d + "\n"
					} else {
						styleSeg += materialNote(len(segMats))
					}
					styleSeg += materialsNote(segMats)
					_, err2 := gen(acc, promptFor(feedback, ""), &payload)
					if err2 != nil {
						werr = fmt.Errorf("%s 带素材终稿生成失败: %w", seg.ID, err2)
						fail(werr)
						return
					}
				}
			}
		}
		errs := sceneValidate(r, ctx, p, payload.HTML, seg.ID)
		for repair := 0; repair < 2 && len(errs) > 0; repair++ { // 外科修复：违规逐条回喂
			var err2 error
			if hist != nil { // 续写会话：lint 违规也走 patch 优先的续写修复
				var np scenePayload
				np, contMsgs, err2 = contRepair(acc, contMsgs,
					scenePayload{HTML: payload.HTML, Elements: payload.Elements},
					"上一版场景文件被契约检查拒绝，逐条修正（只修列出的错，其余保持原样）：\n- "+strings.Join(errs, "\n- "))
				if err2 == nil {
					payload.HTML, payload.Elements = np.HTML, np.Elements
				}
			} else {
				fb := instruction + feedback + "\n上一版场景文件被契约检查拒绝，必须逐条修正：\n- " + strings.Join(errs, "\n- ")
				var fixed struct {
					HTML     string                  `json:"html"`
					Elements []contract.NamedElement `json:"elements"`
				}
				_, err2 = gen(acc, promptFor(fb, payload.HTML), &fixed)
				if err2 == nil {
					payload.HTML, payload.Elements = fixed.HTML, fixed.Elements
				}
			}
			if err2 != nil {
				werr = fmt.Errorf("%s 场景修复失败: %w", seg.ID, err2)
				fail(werr)
				return
			}
			errs = sceneValidate(r, ctx, p, payload.HTML, seg.ID)
		}
		if len(errs) > 0 {
			if instruction == "" && feedback == "" && r.fallbackOldScene(p, seg.ID, "重生成契约检查三轮未过") {
				return
			}
			werr = fmt.Errorf("%s 契约检查三轮未过：\n- %s", seg.ID, strings.Join(errs, "\n- "))
			fail(werr)
			return
		}
		if len(payload.Elements) < 3 {
			if instruction == "" && feedback == "" && r.fallbackOldScene(p, seg.ID, "重生成的元素注册表不足 3 条") {
				return
			}
			werr = fmt.Errorf("%s 元素注册表不足 3 条（元素级指认依赖它）", seg.ID)
			fail(werr)
			return
		}
		// 会话历史落盘（D2）：续写轮把成对消息写回并计数；全新会话轮（首写/兜底
		// 重掷）重建为 [user(完整提示但不带 prevHTML), assistant(成稿)]——prevHTML
		// 已被 assistant 成稿取代，存进历史只会让每轮续写白背一份旧稿。
		if hist != nil {
			hist.Messages = contMsgs
			hist.Cont++
			saveSceneHistory(p, seg.ID, hist)
		} else {
			saveSceneHistory(p, seg.ID, &sceneHistory{
				FP: fp,
				Messages: []openai.ChatCompletionMessage{
					{Role: openai.ChatMessageRoleUser, Content: promptFor(feedback, "")},
					{Role: openai.ChatMessageRoleAssistant, Content: scenePayloadJSON(payload.HTML, payload.Elements)},
				},
			})
		}
		b, _ := json.MarshalIndent(payload, "", "  ")
		if err := pipeline.WriteFileAtomic(p.Artifact("llm/comp-"+seg.ID+".json"), b, 0o644); err != nil {
			werr = err
			fail(err)
			return
		}
		// 指纹旁车紧随产物落盘（写失败按过期处理：下次重生成，宁重复不陈旧）。
		_ = os.WriteFile(sceneSidecarPath(p, seg.ID), []byte(sceneSidecarFingerprint(p, sb, seg)), 0o644)
		p.Manifest("llm.usage", fmt.Sprintf("scene %s prompt=%d completion=%d tokens", seg.ID, acc.prompt, acc.compl))
		r.emit(p.ID, "progress", "compositions", fmt.Sprintf("%s 场景文件 ✓（%d 元素）", seg.ID, len(payload.Elements)))
	}
	for _, seg := range todo {
		wg.Add(1)
		sem <- struct{}{}
		go func(seg contract.Segment) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := ctx.Err(); err != nil {
				fail(err)
				return
			}
			work(seg)
		}(seg)
	}
	wg.Wait()
	return firstErr
}

// snapSamplesCache 样张截图指纹缓存（key=项目目录）：指纹取 style_samples.json 的
// mtime+size——样张重新生成（style_samples 阶段重写文件）指纹自然变化触发重截；
// 截图失败不缓存，下轮 sceneGen 自动重试。包级 map + 锁：Runner 可能被测试重建，
// 键用项目目录与具体实例解耦。
var (
	snapSamplesMu    sync.Mutex
	snapSamplesCache = map[string]string{} // 项目目录 → 上次成功截图时的样张指纹
)

// styleSamplesFingerprint 样张文件的 mtime+size 指纹（缺失返回 "-"，照常尝试截图，
// 失败不缓存所以缺失态不会被钉死）。
func styleSamplesFingerprint(p *pipeline.Project) string {
	fi, err := os.Stat(p.Artifact("style/style_samples.json"))
	if err != nil {
		return "-"
	}
	return fmt.Sprintf("%d|%d", fi.ModTime().UnixNano(), fi.Size())
}

// snapSamples 样张截图（指纹未变则跳过重跑）。失败只发进度提示不挡生成。
func (r *Runner) snapSamples(ctx context.Context, p *pipeline.Project) {
	fp := styleSamplesFingerprint(p)
	snapSamplesMu.Lock()
	cached := snapSamplesCache[p.Dir] == fp
	snapSamplesMu.Unlock()
	if cached {
		return
	}
	if err := r.python(ctx, "ai/snap_samples.py", p.Dir, time.Minute); err != nil {
		r.emit(p.ID, "progress", "compositions", "样张截图失败，风格注入退回纯文本")
		return
	}
	snapSamplesMu.Lock()
	snapSamplesCache[p.Dir] = fp
	snapSamplesMu.Unlock()
}

// fallbackOldScene 段级重生成反复过不了契约时，只要上一版产物还在就沿用旧画面
// 继续出片——宁要旧设计的合格段，不要没有成片（2026-10-11 夜跑教训：一段连挂
// 三轮把整片拖死，且每次重掷都是新骰子；2026-10-19 高-11：旧门槛 only=="" 把
// 段级流水线（恒传段号）挡在门外，首写顽固失败只能整片报废）。
// 指纹故意不刷新：下轮制作还会再试重生成，过了自然换新。
// instruction/feedback 非空（用户点名改段 / 审查打回重写）不适用：那是明确要
// 改这一段，静默沿用旧画面等于没改——审查重写的收敛由签名刹车+带伤放行负责。
func (r *Runner) fallbackOldScene(p *pipeline.Project, segID, why string) bool {
	old := p.Artifact("llm/comp-" + segID + ".json")
	if info, err := os.Stat(old); err != nil || info.IsDir() || info.Size() == 0 {
		return false
	}
	p.Manifest("scene.fallback", fmt.Sprintf("%s %s，沿用旧画面", segID, why))
	r.emit(p.ID, "progress", "compositions", fmt.Sprintf("%s 重生成未过契约，沿用旧画面继续", segID))
	return true
}

// sceneSystemFor 系统提示词只装跨项目通用设定：角色、工作内容、兜底原则、产出
// 格式（用户原则 2026-10-29，与 agent 工具 description 边界化同源）。项目相关的
// 一切——风格方向、贯穿装置、设计系统细则——由作业正文（user 侧）注入，且注入块
// 自带权威性说明（风格画像/设计系统的节标题），注入了什么模型就看什么，system
// 不做元描述。未注入风格的兜底（中性版式）是工作原则而非项目内容，留在这里。
func sceneSystemFor() string {
	return "你是视频的画面工程师，为一段视频直接写可渲染的场景 HTML。\n" +
		"未注入风格与设计系统时，保持干净克制的中性版式——高对比配色、少装饰。\n" +
		"只输出 JSON。"
}

// goldenExample 契约金样例（ai/contract-example/seg-golden.html）作为结构 few-shot。
func goldenExample(rootDir string) string {
	b, err := os.ReadFile(filepath.Join(rootDir, "ai", "contract-example", "seg-golden.html"))
	if err != nil {
		return ""
	}
	return string(b)
}

// sceneValidate 契约校验：Go 结构检查 + ai/scene_lint.py 静态门禁（Tier 0）+
// 素材引用白名单（assets/img/ 引用必须命中素材清单，清单外文件渲染必裂图）。
func sceneValidate(r *Runner, ctx context.Context, p *pipeline.Project, html, segID string) []string {
	var errs []string
	if strings.TrimSpace(html) == "" {
		return []string{"html 为空"}
	}
	if !strings.Contains(html, "<template") {
		errs = append(errs, "文件必须是 <template> 片段（无 DOCTYPE/html/head/body）")
	}
	if !strings.Contains(html, `data-composition-id="`+segID+`"`) {
		errs = append(errs, fmt.Sprintf("缺 data-composition-id=%q（根元素，逐字一致）", segID))
	}
	if !strings.Contains(html, `window.__timelines["`+segID+`"]`) {
		errs = append(errs, fmt.Sprintf("未注册 window.__timelines[%q]", segID))
	}
	if n := strings.Count(html, "data-hf-name="); n < 3 {
		errs = append(errs, fmt.Sprintf("data-hf-name 语义标注仅 %d 处（<3；元素级指认依赖它）", n))
	}
	// 字幕是平台层（assemble 注入）：模型写逐词标注 = 越权且必然与 TTS 时间戳冲突。
	if strings.Contains(html, "data-ph=") || strings.Contains(html, "data-w=") {
		errs = append(errs, "禁止字幕标注（data-ph/data-w）——字幕带由平台按 TTS 时间戳机械装配，内容留在上 83%")
	}
	errs = append(errs, materialWhitelistCheck(p, html)...)
	errs = append(errs, r.sceneLintWithVend(ctx, html)...)
	return errs
}

// sceneLintRun 跑 ai/scene_lint.py（临时文件进、✗ 行出）。lint 跟任务 ctx 走：
// 用户取消后不再让子进程跑满全程。
func (r *Runner) sceneLintRun(ctx context.Context, html string) []string {
	tmp, err := os.CreateTemp("", "scene-lint-*.html")
	if err != nil {
		return []string{"lint 无法建临时文件: " + err.Error()}
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.WriteString(html); err != nil {
		tmp.Close()
		return []string{"lint 临时文件写失败: " + err.Error()}
	}
	tmp.Close()
	// pipeline.CommandContext：取消时整棵进程树击杀，python 拉起的孙进程不残留
	cmd := pipeline.CommandContext(ctx, pipeline.PythonExe(), filepath.Join(r.RootDir, "ai", "scene_lint.py"), name)
	cmd.Dir = r.RootDir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	var errs []string
	for _, ln := range strings.Split(string(out), "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "✗") || strings.HasPrefix(ln, "  ✗") {
			errs = append(errs, strings.TrimPrefix(strings.TrimSpace(ln), "✗ "))
		}
	}
	if len(errs) == 0 { // lint 崩了（非违规）：报原文，别静默放行
		errs = append(errs, "scene_lint 异常: "+pipeline.FirstLine(string(out), 3))
	}
	return errs
}

// ── vend 自动闭环（docs/vend-stage.md §3）：模型只有申请权（场景内 <!-- vend: lib@ver -->），
// lint 报缺库 → 平台代码代为采购（npm 钉版+验收入册）→ 复检放行；采购失败按"路的问题"
// 原样上抛转人工（§5），不伪装成场景违规回喂模型。

var vendMu sync.Mutex // 段间并发（SCENE_CONCURRENCY）采购串行化：manifest 写与 tarball 落盘防竞态

var missingLibRe = regexp.MustCompile(`缺库:([A-Za-z0-9_.\-]+)@([A-Za-z0-9_.\-]+)\s`)

// missingLibSpecs 从 lint 违规清单提取缺库申请单（A3），去重排序（采购顺序确定）。
func missingLibSpecs(errs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range errs {
		if m := missingLibRe.FindStringSubmatch(e); m != nil {
			spec := m[1] + "@" + m[2]
			if !seen[spec] {
				seen[spec] = true
				out = append(out, spec)
			}
		}
	}
	sort.Strings(out)
	return out
}

// vendorEnsure 跑 ai/vendor_tool.py ensure（确定性代码执行；成员文件由 npm 元数据自动定位）。
// 超时从任务 ctx 派生：npm 采购最长 3 分钟，用户取消后不该继续占着。
func (r *Runner) vendorEnsure(ctx context.Context, spec string, timeout time.Duration) error {
	vendMu.Lock()
	defer vendMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// pipeline.CommandContext：取消时整棵进程树击杀，npm 采购拉起的孙进程不残留
	cmd := pipeline.CommandContext(ctx, pipeline.PythonExe(), filepath.Join(r.RootDir, "ai", "vendor_tool.py"),
		"ensure", spec, "--allow-pending-replay")
	cmd.Dir = r.RootDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", pipeline.FirstLine(string(out), 2))
	}
	return nil
}

// sceneLintWithVend = lint + 缺库自动采购：申请单全数采到 → 复检放行（以复检为准）；
// 有采购失败 → 非缺库违规 + 采购失败一起返回（转人工，不静默）。
func (r *Runner) sceneLintWithVend(ctx context.Context, html string) []string {
	errs := r.sceneLintRun(ctx, html)
	specs := missingLibSpecs(errs)
	if len(specs) == 0 {
		return errs
	}
	var nonLib []string
	for _, e := range errs {
		if !strings.Contains(e, "缺库:") {
			nonLib = append(nonLib, e)
		}
	}
	var buyErrs []string
	for _, spec := range specs {
		if err := r.vendorEnsure(ctx, spec, 3*time.Minute); err != nil {
			buyErrs = append(buyErrs, "vend 采购失败 "+spec+": "+err.Error())
		}
	}
	if len(buyErrs) > 0 {
		return append(nonLib, buyErrs...)
	}
	return r.sceneLintRun(ctx, html)
}

// sceneUserPrompt 场景生成作业正文。稳定前缀布局（七刀#4，docs/cost-optimization-plan
// §6 表#4）：跨段不变的部分（契约摘要/内容忠实/设计系统主体/样张源码/金样例/布局
// 规则/动效手感）连成整块前置，段变量（段信息/导演统筹/本段版式指派/方法参考/
// 词序表/用户指令/修复反馈）全部垫尾——GLM 前缀缓存按公共前缀计价，段信息插在
// 共享文本中间曾把共同前缀斩断在 ≈2944 token（=系统+3样张+契约头），后面的共享
// 文本全够不着。契约摘要里的段 id/段总时长也随之去段化（指向文末段信息）。
// allowAsk=本段尚无素材，开放真图申请单（image_requests）；已有素材带图生成时关闭。
// prevHTML=上一版场景文件（修复轮回喂底稿，首稿为空）。
func sceneUserPrompt(seg contract.Segment, v voiceMeta, instruction, feedback string, cv contract.Canvas, styleShared, styleSeg, golden, sceneTpl, compMethods string, allowAsk bool, prevHTML string, paraNote string) string {
	var wb strings.Builder
	for i, w := range v.Words {
		if i > 0 && i%10 == 0 {
			wb.WriteString("\n")
		}
		fmt.Fprintf(&wb, "%d:%s@%.1fs ", i, w.Text, w.Start)
	}
	// 段落定位行（工件 C 段落层，§4 收益①）：结构信息紧跟标题；老项目空注不加行
	paraLine := ""
	if paraNote != "" {
		paraLine = "- 段落定位：" + paraNote + "（同段落相邻段是同一话题的画面延续，画面要接得住上段末拍；段落之间是话题切换，允许换舞台换气质）\n"
	}
	rules := "- 内容安全区 x∈[0,1080]、y∈[0,1594]；底部 1594px 起是平台字幕带，禁放内容\n" +
		"- 顶部两角是平台角标保留区（启用章节时左上放章名、右上放计数器）：内容避开左上(x<280,y<150)与右上(x>800,y<150)两个角域\n" +
		"- 竖屏 1080×1920：构图自己按本段画面与设计系统定——竖屏画幅天然适合纵向叙事，主体尺寸与位置不受限"
	if cv.Aspect == "16:9" {
		rules = "- 内容安全区 x∈[0,1920]、y∈[0,896]；底部 896px 起是平台字幕带，禁放内容\n" +
			"- 顶部两角是平台角标保留区（启用章节时左上放章名、右上放计数器）：内容避开左上(x<500,y<150)与右上(x>1420,y<150)两个角域\n" +
			"- 横屏 1920×1080：构图自己按本段画面与设计系统定——主体尺寸、位置、层次都由你编排"
	}
	instrBlock := ""
	briefLabel := "画面提示"
	if instruction != "" {
		instrBlock = "\n## 用户修改要求（最高优先级——这是重画指令，与「段信息」里的原画面提示冲突时一律以用户要求为准，逐字落实）\n" + instruction + "\n"
		briefLabel = "原画面提示（旧版，仅参考）"
	}
	goldenBlock := ""
	if golden != "" {
		goldenBlock = "\n## 结构金样例（结构/时序/canvas 写法照这个模式；视觉内容必须按本段重写）\n```html\n" + golden + "\n```\n"
	}
	askBlock, reqField := "", ""
	if allowAsk {
		askBlock = materialAskBlock
		reqField = `"image_requests":[{"query":"英文检索词","why":"画面哪里要用一句话"}],`
	}
	return fmt.Sprintf(`为这一段视频直接写场景文件 HTML（你是画面工程师，版式/动效/图形程序全部自己写）。

## 契约摘要（完整版 docs/contract.md；违反任意一条会被 lint 拒收）
1. 整个文件 = 一个 <template> 片段，无 DOCTYPE/html/head/body
2. 根元素 data-composition-id="<段id>"（逐字用文末「段信息」给的段 id）data-width="%d" data-height="%d"；内部 #root 为舞台
3. 恰好一个 gsap.timeline({paused:true})，末尾注册 window.__timelines["<段id>"]
4. 全屏轨道层（背景等）带 class="clip" + data-start/data-duration/data-track-index
5. 每个有意义的可见元素带 data-hf-name="人话名"（≥3 个；元素级指认依赖它）
%s8. 字体：只准用渲染端实际安装的这些——微软雅黑 Microsoft YaHei、黑体 SimHei、宋体 SimSun、楷体 KaiTi、仿宋 FangSong、Consolas、Segoe UI、Georgia、Arial、Times New Roman、Courier New、Impact（外加 serif/sans-serif/monospace 通用族收尾）；
   例外：注入的设计系统展示字体（目录字体，或样张阶段自装的字体——如 ZCOOL QingKe HuangYou / 自装像素体）可以直接用，平台保证声明与字体文件；
   引用即声明（@font-face{font-family:'X';src:local('X');}）；除此之外禁 PingFang SC、思源 Source Han/Noto 等未安装字体——lint 对未声明字体会一票否决整段；
   禁止在 CSS transform 里做居中（translateX(-50%%) 等）再让 GSAP 补间同一元素——GSAP 会整体覆盖 CSS transform，居中必须用 GSAP 的 xPercent/yPercent；
   元素的初始缩放/位移状态（如 scale(1.12)、scaleX(0)）一律写进 GSAP 的 fromTo from 值，CSS 里禁写 transform 声明——lint 对 CSS transform 与 GSAP 补间同元素一票否决
9. 不写字幕——底部字幕带由平台注入；内容留在上 83%%，禁止出现 data-ph=/data-w= 标注
10. 元素揭示随旁白节拍分布（对齐文末词序表，不前载不拖尾）
11. 需要新框架（如 three.js）：在 <template> 内写注释声明 `+"`<!-- vend: 包名@精确版本 -->`"+`，
    平台自动采购并注入（模型不写任何 <script> 标签，含本地路径也不行）
12. 段间接续契约（机检按首/末帧亮度闸直接打回违反者）：首帧即景——t=0 背景与主体场景已在画面，
    入场只做元素级位移/缩放/点亮，禁整页从黑场/纯色空白淡入开场；末帧定格——结尾定格在内容帧，
    该退的元素提前退完、留下的停稳，禁整页淡出到黑/白

## 内容忠实（硬要求——机检只查工程不查内容，画错了直接进成片没人拦）
- 画面提示里的场景设定与首拍/末拍承诺必须可见落实：首拍按承诺开场，末拍收在承诺的画面状态
- 旁白点到的关键实体（人物/物件/数字/年份/地名）画面里必须可辨认；旁白没提到的主体元素不要凭空添加
- 画面中的文字（字卡/标签/数字/单位）与旁白和画面提示逐字一致，禁止自创数据
%s
%s
## 布局规则
%s
- 每屏至少一个可辨认的视觉重心（它是什么、多大、放哪由你按本段画面决定）；
  禁止全屏无差别小元素平铺；段落适合铺氛围时优先写背景层（渐变/纹理/程序化画布），别停在纯色底
- 样张/参考图里的场记标注文字（「N2 · 字锚点亮 + 梦尘粒子」「样张 A」「图N = …」这类制作注记）
  是打样批注，不是画面内容——严禁把它们写进场景；场景里的每一条可见文字都必须是观众该读的内容
- 字卡/标题类大字不得逐字重复本段旁白句——旁白原文由底部字幕承载，字卡再放一遍等于观众读两遍同句话；
  字卡只提炼关键词/短语（如旁白「它开始放一场只有你能看的电影，这就是梦」→ 字卡「梦」或「只为你放映」）

## 动效手感（防呆板——节奏与编排的硬要求，手法本身自由）
- 入场手法从设计系统「入场手法菜单」取用并轮换：同一段至少两种手法、相邻段主手法不重复；全片只用一种「淡入+浮入」=PPT，打回
- 段内按设计系统「段内节奏」下限排事件：旁白每转一个信息点画面必须响应（新元素入场/已有元素强调变色/运镜变向）；入场完成后静坐到段尾=PPT，打回
- 运镜按设计系统「运镜」条执行（安静段/定格也是合法设计，全片没有一处可感知运动才是失败）
- 禁所有元素同秒砸入：入场错峰对齐旁白短语节拍，间隔有快慢变化，不要均匀排队
- 缓动与时长按设计系统档位取用，与本段气质一致即可——手法与性格你自己定
- 扫屏条/横移/滑轨类位移一律用 x/y transform 补间（元素锚定后改 x），禁补间 left/top——
  那是布局属性，逐帧重排会卡顿且过不了检查
- 禁 ease:"none" 的平移/淡入裸入场（canvas 驱动线本身的 ease:"none" 不受此限，那是时钟）：
  入场要有性格；canvas 内运动把缓动写进 f(t) 包络，同样禁匀速直线
- 次要元素跟随主元素滞后 0.1–0.3s 入场，成组出现要有主次
- 出场干脆不拖尾：该退的元素在旁白收尾前退完，不恋战；段尾画面定格在内容帧（接续契约收尾半条，整页淡出收黑会被亮度闸打回）

## 段信息（本段专属——从这里到文末逐段不同；上文契约/设计/样张/金样例/规则为全片共享）
- 段 id（根元素 data-composition-id 与 timeline 注册逐字用它）：%s
- 标题：%s
%s- 旁白（%d 词，%.1f 秒）：%s
- 段总时长：%.3f 秒（timeline 全长，含 %.2fs 收尾垫；canvas 接线里的 duration 用它）
- %s：%s
%s
%s
%s
## 词序表（reveal 对齐用）
%s
%s
%s
%s
## 输出（只输出一个 JSON 对象：第一个字符必须是 {、最后一个字符必须是 }，前后不许有任何字、标签、围栏或解释）
{"note":"画面思路一句话",%s"html":"<template>…整段场景文件…</template>","elements":[{"id":"元素DOM id","name":"人话名（与 data-hf-name 一致）"}]}
`, int(cv.W), int(cv.H), pipeline.SceneContractCore("段总时长秒", "段id"), styleShared, goldenBlock, rules,
		seg.ID, seg.Key, paraLine, len(v.Words), v.DurationS, seg.Narration, v.DurationS+contract.SegTail, contract.SegTail, briefLabel, seg.VisualBrief,
		styleSeg, sceneTpl, compMethods, wb.String(), instrBlock, askBlock, feedbackBlock(feedback, prevHTML), reqField)
}

// prevSceneHTML 上一版场景文件（修复轮回喂底稿——模型看得见旧稿才谈得上定点
// 修改）。超长保头尾截中段（保护 prompt 预算，截断处注明）。读不到（首稿）空串。
func prevSceneHTML(p *pipeline.Project, segID string) string {
	b, err := os.ReadFile(p.Artifact("compositions/frames/" + segID + ".html"))
	if err != nil {
		return ""
	}
	s := string(b)
	const half = 24000
	if len(s) > half*2 {
		s = s[:half] + "\n<!-- …（中段过长截断；尾部结构如下） … -->\n" + s[len(s)-half:]
	}
	return s
}
