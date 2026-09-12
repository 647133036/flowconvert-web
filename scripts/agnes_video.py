#!/usr/bin/env python3
"""Agnes AI 视频生成流程：LLM 分析 → 分段生成 → 本地拼接。

流程：
  1. 调 Agnes 3.0 Flash（LLM chat）分析用户输入 → 专业视频 prompt
  2. 按时长切分（>12s 分段，每段 4-12s）
  3. 调 agnes-video-2.5-flash 逐段生成（段间末帧衔接，保持人物/场景一致）
  4. 本地 ffmpeg 归一化拼接

用法: agnes_video.py <payload_path> <dest>
payload JSON: {prompt, duration, aspect_ratio}
环境变量: AGNES_API_KEY, AGNES_BASE_URL(可选), AGNES_CHAT_MODEL(可选, 默认 agnes-3.0-flash)
"""

import sys
import json
import os
import time
import base64
import math
import re
import subprocess

import requests

AGNES_VIDEO_MODEL = "agnes-video-2.5-flash"
AGNES_CHAT_MODEL_DEFAULT = "agnes-3.0-flash"
SEG_DELAY = 12          # 段间间隔，规避 Agnes 6 req/min 限流
POLL_INTERVAL = 6       # 轮询间隔
POLL_TIMEOUT = 1800     # 单段轮询上限 30 分钟
QUEUE_RETRY = 10        # 503 队列满重试次数


def fail(msg):
    print(json.dumps({"error": msg}, ensure_ascii=False))
    sys.exit(1)


def env_or_die(name):
    v = os.environ.get(name, "")
    if not v:
        fail(f"环境变量 {name} 未配置，无法调用 Agnes")
    return v


def log(obj):
    print(json.dumps(obj, ensure_ascii=False), flush=True)


# ── Step 1: LLM 分析 ──

SYSTEM_PROMPT = """你是一位 AI 视频生成专家，擅长把用户的动作/舞蹈描述改写成文生视频模型能精确执行的 prompt。用户输入可能包含人物着装、环境背景、主题或舞名、整体风格、动作要诀等要素，也可能只是一段自由描述。

请将其分析、整合、扩写为一段适合 AI 文生视频模型理解的专业 prompt：
1. 输出一段连贯的中文描述，直接描绘画面与动作
2. 动作保真优先：用户对身体部位数量、左右手、姿势的指定必须逐字保留并强化，绝不泛化（例如"单手"绝不能写成或暗示成"双手"）
3. 抽象动作名必须转写为具体身体描述：说明是哪只手/哪条腿、抬到什么位置、呈什么形状；同时明确其余肢体的状态（如"左手始终自然下垂贴于身侧，不参与动作"），防止模型按常见姿势脑补
4. 舞蹈/动作类描述按节拍拆解为连续动作序列：起始姿势 → 动作轨迹 → 定格姿势，动作清晰可辨
5. 补充必要的视觉细节：光影、色调、构图；人物动作/舞蹈类用固定机位中近景，避免运镜干扰动作呈现
6. 外观与场景必须写成可锁定的具体细节：服装的款式、颜色、扣子/拉链/图案、发型、配饰，以及场景布置、色调——这些细节会被逐段复用贯穿全片，必须具体、唯一、不含糊（不要写"服装整洁"这类无法锁定的泛化描述）
7. 强调动作的连贯性与节奏推进，避免同一动作反复循环
8. 保留用户原意与核心要素，不偏离主题
9. 只输出 prompt 正文，不要解释、不要前后缀、不要引号、不要分点编号
10. 控制在 220 字以内

示例：用户输入"一个女孩单手比心"，输出类似：一位年轻女孩面向镜头微笑站立，柔和日光，浅色背景，固定机位中近景。她只抬起右手至脸颊右侧，拇指与食指交叉捏合比出一个小巧的心形，保持姿势轻微晃动；左手全程自然下垂贴于身侧，不参与任何动作。画面明亮清新，节奏轻快。"""


