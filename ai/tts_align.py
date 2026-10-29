# -*- coding: utf-8 -*-
"""TTS + 词级时间戳对齐（帧述，v2 · 2026-09-23 重构）。

用法：
    python ai/tts_align.py <项目目录>

链路（幂等，缺什么补什么）：
  1. storyboard.json 每段旁白 → audio/segNN.txt
  2. TTS 引擎按优先级（逐段级联，前者失败落后者；2026-10-10 精简为两档）：
       a. 百炼 Qwen-Audio-TTS（DASHSCOPE_API_KEY，qwen-audio-3.1-tts-plus；
          ¥1.4/万字符，free-style 指令控情绪/语速；BAILIAN_TTS_MODEL/VOICE 可换）
       b. Edge TTS（微软免费兜底，zh-CN-XiaoxiaoNeural 晓晓；EDGE_TTS_VOICE 可换音色，
          如 zh-CN-YunxiNeural 云希男声；EDGE_TTS_RATE 可调速，如 +5%）
  3. 时间轴锚点：FunASR paraformer-zh（本地，字级时间戳）；
     不可用降级 faster-whisper（词级→字级均分）。
  4. 对齐策略（断句质量的关键）：ASR 只提供「字符 → 时刻」锚点，
     字幕文本严格取自旁白原文——锚点字符与旁白去标点字符贪心对齐
     （difflib），旁白每字拿到时刻后 jieba 分词、按标点切短语。
     ASR 听错不影响字幕文本，只轻微影响时刻。

产物 audio_meta.json：
  {"voices": [{"id", "duration_s",
               "words":   [{"text","start","end"}, ...],
               "phrases": [[w0,w1],[w2,w5], ...]}]}   # 词序号区间 = 字幕短语
  phrases 缺省时渲染层回落整段胶囊（v1 行为）。

依赖：pip install funasr modelscope jieba soundfile torch(CPU) edge-tts；
     DASHSCOPE_API_KEY 可选（百炼引擎另需 pip install dashscope，无 key 纯 Edge）；
     ffmpeg 在 PATH（edge/百炼产物 mp3→wav 转码用）
"""
import difflib
import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import sys
import time

PUNCT = set('，。！？；、：,.!?;:…—·')
# 百炼档（2026-10-27 用户升级 qwen-audio-3.1-tts-plus，此前 3.0 自 2026-10-10 起用：
# free-style 指令控情绪/语速，
# 内置音色见 help.aliyun.com/zh/model-studio/qwen-audio-tts-voice-list；
# 音色必须属于当前模型，跨模型混用报 cosyvoice Engine error 411）
BL_MODEL = os.environ.get('BAILIAN_TTS_MODEL', 'qwen-audio-3.1-tts-plus')
BL_VOICE = os.environ.get('BAILIAN_TTS_VOICE', 'longanlingxin')


def bailian_voice_id(v):
    """百炼 Qwen-Audio-TTS 音色判别：longan*（中文龙安系）/loong*（英文系）/
    xunanchuan（许南川）；非百炼音色返回 ''。
    3.1 起模型只认带 _v3.x 后缀的音色（裸 id 报 Engine error 411 → 逐段静默落
    Edge），这里按 BL_MODEL 版本自动补后缀——存量 project.json 里的裸 id
    （longanlingxin 等）靠这步续命；3.0 模型仍用裸 id，不补。已带后缀原样返回。"""
    if not v.startswith(('longan', 'loong', 'xunanchuan')):
        return ''
    m = re.search(r'-(\d+)\.(\d+)-tts', BL_MODEL)
    if m and (int(m.group(1)), int(m.group(2))) >= (3, 1) and '_v' not in v:
        v += f'_v{m.group(1)}.{m.group(2)}'
    return v

