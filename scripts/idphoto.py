#!/usr/bin/env python3
"""证件照生成 - 参照 HivisionIDPhoto 实现方式重写

管线（与 HivisionIDPhoto 一致）:
  1. 人像抠图 (rembg u2netp) -> RGBA
  2. MTCNN 人脸检测 -> face_rect
  3. 依据 head_measure_ratio / head_height_ratio 计算裁剪框
  4. get_box 检测裁剪后人像边界并修正（左右空隙/头顶距离/底部落下）
  5. 渲染背景色
  6. 缩放到目标标准尺寸
"""
import sys
import json
import os
import io
import math

import numpy as np
import cv2
from PIL import Image, ImageOps

DPI = 300
PX_PER_MM = DPI / 25.4

# 尺寸表: 名称 -> (宽mm, 高mm)
SIZES = {
    "一寸": (25, 35),
    "小一寸": (22, 32),
    "大一寸": (33, 48),
    "小二寸": (35, 45),
    "二寸": (35, 49),
    "大二寸": (40, 50),
    "美签": (51, 51),
    "身份证": (30.3, 37.3),
    "小三寸": (35, 45),
    "三寸": (55, 84),
    "社保": (32, 36),
    "教资": (25, 35),
    "公务员": (25, 35),
    "四六级": (57, 76),
    "日语": (30, 42),
    "五寸": (89, 127),
    "六寸": (102, 152),
}

BACKGROUNDS = {
    "白色": (255, 255, 255),
    "蓝色": (0, 114, 227),
    "深蓝": (0, 81, 174),
    "红色": (237, 28, 36),
    "粉色": (255, 182, 193),
    "灰色": (191, 191, 191),
    "浅蓝": (173, 216, 230),
    "青色": (0, 191, 255),
    "黄色": (255, 204, 0),
}

# HivisionIDPhoto 默认布局参数
HEAD_MEASURE_RATIO = 0.2  # 人脸面积占裁剪面积的期望比值
HEAD_HEIGHT_RATIO = 0.45  # 人脸中心距裁剪框顶部的比例
HEAD_TOP_RANGE = (0.12, 0.1)  # 头顶距照片顶部的范围 (max, min)


def mm_to_px(mm, px_per_mm=PX_PER_MM):
    return max(1, int(round(mm * px_per_mm)))


def remove_background(pil_img):
    """使用 rembg (u2net_human_seg) 去除背景，返回 RGBA 图像。

    u2net_human_seg 是人像分割专用模型，能完整保留躯干与四肢；
    通用的 u2net/u2netp 会把身体局部误判成背景（换底色时身体被吃掉）。
    模型首次使用自动下载到 ~/.rembg/models/ 并缓存（约 170MB）。
    sess_opts 用 ORT_ENABLE_BASIC：跳过全图优化，模型加载从 ~17s 降到亚秒级，
    否则每个请求新建进程加载 170MB 模型会卡 ~36s，页面看起来就像没有响应。

    额外开启 alpha matting（精修半透明发丝）+ decontaminate（去除发丝边缘
    渗进来的旧背景色）：换底色时毛发边缘能随新底色正确换色、无白边/残影。
    matting 不可用或把人物侵蚀到几乎为空时，回退普通后处理，保证人物不丢。
    """
    import onnxruntime as ort
    from rembg import remove, new_session
    opts = ort.SessionOptions()
    # 兼容新旧 onnxruntime：旧版顶层 ORT_ENABLE_BASIC，新版移到 GraphOptimizationLevel
    _basic = getattr(ort, "ORT_ENABLE_BASIC", None)
    if _basic is None:
        _basic = ort.GraphOptimizationLevel.ORT_ENABLE_BASIC
    opts.graph_optimization_level = _basic
    opts.log_severity_level = 3  # ERROR，抑制加载告警
    session = new_session(model_name="u2net_human_seg", sess_opts=opts)

    def _to_rgba(res):
        if isinstance(res, (bytes, bytearray)):
            return Image.open(io.BytesIO(res)).convert("RGBA")
        return res.convert("RGBA")

    try:
        result = _to_rgba(remove(
            pil_img,
            session=session,
            alpha_matting=True,
            alpha_matting_foreground_threshold=240,
            alpha_matting_background_threshold=10,
            alpha_matting_erode_size=8,
            decontaminate=True,
            post_process_mask=True,
        ))
        # matting 过度侵蚀保护：人物有效区域低于 3% 视为失败，回退普通后处理
        a = np.asarray(result.getchannel("A"))
        if float((a > 127).mean()) < 0.03:
            result = _to_rgba(remove(pil_img, session=session, post_process_mask=True))
        return result
    except Exception:  # noqa: BLE001  matting 依赖缺失/异常 → 普通后处理兜底
        return _to_rgba(remove(pil_img, session=session, post_process_mask=True))


