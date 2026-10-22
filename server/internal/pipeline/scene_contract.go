package pipeline

import "fmt"

// SceneContractCore 场景契约的核心两条——第 6 条确定性禁令与第 7 条 canvas 接线。
// 正式段（produce/scenegen sceneUserPrompt）与迷你真段（styleclips clipUserPrompt）
// 共用同一份：渲染器/lint 只认这一套写法，两边各抄一份迟早漂移，改这里两边一起变。
// durLabel/idLabel 只是占位符的人话名（正式段「段总时长秒/段id」、小样「小样总时长秒/
// 小样id」），其余文本两侧逐字一致。
func SceneContractCore(durLabel, idLabel string) string {
	return fmt.Sprintf(`6. 确定性：禁 Date.now/new Date/setTimeout/setInterval/requestAnimationFrame/performance.now；
   禁 Math.random（随机一律种子化，如 __rng32(seed) 闭包）；禁 CSS animation/transition/@keyframes；
   禁网络（fetch/XHR/WebSocket/外链 http(s)）；禁 Worker；禁 <video>/<audio>
7. canvas 画布：绘制函数 = window["__draw_<id>"]=function(t,ctx,W,H,rng)——t 的纯函数，
   每帧从零重画；运动一律写成 t 的函数。接线三行逐字照抄（只换尖括号占位），漏注册=lint 拒收 [F3]：
   var tl=gsap.timeline({paused:true});
   tl.to({},{duration:<%s>,ease:"none",onUpdate:function(){window["__draw_<id>"](tl.time(),ctx,W,H,rng);}},0);
   window.__timelines["<%s>"]=tl;
`, durLabel, idLabel)
}
