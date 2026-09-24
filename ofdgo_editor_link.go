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
	"encoding/xml"
	"fmt"
	"slices"
)

// AnnotationLink 表示外部地址或文档内目标，两者必须且只能指定一项
type AnnotationLink struct {
	URI  *URI
	Dest *Dest
}

// SetObjectActions 原子替换普通对象的链接动作，空列表移除动作
// 入参: page 页面索引, ids 对象标识, actions 链接动作
// 返回: error 错误信息
func (e *Editor) SetObjectActions(page int, ids []string, actions []Action) error {
	if err := validateObjectActions(actions); err != nil {
		return err
	}
	objects, _, err := e.selectedObjects(page, ids)
	if err != nil {
		return err
	}
	for i := range objects {
		switch objects[i].Type {
		case "TextObject":
			objects[i].TextObject.Actions = cloneEditorData(actions)
		case "PathObject", "Path":
			objects[i].PathObject.Actions = cloneEditorData(actions)
		case "ImageObject":
			objects[i].ImageObject.Actions = cloneEditorData(actions)
		case "CompositeObject", "CompositeGraphicUnit":
			objects[i].CompositeGraphicUnit.Actions = cloneEditorData(actions)
		default:
			return fmt.Errorf("object %q cannot contain actions", ids[i])
		}
	}
	return e.updateObjects(page, objects, true)
}

