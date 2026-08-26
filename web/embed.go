package web

import "embed"

//go:embed all:static
//go:embed index.html idphoto.html translate.html video.html image.html about.html donate.html
var Assets embed.FS