def agnes_chat(base, key, model, system, user):
    """调 Agnes chat/completions，返回生成的 prompt 文本。"""
    url = f"{base}/chat/completions"
    headers = {"Authorization": f"Bearer {key}", "Content-Type": "application/json"}
    body = {
        "model": model,
        "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": user},
        ],
    }
    for attempt in range(4):
        try:
            r = requests.post(url, headers=headers, json=body, timeout=180)
            if r.status_code == 429 or r.status_code >= 500:
                time.sleep(5 * (attempt + 1))
                continue
            if r.status_code >= 400:
                raise RuntimeError(f"Agnes LLM 分析失败: HTTP {r.status_code}: {r.text[:400]}")
            data = r.json()
            return data["choices"][0]["message"]["content"].strip()
        except (requests.RequestException, KeyError, ValueError) as e:
            if attempt == 3:
                raise RuntimeError(f"Agnes LLM 分析失败: {e}")
            time.sleep(5 * (attempt + 1))
    raise RuntimeError("Agnes LLM 分析失败")


def image_to_data_uri(url):
    """下载图片转 base64 data URI（供 LLM chat 多模态输入，绕过 chat 接口对 HTTP URL 的限制）。"""
    r = requests.get(url, timeout=90)
    r.raise_for_status()
    ct = r.headers.get("Content-Type", "image/png").split(";")[0].strip()
    b64 = base64.b64encode(r.content).decode()
    return f"data:{ct};base64,{b64}"


def image_to_base64(url):
    """下载图片转纯 base64（供 Agnes 视频接口 first_frame/last_frame，接口要求 base64 data 而非 data URI）。"""
    r = requests.get(url, timeout=90)
    r.raise_for_status()
    return base64.b64encode(r.content).decode()


AGNES_IMAGE_MODEL = os.environ.get("AGNES_IMAGE_MODEL", "agnes-image-2.5-flash")
WATERMARK_PROMPT = (
    "请移除图片上所有的水印、台标、logo、字幕或文字标识，"
    "对移除区域做自然修复，保持画面其余内容、构图、人物、服装、场景与色调完全不变。"
    "如果图中没有水印，则原样保持画面不变。"
)


def fetch_image_bytes(url):
    """下载图片，返回 (原始字节, content_type)。"""
    r = requests.get(url, timeout=90)
    r.raise_for_status()
    ct = r.headers.get("Content-Type", "image/png").split(";")[0].strip()
    return r.content, ct


def bytes_to_data_uri(b, ct):
    """图片字节 → data URI（供 LLM chat 多模态输入与 Agnes 图片编辑 image 输入）。"""
    return f"data:{ct};base64,{base64.b64encode(b).decode()}"


def agnes_image_edit(base, key, img_bytes, ct, prompt=WATERMARK_PROMPT):
    """调 Agnes 图片编辑 API 去除水印，返回处理后的图片字节。"""
    url = f"{base}/images/generations"
    headers = {"Authorization": f"Bearer {key}", "Content-Type": "application/json"}
    body = {
        "model": AGNES_IMAGE_MODEL,
        "prompt": prompt,
        "size": "1K",
        "extra_body": {"response_format": "url", "image": [bytes_to_data_uri(img_bytes, ct)]},
    }
    for attempt in range(3):
        try:
            r = requests.post(url, headers=headers, json=body, timeout=180)
            if r.status_code in (429, 500, 502, 503, 504) and attempt < 2:
                time.sleep(3 * (attempt + 1))
                continue
            r.raise_for_status()
            data = r.json()
            if data.get("error"):
                raise RuntimeError(f"Agnes图片API错误: {data['error'].get('message', data['error'])}")
            if not data.get("data"):
                raise RuntimeError("Agnes图片API返回空数据")
            d = data["data"][0]
            if d.get("b64_json"):
                return base64.b64decode(d["b64_json"])
            if d.get("url"):
                rr = requests.get(d["url"], timeout=90)
                rr.raise_for_status()
                return rr.content
            raise RuntimeError("Agnes图片API未返回图片")
        except Exception as e:
            if attempt < 2:
                time.sleep(2)
                continue
            log({"stage": "image_edit_failed", "error": str(e)})
            return img_bytes
    return img_bytes


