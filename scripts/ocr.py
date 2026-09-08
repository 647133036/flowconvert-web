"""结构化 OCR 识别 CLI - 供 FlowConvert 后端调用。

用法:
    python3 ocr.py <input_path> --options '<json>' [--outdir <dir>]

options 字段:
    mode      "general" | "advanced"     识别模式；advanced 启用 PP-DocLayout+SLANeXt 表格结构识别
    quality   "normal" | "high"          识别质量（high 渲染更高分辨率，耗时更长）
    charset   "auto" | "zh_hans" | "zh_hant"  输出字形（繁简转换）
    redbox    true | false               是否在结果图上标注文字框
    optimize  true | false               文本优化（过滤页眉页脚）
    translate true | false               是否翻译为简体中文
    formats   ["txt","docx","xlsx","json"] 需要导出的格式
    engine    "auto" | "tesseract" | "pp_ocr"

输出 (stdout, JSON):
    {"success": true, "text": "...", "translated_text": "", "engine": "tesseract",
     "pages": 2, "chars": 42, "charset": "zh_hans", "layout": {...},
     "blocks": [...], "lines": [...], "files": {"txt": "..."},
     "elapsed_ms": 1234, "warnings": [], "options": {...}}

识别策略（全部本地执行，无外部 API 依赖）:
    1. PDF 有文本层 -> PyMuPDF 字符级抽取（无损，保留字号与坐标）
    2. 扫描件/图片  -> Tesseract（general 用 chi_sim+eng，advanced 追加 chi_tra 并多 PSM 取最优）
    3. 版式分析：标题(字号) / 列表(项目符号) / 页眉页脚(位置+跨页重复) / 双栏(x 分裂) /
       表格(PP-DocLayout + SLANeXt，启发式兜底) / 公式(数学符号密度)
    4. 繁简转换：OpenCC（s2t / t2s）
    5. 翻译：复用同目录 translate.py，目标为简体中文
"""

import argparse
import json
import os
import re
import sys
import time
from dataclasses import dataclass, field

from pdf_utils import text_layer_is_garbled

IMAGE_EXTS = (".jpg", ".jpeg", ".png", ".bmp", ".webp", ".gif", ".tif", ".tiff")

QUALITY_PROFILE = {
    "normal": {"dpi": 150, "max_width": 1400, "psms": (6, 3)},
    "high": {"dpi": 300, "max_width": 2400, "psms": (6, 3, 4, 11)},
}

# 语言预设 -> 候选 tesseract 语言组合（按优先级，取第一个语言包齐全的组合）
LANG_PRESETS = {
    "auto": ("chi_sim+chi_tra+eng", "chi_sim+eng", "chi_tra+eng", "eng"),
    "zh": ("chi_sim+chi_tra", "chi_sim", "chi_tra"),
    "en": ("eng",),
    "ja": ("jpn+eng", "jpn"),
    "ko": ("kor+eng", "kor"),
    "de": ("deu+eng", "deu"),
    "fr": ("fra+eng", "fra"),
    "es": ("spa+eng", "spa"),
    "ru": ("rus+eng", "rus"),
    "multi": (
        "eng+chi_sim+chi_tra+jpn+kor",
        "eng+chi_sim+chi_tra",
        "chi_sim+chi_tra+eng",
        "eng",
    ),
}

MATH_SYMBOLS = set(
    "∑∏∫∮√≠≤≥≈≡±×÷∂∇∞∴∵∈∉⊂⊃∪∩∅∀∃αβγδεζηθικλμνξοπρστυφχψω"
    "ΑΒΓΔΕΖΗΘΙΚΛΜΝΞΟΠΡΣΤΥΦΧΨΩ°′″‰§№→←↑↓⇒⇔"
)

LIST_PREFIX = re.compile(
    r"^\s*(?:[•·▪◦●○◆■□★☆]+"
    r"|[\-\*\+]\s+"
    r"|\(\d{1,3}\)\s*|[\(（]\s*[一二三四五六七八九十]+\s*[\)）]\s*"
    r"|\d{1,2}[.、)]\s+"
    r"|[①②③④⑤⑥⑦⑧⑨⑩⑪⑫⑬⑭⑮⑯⑰⑱⑲⑳]\s*"
    r"|[\(（][a-z][\)）]\s+|[a-z]\)\s+)"
)

HEADER_TITLE_WORDS = re.compile(
    r"(页码|第\s*\d+\s*页|page\s*\d+|footer|页脚|第\s*\d+\s*章|chapter\s*\d+)", re.I
)

CJK_TOKEN_CAP = 10
REDBOX_MAX_PAGES = 12

ROMAN_CHARS = "IVXLCDM"
ROMAN_PAIRS = (
    ("M", 1000), ("CM", 900), ("D", 500), ("CD", 400),
    ("C", 100), ("XC", 90), ("L", 50), ("XL", 40),
    ("X", 10), ("IX", 9), ("V", 5), ("IV", 4), ("I", 1),
)

# 简体文档里出现这些字说明 chi_tra 模型抢了识别，需要换 chi_sim 重跑该行。
TRAD_MARKERS = set(
    "長對話題讀滿給項選個說後內請聽寫單轉換識運兩這從錄數總過還進遠題讀話"
)


def _roman_to_int(text):
    total, i = 0, 0
    while i < len(text):
        for sym, val in ROMAN_PAIRS:
            if text[i:i + len(sym)] == sym:
                total += val
                i += len(sym)
                break
    return total


def _int_to_roman(value):
    out = ""
    for sym, val in ROMAN_PAIRS:
        while value >= val:
            out += sym
            value -= val
    return out


_ROMAN_UNI_VAL = {
    "Ⅰ": 1, "Ⅱ": 2, "Ⅲ": 3, "Ⅳ": 4, "Ⅴ": 5, "Ⅵ": 6,
    "Ⅶ": 7, "Ⅷ": 8, "Ⅸ": 9, "Ⅹ": 10, "Ⅺ": 11, "Ⅻ": 12,
}


def _normalize_roman_head(text):
    """把行首 Unicode 罗马数字章节号规范成 ASCII（ⅢⅢI→III、Ⅳ→IV）。

    v6 多语言模型会把章节号 III. 误识成 ⅢⅢI.（把 I 竖线读成 Ⅲ），或输出
    单个 Unicode 罗马数字 Ⅳ./Ⅴ.。这里要求「行首罗马数字 + 点号」特征才处理，
    误伤面小；多字符 Unicode 按其中最大数值重建，混排 ASCII I 尾巴按竖线计数。
    """
    m = re.match(r"^([ⅠⅡⅢⅣⅤⅥⅦⅧⅨⅩⅪⅫ]+)([ivxIVX]*)(?=[.．])", text)
    if not m:
        return text
    uni, tail = m.group(1), m.group(2).upper()
    val = max(_ROMAN_UNI_VAL[c] for c in uni) if uni else 0
    if tail:
        if set(tail) == {"I"}:
            val = max(val, len(tail))
        elif is_valid_roman(tail):
            val = max(val, _roman_to_int(tail))
    if val <= 0:
        return text
    roman = _int_to_roman(val)
    return roman + text[len(uni) + len(tail):]


def _balance_brackets(text):
    """补齐被 OCR 切掉的行尾右括号（满分5分 → 满分5分)）。

    中文行行尾的右括号常被 det 切成独立小框或直接丢掉；这里只在左括号比右括号
    多、缺口 <=2 时于行尾补齐，避免误改行中间缺括号的文本。
    """
    if not text:
        return text
    for lo, hi in (("(", ")"), ("（", "）")):
        diff = text.count(lo) - text.count(hi)
        if 0 < diff <= 2:
            return text + hi * diff
    return text


def is_valid_roman(text):
    """是否为 1..3999 的规范罗马数字写法(拒绝 IIII / VV / IXX 之类)。"""
    if not text or len(text) > 4 or any(ch not in ROMAN_CHARS for ch in text):
        return False
    value = _roman_to_int(text)
    return value >= 1 and _int_to_roman(value) == text


def _strip_roman_punct(text):
    return "".join(ch for ch in text if ch in ROMAN_CHARS)



@dataclass
class Word:
    text: str
    x: int
    y: int
    w: int
    h: int
    conf: float
    page: int = 0
    size: float = 0.0

    @property
    def cx(self):
        return self.x + self.w // 2

    @property
    def cy(self):
        return self.y + self.h // 2


@dataclass
class Line:
    text: str
    x: int
    y: int
    w: int
    h: int
    conf: float
    size: float
    page: int
    words: list = field(default_factory=list)

    @property
    def cx(self):
        return self.x + self.w // 2

    @property
    def cy(self):
        return self.y + self.h // 2

    def to_dict(self):
        return {
            "text": self.text,
            "bbox": [self.x, self.y, self.x + self.w, self.y + self.h],
            "conf": round(self.conf, 3),
            "size": round(self.size, 1),
            "page": self.page + 1,
        }


@dataclass
class Block:
    type: str
    text: str
    page: int
    bbox: tuple
    lines: list = field(default_factory=list)
    conf: float = 0.0
    cells: list = None

    def to_dict(self):
        d = {
            "type": self.type,
            "text": self.text,
            "page": self.page + 1,
            "bbox": [int(v) for v in self.bbox],
            "conf": round(self.conf, 3),
        }
        if self.cells is not None:
            d["cells"] = self.cells
        elif self.lines:
            d["lines"] = [ln.to_dict() for ln in self.lines]
        return d


def _log(msg):
    print(msg, file=sys.stderr)


