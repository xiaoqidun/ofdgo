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
	"reflect"
)

// sharedClipObjects 将连续重复的大裁剪区提取为复合对象，保留页面坐标和绘制顺序
// 入参: objects 本库生成且尚未分配标识的对象
// 返回: []GraphicObject 提取后的对象, error 资源创建或取消错误
func (p *pdfImporter) sharedClipObjects(objects []GraphicObject) ([]GraphicObject, error) {
	var output []GraphicObject
	for start := 0; start < len(objects); {
		if err := p.ctx.Err(); err != nil {
			return nil, err
		}
		clips := pdfPageClip(objects[start])
		end := start + 1
		if clips != nil {
			for end < len(objects) && pdfMatchesPageClip(objects[end], clips) {
				if err := p.ctx.Err(); err != nil {
					return nil, err
				}
				end++
			}
		}
		if end-start < 2 {
			if output != nil {
				output = append(output, objects[start])
			}
			start = end
			continue
		}
		if output == nil {
			output = append(make([]GraphicObject, 0, len(objects)), objects[:start]...)
		}
		members := objects[start:end:end]
		for index := range members {
			*editorObjectClips(&members[index]) = nil
		}
		id, err := p.objectValidation().addOwnedVector(p.ctx, members, p.pageWidth, p.pageHeight)
		if err != nil {
			return nil, err
		}
		output = append(output, GraphicObject{Type: "CompositeObject", CompositeGraphicUnit: CompositeGraphicUnit{
			Boundary: pdfBoundary(Box{W: p.pageWidth, H: p.pageHeight}), ResourceID: id, Clips: clips,
		}})
		start = end
	}
	if output == nil {
		return objects, nil
	}
	return output, nil
}

// pdfPageClip 将导入裁剪区还原到页面坐标，仅提取包含大量重复路径数据的裁剪
// 入参: object 导入对象
// 返回: *Clips 页面坐标裁剪，不满足提取条件时为空
func pdfPageClip(object GraphicObject) *Clips {
	if object.Type != "TextObject" && object.Type != "PathObject" && object.Type != "ImageObject" {
		return nil
	}
	clips := *editorObjectClips(&object)
	if clips == nil || clips.TransFlag == nil || *clips.TransFlag || len(clips.Clip) == 0 {
		return nil
	}
	boundary, _ := editorGeometry(object)
	box, err := ParseBox(boundary)
	if err != nil {
		return nil
	}
	translation := ""
	if box.X != 0 || box.Y != 0 {
		translation = pdfNumbers(1, 0, 0, 1, -box.X, -box.Y)
	}
	bytes := 0
	result := &Clips{TransFlag: clips.TransFlag, Clip: make([]Clip, len(clips.Clip))}
	for index, clip := range clips.Clip {
		if len(clip.Area) != 1 || clip.Area[0].CTM != translation {
			return nil
		}
		area := clip.Area[0]
		area.CTM = ""
		result.Clip[index].Area = []ClipArea{area}
		for _, path := range area.Path {
			bytes += len(path.AbbreviatedData)
		}
	}
	if bytes < 2048 {
		return nil
	}
	return result
}

// pdfMatchesPageClip 比较页面坐标裁剪，不将相同外接矩形视为相同路径
// 入参: object 导入对象, pageClip 已还原的裁剪区
// 返回: bool 是否可共享裁剪区
func pdfMatchesPageClip(object GraphicObject, pageClip *Clips) bool {
	if object.Type != "TextObject" && object.Type != "PathObject" && object.Type != "ImageObject" {
		return false
	}
	clips := *editorObjectClips(&object)
	if clips == nil || clips.TransFlag == nil || *clips.TransFlag || len(clips.Clip) != len(pageClip.Clip) {
		return false
	}
	boundary, _ := editorGeometry(object)
	box, err := ParseBox(boundary)
	if err != nil {
		return false
	}
	translation := ""
	if box.X != 0 || box.Y != 0 {
		translation = pdfNumbers(1, 0, 0, 1, -box.X, -box.Y)
	}
	for index, clip := range clips.Clip {
		if len(clip.Area) != 1 || clip.Area[0].CTM != translation {
			return false
		}
		area, expected := clip.Area[0], pageClip.Clip[index].Area[0]
		if area.DrawParam != expected.DrawParam || !reflect.DeepEqual(area.Path, expected.Path) || !reflect.DeepEqual(area.Text, expected.Text) {
			return false
		}
	}
	return true
}