# ── Step 2: 切分 ──

def split_duration(total):
    """按时长切分，每段 4-12s。"""
    if total <= 4:
        return [4]
    if total <= 12:
        return [total]
    n = (total + 11) // 12
    base_d = total // n
    rem = total % n
    return [base_d + (1 if i < rem else 0) for i in range(n)]


def clamp_seconds(d):
    if d < 4:
        return 4
    if d > 12:
        return 12
    return d


def split_clauses(prompt):
    """按中英文标点切分自包含短句。"""
    parts = re.split(r"[，。！？、；,!?;]", prompt)
    return [p.strip() for p in parts if p.strip()]


def segment_prompt(prompt, i, n):
    """为第 i 段构造聚焦 prompt（复用 Go segmentStagePrompt 思路）。"""
    clauses = split_clauses(prompt)
    focus = clauses[i % len(clauses)] if clauses else prompt
    if i == 0:
        stage = "故事开端"
    elif i == n - 1:
        stage = "故事结尾"
    else:
        stage = f"第{i + 1}阶段"
    base = f"{prompt}。本段聚焦：{focus}。叙事：{stage}"
    if i == 0:
        return base + "。画面以一个明确动作开场并自然推进发展，运镜平稳，避免同一动作反复循环"
    return base + "。严格延续上一段画面：保持同一人物、同一服装、同一场景、同一光影与同一色调，动作自然接续并持续推进、避免反复循环，不要重新生成新场景"


# ── Step 2b: 多模态分析（首尾帧 / 参考图）──

KEYFRAME_SYS = """你是 AI 视频导演。我会给你首帧和尾帧两张图片（顺序：首帧在前，尾帧在后），以及用户期望的过渡主题。

请完成三件事：
1. 判断每张图是否有水印/logo/文字标识，输出 watermark 布尔数组，顺序与输入图一致。
2. 从首帧提取一份固定外观设定 setting：人物服装的款式、颜色、扣子/拉链/图案、发型、配饰，以及场景布置、色调、光影——必须具体到可逐字锁定（不写"服装整洁"这类泛化描述）。
3. 识别首帧和尾帧的画面内容（人物着装、场景、色调、构图），设计从首帧自然过渡到尾帧的连续叙事，分成 {n} 段：第 1 段以首帧为起点，第 {n} 段以尾帧为收尾，中间段设计自然过渡动作与场景推进，保持人物与色调连贯。

动作保真：过渡主题中对身体部位数量、左右手、姿势的指定必须逐字保留并强化，绝不泛化（如"单手"不能变成"双手"）；抽象动作名转写为具体身体描述（哪只手、抬到什么位置、呈什么形状），并明确其余肢体状态。

只输出一个 JSON 对象（不要 markdown 代码块、不要解释）：
{{"watermark":[false,true],"setting":"固定外观设定，60字以内","narrative":["第1段画面描述...","第{n}段画面描述..."]}}
narrative 为 {n} 个元素，每个是一段中文视频画面描述（不超过120字，含动作、运镜、光影、色调；不要重复 setting 内容，代码会自动拼接）。"""