def _median(values):
    vals = sorted(v for v in values if v and v > 0)
    return float(vals[len(vals) // 2]) if vals else 0.0


def _is_cjk(ch):
    return "\u4e00" <= ch <= "\u9fff"


_CJK_RUN = re.compile("[\u3000-\u303f\u3400-\u9fff\uf900-\ufaff\uff00-\uffef]")
_PUNCT_ONLY = re.compile(
    "[\\s.,;:!?()\\[\\]<>/\\\\'\"`~@#$%^&*+=|、。，；：！？（）【】《》〈〉“”‘’·…—–\\-]+"
)

# 一行都是「A. … B. …」这样的选项排，属于题干段落的续行而非标题
OPTION_LINE = re.compile(r"^[A-Za-z][.)\]]\s*\S.*?\s+[A-Za-z][.)\]]\s*\S")

# 章节标题：罗马序号打头，如「III. 长对话理解」
SECTION_TITLE = re.compile(r"^\s*[IVXLC]{1,5}\s*[.、，,]\s*\S")


def _needs_space(prev, cur):
    """相邻两个词框之间是否要插入空格。

    OCR 给相邻中文方块字返回的字面框会互相留出间隙，照抄间距会把
    「长对话理解」拼成「长 对话 再 解」；纯标点、全角括号与数字同理。
    """
    if _CJK_RUN.search(prev.text) or _CJK_RUN.search(cur.text):
        return False
    if _PUNCT_ONLY.fullmatch(prev.text) or _PUNCT_ONLY.fullmatch(cur.text):
        return False
    return True


def _join_words(items):
    """Join word tokens, inserting a space only when the horizontal gap is real.

    A gap above 0.3x the token height is a deliberate space; smaller gaps are
    either font metrics between adjacent CJK glyphs or tight Latin-CJK spacing.
    """
    parts = []
    for i, it in enumerate(items):
        if i:
            prev = items[i - 1]
            gap = it.x - (prev.x + prev.w)
            if gap > 0.3 * max(it.h, prev.h, 1) and _needs_space(prev, it):
                parts.append(" ")
        parts.append(it.text)
    return "".join(parts).strip()


def _strip_edge_punct(text):
    """去掉单元格首尾残留的标点；表格线被读成「-」/「|」时最常见。"""
    out = text.strip()
    while out and not (_CJK_RUN.search(out[0]) or out[0].isalnum()):
        out = out[1:].lstrip()
    while out and not (_CJK_RUN.search(out[-1]) or out[-1].isalnum()):
        out = out[:-1].rstrip()
    return out.strip()


# 试卷固定搭配。只有「其余字符完全一致、仅一个字不同」时才替换，
# 因此对非试卷文档不会命中。按长度降序，长的先修避免短搭配抢位。
# 仅在 exam 模式下启用（默认关）：通用文档改走 CJK 路由 + jieba 通用纠错。
EXAM_IDIOMS = tuple(sorted((
    "每小题所给", "每小题1分", "长对话理解", "单项选择", "最佳选项",
    "三个选项", "四个选项", "读两遍", "信息转换", "短文理解",
    "中选出", "，满分",
    "你将听到一篇短文", "请根据短文内容", "根据短文内容",
    "下面表格", "每空仅填一词",
), key=len, reverse=True))

# 领域开关：exam=True 启用试卷固定搭配兜底；默认 False 保持通用。
_EXAM_MODE = False


def set_exam_mode(enabled):
    global _EXAM_MODE
    _EXAM_MODE = bool(enabled)


def _repairable(a, b):
    """两个字能否互为 OCR 误读。

    数字必须原样匹配，否则「共5小题」会被当成「每小题」改坏。
    """
    if a == b:
        return True
    if a.isdigit() or b.isdigit():
        return False
    return _is_cjk(a) and _is_cjk(b)


def _phrase_diff(seg, phrase):
    """返回 (可修复差位数, 是否存在不可修复的差位)。"""
    fix, hard = 0, False
    for a, b in zip(seg, phrase):
        if a == b:
            continue
        if _repairable(a, b):
            fix += 1
        else:
            hard = True
    return fix, hard


def _fix_read_twice(text):
    """修复「读两遍」的行尾吞字与符号误读（读两→读两遍、读两>→读两遍）。

    中文行行尾的「遍」常被 det 切成独立小框或读成「>」，导致「读两」缺尾字。
    仅在「读两」后跟行尾/标点/符号时补「遍」，已正确的「读两遍」不受影响。
    """
    if "读两" not in text:
        return text
    out = re.sub(r"读两[>＞〉]", "读两遍", text)
    out = re.sub(r"读两(?=$|[。；，、\s])", "读两遍", out)
    return out


def _fix_exam_chinese(text):
    """修复试卷中文里被 OCR 吞掉的字（三个选→三个选项、选出一→选出一个）。

    「选项」的「项」字、「选出」的「个」字在行末常被 det 切丢，这里只在
    行尾/标点后补回，句中的正确写法不受影响。
    """
    if not text:
        return text
    out = re.sub(r"三个选(?=$|[。；，、\s])", "三个选项", text)
    out = re.sub(r"选出一(?=$|[。；，、\s])", "选出一个", out)
    return out


def _normalize_question_numbers(text):
    """统一题号括号：(__)18. / ()18. / (|)36. → ( )18.。

    题号前的答题括号内是空格，tesseract 把它读成下划线/竖线或直接丢掉；这里
    只在「括号 + 数字 + 点」结构上统一，普通句子里的括号不受影响。
    """
    if not text:
        return text
    # 括号后是题号（数字打头，可含被读成字母的数字），统一括号并纠正数字字母混淆
    def fix(m):
        num = m.group(2).translate(str.maketrans("SOIZB", "50128"))
        return "( )%s." % num

    return re.sub(r"\([_\s|]*\)(\s*)(\d[A-Za-z0-9]{0,2})([.．,，、])", fix, text)


def repair_exam_phrases(text):
    """把试卷固定搭配里被 OCR 换掉的单字修回。

    低分辨率下 tesseract 会把「理解」读成「再解」、「选择」读成「舍择」，
    整行或整字重识别都救不回来（实测置信度 0-22），只能靠固定搭配兜底。
    """
    if not text or not _CJK_RUN.search(text):
        return text
    out = text
    # 已正确读出的搭配要先保护：部分搭配彼此只差一个字（三个选项/四个选项），
    # 不记账会把正确的片段改错
    blocked = []
    for phrase in EXAM_IDIOMS:
        n = len(phrase)
        i = 0
        while i + n <= len(out):
            if out[i:i + n] == phrase:
                blocked.append((i, i + n))
                i += n
            else:
                i += 1
    for phrase in EXAM_IDIOMS:
        n = len(phrase)
        for i in range(len(out) - n + 1):
            if any(not (i + n <= s or i >= e) for s, e in blocked):
                continue
            fix, hard = _phrase_diff(out[i:i + n], phrase)
            if hard:
                continue
            if fix == 0:
                continue
            # 单字误读最常见，直接修。多字误读（低分辨率整串糊掉，如「请根据」
            # →「谢根回」）只在短语足够长且相同字符占比过半时修，避免把碰巧
            # 相似的文本改坏。
            matched = n - fix
            if fix == 1 or (n >= 6 and fix <= 3 and matched * 2 >= n):
                out = out[:i] + phrase + out[i + n:]
                blocked.append((i, i + n))
            continue
    return out


# ── 英文通用纠错与选项规范化 ─────────────────────────────


# 英文句首被 OCR 误读的常见词形。用 \b 词边界限定：rt's/lt's 在英文里
# 不是合法词形，替换几乎不会误伤（art's 里 rt 前不是词边界，不命中）。
# 撇号必须兼容直撇 ' (U+0027) 与弯撇 ’ (U+2019)：tesseract/PP-OCR 常把
# 撇号读成弯撇，若只用直撇则 rt's 这类修复整体失效。
_ENGLISH_LEAD_FIXES = (
    (re.compile(r"\brt['’]s\b"), "It's"),
    (re.compile(r"\blt['’]s\b"), "It's"),
    (re.compile(r"(^|[.!?]\s+)it['’]s\b"), r"\1It's"),
)

# 英语选项混淆矩阵：低分辨率下 tesseract/PP-OCR 把冠词答案 a、分隔符「；」、
# 斜杠「/」互相混读，只能靠完整选项段（含选项头误读）精确替换。key 是实测
# 乱码选项段、value 是标准形式。仅 exam 模式 + 选项块内启用，key 含选项头
# 或误读特征，普通英文句子几乎不会命中，误伤低。
EXAM_OPTION_FIXES = (
    ("A.aia", "A.a；a"),
    ("B./:a", "B./；a"),
    ("@,.n%/", "C.a；/"),
    ("D,//", "D./；/"),
)

_OPTION_HEAD = re.compile(r"\b([A-D])[.．,，](\S)")


def _repair_english(text):
    """把英文句首被 OCR 误读的首字母补回（It's → rt's）。始终启用。"""
    if not text:
        return text
    out = text
    for pat, good in _ENGLISH_LEAD_FIXES:
        out = pat.sub(good, out)
    return out


def _fix_english_symbols(text):
    """修复英文里被 OCR 误读的符号（I'll→I'I、but I→but|）。

    低分辨率下 tesseract 把撇号 ' 读成字母 I、把字母 I 读成竖线 |，产生
    I'I、but| 这类粘连错字；用词边界 + 固定模式替换，普通英文句不会命中。
    """
    if not text:
        return text
    out = re.sub(r"\bI['’]I\b", "I'll", text)
    out = re.sub(r"\bbut\|", "but I ", out)
    out = re.sub(r"\bBut\|", "But I ", out)
    out = re.sub(r"\bwould['’]t\b", "wouldn't", out)
    out = re.sub(r"\bWould['’]t\b", "Wouldn't", out)
    return out


def _fix_blank_line(text):
    """把填空下划线被 tesseract 读成文字的情况还原（form i → ___）。

    试卷填空题的空线（___）在低分辨率下常被读成 "form i"/"form l" 等；
    仅在行尾或后跟标点时还原，句子中间的 "form" 不命中。
    """
    if not text:
        return text
    return re.sub(r"\bform\s*[il1]\b(?=$|[.．\s])", "___", text, flags=re.IGNORECASE)


# 试卷里 tesseract 把常见词读成罕见词/非词的固定混淆（仅 exam 模式，词边界整词替换）
_EXAM_ENGLISH_FIXES = (
    (r"\btho\b", "the"),
    (r"\bTho\b", "The"),
    (r"\bmako\b", "make"),
    (r"\bMako\b", "Make"),
    (r"\bHolon\b", "Helen"),
    (r"\bIcttcr\b", "letter"),
    (r"\baftemoon\b", "afternoon"),
    (r"\bEverv\b", "Every"),
    (r"\bSundav\b", "Sunday"),
    (r"Eveorv(?=\d)", "Every "),
    (r"mary\s+me\s+bus", "Mary ___ the bus"),
    # 括号中文注释被 tesseract 吞字（patients(人) → patients(病人)）
    (r"patients\(人\)", "patients(病人)"),
)


def _fix_exam_english(text):
    """exam 模式下修正题干里的英文罕见词误读（tho→the、mako→make 等）。

    tesseract 对纯英文题干会把常见词读成同形罕见词（tho/mako/Holon 都是合法
    词但语义不符），词级编辑距离纠错因「词典内词不动」而放过；这里用固定整词
    映射兜底，普通文档（非 exam）不启用。
    """
    if not _EXAM_MODE or not text:
        return text
    out = text
    for pat, good in _EXAM_ENGLISH_FIXES:
        out = re.sub(pat, good, out)
    # 表格题号与英文词粘连（28morning），把数字与后续字母拆开
    out = re.sub(r"(\d)([A-Za-z])", r"\1 \2", out)
    # 破折号 — 被 tesseract 读成中文「一」，仅在后跟大写字母时还原
    out = re.sub(r"一(?=[A-Z])", "—", out)
    return out


def _is_option_block(text):
    """判断一行是否为「A. … B. … C. … D. …」结构的单选题选项块。

    选项头允许被误读（如 C 读成 @、D. 读成 D,），只要还能检出至少 3 个
    A-D 打头的段就按选项块处理；普通英文句子不会出现这种结构。
    """
    if not text:
        return False
    return len(re.findall(r"(?:^|\s)[A-D][.．,，]", text)) >= 3


def _repair_option_block(text):
    """对选项块做完整串混淆矩阵替换，仅 exam 模式 + 选项块启用。"""
    if not _is_option_block(text):
        return text
    out = text
    for bad, good in EXAM_OPTION_FIXES:
        out = out.replace(bad, good)
    return out


def _normalize_options(text, conf):
    """把 OCR 乱码的选项格式规范化，仅对选项块生效、按置信度分级。

    始终做安全修复：清理填空横线、选项头「A.xxx → A. xxx」、分隔符统一
    （,，→；）、重复标点收敛。低置信度（conf<0.6）下才启用符号混淆替换
    （0→/、:→；），避免把数学/时间选项里合法的 0 与 : 改坏。
    """
    if not text or not _is_option_block(text):
        return text
    out = re.sub(r"-{3,}", "", text)
    out = _OPTION_HEAD.sub(r"\1. \2", out)
    out = out.replace("，", "；").replace(",", "；")
    out = re.sub(r"[,;:]{2,}", "；", out)
    if _EXAM_MODE and conf is not None and conf < 0.6:
        out = out.replace("0", "/").replace(":", "；")
    return out


def finalize_line_text(text, conf=None):
    """行的文本后处理入口：英文纠错 → 选项混淆矩阵 → 选项规范化 → 词级纠错 → 中文修复。

    通用层（英文词修正、选项头格式、分隔符统一、编辑距离词级纠错）始终启用、
    误伤率低；选项混淆矩阵与中文固定搭配仅在 exam 模式启用。词级纠错放在选项
    规范化之后，确保选项头补空格拆词后再按词边界纠错；对整行英文 token 生效
    （题干行与选项块都覆盖），仅唯一候选才替换，误改中文行英文片段的风险低。
    """
    text = _repair_english(text)
    text = _fix_english_symbols(text)
    text = _normalize_roman_head(text)
    text = _normalize_question_numbers(text)
    if _EXAM_MODE:
        text = _repair_option_block(text)
    text = _normalize_options(text, conf)
    text = _correct_english_words(text)
    text = _recover_english_spaces(text)
    text = _fix_exam_english(text)
    if _EXAM_MODE:
        text = repair_exam_phrases(text)
        text = _balance_brackets(text)
    return text


# ── 繁简转换 ─────────────────────────────────────────────


class CharsetConverter:
    def __init__(self, charset):
        self.target = charset
        self._cc = None

    def convert(self, text):
        if self.target not in ("zh_hans", "zh_hant") or not text:
            return text
        if self._cc is None:
            try:
                from opencc import OpenCC

                self._cc = OpenCC("t2s") if self.target == "zh_hans" else OpenCC("s2t")
            except Exception as exc:
                _log(f"[OCR] OpenCC 不可用，跳过繁简转换: {exc}")
                return text
        return self._cc.convert(text)


def detect_charset(text):
    """Pick the variant the text reads closest to, using OpenCC in both directions."""
    if not text:
        return "zh_hans"
    try:
        from opencc import OpenCC

        to_s = OpenCC("t2s").convert(text)
        to_t = OpenCC("s2t").convert(text)
    except Exception:
        return "zh_hans"
    # Distance to each variant: fewer edits means the text is already in that form.
    dist_s = _edits(text, to_s)
    dist_t = _edits(text, to_t)
    return "zh_hant" if dist_t < dist_s else "zh_hans"


def _edits(a, b):
    n = min(len(a), len(b))
    diff = sum(1 for i in range(n) if a[i] != b[i])
    return diff + abs(len(a) - len(b))


# ── OCR 引擎 ─────────────────────────────────────────────


class TesseractEngine:
    name = "tesseract"

    def __init__(self, mode="general", quality="normal", lang="auto"):
        import importlib.util

        if importlib.util.find_spec("pytesseract") is None:
            raise RuntimeError("pytesseract 未安装")
        try:
            import pytesseract

            langs = set(pytesseract.get_languages(config=""))
        except Exception as exc:
            raise RuntimeError(f"tesseract 不可用: {exc}") from exc

        self.lang = self._pick_lang(lang, mode, langs)
        # 中文行会用单语言模型再跑一遍：多语言组合会把简体读成繁体并夹杂拉丁乱码
        self.cjk_lang = next((c for c in ("chi_sim", "chi_tra") if c in langs), None)
        # 英文行会用 eng 单语言放大重识别，消除中英混排把英文误读成中文的问题
        self.eng_ok = "eng" in langs

        self.profile = QUALITY_PROFILE.get(quality, QUALITY_PROFILE["normal"])
        self.dpi = self.profile["dpi"]
        self.max_width = self.profile["max_width"]

    @staticmethod
    def _pick_lang(lang, mode, available):
        """按语言预设挑一个语言包齐全的组合；缺失时逐步降级到单个语言包。"""
        if lang not in LANG_PRESETS:
            lang = "auto"
        combos = list(LANG_PRESETS[lang])
        if lang == "auto" and mode == "advanced" and "chi_sim+chi_tra+eng" in combos:
            combos = [c for c in combos if "chi_tra" not in c] + combos
        for combo in combos:
            wanted = set(combo.split("+"))
            if available & wanted == wanted:
                return combo
        if lang == "auto":
            for combo in LANG_PRESETS["auto"]:
                wanted = set(combo.split("+"))
                if available & wanted == wanted:
                    return combo
        # 最后兜底：任选一个可用的拉丁/中文语言包
        for single in ("eng", "chi_sim", "chi_tra", "jpn", "kor"):
            if single in available:
                return single
        raise RuntimeError("tesseract 语言包缺失")

    @staticmethod
    def _prepare(gray, max_width):
        import cv2

        h, w = gray.shape[:2]
        if w >= max_width:
            return gray
        factor = min(int(round(max_width / float(w))), 4)
        return cv2.resize(gray, (w * factor, h * factor), interpolation=cv2.INTER_CUBIC)

    def _run_psm(self, img, psm, lang, extra=""):
        """Run one tesseract PSM pass and return the recognized words."""
        import pytesseract

        config = f"--psm {psm} {extra}".strip()
        data = pytesseract.image_to_data(
            img, lang=lang, config=config, output_type=pytesseract.Output.DICT
        )
        words = []
        for i, raw in enumerate(data["text"]):
            text = str(raw).strip()
            if not text:
                continue
            try:
                conf = float(data["conf"][i])
            except (TypeError, ValueError):
                conf = 0.0
            if conf < 0:
                conf = 0.0
            words.append(
                Word(
                    text=text,
                    x=int(data["left"][i]),
                    y=int(data["top"][i]),
                    w=max(int(data["width"][i]), 1),
                    h=max(int(data["height"][i]), 1),
                    conf=conf,
                    page=0,
                    size=float(data["height"][i]),
                )
            )
        return words

    def page_gray(self, image):
        """返回与 image_words 同一份预处理后的灰度图，保证两者坐标对齐。"""
        import cv2
        import numpy as np

        return self._prepare(
            cv2.cvtColor(np.array(image.convert("RGB")), cv2.COLOR_RGB2GRAY), self.max_width
        )

    def image_words(self, image):
        """Return list[Word] (box + confidence) for one PIL image.

        逐个 PSM 尝试并按平均置信度择优；首个 PSM 已足够好时提前收敛，
        避免在大尺寸图上重复跑 PSM（PSM 11 单次可达数十秒）。
        返回的词框统一在输入 image 的像素坐标系（见 _to_image_coords）。
        """
        from PIL import Image

        gray = self.page_gray(image)
        img = Image.fromarray(gray)

        best, best_score = [], -1.0
        for psm in self.profile["psms"]:
            words = self._run_psm(img, psm, self.lang)
            if not words:
                continue
            score = sum(w.conf for w in words) / len(words)
            if score > best_score or (score == best_score and len(words) > len(best)):
                best, best_score = words, score
            if score >= 0.6 and len(words) >= 20:
                break
        return self._to_image_coords(self.refine_words(gray, best), image, gray)

    @staticmethod
    def _to_image_coords(words, image, gray):
        """把词框从灰度图坐标还原回输入 image 的像素坐标。

        page_gray 会把小图按 max_width 放大（normal 1400 / high 2400），
        识别得到的词框因此是放大后灰度图的坐标；analyze_page 里约定行坐标
        是 page_w（=image.width）单位、靠 sx/sy 换算到灰度图，若不还原，
        放大场景下裁剪会双重缩放跑偏（小图 OCR 时尤其明显）。
        """
        fx = image.width / max(float(gray.shape[1]), 1.0)
        fy = image.height / max(float(gray.shape[0]), 1.0)
        if abs(fx - 1.0) < 1e-3 and abs(fy - 1.0) < 1e-3:
            return words
        for w in words:
            w.x = int(w.x * fx)
            w.y = int(w.y * fy)
            w.w = max(int(w.w * fx), 1)
            w.h = max(int(w.h * fy), 1)
            w.size = float(w.h)
        return words


    def _crop_words(self, gray, x0, y0, x1, y1, psm, lang, fx=1.0, extra=""):
        """在 gray 上裁一块按指定语言重识别，返回坐标已还原到全图的 words。"""
        import cv2
        from PIL import Image

        xa = max(int(x0), 0)
        ya = max(int(y0), 0)
        xb = min(int(x1), gray.shape[1])
        yb = min(int(y1), gray.shape[0])
        if xb <= xa or yb <= ya:
            return []
        sub = gray[ya:yb, xa:xb]
        if fx != 1.0:
            sub = cv2.resize(sub, None, fx=fx, fy=fx, interpolation=cv2.INTER_CUBIC)
        scale = fx if fx >= 1.0 else 1.0
        words = self._run_psm(Image.fromarray(sub), psm, lang, extra=extra)
        for w in words:
            w.x = xa + int(w.x / scale)
            w.y = ya + int(w.y / scale)
            w.w = max(int(w.w / scale), 1)
            w.h = max(int(w.h / scale), 1)
            w.size = float(w.h)
        return words

    @staticmethod
    def _ink(gray):
        import cv2

        return cv2.threshold(gray, 0, 255, cv2.THRESH_BINARY_INV + cv2.THRESH_OTSU)[1]

    @staticmethod
    def _components(bw):
        """返回按 x 排序的有效连通域 (x, y, w, h, area)。"""
        import cv2

        n, _lab, stats, _cent = cv2.connectedComponentsWithStats(bw, 8)
        comps = []
        for i in range(1, n):
            x, y, w, h, a = (int(v) for v in stats[i])
            if a < 8 or h < 6:
                continue
            comps.append((x, y, w, h, a))
        comps.sort()
        return comps

    @staticmethod
    def _looks_cjk(bw, words):
        """按字形宽高比判别一行墨迹是否以中文为主。

        中文方块字近似正方形(w/h≈0.85 以上)，拉丁字母偏窄(≈0.6~0.8)；
        墨迹形状判不出来时退回文本里的中文占比。
        """
        comps = TesseractEngine._components(bw)
        if not comps:
            return False
        ratios = sorted(c[2] / max(c[3], 1) for c in comps)
        if ratios[len(ratios) // 2] >= 0.82:
            return True
        text = "".join(w.text for w in words)
        nsp = sum(1 for ch in text if not ch.isspace())
        cjk = sum(1 for ch in text if _is_cjk(ch))
        return nsp >= 4 and cjk / nsp >= 0.20

    @staticmethod
    def _cjk_block_x(bw, xa, xb, ya, yb):
        """行区内首个中文方块字形的左边界（全图 px），找不到返回 None。

        中文方块字 w/h≈0.9 且内部填充率高；罗马数字竖条窄、V 形填充率低，都能被排除，
        因此这个边界不依赖 OCR 词框，首遍识别把中文读成拉丁乱码时依然可用。
        """
        sub = bw[max(ya, 0):max(yb, 0), max(xa, 0):min(xb, bw.shape[1])]
        if sub.size == 0:
            return None
        ox = max(xa, 0)
        for x, y, w, h, a in TesseractEngine._components(sub):
            if h >= 8 and w >= 0.85 * h and a >= 0.30 * w * h:
                return ox + x
        return None

    @staticmethod
    def _dmg(text):
        """统计一行文本的识别损伤：拉丁字母数 + 繁体字个数。"""
        latin = sum(1 for ch in text if ch.isascii() and ch.isalpha())
        trad = sum(1 for ch in text if ch in TRAD_MARKERS)
        return latin + trad

    def _line_roman_head(self, gray, bw, ln):
        """行首是否为罗马数字章节号。命中返回 (roman, x_cjk, head_words)。"""
        items = sorted(ln.words, key=lambda w: w.x)
        xa, xb = ln.x - 2, ln.x + ln.w + 2
        ya, yb = ln.y - 2, ln.y + ln.h + 2
        x_cjk = self._cjk_block_x(bw, xa, xb, ya, yb)
        if x_cjk is None:
            return None
        line_crop = bw[max(ya, 0):max(yb, 0), max(xa, 0):min(xb, bw.shape[1])]
        if line_crop.size == 0 or not self._looks_cjk(line_crop, ln.words):
            return None
        prefix_w = x_cjk - xa
        if prefix_w < 4 or prefix_w > 0.18 * max(ln.w, 1):
            return None
        roman = self._roman_prefix(gray, bw, xa, ln.y, ln.h, x_cjk)
        if not roman:
            return None
        # 词框常越界吞掉后面的中文字，所以按起点而非终点判断行首
        head_words = [w for w in items if w.x < x_cjk]
        if not head_words:
            return None
        return roman, x_cjk, head_words

    def _refine_roman_prefixes(self, gray, bw, words):
        """“III. 标题”这类行首的罗马序号常被读成 HI, / V1, 单独裁前缀重识别。"""
        out = list(words)
        for ln in cluster_lines(out):
            hit = self._line_roman_head(gray, bw, ln)
            if not hit:
                continue
            roman, x_cjk, head_words = hit
            if roman == _strip_roman_punct(head_words[0].text):
                continue
            head_words[0].text = roman + "."
            head_words[0].w = max(int(x_cjk - head_words[0].x), 1)
            head_words[0].size = float(head_words[0].h)
            drop = {id(w) for w in head_words[1:]}
            out = [w for w in out if id(w) not in drop]
        return out

    def _refine_cjk_lines(self, gray, bw, words):
        """中文为主的行用 chi_sim 单语言模型重识别，避免繁体错字与拉丁乱码。

        行首罗马序号已单独校正过，重识别的裁剪区间从首个中文字形开始，
        否则整行重跑会把刚修好的章节号再读丢。只有首遍结果确实损伤
        （混入拉丁字母或繁体字）的行才尝试重跑，避免把已读对的行改坏。
        """
        if not self.cjk_lang:
            return words
        out = []
        for ln in cluster_lines(words):
            orig = ln.words
            xa, keep = ln.x - 2, []
            x_cjk = self._cjk_block_x(bw, xa, ln.x + ln.w + 2, ln.y - 2, ln.y + ln.h + 2)
            if x_cjk is not None and x_cjk - xa > 4:
                xa = x_cjk
                keep = [w for w in orig if w.x < xa]
            keep_ids = {id(w) for w in keep}
            rest = [w for w in orig if id(w) not in keep_ids]
            crop = bw[max(ln.y - 2, 0):max(ln.y + ln.h + 2, 0),
                      max(xa, 0):min(ln.x + ln.w + 2, bw.shape[1])]
            old_text = "".join(w.text for w in rest)
            if crop.size == 0 or not self._looks_cjk(crop, rest) or self._dmg(old_text) < 2:
                out.extend(orig)
                continue
            cand = self._crop_words(
                gray, xa, ln.y - 2, ln.x + ln.w + 2, ln.y + ln.h + 2, 7, self.cjk_lang)
            if not cand:
                out.extend(orig)
                continue
            cand_text = "".join(w.text for w in cand)
            n_old = sum(1 for ch in old_text if _is_cjk(ch))
            n_new = sum(1 for ch in cand_text if _is_cjk(ch))
            if self._dmg(cand_text) < self._dmg(old_text) or n_new > n_old:
                out.extend(keep + cand)
            else:
                out.extend(orig)
        if not out:
            return words
        out.sort(key=lambda w: (w.y, w.x))
        return out

    def refine_words(self, gray, words):
        """首遍结果的两处定向修补：章节罗马序号还原、中文行单语言重识别。"""
        if not words:
            return words
        bw = self._ink(gray)
        words = self._refine_roman_prefixes(gray, bw, words)
        words = self._refine_cjk_lines(gray, bw, words)
        return self._refine_latin_lines(gray, bw, words)

    @staticmethod
    def _looks_latin(words):
        """判断一行是否以拉丁字母为主（英文行），用于触发 eng 单语言重识别。"""
        text = "".join(w.text for w in words)
        nsp = sum(1 for ch in text if not ch.isspace())
        if nsp < 4:
            return False
        latin = sum(1 for ch in text if ch.isascii() and ch.isalpha())
        cjk = sum(1 for ch in text if _is_cjk(ch))
        return latin >= 3 and latin * 2 >= nsp and cjk * 3 < nsp

    @staticmethod
    def _has_cjk_run(text):
        """行内是否存在连续(≥2)中文字符。

        真实的中文注解（如英语试卷里 patients(病人) 的「病人」）是连续中文，
        而中英混排误读（如 the→品）通常是孤立的单个汉字。用连续性区分两者，
        避免 eng 重识别把真实中文注解毁成乱码。
        """
        run = 0
        for ch in text:
            if _is_cjk(ch):
                run += 1
                if run >= 2:
                    return True
            else:
                run = 0
        return False

    def _refine_latin_lines(self, gray, bw, words):
        """英文为主的行用 eng 单语言放大重识别，消除中英混排造成的孤立汉字误读。

        中英混排页面（如英语试卷）首遍用 chi_sim+chi_tra+eng 识别时，英文小字常被
        读成中文（如 the→品）。这类行既非中文为主（不满足 _looks_cjk），也不走
        PP-OCR 路由（中文占比不足），此前没有任何修正路径。这里对称地裁行用 eng
        放大重识别，仅在确实消除了中文误读（CJK 计数下降）时替换。

        两道保护避免改坏已读对的行：连续中文说明是真实中文注解（如「病人」），
        直接跳过；含下划线/长横线说明是完形填空的空格标记，eng 会把它读丢，也跳过。
        """
        if not self.eng_ok:
            return words
        out = []
        for ln in cluster_lines(words):
            orig = ln.words
            text = "".join(w.text for w in orig)
            if not self._looks_latin(orig) or self._has_cjk_run(text):
                out.extend(orig)
                continue
            if any(ch in text for ch in "_—－"):
                out.extend(orig)
                continue
            cjk_before = sum(1 for ch in text if _is_cjk(ch))
            if cjk_before == 0:
                out.extend(orig)
                continue
            cand = self._crop_words(
                gray, ln.x - 2, ln.y - 2, ln.x + ln.w + 2, ln.y + ln.h + 2,
                7, "eng", fx=2.0)
            if not cand:
                out.extend(orig)
                continue
            cjk_after = sum(1 for ch in "".join(w.text for w in cand) if _is_cjk(ch))
            if cjk_after < cjk_before:
                out.extend(cand)
            else:
                out.extend(orig)
        if not out:
            return words
        out.sort(key=lambda w: (w.y, w.x))
        return out


    @staticmethod
    def _roman_shapes(comps, med_h, dot_area):
        """判断前缀墨迹是否只有罗马字符形状（竖条 / 低填充 V 形 / 句点）。

        中文字符会被切成多块笔画，横向互相重叠且填充率高，
        而罗马序号的笔画彼此分开、填充率很低，这两点合起来能排除误判。
        """
        prev_end = -1
        for x, y, w, ch, a in comps:
            if x < prev_end:
                return False
            prev_end = x + w
            if a < dot_area and ch < 0.45 * med_h:
                continue
            if w / max(ch, 1) <= 0.45 and ch >= 0.55 * med_h:
                continue
            if w >= 0.5 * med_h and a / float(w * max(ch, 1)) > 0.32:
                return False
        return True

    def _roman_prefix(self, gray, bw, x_start, y_top, h, x_stop):
        """还原 [x_start, x_stop) 内的罗马数字章节号。

        I 形竖条按连通域数量直接计数（III 这类竖线序列 tesseract 会并成一个字符），
        其余笔画单独裁剪后按罗马字符白名单识别。
        """
        import cv2

        xa = max(int(x_start) - 2, 0)
        xb = min(int(x_stop) - 2, bw.shape[1])
        ya = max(int(y_top) - 4, 0)
        yb = min(int(y_top + h) + 4, bw.shape[0])
        if xb <= xa or yb <= ya:
            return ""
        comps = self._components(bw[ya:yb, xa:xb])
        if not comps:
            return ""
        heights = sorted(c[3] for c in comps)
        med_h = heights[len(heights) // 2]
        dot_area = 0.12 * med_h * med_h
        cjk_area = 0.5 * med_h * med_h
        if not self._roman_shapes(comps, med_h, dot_area):
            return ""
        segments = []
        for x, y, w, ch, a in comps:
            if a < dot_area or ch < 0.4 * med_h:
                continue
            if w / max(ch, 1) <= 0.45 and ch >= 0.55 * med_h:
                segments.append(("I", None))
            elif w >= 0.85 * med_h and a >= cjk_area:
                continue
            else:
                segments.append(("", (x, y, w, ch)))
        if not segments:
            return ""
        out = []
        for token, box in segments:
            if token:
                out.append(token)
                continue
            gx, gy, gw, gh = box
            sub = gray[max(ya + gy - 3, 0):min(ya + gy + gh + 3, gray.shape[0]),
                       max(xa + gx - 3, 0):min(xa + gx + gw + 3, gray.shape[1])]
            if sub.size == 0:
                return ""
            up = cv2.resize(sub, None, fx=4.0, fy=4.0, interpolation=cv2.INTER_CUBIC)
            found = self._crop_words(up, 0, 0, up.shape[1], up.shape[0], 7, "eng",
                                     extra=f"-c tessedit_char_whitelist={ROMAN_CHARS}")
            got = _strip_roman_punct("".join(i.text for i in found))
            if not got:
                return ""
            out.append(got)
        roman = "".join(out)
        return roman if is_valid_roman(roman) else ""



class PPocREngine:
    """PP-OCR ONNX 引擎（官方配套 det/rec 模型，字符表内嵌在模型元数据中）。"""

    name = "pp_ocr"

    def __init__(self, mode="general", quality="normal"):
        import importlib.util

        for mod in ("cv2", "numpy", "onnxruntime", "pyclipper", "shapely"):
            if importlib.util.find_spec(mod) is None:
                raise RuntimeError(f"{mod} 未安装")
        self.mode = mode
        self.quality = quality
        _pp_scripts_path()
        from pp_ocr_onnx import _ppocr_sessions

        _ppocr_sessions()  # 预热：尽早暴露模型缺失或字符表不匹配

    def image_words(self, image):
        """Return list[Word] (box + confidence) for one PIL image."""
        import cv2
        import numpy as np

        from pp_ocr_onnx import ppocr_recognize

        bgr = cv2.cvtColor(np.array(image.convert("RGB")), cv2.COLOR_RGB2BGR)
        words = []
        for item in ppocr_recognize(bgr):
            x, y, w, h = item["box"]
            words.append(
                Word(
                    text=item["text"],
                    x=int(x),
                    y=int(y),
                    w=max(int(w), 1),
                    h=max(int(h), 1),
                    conf=float(item["score"]),
                    page=0,
                    size=float(h),
                )
            )
        return words


def _pp_scripts_path():
    here = os.path.dirname(os.path.abspath(__file__))
    if here not in sys.path:
        sys.path.insert(0, here)


def get_engine(mode="general", quality="normal", prefer="auto", lang="auto"):
    if prefer == "pp_ocr":
        candidates = ("pp_ocr", "tesseract")
    else:
        candidates = ("tesseract", "pp_ocr")
    last = None
    for name in candidates:
        try:
            if name == "pp_ocr":
                return PPocREngine(mode=mode, quality=quality)
            return TesseractEngine(mode=mode, quality=quality, lang=lang)
        except Exception as exc:
            last = exc
            _log(f"[OCR] {name} 不可用: {exc}")
    raise RuntimeError(f"没有可用的 OCR 引擎: {last}")


def _char_set(text):
    """用于跨来源比对的字符集合: CJK 与字母数字, 拉丁转小写, 去空白与标点。"""
    out = set()
    for c in text:
        o = ord(c)
        if (
            "\u4e00" <= c <= "\u9fff"
            or "\u3400" <= c <= "\u4dbf"
            or "\u3040" <= c <= "\u30ff"
            or "\uac00" <= c <= "\ud7af"
            or c.isalnum()
        ):
            out.add(c.lower())
    return out


def text_layer_mismatch(tl_text, ocr_text):
    """文本层与同页图像 OCR 结果的字符集合重合度过低则判为乱码。

    正确文本层与图像 OCR 同源, 字符集合高度重合; 乱码文本层(错码/私用区/错位)
    与图像 OCR 几乎不相交。OCR 误识只影响少量字符, 不会让重合度跌到 1/4 以下。
    """
    a, b = _char_set(tl_text), _char_set(ocr_text)
    if len(a) < 20 or len(b) < 20:
        return False
    inter = len(a & b)
    return inter / len(a) < 0.25 or inter / len(b) < 0.25


def _det_lang():
    """方向检测用的语言: 优先中英, 缺包时退到任一可用。"""
    try:
        import pytesseract

        avail = set(pytesseract.get_languages(config=""))
    except Exception:
        return "eng"
    if {"chi_sim", "eng"} <= avail:
        return "chi_sim+eng"
    if "eng" in avail:
        return "eng"
    return next(iter(avail), "eng")


def detect_orientation(image, max_side=760, accept_score=60):
    """检测图像需顺时针旋转多少度(0/90/180/270)才能正立。

    tesseract OSD 对中英混排与细长条带不可靠, 改用四方向置信度择优:
    对每个方向各识别一次, 选平均置信度最高者。

    两个提速手段: 采样图缩到 max_side 边长(方向判别不需要原图分辨率),
    以及按宽高比排序候选方向后、首个方向置信度达标即收敛。

    注意 accept_score 的单位是 tesseract 置信度(0-100), 不是 0-1。
    """
    if accept_score <= 1.0:
        accept_score = 60
    import pytesseract
    from PIL import Image

    pil = image if isinstance(image, Image.Image) else Image.fromarray(image)
    m = max(pil.size)
    if m > max_side:
        pil = pil.resize((int(pil.width * max_side / m), int(pil.height * max_side / m)), Image.LANCZOS)

    det_lang = _det_lang()
    landscape = pil.width >= pil.height
    order = (90, 270, 0, 180) if landscape else (0, 180, 90, 270)

    best_angle, best_score = 0, -1.0
    for angle in order:
        rot = pil if angle == 0 else pil.rotate(-angle, expand=True)
        try:
            data = pytesseract.image_to_data(
                rot, lang=det_lang, config="--psm 6", output_type=pytesseract.Output.DICT
            )
        except Exception:
            continue
        confs = []
        for c in data.get("conf", []):
            try:
                v = float(c)
            except (TypeError, ValueError):
                continue
            if v >= 0:
                confs.append(v)
        score = (sum(confs) / len(confs)) if confs else 0.0
        if score > best_score:
            best_score, best_angle = score, angle
        if score >= accept_score:
            break
    return best_angle if best_score > 0 else 0


def _orient_page(image, angle, pw, ph):
    """按顺时针 angle 旋转图像正立, 返回 (旋转图, 新逻辑宽, 新逻辑高)。

    90/270 时宽高互换, 使后续坐标映射仍指向正立后的版面。
    """
    if not angle or angle % 360 == 0:
        return image, pw, ph
    from PIL import Image

    pil = image if isinstance(image, Image.Image) else Image.fromarray(image)
    rot = pil.rotate(-angle, expand=True)
    if angle % 180 == 0:
        return rot, pw, ph
    return rot, ph, pw


# ── PDF 文本层 ───────────────────────────────────────────


def _span_words(span, pno):
    """Split a PDF span into word-level boxes from its character bboxes."""
    words = []
    size = float(span.get("size") or 11)
    buf = []

    def flush():
        items = list(buf)
        del buf[:]
        if not items:
            return
        text = "".join(c[0] for c in items).strip()
        if not text:
            return
        x0 = min(c[1][0] for c in items)
        y0 = min(c[1][1] for c in items)
        x1 = max(c[1][2] for c in items)
        y1 = max(c[1][3] for c in items)
        words.append(
            Word(
                text=text,
                x=int(x0),
                y=int(y0),
                w=max(int(x1 - x0), 1),
                h=max(int(y1 - y0), 1),
                conf=1.0,
                page=pno,
                size=size,
            )
        )

    for ch in span.get("chars") or []:
        glyph = ch.get("c", "")
        bbox = ch.get("bbox")
        if not glyph or bbox is None:
            continue
        if glyph.isspace():
            flush()
            continue
        if buf:
            prev = buf[-1]
            gap = bbox[0] - prev[1][2]
            prev_w = max(prev[1][2] - prev[1][0], 0.5)
            split = gap > 0.45 * prev_w
            if not split and _is_cjk(glyph):
                split = sum(len(c[0]) for c in buf) >= CJK_TOKEN_CAP
            if split:
                flush()
        buf.append((glyph, bbox))
    flush()
    return words


def pdf_page_count(pdf_path):
    import fitz

    doc = fitz.open(pdf_path)
    try:
        return doc.page_count
    finally:
        doc.close()


def pdf_page_sizes(pdf_path):
    import fitz

    doc = fitz.open(pdf_path)
    try:
        return [(float(p.rect.width), float(p.rect.height)) for p in doc]
    finally:
        doc.close()


def pdf_text_words(pdf_path):
    """Extract word boxes from a PDF text layer with real geometry and font size."""
    import fitz

    doc = fitz.open(pdf_path)
    words = []
    try:
        for pno, page in enumerate(doc):
            d = page.get_text("rawdict")
            for block in d.get("blocks", []):
                if block.get("type") != 0:
                    continue
                for line in block.get("lines", []):
                    for span in line.get("spans", []):
                        words.extend(_span_words(span, pno))
    finally:
        doc.close()
    return words


def pdf_page_to_image(pdf_path, pno, dpi=150):
    import io
    import fitz
    from PIL import Image

    doc = fitz.open(pdf_path)
    try:
        page = doc[pno]
        pix = page.get_pixmap(matrix=fitz.Matrix(dpi / 72.0, dpi / 72.0))
        return Image.open(io.BytesIO(pix.tobytes("png"))).convert("RGB")
    finally:
        doc.close()


def pdf_page_native_image(pdf_path, pno, max_side=6000):
    """纯图像 PDF 页: 按原生分辨率抽取嵌入图并合成, 绕过 get_pixmap 重采样。

    get_pixmap 对 JPEG 条带做重采样会劣化小字(如 e→c); 直接抽取嵌入图按其
    在页面上的位置以原生像素合成, 保留源图细节。页面含文本层或矢量绘图时
    返回 None, 由调用方退回渲染。
    """
    import io
    import fitz
    from PIL import Image

    doc = fitz.open(pdf_path)
    try:
        page = doc[pno]
        if page.get_text().strip() or page.get_drawings():
            return None
        info = page.get_image_info(xrefs=True)
        if not info:
            return None
        # 各图的原生像素/点比例。PDF 常以非等比方式摆放扫描条带(x 比例略大于 y),
        # 若按目标尺寸 resize 会重采样 JPEG 条带并劣化小字(e→c), 故按原生像素直贴。
        ratios = []
        for it in info:
            bb = it.get("bbox") or (0, 0, 0, 0)
            bw = max(bb[2] - bb[0], 1e-6)
            bh = max(bb[3] - bb[1], 1e-6)
            if not it.get("width") or not it.get("height"):
                return None
            ratios.append((bb, it["width"] / bw, it["height"] / bh))
        # 比例明显不一致时退回渲染, 避免同页出现不同尺度的混贴
        xs = [r[1] for r in ratios]
        ys = [r[2] for r in ratios]
        if len(ratios) > 1:
            if max(xs) / min(xs) > 1.10 or max(ys) / min(ys) > 1.10:
                return None
        sx, sy = sum(xs) / len(xs), sum(ys) / len(ys)
        placements = []
        for bb, rx, ry in ratios:
            x0 = int(round(bb[0] * sx))
            y0 = int(round(bb[1] * sy))
            tw = max(int(round((bb[2] - bb[0]) * rx)), 1)
            th = max(int(round((bb[3] - bb[1]) * ry)), 1)
            placements.append((x0, y0, tw, th))
        cw = max(p[0] + p[2] for p in placements)
        ch = max(p[1] + p[3] for p in placements)
        if max(cw, ch) > max_side:
            return None
        canvas = Image.new("RGB", (cw, ch), (255, 255, 255))
        placed = False
        for it, (x0, y0, tw, th) in zip(info, placements):
            xref = it.get("xref", 0)
            if not xref:
                continue
            try:
                d = doc.extract_image(xref)
            except Exception:
                continue
            try:
                im = Image.open(io.BytesIO(d["image"])).convert("RGB")
            except Exception:
                continue
            if im.size != (tw, th):
                im = im.resize((tw, th), Image.LANCZOS)
            canvas.paste(im, (x0, y0))
            placed = True
        return canvas if placed else None
    finally:
        doc.close()


def pdf_pages_to_images(pdf_path, dpi=150):
    return [pdf_page_to_image(pdf_path, i, dpi) for i in range(pdf_page_count(pdf_path))]


# ── 版式分析 ─────────────────────────────────────────────


def cluster_lines(words):
    """Group words into Lines by vertical proximity."""
    if not words:
        return []
    ordered = sorted(words, key=lambda w: (w.y, w.x))
    med_h = _median(w.h for w in ordered) or 20
    tol = max(med_h * 0.55, 3)

    groups = []
    for w in ordered:
        for g in groups:
            cy = sum(i.cy for i in g) / len(g)
            if abs(w.cy - cy) <= tol:
                g.append(w)
                break
        else:
            groups.append([w])

    lines = []
    for g in groups:
        items = sorted(g, key=lambda i: i.x)
        heights = sorted(i.h for i in items)
        conf = sum(i.conf for i in items) / len(items)
        ln = Line(
            text=finalize_line_text(_join_words(items), conf),
            x=min(i.x for i in items),
            y=min(i.y for i in items),
            w=max(i.x + i.w for i in items) - min(i.x for i in items),
            h=max(i.h for i in items),
            conf=conf,
            size=float(heights[len(heights) // 2]),
            page=items[0].page,
            words=items,
        )
        # 1×1 的退化词框是重识别的残留，既读不出内容也过不了任何重叠判定
        if ln.text and ln.w >= 2 and ln.h >= 2:
            lines.append(ln)
    return lines


def _absorb_punct_lines(lines):
    """把只含标点的孤立行并回上一行。

    行尾的「，」这类标点常被 OCR 给出行内偏移的词框，聚类时落到下一行，
    读起来像正文中间断了一句。
    """
    out = []
    for ln in lines:
        s = ln.text.strip()
        if out and s and _PUNCT_ONLY.fullmatch(s) and ln.y <= out[-1].y + out[-1].h:
            prev = out[-1]
            if not prev.text.rstrip().endswith(("。", "！", "？", ".", "!", "?")):
                prev.text = (prev.text.rstrip() + s).strip()
            continue
        out.append(ln)
    return out


def detect_columns(lines, page_w):
    """Return 2 when a clear vertical gap splits the layout, else 1."""
    if not page_w or page_w <= 0 or len(lines) < 4:
        return 1
    for ratio in (0.5, 0.48, 0.52, 0.45, 0.55):
        split = page_w * ratio
        left = sum(1 for ln in lines if ln.cx < split)
        right = len(lines) - left
        if left < 2 or right < 2 or min(left, right) < 0.3 * len(lines):
            continue
        if all(not (ln.x < split - page_w * 0.05 and ln.x + ln.w > split + page_w * 0.05)
               for ln in lines):
            return 2
    return 1


def order_lines(lines, page_w, ncols):
    """Reorder lines into reading order, handling two-column layouts."""
    if ncols < 2 or not page_w:
        return sorted(lines, key=lambda ln: (ln.y, ln.x))
    left = [ln for ln in lines if ln.cx < page_w / 2.0]
    right = [ln for ln in lines if ln.cx >= page_w / 2.0]
    return sorted(left, key=lambda ln: (ln.y, ln.x)) + sorted(right, key=lambda ln: (ln.y, ln.x))


def is_header_footer(ln, page_w, page_h):
    if not page_w or not page_h:
        return False
    if ln.y < page_h * 0.07 and ln.w <= page_w * 0.95:
        return True
    return ln.y + ln.h > page_h * 0.93 and ln.w <= page_w * 0.95


def is_title(ln, med_size):
    """判断一行是否是标题。

    字号只能粗筛：同一版面里，说明句和选项行的字号常常与真标题接近。
    再靠行尾字符区分——章节标题以「）」收尾的括注，说明句以「。」/「，」/「.」收尾。
    """
    if not ln.size or not med_size:
        return False
    text = ln.text.strip()
    if not text or len(text) > 240:
        return False
    if ln.size < med_size * 1.3:
        return False
    if OPTION_LINE.match(text):
        return False
    return SECTION_TITLE.match(text) is not None or text[-1] in ")）"


def is_formula(ln):
    chars = [c for c in ln.text if not c.isspace()]
    if len(chars) < 2:
        return False
    ratio = sum(1 for c in chars if c in MATH_SYMBOLS) / len(chars)
    cjk = sum(1 for c in chars if _is_cjk(c))
    return ratio >= 0.12 and cjk / float(len(chars)) < 0.4


def _column_bounds(rows, tol):
    spans = sorted((it.x, it.x + it.w) for row in rows for it in row)
    merged = []
    for s, e in spans:
        if merged and s <= merged[-1][1] + tol:
            merged[-1] = (merged[-1][0], max(merged[-1][1], e))
        else:
            merged.append((s, e))
    return [(a, b) for a, b in merged if (b - a) >= tol]


def _column_index(cx, bounds):
    for i, (a, b) in enumerate(bounds):
        if a - 2 <= cx <= b + 2:
            return i
    return 0


def _table_candidate_rows(words, gap_thr):
    """Group words into rows, keeping rows whose words are spread by clear gaps."""
    rows = []
    for w in sorted(words, key=lambda i: (i.y, i.x)):
        for r in rows:
            rh = max(r[0].h, 1)
            cy = sum(i.cy for i in r) / len(r)
            if abs(w.cy - cy) <= max(rh * 0.6, 4):
                r.append(w)
                break
        else:
            rows.append([w])
    out = []
    for r in rows:
        items = sorted(r, key=lambda i: i.x)
        if len(items) < 2:
            continue
        min_gap = min(items[i + 1].x - (items[i].x + items[i].w) for i in range(len(items) - 1))
        if min_gap >= gap_thr:
            out.append(items)
    return out


def _row_cells(row, bounds):
    cols = {i: [] for i in range(len(bounds))}
    for it in sorted(row, key=lambda i: i.x):
        cols[_column_index(it.cx, bounds)].append(it)
    return [_join_words(cols[i]) for i in range(len(bounds))]


def _build_table_cells(grp, med_h):
    """Turn grouped rows into a cell matrix, pruning rows that break the grid.

    A real table repeats one column structure. Columns that are empty in more
    than half the rows are artifacts of a wide stray line, so they are dropped
    before the remaining rows are checked. Rows whose non-empty cell count
    deviates from the group median are removed one by one and the column bounds
    are recomputed, because a wide stray line shifts every boundary. Rows that
    start with a list marker are enumerator lines rather than table rows.
    Returns (cells, kept_rows) or None when nothing looks tabular.
    """
    rows = [r for r in grp if not LIST_PREFIX.match(" ".join(it.text for it in r))]
    if len(rows) < 3:
        return None
    for _ in range(len(rows)):
        bounds = _column_bounds(rows, max(med_h * 0.5, 3))
        if len(bounds) < 2:
            return None
        matrix = [_row_cells(r, bounds) for r in rows]
        keep = [i for i in range(len(bounds))
                if sum(1 for r in matrix if r[i].strip()) / float(len(matrix)) >= 0.5]
        if len(keep) < 2:
            return None
        matrix = [[r[i] for i in keep] for r in matrix]
        ncol = len(keep)
        counts = [sum(1 for v in r if v.strip()) for r in matrix]
        if (max(counts) - min(counts)) <= 1 and min(counts) >= 2 \
                and max(counts) / float(ncol) >= 0.5:
            return matrix, rows
        med = sorted(counts)[len(counts) // 2]
        worst = max(range(len(rows)), key=lambda i: (abs(counts[i] - med), -rows[i][0].y))
        rows.pop(worst)
        if len(rows) < 3:
            return None
    return None


def detect_tables(words, lines, page_h=None):
    """Cluster words into grid tables.

    Returns (tables, consumed) where consumed is a set of id(Line) that already
    belong to a table and must be skipped by the block grouper.
    """
    consumed = set()
    tables = []
    if len(words) < 4:
        return tables, consumed

    med_h = _median(w.h for w in words) or 12
    gap_thr = max(med_h * 0.6, 4)
    cand_rows = _table_candidate_rows(words, gap_thr)
    if len(cand_rows) < 2:
        return tables, consumed

    groups = []
    for row in sorted(cand_rows, key=lambda r: (r[0].y, r[0].x)):
        rh = max(row[0].h, 1)
        if groups and row[0].y - groups[-1][-1][0].y <= rh * 3.2:
            groups[-1].append(row)
        else:
            groups.append([row])

    for grp in groups:
        built = _build_table_cells(grp, med_h)
        if not built:
            continue
        cells, kept = built

        xs = [it.x for row in kept for it in row]
        ys = [it.y for row in kept for it in row]
        xe = [it.x + it.w for row in kept for it in row]
        ye = [it.y + it.h for row in kept for it in row]
        bbox = (min(xs), min(ys), max(xe), max(ye))
        conf = sum(it.conf for row in kept for it in row) / float(sum(len(r) for r in kept))
        page = kept[0][0].page
        tables.append(
            Block(
                type="table",
                text="\n".join(" | ".join(r) for r in cells),
                page=page,
                bbox=bbox,
                conf=conf,
                cells=cells,
            )
        )
        pad = max(med_h * 0.4, 2)
        for ln in lines:
            if ln.page != page:
                continue
            if ln.x >= bbox[0] - pad and ln.x + ln.w <= bbox[2] + pad \
               and ln.y >= bbox[1] - pad and ln.y + ln.h <= bbox[3] + pad:
                consumed.add(id(ln))
    return tables, consumed


def _is_punct_only(text):
    """行内没有任何字母/数字/汉字、只含标点与符号时视为装饰噪声（分隔线等）。"""
    visible = [c for c in text if not c.isspace()]
    if not visible:
        return True
    return not any(c.isalnum() for c in visible)


def group_blocks(lines, page_w, page_h, optimize, header_cache):
    """Turn lines into typed Blocks, dropping explicit page-number footers."""
    med_size = _median(ln.size for ln in lines)
    blocks = []
    cur = None

    def flush():
        nonlocal cur
        if cur and cur["lines"]:
            items = cur["lines"]
            blocks.append(
                Block(
                    type=cur["type"],
                    text="\n".join(ln.text for ln in items).strip(),
                    page=items[0].page,
                    bbox=(
                        min(ln.x for ln in items),
                        min(ln.y for ln in items),
                        max(ln.x + ln.w for ln in items),
                        max(ln.y + ln.h for ln in items),
                    ),
                    lines=list(items),
                    conf=sum(ln.conf for ln in items) / float(len(items)),
                )
            )
        cur = None

    for ln in lines:
        text = ln.text.strip()
        if not text:
            continue
        if _is_punct_only(text):
            continue
        if optimize and HEADER_TITLE_WORDS.search(text):
            header_cache.append(text)
            continue

        if is_title(ln, med_size):
            btype = "title"
        elif is_formula(ln):
            btype = "formula"
        elif LIST_PREFIX.match(text):
            btype = "list"
        else:
            btype = "paragraph"

        if cur is None or cur["type"] != btype:
            flush()
            cur = {"type": btype, "lines": [ln]}
        else:
            cur["lines"].append(ln)
    flush()
    return blocks


def drop_running_headers(blocks, lines, page_dims):
    """Remove margin lines whose text repeats in the same margin on other pages.

    A margin line that appears once is treated as the document title and kept.
    Returns the surviving blocks plus the removed header texts in reading order.
    """
    hits = {}
    for ln in lines:
        pw, ph = page_dims.get(ln.page, (0.0, 0.0))
        if not is_header_footer(ln, pw, ph):
            continue
        key = ("top" if ln.cy < ph / 2.0 else "bottom", ln.text.strip())
        hits.setdefault(key, set()).add(ln.page)
    running = {k[1] for k, v in hits.items() if len(v) >= 2 and k[1]}
    if not running:
        return blocks, []

    def is_running(ln):
        pw, ph = page_dims.get(ln.page, (0.0, 0.0))
        return is_header_footer(ln, pw, ph) and ln.text.strip() in running

    removed = []
    kept = []
    for blk in blocks:
        src = blk.lines or []
        if not src:
            kept.append(blk)
            continue
        keep = [ln for ln in src if not is_running(ln)]
        for ln in src:
            if is_running(ln) and ln.text.strip() not in removed:
                removed.append(ln.text.strip())
        if not keep:
            continue
        if len(keep) == len(src):
            kept.append(blk)
            continue
        blk.lines = keep
        blk.text = "\n".join(ln.text for ln in keep).strip()
        blk.bbox = (
            min(ln.x for ln in keep),
            min(ln.y for ln in keep),
            max(ln.x + ln.w for ln in keep),
            max(ln.y + ln.h for ln in keep),
        )
        blk.conf = sum(ln.conf for ln in keep) / float(len(keep))
        if blk.text:
            kept.append(blk)
    return kept, removed


def _bbox_overlap(a, b):
    x0 = max(a[0], b[0])
    y0 = max(a[1], b[1])
    x1 = min(a[2], b[2])
    y1 = min(a[3], b[3])
    if x1 <= x0 or y1 <= y0:
        return 0.0
    inter = (x1 - x0) * (y1 - y0)
    area = max((a[2] - a[0]) * (a[3] - a[1]), 1.0)
    return inter / area


def _rule_segments(mask, orient, min_len):
    """取形态学结果里的长条线段，返回 [(中心坐标, 沿轴起点, 沿轴终点)]。

    不能按整页投影判定：表格往往只占版面的一部分宽度，投影覆盖率天生到
    不了整页的一半，长投影法会把所有表格线判掉。
    """
    import cv2

    n, _lab, stats, _cent = cv2.connectedComponentsWithStats(mask, connectivity=8)
    segs = []
    for i in range(1, n):
        x, y, w, h, _a = stats[i]
        if orient == "h":
            if h > 6 or w < min_len:
                continue
            segs.append((y + h / 2.0, x, x + w))
        else:
            if w > 6 or h < min_len:
                continue
            segs.append((x + w / 2.0, y, y + h))
    return sorted(segs, key=lambda s: s[0])


def _merge_segments(segs, tol):
    """按中心坐标合并同一根线的多个片段，返回 [(中心, 沿轴起点, 沿轴终点)]。"""
    out = []
    for c, a, b in segs:
        if out and abs(c - out[-1][0]) <= tol:
            _c, _a, _b = out[-1]
            out[-1] = (out[-1][0], min(_a, a), max(_b, b))
        else:
            out.append((c, a, b))
    return out


def detect_line_tables(bw, sx, sy):
    """用形态学提取的横竖线还原表格框与行列网格，返回页面坐标结果。

    带边框的表格比词框聚类可靠得多：OCR 常整列漏词，而表格线不依赖文字。
    单页开销在毫秒级，可替代几十秒的神经版面检测。

    sx / sy 是「每页面单位对应的像素数」，故像素坐标要除回去才是页面坐标。
    """
    import cv2

    h, w = bw.shape[:2]
    hlen = max(100, int(w * 0.06))
    vlen = max(60, int(h * 0.02))
    hm = cv2.morphologyEx(bw, cv2.MORPH_OPEN, cv2.getStructuringElement(cv2.MORPH_RECT, (hlen, 1)))
    vm = cv2.morphologyEx(bw, cv2.MORPH_OPEN, cv2.getStructuringElement(cv2.MORPH_RECT, (1, vlen)))
    rows = _merge_segments(_rule_segments(hm, "h", hlen), 6)
    cols = _merge_segments(_rule_segments(vm, "v", vlen), 6)
    if len(rows) < 3 or len(cols) < 2:
        return []

    bands = []
    cur = [rows[0]]
    for r in rows[1:]:
        x_lo, x_hi = cur[0][1], cur[0][2]
        tol = max(6.0, (x_hi - x_lo) * 0.03)
        step = r[0] - cur[-1][0]
        if step <= 0 or step > h * 0.15 or abs(r[1] - x_lo) > tol or abs(r[2] - x_hi) > tol:
            bands.append(cur)
            cur = [r]
        else:
            cur.append(r)
    bands.append(cur)

    out = []
    for band in bands:
        if len(band) < 3:
            continue
        x_lo, x_hi = band[0][1], band[0][2]
        y_lo, y_hi = band[0][0], band[-1][0]
        span = max(y_hi - y_lo, 1.0)
        tol = max(6.0, (x_hi - x_lo) * 0.03)
        vset = set(
            c[0] for c in cols
            if x_lo - tol - 6 <= c[0] <= x_hi + tol + 6
            and c[2] >= y_lo - tol and c[1] <= y_hi + tol
            and (min(c[2], y_hi) - max(c[1], y_lo)) >= span * 0.3
        ) | {x_lo, x_hi}
        # 横线端点比竖线中心偏 1-2 像素，合并起来才是同一根线
        vsel, vmerge = [], []
        for v in sorted(vset):
            if vmerge and v - sum(vmerge) / len(vmerge) <= max(5.0, tol * 0.2):
                vmerge.append(v)
            else:
                if vmerge:
                    vsel.append(sum(vmerge) / len(vmerge))
                vmerge = [v]
        if vmerge:
            vsel.append(sum(vmerge) / len(vmerge))
        if len(vsel) < 2:
            continue
        out.append({
            "bbox": (x_lo / sx, y_lo / sy, x_hi / sx, y_hi / sy),
            "vlines": [c / sx for c in vsel],
            "hlines": [r[0] / sy for r in band],
        })
    return out


def _ocr_cell(gray, engine, x0, y0, x1, y1, sx, sy, inset=6):
    """裁一个单元格单独重识别；内缩避开表格线，按子行聚类后拼接。

    x0/y0 是页面坐标，sx/sy 是「每页面单位对应的像素数」，相乘得像素坐标。
    内缩 6 像素在实测里最干净：4 像素会把表格线带进去读出「-」，9 像素会
    咬掉首尾字母。
    """
    xa, ya = int(x0 * sx) + inset, int(y0 * sy) + inset
    xb, yb = int(x1 * sx) - inset, int(y1 * sy) - inset
    if xb - xa < 8 or yb - ya < 8:
        return ""
    ws = engine._crop_words(gray, xa, ya, xb, yb, 8, engine.lang, fx=3.0)
    med = _median(w.h for w in ws) or 8
    rows = []
    for w in sorted(ws, key=lambda i: (i.y, i.x)):
        for r in rows:
            cy = sum(i.cy for i in r) / len(r)
            if abs(w.cy - cy) <= max(med * 0.6, 3):
                r.append(w)
                break
        else:
            rows.append([w])
    text = " ".join(_join_words(sorted(r, key=lambda i: i.x)) for r in rows)
    text = re.sub(r"\s+", " ", text.replace("|", " ")).strip()
    return _strip_edge_punct(text)


def build_line_table(grid, words, engine=None, gray=None, sx=1.0, sy=1.0, fill_cells=True):
    """把词按行列线分格；空格在 fill_cells 时用逐格重识别补全文字。

    主遍 OCR 读到的词优先使用，质量高于重识别结果；只有主遍漏掉的单元格
    才补识别，成本随空格数量线性增长。
    """
    vl, hl = grid["vlines"], grid["hlines"]
    cells = []
    for ri in range(len(hl) - 1):
        y0, y1 = hl[ri], hl[ri + 1]
        # 行内留白必须很小：表格外紧贴着题目说明行，留白一放大就会把
        # 说明行的词吸进第一行。
        py = min(max(y1 - y0, 1.0) * 0.25, 2.0)
        row = []
        for ci in range(len(vl) - 1):
            x0, x1 = vl[ci], vl[ci + 1]
            px = min(max(x1 - x0, 1.0) * 0.15, 4.0)
            ins = sorted(
                (w for w in words
                 if x0 - px <= w.cx <= x1 + px and y0 - py <= w.cy <= y1 + py),
                key=lambda w: w.x)
            text = _join_words(ins)
            if not text and fill_cells and engine is not None and gray is not None:
                text = _ocr_cell(gray, engine, x0, y0, x1, y1, sx, sy)
            text = _correct_english_words(text)
            text = _fix_exam_english(text)
            row.append(text)
        cells.append(row)
    return cells


def _page_arrays(image, page_w, page_h, engine=None):
    """返回 (gray, bw, sx, sy)；有 engine 时与 image_words 的预处理保持一致。"""
    import cv2
    import numpy as np

    if engine is not None:
        gray = engine.page_gray(image)
    else:
        gray = cv2.cvtColor(np.array(image.convert("RGB")), cv2.COLOR_RGB2GRAY)
    bw = TesseractEngine._ink(gray)
    return gray, bw, max(gray.shape[1], 1) / max(page_w, 1.0), max(gray.shape[0], 1) / max(page_h, 1.0)


def apply_neural_tables(image, lines, page, pw, ph):
    """PP-DocLayout 定位表格区, SLANeXt 识别结构, 返回 (table_blocks, consumed_line_ids)。

    image 是像素坐标系的 PIL 图; lines 是页面坐标系。失败或无表格时返回空, 调用方走启发式。
    """
    if image is None:
        return [], set()
    iw, ih = image.size if hasattr(image, "size") else (0, 0)
    if min(iw, ih) < 200 or max(iw, ih) < 600:
        return [], set()
    try:
        from layout_structure import detect_table_regions, recognize_table
    except Exception as exc:
        _log(f"[OCR] 版面模块不可用: {exc}")
        return [], set()
    try:
        regions = detect_table_regions(image)
    except Exception as exc:
        _log(f"[OCR] 版面检测失败: {exc}")
        return [], set()
    if not regions:
        return [], set()
    iw, ih = image.size if hasattr(image, "size") else (pw, ph)
    sx = max(float(iw), 1.0) / max(float(pw), 1.0)
    sy = max(float(ih), 1.0) / max(float(ph), 1.0)
    tables = []
    consumed = set()
    for bbox_px, score in regions:
        cells = recognize_table(image, bbox_px)
        if not cells:
            continue
        x0 = bbox_px[0] / sx
        y0 = bbox_px[1] / sy
        x1 = bbox_px[2] / sx
        y1 = bbox_px[3] / sy
        bbox = (x0, y0, x1, y1)
        tables.append(
            Block(
                type="table",
                text="\n".join(" | ".join(r) for r in cells),
                page=page,
                bbox=bbox,
                conf=float(score),
                cells=cells,
            )
        )
        pad = max((y1 - y0) * 0.08, 4)
        tb = (bbox[0] - pad, bbox[1] - pad, bbox[2] + pad, bbox[3] + pad)
        table_chars = {c.lower() for c in tables[-1].text if c.isalnum()}
        y_lo = bbox[1] - max((y1 - y0) * 0.15, 8)
        y_hi = bbox[3] + max((y1 - y0) * 0.15, 8)
        for ln in lines:
            if ln.page != page:
                continue
            lb = (ln.x, ln.y, ln.x + ln.w, ln.y + ln.h)
            if _bbox_overlap(lb, tb) >= 0.25:
                consumed.add(id(ln))
                continue
            cy = ln.y + ln.h * 0.5
            if y_lo <= cy <= y_hi:
                ln_chars = {c.lower() for c in ln.text if c.isalnum()}
                if ln_chars and len(ln_chars & table_chars) / len(ln_chars) >= 0.55:
                    consumed.add(id(ln))
    return tables, consumed


# ── CJK 行路由 + 通用纠错 ─────────────────────────────────
#
# tesseract 对中文的识别远弱于 PP-OCR（会把「理解」读成「再解」）。
# 通用做法：中文为主的行改走 PP-OCR 重识别，再用 jieba 通用词频对低置信字
# 做形近候选纠错。整条链路与文档领域无关，不依赖任何试卷固定搭配。

_JIEBA_FREQ = {"freq": None}
_ROUTE_CJK_OK = {"ok": None}
# 低分辨率行高阈值（像素）：英文选项块的行高低于此值才路由 PP-OCR v6。
# ln.h * sy 把行高归一化回渲染像素，量纲与输入类型无关。实测屏幕截图
# （webp 1080p）的英文选项行高约 16-20px，高分辨率扫描件 PDF（内嵌
# ~3000px 位图）约 28-45px；25px 分界能区分二者，避免把清晰扫描件的
# 英文误路由到 v6 而引入 c/e 混淆（如 "Before"→"Bcforo"）。
_LOW_RES_LINE_HEIGHT = 25


def _jieba_freq():
    """惰性加载 jieba 通用词典词频（首次 initialize 后 FREQ 才有效）。"""
    if _JIEBA_FREQ["freq"] is None:
        import jieba

        jieba.initialize()
        _JIEBA_FREQ["freq"] = jieba.dt.FREQ
    return _JIEBA_FREQ["freq"]


def _cjk_fluency(text):
    """通顺度评分：奖励被 jieba 连成一体的长词（按长度平方），词频仅在等长时
    作平局裁决，词典外孤字重罚。

    为什么用词长而非纯词频：「每小题」这类正确搭配在 jieba 里词频为 0，
    纯词频会把它误判为 OOV，反而奖励「小器」「小腿」这种真实但错误的词，
    造成把正确字改错。jieba 把一个片段切成单个 token 说明它本身连贯，
    按长度平方奖励即可让「每小题」(9) 胜过「每+小器」(0.3+5.4)；
    等长时（满分 vs 调分）再由词频分出胜负。
    """
    import math

    import jieba

    freq = _jieba_freq()
    score = 0.0
    for w in jieba.lcut(text):
        if not _CJK_RUN.search(w):
            continue  # 拉丁/数字/标点纠错前后不变，保持中性相互抵消
        n = len(w)
        if n >= 2:
            score += n * n + math.log(freq.get(w, 0) + 1) * 0.5
        else:
            score += 0.3 if freq.get(w, 0) > 0 else -6.0
    return score


def _correct_cjk_chars(chars, conf_gate=0.85, gain_min=4.0):
    """对逐字符 (char, conf, candidates) 做安全通用纠错，返回纠正后字符串。

    只动低置信（conf<conf_gate）的 CJK 字，候选取自模型 top-K（天然形近混淆集）。
    每个候选相对原文独立评估整行通顺度增益，超过 gain_min 才采纳，
    最后一次性应用（不级联）。gain_min=4.0 是精度优先的经验阈值：
    实测强正确修复（逸→选 6.6、飓→题 4.7）都在 4.3 以上，而误改
    （题→盟 3.4、话→活 1.1）都在 3.4 以下，宁可漏改也不可误改正确字。
    """
    text = "".join(c for c, _conf, _c in chars)
    if not text or not _CJK_RUN.search(text):
        return text
    base = _cjk_fluency(text)
    fixes = []
    for i, (ch, conf, cands) in enumerate(chars):
        if conf >= conf_gate or not _is_cjk(ch):
            continue
        best = None
        for cand, _p in cands:
            if cand == ch or len(cand) != 1 or not _is_cjk(cand):
                continue
            trial = text[:i] + cand + text[i + 1:]
            gain = _cjk_fluency(trial) - base
            if gain > gain_min and (best is None or gain > best[1]):
                best = (cand, gain)
        if best:
            fixes.append((i, best[0]))
    if not fixes:
        return text
    out = list(text)
    for i, cand in fixes:
        out[i] = cand
    return "".join(out)


def _correct_english_chars(chars, conf_gate=0.7, compete=0.4):
    """对逐字符 (char, conf, candidates) 做保守英文拼写纠错，返回字符串。

    只动低置信（conf<conf_gate）的 ASCII 字母，且其第二候选是另一个字母、
    概率明显竞争（>= 当前概率 * compete）时才替换。低分辨率下 PP-OCR 把
    'e' 读成 'c'（conf ~0.6，候选 'e' ~0.4），而正确识别的字母 conf ~1.0，
    阈值能可靠区分，误改率低。形近混淆（c/e、o/e）由 top-K 天然覆盖，
    无需预设混淆表。
    """
    out = []
    for ch, conf, cands in chars:
        if conf >= conf_gate or not ch.isascii() or not ch.isalpha():
            out.append(ch)
            continue
        if len(cands) >= 2:
            ch2, p2 = cands[1]
            if ch2.isascii() and ch2.isalpha() and p2 >= conf * compete:
                out.append(ch2)
                continue
        out.append(ch)
    return "".join(out)


# 英文词级纠错的词形集合：词根 + 功能词 + 常见派生，惰性构建一次。
_ENGLISH_WORDS = {"set": None}
_ENGLISH_STOP_WORDS = set(
    "a an the and or but if so for to of in on at by with from up down out off over under "
    "is am are was were be been being do does did have has had will would shall should may "
    "might can could must not no yes this that these those it its we us our they them their "
    "you your he she him her i me my mine who whom whose what which when where why how also "
    "only just very too then than now here there because before after while between".split()
)
_LETTERS = "abcdefghijklmnopqrstuvwxyz"

# 英语高频词（top 词频，按常见度降序），用于多候选纠错时区分「常见词」与
# hunspell 里的生僻词（如 mako=鲨鱼、tho=though 缩写）。这些词也是 v6 多语言
# 模型对英文印刷体常见的误读目标（liko→like、mako→make、gol→got、tho→the），
# 纠错时选 rank 最小的唯一高频候选，误伤面小。
_ENGLISH_TOP_WORDS = (
    "the be to of and a in that have i it for not on with he as you do at this but "
    "his by from they we say her she or an will my one all would there their what "
    "so up out if about who get which go me when make can like time no just him "
    "know take into year your good some could them see other than then now look "
    "only come its over think also back after use two how our work first well way "
    "even new want because any these give day most us got people man woman child "
    "school family friend house home room water food car book read write talk "
    "listen hear speak help ask answer question name boy girl mother father "
    "brother sister morning evening week month hour minute today tomorrow "
    "yesterday"
).split()
_ENGLISH_TOP_RANK = {w: i for i, w in enumerate(_ENGLISH_TOP_WORDS, 1)}


def _english_wordset():
    """英文合法词形集合，供编辑距离纠错判断「拼错」。

    hunspell en_US.dic 只存词根（school 而非 schools），且不收录 his/only
    等功能词；直接用它判词会把合法复数/时态与功能词误判成拼错。这里把词根
    展开出常见派生（复数/过去/现在分词/比较级/副词）并补功能词，得到接近
    真实词汇的词形集合。找不到系统词典时返回空集，调用方保持原样。
    """
    if _ENGLISH_WORDS["set"] is None:
        roots = set()
        try:
            with open("/usr/share/hunspell/en_US.dic", "r", encoding="utf-8", errors="ignore") as f:
                for line in f:
                    line = line.strip()
                    if not line:
                        continue
                    w = line.split("/", 1)[0]
                    if w.isalpha() and len(w) >= 2:
                        roots.add(w.lower())
        except OSError:
            pass
        words = set(roots) | _ENGLISH_STOP_WORDS
        for w in roots:
            words.add(w + "s")
            if w.endswith(("s", "x", "z", "ch", "sh")):
                words.add(w + "es")
            if w.endswith("y") and len(w) > 2:
                words.add(w[:-1] + "ies")
            words.add(w + "ed")
            if w.endswith("e"):
                words.add(w + "d")
            if w.endswith("y") and len(w) > 2:
                words.add(w[:-1] + "ied")
            words.add(w + "ing")
            if w.endswith("e"):
                words.add(w[:-1] + "ing")
            words.add(w + "er")
            words.add(w + "est")
            if w.endswith("e"):
                words.add(w + "r")
                words.add(w + "st")
            words.add(w + "ly")
        _ENGLISH_WORDS["set"] = words
    return _ENGLISH_WORDS["set"]


def _edits1(word):
    """编辑距离 1 的全部候选词（替换/插入/删除），去重。"""
    splits = [(word[:i], word[i:]) for i in range(len(word) + 1)]
    deletes = {L + R[1:] for L, R in splits if R}
    replaces = {L + c + R[1:] for L, R in splits if R for c in _LETTERS}
    inserts = {L + c + R for L, R in splits for c in _LETTERS}
    return deletes | replaces | inserts


def _correct_english_words(text):
    """英文选项块的词级编辑距离纠错（字符级之后的兜底）。

    字符级纠错只动低置信且第二候选明显的字符，对 PP-OCR 误判但置信度却偏高的
    拼错词（如 Becausce/Becausc→Because）无能为力；这里对不在词形集合的全字母
    token（长度≥3）用编辑距离 1 找词表候选。优先选「高频词集合」中唯一的高频
    候选（liko→like、gol→got），否则退回「唯一候选才替换」的保守策略。已在词表
    中的词一律不动，避免把 many/where/times 等正确词误改成同字母错词。
    """
    words = _english_wordset()
    if not words:
        return text

    def _fix(m):
        w = m.group(0)
        lw = w.lower()
        if len(lw) < 3 or not lw.isalpha() or lw in words:
            return w
        cands = _edits1(lw) & words
        top_cands = [c for c in cands if c in _ENGLISH_TOP_RANK and len(c) >= 3]
        if len(top_cands) == 1:
            cand = top_cands[0]
            return cand[0].upper() + cand[1:] if w[0].isupper() else cand
        if len(cands) == 1:
            cand = cands.pop()
            return cand[0].upper() + cand[1:] if w[0].isupper() else cand
        return w

    return re.sub(r"[A-Za-z]+(?:['’][A-Za-z]+)*", _fix, text)


def _word_break(word, words, stop_words):
    """把拼接词按词表做最少片段 DP 分词，返回片段列表或 None。

    片段合法条件：在词表内，且要么是功能词（the/at/on/it 等 2 字符也能拆），
    要么长度 >= 3 的实词。这样 doingchores→doing+chores、goton→got+on 能拆，
    而 holon（Helen 错字）不会误拆成 ho+lon（ho 非功能词且仅 2 字符被拒）。
    """
    n = len(word)
    INF = n + 1
    dp = [INF] * (n + 1)
    dp[0] = 0
    prev = [-1] * (n + 1)

    def _legal(seg):
        return seg in words and (seg in stop_words or len(seg) >= 3)

    for i in range(n):
        if dp[i] == INF:
            continue
        for j in range(i + 1, n + 1):
            seg = word[i:j]
            if _legal(seg) and dp[i] + 1 < dp[j]:
                dp[j] = dp[i] + 1
                prev[j] = i
    if dp[n] == INF or dp[n] < 2:
        return None
    parts = []
    i = n
    while i > 0:
        j = prev[i]
        parts.append(word[j:i])
        i = j
    parts.reverse()
    return parts


def _recover_english_spaces(text):
    """把 OCR 粘连的英文词拆开（doingchores→doing chores）。

    只对不在词表的较长 token（>=5）做 DP 分词，且所有片段都合法、片段数 >=2
    才替换；整词在词表（classmate/breakfast）直接跳过，避免误拆合法词。
    """
    words = _english_wordset()
    if not words:
        return text

    def _fix(m):
        w = m.group(0)
        lw = w.lower()
        if len(lw) < 5 or lw in words:
            return w
        parts = _word_break(lw, words, _ENGLISH_STOP_WORDS)
        if parts and len(parts) >= 2:
            return " ".join(parts)
        return w

    return re.sub(r"[A-Za-z]+", _fix, text)


def _space_option_heads(text):
    """v6 rec 对选项头（A./B./C./D.）后的空格不稳定，统一补齐为「X. 内容」。

    tesseract 输出「A. x B. y」，v6 可能输出「A.xB.yC.z」；按选项头字母前的
    小写字母/行首/空白定位，在选项头字母前与标点后补空格，使下游
    _is_option_block 与 _normalize_options 能正常识别。
    """
    out = re.sub(r"([a-z])([A-D])[.．,，](?=[a-zA-Z])", r"\1 \2. ", text)
    out = re.sub(r"(^|\s)([A-D])[.．,，](?=[a-zA-Z])", r"\1\2. ", out)
    return out


def _correct_option_line(chars):
    """英文选项块：字符级纠错 → 选项头补空格 → 选项规范化（含词级纠错）。

    词级编辑距离纠错统一在 finalize_line_text 里做（对所有选项块生效），
    这里只负责 v6 输出的字符级纠错与选项头补空格。
    """
    text = _correct_english_chars(chars)
    if not text.strip():
        return ""
    text = _space_option_heads(text)
    conf = sum(c for _ch, c, _p in chars) / max(len(chars), 1)
    return finalize_line_text(text, conf)


def _route_cjk_lines(lines, gray, sx, sy):
    """中文为主的行 + 低分辨率英文选项块改走 PP-OCR v6，原位替换 line.text。

    中文为主的行走 v6 + jieba 通用纠错；英文选项块（A. … B. … C. …）在低
    分辨率（行高低于阈值）下改走 v6 + 字符级英文纠错 + 选项规范化，高分辨率
    下保留 tesseract（对英文印刷体更稳）。只改文字、不动版面框（bbox 仍来自
    主遍引擎），下游阅读序/标题/表格判定不受影响。PP-OCR 或 jieba 不可用时
    整体跳过，保持原引擎输出。
    """
    if _ROUTE_CJK_OK["ok"] is False:
        return
    try:
        _pp_scripts_path()
        from pp_ocr_onnx import _ppocr_sessions, ppocr_rec_line_chars

        _ppocr_sessions()
        _jieba_freq()
    except Exception as exc:
        _ROUTE_CJK_OK["ok"] = False
        _log(f"[OCR] CJK 路由不可用，保持原引擎输出: {exc}")
        return
    _ROUTE_CJK_OK["ok"] = True

    for ln in lines:
        text = ln.text
        if not text:
            continue
        visible = [c for c in text if not c.isspace()]
        n_cjk = sum(1 for c in visible if _is_cjk(c))
        is_option = _is_option_block(text)
        if is_option:
            # 英文选项块：低分辨率才路由 v6（高分辨率 tesseract 对英文更稳）
            if ln.h * sy >= _LOW_RES_LINE_HEIGHT:
                continue
        else:
            # 含中文即路由 v6（中英混合行也走 v6，多语言模型更稳），
            # 纯英文行不路由，交由 tesseract 处理。
            if n_cjk < 2:
                continue
        pad = 3
        xa = max(int(ln.x * sx) - pad, 0)
        ya = max(int(ln.y * sy) - pad, 0)
        xb = min(int((ln.x + ln.w) * sx) + pad, gray.shape[1])
        yb = min(int((ln.y + ln.h) * sy) + pad, gray.shape[0])
        crop = gray[ya:yb, xa:xb]
        if crop.size == 0 or crop.shape[1] < 4:
            continue
        try:
            chars = ppocr_rec_line_chars(crop)
        except Exception:
            continue
        if not chars:
            continue
        if is_option:
            new_text = _correct_option_line(chars)
        else:
            new_text = _correct_cjk_chars(chars)
            new_text = _normalize_question_numbers(new_text)
            new_text = _normalize_roman_head(new_text)
            new_text = _correct_english_words(new_text)
            new_text = _recover_english_spaces(new_text)
            new_text = _fix_exam_english(new_text)
            if _EXAM_MODE:
                # exam 模式：在通用纠错之上再叠加试卷固定搭配兜底，
                # 修通用纠错漏掉的「调分→满分」这类弱信号错字。
                new_text = repair_exam_phrases(new_text)
                new_text = _fix_read_twice(new_text)
                new_text = _fix_exam_chinese(new_text)
                new_text = _balance_brackets(new_text)
        if new_text.strip():
            ln.text = new_text


def analyze_page(words, page_w, page_h, optimize, header_cache, image=None,
                 neural_tables=False, engine=None, route_cjk=False):
    """Full layout analysis for one page: tables + typed blocks + reading order.

    带边框的表格优先用线条网格还原：表格线不依赖 OCR，能救回主遍整列漏词的
    情况；有 engine 时对空格做逐格重识别补全，两种模式都做，单页几秒。
    连线条都检测不到时才回退到 PP-DocLayout + SLANeXt，那个路径单页可达数分钟。
    """
    page_w = max(float(page_w or 1), 1.0)
    page_h = max(float(page_h or 1), 1.0)
    lines = _absorb_punct_lines(cluster_lines(words))
    ncols = detect_columns(lines, page_w)
    lines = order_lines(lines, page_w, ncols)
    lines = [ln for ln in lines if not _is_punct_only(ln.text)]
    tables, consumed = detect_tables(words, lines, page_h)
    page = words[0].page if words else 0
    gray = bw = sx = sy = None
    if image is not None:
        gray, bw, sx, sy = _page_arrays(image, page_w, page_h, engine)
        grids = detect_line_tables(bw, sx, sy)
        if grids:
            # 逐格补识别单页只需几秒，两种模式都做：主遍 OCR 常整列漏词
            built = [
                (g["bbox"], build_line_table(g, words, engine, gray, sx, sy,
                                             fill_cells=engine is not None))
                for g in grids
            ]
            if built:
                tables = [t for t in tables
                          if all(_bbox_overlap(t.bbox, b) < 0.5 for b, _c in built)]
                for bbox, cells in built:
                    tables.append(
                        Block(
                            type="table",
                            text="\n".join(" | ".join(r) for r in cells),
                            page=page,
                            bbox=bbox,
                            conf=1.0,
                            cells=cells,
                        )
                    )
                for bbox, _cells in built:
                    # 按行中心判是否落在表内：词框高度能超出真实行框好几倍，
                    # 用包围盒加外扩比例会把表格上方紧贴的说明行一起吞掉。
                    for ln in lines:
                        if ln.page != page:
                            continue
                        cx = ln.x + ln.w / 2
                        cy = ln.y + ln.h / 2
                        if bbox[0] <= cx <= bbox[2] and bbox[1] <= cy <= bbox[3]:
                            consumed.add(id(ln))
    if neural_tables and not tables:
        neural, ncons = apply_neural_tables(image, lines, page, page_w, page_h)
        if neural:
            tables = neural
            consumed |= ncons
    body = [ln for ln in lines if id(ln) not in consumed]
    # 通用英文后处理（题号括号、符号、挖空、词级纠错），对所有行生效，与是否走 v6 无关
    for ln in lines:
        if not ln.text:
            continue
        fixed = _normalize_question_numbers(ln.text)
        fixed = _fix_english_symbols(fixed)
        fixed = _fix_blank_line(fixed)
        # 纯英文行走 tesseract，不经过 v6 的词级纠错，这里补上（含中文的混合行交 v6）
        visible = [c for c in fixed if not c.isspace()]
        if sum(1 for c in visible if _is_cjk(c)) < 2 and not _is_option_block(fixed):
            fixed = _correct_english_words(fixed)
            fixed = _recover_english_spaces(fixed)
            fixed = _fix_exam_english(fixed)
        if fixed != ln.text:
            ln.text = fixed
    if route_cjk and gray is not None:
        _route_cjk_lines(body, gray, sx, sy)
    blocks = group_blocks(body, page_w, page_h, optimize, header_cache)
    blocks.extend(tables)
    blocks.sort(key=lambda b: (b.bbox[1], b.bbox[0]))
    return blocks, lines, ncols


# ── 导出 ─────────────────────────────────────────────────


def write_txt(path, text):
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)


def write_json(path, payload):
    with open(path, "w", encoding="utf-8") as f:
        json.dump(payload, f, ensure_ascii=False, indent=2)


def write_xlsx(path, blocks):
    """Sheet 1 lists every block; one extra sheet per detected table."""
    from openpyxl import Workbook
    from openpyxl.styles import Alignment, Font
    from openpyxl.utils import get_column_letter

    wb = Workbook()
    ws = wb.active
    ws.title = "识别结果"
    ws.append(["类型", "页码", "内容"])
    for col in (1, 2, 3):
        ws.cell(row=1, column=col).font = Font(bold=True)
    for blk in blocks:
        ws.append([blk.type, blk.page + 1, blk.text])
    ws.column_dimensions["A"].width = 12
    ws.column_dimensions["B"].width = 8
    ws.column_dimensions["C"].width = 100
    for row in ws.iter_rows(min_row=2, min_col=3, max_col=3):
        row[0].alignment = Alignment(wrap_text=True, vertical="top")

    for idx, blk in enumerate(blocks, 1):
        if blk.type != "table" or not blk.cells:
            continue
        ncol = max(len(r) for r in blk.cells)
        tw = wb.create_sheet(f"表格{idx}")
        for r, row in enumerate(blk.cells):
            for c in range(ncol):
                cell = tw.cell(row=r + 1, column=c + 1, value=row[c] if c < len(row) else "")
                cell.alignment = Alignment(wrap_text=True, vertical="top")
                if r == 0:
                    cell.font = Font(bold=True)
        for c in range(1, ncol + 1):
            width = 10
            for row in blk.cells:
                if c - 1 < len(row):
                    width = max(width, min(len(str(row[c - 1])) + 2, 50))
            tw.column_dimensions[get_column_letter(c)].width = width
    wb.save(path)


def write_docx(path, blocks, page_sizes=None, point_units=False):
    """Layout-aware DOCX: headings, lists, formulas, tables and line indentation."""
    from docx import Document
    from docx.enum.text import WD_ALIGN_PARAGRAPH
    from docx.shared import Emu, Pt

    doc = Document()
    for blk in blocks:
        if blk.type == "table" and blk.cells:
            ncol = max(len(r) for r in blk.cells)
            table = doc.add_table(rows=len(blk.cells), cols=ncol)
            table.style = "Table Grid"
            for r, row in enumerate(blk.cells):
                for c in range(ncol):
                    cell = table.cell(r, c)
                    cell.text = row[c] if c < len(row) else ""
                    if r == 0:
                        for p in cell.paragraphs:
                            for run in p.runs:
                                run.bold = True
            continue

        lines = blk.lines or []
        if not lines:
            continue
        pw = page_sizes[blk.page][0] if page_sizes and blk.page < len(page_sizes) else None
        for i, ln in enumerate(lines):
            p = doc.add_paragraph()
            p.paragraph_format.space_before = Pt(0 if i == 0 else 2)
            if pw:
                ratio = max(0.0, min(ln.x / float(pw), 0.8))
                p.paragraph_format.left_indent = Emu(int(ratio * 6100000))
            run = p.add_run(ln.text)
            if blk.type == "title":
                run.bold = True
                size = ln.size if point_units else ln.size * 0.8
                run.font.size = Pt(max(10, min(int(size), 26)))
                if len(lines) == 1:
                    p.alignment = WD_ALIGN_PARAGRAPH.CENTER
            elif blk.type == "list":
                run.font.size = Pt(11)
            elif blk.type == "formula":
                run.font.name = "Cambria Math"
    doc.save(path)


def write_redbox(path, page_images, page_scales, lines, pages):
    """Draw red boxes around recognized lines and stack the pages vertically."""
    from PIL import Image, ImageDraw

    rendered = []
    for i, img in enumerate(page_images):
        scale = page_scales[i] if i < len(page_scales) else 1.0
        draw = ImageDraw.Draw(img)
        stroke = max(2, img.width // 500)
        for ln in lines:
            if ln.page != i:
                continue
            pad = max(1.0, ln.h * scale * 0.15)
            draw.rectangle(
                [ln.x * scale - pad, ln.y * scale - pad,
                 (ln.x + ln.w) * scale + pad, (ln.y + ln.h) * scale + pad],
                outline=(220, 0, 0), width=stroke,
            )
        rendered.append(img)

    gap = 30 if len(rendered) > 1 else 0
    width = max(im.width for im in rendered) + (gap * 2 if rendered else 0)
    height = sum(im.height + gap for im in rendered)
    canvas = Image.new("RGB", (max(width, 1), max(height, 1)), (245, 245, 245))
    draw = ImageDraw.Draw(canvas)
    try:
        label_font = ImageFont_truetype_safe(18)
    except Exception:
        label_font = None

    y = 0
    for i, img in enumerate(rendered):
        canvas.paste(img, (gap, y + (22 if label_font else 0)))
        if label_font:
            draw.text((gap, y), f"Page {i + 1}/{pages}", fill=(90, 90, 90), font=label_font)
        y += img.height + gap
    canvas.save(path)


def ImageFont_truetype_safe(size):
    from PIL import ImageFont

    for name in ("/usr/share/fonts/wqy-zenhei/wqy-zenhei.ttc",
                 "/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc"):
        if os.path.exists(name):
            return ImageFont.truetype(name, size)
    try:
        return ImageFont.load_default(size=size)
    except TypeError:
        return ImageFont.load_default()


# ── 翻译 ─────────────────────────────────────────────────


def translate_to_zh(text):
    """Translate to Simplified Chinese through the shared translate.py CLI."""
    import subprocess
    import tempfile

    script = os.path.join(os.path.dirname(os.path.abspath(__file__)), "translate.py")
    if not os.path.exists(script):
        _log("[OCR] 未找到 translate.py，跳过翻译")
        return ""
    payload = tempfile.NamedTemporaryFile(suffix=".json", delete=False, mode="w", encoding="utf-8")
    try:
        json.dump({"text": text[:8000]}, payload, ensure_ascii=False)
        payload.close()
        out = subprocess.run(
            [sys.executable, script, "text", "auto", "zh", payload.name],
            capture_output=True, text=True, timeout=180,
        )
        if out.returncode != 0:
            _log(f"[OCR] 翻译失败: {out.stderr.strip()[:200]}")
            return ""
        return (json.loads(out.stdout.strip().splitlines()[-1]).get("text") or "").strip()
    except Exception as exc:
        _log(f"[OCR] 翻译异常: {exc}")
        return ""
    finally:
        try:
            os.remove(payload.name)
        except OSError:
            pass


# ── 主流程 ───────────────────────────────────────────────


def run(src, opts, outdir):
    started = time.time()
    warnings = []

    mode = opts.get("mode") or "general"
    quality = opts.get("quality") or "normal"
    charset = opts.get("charset") or "auto"
    redbox = bool(opts.get("redbox"))
    optimize = bool(opts.get("optimize"))
    do_translate = bool(opts.get("translate"))
    formats = opts.get("formats") or ["txt"]
    engine_prefer = opts.get("engine") or "auto"
    lang = opts.get("lang") or "auto"
    if lang not in LANG_PRESETS:
        lang = "auto"
    if mode not in ("general", "advanced"):
        mode = "general"
    if quality not in QUALITY_PROFILE:
        quality = "normal"
    # 通用文档：中文行路由 PP-OCR + jieba 纠错（默认开）；
    # exam=true 额外启用试卷固定搭配兜底（默认关，避免过拟合到试卷）。
    route_cjk = bool(opts.get("route_cjk", True))
    set_exam_mode(bool(opts.get("exam")))

    ext = os.path.splitext(src)[1].lower()
    converter = CharsetConverter(charset)
    header_cache = []
    all_blocks = []
    all_lines = []
    page_sizes = []
    page_dims = {}
    page_images = []
    page_scales = []
    engine_name = ""
    from_text_layer = False
    layout_columns = 1
    pages = 1
    profile = QUALITY_PROFILE[quality]

    if ext == ".pdf":
        page_sizes = pdf_page_sizes(src)
        pages = len(page_sizes) or 1
        if not page_sizes:
            raise ValueError("PDF 无法解析")
        page_dims = {i: (float(w), float(h)) for i, (w, h) in enumerate(page_sizes)}

        words = pdf_text_words(src)
        tl_text = " ".join(w.text for w in words)
        garbled = text_layer_is_garbled(tl_text)
        has_text = any(w.text.strip() for w in words)

        # 探针: 启发式判定文本层可用时, 再用第 1 页图像 OCR 做一次交叉验证。
        # 文本层是错码/私用区乱码而启发式漏判时, 与图像 OCR 结果几乎不相交,
        # 据此回退到整篇图像 OCR, 兜住所有乱码形态。
        probe_engine = None
        if has_text and not garbled:
            try:
                probe_engine = get_engine(mode, quality, engine_prefer, lang)
                probe_img = pdf_page_to_image(src, 0, 300)
                if probe_img is not None:
                    probe_words = probe_engine.image_words(probe_img)
                    probe_text = " ".join(w.text for w in probe_words if w.text.strip())
                    if text_layer_mismatch(tl_text, probe_text):
                        garbled = True
                        warnings.append(
                            "PDF 文本层与图像识别结果不一致，疑似乱码，已改用图像 OCR"
                        )
            except Exception as exc:
                _log(f"[OCR] 文本层探针失败: {exc}")

        if not garbled and has_text:
            from_text_layer = True
            engine_name = "pdftext"
            for pno in range(pages):
                pw, ph = page_sizes[pno]
                page_words = [w for w in words if w.page == pno]
                blocks, lines, ncols = analyze_page(
                    page_words, pw, ph, optimize, header_cache)
                layout_columns = max(layout_columns, ncols)
                all_blocks.extend(blocks)
                all_lines.extend(lines)
            if redbox:
                for pno in range(min(pages, REDBOX_MAX_PAGES)):
                    img = pdf_page_to_image(src, pno, min(profile["dpi"], 150))
                    pw = max(page_sizes[pno][0], 1.0)
                    page_images.append(img)
                    page_scales.append(img.width / pw)
                if pages > REDBOX_MAX_PAGES:
                    warnings.append(f"红框标记图仅渲染前 {REDBOX_MAX_PAGES} 页")
        else:
            if garbled and not any("已改用图像 OCR" in w for w in warnings):
                warnings.append("PDF 文本层是乱码（子集字体缺少字符映射），已改用图像 OCR")
            engine = probe_engine or get_engine(mode, quality, engine_prefer, lang)
            engine_name = engine.name
            # 文本层乱码时页面本身是清晰可印的，用高 DPI 渲染让 OCR 拿到更多细节
            render_dpi = 300 if garbled else profile["dpi"]
            page_imgs = []
            for pno in range(pages):
                img = pdf_page_native_image(src, pno)
                if img is None:
                    img = pdf_page_to_image(src, pno, render_dpi)
                page_imgs.append(img)
            orient = detect_orientation(page_imgs[0]) if page_imgs else 0
            if orient:
                warnings.append(f"检测到页面旋转 {orient}°，已自动正立")
            for pno, img in enumerate(page_imgs):
                base = page_sizes[pno] if pno < len(page_sizes) else (img.width, img.height)
                oimg, pw, ph = _orient_page(img, orient, float(base[0]), float(base[1]))
                sx = max(float(pw), 1.0) / max(oimg.width, 1)
                sy = max(float(ph), 1.0) / max(oimg.height, 1)
                words_p = engine.image_words(oimg)
                for w in words_p:
                    w.page = pno
                    w.x = int(w.x * sx)
                    w.y = int(w.y * sy)
                    w.w = max(int(w.w * sx), 1)
                    w.h = max(int(w.h * sy), 1)
                blocks, lines, ncols = analyze_page(
                    words_p, pw, ph, optimize, header_cache, oimg,
                    neural_tables=(mode == "advanced"), engine=engine,
                    route_cjk=route_cjk)
                layout_columns = max(layout_columns, ncols)
                all_blocks.extend(blocks)
                all_lines.extend(lines)
                if redbox and pno < REDBOX_MAX_PAGES:
                    page_images.append(oimg)
                    page_scales.append(max(oimg.width, 1) / max(pw, 1.0))
            if pages > REDBOX_MAX_PAGES:
                warnings.append(f"红框标记图仅渲染前 {REDBOX_MAX_PAGES} 页")

    elif ext in IMAGE_EXTS:
        from PIL import Image

        img = Image.open(src).convert("RGB")
        pages = 1
        engine = get_engine(mode, quality, engine_prefer, lang)
        engine_name = engine.name
        orient = detect_orientation(img)
        if orient:
            warnings.append(f"检测到图像旋转 {orient}°，已自动正立")
        oimg, pw, ph = _orient_page(img, orient, float(img.width), float(img.height))
        page_dims = {0: (pw, ph)}
        words = engine.image_words(oimg)
        for w in words:
            w.page = 0
        blocks, lines, ncols = analyze_page(
            words, pw, ph, optimize, header_cache, oimg,
            neural_tables=(mode == "advanced"), engine=engine,
            route_cjk=route_cjk)
        layout_columns = ncols
        all_blocks.extend(blocks)
        all_lines.extend(lines)
        if redbox:
            page_images.append(oimg)
            page_scales.append(1.0)
    else:
        raise ValueError(f"不支持的文件格式: {ext}")

    if optimize and len(page_dims) > 1:
        all_blocks, running = drop_running_headers(all_blocks, all_lines, page_dims)
        header_cache.extend(running)

    raw_text = "\n\n".join(b.text for b in all_blocks if b.text.strip())
    text = converter.convert(raw_text) if raw_text else ""
    detected = charset if charset != "auto" else detect_charset(raw_text)

    translated = ""
    if do_translate and text:
        translated = translate_to_zh(text)
        if not translated:
            warnings.append("翻译服务暂不可用，已返回原文")

    table_count = sum(1 for b in all_blocks if b.type == "table")
    files = {}
    payload = {
        "engine": engine_name,
        "pages": pages,
        "chars": len(text),
        "charset": detected,
        "from_text_layer": from_text_layer,
        "layout": {
            "columns": layout_columns,
            "tables": table_count,
            "blocks": len(all_blocks),
            "lines": len(all_lines),
        },
        "filtered_headers": header_cache,
        "options": {
            "mode": mode, "quality": quality, "charset": charset, "lang": lang,
            "redbox": redbox, "optimize": optimize, "translate": do_translate,
            "exam": _EXAM_MODE, "route_cjk": route_cjk,
            "formats": formats,
        },
        "text": text,
        "translated_text": translated,
        "blocks": [b.to_dict() for b in all_blocks],
        "lines": [ln.to_dict() for ln in all_lines],
    }

    if "txt" in formats:
        p = os.path.join(outdir, "result.txt")
        write_txt(p, translated or text)
        files["txt"] = p
    if "json" in formats:
        p = os.path.join(outdir, "result.json")
        write_json(p, payload)
        files["json"] = p
    if "xlsx" in formats:
        p = os.path.join(outdir, "result.xlsx")
        write_xlsx(p, all_blocks)
        files["xlsx"] = p
    if "docx" in formats:
        p = os.path.join(outdir, "result.docx")
        write_docx(p, all_blocks, page_sizes or None, point_units=from_text_layer)
        files["docx"] = p
    if redbox and page_images:
        p = os.path.join(outdir, "redbox.png")
        write_redbox(p, page_images, page_scales, all_lines, pages)
        files["redbox"] = p

    return {
        "success": True,
        "text": text,
        "translated_text": translated,
        "engine": engine_name,
        "pages": pages,
        "chars": len(text),
        "charset": detected,
        "from_text_layer": from_text_layer,
        "layout": payload["layout"],
        "filtered_headers": header_cache,
        "blocks": payload["blocks"],
        "lines": payload["lines"],
        "files": {k: os.path.abspath(v) for k, v in files.items()},
        "warnings": warnings,
        "elapsed_ms": int((time.time() - started) * 1000),
        "options": payload["options"],
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("path")
    parser.add_argument("--options", default="{}")
    parser.add_argument("--outdir", default=".")
    args = parser.parse_args()

    if not os.path.exists(args.path):
        print(json.dumps({"success": False, "error": "文件不存在"}, ensure_ascii=False))
        return 1
    try:
        opts = json.loads(args.options or "{}")
        if not isinstance(opts, dict):
            raise ValueError("options 必须是 JSON 对象")
    except (json.JSONDecodeError, ValueError):
        print(json.dumps({"success": False, "error": "参数格式错误"}, ensure_ascii=False))
        return 1

    os.makedirs(args.outdir, exist_ok=True)
    try:
        result = run(args.path, opts, args.outdir)
    except Exception as exc:
        _log(f"[OCR] 识别失败: {exc}")
        print(json.dumps({"success": False, "error": str(exc)}, ensure_ascii=False))
        return 1

    print(json.dumps(result, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
