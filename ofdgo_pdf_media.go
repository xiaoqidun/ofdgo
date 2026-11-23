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
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"math"
	"path"
	"reflect"
	"strings"

	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// errPDFMediaRead 标识媒体读取失败，避免作为可恢复的导航问题忽略
var errPDFMediaRead = errors.New("PDF media read failed")

// pdfMediaFileKey 区分媒体文件及注册用途，避免重复解码或调用文件解析器
type pdfMediaFileKey struct {
	Stream        *pdfgo.Stream
	Name          string
	System        pdfgo.Name
	Specification uintptr
	Attachment    bool
	Kind          string
	Format        string
}

// pdfMediaResource 保留资源标识和外部文件说明，避免缓存期间字典地址被复用
type pdfMediaResource struct {
	ID         string
	Dictionary pdfgo.Dictionary
}

// pdfMediaInstanceKey 区分播放目标与资源文件，避免不同注解共用控制状态
type pdfMediaInstanceKey struct {
	Page       int
	Reference  pdfgo.Reference
	Dictionary uintptr
	Resource   string
}

// pdfAttachmentKey 按显示名与原始数据复用PDF附件，不合并名称不同的附件
type pdfAttachmentKey struct {
	name   string
	digest [32]byte
}

// pdfMoviePlaybackSupported 判断OFD视频动作能否保持播放内容、声音及控制方式
// 入参: movie 视频信息, parameters 播放参数
// 返回: bool 是否可直接转换，不含播放窗口差异
func pdfMoviePlaybackSupported(movie pdfgo.Movie, parameters pdfgo.MovieActivation) bool {
	return (parameters.Start == nil || parameters.Start.Value == 0) && parameters.Duration == nil && parameters.Rate == 1 && parameters.Volume == 1 && !parameters.ShowControls && !parameters.Synchronous && parameters.Mode == "Once" && movie.Rotate == 0
}

// mediaInstance 为播放目标分配独立标识，同一目标的动作继续共用控制状态
// 入参: id 已注册的媒体资源, target 目标注解, page 所属页面索引
// 返回: string 独立资源标识, error 资源复制或取消错误
func (p *pdfImporter) mediaInstance(id string, target pdfgo.Annotation, page int) (string, error) {
	if err := p.ctx.Err(); err != nil {
		return "", err
	}
	key := pdfMediaInstanceKey{Page: page, Reference: target.Reference, Resource: id}
	if key.Reference == (pdfgo.Reference{}) {
		key.Dictionary = reflect.ValueOf(target.Dictionary).Pointer()
	}
	if resource, exists := p.mediaInstances[key]; exists {
		return resource.ID, nil
	}
	copy, err := p.editor.CopyMedia(id)
	if err != nil {
		return "", err
	}
	if p.mediaInstances == nil {
		p.mediaInstances = make(map[pdfMediaInstanceKey]pdfMediaResource)
	}
	p.mediaInstances[key] = pdfMediaResource{ID: copy, Dictionary: target.Dictionary}
	return copy, nil
}

// mediaResource 复用媒体文件的注册结果，区分格式、类别及附件用途
// 入参: ctx 取消上下文, file 文件说明, kind 媒体类别, format 文件格式, attachment 是否作为附件保存
// 返回: string 资源标识，外部资源不可用时为空, error 读取、注册或取消错误
func (p *pdfImporter) mediaResource(ctx context.Context, file pdfgo.FileSpecification, kind, format string, attachment bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	key := pdfMediaFileKey{Stream: file.Embedded, Name: file.Name, System: file.FileSystem, Attachment: attachment, Kind: kind, Format: format}
	if file.Embedded == nil && file.Dictionary != nil {
		key.Specification = reflect.ValueOf(file.Dictionary).Pointer()
	}
	if resource, exists := p.mediaResources[key]; exists {
		return resource.ID, nil
	}
	data, available, err := p.mediaData(ctx, file)
	if err != nil {
		return "", err
	}
	var id string
	if available {
		if attachment {
			id, err = p.attachmentFile(file.Name, data)
		} else {
			id, err = p.editor.AddMedia(kind, format, data)
		}
		if err != nil {
			return "", err
		}
	}
	if p.mediaResources == nil {
		p.mediaResources = make(map[pdfMediaFileKey]pdfMediaResource)
	}
	p.mediaResources[key] = pdfMediaResource{ID: id, Dictionary: file.Dictionary}
	return id, ctx.Err()
}