REF_SYS = """你是 AI 视频导演。我会给你若干参考图，以及用户期望的主题。参考图仅作为视觉内容参考，不逐帧对应。

请完成三件事：
1. 判断每张图是否有水印/logo/文字标识，输出 watermark 布尔数组，顺序与输入图一致。
2. 从参考图提取一份固定外观设定 setting：人物服装的款式、颜色、扣子/拉链/图案、发型、配饰，以及场景布置、色调、光影——必须具体到可逐字锁定（不写"服装整洁"这类泛化描述）。
3. 识别每张图的画面内容（人物着装、场景、色调、构图），把参考图内容作为视觉参考，设计 {n} 段连贯视频叙事：第 1 段开场，第 {n} 段收尾，中间段自然推进，整体风格与参考图保持一致。

动作保真：主题描述中对身体部位数量、左右手、姿势的指定必须逐字保留并强化，绝不泛化（如"单手"不能变成"双手"）；抽象动作名转写为具体身体描述（哪只手、抬到什么位置、呈什么形状），并明确其余肢体状态。

只输出一个 JSON 对象（不要 markdown 代码块、不要解释）：
{{"watermark":[false,false,true],"setting":"固定外观设定，60字以内","narrative":["第1段画面描述...","第{n}段画面描述..."]}}
narrative 为 {n} 个元素，每个是一段中文视频画面描述（不超过120字，含动作、运镜、光影；不要重复 setting 内容，代码会自动拼接）。"""


def parse_segment_prompts(text, n):
    """宽松解析 LLM 输出的分段 prompt 列表。"""
    text = (text or "").strip()
    try:
        arr = json.loads(text)
        if isinstance(arr, list) and arr:
            out = [str(x).strip() for x in arr if str(x).strip()]
            if out:
                return (out + [out[-1]] * n)[:n]
    except Exception:
        pass
    m = re.findall(r'["“](.*?)["”]', text, re.S)
    if len(m) >= n:
        return [x.strip() for x in m[:n]]
    if m:
        return ([x.strip() for x in m] + [m[-1].strip()] * n)[:n]
    lines = [re.sub(r'^[\s\-*\d.、）)]+', '', l).strip() for l in text.splitlines() if l.strip()]
    lines = [l for l in lines if l]
    if len(lines) >= n:
        return lines[:n]
    if lines:
        return (lines + [lines[-1]] * n)[:n]
    return None


def parse_analysis(text, n, m, fallback_prompt):
    """解析 LLM 组合输出 → (narrative[N], watermark[M], setting)。失败给默认值。"""
    text = (text or "").strip()
    narrative = None
    watermark = [False] * m
    setting = ""
    obj = None
    try:
        obj = json.loads(text)
    except Exception:
        mo = re.search(r"\{.*\}", text, re.S)
        if mo:
            try:
                obj = json.loads(mo.group(0))
            except Exception:
                obj = None
    if isinstance(obj, dict):
        narr = obj.get("narrative")
        if isinstance(narr, list) and narr:
            out = [str(x).strip() for x in narr if str(x).strip()]
            if out:
                narrative = (out + [out[-1]] * n)[:n]
        st = obj.get("setting")
        if isinstance(st, str) and st.strip():
            setting = st.strip()
        wm = obj.get("watermark")
        if isinstance(wm, list):
            parsed = []
            for x in wm:
                if isinstance(x, bool):
                    parsed.append(x)
                elif str(x).strip().lower() in ("true", "yes", "false", "no"):
                    parsed.append(str(x).strip().lower() in ("true", "yes"))
            if parsed:
                watermark = (parsed + [False] * m)[:m]
    if narrative is None:
        narrative = parse_segment_prompts(text, n)
    if narrative is None:
        narrative = [fallback_prompt] * n
    return narrative, watermark, setting