class FaceDetector:
    """MTCNN 人脸检测（参照 hivision/creator/face_detector.py detect_face_mtcnn）"""
    def __init__(self):
        from mtcnnruntime import MTCNN
        self.mtcnn = MTCNN()

    def detect(self, img_bgr, scale=2):
        """检测原图中的人脸，返回 face_rect (x, y, w, h) 或 None"""
        try:
            faces = self._detect_once(img_bgr, scale)
            if faces is not None:
                return faces
            # 保险措施：缩放检测失败则用原图再检测一次
            return self._detect_once(img_bgr, 1)
        except Exception:
            return None

    def _detect_once(self, img_bgr, scale):
        if scale > 1:
            small = cv2.resize(
                img_bgr,
                (img_bgr.shape[1] // scale, img_bgr.shape[0] // scale),
                interpolation=cv2.INTER_AREA,
            )
        else:
            small = img_bgr
        faces, _ = self.mtcnn.detect(small, thresholds=[0.8, 0.8, 0.8])
        if faces is None:
            return None
        faces = faces.tolist()
        if len(faces) != 1:
            return None
        face = faces[0]
        left, top, right, bottom = face[0], face[1], face[2], face[3]
        if scale > 1:
            left, top, right, bottom = (v * scale for v in (left, top, right, bottom))
        width = right - left + 1
        height = bottom - top + 1
        return (int(left), int(top), int(width), int(height))


_detector = None
_detector_broken = False

# YuNet 人脸检测模型（OpenCV 官方，Apache-2.0，精度优于 MTCNN）
_YUNET_MODEL = os.path.join(
    os.path.dirname(os.path.abspath(__file__)),
    "..", "models", "face", "face_detection_yunet_2023mar.onnx",
)

_yunet = None

# YuNet 在全分辨率大图上会漏检（人脸在特征图上占比过小）。
# 先缩放到最长边该值再检测，实测 2000x2800 全分辨率 0 检出，缩到 1280 后正常。
_YUNET_MAX_SIDE = 1280


def _yunet_detect_once(img_bgr):
    """在给定分辨率上跑一次 YuNet，返回 face_rect (x, y, w, h) 或 None。"""
    global _yunet
    if _yunet is None:
        _yunet = cv2.FaceDetectorYN_create(_YUNET_MODEL, "", (320, 320), 0.6, 0.3, 5000)
    h, w = img_bgr.shape[:2]
    _yunet.setInputSize((w, h))
    _, faces = _yunet.detect(img_bgr)
    if faces is None or len(faces) == 0:
        return None
    areas = faces[:, 2] * faces[:, 3]
    best = faces[int(np.argmax(areas))]
    return (int(best[0]), int(best[1]), int(best[2]), int(best[3]))


def _detect_face_yunet(img_bgr):
    """YuNet 人脸检测（cv2.FaceDetectorYN），带多尺度重试。

    这不是降级：YuNet 是 OpenCV 官方推荐的现代检测器，精度优于 MTCNN，
    仅依赖 models/face/ 下的 ONNX 模型，零第三方依赖。
    多张时取面积最大的主脸（证件照主体），避免背景误检导致的误判失败。

    多尺度：大图先缩到最长边 _YUNET_MAX_SIDE 检测再把坐标还原回原图，
    缩放失败才用原图重试一次，兼顾大照片与小图特写。
    """
    if not os.path.exists(_YUNET_MODEL):
        return None
    h, w = img_bgr.shape[:2]
    max_side = max(w, h)
    if max_side > _YUNET_MAX_SIDE:
        s = _YUNET_MAX_SIDE / max_side
        small = cv2.resize(img_bgr, (int(w * s), int(h * s)), interpolation=cv2.INTER_AREA)
        box = _yunet_detect_once(small)
        if box is not None:
            x, y, fw, fh = box
            return (int(x / s), int(y / s), int(fw / s), int(fh / s))
    return _yunet_detect_once(img_bgr)


def detect_face(img_bgr):
    global _detector, _detector_broken
    if _detector_broken:
        return _detect_face_yunet(img_bgr)
    if _detector is None:
        try:
            _detector = FaceDetector()
        except ImportError:
            _detector_broken = True
            print("[IDPhoto] mtcnnruntime 不可用，改用 YuNet 人脸检测", file=sys.stderr)
            return _detect_face_yunet(img_bgr)
    return _detector.detect(img_bgr)


def get_box(image, model=1, correction_factor=None, thresh=127):
    """参照 hivision/creator/utils.py get_box

    输入四通道图像，返回最大连续非透明区域的矩形信息。
    model=1 返回 [y_up, y_down, x_left, x_right]（坐标）
    model=2 返回 [y_up, height-y_down, x_left, width-x_right]（距边距离）
    """
    if correction_factor is None:
        correction_factor = [0, 0, 0, 0]
    if not isinstance(image, np.ndarray) or len(cv2.split(image)) != 4:
        raise TypeError("输入的图像必须为四通道 np.ndarray 类型矩阵！")
    if isinstance(correction_factor, int):
        correction_factor = [0, 0, correction_factor, correction_factor]
    elif not isinstance(correction_factor, list):
        raise TypeError("correction_factor 必须为 int 或者 list 类型！")

    _, _, _, mask = cv2.split(image)
    _, mask = cv2.threshold(mask, thresh=thresh, maxval=255, type=0)
    contours, hierarchy = cv2.findContours(mask, cv2.RETR_TREE, cv2.CHAIN_APPROX_SIMPLE)
    temp = np.ones(image.shape, np.uint8) * 255
    cv2.drawContours(temp, contours, -1, (0, 0, 255), -1)
    contours_area = [cv2.contourArea(cnt) for cnt in contours]
    idx = contours_area.index(max(contours_area))
    x, y, w, h = cv2.boundingRect(contours[idx])

    height, width, _ = image.shape
    y_up = y - correction_factor[0] if y - correction_factor[0] >= 0 else 0
    y_down = (
        y + h + correction_factor[1]
        if y + h + correction_factor[1] < height
        else height - 1
    )
    x_left = x - correction_factor[2] if x - correction_factor[2] >= 0 else 0
    x_right = (
        x + w + correction_factor[3]
        if x + w + correction_factor[3] < width
        else width - 1
    )
    if model == 1:
        return [y_up, y_down, x_left, x_right]
    elif model == 2:
        return [y_up, height - y_down, x_left, width - x_right]
    else:
        raise EOFError("请选择正确的模式！")


def detect_distance(value, crop_height, max=0.06, min=0.04):
    """参照 hivision/creator/utils.py detect_distance

    检测人头顶与照片顶部距离是否在适当范围内。
    返回 (status, move_value)：
      status=0 不动；status=1 人像应向上移动(框向下)；status=-1 人像应向下移动(框向上)
    """
    value = value / crop_height
    if min <= value <= max:
        return 0, 0
    elif value > max:
        move_value = value - max
        move_value = int(move_value * crop_height)
        return 1, move_value
    else:
        move_value = min - value
        move_value = int(move_value * crop_height)
        return -1, move_value


def idphotos_cut(x1, y1, x2, y2, img):
    """参照 hivision/creator/photo_adjuster.py IDphotos_cut

    按裁剪框裁剪；超出图像范围的部分用全透明补位。
    """
    crop_size = (y2 - y1, x2 - x1)
    temp_x_1 = temp_y_1 = temp_x_2 = temp_y_2 = 0

    if y1 < 0:
        temp_y_1 = abs(y1)
        y1 = 0
    if y2 > img.shape[0]:
        temp_y_2 = y2
        y2 = img.shape[0]
        temp_y_2 = temp_y_2 - y2
    if x1 < 0:
        temp_x_1 = abs(x1)
        x1 = 0
    if x2 > img.shape[1]:
        temp_x_2 = x2
        x2 = img.shape[1]
        temp_x_2 = temp_x_2 - x2

    background = np.zeros((crop_size[0], crop_size[1], 4), dtype=np.uint8)
    background[
        temp_y_1: crop_size[0] - temp_y_2, temp_x_1: crop_size[1] - temp_x_2
    ] = img[y1:y2, x1:x2]
    return background


def move_bottom(input_image):
    """参照 hivision/creator/photo_adjuster.py move

    当照片底部存在空隙时，将人像下移使底部与画面底部贴合。
    返回 (处理后的图, 下移量)。
    """
    png_img = input_image
    height, width, channels = png_img.shape
    y_low, y_high, _, _ = get_box(png_img, model=2)
    base = np.zeros((y_high, width, channels), dtype=np.uint8)
    png_img = png_img[0: height - y_high, :, :]
    png_img = np.concatenate((base, png_img), axis=0)
    return png_img, y_high


def adjust_photo(matting_bgra, face_rect, standard_size):
    """参照 hivision/creator/photo_adjuster.py adjust_photo

    standard_size: (高, 宽) 像素
    返回裁剪并修正后的四通道图像（尚未缩放至标准尺寸）。
    """
    x, y = face_rect[0], face_rect[1]
    w, h = face_rect[2], face_rect[3]
    height, width = matting_bgra.shape[:2]
    width_height_ratio = standard_size[0] / standard_size[1]

    # Step2. 计算高级参数
    face_center = (x + w / 2, y + h / 2)
    face_measure = w * h
    crop_measure = face_measure / HEAD_MEASURE_RATIO
    resize_ratio = crop_measure / (standard_size[0] * standard_size[1])
    resize_ratio_single = math.sqrt(resize_ratio)
    crop_size = (
        int(standard_size[0] * resize_ratio_single),
        int(standard_size[1] * resize_ratio_single),
    )

    # 裁剪框的定位信息
    x1 = int(face_center[0] - crop_size[1] / 2)
    y1 = int(face_center[1] - crop_size[0] * HEAD_HEIGHT_RATIO)
    y2 = y1 + crop_size[0]
    x2 = x1 + crop_size[1]

    # Step3, 裁剪框的调整
    cut_image = idphotos_cut(x1, y1, x2, y2, matting_bgra)
    cut_image = cv2.resize(cut_image, (crop_size[1], crop_size[0]))
    y_top, y_bottom, x_left, x_right = get_box(
        cut_image.astype(np.uint8), model=2, correction_factor=0
    )

    # Step5. 判定人像位置是否合理
    if x_left > 0 or x_right > 0:
        status_left_right = 1
        cut_value_top = int(((x_left + x_right) * width_height_ratio) / 2)
    else:
        status_left_right = 0
        cut_value_top = 0

    status_top, move_value = detect_distance(
        y_top - cut_value_top,
        crop_size[0],
        max=HEAD_TOP_RANGE[0],
        min=HEAD_TOP_RANGE[1],
    )

    # Step6. 第二轮裁剪
    if status_left_right == 0 and status_top == 0:
        result_image = cut_image
    else:
        result_image = idphotos_cut(
            x1 + x_left,
            y1 + cut_value_top + status_top * move_value,
            x2 - x_right,
            y2 - cut_value_top + status_top * move_value,
            matting_bgra,
        )

    # Step7. 当照片底部存在空隙时，下拉至底部
    result_image, y_high = move_bottom(result_image.astype(np.uint8))

    return result_image


def standard_photo_resize(input_image, size):
    """参照 hivision/creator/photo_adjuster.py standard_photo_resize

    size: (高, 宽)
    当缩放比例 >= 2 时逐级缩放防止像素丢失。
    """
    resize_ratio = input_image.shape[0] / size[0]
    resize_item = int(round(input_image.shape[0] / size[0]))
    if resize_ratio >= 2:
        result_image = input_image
        for i in range(resize_item - 1):
            if i == 0:
                result_image = cv2.resize(
                    input_image,
                    (size[1] * (resize_item - i - 1), size[0] * (resize_item - i - 1)),
                    interpolation=cv2.INTER_AREA,
                )
            else:
                result_image = cv2.resize(
                    result_image,
                    (size[1] * (resize_item - i - 1), size[0] * (resize_item - i - 1)),
                    interpolation=cv2.INTER_AREA,
                )
    else:
        result_image = cv2.resize(
            input_image, (size[1], size[0]), interpolation=cv2.INTER_AREA
        )
    return result_image


def _feather_alpha(bgra, ksize=5):
    """对 alpha 通道做轻微高斯羽化，平滑发丝/轮廓的硬过渡与锯齿。

    对齐"图像生成页换背景"(compose_bg.py) 对 alpha 的 GaussianBlur 处理：
    发丝是半透明边缘，未羽化时缩放后易出现灰边/白边/毛糙；小核(5)在高分辨率
    裁剪图上仅软化 2~3 像素，人物内部 alpha=255 区基本不变。ksize<2 原样返回。
    """
    if ksize < 2:
        return bgra
    b, g, r, a = cv2.split(bgra)
    a = cv2.GaussianBlur(a, (ksize, ksize), 0)
    return cv2.merge([b, g, r, a])


def add_background(bgra, rgb):
    """将透明人像合成到纯色背景（参照 hivision/utils.py add_background pure_color）"""
    b, g, r, a = cv2.split(bgra)
    a_cal = a / 255.0
    bgr = rgb[::-1]
    output = cv2.merge((
        b * a_cal + bgr[0] * (1 - a_cal),
        g * a_cal + bgr[1] * (1 - a_cal),
        r * a_cal + bgr[2] * (1 - a_cal),
    ))
    return output.astype(np.uint8)


def make_idphoto(image_path, out_path, size_key="大一寸", bg_key="白色", hi_res=False):
    """生成证件照主函数（入口签名与 Go 后端保持一致）。

    hi_res=True 时按 600DPI 出图（像素为普通档 4 倍），适合线上上传/冲印；
    普通档 300DPI 是国标打印分辨率。
    """
    if size_key not in SIZES:
        size_key = "一寸"
    if bg_key not in BACKGROUNDS:
        bg_key = "白色"

    out_dpi = DPI * 2 if hi_res else DPI
    ppmm = out_dpi / 25.4
    mm_w, mm_h = SIZES[size_key]
    tw, th = mm_to_px(mm_w, ppmm), mm_to_px(mm_h, ppmm)
    bg_rgb = BACKGROUNDS[bg_key]

    with Image.open(image_path) as pil:
        pil = ImageOps.exif_transpose(pil)
        pil = pil.convert("RGB")
        # u2net 分割本身固定 320x320 推理；但 alpha matting / decontaminate 在
        # 「输入分辨率」上精修发丝，输入越大发丝边缘越干净（对齐图像生成页换背景，
        # 后者用原图不降采样）。故把下限提到 1600、上限 2000，既保留发丝细节又控内存。
        max_dim = min(2000, max(1600, max(tw, th) * 2))
        if pil.width > max_dim or pil.height > max_dim:
            s = max_dim / max(pil.width, pil.height)
            pil = pil.resize((int(pil.width * s), int(pil.height * s)), Image.LANCZOS)

    # 1. 人像抠图
    rgba = remove_background(pil)
    rgba = rgba.crop((0, 0, pil.width, pil.height))
    rgba_np = np.array(rgba)  # HxWx4 (R,G,B,A)
    bgra = np.concatenate([rgba_np[:, :, 2:3], rgba_np[:, :, 1:2], rgba_np[:, :, 0:1], rgba_np[:, :, 3:4]], axis=2)

    # 人像覆盖率检查：u2net_human_seg 未能分离出人物时提前给友好提示，
    # 避免后续 get_box 在空轮廓上崩溃（max() 空序列）。
    if float((rgba_np[:, :, 3] > 127).mean()) < 0.02:
        return {"error": "未能从图片中分离出人像，请换一张人物清晰、背景简洁的照片重试"}

    # 2. 人脸检测（MTCNN 或 YuNet，对原图 RGB 转 BGR 进行）
    origin_bgr = cv2.cvtColor(np.array(pil), cv2.COLOR_RGB2BGR)
    face_rect = detect_face(origin_bgr)
    if face_rect is None:
        return {"error": "未检测到清晰人脸，请换一张正面照重试"}

    # 3-4. 裁剪与修正（依赖 alpha 透明通道定位人像版式框，须在换底色之前）
    standard_size = (th, tw)
    result_bgra = adjust_photo(bgra, face_rect, standard_size)

    # 5. 先换底色：在高分辨率裁剪图上直接合成纯色背景。
    #    先对 alpha 轻微羽化（对齐换背景管线的边缘处理），平滑发丝半透明边缘，
    #    再与纯色按 alpha 混合；避免"先缩到标准尺寸再合成"在低分辨率下把发丝旧色/白边糊出来。
    feathered_bgra = _feather_alpha(result_bgra, ksize=5)
    composited_bgr = add_background(feathered_bgra, bg_rgb)

    # 6. 再缩放到标准尺寸（此时头发已换好色，与背景作为整体一起降采样，边缘更干净）
    out_img_bgr = standard_photo_resize(composited_bgr, standard_size)
    out_img = Image.fromarray(cv2.cvtColor(out_img_bgr, cv2.COLOR_BGR2RGB))
    ext = os.path.splitext(out_path)[1].lower()
    if ext in (".jpg", ".jpeg"):
        out_img.save(out_path, "JPEG", quality=95, dpi=(out_dpi, out_dpi))
    else:
        out_img.save(out_path, dpi=(out_dpi, out_dpi))

    return {"size": size_key, "bg": bg_key, "w": tw, "h": th, "mm_w": mm_w, "mm_h": mm_h, "dpi": out_dpi, "hi_res": hi_res}


if __name__ == "__main__":
    image_path = sys.argv[1]
    out_path = sys.argv[2]
    size_key = sys.argv[3] if len(sys.argv) > 3 else "一寸"
    bg_key = sys.argv[4] if len(sys.argv) > 4 else "白色"
    hi_res = sys.argv[5].lower() in ("1", "true", "yes") if len(sys.argv) > 5 else False
    info = make_idphoto(image_path, out_path, size_key, bg_key, hi_res)
    print(json.dumps(info, ensure_ascii=False))