# -*- coding: utf-8 -*-
"""vendor_tool:vend 阶段 CLI(docs/vend-stage.md §3)——库的采购/验收/台账。

vend 铁律:模型只有申请权(场景内 <!-- vend: lib@x.y.z -->),执行永远由本工具
(平台代码)代劳;哈希不符=安全事件拒收;全程留痕(manifest 台账)。

用法:
  python ai/vendor_tool.py list                        # 台账一览
  python ai/vendor_tool.py ensure three@0.152.2 --file build/three.min.js
                                                       # 缺则采购+验收+入册;已登记则复核放行
  python ai/vendor_tool.py exam three --replay 证据路径  # 补录确定性回放证据后复验
  python ai/vendor_tool.py inline <html路径> [--keep-missing]
                                                       # 把 html 内 vend 声明替换为内联 <script>
                                                       # (样张/小样等自包含文档用;缺册默认严格失败)

vendor 布局(挂卷 data/library,容器重建不丢——vend-stage.md §4):
  data/library/vendor/<lib>/<version>/<file>
  data/library/vendor/manifest.json
"""
import hashlib
import json
import pathlib
import re
import shutil
import subprocess
import sys
import tarfile
import time

ROOT = pathlib.Path(__file__).resolve().parent.parent
VENDOR = ROOT / 'data' / 'library' / 'vendor'
MANIFEST = VENDOR / 'manifest.json'

SIZE_CAP = 20 * 1024 * 1024          # 单文件硬上限(vend-stage.md §5 体积限额)
LICENSE_ALLOW = {'MIT', 'Apache-2.0', 'ISC', 'BSD-2-Clause', 'BSD-3-Clause',
                 '0BSD', 'Unlicense', 'CC0-1.0'}
REPLAY_TOLERANCE_NOTE = '确定性回放证据=两轮渲染抽帧 md5 逐对全同(见 GATE.md 类记录)'


def load_manifest():
    if MANIFEST.exists():
        return json.loads(MANIFEST.read_text(encoding='utf-8'))
    return {'libs': {}}


def save_manifest(m):
    MANIFEST.parent.mkdir(parents=True, exist_ok=True)
    tmp = MANIFEST.with_suffix('.json.tmp')
    tmp.write_text(json.dumps(m, ensure_ascii=False, indent=1, sort_keys=True), encoding='utf-8')
    tmp.replace(MANIFEST)


def sha256_of(p: pathlib.Path) -> str:
    h = hashlib.sha256()
    h.update(p.read_bytes())
    return h.hexdigest()


def curl_retry(url: str, out: pathlib.Path, tries: int = 4) -> bool:
    if not shutil.which('curl'):
        print('✗ 环境缺 curl——按 vend-stage.md §5 报修（不回喂模型）')
        return False
    for k in range(tries):
        r = subprocess.run(['curl', '-s', '--retry', '2', '--max-time', '120',
                            '-L', '-o', str(out), url])
        if r.returncode == 0 and out.exists() and out.stat().st_size > 0:
            return True
        time.sleep(2)
    return False


def registry_meta(lib: str, version: str) -> dict:
    """npm registry 元数据(小 JSON):auto 定位浏览器版 JS 的依据。"""
    r = subprocess.run(['curl', '-s', '--retry', '2', '--max-time', '60',
                        f'https://registry.npmjs.org/{lib}/{version}'], capture_output=True)
    try:
        return json.loads(r.stdout.decode('utf-8', 'replace'))
    except Exception:
        return {}


