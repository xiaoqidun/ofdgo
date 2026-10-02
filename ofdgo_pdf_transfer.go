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

import "github.com/xiaoqidun/pdfgo"

// pdfTransferKey 区分图形状态与网屏覆盖函数
type pdfTransferKey struct {
	transfer *pdfgo.TransferFunction
	halftone *pdfgo.Halftone
}

// resolveTransfer 在页面内复用连续RGB设备的有效传递函数
// 入参: style 原始图形状态与有效输出状态
// 返回: error 网屏传递函数错误
func (p *pdfImporter) resolveTransfer(style *pdfgo.Style) error {
	if style.Halftone == nil {
		return nil
	}
	cache := p.compositingCache()
	if cache.transfers == nil {
		cache.transfers = make(map[pdfTransferKey]*pdfgo.TransferFunction)
	}
	key := pdfTransferKey{style.Transfer, style.Halftone}
	transfer, found := cache.transfers[key]
	if !found {
		var err error
		transfer, err = p.reader.DeviceTransfer(style.Transfer, style.Halftone, "DeviceRGB")
		if err != nil {
			return err
		}
		cache.transfers[key] = transfer
	}
	style.Transfer = transfer
	return nil
}

// pdfCompositeHasTransfer 检查页面是否需要保留最终设备传递函数
// 入参: nodes 原始图元及透明组
// 返回: bool 是否存在非恒等函数
func pdfCompositeHasTransfer(nodes []pdfCompositeNode) bool {
	for _, node := range nodes {
		if node.group != nil {
			if pdfCompositeHasTransfer(node.children) {
				return true
			}
		} else {
			style, _, _ := node.style()
			if style.Transfer != nil {
				return true
			}
		}
	}
	return false
}

// selectTransfer 按最上层基本图元及祖先的不透明状态选择最终设备函数
// 入参: node 基本图元, outline 是否描边
// 返回: *pdfgo.TransferFunction 当前函数或设备默认值, error 图案解析错误
func (c *pdfCompositor) selectTransfer(node pdfCompositeNode, outline bool) (*pdfgo.TransferFunction, error) {
	style, _, _ := node.style()
	paint := style.Fill
	if outline {
		paint = style.Stroke
	}
	if c.transferBlocked || paint.Alpha != 1 || style.SoftMask != nil || !pdfNormalBlend(style.BlendMode) {
		return nil, nil
	}
	if style.Transfer == nil {
		return nil, nil
	}
	if node.image != nil {
		soft, err := node.image.Image.HasSoftMask()
		if err != nil || soft {
			return nil, err
		}
	}
	if paint.Tiling != nil && (node.image == nil || node.image.Image.ImageMask) {
		pattern, err := c.importer.compositePattern(paint)
		if err != nil {
			return nil, err
		}
		opaque, err := pattern.opaqueTransfer()
		if err != nil || !opaque {
			return nil, err
		}
	}
	return style.Transfer, nil
}

// opaqueTransfer 复用图案不透明性检查，图案内部函数不提前作用于源颜色
// 返回: bool 是否允许使用外层设备函数, error 单元解析错误
func (p *pdfCompositePattern) opaqueTransfer() (bool, error) {
	if !p.transferChecked {
		for _, child := range p.nodes {
			opaque, err := p.local.transferOpaque(child)
			if err != nil {
				return false, err
			}
			if !opaque {
				p.transferChecked = true
				return false, nil
			}
		}
		p.transferChecked, p.transferOpaque = true, true
	}
	return p.transferOpaque, nil
}

// transferOpaque 检查全部图案内容的不透明条件，不限制混合颜色空间
// 入参: node 单元图元或透明组
// 返回: bool 是否不透明, error 图案或图片遮罩引用错误
func (p *pdfImporter) transferOpaque(node pdfCompositeNode) (bool, error) {
	if group := node.group; group != nil {
		if group.Alpha != 1 || group.SoftMask != nil || !pdfNormalBlend(group.BlendMode) {
			return false, nil
		}
		for _, child := range node.children {
			if opaque, err := p.transferOpaque(child); err != nil || !opaque {
				return opaque, err
			}
		}
		return true, nil
	}
	style, fill, stroke := node.style()
	if style.SoftMask != nil || !pdfNormalBlend(style.BlendMode) || fill && style.Fill.Alpha != 1 || stroke && style.Stroke.Alpha != 1 {
		return false, nil
	}
	if node.image != nil {
		soft, err := node.image.Image.HasSoftMask()
		if err != nil || soft {
			return false, err
		}
		if !node.image.Image.ImageMask {
			return true, nil
		}
	}
	for i, paint := range [2]pdfgo.Paint{style.Fill, style.Stroke} {
		if i == 0 && !fill || i == 1 && !stroke || paint.Tiling == nil {
			continue
		}
		pattern, err := p.compositePattern(paint)
		if err != nil {
			return false, err
		}
		if opaque, err := pattern.opaqueTransfer(); err != nil || !opaque {
			return opaque, err
		}
	}
	return true, nil
}
