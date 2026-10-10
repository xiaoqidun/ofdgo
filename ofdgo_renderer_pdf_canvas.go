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
	"image"
	"image/color"
	"strconv"

	"github.com/tdewolff/canvas"
	cimage "github.com/tdewolff/canvas/image"
	"github.com/tdewolff/canvas/renderers/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// pdfRenderer PDF渲染器
type pdfRenderer struct {
	*pdf.PDF
	glyphPaths  map[*canvas.Path]*canvas.Path
	fonts       map[*canvas.Font]*canvas.Font
	images      [][]image.Image
	exactImages bool
	imageError  error
	navigation  *pdfNavigation
	text        []pdfPageText
	exactText   bool
}

// pdfPageText 记录后端文字对象数量及OFD原文所属区段
type pdfPageText struct {
	count        int
	replacements []pdfgo.TextReplacement
}

// pdfNavigation PDF导航信息
type pdfNavigation struct {
	Link        map[int][]pdfLink
	Outline     []pdfOutline
	Open        []pdfNavigationAction
	PageOpen    map[int][]pdfNavigationAction
	attachments map[string]Attachment
	audio       map[string]*pdfgo.FileSpecification
	video       map[string]*pdfVideoResource
	mediaFiles  map[string]*pdfgo.FileSpecification
	source      *Reader
	nativeWrite bool
}

// pdfLink PDF链接
type pdfLink struct {
	Rect    canvas.Rect
	Region  *pdfgo.LinkRegion
	Actions []pdfNavigationAction
}

// pdfTarget 保存输出页索引及默认用户空间中的跳转参数
type pdfTarget struct {
	Page        int
	Destination pdfgo.Destination
}

// pdfOutline PDF大纲
type pdfOutline struct {
	Name    string
	Level   int
	Actions []pdfNavigationAction
}

// pdfNavigationAction 保存待写出的导航动作，输出时关联实际页面引用
type pdfNavigationAction struct {
	URI        string
	Target     *pdfTarget
	Attachment *pdfgo.AttachmentAction
	Sound      *pdfgo.SoundAction
	Movie      *pdfMovieAction
}

// RenderImage 校验惰性图片并同步不透明画笔，保留解码错误及后端透明度状态
// 入参: img 图像, matrix 图像变换
func (r *pdfRenderer) RenderImage(img image.Image, matrix canvas.Matrix) {
	if r.imageError != nil {
		return
	}
	if source, ok := img.(interface{ Image() (image.Image, error) }); ok {
		pixels, err := source.Image()
		if err != nil {
			r.imageError = err
			return
		}
		if pixels == nil || pixels.Bounds() != img.Bounds() {
			r.imageError = fmt.Errorf("PDF output image pixel bounds differ")
			return
		}
	}
	if len(r.images) != 0 {
		var original image.Image
		opaque := false
		if source, ok := img.(interface{ Opaque() bool }); ok {
			opaque = source.Opaque()
		}
		switch img.ColorModel() {
		case color.GrayModel, color.CMYKModel:
		case color.RGBAModel, color.NRGBAModel, color.AlphaModel:
			if !opaque {
				original = img
			}
		default:
			if source, ok := img.(*cimage.Image); !ok || source.Mimetype != "image/jpeg" || source.Mask != nil {
				original = img
			}
		}
		r.exactImages = r.exactImages || original != nil
		last := len(r.images) - 1
		r.images[last] = append(r.images[last], original)
	}
	style := canvas.DefaultStyle
	style.Fill = canvas.Paint{Color: canvas.White}
	style.Stroke = canvas.Paint{Color: canvas.Black}
	r.PDF.RenderPath(&canvas.Path{}, style, canvas.Identity)
	r.PDF.RenderImage(img, matrix)
}

