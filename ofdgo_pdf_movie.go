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

	"github.com/xiaoqidun/pdfgo"
)

// movieResource 保存视频原始内容，按用途复用媒体或附件资源
// 入参: ctx 取消上下文, file 文件说明, attachment 是否作为附件保存
// 返回: string 媒体或附件标识，缺少外部资源时为空, error 读取、注册或取消错误
func (p *pdfImporter) movieResource(ctx context.Context, file pdfgo.FileSpecification, attachment bool) (string, error) {
	return p.mediaResource(ctx, file, "Video", pdfMediaFormat(file), attachment)
}

// movieAnnotation 保留视频外观及可表示的播放动作，无法等价转换时按策略处理
// 入参: ctx 取消上下文, page PDF页面, annotation 视频注解, strict 是否禁止语义损失
// 返回: error 媒体、外观、播放参数或取消错误
func (p *pdfImporter) movieAnnotation(ctx context.Context, page *pdfgo.Page, annotation pdfgo.Annotation, strict bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	movie, err := p.reader.ReadMovie(annotation.Dictionary["Movie"])
	if err != nil {
		return err
	}
	activation, err := p.reader.Resolve(annotation.Dictionary["A"])
	if err != nil {
		return err
	}
	if activation == pdfgo.Boolean(false) {
		return p.appearanceAnnotation(ctx, page, annotation)
	}
	if activation == pdfgo.Boolean(true) {
		activation = nil
	}
	parameters, err := p.reader.ReadMovieActivation(ctx, activation)
	if err != nil {
		return err
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

// pdfMoviePlaybackSupported 判断OFD视频动作能否保持播放内容、声音及控制方式
// 入参: movie 视频信息, parameters 播放参数
// 返回: bool 是否可直接转换，不含播放窗口差异
func pdfMoviePlaybackSupported(movie pdfgo.Movie, parameters pdfgo.MovieActivation) bool {
	return (parameters.Start == nil || parameters.Start.Value == 0) && parameters.Duration == nil && parameters.Rate == 1 && parameters.Volume == 1 && !parameters.ShowControls && !parameters.Synchronous && parameters.Mode == "Once" && movie.Rotate == 0
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
