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
	"encoding/binary"
	"fmt"
	"image"
	"io"
	"math"
	"sync"

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
	"github.com/xiaoqidun/pdfgo"
)

// ImagePageSource 图片页面来源，按列表顺序逐张读取，每张图片处理完成后不再引用返回数据
// Read不可重入修改编辑器，取消时应返回上下文错误
type ImagePageSource struct {
	Name string
	Read func(context.Context) ([]byte, error)
}

// ImagePageOptions 图片建页选项，尺寸和边距单位为毫米
// Width和Height均为零时随图建页，否则按纸张等比居中，不裁剪图片
// DPI为缺少物理尺寸时的分辨率，零值使用96；图片方向和有效密度优先采用原图信息
// OnProgress按images、commit阶段回报进度，返回错误则撤销整个操作，不可重入修改编辑器
type ImagePageOptions struct {
	Width, Height float64
	Margin        float64
	DPI           float64
	OnProgress    func(stage string, completed, total int) error
}

// ImagePreviewInfo 保存图片按方向和密度换算后的毫米尺寸
type ImagePreviewInfo struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// editorImagePageInfo 保存同批次已校验图片的建页信息与可选压缩载荷
type editorImagePageInfo struct {
	size          image.Point
	format        string
	metadata      baseimage.PageInfo
	payload       []byte
	payloadFormat string
}

// editorPreparedImage 保存并发预处理后的建页元数据与待写入载荷
type editorPreparedImage struct {
	checksum [32]byte
	info     editorImagePageInfo
	data     []byte
	format   string
}

// editorImageTask 保存单张导入图片的并发预处理状态
type editorImageTask struct {
	name     string
	data     []byte
	prepared editorPreparedImage
	err      error
	done     chan struct{}
}