// soundResource 注册采样音频或调用方提供的外部音频，不自动访问文件
// 入参: ctx 取消上下文, sound 音频对象
// 返回: string 资源标识，不可用时为空, error 读取或封装错误
func (p *pdfImporter) soundResource(ctx context.Context, sound pdfgo.Sound) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var data []byte
	var err error
	format := "WAV"
	if sound.File != nil {
		var available bool
		data, available, err = p.mediaData(ctx, *sound.File)
		if err != nil || !available {
			return "", err
		}
		format = codecpdf.MediaFormat(*sound.File)
	} else {
		data, err = codecpdf.SoundWAV(ctx, sound)
	}
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return p.editor.AddMedia("Audio", format, data)
}

// soundLinkAction 保留音频点击动作，混音、交互限制及音量损失按策略报告
// 入参: object 音频动作, strict 是否禁止播放参数损失
// 返回: *Action 音频动作, error 结构、参数或资源错误
func (p *pdfImporter) soundLinkAction(object pdfgo.Object, strict bool) (*Action, error) {
	source, err := p.reader.ReadSoundAction(p.ctx, object)
	if err != nil {
		return nil, err
	}
	if source.Volume < 0 {
		return nil, &pdfgo.UnsupportedError{Feature: "negative sound action volume conversion"}
	}
	if strict {
		return nil, &pdfgo.UnsupportedError{Feature: "sound action playback policy conversion"}
	}
	volume := int(math.Round(source.Volume * 100))
	if source.Mix {
		p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF sound action mixing policy not transferred to OFD"})
	} else {
		p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF sound action exclusive playback policy not transferred to OFD"})
	}
	if source.Synchronous {
		p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF sound action interaction restriction not transferred to OFD; action waiting retained"})
	}
	if float64(volume)/100 != source.Volume {
		p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF sound action volume rounded to OFD integer volume"})
	}
	id, err := p.soundResource(p.ctx, source.Sound)
	if err != nil || id == "" {
		return nil, err
	}
	return &Action{Event: "CLICK", Sound: &Sound{ResourceID: id, Volume: &volume, Repeat: source.Repeat, Synchronous: source.Synchronous}}, nil
}

