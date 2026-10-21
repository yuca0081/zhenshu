# -*- coding: utf-8 -*-
"""cache_probe2.py —— 前缀缓存「同头不同尾」探针（2026-10-21 深夜诊断）。

cache_probe.py 测的是「同一请求连发两遍」——生产里写画面真正的复用形态是
「共享头 + 每次不同的段信息结尾」。本探针补这个对照组：
  X1 纯文本头，同头不同尾（对照：预期命中）
  X2 图在前+同头不同尾（写画面现状，2026-10-21 实测全零）
  X3 文在前图在后+同头不同尾（候选改法）
  X4 图在前，同一请求连发两遍（复测昨天的结论）
每组第二发前等 45s（给传播留时间；生产里调用间隔是分钟级）。
"""
import base64
import io
import json
import sys
import time
import urllib.request

sys.path.insert(0, __import__("os").path.dirname(__file__))
from cache_probe import load_env, shrink_jpeg, LONG_TEXT, BASE_DEFAULT, MODEL_DEFAULT  # noqa: E402


def call(url, key, model, msgs):
    body = {"model": model, "messages": msgs, "thinking": {"type": "disabled"}}
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
    return {"prompt": u.get("prompt_tokens"), "cached": det.get("cached_tokens"),
            "secs": round(time.time() - t0, 1)}


def diff_tail(i):
    """同头不同尾：尾部 = 段信息（每段不同——生产写画面形态）。"""
    return f"\n\n## 段信息（seg{i:02d}）\n- 标题：第{i}段的独有信息， Snowflake-{i}\n" + (
        "本段画面提示：冰晶在暗色湖面上生长，冷蓝辉光，粒子上浮。词序表：0:冰@0.5s 1:晶@1.2s。\n"
    ) * 6 + "\n读完只需回答两个字：收到"


def main():
    env = load_env()
    key = env["LLM_API_KEY"]
    url = (env.get("LLM_BASE_URL") or BASE_DEFAULT).rstrip("/") + "/chat/completions"
    model = env.get("LLM_MODEL") or MODEL_DEFAULT
    img_path = sys.argv[1] if len(sys.argv) > 1 else "data/projects/p20261021-012948/style/sample-0.png"
    img = shrink_jpeg(img_path)
    print(f"端点={url}  模型={model}  图={len(img)//1024}KB  长头≈{len(LONG_TEXT)}字\n")

    sysmsg = {"role": "system", "content": "你是视频画面工程师，直接写可渲染的场景 HTML。只输出 JSON。风格：冷晶仪象。"}
    head_text = LONG_TEXT  # 共享头文本（契约/规则，跨调用不变）
    img_part = {"type": "image_url", "image_url": {"url": img}}

    def build(order, tail):
        if order is None:  # 纯文本对照（无图）
            return [sysmsg, {"role": "user", "content": head_text + tail}]
        text_part = {"type": "text", "text": head_text + tail}
        user = [{"role": "user", "content": [img_part, text_part] if order == "img_first"
                 else [text_part, img_part]}]
        return [sysmsg] + user

    def duo(label, order, gap=45):
        a = call(url, key, model, build(order, diff_tail(1)))
        time.sleep(gap)  # 传播窗口（生产里调用间隔分钟级；这里取 45s）
        b = call(url, key, model, build(order, diff_tail(2)))
        hit = "命中" if (b["cached"] or 0) > 0 else "未命中"
        print(f"{label:<34} A: prompt={a['prompt']} cached={a['cached']} ({a['secs']}s)")
        print(f"{'':<34} B: prompt={b['prompt']} cached={b['cached']} ({b['secs']}s)  → {hit}")

    def repeat(label, order, gap=45):
        msgs = build(order, diff_tail(1))
        a = call(url, key, model, msgs)
        time.sleep(gap)
        b = call(url, key, model, msgs)
        hit = "命中" if (b["cached"] or 0) > 0 else "未命中"
        print(f"{label:<34} A: prompt={a['prompt']} cached={a['cached']} ({a['secs']}s)")
        print(f"{'':<34} B: prompt={b['prompt']} cached={b['cached']} ({b['secs']}s)  → {hit}")

    duo("X1 纯文本 同头不同尾", None)
    duo("X2 图在前 同头不同尾(现状)", "img_first")
    duo("X3 文在前图在后 同头不同尾", "text_first")
    repeat("X4 图在前 同一请求两遍", "img_first")


if __name__ == "__main__":
    main()