// validateObjectActions 校验新建对象的链接动作，目标不必可达
// 入参: actions 动作列表
// 返回: error 错误信息
func validateObjectActions(actions []Action) error {
	for _, action := range actions {
		if !slices.Contains([]string{"CLICK", "DO", "PO"}, action.Event) {
			return fmt.Errorf("unsupported action event %q", action.Event)
		}
		if action.GotoA != nil || action.Sound != nil || action.Movie != nil {
			return fmt.Errorf("new attachment and multimedia actions require resource registration")
		}
		if (action.URI == nil) == (action.Goto == nil) {
			return fmt.Errorf("specify one URI or goto action")
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
			for _, area := range action.Region.Area {
				if _, err := creationNumbers(area.Start, 2); err != nil {
					return err
				}
				for _, command := range area.Command {
					var points []string
					switch command.Type {
					case "Line":
						points = []string{command.Point1}
					case "QuadraticBezier":
						points = []string{command.Point1, command.Point2}
					case "CubicBezier":
						points = []string{command.Point1, command.Point2, command.Point3}
					case "Close":
					default:
						return fmt.Errorf("unsupported new action region command %q", command.Type)
					}
					for _, point := range points {
						if _, err := creationNumbers(point, 2); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	return nil
}

// AnnotationLink 读取注解中唯一的点击跳转，书签目标保持原始动作
// 入参: page 页面索引, id 注解标识
// 返回: Action 跳转动作, error 错误信息
func (e *Editor) AnnotationLink(page int, id string) (Action, error) {
	data, err := e.annotationXML(page, id)
	if err != nil {
		return Action{}, err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return Action{}, err
	}
	targets, _ := annotationLinkNodes(root)
	if len(targets) != 1 {
		return Action{}, fmt.Errorf("annotation does not contain one link target")
	}
	var action Action
	node := targets[0].parent
	err = xml.Unmarshal(data[node.start:node.end], &action)
	return action, err
}

// annotationLinkNodes 获取外观内的点击跳转和基本对象，不进入私有扩展
// 入参: root 注解根节点
// 返回: []*editorXML 跳转目标, []*editorXML 基本对象
func annotationLinkNodes(root *editorXML) ([]*editorXML, []*editorXML) {
	var targets, objects []*editorXML
	var walk func(*editorXML)
	walk = func(n *editorXML) {
		if n.name.Space != root.name.Space {
			return
		}
		if n.name.Local == "Action" && n.attr("Event") == "CLICK" {
			for _, child := range n.children {
				if child.name.Space == n.name.Space && (child.name.Local == "URI" || child.name.Local == "Goto") {
					targets = append(targets, child)
				}
			}
		}
		if slices.Contains([]string{"TextObject", "PathObject", "ImageObject"}, n.name.Local) {
			objects = append(objects, n)
		}
		for _, child := range n.children {
			walk(child)
		}
	}
	if appearance := root.child("Appearance"); appearance != nil {
		walk(appearance)
	}
	return targets, objects
}

// AddLinkAnnotation 新增标准链接注解，区域位于页面毫米坐标，不检查目标是否可达
// 入参: page 页面索引, box 点击区域, target 链接目标, creator 作者
// 返回: string 注解标识, error 错误信息
func (e *Editor) AddLinkAnnotation(page int, box Box, target AnnotationLink, creator string) (string, error) {
	var id string
	err := e.Transaction(func(edit *Editor) error {
		if _, err := annotationLinkXML(target); err != nil {
			return err
		}
		object, err := NewShape(ShapeRectangle, Box{W: box.W, H: box.H})
		if err != nil {
			return err
		}
		no := false
		object.Fill, object.Stroke = &no, &no
		id, err = edit.AddAnnotation(page, Annotation{Type: "Link", Creator: creator, Appearance: Appearance{
			Boundary: editorBoxString(box), Objects: []GraphicObject{{Type: "PathObject", PathObject: object}},
		}})
		if err != nil {
			return err
		}
		return edit.UpdateAnnotationLink(page, id, target)
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// UpdateAnnotationLink 修改注解中唯一的点击跳转，保留动作区域、其他动作及扩展属性
// 无跳转时仅允许在单个基本对象上添加，多个跳转不隐式合并
// 入参: page 页面索引, id 注解标识, target 新目标，不验证目标可达性
// 返回: error 错误信息
func (e *Editor) UpdateAnnotationLink(page int, id string, target AnnotationLink) error {
	link, err := annotationLinkXML(target)
	if err != nil {
		return err
	}
	return e.editAnnotations(page, []string{id}, func(data []byte, node *editorXML) ([]byte, error) {
		data, err := editorXMLStandalone(data[node.start:node.end], node)
		if err != nil {
			return nil, err
		}
		root, err := parseEditorXML(data)
		if err != nil {
			return nil, err
		}
		targets, objects := annotationLinkNodes(root)
		if len(targets) > 1 {
			return nil, fmt.Errorf("annotation contains multiple link targets")
		}
		if len(targets) == 1 {
			n := targets[0]
			updated := link
			if n.name.Local == "URI" && target.URI != nil {
				var old URI
				if err := xml.Unmarshal(data[n.start:n.end], &old); err != nil {
					return nil, err
				}
				before, err := annotationLinkXML(AnnotationLink{URI: &old})
				if err != nil {
					return nil, err
				}
				updated, err = editorXMLMerge(data, n, before, link)
				if err != nil {
					return nil, err
				}
			} else if n.name.Local == "Goto" && target.Dest != nil && n.child("Dest") != nil {
				var old Goto
				if err := xml.Unmarshal(data[n.start:n.end], &old); err != nil {
					return nil, err
				}
				before, err := annotationLinkXML(AnnotationLink{Dest: old.Dest})
				if err != nil {
					return nil, err
				}
				updated, err = editorXMLMerge(data, n, before, link)
				if err != nil {
					return nil, err
				}
			} else {
				var check func(*editorXML) bool
				check = func(node *editorXML) bool {
					allowed := map[string]string{"URI": "URI Base", "Goto": "", "Dest": "Type PageID Left Right Top Bottom Zoom", "Bookmark": "Name"}
					attrs, known := allowed[node.name.Local]
					if !known || node.name.Space != root.name.Space || !editorXMLAttributes(node, attrs) {
						return false
					}
					for _, attr := range node.attrs {
						if attr.Name.Space != "" && attr.Name.Space != "xmlns" {
							return false
						}
					}
					for _, child := range node.children {
						if !check(child) {
							return false
						}
					}
					return true
				}
				if !check(n) {
					return nil, fmt.Errorf("changing link type would discard extension data")
				}
			}
			data = editorPatchXML(data, []editorXMLPatch{{n.start, n.end, updated}})
		} else {
			if len(objects) != 1 {
				return nil, fmt.Errorf("a new link requires one appearance object")
			}
			action, err := editorXMLContainer("Action", ofdAttrs{{Name: xml.Name{Local: "Event"}, Value: "CLICK"}}, link)
			if err != nil {
				return nil, err
			}
			action = bytes.TrimPrefix(action, []byte(xml.Header))
			if actions := objects[0].child("Actions"); actions != nil {
				data = editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, actions, append(bytes.Clone(data[actions.open:actions.close]), action...))})
			} else {
				actions, err := editorXMLContainer("Actions", nil, action)
				if err != nil {
					return nil, err
				}
				position := objects[0].open
				data = editorPatchXML(data, []editorXMLPatch{{position, position, bytes.TrimPrefix(actions, []byte(xml.Header))}})
			}
		}
		return editorAnnotationDate(data)
	})
}

// annotationLinkXML 序列化标准跳转目标，不改写其他动作或命名空间
// 入参: target 跳转目标
// 返回: []byte 独立XML片段, error 错误信息
func annotationLinkXML(target AnnotationLink) ([]byte, error) {
	if (target.URI == nil) == (target.Dest == nil) {
		return nil, fmt.Errorf("specify one URI or destination")
	}
	var data []byte
	var err error
	if target.URI != nil {
		attrs := ofdAttrs{{Name: xml.Name{Local: "URI"}, Value: target.URI.URI}}
		attrs.add("Base", target.URI.Base)
		data, err = editorXMLContainer("URI", attrs, nil)
	} else {
		dest := target.Dest
		if !slices.Contains([]string{"XYZ", "Fit", "FitH", "FitV", "FitR"}, dest.Type) {
			return nil, fmt.Errorf("invalid destination type %q", dest.Type)
		}
		attrs := ofdAttrs{{Name: xml.Name{Local: "Type"}, Value: dest.Type}, {Name: xml.Name{Local: "PageID"}, Value: dest.PageID}}
		for _, value := range []struct {
			name  string
			value float64
		}{{"Left", dest.Left}, {"Top", dest.Top}, {"Right", dest.Right}, {"Bottom", dest.Bottom}, {"Zoom", dest.Zoom}} {
			if !finite(value.value) {
				return nil, fmt.Errorf("destination coordinates must be finite")
			}
			if dest.Type == "XYZ" && slices.Contains([]string{"Left", "Top", "Zoom"}, value.name) || dest.Type == "FitH" && value.name == "Top" || dest.Type == "FitV" && value.name == "Left" || dest.Type == "FitR" && value.name != "Zoom" {
				attrs.add(value.name, ofdNumber(value.value))
			}
		}
		data, err = editorXMLContainer("Dest", attrs, nil)
		if err == nil {
			data, err = editorXMLContainer("Goto", nil, bytes.TrimPrefix(data, []byte(xml.Header)))
		}
	}
	return bytes.TrimPrefix(data, []byte(xml.Header)), err
}