// interactiveAnnotation 保留3D或富媒体的静态外观和原始资源，不执行脚本
// 入参: ctx 取消上下文, page PDF页面, annotation 交互注解, strict 严格检查开关
// 返回: error 外观、资源或不可等价转换错误
func (p *pdfImporter) interactiveAnnotation(ctx context.Context, page *pdfgo.Page, annotation pdfgo.Annotation, strict bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strict {
		return &pdfgo.UnsupportedError{Feature: "interactive " + string(annotation.Subtype) + " conversion"}
	}
	var actions []Action
	if annotation.Subtype == "3D" {
		return p.threeDAnnotation(ctx, page, annotation)
	} else {
		media, err := p.reader.ReadRichMedia(ctx, annotation)
		if err != nil {
			return p.interactiveAppearanceFallback(ctx, page, annotation, "metadata", err)
		}
		config := media.Configurations[media.ActiveConfiguration]
		var playing *pdfgo.FileSpecification
		if (config.Subtype == "Sound" || config.Subtype == "Video") && len(config.Instances) == 1 && config.Instances[0].Subtype == config.Subtype {
			condition, err := p.reader.Resolve(media.Activation["Condition"])
			if err != nil {
				return err
			}
			style, err := p.reader.Resolve(media.Presentation["Style"])
			if err != nil {
				return err
			}
			if (condition == nil || condition == pdfgo.Name("XA")) && (style == nil || style == pdfgo.Name("Embedded")) && media.Activation["Scripts"] == nil {
				file := config.Instances[0].Asset
				data, available, err := p.mediaData(ctx, file)
				if err != nil {
					return err
				}
				if available {
					kind := "Video"
					if config.Subtype == "Sound" {
						kind = "Audio"
					}
					id, err := p.editor.AddMedia(kind, codecpdf.MediaFormat(file), data)
					if err != nil {
						return err
					}
					if kind == "Audio" {
						actions = append(actions, Action{Event: "CLICK", Sound: &Sound{ResourceID: id}})
					} else {
						actions = append(actions, Action{Event: "CLICK", Movie: &Movie{ResourceID: id, Operator: "Play"}})
					}
				}
				playing = &file
			}
		}
		files := append([]pdfgo.MediaAsset(nil), media.Assets...)
		seen := make(map[*pdfgo.Stream]bool)
		external := make(map[string]bool)
		if playing != nil {
			if playing.Embedded != nil {
				seen[playing.Embedded] = true
			} else {
				external[string(playing.FileSystem)+"\x00"+playing.Name] = true
			}
		}
		for _, config := range media.Configurations {
			for _, instance := range config.Instances {
				files = append(files, pdfgo.MediaAsset{File: instance.Asset})
			}
		}
		for index, asset := range files {
			key := string(asset.File.FileSystem) + "\x00" + asset.File.Name
			if asset.File.Embedded != nil && seen[asset.File.Embedded] || asset.File.Embedded == nil && external[key] {
				continue
			}
			data, available, err := p.mediaData(ctx, asset.File)
			if err != nil {
				return err
			}
			if available {
				name := path.Base(strings.ReplaceAll(asset.File.Name, "\\", "/"))
				if name == "" || name == "." || name == "/" {
					name = fmt.Sprintf("Media_%d_%d", p.page+1, index+1)
				}
				_, err = p.editor.AddAttachment(name, data)
				if err != nil {
					return err
				}
			}
			if asset.File.Embedded != nil {
				seen[asset.File.Embedded] = true
			} else {
				external[key] = true
			}
		}
	}
	if err := p.appearanceAnnotation(ctx, page, annotation, actions...); err != nil {
		return err
	}
	p.addWarning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF " + string(annotation.Subtype) + " appearance and available primary assets retained; advanced interaction not transferred to OFD"})
	return nil
}

// interactiveAppearanceFallback 保留交互注解的有效正常外观，取消时不继续转换
// 入参: ctx 取消上下文, page PDF页面, annotation 交互注解, kind 失败资源类型, cause 原始错误
// 返回: error 取消或外观转换错误
func (p *pdfImporter) interactiveAppearanceFallback(ctx context.Context, page *pdfgo.Page, annotation pdfgo.Annotation, kind string, cause error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	if err := p.appearanceAnnotation(ctx, page, annotation); err != nil {
		return err
	}
	p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: fmt.Sprintf("PDF %s %s unavailable; normal appearance retained: %v", annotation.Subtype, kind, cause)})
	return nil
}

// mediaData 读取媒体或附件，宽松模式缺少外部读取器时报告资源不可用
// 入参: ctx 取消上下文, file PDF文件说明
// 返回: []byte 原始文件, bool 是否可用, error 解码、外部读取或严格模式错误
func (p *pdfImporter) mediaData(ctx context.Context, file pdfgo.FileSpecification) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if file.Embedded == nil && p.resolveFile == nil && p.warning != nil {
		p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF external file unavailable: " + file.Name + "; appearance retained"})
		return nil, false, nil
	}
	data, err := p.reader.ReadFileData(ctx, file, p.resolveFile)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %w", errPDFMediaRead, err)
	}
	return data, true, nil
}

// movieResource 保存视频原始内容，按用途复用媒体或附件资源
// 入参: ctx 取消上下文, file 文件说明, attachment 是否作为附件保存
// 返回: string 媒体或附件标识，缺少外部资源时为空, error 读取、注册或取消错误
func (p *pdfImporter) movieResource(ctx context.Context, file pdfgo.FileSpecification, attachment bool) (string, error) {
	return p.mediaResource(ctx, file, "Video", codecpdf.MediaFormat(file), attachment)
}

