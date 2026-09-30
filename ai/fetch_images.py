# -*- coding: utf-8 -*-
"""制作期图片获取（帧述，2026-09-23；2026-09-26 加 AI 生图源；2026-09-27 素材库化）。

扫 llm/comp-*.spec.json 里的 image 元素 → 取图 → **暂存 assets/img/.stage/**
（内容 hash 命名 + sidecar 打标）→ spec 回写 `stage:<hash>`。
入库（catalog.json 唯一索引）由 Go 侧 assetlib 完成（单一写者）：
  produce.fetchImages 在本脚本跑完后扫 .stage → Ingest（搜索词打标/确定性 meta）
  → spec 的 stage: 引用改写为素材 id `lib:<id>`。
渲染层 render_spec.py 按 `lib:<id>` 从素材库拷回项目 assets/img/（渲染本地性），
spec 文件本身永远只持 id（§5 引用统一）。
credits.txt 记录出处（自用 1.0 版权留档）。

图源链（按元素 source 字段分流，2026-09-26）：
  source=gen ：AI 生图（智谱 CogView）→ 必应 → Commons
  其他/空    ：必应 → Commons → AI 生图兜底
写实名词（人物/产品/地标/新闻实物）spec 里必须用 search——生图会编造事实。

环境变量：
  IMG_GEN       0 关闭生图源（默认开）
  IMG_GEN_HD    1 用无水印付费档（cogview-4，按量计费；失败自动回退免费档并警告。
                默认 0——免费档 cogview-3-flash 右下角带"AI生成"水印，P0-4 去水印开关）
  IMG_GEN_MODEL 生图模型（显式指定则单链，优先于 IMG_GEN_HD）
  IMG_GEN_KEY   生图 API key（缺省用 LLM_API_KEY——智谱同 key 体系，实测套餐 key 可调免费档）

失败兜底：src 置空 → render_spec 画便签占位，管线不死。

用法：python ai/fetch_images.py <项目目录>
"""
import hashlib
import json
import os
import pathlib
import re
import sys
import urllib.parse
import urllib.request

UA = {
    'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64)',
    'Referer': 'https://cn.bing.com/',
}
MAGIC = {
    b'\xff\xd8\xff': '.jpg',
    b'\x89PNG': '.png',
    b'GIF8': '.gif',
}
GEN_URL = 'https://open.bigmodel.cn/api/paas/v4/images/generations'
GEN_SIZES = {'16:9': '1344x768', '9:16': '768x1344'}


# ── 图源：搜图 ───────────────────────────────────────────────

def search_bing(query, n=10):
    """必应图片异步接口 → [(原图url, 来源页url)]。"""
    q = urllib.parse.quote(query)
    url = f'https://cn.bing.com/images/async?q={q}&first=1&count={n}&mmasync=1'
    try:
        req = urllib.request.Request(url, headers=UA)
        html = urllib.request.urlopen(req, timeout=15).read().decode('utf-8', 'ignore')
    except Exception as e:
        print(f'  bing 搜索失败: {e}')
        return []
    out = []
    for m in re.findall(r'm="({&quot;.*?})"', html)[:n]:
        try:
            d = json.loads(m.replace('&quot;', '"').replace('&#39;', "'").replace('&amp;', '&'))
            if d.get('murl', '').startswith('http'):
                out.append((d['murl'], d.get('purl', '')))
        except json.JSONDecodeError:
            continue
    return out


def search_commons(query, n=8):
    """Wikimedia Commons（海外可达时）。"""
    q = urllib.parse.quote(query)
    url = ('https://commons.wikimedia.org/w/api.php?action=query&format=json'
           f'&generator=search&gsrsearch=filetype:bitmap%20{q}&gsrlimit={n}'
           '&gsrnamespace=6&prop=imageinfo&iiprop=url|size|mime&iiurlwidth=1280')
    try:
        req = urllib.request.Request(url, headers={'User-Agent': 'zhenshu-dev/1.0'})
        data = json.load(urllib.request.urlopen(req, timeout=15))
    except Exception as e:
        print(f'  commons 搜索失败: {e}')
        return []
    out = []
    for p in ((data.get('query') or {}).get('pages') or {}).values():
        ii = (p.get('imageinfo') or [{}])[0]
        if ii.get('mime') in ('image/jpeg', 'image/png') and ii.get('width', 0) >= 480:
            out.append((ii.get('thumburl') or ii.get('url', ''), p.get('title', '')))
    return out


def download(url):
    """下载并校验图片字节。返回 bytes / None。"""
    try:
        r = urllib.request.urlopen(urllib.request.Request(url, headers=UA), timeout=20)
        b = r.read()
    except Exception:
        return None
    if len(b) < 30_000:            # 太小多为占位图/损坏
        return None
    if not any(b.startswith(m) for m in MAGIC):
        return None
    return b


# ── 图源：AI 生图（2026-09-26，智谱 CogView）────────────────

def gen_key():
    return os.environ.get('IMG_GEN_KEY') or os.environ.get('LLM_API_KEY') or ''


def gen_enabled():
    return os.environ.get('IMG_GEN', '1') != '0' and bool(gen_key())


