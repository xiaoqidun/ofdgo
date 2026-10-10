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
	"reflect"
	"slices"
	"strings"
)

// DocumentActions 获取文档打开动作的独立副本，不执行动作
// 返回: []Action 动作列表
func (e *Editor) DocumentActions() []Action {
	if e.source == nil {
		return nil
	}
	return cloneEditorData(e.source.document.Actions)
}

// SetDocumentActions 替换文档打开动作，空列表清除动作并记录撤销
// 入参: actions 事件为DO的动作及已注册的资源引用
// 返回: error 结构、编码或提交错误
func (e *Editor) SetDocumentActions(actions []Action) error {
	if err := validateActionEvents(actions, "DO"); err != nil {
		return err
	}
	current := e.DocumentActions()
	if reflect.DeepEqual(current, actions) || len(current) == 0 && len(actions) == 0 {
		return nil
	}
	base := e.source
	var err error
	if base == nil {
		base, err = e.importBase()
		if err != nil {
			return err
		}
	}
	name := base.reader.documentRoot
	data, err := base.reader.readFile(name)
	if err != nil {
		return err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return err
	}
	encoded, err := editorActionsXML(actions)
	if err != nil {
		return err
	}
	patch := editorXMLPatch{root.close, root.close, encoded}
	context := root
	if node := root.child("Actions"); node != nil {
		patch.start, patch.end = node.start, node.end
		context = node
	} else {
		for _, child := range root.children {
			if child.matchesOFD(child.name.Local) && slices.Contains([]string{"VPreferences", "Bookmarks", "Annotations", "CustomTags", "Attachments", "Extensions"}, child.name.Local) {
				patch.start, patch.end = child.start, child.start
				break
			}
		}
	}
	patch.data, err = editorXMLGenerated(encoded, context.name.Space)
	if err != nil {
		return err
	}
	return e.commitAnnotationParts(base, map[string][]byte{name: editorPatchXML(data, []editorXMLPatch{patch})})
}

// SetPageActions 替换页面打开动作，输入独立保存，空列表清除动作
// 入参: index 页面索引, actions 事件为PO的动作及已注册的资源引用
// 返回: error 页码或动作错误
func (e *Editor) SetPageActions(index int, actions []Action) error {
	if err := validateActionEvents(actions, "PO"); err != nil {
		return err
	}
	page, err := e.page(index)
	if err != nil {
		return err
	}
	if reflect.DeepEqual(page.Actions, actions) || len(page.Actions) == 0 && len(actions) == 0 {
		return nil
	}
	before, after := cloneEditorData(page.Actions), cloneEditorData(actions)
	page.Actions = cloneEditorData(after)
	if change := e.recordChange(); change != nil {
		change.undo = func(e *Editor) { e.pages[index].Actions = cloneEditorData(before) }
		change.redo = func(e *Editor) { e.pages[index].Actions = cloneEditorData(after) }
	}
	return nil
}

// validateActionEvents 按动作归属校验事件类型和动作结构
// 入参: actions 动作列表, event 允许的事件
// 返回: error 事件或动作错误
func validateActionEvents(actions []Action, event string) error {
	for _, action := range actions {
		if action.Event != event {
			return fmt.Errorf("expected %s action event", event)
		}
	}
	return validateActions(actions)
}

// validateActions 校验动作结构，目标不必可达
// 入参: actions 动作列表
// 返回: error 错误信息
func validateActions(actions []Action) error {
	for _, action := range actions {
		if !slices.Contains([]string{"CLICK", "DO", "PO"}, action.Event) {
			return fmt.Errorf("unsupported action event %q", action.Event)
		}
		count := 0
		for _, present := range []bool{action.URI != nil, action.Goto != nil, action.GotoA != nil, action.Sound != nil, action.Movie != nil} {
			if present {
				count++
			}
		}
		if count != 1 {
			return fmt.Errorf("specify one action target")
		}
		if action.GotoA != nil && action.GotoA.AttachID == "" {
			return fmt.Errorf("attachment action requires an attachment ID")
		}
		if sound := action.Sound; sound != nil {
			if sound.ResourceID == "" || sound.Volume != nil && (*sound.Volume < 0 || *sound.Volume > 100) {
				return fmt.Errorf("invalid sound action resource or volume")
			}
		}
		if movie := action.Movie; movie != nil {
			if movie.ResourceID == "" || !slices.Contains([]string{"", "Play", "Stop", "Pause", "Resume"}, movie.Operator) {
				return fmt.Errorf("invalid movie action resource or operator")
			}
		}
		if action.Goto != nil {
			if (action.Goto.Dest == nil) == (action.Goto.Bookmark == nil) {
				return fmt.Errorf("specify one destination or bookmark")
			}
			if action.Goto.Dest != nil {
				if _, err := annotationLinkXML(AnnotationLink{Dest: action.Goto.Dest}); err != nil {
					return err
				}
			}
		}
		if action.Region != nil {
			if err := validateActionRegion(action.Region); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateActionRegion 校验动作区域，保留标准允许的曲线缺省值和圆弧异常处理
// 入参: region 区域
// 返回: error 路径或参数错误
func validateActionRegion(region *Region) error {
	if len(region.Area) == 0 {
		return fmt.Errorf("action region requires an area")
	}
	for _, area := range region.Area {
		values, err := creationNumbers(area.Start, 2)
		if err != nil {
			return err
		}
		current := Point{values[0], values[1]}
		start := current
		if len(area.Command) == 0 {
			return fmt.Errorf("action area requires a path command")
		}
		for _, command := range area.Command {
			var points []string
			switch command.Type {
			case "Move", "Line":
				points = []string{command.Point1}
			case "QuadraticBezier":
				points = []string{command.Point1, command.Point2}
			case "CubicBezier":
				for _, control := range []string{command.Point1, command.Point2} {
					if control != "" {
						points = append(points, control)
					}
				}
				points = append(points, command.Point3)
			case "Arc":
				if _, err := creationNumbers(command.EllipseSize, len(strings.Fields(command.EllipseSize))); err != nil {
					return err
				}
				if _, err := creationNumbers(command.RotationAngle, 1); err != nil {
					return err
				}
				for _, flag := range []string{command.LargeArc, command.SweepDirection} {
					if !slices.Contains([]string{"true", "false", "1", "0"}, flag) {
						return fmt.Errorf("invalid arc boolean %q", flag)
					}
				}
				points = []string{command.EndPoint}
			case "Close":
				current = start
				continue
			default:
				return fmt.Errorf("unsupported action region command %q", command.Type)
			}
			var end Point
			for _, point := range points {
				values, err := creationNumbers(point, 2)
				if err != nil {
					return err
				}
				end = Point{values[0], values[1]}
			}
			if command.Type == "Arc" && end == current {
				return fmt.Errorf("arc endpoint equals current point")
			}
			current = end
			if command.Type == "Move" {
				start = current
			}
		}
	}
	return nil
}

// editorActionsXML 复用动作编码并补齐独立片段的命名空间，空列表不输出节点
// 入参: actions 动作列表
// 返回: []byte 动作片段, error 编码错误
func editorActionsXML(actions []Action) ([]byte, error) {
	if len(actions) == 0 {
		return nil, nil
	}
	data, err := encodeOFDXML(func(x *ofdXML) {
		x.root("Document", nil)
		x.actions(actions)
		x.end("Document")
	})
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	node := root.child("Actions")
	return editorXMLStandalone(data[node.start:node.end], node)
}