# 项目级音色（project.json "voice" 字段）：百炼音色（longanlingxin 等，需 DASHSCOPE_API_KEY，
# 未配自动回落 Edge）或 Edge 音色中文名/完整 id。设置后锁定对应引擎并以此
# 音色全量重配（audio_meta.json 的 voice 字段变更即触发）。
VOICE_ALIASES = {
    '晓晓': 'zh-CN-XiaoxiaoNeural',   # 女 · 温暖（默认）
    '晓伊': 'zh-CN-YiyiNeural',      # 女 · 清亮
    '云健': 'zh-CN-YunjianNeural',    # 男 · 磁性浑厚
    '云希': 'zh-CN-YunxiNeural',      # 男 · 年轻阳光
    '云扬': 'zh-CN-YunyangNeural',    # 男 · 新闻专业
    '云野': 'zh-CN-YunyeNeural',      # 男 · 沉稳
    '龙安灵心': 'longanlingxin',      # 百炼 女 · 知心温暖（旗舰，需 DASHSCOPE_API_KEY）
    '龙安鲁风': 'longanlufeng',       # 百炼 男 · 明亮开朗（需 DASHSCOPE_API_KEY）
}


def project_voice(proj):
    """读 project.json 的 voice，别名翻译成完整音色 id；空 = 用默认。"""
    pj = proj / 'project.json'
    if not pj.exists():
        return ''
    try:
        raw = str(json.loads(pj.read_text(encoding='utf-8')).get('voice') or '').strip()
    except Exception:
        return ''
    return VOICE_ALIASES.get(raw, raw) if raw else ''

_FUNASR = None
_WHISPER = None


# ── 第 2 步：TTS ─────────────────────────────────────────────

def _ffmpeg_mp3_to_wav(mp3, out_wav):
    """mp3 文件 → 32kHz 单声道 wav。失败清产物（半截 wav 会毒化幂等重跑）返回 False。"""
    ff = shutil.which('ffmpeg')
    if not ff:
        print('  ffmpeg 缺失，mp3 无法转 wav')
        mp3.unlink(missing_ok=True)
        return False
    try:
        # 显式 utf-8 解码：中文 Windows 默认 cp936 严格解码，ffmpeg 报错回显 UTF-8
        # 内容时会抛 UnicodeDecodeError，把可降级的失败炸成崩溃
        r = subprocess.run(
            [ff, '-y', '-v', 'error', '-i', str(mp3), '-ar', '32000', '-ac', '1', str(out_wav)],
            capture_output=True, text=True, encoding='utf-8', errors='replace', timeout=120)
    except subprocess.TimeoutExpired:
        # 超时按失败处理：不接住会绕过「百炼失败落 Edge」的级联回落直接崩给 Go
        print('  mp3→wav FAIL: ffmpeg 转码超时（120s）')
        mp3.unlink(missing_ok=True)
        out_wav.unlink(missing_ok=True)
        return False
    mp3.unlink(missing_ok=True)
    if r.returncode != 0:
        out_wav.unlink(missing_ok=True)
        print(f'  mp3→wav FAIL: {(r.stderr or "")[-150:]}')
        return False
    return out_wav.exists()


def tts_bailian(text, out_wav, voice=None):
    """百炼 Qwen-Audio-TTS（websocket tts_v2，¥1.4/万字符）→ mp3 → wav。
    失败返回 False（逐段级联回落 Edge）。音色/模型见 BL_MODEL/BL_VOICE。"""
    try:
        from dashscope.audio.tts_v2 import SpeechSynthesizer
    except ImportError:
        print('  bailian tts 跳过: dashscope 未安装（pip install dashscope）')
        return False
    # websocket 建连是瞬态失败重灾区：SDK 内 5s 建连窗口是死数改不了，同一次
    # 运行里前一段刚连上、下一段连挂 3 次（2026-10-29 seg08 实测）→ 整链失败。
    # 在外面补 3 次重试，抖动间隙 3s；连续失败才算真失败回落 Edge。
    last_err = ''
    for attempt in range(3):
        try:
            syn = SpeechSynthesizer(model=BL_MODEL, voice=voice or BL_VOICE)
            audio = syn.call(text)
            if not audio or len(audio) < 2000:
                print(f'  bailian tts 返回过短: {len(audio) if audio else 0} bytes')
                return False
            break
        except Exception as e:
            last_err = str(e)[:200]
            if attempt < 2:
                print(f'  bailian tts 建连抖动，重试 {attempt + 1}/2: {last_err[:120]}')
                time.sleep(3)
    else:
        print(f'  bailian tts 失败: {last_err}')
        return False
    mp3 = out_wav.with_suffix('.mp3')
    mp3.write_bytes(audio)
    return _ffmpeg_mp3_to_wav(mp3, out_wav)