// movieAnnotation 保留视频外观及可表示的播放动作，无法等价转换时按策略处理
// 入参: ctx 取消上下文, page PDF页面, annotation 视频注解, strict 是否禁止语义损失
// 返回: error 媒体、外观、播放参数或取消错误
func (p *pdfImporter) movieAnnotation(ctx context.Context, page *pdfgo.Page, annotation pdfgo.Annotation, strict bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	movie, err := p.reader.ReadMovie(annotation.Dictionary["Movie"])
	if err != nil && p.warning != nil && pdfAuxiliaryError(err) {
		value, resolveErr := p.reader.Resolve(annotation.Dictionary["Movie"])
		if resolveErr != nil {
			return resolveErr
		}
		if dict, ok := value.(pdfgo.Dictionary); ok && dict["Aspect"] != nil {
			corrected := maps.Clone(dict)
			delete(corrected, "Aspect")
			retry, readErr := p.reader.ReadMovie(corrected)
			if readErr == nil {
				if err := p.recoverPDFObject(ctx, "Annotation.Movie", annotation.Dictionary["Movie"], err); err != nil {
					return err
				}
				annotation.Dictionary = maps.Clone(annotation.Dictionary)
				annotation.Dictionary["Movie"] = corrected
				movie, err = retry, nil
			} else if !pdfAuxiliaryError(readErr) {
				return readErr
			}
		}
	}
	if err != nil {
		if err := p.recoverAnnotationAuxiliary(ctx, annotation, "Annotation.Movie", annotation.Dictionary["Movie"], err); err != nil {
			return err
		}
		return p.appearanceAnnotation(ctx, page, annotation)
	}
	activation, err := p.reader.Resolve(annotation.Dictionary["A"])
	if err != nil {
		if err := p.recoverAnnotationAuxiliary(ctx, annotation, "Annotation.Movie.A", annotation.Dictionary["A"], err); err != nil {
			return err
		}
		return p.appearanceAnnotation(ctx, page, annotation)
	}
	if activation == pdfgo.Boolean(false) {
		return p.appearanceAnnotation(ctx, page, annotation)
	}
	if activation == pdfgo.Boolean(true) {
		activation = nil
	}
	parameters, err := p.reader.ReadMovieActivation(ctx, activation)
	if err != nil {
		if err := p.recoverAnnotationAuxiliary(ctx, annotation, "Annotation.Movie.A", annotation.Dictionary["A"], err); err != nil {
			return err
		}
		return p.appearanceAnnotation(ctx, page, annotation)
	}
	if !pdfMoviePlaybackSupported(movie, parameters) {
		if err := p.moviePlaybackLoss(ctx, movie.File, strict); err != nil {
			return err
		}
		return p.appearanceAnnotation(ctx, page, annotation)
	}
	if parameters.FloatingScale != nil {
		if err := p.movieActionLoss("movie playback window", strict, true); err != nil {
			return err
		}
	}
	id, err := p.movieResource(ctx, movie.File, false)
	if err != nil {
		return err
	}
	if id == "" {
		return p.appearanceAnnotation(ctx, page, annotation)
	}
	id, err = p.mediaInstance(id, annotation, p.page)
	if err != nil {
		return err
	}
	return p.appearanceAnnotation(ctx, page, annotation, Action{Event: "CLICK", Movie: &Movie{ResourceID: id, Operator: "Play"}})
}

