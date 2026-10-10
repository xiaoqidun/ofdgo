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
	"fmt"
	"maps"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/xiaoqidun/pdfgo"
)

// pdfVideoResource 共享视频内容及各播放区域，控制动作按资源关联
type pdfVideoResource struct {
	Clip    *pdfgo.MediaClip
	Screens []*pdfVideoScreen
}

// pdfVideoScreen 保存播放区域的页面归属及呈现参数
type pdfVideoScreen struct {
	Page      int
	Screen    *pdfgo.ScreenAnnotation
	Rendition *pdfgo.Rendition
}

// pdfMovieAction 保存视频控制及可选播放区域，待全部来源收集后关联
type pdfMovieAction struct {
	Resource  *pdfVideoResource
	Target    *pdfVideoScreen
	Operation int
}

// movieAction 按OFD第12章选择内嵌区域或浮窗，控制动作不另建播放器
// 入参: action 视频动作, page 来源页索引，负值表示文档, source 动作来源, pages 输出页面
// 返回: *pdfNavigationAction 待关联视频动作, error 资源或参数错误
func (n *pdfNavigation) movieAction(action Movie, page int, source actionSource, pages []RenderDocumentPage) (*pdfNavigationAction, error) {
	operation := 0
	switch action.Operator {
	case "", "Play":
	case "Stop":
		operation = 1
	case "Pause":
		operation = 2
	case "Resume":
		operation = 3
	default:
		return nil, fmt.Errorf("invalid movie operator: %s", action.Operator)
	}
	resource := n.video[action.ResourceID]
	if resource == nil {
		media, err := n.source.Media(action.ResourceID)
		if err != nil {
			return nil, err
		}
		if media.Type != "Video" {
			return nil, fmt.Errorf("movie resource is not video: %s", action.ResourceID)
		}
		resource = &pdfVideoResource{Clip: &pdfgo.MediaClip{
			Subtype: "MCD", ContentType: pdfVideoContentType(media),
			File: n.mediaFile(media),
		}}
		if n.video == nil {
			n.video = make(map[string]*pdfVideoResource)
		}
		n.video[action.ResourceID] = resource
	}
	value := &pdfMovieAction{Resource: resource, Operation: operation}
	if operation != 0 {
		return &pdfNavigationAction{Movie: value}, nil
	}
	if len(pages) == 0 || page >= len(pages) {
		return nil, fmt.Errorf("missing movie playback page")
	}
	var rect pdfgo.Rectangle
	if source.Graphic {
		if page < 0 || source.Box.W <= 0 || source.Box.H <= 0 {
			return nil, fmt.Errorf("invalid movie playback region")
		}
		const scale = 72.0 / 25.4
		box := source.Box
		rect = pdfgo.Rectangle{XMin: box.X * scale, YMin: (pages[page].Box.H - box.Y - box.H) * scale, XMax: (box.X + box.W) * scale, YMax: (pages[page].Box.H - box.Y) * scale}
	} else {
		page = 0
	}
	for _, screen := range resource.Screens {
		if screen.Page == page && screen.Screen.Rect == rect {
			value.Target = screen
			return &pdfNavigationAction{Movie: value}, nil
		}
	}
	screen := &pdfVideoScreen{Page: page, Screen: &pdfgo.ScreenAnnotation{Rect: rect, Hidden: true}, Rendition: &pdfgo.Rendition{Subtype: "MR", Clip: resource.Clip}}
	if !source.Graphic {
		screen.Rendition.Screen = pdfgo.Dictionary{"MH": pdfgo.Dictionary{"W": pdfgo.Integer(0)}, "BE": pdfgo.Dictionary{"F": pdfgo.Dictionary{"D": pdfgo.Array{pdfgo.Integer(640), pdfgo.Integer(360)}, "R": pdfgo.Integer(1)}}}
	}
	resource.Screens = append(resource.Screens, screen)
	value.Target = screen
	return &pdfNavigationAction{Movie: value}, nil
}

// values 将资源控制关联到全部播放区域，重新播放前停止同一资源的其他实例
// 返回: []pdfgo.NavigationAction 有序呈现动作
func (a *pdfMovieAction) values() []pdfgo.NavigationAction {
	values := make([]pdfgo.NavigationAction, 0, len(a.Resource.Screens))
	for _, screen := range a.Resource.Screens {
		if screen == a.Target {
			continue
		}
		operation := a.Operation
		if operation == 0 {
			operation = 1
		}
		values = append(values, pdfgo.NavigationAction{Rendition: &pdfgo.RenditionAction{Target: screen.Screen, Operation: operation}})
	}
	if a.Target != nil {
		values = append(values, pdfgo.NavigationAction{Rendition: &pdfgo.RenditionAction{Target: a.Target.Screen, Rendition: a.Target.Rendition}})
	}
	return values
}

// writeVideo 在绘制后读取被播放的视频，原样嵌入并关联输出页面
// 入参: ctx 取消上下文, pages 输出页引用, options 替换配置
// 返回: error 资源读取或取消错误
func (n *pdfNavigation) writeVideo(ctx context.Context, pages []pdfgo.Reference, options *pdfgo.RewriteOptions) error {
	files := make(map[string]*pdfgo.FileSpecification)
	for _, id := range slices.Sorted(maps.Keys(n.video)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		resource := n.video[id]
		for _, screen := range resource.Screens {
			if screen.Page < 0 || screen.Page >= len(pages) {
				return fmt.Errorf("missing movie playback page")
			}
			if options.ScreenAnnotations == nil {
				options.ScreenAnnotations = make(map[pdfgo.Reference][]*pdfgo.ScreenAnnotation)
			}
			ref := pages[screen.Page]
			options.ScreenAnnotations[ref] = append(options.ScreenAnnotations[ref], screen.Screen)
			files[id] = resource.Clip.File
		}
	}
	if err := n.loadMediaFiles(ctx, files); err != nil {
		return err
	}
	for _, id := range slices.Sorted(maps.Keys(files)) {
		clip := n.video[id].Clip
		if clip.ContentType == "" {
			kind := http.DetectContentType(clip.File.Embedded.Data)
			if !strings.HasPrefix(kind, "video/") && kind != "application/ogg" {
				return fmt.Errorf("cannot identify video content type: %s", id)
			}
			clip.ContentType = kind
		}
	}
	return ctx.Err()
}

// pdfVideoContentType 转换明确声明的视频格式，未知格式留待检查文件头
// 入参: media 视频资源
// 返回: string 媒体类型，无法确定时为空
func pdfVideoContentType(media MultiMedia) string {
	format := strings.ToLower(strings.TrimSpace(media.Format))
	if format == "" {
		format = strings.TrimPrefix(strings.ToLower(path.Ext(media.MediaFile)), ".")
	}
	switch format {
	case "mp4", "m4v":
		return "video/mp4"
	case "mpeg", "mpg":
		return "video/mpeg"
	case "mov", "qt":
		return "video/quicktime"
	case "webm":
		return "video/webm"
	case "ogv", "ogg":
		return "video/ogg"
	case "avi":
		return "video/x-msvideo"
	case "wmv":
		return "video/x-ms-wmv"
	case "3gp":
		return "video/3gpp"
	case "3g2":
		return "video/3gpp2"
	}
	return ""
}