// glyphPath 复用相似变换下的PDF字形圆弧转换
// 入参: path 缓存字形路径, matrix 字形变换
// 返回: *canvas.Path PDF字形路径
func (r *pdfRenderer) glyphPath(path *canvas.Path, matrix canvas.Matrix) *canvas.Path {
	if !matrix.IsSimilarity() {
		return path
	}
	if cached, ok := r.glyphPaths[path]; ok {
		return cached
	}
	converted := path.ReplaceArcs()
	r.glyphPaths[path] = converted
	return converted
}

// newPDFNavigation 创建PDF导航信息
// 入参: renderer 渲染器, doc 文档结构, pages 页面数据
// 返回: *pdfNavigation PDF导航信息, error 区域或目标错误
func newPDFNavigation(renderer *Renderer, doc *Document, pages []RenderDocumentPage) (*pdfNavigation, error) {
	navigation := &pdfNavigation{
		Link:     make(map[int][]pdfLink),
		PageOpen: make(map[int][]pdfNavigationAction),
		source:   renderer.Reader,
	}
	if doc != nil && (doc.Attachments.Path != "" || len(doc.Attachments.Attachment) != 0) {
		attachments, err := renderer.Reader.Attachments()
		if err != nil {
			return nil, err
		}
		navigation.attachments = make(map[string]Attachment, len(attachments))
		for _, attachment := range attachments {
			if attachment.ID == "" {
				continue
			}
			if _, exists := navigation.attachments[attachment.ID]; exists {
				return nil, fmt.Errorf("duplicate attachment identifier: %s", attachment.ID)
			}
			navigation.attachments[attachment.ID] = attachment
		}
		for i, attachment := range attachments {
			if attachment.ID != "" {
				continue
			}
			key := fmt.Sprintf("attachment-%d", i+1)
			for {
				if _, exists := navigation.attachments[key]; !exists {
					break
				}
				key += "-"
			}
			navigation.attachments[key] = attachment
		}
		navigation.nativeWrite = len(attachments) != 0
	}
	pageIndex := make(map[string]int, len(pages))
	for i, page := range pages {
		pageIndex[page.Content.ID] = i
	}
	bookmarks := make(map[string]Dest)
	if doc != nil {
		for _, bookmark := range doc.Bookmarks.Bookmark {
			bookmarks[bookmark.Name] = bookmark.Dest
		}
		if err := navigation.addLifecycleActions(renderer.outputContext(), actionSource{Actions: doc.Actions}, -1, bookmarks, pageIndex, pages); err != nil {
			return nil, err
		}
	}
	for i, page := range pages {
		for _, source := range renderer.pageOpenActionSources(page.Content) {
			if err := navigation.addLifecycleActions(renderer.outputContext(), source, i, bookmarks, pageIndex, pages); err != nil {
				return nil, err
			}
		}
		sources := renderer.pageActionSources(page.Content)
		if renderer.RenderAnnotations {
			sources = append(sources, renderer.annotationActionSources(renderer.Reader.Annots[page.Content.ID])...)
		}
		for _, source := range sources {
			if err := navigation.addActions(renderer, i, source, bookmarks, pageIndex, pages); err != nil {
				return nil, err
			}
		}
	}
	if doc != nil {
		if err := navigation.addOutlines(doc.Outlines.OutlineElem, 0, bookmarks, pageIndex, pages); err != nil {
			return nil, err
		}
	}
	return navigation, nil
}

