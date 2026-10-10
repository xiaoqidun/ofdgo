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
	"fmt"
	"math"
	"strconv"
	"unicode/utf8"
)

// SignaturePageStamps 生成所选页面相同毫米坐标的印章位置并检查页内范围
// 入参: pages 从1开始且不重复的页码, box 完整印章区域
// 返回: []SignatureStamp 可传入签署选项的位置, error 错误信息
func (r *Reader) SignaturePageStamps(pages []int, box Box) ([]SignatureStamp, error) {
	if r == nil || len(pages) == 0 || !signatureWriteBoxValid(box) {
		return nil, fmt.Errorf("invalid page stamp parameters")
	}
	doc, err := r.Doc()
	if err != nil {
		return nil, err
	}
	seen := make(map[int]bool)
	stamps := make([]SignatureStamp, 0, len(pages))
	for _, page := range pages {
		if page < 1 || page > len(doc.Pages.Page) || seen[page] {
			return nil, fmt.Errorf("invalid stamp page: %d", page)
		}
		seen[page] = true
		stamps = append(stamps, SignatureStamp{PageRef: doc.Pages.Page[page-1].ID, Boundary: signatureWriteBox(box)})
	}
	return signatureWriteStamps(r, stamps, true)
}

// SignatureSeamStamps 生成页面右侧的等宽骑缝印章位置
// 页码从1开始，按传入顺序分割同一印章；box.X为右侧内缩，Y为纵坐标，W/H为完整印章尺寸
// 入参: pages 至少两页且不重复, box 印章参数，单位毫米
// 返回: []SignatureStamp 可传入签署选项的位置, error 错误信息
func (r *Reader) SignatureSeamStamps(pages []int, box Box) ([]SignatureStamp, error) {
	if r == nil || len(pages) < 2 || !signatureWriteBoxValid(box) || box.X < 0 {
		return nil, fmt.Errorf("invalid seam stamp parameters")
	}
	doc, err := r.Doc()
	if err != nil {
		return nil, err
	}
	seen := make(map[int]bool)
	stamps := make([]SignatureStamp, 0, len(pages))
	width := box.W / float64(len(pages))
	for i, page := range pages {
		if page < 1 || page > len(doc.Pages.Page) || seen[page] {
			return nil, fmt.Errorf("invalid seam stamp page: %d", page)
		}
		seen[page] = true
		area, err := r.PageArea(doc.Pages.Page[page-1])
		if err != nil {
			return nil, err
		}
		physical, err := ParseBox(area.PhysicalBox)
		if err != nil {
			return nil, err
		}
		clip := Box{X: float64(i) * width, W: width, H: box.H}
		boundary := Box{X: physical.X + physical.W - box.X - width - clip.X, Y: box.Y, W: box.W, H: box.H}
		stamps = append(stamps, SignatureStamp{PageRef: doc.Pages.Page[page-1].ID, Boundary: signatureWriteBox(boundary), Clip: signatureWriteBox(clip)})
	}
	return signatureWriteStamps(r, stamps, true)
}

