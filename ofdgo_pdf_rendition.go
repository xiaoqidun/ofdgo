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
	"errors"
	"fmt"
	"maps"
	"mime"
	"reflect"
	"strings"

	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
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

// screenAnnotation 转换屏幕注解的音视频呈现，不忽略必须遵守的播放器约束
// 入参: ctx 取消上下文, page PDF页面, annotation 屏幕注解, strict 严格检查开关
// 返回: error 动作、媒体或外观错误
func (p *pdfImporter) screenAnnotation(ctx context.Context, page *pdfgo.Page, annotation pdfgo.Annotation, strict bool) error {
	if annotation.Dictionary["A"] == nil {
		return p.appearanceAnnotation(ctx, page, annotation)
	}
	appearance := annotation
	appearance.Dictionary = maps.Clone(annotation.Dictionary)
	delete(appearance.Dictionary, "A")
	additional, err := p.reader.Resolve(annotation.Dictionary["AA"])
	if err != nil {
		if err := p.recoverPDFObject(ctx, "Annotation.AA", annotation.Dictionary["AA"], err); err != nil {
			return err
		}
		delete(appearance.Dictionary, "AA")
	}
	if dict, ok := additional.(pdfgo.Dictionary); ok {
		dict = maps.Clone(dict)
		delete(dict, "U")
		appearance.Dictionary["AA"] = dict
	}
	if appearance.Dictionary["AP"] == nil {
		appearance.Dictionary["AP"] = pdfgo.Dictionary{"N": &pdfgo.Stream{Dictionary: pdfgo.Dictionary{
			"Type": pdfgo.Name("XObject"), "Subtype": pdfgo.Name("Form"), "BBox": pdfgo.Array{pdfgo.Integer(0), pdfgo.Integer(0), pdfgo.Integer(1), pdfgo.Integer(1)},
		}}}
	}
	flags, err := p.reader.ReadAnnotationFlags(annotation)
	if err != nil {
		if err := p.recoverPDFObject(ctx, "Annotation.F", annotation.Dictionary["F"], err); err != nil {
			return err
		}
		delete(appearance.Dictionary, "F")
		flags = 0
	}
	if flags&64 != 0 {
		return p.appearanceAnnotation(ctx, page, appearance)
	}
	var actions []Action
	currentPage := p.page
	if err := p.reader.WalkActions(ctx, annotation.Dictionary["A"], func(info pdfgo.ActionInfo) error {
		current := annotation
		current.Dictionary = pdfgo.Dictionary{"A": info.Dictionary}
		converted, err := p.linkActions(current, strict, &currentPage)
		actions = append(actions, converted...)
		return err
	}); err != nil {
		if err := p.recoverPDFObject(ctx, "Annotation.Screen.A", annotation.Dictionary["A"], err); err != nil {
			return err
		}
		actions = nil
	}
	return p.appearanceAnnotation(ctx, page, appearance, actions...)
}