def auto_member(tgz: pathlib.Path, meta: dict) -> str:
    """自动定位 tarball 内浏览器版 JS:unpkg/jsdelivr/browser/main 元数据优先,
    兜底扫 .min.js(浅路径优先)——Go 闭环要求 ensure 无需人工指路径。"""
    with tarfile.open(tgz, 'r:gz') as tf:
        names = [n[len('package/'):] for n in tf.getnames() if n.startswith('package/')]
    for key in ('unpkg', 'jsdelivr', 'browser', 'main'):
        v = meta.get(key) if isinstance(meta, dict) else None
        if isinstance(v, str):
            v = v.lstrip('./')
            if v in names:
                return v
    mins = sorted((n for n in names if n.endswith('.min.js')),
                  key=lambda n: (n.count('/'), len(n)))
    if mins:
        return mins[0]
    raise SystemExit('✗ 无法自动定位浏览器版 JS(用 --file 指定 tarball 内路径)')


def npm_fetch(lib: str, version: str, member: str, workdir: pathlib.Path):
    """从 npm registry 拉钉版 tarball,抽取 member(package/ 前缀),返回 (文件, 元数据, 成员路径)。
    member 为空 → 自动解析。tarball 用完即删,vendor 卷不堆杂物。"""
    tgz = workdir / f'{lib}-{version}.tgz'
    url = f'https://registry.npmjs.org/{lib}/-/{lib}-{version}.tgz'
    print(f'  下载 {url}')
    if not curl_retry(url, tgz):
        raise SystemExit('✗ 采购失败:下载不可达(路的问题——按 vend-stage.md §5 报修,不回喂模型)')
    meta = registry_meta(lib, version)
    if not member:
        member = auto_member(tgz, meta)
        print(f'  自动定位成员文件: {member}')
    with tarfile.open(tgz, 'r:gz') as tf:
        f = tf.extractfile('package/' + member)
        if not f:
            raise SystemExit(f'✗ 采购失败:tarball 内无 package/{member}')
        data = f.read()
    tgz.unlink(missing_ok=True)  # 抽完成员即删：tgz 留在 VENDOR/lib/version/ 里只添乱
    out = workdir / pathlib.Path(member).name
    out.write_bytes(data)
    return out, meta, member


PENDING_REPLAY = 'pending(自动闭环采购;首次使用段的 sceneReplayCheck 为回放硬闸,exam --replay 可升级证据)'


def exam_entry(entry: dict, replay_evidence: str = '') -> list:
    """离线验收:哈希/体积/许可证;回放证据分层——真实证据(GATE/双渲染 md5)为佳,
    pending 视为附条件通过(运行期由管线 sceneReplayCheck 硬闸兜底,台账可见)。
    只读不改 entry:回放证据由调用方在验收通过后自行登记——旧版在这就写
    entry['replay_exam'],失败版本的证据会随成功版本的 save_manifest 一并落盘。"""
    errs = []
    p = VENDOR / entry['lib'] / entry['version'] / entry['file']
    if not p.exists():
        return [f'文件缺失 {p}']
    if sha256_of(p) != entry['sha256']:
        errs.append(f'哈希不符:{p.name}——安全事件,拒收不重试(§5)')
    if p.stat().st_size > SIZE_CAP:
        errs.append(f'体积 {p.stat().st_size} 超单库上限 {SIZE_CAP}')
    lic = entry.get('license', '')
    if lic and lic not in LICENSE_ALLOW:
        errs.append(f'许可证 {lic} 不在白名单 {sorted(LICENSE_ALLOW)}')
    if not (replay_evidence or entry.get('replay_exam')):
        errs.append('缺确定性回放证据(两轮渲染抽帧 md5 对比;exam --replay 补录)')
    return errs