// RenderImagePreview 输出最长边不超过size像素的PNG预览，并返回建页物理尺寸
// 使用原图方向和密度，缺少密度时使用96DPI，不修改源图片
// 入参: ctx 取消上下文, data 图片数据, writer 预览输出流, size 最长边像素数
// 返回: ImagePreviewInfo 物理尺寸, error 解码、取消或输出错误
func RenderImagePreview(ctx context.Context, data []byte, writer io.Writer, size int) (ImagePreviewInfo, error) {
	if size <= 0 {
		return ImagePreviewInfo{}, fmt.Errorf("invalid preview size")
	}
	config, format, err := baseimage.DecodeConfigDataContext(ctx, data)
	if err != nil {
		return ImagePreviewInfo{}, err
	}
	metadata, err := baseimage.PageMetadataContext(ctx, data, format, 96)
	if err != nil {
		return ImagePreviewInfo{}, err
	}
	info := ImagePreviewInfo{Width: float64(config.Width) * 25.4 / metadata.XDPI, Height: float64(config.Height) * 25.4 / metadata.YDPI}
	pixels, _, err := baseimage.DecodePixelsContext(ctx, data)
	if err != nil {
		return ImagePreviewInfo{}, err
	}
	scale := min(1, float64(size)/float64(max(config.Width, config.Height)))
	pixels, err = pdfgo.ResizeImage(ctx, pixels, image.Pt(max(1, int(float64(config.Width)*scale)), max(1, int(float64(config.Height)*scale))))
	if err != nil {
		return ImagePreviewInfo{}, err
	}
	bounds := pixels.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	width, height := w, h
	if metadata.Orientation >= 5 {
		width, height = h, w
		info.Width, info.Height = info.Height, info.Width
	}
	preview := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range h {
		if err := ctx.Err(); err != nil {
			return ImagePreviewInfo{}, err
		}
		for x := range w {
			dx, dy := x, y
			switch metadata.Orientation {
			case 2:
				dx = w - 1 - x
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dy = h - 1 - y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			preview.Set(dx, dy, pixels.At(bounds.Min.X+x, bounds.Min.Y+y))
		}
	}
	return info, pdfgo.EncodePNG(ctx, writer, preview)
}

// prepareImagePage 对单张图片执行摘要计算、轻量结构校验、元数据读取及按需黑白PNG压缩
// 入参: ctx 取消上下文, data 原始图片字节, options 建页选项, verified 批次复用表, mu 并发保护锁，单线程可为nil
// 返回: editorPreparedImage 预处理结果, error 校验或取消错误
func prepareImagePage(ctx context.Context, data []byte, options ImagePageOptions, verified map[[32]byte]editorImagePageInfo, mu *sync.Mutex) (editorPreparedImage, error) {
	if err := ctx.Err(); err != nil {
		return editorPreparedImage{}, err
	}
	checksum := sha256.Sum256(data)
	var info editorImagePageInfo
	known := false
	if mu != nil {
		mu.Lock()
		info, known = verified[checksum]
		mu.Unlock()
	} else {
		info, known = verified[checksum]
	}
	if known {
		payload, payloadFormat := data, info.format
		if info.payload != nil {
			payload, payloadFormat = info.payload, info.payloadFormat
		}
		return editorPreparedImage{checksum: checksum, info: info, data: payload, format: payloadFormat}, nil
	}
	config, format, err := baseimage.DecodeConfigDataContext(ctx, data)
	if err != nil {
		return editorPreparedImage{}, err
	}
	if format != "png" && format != "jpeg" && format != "bmp" && format != "tiff" && format != "jbig2" {
		return editorPreparedImage{}, fmt.Errorf("only PNG, JPEG, BMP, single-page TIFF and JBIG2 images are supported")
	}
	if config.Width <= 0 || config.Height <= 0 {
		return editorPreparedImage{}, fmt.Errorf("invalid image dimensions")
	}
	if err := validateImageStructure(ctx, data, format); err != nil {
		return editorPreparedImage{}, err
	}
	metadata, err := baseimage.PageMetadataContext(ctx, data, format, options.DPI)
	if err != nil {
		return editorPreparedImage{}, err
	}
	payload, payloadFormat := data, format
	if format == "png" && uint64(config.Width)*uint64(config.Height) <= 64<<20 && pngBinarySource(ctx, data) && !pngNonBinaryImage(ctx, data) {
		decoded, _, err := baseimage.DecodePixelsContext(ctx, data)
		if err != nil {
			return editorPreparedImage{}, err
		}
		compact, err := editorBinaryImage(ctx, data, decoded)
		if err != nil {
			return editorPreparedImage{}, err
		}
		if compact != nil {
			payload, payloadFormat = compact, "jbig2"
		}
	}
	info = editorImagePageInfo{size: image.Pt(config.Width, config.Height), format: format, metadata: metadata}
	if payloadFormat != format {
		info.payload = payload
		info.payloadFormat = payloadFormat
	}
	if mu != nil {
		mu.Lock()
		verified[checksum] = info
		mu.Unlock()
	} else {
		verified[checksum] = info
	}
	return editorPreparedImage{checksum: checksum, info: info, data: payload, format: payloadFormat}, nil
}

// validateImageStructure 对图片容器做轻量结构校验，避免对非PNG格式执行整图像素解码
// 入参: ctx 取消上下文, data 图片编码数据, format 归一化格式名
// 返回: error 结构或取消错误
func validateImageStructure(ctx context.Context, data []byte, format string) error {
	switch format {
	case "jpeg":
		return validateJPEGStructure(ctx, data)
	case "bmp":
		return validateBMPStructure(ctx, data)
	case "tiff":
		return validateTIFFStructure(ctx, data)
	case "png":
		return validatePNGStructure(ctx, data)
	case "jbig2":
		return validateJBIG2Structure(ctx, data)
	default:
		return fmt.Errorf("unsupported image format %s", format)
	}
}

// validateJPEGStructure 扫描JPEG标记段与熵编码边界，验证完整帧与扫描结束标记
// 入参: ctx 取消上下文, data JPEG编码数据
// 返回: error 格式或截断错误
func validateJPEGStructure(ctx context.Context, data []byte) error {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return fmt.Errorf("invalid JPEG header")
	}
	pos := 2
	hasSOF, hasSOS := false, false
	for pos < len(data) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if data[pos] != 0xff {
			return fmt.Errorf("invalid JPEG marker")
		}
		for pos < len(data) && data[pos] == 0xff {
			pos++
		}
		if pos >= len(data) {
			return fmt.Errorf("truncated JPEG image")
		}
		marker := data[pos]
		pos++
		if marker == 0xd9 {
			if !hasSOF || !hasSOS {
				return fmt.Errorf("incomplete JPEG image")
			}
			return nil
		}
		if marker == 0xd8 {
			return fmt.Errorf("unexpected JPEG SOI marker")
		}
		if marker == 0x00 || marker == 0x01 || (marker >= 0xd0 && marker <= 0xd7) {
			continue
		}
		if pos+2 > len(data) {
			return fmt.Errorf("truncated JPEG segment")
		}
		segLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
		if segLen < 2 || pos+segLen > len(data) {
			return fmt.Errorf("truncated JPEG segment")
		}
		switch marker {
		case 0xc0, 0xc1, 0xc2:
			if segLen < 8 || data[pos+2] != 8 {
				return fmt.Errorf("unsupported JPEG precision")
			}
			hasSOF = true
		case 0xc3, 0xc5, 0xc6, 0xc7, 0xc9, 0xca, 0xcb, 0xcd, 0xce, 0xcf:
			return fmt.Errorf("unsupported JPEG SOF marker 0x%02x", marker)
		}
		pos += segLen
		if marker == 0xda {
			hasSOS = true
			for {
				if err := ctx.Err(); err != nil {
					return err
				}
				idx := bytes.IndexByte(data[pos:], 0xff)
				if idx < 0 {
					return fmt.Errorf("truncated JPEG scan")
				}
				markerPos := pos + idx
				pos = markerPos
				for pos < len(data) && data[pos] == 0xff {
					pos++
				}
				if pos >= len(data) {
					return fmt.Errorf("truncated JPEG scan")
				}
				next := data[pos]
				if next == 0x00 || (next >= 0xd0 && next <= 0xd7) {
					pos++
					continue
				}
				pos = markerPos
				break
			}
		}
	}
	return fmt.Errorf("truncated JPEG image")
}

