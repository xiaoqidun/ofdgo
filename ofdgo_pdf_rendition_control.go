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
	"errors"
	"reflect"

	"github.com/xiaoqidun/pdfgo"
)

// pdfRenditionImport 保存可转换事件中的屏幕关联，首次遇到呈现动作时建立
type pdfRenditionImport struct {
	actions map[pdfgo.Reference][]pdfgo.Dictionary
	media   map[pdfgo.Reference][]pdfRenditionResource
	err     error
}

// pdfRenditionResource 保存屏幕候选媒体及其独立控制标识
type pdfRenditionResource struct {
	ID   string
	Kind string
}

// pdfRenditionAssignment 区分同一屏幕的呈现定义，重复触发不重复登记
type pdfRenditionAssignment struct {
	Screen    pdfgo.Reference
	Rendition uintptr
}

// renditionControls 生成屏幕关联的控制序列，切换时不停止即将重新播放的资源
// 入参: screen 屏幕引用, operation 停止、暂停或恢复操作, playing 即将播放的资源，控制动作为空, strict 是否禁止语义损失
// 返回: []Action 控制序列，nil表示无法转换, error 资源或严格模式错误
func (p *pdfImporter) renditionControls(screen pdfgo.Reference, operation int64, playing string, strict bool) ([]Action, error) {
	if p.renditions == nil {
		p.renditions = &pdfRenditionImport{actions: make(map[pdfgo.Reference][]pdfgo.Dictionary), media: make(map[pdfgo.Reference][]pdfRenditionResource)}
		p.renditions.err = p.collectRenditions()
	}
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	if p.renditions.err != nil {
		return nil, p.renditionActionLoss("screen associations cannot be determined: "+p.renditions.err.Error(), strict)
	}
	resources, err := p.renditionResources(screen, strict)
	if err != nil {
		return nil, err
	}
	for _, resource := range resources {
		if resource.Kind != "Video" && (playing == "" || len(resources) > 1) {
			return nil, p.renditionActionLoss("audio rendition control cannot be represented by OFD Sound", strict)
		}
	}
	operator := map[int64]string{1: "Stop", 2: "Pause", 3: "Resume"}[operation]
	actions := make([]Action, 0, len(resources))
	for _, resource := range resources {
		if resource.ID != playing {
			actions = append(actions, Action{Event: "CLICK", Movie: &Movie{ResourceID: resource.ID, Operator: operator}})
		}
	}
	return actions, nil
}

// renditionResources 解析一个屏幕的可用媒体，文件数据仅在该屏幕参与转换时读取
// 入参: screen 屏幕引用, strict 是否禁止忽略无效动作
// 返回: []pdfRenditionResource 候选媒体, error 解析、资源或取消错误
func (p *pdfImporter) renditionResources(screen pdfgo.Reference, strict bool) ([]pdfRenditionResource, error) {
	if resources, exists := p.renditions.media[screen]; exists {
		return resources, nil
	}
	resources := make([]pdfRenditionResource, 0)
	seen := make(map[string]bool)
	for _, object := range p.renditions.actions[screen] {
		playback, err := p.reader.ReadRenditionPlayback(p.ctx, object)
		if err != nil {
			if !strict && errors.Is(err, pdfgo.ErrInvalidAction) {
				continue
			}
			return nil, err
		}
		if _, exists := p.pages[playback.Page]; !exists {
			continue
		}
		selected, err := playback.Rendition.Select(p.ctx, func(candidate *pdfgo.Rendition) (bool, error) {
			_, _, viable, err := p.nativeRendition(candidate)
			return viable, err
		})
		if err != nil {
			return nil, err
		}
		if selected == nil {
			continue
		}
		_, kind, _, err := p.nativeRendition(selected)
		if err != nil {
			return nil, err
		}
		id, err := p.renditionResource(selected, kind, playback)
		if err != nil {
			return nil, err
		}
		if id != "" && !seen[id] {
			seen[id] = true
			resources = append(resources, pdfRenditionResource{ID: id, Kind: kind})
		}
	}
	p.renditions.media[screen] = resources
	return resources, p.ctx.Err()
}

// collectRenditions 索引可转换事件中的媒体关联，不解码媒体或执行脚本
// 返回: error 动作结构、引用或取消错误
func (p *pdfImporter) collectRenditions() error {
	seen := make(map[pdfRenditionAssignment]bool)
	collect := func(object pdfgo.Object) error {
		return p.reader.WalkActions(p.ctx, object, func(info pdfgo.ActionInfo) error {
			if info.Type != "Rendition" {
				return nil
			}
			value, err := p.reader.Resolve(info.Dictionary["OP"])
			if err != nil {
				return err
			}
			if value != pdfgo.Integer(0) && value != pdfgo.Integer(4) {
				return nil
			}
			screen, ok := info.Dictionary["AN"].(pdfgo.Reference)
			if !ok {
				return nil
			}
			value, err = p.reader.Resolve(info.Dictionary["R"])
			if err != nil {
				return err
			}
			r, ok := value.(pdfgo.Dictionary)
			if !ok || r == nil {
				return nil
			}
			key := pdfRenditionAssignment{Screen: screen, Rendition: reflect.ValueOf(r).Pointer()}
			if !seen[key] {
				seen[key] = true
				p.renditions.actions[screen] = append(p.renditions.actions[screen], info.Dictionary)
			}
			return nil
		})
	}
	opened, err := p.reader.ReadOpenAction(p.ctx)
	if err != nil && !errors.Is(err, pdfgo.ErrInvalidDestination) {
		return err
	}
	if opened.Action != nil {
		if err := collect(opened.Action); err != nil {
			return err
		}
	}
	if err := p.reader.WalkOutlines(p.ctx, func(_ int, item pdfgo.OutlineItem) error {
		return collect(item.Action)
	}); err != nil {
		return err
	}
	for _, page := range p.pageOrder {
		triggers, err := p.reader.ReadPageTriggers(p.ctx, page)
		if err != nil {
			return err
		}
		for _, trigger := range triggers {
			if trigger.Event == "O" {
				if err := collect(trigger.Action); err != nil {
					return err
				}
			}
		}
		annotations, err := page.AnnotationsContext(p.ctx)
		if err != nil {
			return err
		}
		for _, annotation := range annotations {
			flags, err := p.reader.ReadAnnotationFlags(annotation)
			if err != nil {
				return err
			}
			if flags&64 == 0 && (annotation.Subtype == "Screen" || annotation.Subtype == "Link") {
				if err := collect(annotation.Dictionary["A"]); err != nil {
					return err
				}
			}
			triggers, err := p.reader.ReadAnnotationTriggers(p.ctx, annotation)
			if err != nil {
				return err
			}
			for _, trigger := range triggers {
				if trigger.Event == "PO" || trigger.Event == "U" && flags&64 == 0 && annotation.Subtype != "Widget" {
					if err := collect(trigger.Action); err != nil {
						return err
					}
				}
			}
		}
	}
	return p.ctx.Err()
}
