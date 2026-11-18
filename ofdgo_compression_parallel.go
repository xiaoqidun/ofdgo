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
	"context"
	"crypto/sha256"
	"image"
	"io"
	"sync"

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
)

// outputImageWorkingSetLimit 指定未提供任务预算时的图像成本估算上限
const outputImageWorkingSetLimit = 256 << 20

// outputImageJob 保存独立图片任务，不访问编辑文档或调用进度回调
type outputImageJob struct {
	key        outputImageKey
	data       []byte
	workingSet int
	candidate  []byte
	err        error
}

// outputImageSource 保存已读取资源的摘要和格式，不保留原始编码
type outputImageSource struct {
	key    outputImageKey
	format string
}

// outputImageContext 保留调用方取消检查并支持提前结束并行任务
type outputImageContext struct {
	context.Context
	parent context.Context
}

// Err 检查调用方及本次任务的取消状态
// 返回: error 取消错误
func (c outputImageContext) Err() error {
	if err := c.Context.Err(); err != nil {
		return err
	}
	return c.parent.Err()
}

// outputImageOptions 统一资源压缩及像素需求，受保护图片只进行无损编码
// 入参: key 资源路径
// 返回: CompressionOptions 实际策略, image.Point 像素需求
func (e *Editor) outputImageOptions(key string) (CompressionOptions, image.Point) {
	options := e.output.options
	if !e.output.images[key] {
		options.Mode = CompressionLossless
	}
	demand := e.output.sizes[key]
	if options.Mode != CompressionLossy {
		options = CompressionOptions{Mode: options.Mode}
		demand = image.Point{}
	}
	return options, demand
}

// prepareOutputImages 有界并行优化独立图片，完成后补入任务，读取、缓存及进度保持串行
// 入参: keys 资源路径, load 串行读取方法，空值表示跳过, results 候选缓存, sources 原始资源摘要，可为空
// 返回: error 读取、进度或取消错误
func (e *Editor) prepareOutputImages(keys []string, load func(string) ([]byte, error), results map[outputImageKey][]byte, sources map[string]outputImageSource) error {
	workers := conversionWorkers(e.output.ctx)
	if workers < 2 || len(keys) < 2 {
		return nil
	}
	limit := int(min(int64(workers)*(64<<20), int64(int(^uint(0)>>1)-1)))
	encodedLimit := limit
	if pool, _ := e.output.ctx.Value(conversionPoolKey{}).(*ConversionPool); pool != nil {
		available := pool.memoryLimit()
		if available == 0 {
			return nil
		}
		if available > 0 {
			limit = int(min(available, int64(int(^uint(0)>>1))))
			encodedLimit = limit
		}
	}
	base, cancel := context.WithCancel(e.output.ctx)
	ctx := outputImageContext{Context: base, parent: e.output.ctx}
	var group sync.WaitGroup
	defer func() {
		cancel()
		group.Wait()
	}()
	completed := make(chan outputImageJob, workers)
	pending := make(map[outputImageKey]bool)
	running, size, workingSet := 0, 0, 0
	finish := func() error {
		job := <-completed
		running--
		size -= len(job.data)
		workingSet -= job.workingSet
		delete(pending, job.key)
		if err := e.output.ctx.Err(); err != nil {
			return err
		}
		results[job.key] = nil
		if job.err == nil && len(job.candidate) < len(job.data) {
			results[job.key] = job.candidate
		}
		return nil
	}
	for _, key := range keys {
		if err := e.output.ctx.Err(); err != nil {
			return err
		}
		for len(completed) != 0 || running == workers {
			if err := finish(); err != nil {
				return err
			}
		}
		if err := editorProgress(e.OnWriteProgress).withContext(e.output.ctx).report("compress", e.output.completed, 0); err != nil {
			return err
		}
		data, err := load(key)
		if err != nil {
			return err
		}
		if err := e.output.ctx.Err(); err != nil {
			return err
		}
		if len(data) == 0 {
			continue
		}
		options, demand := e.outputImageOptions(key)
		cacheKey := outputImageKey{hash: sha256.Sum256(data), options: options, size: demand}
		if sources != nil {
			sources[key] = outputImageSource{key: cacheKey, format: outputImageFormat(data)}
		}
		if _, cached := results[cacheKey]; cached {
			continue
		}
		if pending[cacheKey] {
			continue
		}
		cost := imageWorkingSet(e.output.ctx, data, limit)
		for running != 0 && (len(data) > encodedLimit-size || cost > limit-workingSet) {
			if err := finish(); err != nil {
				return err
			}
		}
		job := outputImageJob{key: cacheKey, data: data, workingSet: cost}
		if len(data) >= encodedLimit || cost >= limit {
			if err := e.runOutputImages([]outputImageJob{job}, results); err != nil {
				return err
			}
			continue
		}
		pending[cacheKey] = true
		running++
		size += len(data)
		workingSet += cost
		group.Go(func() {
			job.candidate, job.err = optimizeOutputImage(ctx, job)
			completed <- job
		})
	}
	for running != 0 {
		if err := finish(); err != nil {
			return err
		}
	}
	return e.output.ctx.Err()
}