def _analyze_with_dewater(base, key, chat_model, sys_prompt, user_text, image_urls, n, fallback_prompt):
    """共享流程：下载图 → 多模态 LLM(内容+水印标注) → 有水印的调 Agnes 图片编辑去水印。

    返回 (narrative, watermark_flags, cleaned_b64_list)，cleaned_b64 与 image_urls 一一对应。
    """
    raws = []
    for u in image_urls:
        b, ct = fetch_image_bytes(u)
        raws.append((b, ct))
    data_uris = [bytes_to_data_uri(b, ct) for b, ct in raws]
    content = [{"type": "text", "text": user_text}]
    for du in data_uris:
        content.append({"type": "image_url", "image_url": {"url": du}})
    raw = agnes_chat(base, key, chat_model, sys_prompt, content)
    narrative, watermark, setting = parse_analysis(raw, n, len(raws), fallback_prompt)
    cleaned = []
    for i, (b, ct) in enumerate(raws):
        has_wm = watermark[i] if i < len(watermark) else False
        if has_wm:
            cb = agnes_image_edit(base, key, b, ct)
            log({"stage": "dewater", "image": i})
            cleaned.append(cb)
        else:
            cleaned.append(b)
    return narrative, watermark, [base64.b64encode(c).decode() for c in cleaned], setting


def analyze_keyframe(base, key, chat_model, first_url, last_url, user_prompt, n):
    """下载首尾帧 → LLM(内容+水印+外观设定) → 去水印 → 返回 (n段叙事, [首帧b64, 尾帧b64], setting)。"""
    images = [first_url, last_url]
    sys_prompt = KEYFRAME_SYS.format(n=n)
    user_text = f"过渡主题/描述：{user_prompt}\n共 2 张图（首帧在前、尾帧在后），请输出 {n} 段叙事并标注每张图是否有水印。"
    narrative, wm, cleaned_b64, setting = _analyze_with_dewater(
        base, key, chat_model, sys_prompt, user_text, images, n, user_prompt or "自然过渡")
    log({"stage": "analyzed", "prompts": len(narrative), "watermark": wm, "setting": setting[:60], "sample": narrative[0][:120]})
    return narrative, cleaned_b64, setting


def analyze_ref(base, key, chat_model, image_urls, user_prompt, n):
    """下载参考图 → LLM(内容+水印+外观设定) → 去水印 → 返回 (n段叙事, [清洗后b64], setting)。"""
    m = len(image_urls)
    sys_prompt = REF_SYS.format(n=n)
    user_text = f"主题/描述：{user_prompt}\n共 {m} 张参考图，请输出 {n} 段叙事并标注每张图是否有水印。"
    narrative, wm, cleaned_b64, setting = _analyze_with_dewater(
        base, key, chat_model, sys_prompt, user_text, image_urls, n, user_prompt or "连贯叙事")
    log({"stage": "analyzed", "prompts": len(narrative), "watermark": wm, "setting": setting[:60], "sample": narrative[0][:120]})
    return narrative, cleaned_b64, setting


# ── Step 3: Agnes 视频任务 ──

def create_video_task(base, key, params):
    """提交 Agnes 视频任务，返回 video_id。"""
    url = f"{base}/videos"
    headers = {"Authorization": f"Bearer {key}", "Content-Type": "application/json"}
    body = {
        "model": AGNES_VIDEO_MODEL,
        "prompt": params["prompt"],
        "mode": params.get("mode", "text"),
        "size": "720P",
        "seconds": str(params.get("seconds", 5)),
    }
    if params.get("aspect_ratio"):
        body["aspect_ratio"] = params["aspect_ratio"]
    if params.get("mode") == "keyframe":
        if params.get("first_frame"):
            body["first_frame"] = params["first_frame"]
        if params.get("last_frame"):
            body["last_frame"] = params["last_frame"]
    if params.get("mode") == "reference" and params.get("images"):
        body["images"] = params["images"]
    for attempt in range(QUEUE_RETRY):
        try:
            r = requests.post(url, headers=headers, json=body, timeout=60)
            if r.status_code == 503 and "video_queue_full" in r.text:
                backoff = min(10 * (attempt + 1), 300)
                time.sleep(backoff)
                continue
            if r.status_code == 429:
                time.sleep(15 * (attempt + 1))
                continue
            if r.status_code >= 400:
                raise RuntimeError(f"提交视频任务失败: HTTP {r.status_code}: {r.text[:400]}")
            data = r.json()
            return data.get("id") or data.get("video_id") or data.get("task_id")
        except requests.RequestException as e:
            if attempt == QUEUE_RETRY - 1:
                raise RuntimeError(f"提交视频任务失败: {e}")
            time.sleep(5 * (attempt + 1))
    raise RuntimeError("提交视频任务失败：队列持续拥塞")


