package contract

// 跨端契约常量：Go / Python / TypeScript 三端各自持有一份同值实现，改这里必须同步改：
//   - ai/assemble.py            scene = duration_s + 0.35（MP4 段长）
//   - web/src/segtime.ts        SEG_TAIL = 0.35（时间轴段表）
//
// 漂移由 constants_drift_test.go 门禁兜住。
const (
	// SegTail 每段配音后的尾垫秒数：段长 = duration_s + SegTail，MP4 拼接与前端时间轴同口径。
	SegTail = 0.35
	// FPS 成片帧率（hyperframes 渲染默认），引用卡片「第 N 帧」换算用。
	FPS = 30.0
)
