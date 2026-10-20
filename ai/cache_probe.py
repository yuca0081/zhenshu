# -*- coding: utf-8 -*-
"""cache_probe.py —— GLM 前缀缓存行为探针（2026-10-21）。
同一 key 同一模型，每组配置把"同前缀请求"连发两遍，第二遍读
usage.prompt_tokens_details.cached_tokens：非 0 = 前缀(含图)被缓存。
组矩阵：纯文本/图在前/文在前图在后 × 开思考/关思考。
"""
import base64
import io
import json
import sys
import time
import urllib.request

from PIL import Image

BASE_DEFAULT = "https://open.bigmodel.cn/api/coding/paas/v4"
MODEL_DEFAULT = "glm-5.3-flash"


def load_env(path=".env"):
    env = {}
    for line in open(path, encoding="utf-8"):
        line = line.strip()
        if line and not line.startswith("#") and "=" in line:
            k, v = line.split("=", 1)
            env[k.strip()] = v.strip()
    return env


def shrink_jpeg(path, width=640, quality=75):
    """PNG → 640px JPEG dataURL（复刻 shrinkSampleImage 的真实载荷形态）。"""
    im = Image.open(path).convert("RGB")
    w, h = im.size
    tw = min(width, w)
    th = h * tw // w
    im = im.resize((tw, th))
    buf = io.BytesIO()
    im.save(buf, "JPEG", quality=quality)
    return "data:image/jpeg;base64," + base64.b64encode(buf.getvalue()).decode()


LONG_TEXT = (
    "你是视频画面工程师，以下是场景文件契约，每一条都必须遵守。\n"
    + (
        "规则条目：所有可见元素必须带 data-hf-name 注册；gsap.timeline 必须 paused 且末尾注册 "
        "window.__timelines；禁止 Date.now/new Date/setTimeout/performance.now；禁止 Math.random，"
        "随机一律用种子闭包 __rng32；禁止 CSS animation/transition/@keyframes；禁止 fetch/XHR/WebSocket "
        "与任何外链资源；canvas 绘制函数必须是时间 t 的纯函数，每帧从零重画；字体只准用渲染端已安装清单，"
        "引用即声明 @font-face；底部 1594px 起是平台字幕带，禁放内容；每屏至少一个可辨认的视觉重心。\n"
    )
    * 24
)  # ~4.5k 字 ≈ 3k+ tokens，稳过最小可缓存长度

Q = "\n\n读完只需回答两个字：收到"


def call(url, key, model, msgs, think):
    body = {"model": model, "messages": msgs}
    if think in ("enabled", "disabled"):
        body["thinking"] = {"type": think}
    req = urllib.request.Request(
        url,
        data=json.dumps(body).encode("utf-8"),
        headers={"Content-Type": "application/json", "Authorization": "Bearer " + key},
    )
    t0 = time.time()
    with urllib.request.urlopen(req, timeout=300) as r:
        d = json.loads(r.read())
    u = d.get("usage", {})
    det = u.get("prompt_tokens_details") or {}
    return {
        "prompt": u.get("prompt_tokens"),
        "cached": det.get("cached_tokens"),
        "secs": round(time.time() - t0, 1),
    }


def pair(url, key, model, msgs, think, label):
    r1 = call(url, key, model, msgs, think)
    r2 = call(url, key, model, msgs, think)
    hit = "命中" if (r2["cached"] or 0) > 0 else "未命中"
    print(f"{label:<28} 思考={think:<8} 第1遍 prompt={r1['prompt']} cached={r1['cached']} {r1['secs']}s")
    print(f"{'':<28} {'':<10} 第2遍 prompt={r2['prompt']} cached={r2['cached']} {r2['secs']}s  → {hit}")
    return r2["cached"]


def main():
    env = load_env()
    key = env["LLM_API_KEY"]
    url = (env.get("LLM_BASE_URL") or BASE_DEFAULT).rstrip("/") + "/chat/completions"
    model = env.get("LLM_MODEL") or MODEL_DEFAULT
    img = shrink_jpeg(sys.argv[1] if len(sys.argv) > 1 else
                      "data/projects/p20261020-202642/style/sample-0.png")
    print(f"端点={url}  模型={model}  长文本≈{len(LONG_TEXT)}字  图={img[:40]}...({len(img)//1024}KB)\n")

    sysmsg = {"role": "system", "content": "你是视频画面工程的质量检查员。"}
    text_tail = {"type": "text", "text": LONG_TEXT + Q}
    img_part = {"type": "image_url", "image_url": {"url": img}}

    text_only = [
        {"role": "system", "content": sysmsg["content"]},
        {"role": "user", "content": LONG_TEXT + Q},
    ]
    img_first = [sysmsg, {"role": "user", "content": [img_part, text_tail]}]
    text_first = [sysmsg, {"role": "user", "content": [text_tail, img_part]}]

    pair(url, key, model, text_only, "enabled", "A 纯文本")
    pair(url, key, model, text_only, "disabled", "B 纯文本")
    pair(url, key, model, img_first, "disabled", "C 图在前+文后（写画面现状）")
    pair(url, key, model, img_first, "enabled", "D 图在前+文后（ZCode形状）")
    pair(url, key, model, text_first, "disabled", "E 文在前+图后（改造形状）")


if __name__ == "__main__":
    main()