def tts_edge(text_file, out_wav, voice=None):
    """Edge TTS（微软免费兜底）→ mp3 → ffmpeg 转 wav。失败返回 False（该段失败）。"""
    voice = voice or os.environ.get('EDGE_TTS_VOICE', 'zh-CN-XiaoxiaoNeural')
    rate = os.environ.get('EDGE_TTS_RATE', '+0%')
    mp3 = out_wav.with_suffix('.mp3')
    try:
        r = subprocess.run(
            [sys.executable, '-m', 'edge_tts', '--file', str(text_file),
             '--voice', voice, '--rate', rate, '--write-media', str(mp3)],
            capture_output=True, text=True, encoding='utf-8', errors='replace', timeout=300)
    except subprocess.TimeoutExpired:
        print('  edge-tts FAIL: 合成超时（300s）')
        mp3.unlink(missing_ok=True)
        return False
    # Voice does not exist 这类错误 exit code 非 0 但 mp3 可能残留半截文件——
    # 只靠大小兜底会把失败产物当成功，先看 rc
    if r.returncode != 0:
        print(f'  edge-tts FAIL: rc={r.returncode} {(r.stderr or r.stdout)[-150:]}')
        mp3.unlink(missing_ok=True)
        return False
    if not (mp3.exists() and mp3.stat().st_size > 2000):
        print(f'  edge-tts FAIL: {(r.stderr or r.stdout)[-150:]}')
        return False
    return _ffmpeg_mp3_to_wav(mp3, out_wav)


# ── 第 3 步：时间轴锚点（ASR，只出「字符→时刻」）────────────

def anchor_funasr(wav_path):
    """FunASR paraformer 字级时间戳 → [(char, start, end)]；不可用/异常返回 None。"""
    global _FUNASR
    try:
        from funasr import AutoModel
    except ImportError:
        return None
    try:
        if _FUNASR is None:
            _FUNASR = AutoModel(model='paraformer-zh', vad_model='fsmn-vad', disable_update=True)
        res = _FUNASR.generate(input=str(wav_path))[0]
        text, ts = res.get('text', ''), res.get('timestamp') or []
        text = text.replace(' ', '').replace('\u3000', '')   # funasr 中文输出字符间带空格
        if not text or not ts or len(text) != len(ts):
            return None
        return [(ch, s / 1000.0, e / 1000.0) for ch, (s, e) in zip(text, ts)]
    except Exception as e:
        print(f'  funasr 异常: {e}')
        return None


def anchor_whisper_chars(wav_path, narration):
    """faster-whisper 降级：词级时间戳按字数均分成字级。失败返回 None——与 funasr
    路径同一口径的干净降级（模型下载失败/OOM 在这兜住，不炸 main）。"""
    global _WHISPER
    try:
        from faster_whisper import WhisperModel
    except ImportError:
        return None
    try:
        if _WHISPER is None:
            _WHISPER = WhisperModel('medium', device='cpu', compute_type='int8')
        segments, _ = _WHISPER.transcribe(str(wav_path), language='zh',
                                          initial_prompt=narration[:60], word_timestamps=True)
        raw = []
        for seg in segments:
            for w in (seg.words or []):
                t = re.sub(r'\s', '', w.word)
                if not t:
                    continue
                dur = (w.end - w.start) / len(t)
                for k, ch in enumerate(t):
                    raw.append((ch, w.start + k * dur, w.start + (k + 1) * dur))
        return raw or None
    except Exception as e:
        print(f'  whisper 异常: {str(e)[:200]}')
        return None


# ── 第 4 步：锚点 → 旁白字符时刻 → jieba 词 + 标点短语 ───────

