# -*- coding: utf-8 -*-
"""帧组装与渲染调度。
用法:
  python render.py preview   # 每场景 6 帧样张 -> build/preview/
  python render.py full      # 多进程逐帧 -> ffmpeg 段 -> concat
"""
import json, os, subprocess, sys
import numpy as np
from PIL import Image

from style import W, H, get_base, get_grains, draw_captions, seg
from scenes import SCENES

BUILD = os.path.join(os.path.dirname(__file__), "..", "build")
XFADE = 0.45
TL = None

def tl():
    global TL
    if TL is None:
        TL = json.load(open(os.path.join(BUILD, "timeline.json"), encoding="utf-8"))
    return TL


def scene_index(t):
    scs = tl()["scenes"]
    for i in range(len(scs) - 1, -1, -1):
        if t >= scs[i]["start"] - 1e-9:
            return i
    return 0


def render_scene(i, tloc):
    sc = tl()["scenes"][i]
    im = get_base().convert("RGBA")
    if 0 <= tloc <= sc["dur"] + XFADE:
        SCENES[sc["id"]](im, tloc, sc["dur"], sc)
    # kicker（s9 片尾字幕前淡出）
    from style import draw_kicker
    p = seg(tloc, 0.3, 1.0)
    if sc["id"] == "s9":
        p *= 1 - seg(tloc, sc["dur"] - 4.2, sc["dur"] - 3.2)
    draw_kicker(im, sc["kicker"], p)
    return im


def compose_frame(t):
    data = tl()
    i = scene_index(t)
    sc = data["scenes"][i]
    tloc = t - sc["start"]
    im = render_scene(i, tloc)
    if tloc < XFADE and i > 0:
        prev = data["scenes"][i - 1]
        prev_im = render_scene(i - 1, tloc + prev["dur"])
        im = Image.blend(prev_im, im, tloc / XFADE)
    caps = [c["text"] for c in data["captions"] if c["t0"] <= t <= c["t1"]]
    draw_captions(im, caps)
    arr = np.asarray(im.convert("RGB"), dtype=np.int16)
    fi = int(round(t * data["FPS"]))
    arr = np.clip(arr + get_grains()[fi % 4], 0, 255).astype(np.uint8)
    # 全局淡入淡出
    fade_in = seg(t, 0.0, 0.7)
    fade_out = 1.0 - seg(t, data["events"]["fade_out"], data["total"])
    g = min(fade_in, fade_out)
    if g < 1:
        arr = (arr.astype(np.float32) * g).astype(np.uint8)
    return arr.tobytes()


def preview():
    os.makedirs(os.path.join(BUILD, "preview"), exist_ok=True)
    from PIL import ImageDraw
    for sc in tl()["scenes"]:
        st, du = sc["start"], sc["dur"]
        for k in range(6):
            t = st + (du - 0.15) * (k + 0.5) / 6
            raw = compose_frame(t)
            im = Image.frombytes("RGB", (W, H), raw)
            im.save(os.path.join(BUILD, "preview", f"{sc['id']}_{k}.png"))
        print(sc["id"], "done", flush=True)
    # 拼联页
    files = sorted(os.listdir(os.path.join(BUILD, "preview")))
    files = [f for f in files if f.endswith(".png")]
    for page in range(0, len(files), 6):
        batch = files[page:page + 6]
        sheet = Image.new("RGB", (960 * 3, 540 * 2), (0, 0, 0))
        for j, f in enumerate(batch):
            im = Image.open(os.path.join(BUILD, "preview", f)).resize((960, 540))
            sheet.paste(im, ((j % 3) * 960, (j // 3) * 540))
            d = ImageDraw.Draw(sheet)
            d.text((12 + (j % 3) * 960, 8 + (j // 3) * 540), f, fill=(255, 120, 0))
        sheet.save(os.path.join(BUILD, "preview", f"sheet_{page//6:02d}.png"))
    print("sheets ready")


def _worker(args):
    wid, f0, f1 = args
    data = tl()
    fps = data["FPS"]
    seg_path = os.path.join(BUILD, f"seg_{wid:03d}.mp4")
    cmd = ["ffmpeg", "-y", "-loglevel", "error", "-f", "rawvideo",
           "-pix_fmt", "rgb24", "-s", f"{W}x{H}", "-r", str(fps), "-i", "-",
           "-c:v", "libx264", "-preset", "medium", "-crf", "19",
           "-pix_fmt", "yuv420p", seg_path]
    p = subprocess.Popen(cmd, stdin=subprocess.PIPE)
    for f in range(f0, f1):
        p.stdin.write(compose_frame(f / fps))
    p.stdin.close()
    p.wait()
    return wid


def full():
    from concurrent.futures import ProcessPoolExecutor
    data = tl()
    total_f = int(round(data["total"] * data["FPS"]))
    NW = 12
    step = (total_f + NW - 1) // NW
    jobs = [(k, k * step, min((k + 1) * step, total_f))
            for k in range(NW) if k * step < total_f]
    for f in os.listdir(BUILD):
        if f.startswith("seg_") and f.endswith(".mp4"):
            os.remove(os.path.join(BUILD, f))
    with ProcessPoolExecutor(max_workers=NW) as ex:
        for wid in ex.map(_worker, jobs):
            print(f"seg {wid} done", flush=True)
    lst = os.path.join(BUILD, "concat.txt")
    with open(lst, "w") as f:
        for j in sorted(jobs):
            f.write(f"file 'seg_{j[0]:03d}.mp4'\n")
    out = os.path.join(BUILD, "video_silent.mp4")
    subprocess.run(["ffmpeg", "-y", "-loglevel", "error", "-f", "concat",
                    "-safe", "0", "-i", lst, "-c", "copy", out], check=True)
    dur = subprocess.run(["ffprobe", "-v", "error", "-show_entries", "format=duration",
                          "-of", "csv=p=0", out], capture_output=True, text=True).stdout
    print("video_silent.mp4", dur.strip())


if __name__ == "__main__":
    mode = sys.argv[1] if len(sys.argv) > 1 else "preview"
    if mode == "preview":
        preview()
    else:
        full()
