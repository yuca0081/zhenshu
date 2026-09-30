# -*- coding: utf-8 -*-
"""一次性修复器：把 body 追加中误用的 eid 变量替换为对应的 html 变量。"""
import pathlib
import re

p = pathlib.Path(__file__).resolve().parent / 'build_agi.py'
src = p.read_text(encoding='utf-8')

pairs = re.findall(r'(\w+), (\w+) = (?:el|caret|smiley|canvas_el)\(', src)
eid2html = {eid: hv for hv, eid in pairs}

BS = chr(92)  # 反斜杠
MID = "body += '" + BS + 'n    ' + "' + "
PRE = "body = "
N = 0

for eid, hv in eid2html.items():
    pats = (
        (MID + eid + '\n', MID + hv + '\n'),
        (MID + eid + ' + ', MID + hv + ' + '),
        (PRE + eid + " + '" + BS + 'n    ' + "' + body", PRE + hv + " + '" + BS + 'n    ' + "' + body"),
        ('body += ' + eid + " + '" + BS + 'n    ' + "'", 'body += ' + hv + " + '" + BS + 'n    ' + "'"),
        ('body += ' + eid + '\n', 'body += ' + hv + '\n'),
    )
    for a, b in pats:
        c = src.count(a)
        if c:
            src = src.replace(a, b)
            N += c

p.write_text(src, encoding='utf-8')
print(f'修正 {N} 处（eid 变量 {len(eid2html)} 个）')