// imageWorkingSet 按当前预算估算图像解码工作量
// 入参: ctx 取消上下文, data 图像编码, limit 估算上限
// 返回: int 工作量字节数
func imageWorkingSet(ctx context.Context, data []byte, limit int) int {
	if len(data) >= limit {
		return limit
	}
	input := bytes.NewReader(data)
	available := limit - len(data)
	if baseimage.IsTIFF(data) {
		layout, err := baseimage.ReadTIFFLayoutContext(ctx, input)
		if err != nil {
			return limit
		}
		return len(data) + tiffImageWorkingSet(layout, available)
	}
	config, _, err := baseimage.DecodeConfig(imageInput{ReadCloser: io.NopCloser(input), ctx: ctx})
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return limit
	}
	if config.Width > available/16/config.Height {
		return limit
	}
	return len(data) + config.Width*config.Height*16
}

// tiffImageWorkingSet 分别估算显示缓冲及主图、蒙版的完整瓦片存储，超预算时返回上限
// 入参: layout 已校验的主图布局, limit 可用字节数
// 返回: int 不超过limit的估算值
func tiffImageWorkingSet(layout baseimage.TIFFLayout, limit int) int {
	size := layout.DisplaySize()
	if size.X <= 0 || size.Y <= 0 || size.X > limit/16/size.Y {
		return limit
	}
	cost := size.X * size.Y * 16
	for source := &layout; source != nil; source = source.Transparency {
		width, height := uint64(source.Width), uint64(source.Height)
		if source.TileWidth != 0 {
			x, y := uint64(source.TileWidth), uint64(source.TileHeight)
			width, height = ((width-1)/x+1)*x, ((height-1)/y+1)*y
		}
		pixelSize := max(uint64(16), source.DecodedPixelBytes())
		if !source.Planar {
			pixelSize = max(pixelSize, uint64(source.Samples)*4)
		}
		if width > uint64(limit-cost)/pixelSize/height {
			return limit
		}
		cost += int(width * height * pixelSize)
	}
	return cost
}

// runOutputImages 等待整批结束后记录更小候选，失败图片保留原始编码
// 入参: jobs 独占图片任务, results 本次写出的候选缓存
// 返回: error 取消错误
func (e *Editor) runOutputImages(jobs []outputImageJob, results map[outputImageKey][]byte) error {
	ctx := e.output.ctx
	optimize := func(job *outputImageJob) {
		job.candidate, job.err = optimizeOutputImage(ctx, *job)
	}
	if len(jobs) == 1 {
		optimize(&jobs[0])
	} else {
		var group sync.WaitGroup
		for i := range jobs {
			group.Go(func() { optimize(&jobs[i]) })
		}
		group.Wait()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, job := range jobs {
		results[job.key] = nil
		if job.err == nil && len(job.candidate) < len(job.data) {
			results[job.key] = job.candidate
		}
	}
	return nil
}

// optimizeOutputImage 在共享预算内优化独立图像
// 入参: ctx 转换上下文, job 图像任务
// 返回: []byte 候选编码, error 转换错误
func optimizeOutputImage(ctx context.Context, job outputImageJob) ([]byte, error) {
	if pool, _ := ctx.Value(conversionPoolKey{}).(*ConversionPool); pool != nil {
		if job.workingSet == 0 {
			limit := outputImageWorkingSetLimit
			if available := pool.memoryLimit(); available > 0 {
				limit = int(min(available, int64(int(^uint(0)>>1))))
			}
			job.workingSet = imageWorkingSet(ctx, job.data, limit)
		}
		if err := pool.acquire(ctx, true, int64(job.workingSet)); err != nil {
			return nil, err
		}
		defer pool.release(true, int64(job.workingSet))
	}
	return optimizeImageResource(ctx, job.data, job.key.options, job.key.size)
}