// validateBMPStructure 校验BMP头与像素数据边界，非RLE位图跳过整图解码
// 入参: ctx 取消上下文, data BMP编码数据
// 返回: error 格式或截断错误
func validateBMPStructure(ctx context.Context, data []byte) error {
	offset, width, height, bpp, compression, err := baseimage.ParseBMPHeader(data)
	if err != nil {
		return err
	}
	if offset > len(data) {
		return fmt.Errorf("invalid bmp pixel offset")
	}
	if height < 0 {
		height = -height
	}
	if width <= 0 || height <= 0 {
		return fmt.Errorf("invalid bmp size")
	}
	if compression == 0 || compression == 3 {
		if bpp == 16 || bpp == 24 || bpp == 32 {
			if _, err := baseimage.BMPColorMasks(data, offset, bpp, compression); err != nil {
				return err
			}
		}
		stride := ((int64(width)*int64(bpp) + 31) / 32) * 4
		if stride <= 0 || stride > int64(len(data)-offset)/int64(height) {
			return fmt.Errorf("truncated bmp")
		}
		return nil
	}
	_, _, err = baseimage.DecodePixelsContext(ctx, data)
	return err
}

// validateTIFFStructure 校验单页TIFF目录的压缩编码与条带或瓦片字节范围
// 入参: ctx 取消上下文, data TIFF编码数据
// 返回: error 结构或越界错误
func validateTIFFStructure(ctx context.Context, data []byte) error {
	layout, err := baseimage.ReadTIFFLayoutContext(ctx, bytes.NewReader(data))
	if err != nil {
		return err
	}
	var order binary.ByteOrder = binary.LittleEndian
	if data[0] == 'M' {
		order = binary.BigEndian
	}
	if err := validateTIFFDirectory(data, order, int(layout.Offset)); err != nil {
		return err
	}
	if layout.Transparency != nil {
		if err := validateTIFFDirectory(data, order, int(layout.Transparency.Offset)); err != nil {
			return err
		}
	}
	return nil
}

