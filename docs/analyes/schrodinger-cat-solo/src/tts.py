# -*- coding: utf-8 -*-
"""逐段 TTS：edge-tts 合成 mp3 + 抓 WordBoundary 字级时间戳。"""
import asyncio, json, os, sys
import edge_tts
from script import SEGMENTS

VOICE = "zh-CN-YunxiNeural"
RATE = "-4%"
BUILD = os.path.join(os.path.dirname(__file__), "..", "build")

async def synth(seg):
    mp3 = os.path.join(BUILD, f"{seg['id']}.mp3")
    js = os.path.join(BUILD, f"{seg['id']}.words.json")
    c = edge_tts.Communicate(seg["text"], VOICE, rate=RATE)
    words = []
    with open(mp3, "wb") as f:
        async for chunk in c.stream():
            if chunk["type"] == "audio":
                f.write(chunk["data"])
            elif chunk["type"] == "WordBoundary":
                words.append(dict(t=chunk["offset"] / 1e7, d=chunk["duration"] / 1e7,
                                  w=chunk["text"]))
    with open(js, "w", encoding="utf-8") as f:
        json.dump(words, f, ensure_ascii=False)
    return seg["id"], len(words)

async def main():
    os.makedirs(BUILD, exist_ok=True)
    for seg in SEGMENTS:
        sid, n = await synth(seg)
        print(f"{sid}: {n} 词边界", flush=True)

if __name__ == "__main__":
    asyncio.run(main())