// signatureWriteStamps 复用Reader定位并检查外观几何参数
// 入参: r 阅读器, stamps 位置, seal 是否电子印章
// 返回: []SignatureStamp 独立位置列表, error 错误信息
func signatureWriteStamps(r *Reader, stamps []SignatureStamp, seal bool) ([]SignatureStamp, error) {
	if len(stamps) != 0 && !seal {
		return nil, fmt.Errorf("digital signatures cannot contain seal appearances")
	}
	positions, err := r.SignatureStampPositions(stamps)
	if err != nil {
		return nil, err
	}
	doc, err := r.Doc()
	if err != nil {
		return nil, err
	}
	result := append([]SignatureStamp(nil), stamps...)
	if len(result) != 0 {
		pages := make(map[string]bool)
		for _, page := range doc.Pages.Page {
			id := editorResourceID(page.ID)
			if id == "" || pages[id] {
				return nil, fmt.Errorf("ambiguous stamp page ID: %s", page.ID)
			}
			pages[id] = true
		}
	}
	ids := make(map[string]bool)
	for _, stamp := range stamps {
		if stamp.ID == "" {
			continue
		}
		if !signatureWriteIDValid(stamp.ID) || ids[stamp.ID] {
			return nil, fmt.Errorf("invalid or duplicate stamp ID: %s", stamp.ID)
		}
		ids[stamp.ID] = true
	}
	for _, position := range positions {
		box := position.Box
		if !signatureWriteBoxValid(box) {
			return nil, fmt.Errorf("invalid stamp boundary")
		}
		visible := box
		if clip := position.ClipBox; clip != nil {
			if !signatureWriteBoxValid(*clip) || clip.X < 0 || clip.Y < 0 || clip.X+clip.W > box.W+1e-9 || clip.Y+clip.H > box.H+1e-9 {
				return nil, fmt.Errorf("invalid stamp clip")
			}
			visible = Box{X: box.X + clip.X, Y: box.Y + clip.Y, W: clip.W, H: clip.H}
		}
		area, err := r.PageArea(doc.Pages.Page[position.Page-1])
		if err != nil {
			return nil, err
		}
		page, err := ParseBox(area.PhysicalBox)
		if err != nil || !signatureWriteBoxValid(page) {
			return nil, fmt.Errorf("invalid stamp page area")
		}
		if visible.X < page.X-1e-9 || visible.Y < page.Y-1e-9 || visible.X+visible.W > page.X+page.W+1e-9 || visible.Y+visible.H > page.Y+page.H+1e-9 {
			return nil, fmt.Errorf("stamp is outside page %d", position.Page)
		}
	}
	return result, nil
}

// signatureWriteIDValid 按XML 1.0的NCName规则校验签章标识
// 入参: id 标识
// 返回: bool 是否有效
func signatureWriteIDValid(id string) bool {
	if id == "" || !utf8.ValidString(id) {
		return false
	}
	for i, c := range id {
		if c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' ||
			c >= 0xC0 && c <= 0xD6 || c >= 0xD8 && c <= 0xF6 ||
			c >= 0xF8 && c <= 0x2FF || c >= 0x370 && c <= 0x37D ||
			c >= 0x37F && c <= 0x1FFF || c >= 0x200C && c <= 0x200D ||
			c >= 0x2070 && c <= 0x218F || c >= 0x2C00 && c <= 0x2FEF ||
			c >= 0x3001 && c <= 0xD7FF || c >= 0xF900 && c <= 0xFDCF ||
			c >= 0xFDF0 && c <= 0xFFFD || c >= 0x10000 && c <= 0xEFFFF {
			continue
		}
		if i == 0 || !(c == '-' || c == '.' || c >= '0' && c <= '9' ||
			c == 0xB7 || c >= 0x300 && c <= 0x36F || c >= 0x203F && c <= 0x2040) {
			return false
		}
	}
	return true
}

// signatureWriteBoxValid 判断印章区域是否有限且尺寸为正
// 入参: box 区域
// 返回: bool 是否有效
func signatureWriteBoxValid(box Box) bool {
	for _, value := range []float64{box.X, box.Y, box.W, box.H} {
		if math.IsInf(value, 0) || math.IsNaN(value) {
			return false
		}
	}
	return box.W > 0 && box.H > 0
}

// signatureWriteBox 编码印章区域
// 入参: box 区域
// 返回: string 区域文本
func signatureWriteBox(box Box) string {
	return strconv.FormatFloat(box.X, 'f', -1, 64) + " " + strconv.FormatFloat(box.Y, 'f', -1, 64) + " " + strconv.FormatFloat(box.W, 'f', -1, 64) + " " + strconv.FormatFloat(box.H, 'f', -1, 64)
}
