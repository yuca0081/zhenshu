# -*- coding: utf-8 -*-
"""自述片进度监控：打印各阶段最近事件 + 当前状态。"""
import json, os, sys, time

PID = "p20260925-155753"
BASE = os.path.join("data", "projects", PID)
MANI = os.path.join(BASE, "manifest.jsonl")

def main():
    if not os.path.exists(MANI):
        print("manifest 尚未生成")
        return
    evs = [json.loads(l) for l in open(MANI, encoding="utf-8")]
    counts = {}
    for e in evs:
        counts[e["event"]] = counts.get(e["event"], 0) + 1
    interesting = ["produce.tts", "llm.usage", "spec.sanitized", "composition.saved",
                   "check.repair", "check.passed", "render.seg", "render.seg.cached",
                   "render.done", "film.ready", "produce.done", "produce.error",
                   "frameqa.start", "frameqa.pass", "frameqa.failed", "frameqa.seg",
                   "stage.error", "produce.cancelled"]
    print("== 事件计数 ==")
    for k in interesting:
        if k in counts:
            print(f"  {k}: {counts[k]}")
    print("== 最近 12 条 ==")
    for e in evs[-12:]:
        print(f"  {e['ts'][11:19]} {e['event']}: {e['detail'][:70]}")
    # 音频进度
    audio = os.path.join(BASE, "audio")
    wavs = len([f for f in os.listdir(audio) if f.endswith(".wav")]) if os.path.isdir(audio) else 0
    segs = len([f for f in os.listdir(os.path.join(BASE, "renders", "segs")) if f.endswith(".mp4")]) \
        if os.path.isdir(os.path.join(BASE, "renders", "segs")) else 0
    print(f"== 音频 {wavs} 段 · 段片 {segs} 段 ==")

if __name__ == "__main__":
    main()