def ensure(spec: str, member: str = '', replay_evidence: str = '', allow_pending: bool = False) -> int:
    lib, _, version = spec.partition('@')
    if not lib or not version:
        print('用法: ensure <lib>@<version> [--file <tarball内路径>,缺省自动定位] [--allow-pending-replay]')
        return 1
    if not replay_evidence and allow_pending:
        replay_evidence = PENDING_REPLAY
    m = load_manifest()
    ent = m['libs'].get(lib, {}).get(version)
    if ent:
        errs = exam_entry(ent, replay_evidence)
        if not errs:
            print(f'✓ {lib}@{version} 已登记且复核通过({ent["file"]}, sha256 前12={ent["sha256"][:12]})')
            return 0
        if any(e.startswith('哈希不符') for e in errs):
            # 台账声明「哈希不符=安全事件,拒收不重试」就得说到做到:盘上文件与登记
            # 哈希对不上=已被替换/损坏,自动重新采购=静默换血,把安全事件洗成"一切
            # 正常"。标记需人工留痕并拒收,复购必须人来核源后拍板
            ent['needs_manual'] = f'{time.strftime("%Y-%m-%d %H:%M")} 复核哈希不符,需人工核源'
            save_manifest(m)
            print(f'✗ {lib}@{version} 已登记库复核哈希不符——安全事件,标记需人工并拒收(不自动重购):')
            for e in errs:
                print(f'  - {e}')
            return 1
        print(f'✗ {lib}@{version} 已登记但复核不过,重新采购:')
        for e in errs:
            print(f'  - {e}')
    workdir = VENDOR / lib / version
    workdir.mkdir(parents=True, exist_ok=True)
    f, meta, member = npm_fetch(lib, version, member, workdir)
    entry = {'lib': lib, 'version': version, 'file': f.name,
             'sha256': sha256_of(f), 'bytes': f.stat().st_size,
             'license': meta.get('license', ''), 'source': f'npm:{lib}@{version}#{member}',
             'vendored_at': time.strftime('%Y-%m-%d %H:%M'), 'replay_exam': replay_evidence}
    errs = exam_entry(entry, replay_evidence)
    if errs:
        for e in errs:
            print(f'  ✗ 验收不过:{e}')
        return 1
    m['libs'].setdefault(lib, {})[version] = entry
    save_manifest(m)
    print(f'✓ 入册 {lib}@{version} file={entry["file"]} bytes={entry["bytes"]} '
          f'license={entry["license"]} sha256前12={entry["sha256"][:12]}')
    return 0


def exam_cmd(spec: str, replay_evidence: str) -> int:
    lib, _, version = spec.partition('@')
    m = load_manifest()
    versions = [version] if version else list(m['libs'].get(lib, {}))
    if not versions:
        print(f'✗ 台账无 {lib}(先 ensure)')
        return 1
    bad = 0
    for v in versions:
        ent = m['libs'][lib][v]
        errs = exam_entry(ent, replay_evidence)
        if errs:
            bad += 1
            print(f'✗ {lib}@{v}:')
            for e in errs:
                print(f'  - {e}')
        else:
            if replay_evidence:
                # 验收通过才登记证据并落盘：exam_entry 只读不改，失败版本的
                # replay_exam 不会再随成功版本的 save_manifest 一并落盘
                ent['replay_exam'] = replay_evidence
                save_manifest(m)
            print(f'✓ {lib}@{v} 验收通过(replay 证据:{ent.get("replay_exam", "-")[:60]})')
    return bad


VEND_RE = re.compile(r'<!--\s*vend:\s*([A-Za-z0-9_.-]+)@([^\s>]+?)\s*-->')