def poll_video_task(base, key, video_id):
    """轮询视频任务直到完成，返回视频 URL。"""
    url = f"{base}/agnesapi?video_id={requests.utils.quote(video_id)}&model_name={AGNES_VIDEO_MODEL}"
    headers = {"Authorization": f"Bearer {key}"}
    deadline = time.time() + POLL_TIMEOUT
    while time.time() < deadline:
        try:
            r = requests.get(url, headers=headers, timeout=60)
            if r.status_code == 200:
                data = r.json()
                status = data.get("status")
                if status == "completed" or data.get("progress", 0) >= 100:
                    v = data.get("url") or data.get("metadata", {}).get("url", "")
                    if v:
                        return v
                if status == "failed":
                    msg = (data.get("error") or {}).get("message", "视频生成失败")
                    raise RuntimeError(msg)
            elif r.status_code == 429:
                time.sleep(20)
                continue
        except requests.RequestException:
            pass
        time.sleep(POLL_INTERVAL)
    raise RuntimeError("视频生成超时")


def download_video(url, dest):
    r = requests.get(url, timeout=300, stream=True)
    r.raise_for_status()
    with open(dest, "wb") as f:
        for chunk in r.iter_content(8192):
            f.write(chunk)


# ── 末帧衔接 ──

def extract_last_frame(src, dest):
    """用 ffmpeg 提取视频末帧为 JPEG，返回 base64 data URI。"""
    cmd = ["ffmpeg", "-y", "-v", "error", "-sseof", "-0.2", "-i", src,
           "-frames:v", "1", "-q:v", "3", dest]
    subprocess.run(cmd, check=True, capture_output=True, timeout=60)
    if not os.path.exists(dest):
        raise RuntimeError("提取末帧失败")
    with open(dest, "rb") as f:
        b64 = base64.b64encode(f.read()).decode()
    return b64


# ── Step 4: 拼接 ──

def _ffmpeg_version_major():
    try:
        out = subprocess.run(["ffmpeg", "-version"], capture_output=True, text=True, timeout=15).stdout
        m = re.search(r"ffmpeg version (\d+)\.", out)
        if m:
            return int(m.group(1))
    except Exception:
        pass
    return 5


_CFR_ARGS = None


def cfr_args():
    """ffmpeg 5.0+ 用 -fps_mode，旧版用 -vsync。"""
    global _CFR_ARGS
    if _CFR_ARGS is None:
        _CFR_ARGS = ["-fps_mode", "cfr"] if _ffmpeg_version_major() >= 5 else ["-vsync", "cfr"]
    return _CFR_ARGS


def probe_resolution(path):
    cmd = ["ffprobe", "-v", "error", "-select_streams", "v:0",
           "-show_entries", "stream=width,height", "-of", "json", path]
    out = subprocess.run(cmd, capture_output=True, text=True, timeout=30).stdout
    d = json.loads(out)
    s = d["streams"][0]
    return int(s["width"]), int(s["height"])


def probe_fps(path):
    cmd = ["ffprobe", "-v", "error", "-select_streams", "v:0",
           "-show_entries", "stream=r_frame_rate", "-of", "csv=p=0", path]
    out = subprocess.run(cmd, capture_output=True, text=True, timeout=30).stdout.strip()
    if not out or out == "0/0":
        return 30
    try:
        num, den = out.split("/")
        den = float(den)
        if den == 0:
            return 30
        r = float(num) / den
        return int(math.ceil(r)) if r >= 1 else 30
    except Exception:
        return 30