// actionTarget 转换动作及资源引用，选页未包含的目标不写入
// 入参: action 动作, page 来源页索引，负值表示文档, source 动作来源, bookmarks 书签, pageIndex 页面索引表, pages 页面数据
// 返回: *pdfNavigationAction 导航动作，nil表示无输出动作, error 目标参数错误
func (n *pdfNavigation) actionTarget(action Action, page int, source actionSource, bookmarks map[string]Dest, pageIndex map[string]int, pages []RenderDocumentPage) (*pdfNavigationAction, error) {
	if action.Goto != nil {
		dest := gotoDest(action.Goto, bookmarks)
		if dest == nil {
			return nil, nil
		}
		target, ok := pageIndex[dest.PageID]
		if !ok {
			return nil, nil
		}
		destination, err := pdfDestination(*dest, pages[target].Box.H)
		if err != nil {
			return nil, err
		}
		return &pdfNavigationAction{Target: &pdfTarget{Page: target, Destination: destination}}, nil
	}
	if action.URI != nil && action.URI.URI != "" {
		return &pdfNavigationAction{URI: resolveActionURI(*action.URI)}, nil
	}
	if action.GotoA != nil {
		if attachment, exists := n.attachments[action.GotoA.AttachID]; !exists || attachment.ID != action.GotoA.AttachID {
			return nil, fmt.Errorf("attachment not found: %s", action.GotoA.AttachID)
		}
		window := true
		if action.GotoA.NewWindow != nil {
			window = *action.GotoA.NewWindow
		}
		return &pdfNavigationAction{Attachment: &pdfgo.AttachmentAction{Key: action.GotoA.AttachID, NewWindow: &window}}, nil
	}
	if action.Sound != nil {
		return n.soundAction(*action.Sound)
	}
	if action.Movie != nil {
		return n.movieAction(*action.Movie, page, source, pages)
	}
	return nil, nil
}

// addOutlines 保留目录层级及节点自身的导航动作，选页时移除无保留子项的失效跳转节点
// 入参: outlines 大纲节点, level 节点层级, bookmarks 书签, pageIndex 页面索引表, pages 页面数据
// 返回: error 目标参数错误
func (n *pdfNavigation) addOutlines(outlines []OutlineElem, level int, bookmarks map[string]Dest, pageIndex map[string]int, pages []RenderDocumentPage) error {
	for _, outline := range outlines {
		entry := pdfOutline{Name: outline.Title, Level: level}
		onlyMissingTargets := len(outline.Actions) != 0
		for _, action := range outline.Actions {
			if action.Goto == nil {
				onlyMissingTargets = false
			}
			value, err := n.actionTarget(action, -1, actionSource{}, bookmarks, pageIndex, pages)
			if err != nil {
				return err
			}
			if value != nil {
				entry.Actions = append(entry.Actions, *value)
			}
		}
		index := len(n.Outline)
		n.Outline = append(n.Outline, entry)
		if err := n.addOutlines(outline.OutlineElem, level+1, bookmarks, pageIndex, pages); err != nil {
			return err
		}
		if onlyMissingTargets && len(entry.Actions) == 0 && len(n.Outline) == index+1 {
			n.Outline = n.Outline[:index]
		}
	}
	n.nativeWrite = n.nativeWrite || len(n.Outline) != 0
	return nil
}

// apply 生成链接及大纲结构，内部目标在原生写入时关联
// 入参: renderer PDF渲染器, page 页面索引
func (n *pdfNavigation) apply(renderer *pdf.PDF, page int) {
	for _, link := range n.Link[page] {
		renderer.AddLink("", link.Rect)
	}
	if page == 0 {
		for index, outline := range n.Outline {
			renderer.AddOutline(strconv.Itoa(index), outline.Level, 0)
		}
	}
}