// movieLinkAction 转换当前页视频操作，不将指定播放区间或速度改为完整默认播放
// 入参: object PDF视频动作, source 触发注解, strict 是否禁止语义损失, currentPage 动作链当前页
// 返回: *Action OFD视频动作，不能安全转换时为空, error 参数、资源或严格模式错误
func (p *pdfImporter) movieLinkAction(object pdfgo.Object, source pdfgo.Annotation, strict bool, currentPage int) (*Action, error) {
	if currentPage < 0 || currentPage >= len(p.pageOrder) {
		if _, err := p.reader.ReadMovieAction(p.ctx, object); err != nil {
			return nil, err
		}
		return nil, p.movieActionLoss("movie target without a fixed current page", strict, false)
	}
	playback, err := p.reader.ReadMoviePlayback(p.ctx, p.pageOrder[currentPage], object)
	if err != nil {
		if !strict && errors.Is(err, pdfgo.ErrInvalidAction) {
			p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("invalid PDF movie action ignored: %v", err)})
			return nil, nil
		}
		return nil, err
	}
	if playback.Operation == "Play" {
		parameters := playback.Activation
		if !pdfMoviePlaybackSupported(playback.Movie, parameters) {
			return nil, p.moviePlaybackLoss(p.ctx, playback.Movie.File, strict)
		}
		if parameters.FloatingScale != nil {
			if err := p.movieActionLoss("movie playback window", strict, true); err != nil {
				return nil, err
			}
		}
	}
	id, err := p.movieResource(p.ctx, playback.Movie.File, false)
	if err != nil || id == "" {
		return nil, err
	}
	id, err = p.mediaInstance(id, playback.Annotation, currentPage)
	if err != nil {
		return nil, err
	}
	action := &Action{Event: "CLICK", Movie: &Movie{ResourceID: id, Operator: string(playback.Operation)}}
	if playback.Operation == "Play" && playback.Activation.FloatingScale == nil {
		if err := p.moviePlaybackBounds(action.Movie, source, playback.Annotation, currentPage, strict); err != nil {
			return nil, err
		}
	}
	return action, nil
}

// moviePlaybackBounds 保存同页嵌入视频的播放区域，不改变触发注解的点击区域
// 入参: movie OFD视频动作, source 触发注解, target 播放注解, currentPage 当前页, strict 是否禁止语义损失
// 返回: error 播放区域、标志或严格模式错误
func (p *pdfImporter) moviePlaybackBounds(movie *Movie, source, target pdfgo.Annotation, currentPage int, strict bool) error {
	if source.Subtype == "" || currentPage != p.page {
		return p.movieActionLoss("movie playback window", strict, true)
	}
	if source.Rect == target.Rect {
		return nil
	}
	flags, err := p.reader.ReadAnnotationFlags(target)
	if err != nil {
		return err
	}
	if flags&(8|16) != 0 {
		return p.movieActionLoss("movie playback window scaling", strict, true)
	}
	box := pdfBounds([]pdfgo.Point{
		p.matrix.Apply(pdfgo.Point{X: target.Rect.XMin, Y: target.Rect.YMin}),
		p.matrix.Apply(pdfgo.Point{X: target.Rect.XMax, Y: target.Rect.YMin}),
		p.matrix.Apply(pdfgo.Point{X: target.Rect.XMax, Y: target.Rect.YMax}),
		p.matrix.Apply(pdfgo.Point{X: target.Rect.XMin, Y: target.Rect.YMax}),
	})
	if box.W <= 0 || box.H <= 0 {
		return p.movieActionLoss("empty movie playback window", strict, true)
	}
	if p.movieBounds == nil {
		p.movieBounds = make(map[*Movie]Box)
	}
	p.movieBounds[movie] = box
	return nil
}

// annotationMovieBounds 校验同一动作链的播放区域，保持单一图元上的动作顺序
// 入参: actions 注解动作, source 触发区域, flags 触发注解标志
// 返回: Box 播放区域, error 无法等价转换的窗口要求
func (p *pdfImporter) annotationMovieBounds(actions []Action, source Box, flags int64) (Box, error) {
	playback, found := source, false
	for _, action := range actions {
		if action.Movie == nil || action.Movie.Operator != "" && action.Movie.Operator != "Play" {
			continue
		}
		box, ok := p.movieBounds[action.Movie]
		if !ok {
			box = source
		}
		if found && playback != box || box != source && flags&(8|16) != 0 {
			return source, p.movieActionLoss("multiple or fixed-scale movie playback windows", p.warning == nil, true)
		}
		playback, found = box, true
	}
	return playback, nil
}

// moviePlaybackLoss 保留不可等价转换的视频文件，不生成改变播放内容的动作
// 入参: ctx 取消上下文, file 视频文件说明, strict 是否禁止语义损失
// 返回: error 严格模式、资源读取或取消错误
func (p *pdfImporter) moviePlaybackLoss(ctx context.Context, file pdfgo.FileSpecification, strict bool) error {
	if strict {
		return &pdfgo.UnsupportedError{Feature: "movie playback parameters conversion"}
	}
	id, err := p.movieResource(ctx, file, true)
	if err != nil {
		return err
	}
	message := "PDF movie playback parameters not transferred to OFD; action omitted"
	if id != "" {
		message += "; original media retained as attachment"
	}
	p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: message})
	return nil
}

