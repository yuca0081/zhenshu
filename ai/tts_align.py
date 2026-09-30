# -*- coding: utf-8 -*-
"""TTS + 词级时间戳对齐（帧述，v2 · 2026-09-23 重构）。

用法：
    python ai/tts_align.py <项目目录>

链路（幂等，缺什么补什么）：
  1. storyboard.json 每段旁白 → audio/segNN.txt
  2. TTS 引擎按优先级（逐段级联，前者失败落后者）：
       a. SILICONFLOW_API_KEY（CosyVoice2，中文自然度高；.env 配 key 即启用）
       b. Edge TTS（微软免费，zh-CN-XiaoxiaoNeural 晓晓；EDGE_TTS_VOICE 可换音色，
          如 zh-CN-YunxiNeural 云希男声；EDGE_TTS_RATE 可调速，如 +5%）
       c. hyperframes tts（Kokoro zf_xiaobei，离线兜底，音质差仅应急）
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
     SILICONFLOW_API_KEY 可选；ffmpeg 在 PATH（edge 产物 mp3→wav 转码用）
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
import urllib.request

VOICE_KOKORO, PINNED = 'zf_xiaobei', 'hyperframes@0.8.55'
PUNCT = set('，。！？；、：,.!?;:…—·')
SF_URL = 'https://api.siliconflow.cn/v1/audio/speech'
# 付费档可配（TTS-付费档评估 2026-09-28：默认 CosyVoice2 alex，A/B 见
# docs/analyes/tts-paid-tier-evaluation.md；TTS_MODEL/TTS_VOICE 环境变量可换）
SF_MODEL = os.environ.get('TTS_MODEL', 'FunAudioLLM/CosyVoice2-0.5B')
SF_VOICE = os.environ.get('TTS_VOICE', 'FunAudioLLM/CosyVoice2-0.5B:alex')
SF_KEY = ''

# CosyVoice2 内置音色（SiliconFlow）：短名补全成完整 id；项目 voice 字段命中即走付费档
COSY_VOICES = ('alex', 'benjamin', 'charles', 'david')


def cosy_voice_id(v):
    """'alex' 短名 → 完整 CosyVoice 音色 id；已是完整 id 透传；非 cosy 音色返回 ''。"""
    if v.startswith('FunAudioLLM/CosyVoice'):
        return v
    name = v.split(':')[-1].strip()
    if name in COSY_VOICES:
        return f'FunAudioLLM/CosyVoice2-0.5B:{name}'
    return ''

# 项目级音色（project.json "voice" 字段）：Edge 音色中文名或完整 id；CosyVoice 短名
# （alex 等，需 SILICONFLOW_API_KEY，未配自动回落 Edge）。设置后锁定对应引擎并以此
# 音色全量重配（audio_meta.json 的 voice 字段变更即触发）。
VOICE_ALIASES = {
    '晓晓': 'zh-CN-XiaoxiaoNeural',   # 女 · 温暖（默认）
    '晓伊': 'zh-CN-YiyiNeural',      # 女 · 清亮
    '云健': 'zh-CN-YunjianNeural',    # 男 · 磁性浑厚
    '云希': 'zh-CN-YunxiNeural',      # 男 · 年轻阳光
    '云扬': 'zh-CN-YunyangNeural',    # 男 · 新闻专业
    '云野': 'zh-CN-YunyeNeural',      # 男 · 沉稳
}


def project_voice(proj):
    """读 project.json 的 voice，别名翻译成完整音色 id；空 = 用默认。"""
    pj = proj / 'project.json'
    if not pj.exists():
        return ''
    try:
        raw = str(json.load(open(pj, encoding='utf-8')).get('voice') or '').strip()
    except Exception:
        return ''
    return VOICE_ALIASES.get(raw, raw) if raw else ''

_FUNASR = None
_WHISPER = None


# ── 第 2 步：TTS ─────────────────────────────────────────────

def tts_siliconflow(text, out_wav, voice=None):
    """硅基流动 CosyVoice2（付费档）→ wav。失败返回 False（回落 Edge/Kokoro）。"""
    body = json.dumps({
        'model': SF_MODEL, 'input': text, 'voice': voice or SF_VOICE,
        'response_format': 'wav', 'sample_rate': 32000,
    }).encode('utf-8')
    req = urllib.request.Request(SF_URL, data=body, headers={
        'Authorization': f'Bearer {SF_KEY}', 'Content-Type': 'application/json',
    })
    try:
        with urllib.request.urlopen(req, timeout=180) as r:
            audio = r.read()
        if len(audio) < 2000:
            return False
        out_wav.write_bytes(audio)
        return True
    except Exception as e:
        print(f'  siliconflow tts 失败（回落 kokoro）: {e}')
        return False


def tts_edge(text_file, out_wav, voice=None):
    """Edge TTS（微软免费）→ mp3 → ffmpeg 转 wav。失败返回 False（回落 Kokoro）。"""
    voice = voice or os.environ.get('EDGE_TTS_VOICE', 'zh-CN-XiaoxiaoNeural')
    rate = os.environ.get('EDGE_TTS_RATE', '+0%')
    mp3 = out_wav.with_suffix('.mp3')
    r = subprocess.run(
        [sys.executable, '-m', 'edge_tts', '--file', str(text_file),
         '--voice', voice, '--rate', rate, '--write-media', str(mp3)],
        capture_output=True, text=True, timeout=300)
    if not (mp3.exists() and mp3.stat().st_size > 2000):
        print(f'  edge-tts FAIL: {(r.stderr or r.stdout)[-150:]}')
        return False
    ff = shutil.which('ffmpeg')
    if not ff:
        print('  ffmpeg 缺失，edge-tts mp3 无法转 wav')
        mp3.unlink(missing_ok=True)
        return False
    r2 = subprocess.run(
        [ff, '-y', '-v', 'error', '-i', str(mp3), '-ar', '32000', '-ac', '1', str(out_wav)],
        capture_output=True, text=True, timeout=120)
    mp3.unlink(missing_ok=True)
    if r2.returncode != 0:
        # 半截 wav 会毒化后续重跑（存在即被当成品跳过），失败必须清掉
        out_wav.unlink(missing_ok=True)
        print(f'  edge mp3→wav FAIL: {(r2.stderr or "")[-150:]}')
        return False
    return out_wav.exists()


def tts_kokoro(text_file, out_wav):
    npx = shutil.which('npx') or shutil.which('npx.cmd')
    if not npx:
        print('  kokoro FAIL: npx 不在 PATH')
        return False
    r = subprocess.run(
        [npx, '--offline', '--yes', PINNED, 'tts', '--text-file', str(text_file),
         '-v', VOICE_KOKORO, '-l', 'zh', '-o', str(out_wav), '--json'],
        capture_output=True, text=True, timeout=300)
    ok = any(l.startswith('{"ok":true') for l in r.stdout.splitlines())
    if not ok:
        out_wav.unlink(missing_ok=True)
        print(f'  kokoro FAIL: {(r.stderr or r.stdout)[-150:]}')
    return ok


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
    """faster-whisper 降级：词级时间戳按字数均分成字级。"""
    global _WHISPER
    try:
        from faster_whisper import WhisperModel
    except ImportError:
        return None
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


# ── 第 4 步：锚点 → 旁白字符时刻 → jieba 词 + 标点短语 ───────

def seg_words_and_phrases(narration, anchors):
    """旁白原文 + ASR 锚点 → (words, phrases, 匹配率)。文本严格来自旁白。

    匹配率 = 锚点字符直接命中的旁白字符占比；过低说明 ASR 听错太多
    （Kokoro 音质差时常见），调用方应换锚点源重算。
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
        span = (nxt_t[0] - prev_t[1]) / max(j - i, 1)
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
    proj = pathlib.Path(proj_dir)
    global SF_KEY
    SF_KEY = os.environ.get('SILICONFLOW_API_KEY', '')
    sb = json.load(open(proj / 'storyboards' / 'storyboard.json', encoding='utf-8'))
    (proj / 'audio').mkdir(exist_ok=True)

    # 1+2. 文本落盘 + TTS（先写全 txt 再出 wav，避免路径被当文本朗读的坑）
    for seg in sb['segments']:
        (proj / 'audio' / f"{seg['id']}.txt").write_text(seg['narration'], encoding='utf-8')
    pv = project_voice(proj)
    # 引擎决策（M-C TTS 付费档）：项目音色是 CosyVoice 且配了 key → 锁付费档；
    # 是 Edge 音色 → 锁 edge；未设音色 → 有 key 用付费档，无 key 免费 edge。
    # cosy 音色未配 key → 警告并回落 Edge 默认（别拿 'alex' 去调 edge-tts）。
    cosy = cosy_voice_id(pv) if pv else ''
    if cosy and SF_KEY:
        engine, sf_voice = 'siliconflow', cosy
    elif cosy:
        print('警告: voice 是 CosyVoice 音色但未配 SILICONFLOW_API_KEY → 回落 Edge 晓晓')
        engine, sf_voice, pv = 'edge', '', ''
    elif pv:
        engine, sf_voice = 'edge', ''
    else:
        engine, sf_voice = ('siliconflow', SF_VOICE) if SF_KEY else ('edge', '')
    edge_voice = pv or os.environ.get('EDGE_TTS_VOICE', 'zh-CN-XiaoxiaoNeural')

    # 项目音色切换 → 旧 wav 全部作废重配（audio_meta.json 的 voice 字段对不上即触发）
    meta_path = proj / 'audio_meta.json'
    prev_meta_voice = ''
    if meta_path.exists():
        try:
            prev_meta_voice = str(json.load(open(meta_path, encoding='utf-8')).get('voice') or '')
        except Exception:
            prev_meta_voice = ''
    meta_voice = pv or 'default'
    if prev_meta_voice and prev_meta_voice != meta_voice:
        removed = 0
        for w in (proj / 'audio').glob('*.wav'):
            w.unlink()
            removed += 1
        for s in (proj / 'audio').glob('*.wav.src'):
            s.unlink(missing_ok=True)
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
        if engine == 'siliconflow' and tts_siliconflow(seg['narration'], wav, sf_voice):
            print(seg['id'], f"wav ✓ (cosyvoice {sf_voice.split(':')[-1]})")
        elif tts_edge(proj / 'audio' / f"{seg['id']}.txt", wav, edge_voice):
            print(seg['id'], f"wav ✓ (edge {edge_voice})")
        elif tts_kokoro(proj / 'audio' / f"{seg['id']}.txt", wav):
            print(seg['id'], 'wav ✓ (kokoro)')
        else:
            return 1
        src.write_text(fp, encoding='utf-8')

    # 3+4. 对齐：ASR 锚点 + 旁白原文回写；匹配率过低换锚点源重算
    voices = []
    for seg in sb['segments']:
        sid = seg['id']
        wav = proj / 'audio' / f"{sid}.wav"
        anchors = anchor_funasr(wav)
        best = None
        if anchors is not None:
            words, phrases, ratio = seg_words_and_phrases(seg['narration'], anchors)
            best = (words, phrases, ratio, 'funasr')
            if ratio >= 0.5:
                pass  # funasr 锚点够用
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
                       'words': words, 'phrases': phrases})
        print(f'{sid}: {len(words)} 词 / {len(phrases)} 短语 ✓（锚点 {src}，匹配率 {ratio:.0%}）')

    # 原子替换：Go 侧（loadAudioMeta）与翻译回写并发读此文件，裸写会读到半截 JSON
    tmp = proj / 'audio_meta.json.tmp'
    with open(tmp, 'w', encoding='utf-8') as f:
        json.dump({'voices': voices, 'voice': meta_voice}, f, ensure_ascii=False, indent=1)
    os.replace(tmp, proj / 'audio_meta.json')
    print(f'audio_meta.json ✓（{len(voices)} 段，engine={engine}，voice={meta_voice}）')
    return 0


if __name__ == '__main__':
    sys.exit(main(sys.argv[1]))