def concat_segments(tmp_dir, seg_paths, dest):
    """归一化每段分辨率/帧率/时间戳后 concat 拼接。"""
    w, h = probe_resolution(seg_paths[0])
    fps = probe_fps(seg_paths[0])
    vf = f"scale={w}:{h},setsar=1"
    norm_paths = []
    for i, p in enumerate(seg_paths):
        norm = os.path.join(tmp_dir, f"norm_{i:03d}.mp4")
        cmd = ["ffmpeg", "-y", "-i", p, "-vf", vf, "-r", str(fps)] + cfr_args() + [
            "-c:v", "libx264", "-preset", "fast", "-crf", "23", "-pix_fmt", "yuv420p",
            "-c:a", "aac", "-start_time", "0", norm]
        subprocess.run(cmd, check=True, capture_output=True, timeout=300)
        norm_paths.append(norm)
    list_path = os.path.join(tmp_dir, "concat_list.txt")
    with open(list_path, "w") as f:
        for p in norm_paths:
            f.write(f"file '{os.path.abspath(p)}'\n")
    cmd = ["ffmpeg", "-y", "-fflags", "+genpts", "-f", "concat", "-safe", "0",
           "-i", list_path, "-vf", vf, "-r", str(fps)] + cfr_args() + [
           "-c:v", "libx264", "-preset", "fast", "-crf", "23", "-pix_fmt", "yuv420p",
           "-start_time", "0", "-c:a", "aac", "-movflags", "+faststart", dest]
    subprocess.run(cmd, check=True, capture_output=True, timeout=600)
    if not os.path.exists(dest):
        raise RuntimeError("拼接输出文件不存在")