// validateTIFFDirectory 校验单个TIFF目录的压缩编码与条带或瓦片字节范围
// 入参: data TIFF编码数据, order 字节序, offset 目录偏移
// 返回: error 结构或越界错误
func validateTIFFDirectory(data []byte, order binary.ByteOrder, offset int) error {
	if offset < 8 || offset+2 > len(data) {
		return fmt.Errorf("invalid TIFF directory offset")
	}
	count := int(order.Uint16(data[offset:]))
	if offset+2+count*12 > len(data) {
		return fmt.Errorf("truncated TIFF directory")
	}
	compression := uint64(1)
	var stripOffsets, stripCounts, tileOffsets, tileCounts []uint64
	readValues := func(kind uint16, number uint32, field []byte) ([]uint64, error) {
		elemSize := 0
		switch kind {
		case 3:
			elemSize = 2
		case 4:
			elemSize = 4
		default:
			return nil, fmt.Errorf("invalid TIFF offset field type")
		}
		total := uint64(number) * uint64(elemSize)
		raw := field
		if total > 4 {
			valOff := uint64(order.Uint32(field))
			if valOff+total > uint64(len(data)) {
				return nil, fmt.Errorf("truncated TIFF tag payload")
			}
			raw = data[valOff : valOff+total]
		}
		values := make([]uint64, number)
		for j := range number {
			if elemSize == 2 {
				values[j] = uint64(order.Uint16(raw[j*2:]))
			} else {
				values[j] = uint64(order.Uint32(raw[j*4:]))
			}
		}
		return values, nil
	}
	for i := range count {
		entry := data[offset+2+i*12 : offset+14+i*12]
		tag, kind, number := order.Uint16(entry), order.Uint16(entry[2:]), order.Uint32(entry[4:])
		var err error
		switch tag {
		case 259:
			if kind != 3 || number != 1 {
				return fmt.Errorf("invalid TIFF compression field")
			}
			compression = uint64(order.Uint16(entry[8:10]))
		case 273:
			stripOffsets, err = readValues(kind, number, entry[8:12])
		case 279:
			stripCounts, err = readValues(kind, number, entry[8:12])
		case 324:
			tileOffsets, err = readValues(kind, number, entry[8:12])
		case 325:
			tileCounts, err = readValues(kind, number, entry[8:12])
		}
		if err != nil {
			return err
		}
	}
	switch compression {
	case 0, 1, 2, 3, 4, 5, 8, 32773, 32946:
	default:
		return fmt.Errorf("unsupported TIFF compression %d", compression)
	}
	checkRanges := func(offsets, counts []uint64) error {
		if len(offsets) != len(counts) {
			return fmt.Errorf("mismatched TIFF data ranges")
		}
		for i := range offsets {
			if offsets[i] > uint64(len(data)) || counts[i] > uint64(len(data))-offsets[i] {
				return fmt.Errorf("truncated TIFF image data")
			}
		}
		return nil
	}
	if len(stripOffsets) > 0 {
		return checkRanges(stripOffsets, stripCounts)
	}
	if len(tileOffsets) > 0 {
		return checkRanges(tileOffsets, tileCounts)
	}
	return fmt.Errorf("missing TIFF image data")
}

// validatePNGStructure 校验PNG签名、调色板、IDAT压缩头、APNG与块边界完整性
// 入参: ctx 取消上下文, data PNG编码数据
// 返回: error 格式或截断错误
func validatePNGStructure(ctx context.Context, data []byte) error {
	if len(data) < 33 || string(data[:8]) != "\x89PNG\r\n\x1a\n" {
		return fmt.Errorf("invalid PNG header")
	}
	hasIHDR, hasPLTE, needPLTE, hasIDAT := false, false, false, false
	var zlibHeader [2]byte
	zlibLen := 0
	for pos := 8; pos < len(data); {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(data)-pos < 12 {
			return fmt.Errorf("truncated PNG chunk")
		}
		length := uint64(binary.BigEndian.Uint32(data[pos:]))
		if length > uint64(len(data)-pos-12) {
			return fmt.Errorf("truncated PNG chunk")
		}
		kind := string(data[pos+4 : pos+8])
		payload := data[pos+8 : pos+8+int(length)]
		switch kind {
		case "IHDR":
			if length != 13 {
				return fmt.Errorf("invalid PNG IHDR chunk")
			}
			needPLTE = payload[9] == 3
			hasIHDR = true
		case "PLTE":
			if length == 0 || length%3 != 0 || length > 768 {
				return fmt.Errorf("invalid PNG PLTE chunk")
			}
			hasPLTE = true
		case "acTL":
			return fmt.Errorf("animated PNG images are not supported")
		case "IDAT":
			hasIDAT = true
			if zlibLen < 2 && len(payload) > 0 {
				zlibLen += copy(zlibHeader[zlibLen:], payload)
			}
		case "IEND":
			if length != 0 || !hasIHDR || !hasIDAT || (needPLTE && !hasPLTE) || zlibLen < 2 {
				return fmt.Errorf("incomplete PNG image")
			}
			cmf, flg := zlibHeader[0], zlibHeader[1]
			if cmf&0x0f != 8 || cmf>>4 > 7 || flg&0x20 != 0 || (uint16(cmf)<<8|uint16(flg))%31 != 0 {
				return fmt.Errorf("invalid PNG zlib header")
			}
			return nil
		}
		pos += 12 + int(length)
	}
	return fmt.Errorf("truncated PNG image")
}

