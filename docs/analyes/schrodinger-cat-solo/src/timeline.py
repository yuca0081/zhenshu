# -*- coding: utf-8 -*-
"""由 segments.json 构建全片时间轴 timeline.json：
场景边界（lead+语音+tail）、字幕卡点（句级）、音效事件。"""
import json, os
from script import SEGMENTS

BUILD = os.path.join(os.path.dirname(__file__), "..", "build")
W, H, FPS = 1920, 1080, 30


def main():
    segs = json.load(open(os.path.join(BUILD, "segments.json"), encoding="utf-8"))
    by_id = {s["id"]: s for s in segs}

    scenes, captions, cur = [], [], 0.0
    for seg in SEGMENTS:
        rec = by_id[seg["id"]]
        voice_dur = sum(c["dur"] for c in rec["chunks"])
        dur = seg["lead"] + voice_dur + seg["tail"]
        # 句内局部时刻（相对场景起点）
        chunks = []
        t = seg["lead"]
        for c in rec["chunks"]:
            t1 = t + c["dur"]
            chunks.append(dict(t0=t, t1=t1, text=c["text"]))
            captions.append(dict(t0=cur + t + 0.05, t1=cur + t1 - 0.12,
                                 scene=seg["id"], text=c["text"]))
            t = t1
        scenes.append(dict(id=seg["id"], kicker=seg["kicker"], start=cur,
                           dur=dur, lead=seg["lead"], voice_dur=voice_dur,
                           chunks=chunks))
        cur += dur

    def ev_scene(sid, offset=0.0):
        sc = next(s for s in scenes if s["id"] == sid)
        return round(sc["start"] + offset, 3)

    def chunk_off(sid, idx, offset=0.0):
        sc = next(s for s in scenes if s["id"] == sid)
        return round(sc["start"] + sc["chunks"][idx]["t0"] + offset, 3)

    events = dict(
        bell_title=ev_scene("s1", 0.6),
        geiger_start=chunk_off("s3", 2, 0.4),      # “规则很残忍”起
        geiger_end=ev_scene("s5", 1.0),
        boom=chunk_off("s5", 2, 1.05),             # “打开箱子看一眼”→坍缩瞬间
        split_arp=chunk_off("s6", 4, 1.6),         # “宇宙都在分裂”
        fade_out=round(cur - 2.5, 3),              # 全片末尾 2.5s 淡出
    )
    bells = [round(s["start"] + 0.15, 3) for s in scenes[1:]]
    whooshes = [round(s["start"] - 0.45, 3) for s in scenes[1:]]

    tl = dict(W=W, H=H, FPS=FPS, total=round(cur, 3), scenes=scenes,
              captions=captions, events=events, bells=bells, whooshes=whooshes)
    with open(os.path.join(BUILD, "timeline.json"), "w", encoding="utf-8") as f:
        json.dump(tl, f, ensure_ascii=False, indent=1)
    print(f"总时长 {cur:.1f}s = {int(cur//60)}分{cur%60:04.1f}秒")
    for s in scenes:
        print(f"  {s['id']} {s['kicker']}: {s['start']:7.2f} +{s['dur']:5.1f}s")


if __name__ == "__main__":
    main()