def gen_models():
    """生图模型链：IMG_GEN_MODEL 显式指定 → 单链；IMG_GEN_HD=1 → 无水印付费档
    优先、免费档兜底（余额不足自动降级，出片不中断）；默认仅免费档。"""
    m = os.environ.get('IMG_GEN_MODEL')
    if m:
        return [m]
    if os.environ.get('IMG_GEN_HD', '0') == '1':
        return ['cogview-4', 'cogview-3-flash']
    return ['cogview-3-flash']


def gen_size(proj: pathlib.Path) -> str:
    """生图尺寸随项目画幅（16:9 横 / 9:16 竖）。"""
    try:
        meta = json.load(open(proj / 'project.json', encoding='utf-8'))
        return GEN_SIZES.get(meta.get('aspect') or '16:9', '1344x768')
    except Exception:
        return '1344x768'


def generate_image(prompt, size):
    """智谱生图（沿模型链逐个试）→ (bytes, ext, model) / None。prompt 用中文画面描述。"""
    for model in gen_models():
        body = json.dumps({'model': model, 'prompt': prompt, 'size': size}).encode('utf-8')
        req = urllib.request.Request(GEN_URL, data=body, method='POST', headers={
            'Authorization': f'Bearer {gen_key()}',
            'Content-Type': 'application/json',
        })
        try:
            resp = json.load(urllib.request.urlopen(req, timeout=90))
            url = resp['data'][0]['url']
        except Exception as e:
            print(f'  生图失败（{model}）: {e}')
            continue
        data = download(url)
        if not data:
            continue
        ext = next((ext for magic, ext in MAGIC.items() if data.startswith(magic)), '.png')
        return data, ext, model
    return None


# ── 主流程 ───────────────────────────────────────────────────

def fill_image_element(e, proj, size):
    """按 source 分流取图 → (bytes, ext, 出处) / None。"""
    gen_first = e.get('source') == 'gen'
    chain = ['gen', 'bing', 'commons'] if gen_first else ['bing', 'commons', 'gen']
    for how in chain:
        if how == 'gen':
            if not gen_enabled():
                continue
            r = generate_image(e['query'], size)
            if r:
                return r[0], r[1], f'gen:{r[2]}'
        else:
            hits = search_bing(e['query']) if how == 'bing' else search_commons(e['query'])
            for murl, page in hits:
                data = download(murl)
                if data:
                    return data, None, (murl, page)
    return None


def stage_asset(data, ext, proj, query, page, origin):
    """图落暂存区（.stage/<hash>.<ext> + sidecar 打标），返回 stage 引用 hash。

    sidecar 是 Go assetlib.Ingest 的输入：搜索词即天然 tags（§5）、
    出处页留档；同一内容 hash 只暂存一次（跨段/跨项目去重在 catalog 层幂等）。
    """
    h = hashlib.sha1(data).hexdigest()[:16]
    stage = proj / 'assets' / 'img' / '.stage'
    stage.mkdir(parents=True, exist_ok=True)
    if not (stage / f'{h}{ext}').exists():
        (stage / f'{h}{ext}').write_bytes(data)
        (stage / f'{h}.json').write_text(json.dumps({
            'query': query, 'page': page or '', 'origin': origin,
        }, ensure_ascii=False), encoding='utf-8')
    return h


def main(proj_dir: str) -> int:
    proj = pathlib.Path(proj_dir)
    spec_files = sorted((proj / 'llm').glob('comp-*.spec.json'))
    if not spec_files:
        print('无 spec，跳过')
        return 0
    credits = proj / 'credits.txt'
    size = gen_size(proj)

    got = miss = 0
    for sf in spec_files:
        spec = json.load(open(sf, encoding='utf-8'))
        changed = False
        for e in spec.get('elements', []):
            if e.get('kind') != 'image':
                continue
            query = str(e.get('query', '')).strip()
            if not query:
                continue
            src = str(e.get('src', ''))
            if src.startswith('lib:') or src.startswith('stage:'):
                continue  # 已入库/已暂存（幂等；stage: 由 Go 侧 ingest 收尾）
            r = fill_image_element(e, proj, size)
            if r:
                data, ext, origin = r
                if ext is None:  # 搜图：按 magic 定扩展名
                    ext = next(ext for magic, ext in MAGIC.items() if data.startswith(magic))
                if isinstance(origin, tuple):  # 搜图：(原图url, 来源页)
                    murl, page = origin
                    how = 'fetch'
                    with open(credits, 'a', encoding='utf-8') as f:
                        f.write(f'{query}\t{murl}\t{page}\n')
                else:  # 生图：origin = "gen:<model>"
                    page, how = origin, 'gen'
                    with open(credits, 'a', encoding='utf-8') as f:
                        f.write(f'{query}\t{origin}\tAI 生成（免费档带"AI生成"水印）\n')
                h = stage_asset(data, ext, proj, query, page, how)
                e['src'], e['page'] = f'stage:{h}', page
                print(f"  {sf.stem} 「{query}」 ✓ {len(data)//1024}KB ({how})")
                got += 1
                changed = True
            else:
                e['src'] = ''      # 渲染层兜底便签
                print(f"  {sf.stem} 「{query}」 ✗ 无可用图")
                miss += 1
                changed = True
        if changed:
            json.dump(spec, open(sf, 'w', encoding='utf-8'), ensure_ascii=False, indent=1)
    print(f'图片就绪：{got} 张新取 / {miss} 张无源兜底（入库收尾由服务端完成）')
    return 0


if __name__ == '__main__':
    sys.exit(main(sys.argv[1]))
