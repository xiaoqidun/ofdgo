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
	"strings"
)

// structureVisitor 按引用收集图元及复合图元成员的原文和页面范围
type structureVisitor struct {
	ctx        context.Context
	renderer   *Renderer
	targets    map[string]*StructureTarget
	stack      []*StructureTarget
	text       map[*StructureTarget]*strings.Builder
	boundsOnly bool
}

// structureObjectVisitor 收集页面、复合图元及注释外观标识，绑定校验不解码字体、图片或裁剪
type structureObjectVisitor struct {
	objects     map[string]bool
	targets     []StructureTarget
	listTargets bool
}

// StructureObjects 列出指定页可绑定的图元及原文，包含模板、隐藏内容、复合图元成员和注释外观
// 不解码字体和图片，返回顺序遵循页面遍历顺序，同一标识仅列出一次
// 入参: page 从0开始的页面索引
// 返回: []StructureTarget 候选图元，不计算Bounds, error 页面或遍历错误
func (r *Renderer) StructureObjects(page int) ([]StructureTarget, error) {
	return r.StructureObjectsContext(context.Background(), page)
}

// StructureObjectsContext 列出页面绑定候选，读取或遍历取消时不返回部分结果，不改变渲染选项
// 入参: ctx 取消上下文, page 从0开始的页面索引
// 返回: []StructureTarget 候选图元，不计算Bounds, error 页面读取、遍历或取消错误
func (r *Renderer) StructureObjectsContext(ctx context.Context, page int) ([]StructureTarget, error) {
	content, err := r.reader.PageContentByIndexContext(ctx, page)
	if err != nil {
		return nil, err
	}
	visitor := &structureObjectVisitor{objects: make(map[string]bool), listTargets: true}
	prevAnnots, prevPageCtx := r.renderAnnotations, r.pageContext
	r.renderAnnotations, r.pageContext = true, ctx
	defer func() { r.renderAnnotations, r.pageContext = prevAnnots, prevPageCtx }()
	if err := r.WalkPageContext(ctx, content, visitor); err != nil {
		return nil, err
	}
	for i := range visitor.targets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		visitor.targets[i].Page, visitor.targets[i].PageID = page+1, content.ID
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return visitor.targets, nil
}

// BeginObject 记录当前图元的标引目标，使复合图元引用覆盖其成员
// 入参: object 页面图元
// 返回: bool 是否需要配对结束通知
func (v *structureVisitor) BeginObject(object *GraphicObject) bool {
	target := v.targets[editorResourceID(editorObjectID(*object))]
	if target != nil {
		target.Error = ""
	}
	v.stack = append(v.stack, target)
	return true
}

// EndObject 结束当前图元的标引收集
func (v *structureVisitor) EndObject() {
	v.stack = v.stack[:len(v.stack)-1]
}

// BeginAnnotation 不为注解容器建立额外标引层级
// 入参: id 注解标识
// 返回: bool 始终为false
func (v *structureVisitor) BeginAnnotation(id string) bool { return false }

// SkipObjects 不使用渲染对象序号，隐藏成员不占用额外状态
// 入参: count 跳过的图元数
func (v *structureVisitor) SkipObjects(count int) {}

// IncludeHidden 读取原文时保留隐藏语义，仅定位时跳过隐藏复合图元及注解
// 返回: bool 是否需要隐藏内容
func (v *structureVisitor) IncludeHidden() bool { return !v.boundsOnly }

// SkipClips 保留裁剪语义，以可见范围定位图元
// 返回: bool 始终为false
func (v *structureVisitor) SkipClips() bool { return false }

// DrawObject 按继承变换和裁剪度量被引用的图元，按需汇总原文，不解码图片像素
// 入参: object 页面图元, state 继承状态
// 返回: error 度量或取消错误
func (v *structureVisitor) DrawObject(object *GraphicObject, state RenderState) error {
	if err := v.ctx.Err(); err != nil {
		return err
	}
	wanted := false
	for _, target := range v.stack {
		wanted = wanted || target != nil
	}
	if !wanted {
		return nil
	}
	var box Box
	if !state.Hidden {
		result, err := v.renderer.MeasureObject(*object, MeasureOptions{Defaults: state.Defaults, Parent: state.Parent, BoundaryInCTM: state.BoundaryInCTM, Clip: state.Clip})
		if err != nil {
			return err
		}
		box = result.Bounds
	}
	var text string
	if !v.boundsOnly && object.Type == "TextObject" {
		text = object.TextObject.Text()
	}
	for _, target := range v.stack {
		if target == nil {
			continue
		}
		target.Bounds = unionTextBox(target.Bounds, box)
		if text != "" {
			if target.Text == "" {
				target.Text = text
				continue
			}
			if v.text == nil {
				v.text = make(map[*StructureTarget]*strings.Builder)
			}
			builder := v.text[target]
			if builder == nil {
				builder = new(strings.Builder)
				builder.WriteString(target.Text)
				v.text[target] = builder
			}
			builder.WriteByte('\n')
			builder.WriteString(text)
			target.Text = builder.String()
		}
	}
	return nil
}

// DrawStamp 不将签章外观混入正文标引
// 入参: stamp 签章外观
// 返回: error 取消错误
func (v *structureVisitor) DrawStamp(stamp Stamp) error { return v.ctx.Err() }

// BeginObject 登记图元标识，不创建分组状态
// 入参: object 页面图元
// 返回: bool 始终为false
func (v *structureObjectVisitor) BeginObject(object *GraphicObject) bool {
	id := editorResourceID(editorObjectID(*object))
	if id != "" && !v.objects[id] {
		v.objects[id] = true
		if !v.listTargets {
			return false
		}
		target := StructureTarget{ID: id}
		if object.Type == "TextObject" {
			target.Text = object.TextObject.Text()
		}
		v.targets = append(v.targets, target)
	}
	return false
}

// EndObject 满足分组访问接口，不维护分组栈
func (v *structureObjectVisitor) EndObject() {}

// BeginAnnotation 不将注释容器编号作为图元标识
// 入参: id 注解标识
// 返回: bool 始终为false
func (v *structureObjectVisitor) BeginAnnotation(id string) bool { return false }

// SkipObjects 不依赖图元序号
// 入参: count 跳过数量
func (v *structureObjectVisitor) SkipObjects(count int) {}

// IncludeHidden 允许绑定隐藏图元
// 返回: bool 始终为true
func (v *structureObjectVisitor) IncludeHidden() bool { return true }

// SkipClips 绑定校验不需要裁剪轮廓
// 返回: bool 始终为true
func (v *structureObjectVisitor) SkipClips() bool { return true }

// DrawObject 不绘制图元，标识已由BeginObject登记
// 入参: object 页面图元, state 继承状态
// 返回: error 始终为nil
func (v *structureObjectVisitor) DrawObject(object *GraphicObject, state RenderState) error {
	return nil
}

// DrawStamp 不将签章外观作为正文绑定目标
// 入参: stamp 签章外观
// 返回: error 始终为nil
func (v *structureObjectVisitor) DrawStamp(stamp Stamp) error { return nil }
