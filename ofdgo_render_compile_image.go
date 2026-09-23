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
	"bytes"
	"fmt"
	"image"
	"math"
	"slices"
)

// imageObject 共用图片定位、裁剪和边框，不在度量时读取像素
// 入参: object 图片对象, state 继承状态
// 返回: error 资源或几何错误
func (c *semanticCompiler) imageObject(object ImageObject, state RenderState) error {
	if object.Visible != nil && !*object.Visible || object.Alpha != nil && *object.Alpha == 0 {
		return nil
	}
	box, _ := ParseBox(object.Boundary)
	local := NewMatrix(object.CTM)
	if object.CTM == "" {
		local = Matrix{a: box.W, d: box.H}
	}
	matrix, _ := renderObjectMatrix(object.Boundary, local, state)
	clip, err := c.renderer.objectGeometryClip(object.Clips, object.Boundary, local, state)
	if err != nil {
		return err
	}
	if c.measure {
		path, err := c.geometry.Transform(geometryRectangle(Box{W: 1, H: 1}), matrix)
		if err != nil {
			return err
		}
		path, err = clipGeometry(c.geometry, path, clip)
		if err != nil {
			return err
		}
		if err = c.addBounds(path, false); err != nil {
			return err
		}
	} else {
		img, err := c.renderer.ImageResource(object.ResourceID)
		if err != nil {
			return err
		}
		if object.ImageMask != "" {
			mask, err := c.renderer.ImageResource(object.ImageMask)
			if err != nil {
				return err
			}
			img = imageWithMask(img, mask)
		}
		img = imageWithAlpha(img, object.Alpha)
		bounds := img.Bounds()
		matrix = matrix.Multiply(Matrix{a: 1 / float64(bounds.Dx()), d: 1 / float64(bounds.Dy())}).Multiply(TranslationMatrix(-float64(bounds.Min.X), -float64(bounds.Min.Y)))
		if err := c.addImage(img, matrix, clip); err != nil {
			return err
		}
	}
	if object.Border != nil {
		border := imageBorderObject(object, box)
		if border.LineWidth == 0 {
			return nil
		}
		state.Clip = clip
		state.Defaults = nil
		return c.pathObject(border, state)
	}
	return nil
}

// imageBorderObject 共用图像圆角边框的标准路径与样式
// 入参: object 图像对象, box 物理范围
// 返回: PathObject 边框
func imageBorderObject(object ImageObject, box Box) PathObject {
	border := object.Border
	width := defaultPathLineWidth
	if border.LineWidth != nil {
		width = *border.LineWidth
	}
	w, h := box.W, box.H
	data := fmt.Sprintf("M 0 0 L %g 0 L %g %g L 0 %g C", w, w, h, h)
	rx, ry := min(border.HorizonalCornerRadius, w/2), min(border.VerticalCornerRadius, h/2)
	if rx > 0 && ry > 0 {
		data = fmt.Sprintf("M 0 %g A %g %g 0 0 1 %g 0 L %g 0 A %g %g 0 0 1 %g %g L %g %g A %g %g 0 0 1 %g %g L %g %g A %g %g 0 0 1 0 %g C",
			ry, rx, ry, rx, w-rx, rx, ry, w, ry, w, h-ry, rx, ry, w-rx, h, rx, h, rx, ry, h-ry)
	}
	return PathObject{Boundary: object.Boundary, LineWidth: width, DashOffset: &border.DashOffset, DashPattern: border.DashPattern, Alpha: object.Alpha, StrokeColor: border.BorderColor, AbbreviatedData: data}
}

// addImage 保留图像资源与页面坐标裁剪，延迟像素采样
// 入参: img 图像, matrix 像素到页面矩阵, clip 页面裁剪
// 返回: error 裁剪转换错误
func (c *semanticCompiler) addImage(img image.Image, matrix Matrix, clip *GeometryPath) error {
	command := RasterCommand{Image: img, Transform: RasterMatrix(matrix.Values())}
	if clip != nil {
		var err error
		command.Clip, err = c.segments(*clip)
		if err != nil {
			return err
		}
	}
	c.page.Commands = append(c.page.Commands, command)
	return nil
}

