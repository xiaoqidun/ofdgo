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
	"crypto/sha256"
	"fmt"
	"io"
)

// PreviewOptions 指定页面显示方式，光栅预览不提供SVG对象分组
type PreviewOptions struct {
	Raster  bool
	Objects bool
}

// RenderPreview 生成统一SVG容器及独立资源，原生程序与WebUI使用相同绘制入口
// 入参: page 页面内容, writer 输出流, options 显示方式
// 返回: SVGResources 需由调用方映射地址的资源, error 错误信息
func (r *Renderer) RenderPreview(page *PageContent, writer io.Writer, options PreviewOptions) (SVGResources, error) {
	if !options.Raster {
		if options.Objects {
			return r.RenderToSVGWithObjects(page, writer)
		}
		return r.RenderToSVGWithResources(page, writer)
	}
	if options.Objects {
		return SVGResources{}, fmt.Errorf("raster preview does not provide SVG object groups")
	}
	box, err := r.GetPageBox(page)
	if err != nil {
		return SVGResources{}, err
	}
	var pixels bytes.Buffer
	if err := r.RenderToPNG(page, &pixels); err != nil {
		return SVGResources{}, err
	}
	name := fmt.Sprintf("ofdgo-image-%x", sha256.Sum256(pixels.Bytes()))
	_, err = fmt.Fprintf(writer, `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="%gmm" height="%gmm" viewBox="0 0 %g %g"><image width="%g" height="%g" preserveAspectRatio="none" xlink:href="%s"/></svg>`, box.W, box.H, box.W, box.H, box.W, box.H, name)
	if err != nil {
		return SVGResources{}, err
	}
	return SVGResources{Images: []SVGImage{{Name: name, MIME: "image/png", Data: pixels.Bytes()}}}, nil
}