// renditionLinkActions 按屏幕关联转换播放及控制，切换视频时先停止其他候选
// 入参: object PDF媒体动作, source 触发注解，零值表示非图元动作, strict 是否禁止语义损失, currentPage 动作链当前页
// 返回: []Action 有序OFD动作，无法转换时为空, error 参数、资源或严格模式错误
func (p *pdfImporter) renditionLinkActions(object pdfgo.Object, source pdfgo.Annotation, strict bool, currentPage int) ([]Action, error) {
	playback, err := p.reader.ReadRenditionPlayback(p.ctx, object)
	if err != nil {
		if !strict && errors.Is(err, pdfgo.ErrInvalidAction) {
			p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("invalid PDF rendition action ignored: %v", err)})
			return nil, nil
		}
		return nil, err
	}
	if playback.Operation == nil || *playback.Operation < 0 || *playback.Operation >= 4 {
		return nil, p.renditionActionLoss("conditional rendition playback or script", strict)
	}
	if _, ok := p.pages[playback.Page]; !ok {
		return nil, p.renditionActionLoss("screen page is outside the document", strict)
	}
	if *playback.Operation != 0 {
		return p.renditionControls(playback.Annotation.Reference, *playback.Operation, "", strict)
	}
	selected, err := playback.Rendition.Select(p.ctx, func(candidate *pdfgo.Rendition) (bool, error) {
		_, _, viable, err := p.nativeRendition(candidate)
		return viable, err
	})
	if err != nil {
		return nil, err
	}
	if selected == nil {
		return nil, p.renditionActionLoss("no equivalent media rendition", strict)
	}
	converted, kind, _, err := p.nativeRendition(selected)
	if err != nil {
		return nil, err
	}
	if kind == "Video" {
		parameters, _, err := codecpdf.RenditionParameters(p.reader, selected.Screen, map[pdfgo.Name]func(pdfgo.Object) bool{
			"W": func(value pdfgo.Object) bool { return value == pdfgo.Integer(0) || value == pdfgo.Integer(3) },
		})
		if err != nil {
			return nil, err
		}
		window := pdfgo.Integer(3)
		if value, ok := parameters["W"].(pdfgo.Integer); ok {
			window = value
		}
		matches := window == 0 && source.Subtype == ""
		if window == 3 && source.Subtype != "" {
			index := p.pageIndexes[playback.Page]
			matches = currentPage == index && p.page == index
			if matches {
				if err := p.moviePlaybackBounds(converted.Movie, source, playback.Annotation, currentPage, strict); err != nil {
					return nil, err
				}
			}
		}
		if !matches {
			if err := p.renditionActionLoss("media playback window differs in OFD", strict); err != nil {
				return nil, err
			}
		}
	}
	id, err := p.renditionResource(selected, kind, playback)
	if err != nil || id == "" {
		return nil, err
	}
	controls, err := p.renditionControls(playback.Annotation.Reference, 1, id, strict)
	if err != nil || controls == nil {
		return nil, err
	}
	if kind == "Audio" {
		converted.Sound.ResourceID = id
	} else {
		converted.Movie.ResourceID = id
	}
	if len(selected.Play) != 0 || len(selected.Screen) != 0 || len(selected.Clip.BestEffort) != 0 {
		p.addWarning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF media rendition converted; best-effort presentation preferences may differ in OFD"})
	}
	return append(controls, converted), nil
}

// renditionResource 注册所选媒体，按屏幕区分播放实例并共用原始文件
// 入参: selected 已选择的原生呈现, kind 媒体类型, playback 播放目标
// 返回: string 资源标识，不可用时为空, error 文件或资源错误
func (p *pdfImporter) renditionResource(selected *pdfgo.Rendition, kind string, playback pdfgo.RenditionPlayback) (string, error) {
	format := codecpdf.MediaFormat(*selected.Clip.File)
	if format == "" {
		mediaType, _, _ := mime.ParseMediaType(selected.Clip.ContentType)
		format = strings.TrimPrefix(strings.SplitN(mediaType, "/", 2)[1], "x-")
	}
	id, err := p.mediaResource(p.ctx, *selected.Clip.File, kind, format, false)
	if err != nil || id == "" {
		return "", err
	}
	return p.mediaInstance(id, playback.Annotation, p.pageIndexes[playback.Page])
}

// renditionActionLoss 按导入策略报告媒体控制或呈现差异，不执行脚本
// 入参: reason 无法等价转换的原因, strict 是否禁止语义损失
// 返回: error 严格模式错误
func (p *pdfImporter) renditionActionLoss(reason string, strict bool) error {
	if strict {
		return &pdfgo.UnsupportedError{Feature: reason}
	}
	p.warning(pdfgo.Diagnostic{Message: "PDF rendition action: " + reason})
	return nil
}