// prepareRewrite 关联输出页引用并准备附件、事件、链接及先序大纲的替换
// 入参: ctx 取消上下文, reader 输出PDF, pages 输出页引用, options 替换配置
// 返回: error 资源、目录结构或取消错误
func (n *pdfNavigation) prepareRewrite(ctx context.Context, reader *pdfgo.Reader, pages []pdfgo.Reference, options *pdfgo.RewriteOptions) error {
	if err := n.writeAttachments(ctx, options); err != nil {
		return err
	}
	if err := n.loadAudio(ctx); err != nil {
		return err
	}
	if err := n.writeVideo(ctx, pages, options); err != nil {
		return err
	}
	if len(n.Open) != 0 {
		options.OpenActions = pdfNavigationValues(n.Open, pages)
	}
	if len(n.PageOpen) != 0 {
		options.PageOpenActions = make(map[pdfgo.Reference][]pdfgo.NavigationAction, len(n.PageOpen))
		for page, actions := range n.PageOpen {
			options.PageOpenActions[pages[page]] = pdfNavigationValues(actions, pages)
		}
	}
	options.LinkDestinations = make(map[pdfgo.AnnotationLocation]pdfgo.Destination)
	options.LinkActions = make(map[pdfgo.AnnotationLocation][]pdfgo.NavigationAction)
	for page, links := range n.Link {
		for index, link := range links {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(link.Actions) == 1 && link.Actions[0].Target != nil {
				target := link.Actions[0].Target
				dest := target.Destination
				dest.Page = pages[target.Page]
				options.LinkDestinations[pdfgo.AnnotationLocation{Page: pages[page], Index: index}] = dest
			} else {
				options.LinkActions[pdfgo.AnnotationLocation{Page: pages[page], Index: index}] = pdfNavigationValues(link.Actions, pages)
			}
		}
	}
	if len(n.Outline) == 0 {
		return nil
	}
	options.OutlineDestinations = make(map[pdfgo.Reference]pdfgo.Destination, len(n.Outline))
	options.OutlineTitles = make(map[pdfgo.Reference]string, len(n.Outline))
	options.OutlineActions = make(map[pdfgo.Reference][]pdfgo.NavigationAction)
	count := 0
	if err := reader.WalkOutlines(ctx, func(level int, item pdfgo.OutlineItem) error {
		if count >= len(n.Outline) || n.Outline[count].Level != level || strconv.Itoa(count) != item.Title {
			return fmt.Errorf("PDF output outline differs")
		}
		actions := n.Outline[count].Actions
		if len(actions) == 1 && actions[0].Target != nil {
			target := actions[0].Target
			dest := target.Destination
			dest.Page = pages[target.Page]
			options.OutlineDestinations[item.Reference] = dest
		} else {
			options.OutlineActions[item.Reference] = pdfNavigationValues(actions, pages)
		}
		options.OutlineTitles[item.Reference] = n.Outline[count].Name
		count++
		return nil
	}); err != nil {
		return err
	}
	if count != len(n.Outline) {
		return fmt.Errorf("PDF output outline missing")
	}
	return nil
}

// pdfSourceRect 转换PDF动作区域
// 入参: box OFD区域, pageH 页面高度
// 返回: canvas.Rect PDF区域
func pdfSourceRect(box Box, pageH float64) canvas.Rect {
	return canvas.RectFromSize(box.X, pageH-box.Y-box.H, box.W, box.H)
}

// pdfDestination 按OFD目标语义转换PDF坐标、显示模式及缩放比例
// 入参: dest OFD跳转目标, pageH 页面高度
// 返回: pdfgo.Destination 待关联输出页的目标, error 目标参数错误
func pdfDestination(dest Dest, pageH float64) (pdfgo.Destination, error) {
	dest = dest.effective()
	if _, err := dest.attributes(); err != nil {
		return pdfgo.Destination{}, err
	}
	const scale = 72.0 / 25.4
	result := pdfgo.Destination{Mode: pdfgo.Name(dest.Type)}
	switch dest.Type {
	case "XYZ":
		result.Parameters = pdfgo.Array{pdfgo.Real(dest.Left * scale), pdfgo.Real((pageH - dest.Top) * scale), pdfgo.Real(dest.Zoom)}
	case "FitH":
		result.Parameters = pdfgo.Array{pdfgo.Real((pageH - dest.Top) * scale)}
	case "FitV":
		result.Parameters = pdfgo.Array{pdfgo.Real(dest.Left * scale)}
	case "FitR":
		result.Parameters = pdfgo.Array{pdfgo.Real(dest.Left * scale), pdfgo.Real((pageH - dest.Bottom) * scale), pdfgo.Real(dest.Right * scale), pdfgo.Real((pageH - dest.Top) * scale)}
	}
	return result, nil
}