// DrawStamp 保留签章图片和嵌套OFD的绘制顺序，不混入正文搜索
// 入参: stamp 印章
// 返回: error 印章解析或编译错误
func (c *semanticCompiler) DrawStamp(stamp Stamp) error {
	return c.sharedStamp(stamp, (*semanticCompiler).drawStampContent)
}

// drawStampContent 解释未缓存的签章内容，保持嵌套文档及裁剪语义
// 入参: stamp 印章
// 返回: error 印章解析或编译错误
func (c *semanticCompiler) drawStampContent(stamp Stamp) error {
	if c.textOnly || c.measure || len(stamp.Data) == 0 {
		return nil
	}
	if stamp.Type == "ofd" {
		reader, err := NewReader(bytes.NewReader(stamp.Data), int64(len(stamp.Data)))
		if err != nil {
			return err
		}
		defer reader.Close()
		document, err := reader.Doc()
		if err != nil {
			return err
		}
		renderer := c.renderer.childRenderer(reader)
		renderer.TransparentBackground = true
		for _, reference := range document.Pages.Page {
			content, err := reader.PageContent(reference)
			if err != nil {
				return err
			}
			page, err := renderer.CompilePage(content)
			if err != nil {
				return err
			}
			if stamp.Clip != nil {
				if renderer.backends.Raster == nil {
					return fmt.Errorf("stamp raster: %w", ErrBackendUnavailable)
				}
				img, err := renderer.backends.Raster.Render(page)
				if err != nil {
					return err
				}
				return c.stampImage(stampImageWithTransparentWhite(img), stamp)
			}
			matrix := TranslationMatrix(stamp.Box.X, stamp.Box.Y).Multiply(Matrix{a: stamp.Box.W / page.Width, d: stamp.Box.H / page.Height})
			for _, command := range page.Commands {
				command.Transform = RasterMatrix(matrix.Multiply(MatrixFromValues([6]float64(command.Transform))).Values())
				if command.Stroke != nil {
					stroke := *command.Stroke
					scale := math.Sqrt(math.Abs(matrix.a*matrix.d - matrix.b*matrix.c))
					stroke.Width *= scale
					stroke.DashOffset *= scale
					stroke.Dashes = slices.Clone(stroke.Dashes)
					for i := range stroke.Dashes {
						stroke.Dashes[i] *= scale
					}
					command.Stroke = &stroke
				}
				if command.Clip != nil {
					command.Clip = slices.Clone(command.Clip)
					for i := range command.Clip {
						segment := command.Clip[i]
						segment.End = RasterMatrix(matrix.Values()).Apply(segment.End)
						segment.Control1 = RasterMatrix(matrix.Values()).Apply(segment.Control1)
						segment.Control2 = RasterMatrix(matrix.Values()).Apply(segment.Control2)
						command.Clip[i] = segment
					}
				}
				c.page.Commands = append(c.page.Commands, command)
			}
		}
		return nil
	}
	img, _, err := decodeImageData(stamp.Data)
	if err != nil {
		return err
	}
	return c.stampImage(stampImageWithTransparentWhite(img), stamp)
}

// stampImage 按印章裁剪区域放置图像
// 入参: img 印章图像, stamp 印章
// 返回: error 编译错误
func (c *semanticCompiler) stampImage(img image.Image, stamp Stamp) error {
	box := stamp.Box
	if stamp.Clip != nil {
		img = clipStampImage(img, box, *stamp.Clip)
		box.X += stamp.Clip.X
		box.Y += stamp.Clip.Y
		box.W = stamp.Clip.W
		box.H = stamp.Clip.H
	}
	bounds := img.Bounds()
	matrix := TranslationMatrix(box.X, box.Y).Multiply(Matrix{a: box.W / float64(bounds.Dx()), d: box.H / float64(bounds.Dy())}).Multiply(TranslationMatrix(-float64(bounds.Min.X), -float64(bounds.Min.Y)))
	return c.addImage(img, matrix, nil)
}
