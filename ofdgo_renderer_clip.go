// Copyright 2025-2026 肖其顿 (XIAO QI DUN)
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ofdgo

import (
	"image"

	"github.com/tdewolff/canvas"
)

// clipRenderer 收集图形和文字的绘制轮廓
type clipRenderer struct {
	path *canvas.Path
	clip *canvas.Path
}

// Size 返回不限定边界的裁剪画布尺寸
// 返回: float64 宽度, float64 高度
func (r *clipRenderer) Size() (float64, float64) {
	return 0, 0
}

// RenderPath 合并路径填充及描边轮廓
// 入参: path 路径, style 绘制样式, m 变换矩阵
func (r *clipRenderer) RenderPath(path *canvas.Path, style canvas.Style, m canvas.Matrix) {
	if style.HasFill() {
		r.add(applyClipPath(path.Copy().Transform(m), r.clip))
	}
	if style.HasStroke() {
		p := path.Dash(style.DashOffset, style.Dashes...).Stroke(style.StrokeWidth, style.StrokeCapper, style.StrokeJoiner, canvas.Tolerance)
		r.add(applyClipPath(p.Transform(m), r.clip))
	}
}

// add 合并裁剪轮廓
// 入参: path 裁剪路径
func (r *clipRenderer) add(path *canvas.Path) {
	if r.path == nil {
		r.path = path
	} else {
		r.path = unionClipPath(r.path, path)
	}
}

// RenderText 合并文字字形轮廓
// 入参: text 文字, m 变换矩阵
func (r *clipRenderer) RenderText(text *canvas.Text, m canvas.Matrix) {
	text.RenderTo(r, m, 0)
}

// RenderImage 裁剪区不包含图像对象
// 入参: img 图像, m 变换矩阵
func (r *clipRenderer) RenderImage(img image.Image, m canvas.Matrix) {}