// nativeRendition 检查可等价转换的媒体分支，视频不接受无法表达的音量及循环
// 入参: rendition 媒体呈现或选择器
// 返回: Action 播放动作, string 媒体类别, bool 是否可用, error 参数错误
func (p *pdfImporter) nativeRendition(rendition *pdfgo.Rendition) (Action, string, bool, error) {
	result := Action{Event: "CLICK"}
	viable, err := codecpdf.RenditionCriteria(p.reader, rendition)
	if err != nil || !viable {
		return result, "", false, err
	}
	if rendition.Subtype == "SR" {
		return result, "", true, nil
	}
	clip := rendition.Clip
	if clip == nil || clip.Subtype != "MCD" || clip.File == nil || len(clip.MustHonor) != 0 || len(clip.Players) != 0 {
		return result, "", false, nil
	}
	if clip.File.Embedded == nil && p.resolveFile == nil {
		return result, "", false, nil
	}
	for key, object := range clip.Permissions {
		value, err := p.reader.Resolve(object)
		if err != nil {
			return result, "", false, err
		}
		if key == "Type" && value == pdfgo.Name("MediaPermissions") {
			continue
		}
		if key != "TF" {
			return result, "", false, nil
		}
		if _, ok := value.(pdfgo.String); !ok {
			return result, "", false, fmt.Errorf("invalid media temporary-file permission")
		}
	}
	mediaType, _, err := mime.ParseMediaType(clip.ContentType)
	if err != nil || !strings.HasPrefix(mediaType, "audio/") && !strings.HasPrefix(mediaType, "video/") {
		return result, "", false, nil
	}
	audio := strings.HasPrefix(mediaType, "audio/")
	play, viable, err := codecpdf.RenditionParameters(p.reader, rendition.Play, map[pdfgo.Name]func(pdfgo.Object) bool{
		"V": func(value pdfgo.Object) bool {
			number, ok := value.(pdfgo.Integer)
			return ok && number >= 0 && number <= 100 && (audio || number == 100)
		},
		"RC": func(value pdfgo.Object) bool {
			return value == pdfgo.Integer(1) || value == pdfgo.Real(1) || audio && (value == pdfgo.Integer(0) || value == pdfgo.Real(0))
		},
		"A": func(value pdfgo.Object) bool { return value == pdfgo.Boolean(true) },
		"F": func(value pdfgo.Object) bool { return value == pdfgo.Integer(0) || value == pdfgo.Integer(5) },
	})
	if err != nil || !viable {
		return result, "", false, err
	}
	screen, viable, err := codecpdf.RenditionParameters(p.reader, rendition.Screen, map[pdfgo.Name]func(pdfgo.Object) bool{
		"W": func(value pdfgo.Object) bool { return !audio && value == pdfgo.Integer(0) || value == pdfgo.Integer(3) },
	})
	if err != nil || !viable {
		return result, "", false, err
	}
	if screen["W"] == pdfgo.Integer(0) {
		valid, err := codecpdf.RenditionFloatingWindow(p.reader, rendition.Screen)
		if err != nil || !valid {
			return result, "", false, err
		}
	}
	volume := 100
	if value := play["V"]; value != nil {
		volume = int(value.(pdfgo.Integer))
	}
	repeat := false
	if value := play["RC"]; value != nil && value != pdfgo.Integer(1) && value != pdfgo.Real(1) {
		repeat = true
	}
	if audio {
		result.Sound = &Sound{Volume: &volume, Repeat: repeat}
		return result, "Audio", true, nil
	}
	result.Movie = &Movie{Operator: "Play"}
	return result, "Video", true, nil
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
	ignore := func(err error) error {
		if err == nil {
			return nil
		}
		if ctxErr := p.ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if p.warning != nil && pdfAuxiliaryError(err) {
			return nil
		}
		return err
	}
	collect := func(object pdfgo.Object) error {
		err := p.reader.WalkActions(p.ctx, object, func(info pdfgo.ActionInfo) error {
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
		return ignore(err)
	}
	opened, err := p.reader.ReadOpenAction(p.ctx)
	if err != nil && !errors.Is(err, pdfgo.ErrInvalidDestination) {
		if err := ignore(err); err != nil {
			return err
		}
		opened = pdfgo.OpenAction{}
	}
	if opened.Action != nil {
		if err := collect(opened.Action); err != nil {
			return err
		}
	}
	if err := p.reader.WalkOutlines(p.ctx, func(_ int, item pdfgo.OutlineItem) error {
		return collect(item.Action)
	}); err != nil {
		if err := ignore(err); err != nil {
			return err
		}
	}
	for _, page := range p.pageOrder {
		triggers, err := p.reader.ReadPageTriggers(p.ctx, page)
		if err != nil {
			if err := ignore(err); err != nil {
				return err
			}
			triggers = nil
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
			if err := ignore(err); err != nil {
				return err
			}
			continue
		}
		for _, annotation := range annotations {
			flags, err := p.reader.ReadAnnotationFlags(annotation)
			if err != nil {
				if err := ignore(err); err != nil {
					return err
				}
				continue
			}
			if flags&64 == 0 && (annotation.Subtype == "Screen" || annotation.Subtype == "Link") {
				if err := collect(annotation.Dictionary["A"]); err != nil {
					return err
				}
			}
			triggers, err := p.reader.ReadAnnotationTriggers(p.ctx, annotation)
			if err != nil {
				if err := ignore(err); err != nil {
					return err
				}
				continue
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
