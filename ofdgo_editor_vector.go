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
	"context"
	"encoding/xml"
)

// addOwnedVector 将本库生成的独立对象注册为标准矢量资源，不保留内联复合内容
// 入参: ctx 取消上下文, objects 无原文和标识的对象, width 资源宽度, height 资源高度
// 返回: string 资源编号, error 校验、编码或取消错误
func (e *Editor) addOwnedVector(ctx context.Context, objects []GraphicObject, width, height float64) (string, error) {
	validation := &editorValidation{Editor: e}
	for index, object := range objects {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		prepared, err := validation.prepareObject(e.nextID(), object)
		if err != nil {
			return "", err
		}
		objects[index] = prepared
	}
	id := e.nextID()
	data, err := encodeOFDXMLContext(ctx, func(x *ofdXML) {
		x.root("Res", ofdAttrs{{Name: xml.Name{Local: "BaseLoc"}, Value: "."}})
		x.start("CompositeGraphicUnits", nil)
		x.start("CompositeGraphicUnit", ofdAttrs{{Name: xml.Name{Local: "ID"}, Value: id},
			{Name: xml.Name{Local: "Width"}, Value: ofdNumber(width)}, {Name: xml.Name{Local: "Height"}, Value: ofdNumber(height)}})
		x.start("Content", nil)
		for _, object := range objects {
			if err := ctx.Err(); err != nil {
				x.err = err
				return
			}
			x.object(object, false)
		}
		x.end("Content")
		x.end("CompositeGraphicUnit")
		x.end("CompositeGraphicUnits")
		x.end("Res")
	})
	if err != nil {
		return "", err
	}
	resource, err := e.compositeResource(id, data)
	if err != nil {
		return "", err
	}
	e.resources = append(e.resources, resource)
	return id, ctx.Err()
}