def seg_words_and_phrases(narration, anchors):
    """旁白原文 + ASR 锚点 → (words, phrases, 匹配率)。文本严格来自旁白。

    匹配率 = 锚点字符直接命中的旁白字符占比；过低说明 ASR 听错太多
    （TTS 音质差/混入杂音时常见），调用方应换锚点源重算。
    """
    import jieba
    chars = [ch for ch in narration if ch.strip()]
    a_chars = ''.join(a[0] for a in anchors)
    a_start = [a[1] for a in anchors]
    a_end = [a[2] for a in anchors]

    # 贪心对齐：锚点字符 ↔ 旁白去空白字符
    sm = difflib.SequenceMatcher(None, a_chars, ''.join(chars), autojunk=False)
    char_t = [None] * len(chars)
    matched = 0
    for ai, ci, n in sm.get_matching_blocks()[:-1]:
        for k in range(n):
            if char_t[ci + k] is None:
                char_t[ci + k] = (a_start[ai + k], a_end[ai + k])
                matched += 1
    ratio = matched / max(len(chars), 1)

    # 缺口插值：段首贴 0，段尾贴最后一个锚点后
    last_end = a_end[-1] if a_end else 0.5
    prev_t = (0.0, 0.02)
    i = 0
    while i < len(chars):
        if char_t[i] is not None:
            prev_t = char_t[i]
            i += 1
            continue
        j = i
        while j < len(chars) and char_t[j] is None:
            j += 1
        nxt_t = char_t[j] if j < len(chars) else (last_end + 0.08, last_end + 0.16)
        # ASR 噪声可让下一锚点早于上一锚点结束（时间戳倒挂），负 span 会把插值词
        # 时刻排到前词之前、倒挂写进 audio_meta.json——钳 0 退化为原地均铺
        span = max(nxt_t[0] - prev_t[1], 0.0) / max(j - i, 1)
        for k in range(i, j):
            char_t[k] = (prev_t[1] + (k - i) * span, prev_t[1] + (k - i + 1) * span)
        prev_t = nxt_t
        i = j

    # 原文字符坐标 → chars 下标映射
    cmap, ci = [], 0
    for ch in narration:
        cmap.append(ci if ch.strip() else None)
        if ch.strip():
            ci += 1

    words, phrases, cur, cur_len = [], [], [], 0
    for tok, b, e in jieba.tokenize(narration):
        if not tok.strip():
            continue
        if all(ch in PUNCT for ch in tok):   # 标点：收短语，不入词
            if cur:
                phrases.append([cur[0], cur[-1]])
                cur, cur_len = [], 0
            continue
        ids = [cmap[k] for k in range(b, e) if cmap[k] is not None]
        if not ids:
            continue
        words.append({'text': tok,
                      'start': round(char_t[ids[0]][0], 2),
                      'end': round(char_t[ids[-1]][1], 2)})
        cur.append(len(words) - 1)
        cur_len += len(tok)
        if cur_len >= 16:                    # 无标点超长也切（字幕别一行兜不住）
            phrases.append([cur[0], cur[-1]])
            cur, cur_len = [], 0
    if cur:
        phrases.append([cur[0], cur[-1]])
    return words, phrases, ratio


# ── 主流程 ───────────────────────────────────────────────────

def wav_duration(path):
    import soundfile as sf
    return round(sf.info(str(path)).duration, 3)


