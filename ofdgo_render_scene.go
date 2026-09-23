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

// PageScene 保存后端无关的页面语义与绘制顺序，不提前展开文字或重新编码图片
// 场景借用源页面及资源的只读引用，编辑文档后应重新构建
type PageScene struct {
	Source   *PageContent
	Box      Box
	commands []sceneCommand
}

// sceneCommand 保存叶子对象、签章或嵌套分组事件
type sceneCommand struct {
	kind       sceneCommandKind
	object     *GraphicObject
	state      RenderState
	stamp      Stamp
	annotation string
	count      int
}

// sceneCommandKind 区分场景中的绘制和分组事件
type sceneCommandKind uint8

const (
	sceneObject sceneCommandKind = iota
	sceneStamp
	sceneBeginObject
	sceneBeginAnnotation
	sceneEnd
	sceneSkip
)

// CompileScene 展开页面结构，保留原文、字形索引、动作和对象标识
// 不提前绘制文字或图片，裁剪轮廓按几何后端需求解析
// 入参: page 源页面
// 返回: *PageScene 只读语义场景, error 遍历或几何错误
func (r *Renderer) CompileScene(page *PageContent) (*PageScene, error) {
	box, err := r.GetPageBox(page)
	if err != nil {
		return nil, err
	}
	scene := &PageScene{Source: page, Box: box}
	if err := r.WalkPage(page, (*sceneRecorder)(scene)); err != nil {
		return nil, err
	}
	return scene, nil
}

// Walk 重放语义场景，访问器仍可自行处理文字和原始图片
// 入参: visitor 页面访问器
// 返回: error 访问器错误
func (s *PageScene) Walk(visitor PageVisitor) error {
	groups, grouped := visitor.(PageGroups)
	var stack []bool
	for _, command := range s.commands {
		switch command.kind {
		case sceneObject:
			if err := visitor.DrawObject(command.object, command.state); err != nil {
				closeSceneGroups(groups, stack)
				return err
			}
		case sceneStamp:
			if err := visitor.DrawStamp(command.stamp); err != nil {
				closeSceneGroups(groups, stack)
				return err
			}
		case sceneBeginObject:
			if grouped {
				stack = append(stack, groups.BeginObject(command.object))
			}
		case sceneBeginAnnotation:
			if grouped {
				stack = append(stack, groups.BeginAnnotation(command.annotation))
			}
		case sceneEnd:
			if grouped {
				if stack[len(stack)-1] {
					groups.EndObject()
				}
				stack = stack[:len(stack)-1]
			}
		case sceneSkip:
			if grouped {
				groups.SkipObjects(command.count)
			}
		}
	}
	return nil
}

// closeSceneGroups 在访问器失败时配对结束已建立的分组
// 入参: groups 分组访问器, stack 分组状态
func closeSceneGroups(groups PageGroups, stack []bool) {
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] {
			groups.EndObject()
		}
	}
}

// sceneRecorder 收集公共遍历事件，不解释绘制外观
type sceneRecorder PageScene

// DrawObject 保留源对象及继承状态
// 入参: object 源对象, state 继承状态
// 返回: error 固定为空
func (s *sceneRecorder) DrawObject(object *GraphicObject, state RenderState) error {
	s.commands = append(s.commands, sceneCommand{kind: sceneObject, object: object, state: state})
	return nil
}

// DrawStamp 保留签章外观
// 入参: stamp 签章
// 返回: error 固定为空
func (s *sceneRecorder) DrawStamp(stamp Stamp) error {
	s.commands = append(s.commands, sceneCommand{kind: sceneStamp, stamp: stamp})
	return nil
}

// BeginObject 记录对象分组
// 入参: object 源对象
// 返回: bool 固定为true
func (s *sceneRecorder) BeginObject(object *GraphicObject) bool {
	s.commands = append(s.commands, sceneCommand{kind: sceneBeginObject, object: object})
	return true
}

// BeginAnnotation 记录注解分组
// 入参: id 注解标识
// 返回: bool 固定为true
func (s *sceneRecorder) BeginAnnotation(id string) bool {
	s.commands = append(s.commands, sceneCommand{kind: sceneBeginAnnotation, annotation: id})
	return true
}

// EndObject 记录分组结束
func (s *sceneRecorder) EndObject() { s.commands = append(s.commands, sceneCommand{kind: sceneEnd}) }

// SkipObjects 记录不可见成员数量，保持可编辑输出的对象编号
// 入参: count 对象数量
func (s *sceneRecorder) SkipObjects(count int) {
	s.commands = append(s.commands, sceneCommand{kind: sceneSkip, count: count})
}
