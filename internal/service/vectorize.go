package service

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// VecParams are the user-selectable vectorization settings.
type VecParams struct {
	Mode            string // spline | polygon | pixel
	ColorPrecision  int    // 2-8
	FilterSpeckle   int    // 0-20
	CornerThreshold int    // 1-180
}

func (p *VecParams) normalize() {
	switch p.Mode {
	case "polygon", "pixel", "spline":
	default:
		p.Mode = "spline"
	}
	if p.ColorPrecision < 2 || p.ColorPrecision > 8 {
		p.ColorPrecision = 6
	}
	if p.FilterSpeckle < 0 || p.FilterSpeckle > 20 {
		p.FilterSpeckle = 4
	}
	if p.CornerThreshold < 1 || p.CornerThreshold > 180 {
		p.CornerThreshold = 60
	}
}

// ToolAvailability contains the status of external conversion tools.
type ToolAvailability struct {
	Vtracer   bool
	Inkscape  bool
	Potrace   bool
	Libre     bool
	Soffice   string
	InkscapeBin string
	PotraceBin  string
}

// DetectTools probes for external binaries.
func DetectTools() ToolAvailability {
	a := ToolAvailability{}
	if _, err := exec.LookPath("vtracer"); err == nil {
		a.Vtracer = true
	}
	if _, err := exec.LookPath("inkscape"); err == nil {
		a.Inkscape = true
		a.InkscapeBin = "inkscape"
	}
	if _, err := exec.LookPath("potrace"); err == nil {
		a.Potrace = true
		a.PotraceBin = "potrace"
	}
	for _, name := range []string{"soffice", "libreoffice"} {
		if _, err := exec.LookPath(name); err == nil {
			a.Libre = true
			a.Soffice = name
			break
		}
	}
	return a
}

// vectorizeImage converts a raster image to SVG via the Python wrapper.
func vectorizeImage(src, outSVG string, p VecParams) error {
	p.normalize()
	params := map[string]interface{}{
		"colormode":        "color",
		"mode":             p.Mode,
		"filter_speckle":   p.FilterSpeckle,
		"color_precision":  p.ColorPrecision,
		"corner_threshold": p.CornerThreshold,
		"path_precision":   8,
	}
	b, _ := json.Marshal(params)
	out, err := RunCmd(PythonPath(), ScriptPath("vectorize.py"), "svg", src, outSVG, string(b))
	if err != nil {
		return fmt.Errorf("vtracer 失败: %s", strings.TrimSpace(out))
	}
	if _, err := os.Stat(outSVG); err != nil {
		return fmt.Errorf("vtracer 未能生成SVG: %s", strings.TrimSpace(out))
	}
	return nil
}

// Vectorize performs the image -> target format conversion.
// Returns the path of the converted file.
func Vectorize(tmpDir, src, output string, p VecParams) (string, error) {
	// Normalize input to PNG first for vtracer compatibility
	pngPath := filepath.Join(tmpDir, "input.png")
	if out, err := RunCmd(PythonPath(), ScriptPath("vectorize.py"), "topng", src, pngPath); err != nil {
		return "", fmt.Errorf("图片读取失败: %s", strings.TrimSpace(out))
	}
	output = SafeExt(output)
	if output == "" {
		output = "svg"
	}

	// Produce the base SVG
	svgPath := filepath.Join(tmpDir, "out.svg")
	if err := vectorizeImage(pngPath, svgPath, p); err != nil {
		return "", err
	}

	switch output {
	case "svg":
		dest := filepath.Join(tmpDir, "result.svg")
		return dest, CopyFile(svgPath, dest)

	case "pdf", "ai", "eps":
		// Use Inkscape for PDF/EPS; AI is exported as PDF-compatible
		inkscape := DetectTools().InkscapeBin
		if inkscape == "" {
			return "", fmt.Errorf("inkscape 未安装，无法输出 %s 格式，请改用 SVG", strings.ToUpper(output))
		}
		// AI format: export as PDF first, then rename (AI is PDF-compatible)
		if output == "ai" {
			pdfPath := filepath.Join(tmpDir, "result.pdf")
			args := []string{"--export-type=pdf", "--export-filename=" + pdfPath, svgPath}
			cmd := exec.Command(inkscape, args...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return "", fmt.Errorf("inkscape 转换失败: %s", strings.TrimSpace(string(out)))
			}
			if _, err := os.Stat(pdfPath); err != nil {
				return "", fmt.Errorf("inkscape 输出文件缺失")
			}
			dest := filepath.Join(tmpDir, "result.ai")
			if err := CopyFile(pdfPath, dest); err != nil {
				return "", fmt.Errorf("复制 AI 文件失败")
			}
			return dest, nil
		}
		dest := filepath.Join(tmpDir, "result."+output)
		ext := output
		args := []string{"--export-type=" + ext, "--export-filename=" + dest, svgPath}
		cmd := exec.Command(inkscape, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("inkscape 转换失败: %s", strings.TrimSpace(string(out)))
		}
		if _, err := os.Stat(dest); err != nil {
			// Fallback: inkscape might have named it differently
			alt := filepath.Join(tmpDir, "result."+output)
			if _, err := os.Stat(alt); err != nil {
				return "", fmt.Errorf("inkscape 输出文件缺失")
			}
			dest = alt
		}
		return dest, nil

	case "dxf":
		// Use Potrace for DXF (monochrome).
		potrace := DetectTools().PotraceBin
		if potrace == "" {
			return "", fmt.Errorf("potrace 未安装，无法输出 DXF 格式")
		}
		pbmPath := filepath.Join(tmpDir, "input.pbm")
		if out, err := RunCmd(PythonPath(), ScriptPath("vectorize.py"), "topbm", pngPath, pbmPath); err != nil {
			return "", fmt.Errorf("PBM 转换失败: %s", strings.TrimSpace(out))
		}
		dest := filepath.Join(tmpDir, "result.dxf")
		cmd := exec.Command(potrace, "-b", "dxf", "-o", dest, pbmPath)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("potrace 转换失败: %s", strings.TrimSpace(string(out)))
		}
		if _, err := os.Stat(dest); err != nil {
			return "", fmt.Errorf("potrace 输出文件缺失")
		}
		return dest, nil

	case "sk":
		dest := filepath.Join(tmpDir, "result.sketch")
		if out, err := RunCmd(PythonPath(), ScriptPath("vectorize.py"), "sketch", svgPath, dest); err != nil {
			return "", fmt.Errorf("Sketch 包装失败: %s", strings.TrimSpace(out))
		}
		return dest, nil

	case "fig":
		dest := filepath.Join(tmpDir, "result.fig")
		if out, err := RunCmd(PythonPath(), ScriptPath("vectorize.py"), "fig", svgPath, dest); err != nil {
			return "", fmt.Errorf("Figma 包装失败: %s", strings.TrimSpace(out))
		}
		return dest, nil

	default:
		return "", fmt.Errorf("不支持的输出格式: %s", output)
	}
}