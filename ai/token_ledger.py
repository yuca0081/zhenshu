# -*- coding: utf-8 -*-
"""实测片对账：从 manifest.jsonl 汇总 token 账/墙钟/审核拦截。用法:
python token_ledger.py data/projects/<id>/manifest.jsonl [--baseline 其他manifest]
"""
import json
import sys
from collections import defaultdict

def load(path):
    lines = []
    with open(path, encoding="utf-8") as f:
        for ln in f:
            ln = ln.strip()
            if ln:
                try:
                    lines.append(json.loads(ln))
                except json.JSONDecodeError:
                    pass
    return lines

def group(task):
    """seg.write.seg03 → seg.write；review.final → review.final"""
    parts = task.split(".")
    if parts[0] == "seg" and len(parts) >= 2:
        return ".".join(parts[:2])
    if parts[0] == "review" and len(parts) >= 3 and parts[1] in ("seg", "bnd"):
        return ".".join(parts[:2])
    return task

def report(path, title=""):
    lines = load(path)
    agg = defaultdict(lambda: [0, 0, 0, 0])  # prompt, compl, cached, calls
    for e in lines:
        if e.get("event") == "agent.done":
            g = group(e.get("task", "?"))
            a = agg[g]
            a[0] += e.get("prompt_tokens", 0)
            a[1] += e.get("completion_tokens", 0)
            a[2] += e.get("cached_tokens", 0)
            a[3] += e.get("calls", 0)
    tot_p = sum(v[0] for v in agg.values())
    tot_c = sum(v[1] for v in agg.values())
    tot_ca = sum(v[2] for v in agg.values())
    tot_calls = sum(v[3] for v in agg.values())

    print(f"== {title or path} ==")
    print(f"{'工位':<22}{'prompt':>9}{'completion':>11}{'cached':>9}{'calls':>7}{'合计':>9}")
    for g in sorted(agg, key=lambda k: -(agg[k][0] + agg[k][1])):
        p, c, ca, n = agg[g]
        print(f"{g:<22}{p:>9}{c:>11}{ca:>9}{n:>7}{p+c:>9}")
    print(f"{'TOTAL':<22}{tot_p:>9}{tot_c:>11}{tot_ca:>9}{tot_calls:>7}{tot_p+tot_c:>9}")
    print(f"cached 占 prompt: {tot_ca/tot_p*100:.1f}%" if tot_p else "")

    # 墙钟锚点
    marks = {}
    for e in lines:
        ev, ms = e.get("event"), e.get("ms")
        if ev in ("project.created", "prep.start", "prep.confirmed", "produce.start", "produce.done", "produce.finished", "video.ready") and ms:
            marks.setdefault(ev, ms)
    if marks:
        for ev, ms in sorted(marks.items(), key=lambda kv: kv[1]):
            print(f"  {ev}: {ms}")
        first = min(marks.values())
        last = max(marks.values())
        print(f"  首尾锚点跨度: {(last-first)/1000:.0f}s")

    # 审核拦截/重写账
    cnt = defaultdict(int)
    for e in lines:
        cnt[e.get("event")] += 1
    interesting = ["review.rework", "review.seg_error", "review.final_rework", "seg.seg.audited",
                   "seg.bnd.audited", "seg.seg.rewriting", "check.repair", "produce.retry",
                   "produce.error", "segpipe.error", "render.seg", "render.seg.cached",
                   "patch.fallback", "patch.applied", "scene.patch", "scene.cont"]
    hits = {k: cnt[k] for k in interesting if cnt[k]}
    if hits:
        print("  事件计数:", json.dumps(hits, ensure_ascii=False))
    print()

if __name__ == "__main__":
    args = [a for a in sys.argv[1:] if not a.startswith("--")]
    report(args[0])
    if len(args) > 1:
        report(args[1], "基线(对照)")