def main():
    if len(sys.argv) < 3:
        fail("参数不足，用法: agnes_video.py <payload> <dest>")
    payload_path = sys.argv[1]
    dest = sys.argv[2]
    with open(payload_path, "r", encoding="utf-8") as f:
        payload = json.load(f)

    base = os.environ.get("AGNES_BASE_URL", "https://apihub.agnes-ai.cn/v1")
    key = env_or_die("AGNES_API_KEY")
    chat_model = os.environ.get("AGNES_CHAT_MODEL", AGNES_CHAT_MODEL_DEFAULT)

    user_prompt = (payload.get("prompt") or "").strip()
    duration = int(payload.get("duration") or 5)
    if duration <= 0:
        duration = 5
    if duration > 120:
        duration = 120
    aspect = payload.get("aspect_ratio") or "16:9"
    mode = payload.get("mode", "text")

    first_url = (payload.get("first_frame") or "").strip()
    last_url = (payload.get("last_frame") or "").strip()
    image_urls = [str(u).strip() for u in (payload.get("images") or []) if str(u).strip()]

    if mode == "keyframe" and (not first_url or not last_url):
        fail("首尾帧模式需提供 first_frame 和 last_frame")
    if mode == "ref" and not image_urls:
        fail("参考图模式需提供 images")

    tmp_dir = os.path.dirname(os.path.abspath(dest)) or "."
    os.makedirs(tmp_dir, exist_ok=True)

    segs = split_duration(duration)
    n = len(segs)

    # Step 1: LLM 分析
    kf_clean = []
    ref_clean = []
    setting = ""
    if mode == "keyframe":
        log({"stage": "analyze", "msg": "正在用 Agnes 3.0 Flash 识别首尾帧 + 设计叙事..."})
        try:
            seg_prompts, kf_clean, setting = analyze_keyframe(base, key, chat_model, first_url, last_url, user_prompt or "自然过渡", n)
        except RuntimeError as e:
            fail(str(e))
    elif mode == "ref":
        log({"stage": "analyze", "msg": f"正在用 Agnes 3.0 Flash 识别 {len(image_urls)} 张参考图..."})
        try:
            seg_prompts, ref_clean, setting = analyze_ref(base, key, chat_model, image_urls, user_prompt or "连贯叙事", n)
        except RuntimeError as e:
            fail(str(e))
    else:
        if not user_prompt:
            fail("请输入提示词")
        log({"stage": "analyze", "msg": "正在用 Agnes 3.0 Flash 分析输入..."})
        try:
            enhanced = agnes_chat(base, key, chat_model, SYSTEM_PROMPT, user_prompt)
        except RuntimeError as e:
            fail(str(e))
        if not enhanced:
            enhanced = user_prompt
        seg_prompts = [segment_prompt(enhanced, i, n) for i in range(n)]
        log({"stage": "analyzed", "prompt": enhanced[:200]})

    # 外观设定锁定：同一份 setting 逐字拼进每段 prompt，全片复用，
    # 防止分段之间服装/场景细节漂移（如第一段有扣子、下一段没了）
    if setting:
        seg_prompts = [f"{setting}。{p}" for p in seg_prompts]

    log({"stage": "split", "segments": n, "durations": segs, "mode": mode})

    # Step 2: 分段生成（keyframe 首尾帧锚定 + 末帧衔接；ref reference 模式；text 末帧衔接）
    ref_imgs = ref_clean[:5]
    seg_paths = []
    last_frame_uri = None
    for i, seg_dur in enumerate(segs):
        if i > 0:
            time.sleep(SEG_DELAY)
        prompt_i = seg_prompts[i] if i < len(seg_prompts) else (seg_prompts[-1] if seg_prompts else user_prompt)
        params = {
            "prompt": prompt_i,
            "mode": "keyframe" if mode in ("keyframe", "ref") else "text",
            "seconds": clamp_seconds(seg_dur),
            "aspect_ratio": aspect,
        }
        if mode == "keyframe":
            if i == 0:
                params["first_frame"] = kf_clean[0] if kf_clean else image_to_base64(first_url)
            elif last_frame_uri:
                params["first_frame"] = last_frame_uri
            else:
                params["mode"] = "text"
            if i == n - 1:
                params["last_frame"] = kf_clean[1] if len(kf_clean) > 1 else image_to_base64(last_url)
        elif mode == "ref":
            # 参考图不占帧：纯 reference 模式，参考图作内容参考，每段共用全部参考图
            params["mode"] = "reference"
            params["images"] = ref_imgs
            cont = "严格延续上一段画面，保持同一人物、同一服装、同一场景、同一光影与色调。" if i > 0 else ""
            params["prompt"] = f"Use <Picture 1> as reference. {prompt_i}{cont}"
        else:
            if i > 0 and last_frame_uri:
                params["mode"] = "keyframe"
                params["first_frame"] = last_frame_uri
        log({"stage": "generate", "seg": i + 1, "total": n, "seconds": params["seconds"]})
        try:
            vid = create_video_task(base, key, params)
            vurl = poll_video_task(base, key, vid)
            seg_dest = os.path.join(tmp_dir, f"seg_{i:03d}.mp4")
            download_video(vurl, seg_dest)
            seg_paths.append(seg_dest)
            if i < n - 1:
                try:
                    ff = os.path.join(tmp_dir, f"frame_{i:03d}.jpg")
                    last_frame_uri = extract_last_frame(seg_dest, ff)
                except Exception as e:
                    log({"stage": "warn", "msg": f"提取末帧失败，下段退化为文生视频: {e}"})
                    last_frame_uri = None
        except Exception as e:
            log({"stage": "warn", "msg": f"第{i + 1}段失败: {e}"})
            last_frame_uri = None
            continue

    if not seg_paths:
        fail("所有分段生成失败")

    # Step 3: 拼接
    if len(seg_paths) == 1:
        os.replace(seg_paths[0], dest)
    else:
        try:
            concat_segments(tmp_dir, seg_paths, dest)
        except subprocess.CalledProcessError as e:
            err = e.stderr.decode(errors="replace")[-500:] if e.stderr else ""
            fail(f"视频拼接失败: {err}")
    log({"status": "ok", "segments": len(seg_paths)})


if __name__ == "__main__":
    main()
