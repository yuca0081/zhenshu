# -*- coding: utf-8 -*-
"""font_tool:目录字体的下载缓存 + 按本项目文稿子集化(vend 同款铁律:模型只有申请权,
执行永远由平台代劳;全程留痕台账)。

用法:
  python ai/font_tool.py ensure <project_dir>

流程:
  1. 读 style/font_assets.request.json(Go LockDesign 写:设计点名的目录字体清单)
  2. 逐款:全字库缓存 data/library/vendor/fonts/(缺则下载,sha256 入台账) →
     按本项目用字子集化成 woff2 → base64 data URL
  3. 写 style/font_assets.json:{"resolved": {"<Family>": "<data_url>"}},消费方
     (assemble 字幕 / fixupFrameFrames 场景)按 family 名注入 @font-face
  4. 某款失败(下载/子集/超体积):该家族从设计三件套(skin/style_samples/design.md)
     的字体栈里剥除并记入 "stripped"——降级回系统字体,绝不带病进渲染
"""
import base64
import io
import json
import pathlib
import re
import sys
import time
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parent.parent
FONT_CACHE = ROOT / 'data' / 'library' / 'vendor' / 'fonts'
MANIFEST = FONT_CACHE / 'manifest.json'
DOWNLOAD_CAP = 20 * 1024 * 1024   # 单文件硬上限(与 vendor_tool 同款)
SUBSET_CAP = 6 * 1024 * 1024      # 子集产物上限:超了视为异常(全字库漏子集),拒用

# 子集字符的兜底集:ASCII 可打印 + 常用中文标点 + 阿拉伯数字变体。
# 项目文稿/分镜里的字永远以实际文件为准,这里只保证标点数字不缺。
BASE_CHARS = (
    ''.join(chr(c) for c in range(0x20, 0x7F))
    + '，。！？；：、""''（）《》〈〉【】…—·「」『』～￥％×÷'
    + '一二三四五六七八九十百千万亿'
)


def _http_get(url: str, headers: dict = None) -> bytes:
    req = urllib.request.Request(url, headers=headers or {'User-Agent': 'zhenshu-font-tool'})
    with urllib.request.urlopen(req, timeout=120) as r:
        data = r.read()
    if len(data) > DOWNLOAD_CAP:
        raise ValueError('文件 %d 字节超下载上限' % len(data))
    return data


def _load_manifest() -> dict:
    if MANIFEST.exists():
        return json.loads(MANIFEST.read_text(encoding='utf-8'))
    return {'fonts': {}}


def _cached_ttf(entry: dict) -> pathlib.Path:
    """全字库缓存命中返回文件路径;未命中下载并验收入册。"""
    FONT_CACHE.mkdir(parents=True, exist_ok=True)
    name = '%s-%s.ttf' % (entry['id'], entry['version'])
    path = FONT_CACHE / name
    manifest = _load_manifest()
    key = '%s@%s' % (entry['id'], entry['version'])
    if path.exists() and key in manifest['fonts']:
        return path
    raw = _http_get(entry['url'], headers={
        'Accept': 'application/vnd.github.raw',
        'User-Agent': 'zhenshu-font-tool'})
    if not raw.startswith(b'\x00\x01\x00\x00') and not raw.startswith(b'OTTO'):
        raise ValueError('下载产物不是字体文件(magic 不符)')
    import hashlib
    manifest['fonts'][key] = {
        'sha256': hashlib.sha256(raw).hexdigest(),
        'size': len(raw),
        'license': entry.get('license', ''),
        'url': entry['url'],
        'downloaded_at': time.strftime('%Y-%m-%dT%H:%M:%S'),
    }
    MANIFEST.write_text(json.dumps(manifest, ensure_ascii=False, indent=1), encoding='utf-8')
    path.write_bytes(raw)
    return path


def _project_text(proj: pathlib.Path) -> str:
    """子集字符集 = 文稿 + 分镜 + 设计文档里实际出现的字 + 兜底集。"""
    chars = set(BASE_CHARS)
    for rel in ('input/article.txt', 'storyboards/storyboard.json', 'style/design.md'):
        p = proj / rel
        if p.exists():
            try:
                chars.update(p.read_text(encoding='utf-8', errors='ignore'))
            except Exception:
                pass
    return ''.join(chars)


def _subset_woff2(ttf_path: pathlib.Path, text: str) -> bytes:
    from fontTools import subset
    opts = subset.Options()
    opts.flavor = 'woff2'
    opts.ignore_missing_glyphs = True
    opts.drop_tables += ['FFTM']
    font = subset.load_font(str(ttf_path), opts)
    subsetter = subset.Subsetter(opts)
    subsetter.populate(text=text)
    subsetter.subset(font)
    buf = io.BytesIO()
    subset.save_font(font, buf, opts)
    out = buf.getvalue()
    if len(out) > SUBSET_CAP:
        raise ValueError('子集产物 %d 字节超上限(子集没生效?)' % len(out))
    return out


def _strip_family_from_stacks(proj: pathlib.Path, family: str):
    """把失败家族从设计三件套的字体栈里剥除(与 Go 侧 clampFontStack 同语义)。"""
    pat = re.compile(r"'%s'\s*,\s*|,\s*'%s'|'%s'" % (re.escape(family),) * 3, re.IGNORECASE)
    for rel in ('style/skin.json', 'style/style_samples.json', 'style/design.md'):
        p = proj / rel
        if not p.exists():
            continue
        try:
            s = p.read_text(encoding='utf-8')
            s2 = pat.sub('', s)
            if s2 != s:
                p.write_text(s2, encoding='utf-8')
        except Exception:
            pass


def ensure(proj_dir: str) -> int:
    proj = pathlib.Path(proj_dir)
    req_path = proj / 'style' / 'font_assets.request.json'
    if not req_path.exists():
        return 0
    req = json.loads(req_path.read_text(encoding='utf-8'))
    resolved, stripped = {}, []
    for entry in req.get('fonts', []):
        family = entry['family']
        try:
            ttf = _cached_ttf(entry)
            woff2 = _subset_woff2(ttf, _project_text(proj))
            resolved[family] = 'data:font/woff2;base64,' + base64.b64encode(woff2).decode()
            print('font_tool: %s 子集 %d 字节 ✓' % (family, len(woff2)))
        except Exception as e:
            stripped.append(family)
            _strip_family_from_stacks(proj, family)
            print('font_tool: %s 准备失败,已从设计剥除:%s' % (family, e))
    out = proj / 'style' / 'font_assets.json'
    out.write_text(json.dumps({'resolved': resolved, 'stripped': stripped},
                              ensure_ascii=False), encoding='utf-8')
    return len(resolved)


if __name__ == '__main__':
    # 两种调用都认：font_tool.py ensure <dir>（显式）与 font_tool.py <dir>
    # （produce 的 r.python 约定是「脚本 项目目录」无子命令）
    args = [a for a in sys.argv[1:] if a != 'ensure']
    if len(args) < 1:
        print(__doc__)
        sys.exit(2)
    sys.exit(0 if ensure(args[0]) >= 0 else 1)