def main(proj_dir: str) -> int:
    # Go 侧管道捕获不设 PYTHONUTF8，中文 Windows 默认 cp936 严格解码——✓/✗ 不在 GBK 中，
    # 成功路径的 print 反而炸成 rc=1。errors='replace' 保输出永远可写。
    for stream in (sys.stdout, sys.stderr):
        if hasattr(stream, 'reconfigure'):
            stream.reconfigure(errors='replace')
    proj = pathlib.Path(proj_dir)
    DASHSCOPE_KEY = os.environ.get('DASHSCOPE_API_KEY', '')
    sb = json.loads((proj / 'storyboards' / 'storyboard.json').read_text(encoding='utf-8'))
    (proj / 'audio').mkdir(exist_ok=True)

    # 空旁白段两个引擎都合不出声（百炼返回空 / edge-tts 直接报错），拖进循环才失败
    # 会白烧前面已配好的段——先快速失败，把段 id 列全
    empties = [seg['id'] for seg in sb['segments'] if not str(seg.get('narration') or '').strip()]
    if empties:
        print(f'错误: 旁白为空的段: {", ".join(empties)}——先补分镜稿再配音')
        return 1

    # 1+2. 文本落盘 + TTS（先写全 txt 再出 wav，避免路径被当文本朗读的坑）
    for seg in sb['segments']:
        (proj / 'audio' / f"{seg['id']}.txt").write_text(seg['narration'], encoding='utf-8')
    pv = project_voice(proj)
    # 引擎决策（2026-10-10 精简：百炼 → Edge 兜底，硅基/Kokoro 档退役）：
    # 项目音色是百炼音色 → 锁百炼（未配 key 警告回落 Edge）；是 Edge 音色 → 锁 Edge；
    # 陌生音色（CosyVoice 短名 alex 等已随硅基档退役）→ 警告回落 Edge 默认；
    # 未设音色 → 有 DASHSCOPE_API_KEY 走百炼默认龙安灵心，无 key 纯 Edge。
    bl = bailian_voice_id(pv) if pv else ''
    if bl and not DASHSCOPE_KEY:
        print('警告: voice 是百炼音色但未配 DASHSCOPE_API_KEY → 回落 Edge 晓晓')
        pv = ''
    elif pv and not bl and not ('-' in pv and 'Neural' in pv):
        print(f'警告: voice {pv!r} 非百炼/Edge 音色（CosyVoice 档已退役）→ 回落 Edge 晓晓')
        pv = ''
    # 项目指定 Edge 音色（pv 有值且非百炼系）→ 锁 Edge：之前只看 key，配了 key
    # 就走百炼默认龙安灵心，audio_meta 却记着 Edge 音色——静默错配
    engine = 'bailian' if (DASHSCOPE_KEY and not (pv and not bl)) else 'edge'
    bl_use = bl or (bailian_voice_id(BL_VOICE) if DASHSCOPE_KEY else '')
    # edge_voice 只认 Edge 风格音色（含 '-' 且含 'Neural'，与上面判别同口径）：
    # 百炼失败级联回落 Edge 时，百炼音色 id（longanlingxin 等）传给 edge-tts 必报
    # Voice does not exist → 该段直接 return 1——此时落环境变量/默认晓晓，兜底才兜得住
    edge_voice = (pv if '-' in pv and 'Neural' in pv else '') \
        or os.environ.get('EDGE_TTS_VOICE', 'zh-CN-XiaoxiaoNeural')

    # 项目音色切换 → 旧 wav 全部作废重配（audio_meta.json 的 voice 字段对不上即触发）
    meta_path = proj / 'audio_meta.json'
    prev_meta_voice = ''
    if meta_path.exists():
        try:
            prev_meta_voice = str(json.loads(meta_path.read_text(encoding='utf-8')).get('voice') or '')
        except Exception:
            prev_meta_voice = ''
    # 音色指纹编码「引擎:生效音色:模型」：环境变量换音色/模型（EDGE_TTS_VOICE、
    # BAILIAN_TTS_*）也触发旧 wav 作废重配——旧格式只记 pv，env 变更指纹不变，
    # 旧配音静默残留。旧格式值（'default'/'longanlingxin'）照常参与比较，
    # 首次跑到这里不等即一次性重配，无需迁移。
    if engine == 'bailian':
        meta_voice = f'bailian:{bl_use or BL_VOICE}:{BL_MODEL}'
    else:
        meta_voice = f'edge:{edge_voice}'
    if prev_meta_voice and prev_meta_voice != meta_voice:
        removed = 0
        for w in (proj / 'audio').glob('*.wav'):
            w.unlink()
            removed += 1
        for s in (proj / 'audio').glob('*.wav.src'):
            s.unlink(missing_ok=True)
        # 旧 meta 一并作废（2026-10-19 低危）：它记着旧音色的时长/词级时间戳，
        # 且引用的 wav 已删——留着就是「有账无货」的中间态（TTS 中途失败时
        # 下游读到会按旧时长拼画面）；下一轮跑完自然重写
        meta_path.unlink(missing_ok=True)
        print(f'音色切换 {prev_meta_voice} → {meta_voice}，重配 {removed} 段')

    def narr_fp(text):
        return hashlib.sha256(text.encode('utf-8')).hexdigest()[:16]

    for seg in sb['segments']:
        wav = proj / 'audio' / f"{seg['id']}.wav"
        src = proj / 'audio' / f"{seg['id']}.wav.src"
        fp = narr_fp(seg['narration'])
        if wav.exists():
            # 内容指纹闸：段 id 是顺序编号，分镜改稿后 id 复用而旁白已换——
            # 只看"文件存在"跳过会把旧旁白配音静默带进新成片。
            # 旁车缺失（升级前的旧产物）也按过期处理，一次性重配。
            old = src.read_text(encoding='utf-8').strip() if src.exists() else ''
            if old == fp:
                print(seg['id'], 'wav 已有')
                continue
            wav.unlink(missing_ok=True)
            print(seg['id'], '旁白已变，重配')
        if engine == 'bailian' and tts_bailian(seg['narration'], wav, bl_use):
            print(seg['id'], f"wav ✓ (bailian {bl_use or BL_VOICE})")
        elif tts_edge(proj / 'audio' / f"{seg['id']}.txt", wav, edge_voice):
            print(seg['id'], f"wav ✓ (edge {edge_voice})")
        else:
            return 1
        src.write_text(fp, encoding='utf-8')

    # 3+4. 对齐：ASR 锚点 + 旁白原文回写；匹配率过低换锚点源重算。
    # 快路径：audio_meta.json 已有该段结果且 wav 内容未变（md5 指纹）→ 整段复用不重跑
    # ASR——模型加载+逐段推理占「配音」工位时长大头，produce 修复/自动重跑轮里音频
    # 根本没变，全量重算纯属白付（8 段白付约 1 分半，事件流上看着像重复配音）。
    # 指纹不匹配（重配过的段、旧格式产物无指纹）→ 该段照常重算并补指纹；
    # 音色切换已在上面作废全部 wav 且 meta_voice 变了，这里 prev 只在音色一致时装载。
    prev_voices = {}
    if meta_path.exists() and prev_meta_voice == meta_voice:
        try:
            for v in json.loads(meta_path.read_text(encoding='utf-8')).get('voices') or []:
                if isinstance(v, dict) and v.get('id'):
                    prev_voices[str(v['id'])] = v
        except Exception:
            prev_voices = {}

    def wav_md5(p):
        h = hashlib.md5()
        with open(p, 'rb') as f:
            for chunk in iter(lambda: f.read(1 << 20), b''):
                h.update(chunk)
        return h.hexdigest()

    voices = []
    reused = 0
    for seg in sb['segments']:
        sid = seg['id']
        wav = proj / 'audio' / f"{sid}.wav"
        fp_wav = wav_md5(wav)
        prev = prev_voices.get(sid)
        if prev and prev.get('wav_md5') == fp_wav and prev.get('words') and prev.get('phrases'):
            voices.append(prev)
            reused += 1
            print(f'{sid}: 对齐复用')
            continue
        anchors = anchor_funasr(wav)
        best = None
        if anchors is not None:
            words, phrases, ratio = seg_words_and_phrases(seg['narration'], anchors)
            best = (words, phrases, ratio, 'funasr')  # 先记 funasr 结果，匹配率不足再被 whisper 竞争
        if best is None or best[2] < 0.5:
            print(f'  {sid}: funasr 锚点匹配率低（{best[2]:.0%}）' if best else f'  {sid}: funasr 不可用',
                  '→ faster-whisper 重算')
            w_anchors = anchor_whisper_chars(wav, seg['narration'])
            if w_anchors:
                w_words, w_phrases, w_ratio = seg_words_and_phrases(seg['narration'], w_anchors)
                if best is None or w_ratio > best[2]:
                    best = (w_words, w_phrases, w_ratio, 'whisper')
        if best is None:
            print(f'{sid}: 无锚点，失败')
            return 1
        words, phrases, ratio, src = best
        voices.append({'id': sid, 'duration_s': wav_duration(wav),
                       'words': words, 'phrases': phrases, 'wav_md5': fp_wav})
        print(f'{sid}: {len(words)} 词 / {len(phrases)} 短语 ✓（锚点 {src}，匹配率 {ratio:.0%}）')
    if reused:
        print(f'对齐：{reused} 段复用 / {len(voices) - reused} 段重算')

    # 原子替换：Go 侧（loadAudioMeta）与翻译回写并发读此文件，裸写会读到半截 JSON
    tmp = proj / 'audio_meta.json.tmp'
    with open(tmp, 'w', encoding='utf-8') as f:
        json.dump({'voices': voices, 'voice': meta_voice}, f, ensure_ascii=False, indent=1)
    os.replace(tmp, proj / 'audio_meta.json')
    print(f'audio_meta.json ✓（{len(voices)} 段，engine={engine}，voice={meta_voice}）')
    return 0


if __name__ == '__main__':
    if len(sys.argv) < 2:
        print('用法: python ai/tts_align.py <项目目录>', file=sys.stderr)
        sys.exit(2)
    sys.exit(main(sys.argv[1]))