// validateJBIG2Structure 校验JBIG2标准文件魔数及段头与数据边界完整性
// 入参: ctx 取消上下文, data JBIG2编码数据
// 返回: error 格式或截断错误
func validateJBIG2Structure(ctx context.Context, data []byte) error {
	if len(data) < 9 || string(data[:8]) != "\x97JB2\r\n\x1a\n" {
		return fmt.Errorf("invalid JBIG2 header")
	}
	flags := data[8]
	randomAccess := flags&1 == 0
	unknownPages := flags&2 != 0
	pos := 9
	if !unknownPages {
		if len(data) < 13 {
			return fmt.Errorf("truncated JBIG2 header")
		}
		pos = 13
	}
	segments := 0
	hasEOF := false
	hasUnknownLen := false
	var totalData uint64
	for pos < len(data) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if pos+6 > len(data) {
			return fmt.Errorf("truncated JBIG2 segment header")
		}
		segNum := binary.BigEndian.Uint32(data[pos : pos+4])
		segFlags := data[pos+4]
		segType := segFlags & 0x3f
		pageAssocLong := segFlags&0x40 != 0
		pos += 5
		refCount := int(data[pos] >> 5)
		if refCount == 5 || refCount == 6 {
			return fmt.Errorf("invalid JBIG2 referred segment count")
		}
		if refCount == 7 {
			if pos+4 > len(data) {
				return fmt.Errorf("truncated JBIG2 segment header")
			}
			refCount = int(binary.BigEndian.Uint32(data[pos:pos+4]) & 0x1fffffff)
			pos += 4
			if refCount > 1024 {
				return fmt.Errorf("invalid JBIG2 referred segment count")
			}
			retainBytes := (refCount + 8) / 8
			if pos+retainBytes > len(data) {
				return fmt.Errorf("truncated JBIG2 segment header")
			}
			pos += retainBytes
		} else {
			pos++
		}
		refSize := 1
		if segNum > 65536 {
			refSize = 4
		} else if segNum > 256 {
			refSize = 2
		}
		pageAssocSize := 1
		if pageAssocLong {
			pageAssocSize = 4
		}
		need := refCount*refSize + pageAssocSize + 4
		if need < 0 || pos+need > len(data) {
			return fmt.Errorf("truncated JBIG2 segment header")
		}
		pos += refCount*refSize + pageAssocSize
		dataLen := binary.BigEndian.Uint32(data[pos : pos+4])
		pos += 4
		segments++
		if !randomAccess {
			if dataLen == 0xffffffff {
				if segType != 38 && segType != 39 {
					return fmt.Errorf("invalid JBIG2 unknown segment length")
				}
				return nil
			}
			if uint64(dataLen) > uint64(len(data)-pos) {
				return fmt.Errorf("truncated JBIG2 segment data")
			}
			pos += int(dataLen)
			if segType == 51 {
				hasEOF = true
				break
			}
		} else {
			if dataLen == 0xffffffff {
				if segType != 38 && segType != 39 {
					return fmt.Errorf("invalid JBIG2 unknown segment length")
				}
				hasUnknownLen = true
			} else {
				totalData += uint64(dataLen)
			}
			if segType == 51 {
				hasEOF = true
				break
			}
		}
	}
	if segments == 0 {
		return fmt.Errorf("empty JBIG2 image")
	}
	if randomAccess {
		if !hasEOF {
			return fmt.Errorf("truncated JBIG2 segment headers")
		}
		if !hasUnknownLen && totalData > uint64(len(data)-pos) {
			return fmt.Errorf("truncated JBIG2 segment data")
		}
	}
	return nil
}

