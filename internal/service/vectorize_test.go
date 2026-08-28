package service

import (
	"testing"
)

func TestVecParamsNormalize(t *testing.T) {
	tests := []struct {
		name string
		in   VecParams
		want VecParams
	}{
		{
			"defaults",
			VecParams{},
			VecParams{Mode: "spline", ColorPrecision: 6, FilterSpeckle: 0, CornerThreshold: 60},
		},
		{
			"polygon_valid",
			VecParams{Mode: "polygon", ColorPrecision: 4, FilterSpeckle: 10, CornerThreshold: 90},
			VecParams{Mode: "polygon", ColorPrecision: 4, FilterSpeckle: 10, CornerThreshold: 90},
		},
		{
			"invalid_mode",
			VecParams{Mode: "invalid"},
			VecParams{Mode: "spline", ColorPrecision: 6, FilterSpeckle: 0, CornerThreshold: 60},
		},
		{
			"out_of_range",
			VecParams{ColorPrecision: 100, FilterSpeckle: -1, CornerThreshold: 200},
			VecParams{Mode: "spline", ColorPrecision: 6, FilterSpeckle: 4, CornerThreshold: 60},
		},
		{
			"pixel_mode",
			VecParams{Mode: "pixel", ColorPrecision: 2, FilterSpeckle: 0, CornerThreshold: 1},
			VecParams{Mode: "pixel", ColorPrecision: 2, FilterSpeckle: 0, CornerThreshold: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in
			got.normalize()
			if got != tt.want {
				t.Errorf("normalize() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDetectTools(t *testing.T) {
	tools := DetectTools()
	if tools.Vtracer == false {
		t.Log("vtracer not installed (OK for CI)")
	}
	if tools.Inkscape == false {
		t.Log("inkscape not installed (OK for CI)")
	}
	if tools.Potrace == false {
		t.Log("potrace not installed (OK for CI)")
	}
}