// movieActionLoss 按策略报告OFD无法表达的视频参数，不自行添加扩展属性
// 入参: feature 差异内容, strict 严格模式, retained 是否保留视频操作
// 返回: error 严格模式下的不可等价转换错误
func (p *pdfImporter) movieActionLoss(feature string, strict, retained bool) error {
	if strict {
		return &pdfgo.UnsupportedError{Feature: feature + " conversion"}
	}
	suffix := "; action omitted"
	if retained {
		suffix = "; video operation retained"
	}
	p.warning(pdfgo.Diagnostic{Message: "PDF " + feature + " not transferred to OFD" + suffix})
	return nil
}

// documentAttachments 导入文档名称树中的附件，与注解及动作引用共用附件资源
// 入参: ctx 取消上下文
// 返回: error 名称树、附件读取或写入错误
func (p *pdfImporter) documentAttachments(ctx context.Context) error {
	var original pdfgo.Object
	if p.warning != nil {
		names, err := p.pdfCatalogObject("Names")
		if err != nil {
			return err
		}
		value, err := p.reader.Resolve(names)
		if err != nil {
			return p.recoverPDFObject(ctx, "Names", names, err)
		}
		if dictionary, ok := value.(pdfgo.Dictionary); ok {
			original = dictionary["EmbeddedFiles"]
			if original != nil {
				if err := p.preservePDFObject(ctx, "EmbeddedFiles", original); err != nil {
					return err
				}
			}
		} else if value != nil {
			return p.recoverPDFObject(ctx, "Names", names, fmt.Errorf("invalid names dictionary"))
		}
	}
	err := p.reader.WalkEmbeddedFiles(ctx, func(_ string, file pdfgo.FileSpecification) error {
		if file.Embedded == nil {
			if p.warning == nil {
				return fmt.Errorf("PDF embedded file has no embedded data")
			}
			p.warning(pdfgo.Diagnostic{Message: "PDF embedded file without embedded data ignored"})
			return nil
		}
		data, err := p.reader.DecodeStreamContext(ctx, file.Embedded)
		if err != nil {
			if p.warning != nil && pdfAuxiliaryError(err) {
				p.warning(pdfgo.Diagnostic{Message: "PDF document attachment retained as metadata: " + err.Error()})
				return nil
			}
			return err
		}
		_, err = p.attachmentFile(file.Name, data)
		if err != nil && p.warning != nil && pdfAuxiliaryError(err) {
			p.warning(pdfgo.Diagnostic{Message: "PDF document attachment retained as metadata: " + err.Error()})
			return nil
		}
		return err
	})
	if err != nil {
		return p.recoverPDFObject(ctx, "EmbeddedFiles", original, err)
	}
	return ctx.Err()
}

// attachmentFile 按原名及数据复用附件，兼容末尾NUL，缺名使用固定名称
// 入参: name 附件显示名, data 原始文件数据
// 返回: string 附件标识, error 附件写入错误
func (p *pdfImporter) attachmentFile(name string, data []byte) (string, error) {
	if name == "" {
		name = "Attachment"
	}
	key := pdfAttachmentKey{name: name, digest: sha256.Sum256(data)}
	if id, ok := p.attachmentIDs[key]; ok {
		return id, nil
	}
	if p.warning != nil {
		name = strings.TrimRight(name, "\x00")
		if name == "" {
			name = "Attachment"
		}
	}
	id, err := p.editor.AddAttachment(name, data)
	if err != nil {
		return "", err
	}
	if p.attachmentIDs == nil {
		p.attachmentIDs = make(map[pdfAttachmentKey]string)
	}
	p.attachmentIDs[key] = id
	if name != key.name {
		p.warning(pdfgo.Diagnostic{Message: "PDF attachment name trailing NUL removed; file data retained"})
	}
	return id, nil
}