// imagePageOrientation 将EXIF方向映射为单位图像到页面局部坐标的变换
// 入参: orientation EXIF方向值, w 页面上的图片宽度，单位为毫米, h 页面上的图片高度，单位为毫米
// 返回: Matrix 图像坐标变换
func imagePageOrientation(orientation int, w, h float64) Matrix {
	switch orientation {
	case 2:
		return Matrix{a: -w, d: h, e: w}
	case 3:
		return Matrix{a: -w, d: -h, e: w, f: h}
	case 4:
		return Matrix{a: w, d: -h, f: h}
	case 5:
		return Matrix{b: h, c: w}
	case 6:
		return Matrix{b: h, c: -w, e: w}
	case 7:
		return Matrix{b: -h, c: -w, e: w, f: h}
	case 8:
		return Matrix{b: -h, c: w, f: h}
	default:
		return Matrix{a: w, d: h}
	}
}

// ImportImages 将PNG、JPEG、BMP、单页TIFF或JBIG2逐张建页并插入指定位置，一次操作计入一条撤销记录
// 保留像素及透明度，方向通过对象变换实现，不重新采样；图片压缩沿用AddImage规则
// 同一批次的相同图片复用校验及元数据，不额外保留像素数据
// 失败或取消时不修改文档
// 入参: ctx 取消上下文, sources 按页面顺序排列的图片, at 插入位置, options 建页选项
// 返回: error 错误信息
func (e *Editor) ImportImages(ctx context.Context, sources []ImagePageSource, at int, options ImagePageOptions) error {
	if at < 0 || at > len(e.pages) {
		return fmt.Errorf("page index %d out of range", at)
	}
	if !finite(options.Width) || !finite(options.Height) || !finite(options.Margin) || !finite(options.DPI) || options.Margin < 0 || options.DPI < 0 {
		return fmt.Errorf("invalid image page options")
	}
	if (options.Width != 0 || options.Height != 0) && (options.Width <= 2*options.Margin || options.Height <= 2*options.Margin) {
		return fmt.Errorf("page margins leave no image area")
	}
	if options.DPI == 0 {
		options.DPI = 96
	}
	for _, source := range sources {
		if source.Read == nil {
			return fmt.Errorf("image %q has no reader", source.Name)
		}
	}
	check := func(stage string, completed int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if options.OnProgress != nil {
			if err := options.OnProgress(stage, completed, len(sources)); err != nil {
				return err
			}
		}
		return ctx.Err()
	}
	if err := check("images", 0); err != nil || len(sources) == 0 {
		return err
	}
	return e.Transaction(func(e *Editor) error {
		indexes := make([]int, 0, len(sources))
		verified := make(map[[32]byte]editorImagePageInfo)
		workers := min(len(sources), conversionWorkers(ctx))
		if workers <= 1 {
			for i, source := range sources {
				if err := ctx.Err(); err != nil {
					return err
				}
				data, err := source.Read(ctx)
				if err != nil {
					return fmt.Errorf("image %q: %w", source.Name, err)
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				prepared, err := prepareImagePage(ctx, data, options, verified, nil)
				if err != nil {
					return fmt.Errorf("image %q: %w", source.Name, err)
				}
				page, err := e.commitPreparedImagePage(ctx, prepared, options)
				if err != nil {
					return fmt.Errorf("image %q: %w", source.Name, err)
				}
				indexes = append(indexes, page)
				if err := check("images", i+1); err != nil {
					return err
				}
			}
			if err := e.MovePages(indexes, at); err != nil {
				return err
			}
			return check("commit", len(sources))
		}
		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		var verifiedMu sync.Mutex
		window := workers * 2
		jobs := make(chan *editorImageTask, window)
		inflight := make(chan *editorImageTask, window)
		var workerWG sync.WaitGroup
		for range workers {
			workerWG.Add(1)
			go func() {
				defer workerWG.Done()
				for task := range jobs {
					if err := runCtx.Err(); err != nil {
						task.err = err
						task.data = nil
						close(task.done)
						continue
					}
					prepared, err := prepareImagePage(runCtx, task.data, options, verified, &verifiedMu)
					if err != nil {
						task.err = fmt.Errorf("image %q: %w", task.name, err)
					} else {
						task.prepared = prepared
					}
					task.data = nil
					close(task.done)
				}
			}()
		}
		var producerWG sync.WaitGroup
		producerWG.Add(1)
		go func() {
			defer producerWG.Done()
			defer close(jobs)
			defer close(inflight)
			for _, source := range sources {
				if err := runCtx.Err(); err != nil {
					return
				}
				data, err := source.Read(runCtx)
				task := &editorImageTask{name: source.Name, data: data, done: make(chan struct{})}
				if err != nil {
					task.err = fmt.Errorf("image %q: %w", source.Name, err)
					close(task.done)
					select {
					case <-runCtx.Done():
					case inflight <- task:
					}
					return
				}
				select {
				case <-runCtx.Done():
					return
				case inflight <- task:
				}
				select {
				case <-runCtx.Done():
					task.err = runCtx.Err()
					task.data = nil
					close(task.done)
					return
				case jobs <- task:
				}
			}
		}()
		finish := func() {
			cancel()
			producerWG.Wait()
			workerWG.Wait()
		}
		for i := range sources {
			task, ok := <-inflight
			if !ok {
				finish()
				return runCtx.Err()
			}
			select {
			case <-task.done:
			case <-ctx.Done():
				finish()
				return ctx.Err()
			}
			if task.err != nil {
				finish()
				return task.err
			}
			page, err := e.commitPreparedImagePage(ctx, task.prepared, options)
			task.prepared.data = nil
			if err != nil {
				finish()
				return fmt.Errorf("image %q: %w", task.name, err)
			}
			indexes = append(indexes, page)
			if err := check("images", i+1); err != nil {
				finish()
				return err
			}
		}
		finish()
		if err := e.MovePages(indexes, at); err != nil {
			return err
		}
		return check("commit", len(sources))
	})
}

// commitPreparedImagePage 将已预处理完成的图片注册为资源并按物理尺寸与方向建页
// 入参: ctx 取消上下文, prepared 预处理结果, options 建页选项
// 返回: int 从0开始的页面索引, error 错误信息
func (e *Editor) commitPreparedImagePage(ctx context.Context, prepared editorPreparedImage, options ImagePageOptions) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	metadata := prepared.info.metadata
	w, h := float64(prepared.info.size.X)*25.4/metadata.XDPI, float64(prepared.info.size.Y)*25.4/metadata.YDPI
	if metadata.Orientation >= 5 {
		w, h = h, w
	}
	pw, ph := options.Width, options.Height
	if pw == 0 && ph == 0 {
		pw, ph = w+2*options.Margin, h+2*options.Margin
	} else {
		scale := math.Min((pw-2*options.Margin)/w, (ph-2*options.Margin)/h)
		w, h = w*scale, h*scale
	}
	if !finite(w) || !finite(h) || w <= 0 || h <= 0 {
		return 0, fmt.Errorf("invalid image page dimensions")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	resource, err := e.addImage(ctx, prepared.data, prepared.info.size, prepared.format, nil, prepared.checksum)
	if err != nil {
		return 0, err
	}
	page, err := e.AddPage(pw, ph)
	if err != nil {
		return 0, err
	}
	matrix := imagePageOrientation(metadata.Orientation, w, h)
	_, err = e.AddObject(page, GraphicObject{Type: "ImageObject", ImageObject: ImageObject{
		ResourceID: resource,
		Boundary:   fmt.Sprintf("%s %s %s %s", ofdNumber((pw-w)/2), ofdNumber((ph-h)/2), ofdNumber(w), ofdNumber(h)),
		CTM:        matrix.String(),
	}})
	return page, err
}