def inline_cmd(path: str, keep_missing: bool) -> int:
    """把 html 内的 vend 声明替换为内联 <script>(样张截图/自包含文档的注入形态:
    渲染侧没有工程 assets 目录可挂,内联后文档依旧自包含)。入库库逐个复验哈希
    (防盘上文件被换血后静默内联);缺册默认严格失败——调用方(平台代码)负责先
    ensure 采购,失败走到这就是 bug/竞态,响亮死好过静默产出坏帧。--keep-missing
    降级给无采购环节的批量路径:缺册声明原样保留并告警(画面空转,由判卷兜底)。
    同库多次声明只内联首处,其余声明原地删除(重复加载白付体积)。"""
    p = pathlib.Path(path)
    html = p.read_text(encoding='utf-8')
    m = load_manifest()
    specs, seen = [], set()
    for mm in VEND_RE.finditer(html):
        spec = (mm.group(1), mm.group(2))
        if spec not in seen:
            seen.add(spec)
            specs.append(spec)
    if not specs:
        print('inline: 无 vend 声明,原样返回')
        return 0
    inlined, removed = 0, 0
    done = set()

    def repl(mm):
        nonlocal inlined, removed
        lib, ver = mm.group(1), mm.group(2)
        ent = m['libs'].get(lib, {}).get(ver)
        if ent is None:
            if keep_missing:
                print(f'  ⚠ {lib}@{ver} 未入册,声明保留(--keep-missing)——画面大概率空转,判卷兜底')
                return mm.group(0)
            print(f'✗ {lib}@{ver} 未入册——先跑 python ai/vendor_tool.py ensure {lib}@{ver}')
            raise SystemExit(1)
        f = VENDOR / lib / ver / ent['file']
        if not f.exists() or sha256_of(f) != ent['sha256']:
            print(f'✗ {lib}@{ver} 盘上文件缺失/哈希不符——安全事件,拒收(§5)')
            raise SystemExit(1)
        if (lib, ver) in done:  # 同库重复声明:首处已内联,此处删除(重复加载白付体积)
            removed += 1
            return '<!-- vend ' + lib + '@' + ver + ' 已内联于首处 -->'
        done.add((lib, ver))
        inlined += 1
        return '<script>\n' + f.read_text(encoding='utf-8') + '\n</script>'

    out = VEND_RE.sub(repl, html)
    p.write_text(out, encoding='utf-8')
    print(f'inline: 内联 {inlined} 个库,去重 {removed} 处声明 → {p.name}')
    return 0


def list_cmd() -> int:
    m = load_manifest()
    if not m['libs']:
        print('(台账空——vendor 卷 data/library/vendor)')
        return 0
    for lib, versions in sorted(m['libs'].items()):
        for v, e in sorted(versions.items()):
            rp = e.get('replay_exam', '')
            rp_disp = 'pending' if rp.startswith('pending') else ('有' if rp else '缺')
            nm = f'  [需人工:{e["needs_manual"]}]' if e.get('needs_manual') else ''
            print(f'{lib}@{v}  {e["file"]}  {e["bytes"]//1024}KB  license={e.get("license","-")}  '
                  f'sha256前12={e["sha256"][:12]}  replay={rp_disp}{nm}')
    return 0


if __name__ == '__main__':
    # Go 侧管道捕获不设 PYTHONUTF8，中文 Windows 默认 cp936 严格解码——✓/✗ 不在 GBK 中，
    # 成功路径的 print 反而炸成 rc=1。errors='replace' 保输出永远可写。
    for stream in (sys.stdout, sys.stderr):
        if hasattr(stream, 'reconfigure'):
            stream.reconfigure(errors='replace')
    a = sys.argv[1:]
    if not a or a[0] in ('-h', '--help', 'help'):
        print(__doc__)
        sys.exit(0)
    def opt_val(flag):
        # 手工解析没有 argparse 的边界保护：flag 排末位无值时取值会 IndexError
        i = a.index(flag)
        return a[i + 1] if i + 1 < len(a) else ''

    cmd = a[0]
    if cmd == 'list':
        sys.exit(list_cmd())
    if cmd == 'ensure':
        spec = a[1] if len(a) > 1 else ''
        member = opt_val('--file') if '--file' in a else ''
        ev = opt_val('--replay') if '--replay' in a else ''
        pending = '--allow-pending-replay' in a
        sys.exit(ensure(spec, member, ev, pending))
    if cmd == 'exam':
        spec = a[1] if len(a) > 1 else ''
        ev = opt_val('--replay') if '--replay' in a else ''
        sys.exit(exam_cmd(spec, ev))
    if cmd == 'inline':
        path = a[1] if len(a) > 1 else ''
        if not path:
            print('用法: inline <html路径> [--keep-missing]')
            sys.exit(1)
        sys.exit(inline_cmd(path, '--keep-missing' in a))
    print(__doc__)
    sys.exit(1)
