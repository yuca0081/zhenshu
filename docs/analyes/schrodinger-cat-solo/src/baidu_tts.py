# -*- coding: utf-8 -*-
"""百度翻译公开 TTS：按句切块合成，ffmpeg 量时长 -> segments.json
句块即字幕卡点来源。16kHz 单声道 mp3。
"""
import json, os, re, subprocess, time, urllib.request, urllib.parse

from script import SEGMENTS

BUILD = os.path.join(os.path.dirname(__file__), "..", "build")
SPD = 5
UA = {"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64)",
      "Referer": "https://fanyi.baidu.com/"}


def chunk_text(text):
    """按 。？！；切句（保留标点），过长再按 ，切。"""
    parts = re.findall(r"[^。？！；]*[。？！；]?", text)
    parts = [p for p in parts if p.strip()]
    out = []
    for p in parts:
        if len(p) > 40:
            subs = re.findall(r"[^，]*，?", p)
            acc = ""
            for s in subs:
                if s.strip():
                    acc += s
                    if len(acc) >= 18:
                        out.append(acc)
                        acc = ""
            if acc.strip():
                out.append(acc)
        else:
            out.append(p)
    return out


def fetch(text):
    url = ("https://fanyi.baidu.com/gettts?lan=zh&text="
           + urllib.parse.quote(text) + f"&spd={SPD}&source=web")
    for attempt in range(4):
        try:
            req = urllib.request.Request(url, headers=UA)
            with urllib.request.urlopen(req, timeout=25) as r:
                data = r.read()
            if len(data) > 2000:  # 正常一句至少几十KB
                return data
        except Exception as e:
            print("  retry", attempt, e)
        time.sleep(1.2 + attempt)
    raise RuntimeError("TTS failed: " + text[:20])


def dur(path):
    out = subprocess.run(
        ["ffprobe", "-v", "error", "-show_entries", "format=duration",
         "-of", "csv=p=0", path], capture_output=True, text=True)
    return float(out.stdout.strip())


def main():
    os.makedirs(BUILD, exist_ok=True)
    doc = []
    for seg in SEGMENTS:
        chunks = chunk_text(seg["text"])
        recs = []
        for i, ch in enumerate(chunks):
            mp3 = os.path.join(BUILD, f"{seg['id']}_{i:02d}.mp3")
            if not (os.path.exists(mp3) and os.path.getsize(mp3) > 2000):
                data = fetch(ch)
                with open(mp3, "wb") as f:
                    f.write(data)
                time.sleep(0.35)
            recs.append(dict(text=ch, mp3=os.path.basename(mp3), dur=dur(mp3)))
            print(f"{seg['id']}_{i:02d} {recs[-1]['dur']:.2f}s {ch[:18]}", flush=True)
        doc.append(dict(id=seg["id"], chunks=recs))
    with open(os.path.join(BUILD, "segments.json"), "w", encoding="utf-8") as f:
        json.dump(doc, f, ensure_ascii=False, indent=1)
    voice = sum(c["dur"] for s in doc for c in s["chunks"])
    print(f"语音净长 {voice:.1f}s，共 {sum(len(s['chunks']) for s in doc)} 句")


if __name__ == "__main__":
    main()
