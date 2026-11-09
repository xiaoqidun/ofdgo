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
	"archive/zip"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"weak"

	basefile "github.com/xiaoqidun/ofdgo/internal/base/file"
)

// editorSnapshotArchiveLimit 限制编辑器常驻的快照压缩包字节数
const editorSnapshotArchiveLimit = 32 << 20

// editorSnapshotChunkSize 指定快照压缩数据的分块大小
const editorSnapshotChunkSize = 256 << 10

// ErrSaveTarget 表示编辑器尚未绑定保存目标
var ErrSaveTarget = errors.New("save target is not set")

// ErrSaveConflict 表示保存目标在打开或最近保存后被其他操作修改
var ErrSaveConflict = basefile.ErrChanged

// ErrSaveBusy 表示保存目标已有其他写入事务
var ErrSaveBusy = basefile.ErrBusy

// Output 创建完整文档的输出事务，可用于文件或远程存储
// 实现需支持并发调用Begin，按目标协调并发写入并校验版本冲突
// Begin失败时由实现释放资源；事务持有ctx，提交前应检查取消状态
type Output interface {
	Begin(ctx context.Context) (WriteTransaction, error)
}

// WriteTransaction 暂存文档并提交完整内容，不直接覆盖已提交版本
// Commit成功后释放资源并更新目标版本；失败时不得自动重试提交
// Abort释放未提交资源且可重复调用，不撤销已提交内容
// 远程提交结果不确定时，由实现返回可识别错误并重新确认目标版本
type WriteTransaction interface {
	io.Writer
	Commit() error
	Abort() error
}

// fileOutput 使用本地文件事务保存文档
type fileOutput struct {
	target *basefile.Target
}

// editorSnapshotArchive 复用独立压缩包，超预算时仅在已有快照存活期间共享
type editorSnapshotArchive struct {
	files    []*zip.File
	archive  weak.Pointer[zip.Reader]
	retained *renderCache[int, *zip.Reader]
}

// editorSnapshotBuffer 分块保存压缩数据，写入完成后支持并发随机读取
type editorSnapshotBuffer struct {
	chunks [][]byte
	size   int64
}

// editorProgress 编辑与保存阶段的进度回调，nil表示不回报进度
type editorProgress func(stage string, completed, total int) error

// ofdCountingWriter 记录实际输出字节数
type ofdCountingWriter struct {
	writer io.Writer
	count  int64
}

// ofdXML 使用标准ofd命名空间写出XML
type ofdXML struct {
	encoder    *xml.Encoder
	references *editorReferenceScan
	ctx        context.Context
	attrs      ofdAttrs
	escaped    bytes.Buffer
	spaces     bytes.Buffer
	err        error
}

// ofdAttrs OFD节点属性
type ofdAttrs []xml.Attr

// Snapshot 创建独立编辑快照，完成后可与原编辑器并行使用
// 创建过程需与原实例串行，保留修订、附件清理状态及保存目标，不复制撤销记录和进度回调
// 保存目标与原实例共享，由输出实现协调并发事务及提交版本
// 原包压缩数据独立复制并在快照间复用，超预算数据随最后一个快照释放
// 自定义后端及字体文件系统仍由调用方提供，需支持并行读取
// 入参: ctx 取消上下文
// 返回: *Editor 独立快照, error 准备或取消错误
func (e *Editor) Snapshot(ctx context.Context) (*Editor, error) {
	reader, err := e.readerContext(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	next := &Reader{files: make(map[string][]byte, len(reader.files)), encryption: reader.encryption, documentIndex: reader.documentIndex, selectedVersion: cloneEditorData(reader.selectedVersion), resources: e.runtimeResources, pdfDestinations: reader.pdfDestinations.clone()}
	if reader.encryption != nil {
		next.encryption = &encryptionState{info: cloneEditorData(reader.encryption.info), options: cloneEncryptionOptions(reader.encryption.options)}
	}
	if reader.Zip != nil {
		next.Zip, err = e.copySnapshotArchive(ctx, reader.Zip)
		if err != nil {
			return nil, err
		}
	}
	for name, data := range reader.files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		next.files[name] = bytes.Clone(data)
	}
	if err := next.initRootContext(ctx); err != nil {
		return nil, err
	}
	snapshot, err := next.EditorContext(ctx)
	if err != nil {
		return nil, err
	}
	snapshot.revision, snapshot.serial, snapshot.saveTarget = e.revision, e.serial, e.saveTarget
	snapshot.backends, snapshot.fontDirs, snapshot.fontFS = e.backends, slices.Clone(e.fontDirs), slices.Clone(e.fontFS)
	for _, resource := range e.resources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if resource.font == nil || resource.font.FontFile == "" || !next.fileNames[resource.name] {
			continue
		}
		data, err := next.readFileViewContext(ctx, resource.name)
		if err != nil {
			return nil, err
		}
		snapshot.resources = append(snapshot.resources, editorResource{name: resource.name, data: data, font: cloneEditorData(resource.font)})
	}
	if e.source != nil {
		snapshot.source.retiredAttachments = maps.Clone(e.source.retiredAttachments)
		snapshot.source.removedAttachments = maps.Clone(e.source.removedAttachments)
	}
	return snapshot, nil
}

// copySnapshotArchive 复用条目未变的独立压缩包，取消或失败不发布缓存
// 入参: ctx 取消上下文, source 原压缩包
// 返回: *zip.Reader 独立压缩包, error 复制错误
func (e *Editor) copySnapshotArchive(ctx context.Context, source *zip.Reader) (*zip.Reader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cached := e.snapshotArchive; cached != nil && slices.Equal(cached.files, source.File) {
		if cached.retained != nil {
			if archive, ok := cached.retained.get(0); ok {
				return archive, nil
			}
		}
		if archive := cached.archive.Value(); archive != nil {
			return archive, nil
		}
	}
	var data editorSnapshotBuffer
	var count int64
	archive := zip.NewWriter(convertWriter{ctx: ctx, writer: &data, count: &count})
	for _, file := range source.File {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := archive.Copy(file); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	reader, err := zip.NewReader(&data, data.size)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cached := &editorSnapshotArchive{files: slices.Clone(source.File), archive: weak.Make(reader)}
	limit := int64(editorSnapshotArchiveLimit)
	if e.runtimeResources != nil {
		limit = e.runtimeResources.options.SnapshotBytes
	}
	if int64(len(data.chunks)) <= limit/editorSnapshotChunkSize {
		cached.retained = resourceCache[int, *zip.Reader](e.runtimeResources, editorSnapshotArchiveLimit)
		cached.retained.limit = int(min(limit, int64(int(^uint(0)>>1)-1)))
		cached.retained.put(0, reader, len(data.chunks)*editorSnapshotChunkSize)
	}
	e.snapshotArchive = cached
	return reader, nil
}

// Write 追加快照数据，不扩容或复制已有块
// 入参: data 压缩数据
// 返回: int 写入字节数, error 写入错误
func (b *editorSnapshotBuffer) Write(data []byte) (int, error) {
	size := len(data)
	for len(data) > 0 {
		offset := int(b.size % editorSnapshotChunkSize)
		if offset == 0 {
			b.chunks = append(b.chunks, make([]byte, editorSnapshotChunkSize))
		}
		n := copy(b.chunks[len(b.chunks)-1][offset:], data)
		b.size += int64(n)
		data = data[n:]
	}
	return size, nil
}

// ReadAt 跨块读取快照，不复制或合并底层存储
// 入参: data 目标缓冲, offset 数据偏移
// 返回: int 读取字节数, error 偏移或结束错误
func (b *editorSnapshotBuffer) ReadAt(data []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, fmt.Errorf("negative snapshot offset")
	}
	read := 0
	for len(data) > 0 {
		if offset >= b.size {
			return read, io.EOF
		}
		block := b.chunks[offset/editorSnapshotChunkSize]
		start := int(offset % editorSnapshotChunkSize)
		size := min(int64(len(block)-start), b.size-offset, int64(len(data)))
		n := copy(data, block[start:start+int(size)])
		read += n
		offset += int64(n)
		data = data[n:]
	}
	return read, nil
}

// NewFileOutput 创建本地保存目标并记录当前文件状态，不创建或修改文件
// 同一目标应复用返回值，以便后续保存检测外部修改
// 替换前比较文件标识、大小和修改时间，不提供跨进程排他锁
// 入参: name 文件路径
// 返回: Output 保存目标, error 路径或文件状态错误
func NewFileOutput(name string) (Output, error) {
	target, err := basefile.NewTarget(name)
	if err != nil {
		return nil, err
	}
	return &fileOutput{target: target}, nil
}

// Begin 创建本地文件写入事务
// 入参: ctx 取消上下文
// 返回: WriteTransaction 写入事务, error 文件状态错误
func (o *fileOutput) Begin(ctx context.Context) (WriteTransaction, error) {
	return o.target.Begin(ctx)
}

// SetOutput 绑定保存目标，不读取或修改编辑内容，nil解除绑定
// 调用需与编辑操作串行，目标生命周期由调用方管理
// 入参: output 保存目标
func (e *Editor) SetOutput(output Output) {
	e.saveTarget = output
}

// Output 返回当前保存目标，未绑定时返回nil
// 返回: Output 保存目标
func (e *Editor) Output() Output {
	return e.saveTarget
}

// Save 将当前完整文档写回已绑定目标，不改变编辑状态及撤销记录
// 调用需与编辑操作串行；后台保存应先创建Snapshot，成功后可继续编辑和保存
// 入参: ctx 取消上下文, options 输出配置
// 返回: int64 输出字节数, error 保存错误
func (e *Editor) Save(ctx context.Context, options WriteOptions) (int64, error) {
	return e.saveOutput(ctx, e.saveTarget, options)
}

// SaveAs 将完整文档另存，成功后切换保存目标，不改变撤销记录
// 调用需与编辑操作串行，取消或失败时保持原目标
// 入参: ctx 取消上下文, output 保存目标, options 输出配置
// 返回: int64 输出字节数, error 保存错误
func (e *Editor) SaveAs(ctx context.Context, output Output, options WriteOptions) (int64, error) {
	n, err := e.saveOutput(ctx, output, options)
	if err == nil {
		e.saveTarget = output
	}
	return n, err
}

// saveOutput 使用输出事务提交文档并释放失败事务
// 入参: ctx 取消上下文, target 保存目标, options 输出配置
// 返回: int64 输出字节数, error 保存错误
func (e *Editor) saveOutput(ctx context.Context, target Output, options WriteOptions) (n int64, err error) {
	if target == nil {
		return 0, ErrSaveTarget
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	writer, err := target.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, writer.Abort())
		}
	}()
	n, err = e.WriteToWithOptions(ctx, writer, options)
	if err != nil {
		return n, err
	}
	if err = ctx.Err(); err != nil {
		return n, err
	}
	return n, writer.Commit()
}

// WriteTo 逐个条目写出OFD，不关闭调用方输出流，出错时应丢弃本次输出
// 新增资源仅写入实际引用的部分，静态TrueType轮廓字体按实际文字生成子集
// 原有字体在全包引用可确定时保留字形编号裁剪；编辑资源、复杂字体及无法确定的引用保持原样
// 制作软件统一为xiaoqidun/ofdgo，未修改的文档保留原制作软件及版本
// 重开子集文档后输入未包含的文字，需要通过AddFont注册完整字体并替换原字体引用
// 入参: writer 输出流
// 返回: int64 已写入字节数, error 错误信息
func (e *Editor) WriteTo(writer io.Writer) (int64, error) {
	if e.encryption != nil {
		return e.writeEncrypted(writer)
	}
	return e.writePlaintext(writer)
}

// writePagesTo 按指定顺序将页面另存为独立OFD，不修改当前文档或撤销记录
// 保留描述信息并生成新文档标识，迁移相关资源、目录、注释和签章外观，移除指向未选页面的跳转
// 快照只准备所选页面，关联引用由页面导入流程统一筛选，沿用当前后端及字体来源
// 签名凭据不代表另存文件验签有效；进度与取消复用OnWriteProgress，另含snapshot、ids和commit阶段
// 入参: writer 输出流, indexes 从0开始的页面索引，不可重复或为空
// 返回: int64 已写入字节数, error 错误信息
func (e *Editor) writePagesTo(writer io.Writer, indexes []int) (int64, error) {
	ctx := context.Background()
	if e.output != nil {
		ctx = e.output.ctx
	}
	if len(indexes) == 0 {
		return 0, fmt.Errorf("no pages selected")
	}
	if err := e.validatePageIndexes(indexes); err != nil {
		return 0, err
	}
	progress := editorProgress(e.OnWriteProgress).withContext(ctx)
	if err := progress.report("snapshot", 0, 0); err != nil {
		return 0, err
	}
	snapshot := *e
	snapshot.pages = make([]PageContent, len(indexes))
	if e.source != nil {
		source := *e.source
		source.pages = make(map[string]*editorSourcePage, len(indexes))
		source.annotationPages = nil
		for _, index := range indexes {
			id := e.pages[index].ID
			if page := e.source.pages[id]; page != nil {
				source.pages[id] = page
			}
		}
		snapshot.source = &source
	}
	positions := make([]int, len(indexes))
	for i, index := range indexes {
		snapshot.pages[i] = e.pages[index]
		positions[i] = i
	}
	if err := snapshot.compressPatternPaints(); err != nil {
		return 0, err
	}
	reader, err := snapshot.reader(progress)
	if err != nil {
		return 0, err
	}
	defer reader.Close()
	selected := NewEditor()
	selected.SetRenderBackends(e.backends)
	selected.SetFontDirs(e.fontDirs...)
	selected.SetFontFS(e.fontFS...)
	selected.encryption = e.encryption
	id := selected.Info.DocID
	selected.Info = cloneEditorData(e.Info)
	selected.Info.DocID = id
	selected.OnWriteProgress = e.OnWriteProgress
	if _, err := selected.ImportPagesContext(ctx, reader, positions, 0, PageImportOptions{Outlines: true, OnProgress: e.OnWriteProgress}); err != nil {
		return 0, err
	}
	if e.output != nil {
		return selected.WriteToWithOptions(e.output.ctx, writer, WriteOptions{Compression: e.output.options})
	}
	return selected.WriteTo(writer)
}

// Reader 获取当前文档的独立内存快照，不进行ZIP压缩，后续修改不影响已有快照
// 返回的Reader可用于现有渲染、搜索和导出接口，新增资源仅包含实际引用的部分，二进制数据内部共享只读
// 失败时不返回部分结果，不改变当前编辑状态或撤销记录
// 入参: ctx 取消上下文
// 返回: *Reader 阅读器, error 准备或取消错误
func (e *Editor) Reader(ctx context.Context) (*Reader, error) {
	return e.readerContext(ctx, nil)
}

// Write 写入并累计字节数
// 入参: data 数据
// 返回: int 字节数, error 错误信息
func (w *ofdCountingWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	w.count += int64(n)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return n, err
}

// report 回报进度并原样传递调用方停止原因
// 入参: stage 阶段, completed 已处理项, total 总项数
// 返回: error 调用方停止原因
func (progress editorProgress) report(stage string, completed, total int) error {
	if progress != nil {
		return progress(stage, completed, total)
	}
	return nil
}

// withContext 在进度回调前后检查取消，保留回调的原始错误
// 入参: ctx 取消上下文
// 返回: editorProgress 可取消的进度回调
func (progress editorProgress) withContext(ctx context.Context) editorProgress {
	return func(stage string, completed, total int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := progress.report(stage, completed, total); err != nil {
			return err
		}
		return ctx.Err()
	}
}

// writePlaintext 写出未加密包，供显式明文输出与加密封装共用
// 入参: writer 输出流
// 返回: int64 写入字节数, error 错误信息
func (e *Editor) writePlaintext(writer io.Writer) (int64, error) {
	ctx := context.Background()
	if e.output != nil {
		ctx = e.output.ctx
	}
	if err := e.validate(); err != nil {
		return 0, err
	}
	progress := editorProgress(e.OnWriteProgress)
	fonts, err := e.subsetFonts(progress)
	if err != nil {
		return 0, err
	}
	if e.source != nil {
		return e.writeSource(writer, fonts, progress)
	}
	if err := progress.report("write", 0, 0); err != nil {
		return 0, err
	}
	output := &ofdCountingWriter{writer: writer}
	archive := zip.NewWriter(output)
	if e.output != nil && e.output.options.Mode != CompressionUnchanged {
		parts := make(map[string][]byte)
		err := e.writeParts(ctx, func(name string, data []byte, _ bool) error {
			if subset, ok := fonts[name]; ok {
				data = subset
			}
			parts[name] = data
			return nil
		}, progress)
		if err != nil {
			return 0, err
		}
		removed := make(map[string]bool)
		if err := e.compressResourceParts(parts, nil, removed); err != nil {
			return 0, err
		}
		for _, name := range slices.Sorted(maps.Keys(parts)) {
			if removed[cleanPackagePath(name)] {
				continue
			}
			if err := e.writeOutputEntry(archive, zip.FileHeader{Name: name, Method: zip.Deflate}, parts[name]); err != nil {
				return output.count, err
			}
		}
		err = archive.Close()
		return output.count, err
	}
	err = e.writeParts(ctx, func(name string, data []byte, compressed bool) error {
		if subset, ok := fonts[name]; ok {
			data = subset
		}
		method := uint16(zip.Deflate)
		if compressed {
			method = zip.Store
		}
		return e.writeOutputEntry(archive, zip.FileHeader{Name: name, Method: method}, data)
	}, progress)
	if err == nil {
		err = archive.Close()
	}
	return output.count, err
}

// reader 生成内存快照，保存时复用准备进度，预览时不触发回调
// 入参: progress 准备进度回调
// 返回: *Reader 阅读器, error 错误信息
func (e *Editor) reader(progress editorProgress) (*Reader, error) {
	ctx := context.Background()
	if e.output != nil {
		ctx = e.output.ctx
	}
	return e.readerContext(ctx, progress)
}

// readerContext 准备可取消的内存快照，版本隔离沿用调用方上下文
// 入参: ctx 取消上下文, progress 准备进度回调
// 返回: *Reader 独立快照, error 准备或取消错误
func (e *Editor) readerContext(ctx context.Context, progress editorProgress) (result *Reader, err error) {
	defer func() {
		if result != nil {
			result.pdfDestinations = e.pdfDestinations.clone()
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	progress = progress.withContext(ctx)
	if err := e.validate(); err != nil {
		return nil, err
	}
	if e.source != nil {
		return e.sourceReader(ctx, progress)
	}
	r := &Reader{files: make(map[string][]byte), encryption: e.encryption, resources: e.runtimeResources}
	pages := maps.Clone(e.previewPages)
	if pages == nil && len(e.origins) == 0 {
		pages = make(map[string][]byte)
	}
	if err := e.writePartsWithPages(ctx, func(name string, data []byte, _ bool) error {
		r.files[name] = data
		return nil
	}, progress, true, pages); err != nil {
		return nil, err
	}
	if err := r.initRootContext(ctx); err != nil {
		return nil, err
	}
	if _, err := r.DocContext(ctx); err != nil {
		return nil, err
	}
	for i, page := range r.doc.Pages.Page {
		r.pageHeaderCache[r.ResPath(page.BaseLoc)] = PageContent{Area: e.pages[i].Area, Template: slices.Clone(e.pages[i].Template), PageRes: slices.Clone(e.pages[i].PageRes)}
	}
	e.previewPages = pages
	return r, nil
}

// invalidatePreviewPages 失效受影响页面的预览编码，保留事务和历史快照的独立缓存
// 入参: changes 页面或文档变化范围
func (e *Editor) invalidatePreviewPages(changes EditorChanges) {
	if changes.All {
		e.previewPages = nil
		return
	}
	if len(e.previewPages) == 0 {
		return
	}
	e.previewPages = maps.Clone(e.previewPages)
	for _, index := range changes.Pages {
		delete(e.previewPages, e.pages[index].ID)
	}
}

// resourceReader 为临时图元编译准备资源索引，不序列化页面或作为公共快照
// 返回: *Reader 资源阅读器, error 资源错误
func (e *Editor) resourceReader() (*Reader, error) {
	ctx := context.Background()
	if e.output != nil {
		ctx = e.output.ctx
	}
	if e.source != nil || len(e.origins) != 0 {
		return nil, fmt.Errorf("resource reader requires created objects")
	}
	if err := e.validate(); err != nil {
		return nil, err
	}
	r := &Reader{files: make(map[string][]byte), resources: e.runtimeResources}
	if err := e.writePartsWithPages(ctx, func(name string, data []byte, _ bool) error {
		r.files[name] = data
		return nil
	}, nil, false, nil); err != nil {
		return nil, err
	}
	if err := r.initRoot(); err != nil {
		return nil, err
	}
	if _, err := r.Doc(); err != nil {
		return nil, err
	}
	return r, nil
}

// validate 校验文档保存条件
// 返回: error 错误信息
func (e *Editor) validate() error {
	if len(e.pages) == 0 {
		return fmt.Errorf("document must contain at least one page")
	}
	if e.source == nil || e.Info.DocID != e.source.info.DocID {
		if data, err := hex.DecodeString(e.Info.DocID); err != nil || len(data) != 16 {
			return fmt.Errorf("DocID must contain 32 hexadecimal characters")
		}
	}
	for i, date := range []string{e.Info.CreationDate, e.Info.ModDate} {
		if e.source != nil && date == []string{e.source.info.CreationDate, e.source.info.ModDate}[i] {
			continue
		}
		if date != "" {
			if _, err := time.Parse("2006-01-02", date); err != nil {
				return fmt.Errorf("invalid document date: %w", err)
			}
		}
	}
	return nil
}

// writeParts 生成根节点、文档、资源索引、页面及二进制资源
// 入参: ctx 取消上下文, write 条目写入方法, progress 保存进度回调，预览时为nil
// 返回: error 错误信息
func (e *Editor) writeParts(ctx context.Context, write func(string, []byte, bool) error, progress editorProgress) error {
	return e.writePartsWithPages(ctx, write, progress, true, nil)
}

// writePartsWithPages 统一生成包条目，临时编译可省去页面XML
// 入参: ctx 取消上下文, write 条目写入方法, progress 保存进度回调, includePages 是否写出页面, previews 预览页面编码缓存
// 返回: error 写入错误
func (e *Editor) writePartsWithPages(ctx context.Context, write func(string, []byte, bool) error, progress editorProgress, includePages bool, previews map[string][]byte) error {
	progress = progress.withContext(ctx)
	if len(e.removedPages) != 0 {
		output := write
		write = func(name string, data []byte, binary bool) error {
			if !binary && strings.EqualFold(path.Ext(name), ".xml") {
				var err error
				data, err = pruneCreatedPageReferences(ctx, data, e.removedPages)
				if err != nil {
					return err
				}
			}
			return output(name, data, binary)
		}
	}
	fonts, images, spaces, definitions, err := e.usedResources(ctx)
	if err != nil {
		return err
	}
	writeXML := func(name string, encode func(*ofdXML)) error {
		data, err := encodeOFDXMLContext(ctx, encode)
		if err != nil {
			return err
		}
		if name == "Doc_0/Document.xml" {
			data, err = e.withOutlines(data)
			if err != nil {
				return err
			}
		}
		return write(name, data, false)
	}
	if err := writeXML("OFD.xml", func(x *ofdXML) {
		x.root("OFD", ofdAttrs{{Name: xml.Name{Local: "Version"}, Value: "1.0"}, {Name: xml.Name{Local: "DocType"}, Value: "OFD"}})
		x.start("DocBody", nil)
		x.start("DocInfo", nil)
		for _, field := range [][2]string{
			{"DocID", e.Info.DocID}, {"Title", e.Info.Title}, {"Author", e.Info.Author},
			{"Subject", e.Info.Subject}, {"Abstract", e.Info.Abstract},
			{"CreationDate", e.Info.CreationDate}, {"ModDate", e.Info.ModDate},
			{"DocUsage", e.Info.DocUsage}, {"Cover", e.Info.Cover},
		} {
			if field[1] != "" {
				x.text(field[0], field[1])
			}
		}
		if len(e.Info.Keywords) != 0 {
			x.start("Keywords", nil)
			for _, keyword := range e.Info.Keywords {
				x.text("Keyword", keyword)
			}
			x.end("Keywords")
		}
		for _, field := range [][2]string{{"Creator", e.Info.Creator}, {"CreatorVersion", e.Info.CreatorVersion}} {
			if field[1] != "" {
				x.text(field[0], field[1])
			}
		}
		if e.Info.CustomDatas != nil && len(e.Info.CustomDatas.CustomData) != 0 {
			x.start("CustomDatas", nil)
			for _, item := range e.Info.CustomDatas.CustomData {
				attrs := ofdAttrs{{Name: xml.Name{Local: "Name"}, Value: item.Name}}
				x.start("CustomData", attrs)
				x.token(xml.CharData(item.Value))
				x.end("CustomData")
			}
			x.end("CustomDatas")
		}
		x.end("DocInfo")
		x.text("DocRoot", "Doc_0/Document.xml")
		x.end("DocBody")
		x.end("OFD")
	}); err != nil {
		return err
	}
	if err := writeXML("Doc_0/Document.xml", func(x *ofdXML) {
		x.root("Document", nil)
		x.start("CommonData", nil)
		x.text("MaxUnitID", strconv.Itoa(e.maxID))
		x.pageArea("PageArea", e.pages[0].Area)
		if len(fonts)+len(images)+len(spaces) != 0 {
			x.text("DocumentRes", "DocumentRes.xml")
		}
		for _, name := range slices.Sorted(maps.Keys(definitions)) {
			x.text("DocumentRes", "/"+name)
		}
		x.end("CommonData")
		x.start("Pages", nil)
		for _, page := range e.pages {
			var attrs ofdAttrs
			attrs.add("ID", page.ID)
			attrs.add("BaseLoc", packagePagePath("", page.ID))
			x.start("Page", attrs)
			x.end("Page")
		}
		x.end("Pages")
		x.end("Document")
	}); err != nil {
		return err
	}
	if len(fonts)+len(images)+len(spaces) != 0 {
		if err := writeXML("Doc_0/DocumentRes.xml", func(x *ofdXML) { x.resources(fonts, images, spaces) }); err != nil {
			return err
		}
	}
	if includePages {
		for i, page := range e.pages {
			if err := progress.report("pages", i, len(e.pages)); err != nil {
				return err
			}
			name := packagePagePath(e.packageDirectory(), page.ID)
			if len(e.origins) == 0 {
				data := previews[page.ID]
				if data == nil {
					data, err = encodeOFDXMLContext(ctx, func(x *ofdXML) { x.page(page) })
					if err != nil {
						return err
					}
					if previews != nil {
						previews[page.ID] = data
					}
				}
				if err := write(name, data, false); err != nil {
					return err
				}
			} else {
				data, err := e.sourceNewPageXML(page)
				if err != nil {
					return err
				}
				if err := write(name, data, false); err != nil {
					return err
				}
			}
		}
		if err := progress.report("pages", len(e.pages), len(e.pages)); err != nil {
			return err
		}
	}
	written := make(map[string]bool)
	for _, resources := range [][]editorResource{fonts, images} {
		for _, resource := range resources {
			if resource.name == "" || written[resource.name] {
				continue
			}
			if err := write(resource.name, resource.data, resource.image != nil); err != nil {
				return err
			}
			written[resource.name] = true
		}
	}
	for _, resource := range e.resources {
		if resource.definition() != "" && definitions[resource.name] || resource.colorProfileUsed(spaces) {
			if err := write(resource.name, resource.data, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// editorResourceSelection 保存单次输出中同一编辑快照的资源筛选结果
type editorResourceSelection struct {
	owner         *Editor
	fonts, images []editorResource
	spaces        []ColorSpace
	files         map[string]bool
}

// usedResources 沿图层、对象及资源依赖筛选新增资源和共享文件，不修改资源池
// 入参: ctx 取消上下文
// 返回: []editorResource 新增字体, []editorResource 新增图片, []ColorSpace 新增颜色空间, map[string]bool 独立资源文件集合, error 取消错误
func (e *Editor) usedResources(ctx context.Context) (fonts, images []editorResource, spaces []ColorSpace, sourceFiles map[string]bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, nil, err
	}
	if e.output != nil && e.output.resources != nil && e.output.resources.owner == e {
		cached := e.output.resources
		return slices.Clone(cached.fonts), slices.Clone(cached.images), slices.Clone(cached.spaces), maps.Clone(cached.files), nil
	}
	used := make(map[string]bool)
	promoted := make(map[string]bool)
	files := make(map[string]bool)
	retainAll := false
	if len(e.outlines) != 0 {
		refs := editorResourceRefs{ids: used, files: make(map[string]bool)}
		if _, safe := refs.scanContext(ctx, bytes.NewReader(e.outlines), e.packageName("Document.xml")); !safe {
			retainAll = true
		}
	}
	if e.source != nil {
		collectActionReferences(e.source.document.Actions, used)
		for _, name := range e.annotationFiles() {
			if err := ctx.Err(); err != nil {
				return nil, nil, nil, nil, err
			}
			data, changed := e.source.reader.files[name]
			if !changed {
				continue
			}
			refs := editorResourceRefs{ids: used, files: make(map[string]bool)}
			if _, safe := refs.scanContext(ctx, bytes.NewReader(data), name); !safe {
				retainAll = true
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, nil, err
	}
	if retainAll {
		for _, resource := range e.resources {
			if resource.font != nil {
				used[resource.font.ID] = true
			}
			if resource.image != nil {
				used[resource.image.ID] = true
			}
			if resource.space != nil {
				used[resource.space.ID] = true
			}
			used[resource.definition()] = true
		}
	}
	if e.source != nil {
		maps.Copy(promoted, used)
	}
	for _, page := range e.pages {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, nil, err
		}
		collectActionReferences(page.Actions, used)
		var original map[string]GraphicObject
		var originalLayers map[string]string
		if e.source != nil {
			original = make(map[string]GraphicObject)
			originalLayers = make(map[string]string)
			if source := e.source.pages[page.ID]; source != nil && source.original != nil {
				for _, layer := range source.original.Content.Layer {
					originalLayers[layer.ID] = layer.DrawParam
					for _, object := range layer.Objects {
						original[editorObjectID(object)] = object
					}
				}
			}
		}
		for _, layer := range page.Content.Layer {
			if layer.DrawParam != "" {
				used[layer.DrawParam] = true
				if e.source != nil && originalLayers[layer.ID] != layer.DrawParam {
					promoted[layer.DrawParam] = true
				}
			}
			for _, object := range layer.Objects {
				if err := ctx.Err(); err != nil {
					return nil, nil, nil, nil, err
				}
				before, exists := original[editorObjectID(object)]
				collectObjectReferences(object, used)
				if e.source != nil && (!exists || !reflect.DeepEqual(before, object)) {
					collectObjectReferences(object, promoted)
				}
				if origin := e.objectOrigin(editorObjectID(object)); origin != nil && origin.page != nil && (!exists || before.Type != object.Type || !reflect.DeepEqual(before.CompositeGraphicUnit, object.CompositeGraphicUnit)) {
					for _, name := range origin.page.original.PageRes {
						files[resolveResourcePath(e.source.reader.ResPath(origin.page.ref.BaseLoc), "", name)] = true
					}
				}
			}
		}
	}
	normalizeResourceReferences(used)
	normalizeResourceReferences(promoted)
	definitions := make(map[string]int)
	var pending []string
	for index, resource := range e.resources {
		if id := resource.definition(); id != "" {
			definitions[id] = index
			if used[id] {
				pending = append(pending, id)
			}
		}
	}
	for len(pending) != 0 {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, nil, err
		}
		id := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		index, ok := definitions[id]
		if !ok {
			continue
		}
		delete(definitions, id)
		for _, reference := range e.resources[index].references {
			used[reference] = true
			if e.source != nil {
				promoted[reference] = true
			}
			if _, ok := definitions[reference]; ok {
				pending = append(pending, reference)
			}
		}
	}
	for _, resource := range e.resources {
		if resource.definition() != "" && used[resource.definition()] {
			files[resource.name] = true
		}
		if resource.font != nil && used[resource.font.ID] {
			fonts = append(fonts, resource)
			delete(promoted, resource.font.ID)
		}
		if resource.image != nil && used[resource.image.ID] {
			images = append(images, resource)
			delete(promoted, resource.image.ID)
		}
		if resource.space != nil && used[resource.space.ID] {
			spaces = append(spaces, *resource.space)
			delete(promoted, resource.space.ID)
		}
	}
	for id := range promoted {
		if name, _ := resourceValue(e.source.reader.resourceFiles, id); name != "" {
			files[name] = true
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, nil, err
	}
	if e.output != nil {
		e.output.resources = &editorResourceSelection{owner: e, fonts: slices.Clone(fonts), images: slices.Clone(images), spaces: slices.Clone(spaces), files: maps.Clone(files)}
	}
	return fonts, images, spaces, files, nil
}

// page 按标准顺序写出页面定义、图层、对象及动作
// 入参: page 页面内容
func (x *ofdXML) page(page PageContent) {
	x.root("Page", nil)
	x.pageHeader(page)
	x.start("Content", nil)
	for _, layer := range page.Content.Layer {
		if x.err != nil {
			return
		}
		x.layer(layer)
	}
	x.end("Content")
	x.actions(page.Actions)
	x.end("Page")
}

// pageHeader 写出模板、页资源及页面区域
// 入参: page 页面内容
func (x *ofdXML) pageHeader(page PageContent) {
	for _, template := range page.Template {
		attrs := ofdAttrs{{Name: xml.Name{Local: "TemplateID"}, Value: template.TemplateID}}
		attrs.add("ZOrder", template.ZOrder)
		x.start("Template", attrs)
		x.end("Template")
	}
	for _, resource := range page.PageRes {
		x.text("PageRes", resource)
	}
	x.pageArea("Area", page.Area)
}

// pageArea 写出物理、显示、版心及出血区域，省略未定义的可选区域
// 入参: name 区域容器名称, area 页面区域
func (x *ofdXML) pageArea(name string, area PageArea) {
	x.start(name, nil)
	x.text("PhysicalBox", area.PhysicalBox)
	for _, field := range [][2]string{{"ApplicationBox", area.ApplicationBox}, {"ContentBox", area.ContentBox}, {"BleedBox", area.BleedBox}} {
		if field[1] != "" {
			x.text(field[0], field[1])
		}
	}
	x.end(name)
}

// layer 写出新建图层及其对象
// 入参: layer 图层
func (x *ofdXML) layer(layer Layer) {
	var attrs ofdAttrs
	attrs.add("ID", layer.ID)
	attrs.add("Type", layer.Type)
	attrs.add("DrawParam", layer.DrawParam)
	x.start("Layer", attrs)
	for _, object := range layer.Objects {
		if x.err != nil {
			return
		}
		x.object(object, false)
	}
	x.end("Layer")
}

// resources 写出文档资源索引
// 入参: fonts 字体资源, images 图片资源, spaces 颜色空间
func (x *ofdXML) resources(fonts, images []editorResource, spaces []ColorSpace) {
	x.root("Res", ofdAttrs{{Name: xml.Name{Local: "BaseLoc"}, Value: "Res"}})
	if len(spaces) != 0 {
		x.start("ColorSpaces", nil)
		for _, space := range spaces {
			if x.err != nil {
				return
			}
			attrs := ofdAttrs{{Name: xml.Name{Local: "ID"}, Value: space.ID}, {Name: xml.Name{Local: "Type"}, Value: space.Type}, {Name: xml.Name{Local: "BitsPerComponent"}, Value: strconv.Itoa(space.BitsPerComponent)}}
			attrs.add("Profile", space.Profile)
			x.start("ColorSpace", attrs)
			if len(space.Palette) != 0 {
				x.start("Palette", nil)
				for _, entry := range space.Palette {
					x.text("CV", entry)
				}
				x.end("Palette")
			}
			x.end("ColorSpace")
		}
		x.end("ColorSpaces")
	}
	if len(fonts) != 0 {
		x.start("Fonts", nil)
		for _, resource := range fonts {
			if x.err != nil {
				return
			}
			font := resource.font
			var attrs ofdAttrs
			attrs.add("ID", font.ID)
			attrs.add("FontName", font.FontName)
			attrs.add("FamilyName", font.FamilyName)
			attrs.add("Charset", font.Charset)
			if font.Bold {
				attrs.add("Bold", "true")
			}
			if font.Italic {
				attrs.add("Italic", "true")
			}
			if font.FixedWidth {
				attrs.add("FixedWidth", "true")
			}
			x.start("Font", attrs)
			if font.FontFile != "" {
				x.text("FontFile", font.FontFile)
			}
			x.end("Font")
		}
		x.end("Fonts")
	}
	if len(images) != 0 {
		x.start("MultiMedias", nil)
		for _, resource := range images {
			if x.err != nil {
				return
			}
			image := resource.image
			var attrs ofdAttrs
			attrs.add("ID", image.ID)
			attrs.add("Type", image.Type)
			attrs.add("Format", image.Format)
			x.start("MultiMedia", attrs)
			x.text("MediaFile", image.MediaFile)
			x.end("MultiMedia")
		}
		x.end("MultiMedias")
	}
	x.end("Res")
}

// newOFDXML 创建XML编码器
// 入参: writer 输出流
// 返回: *ofdXML 编码器
func newOFDXML(writer io.Writer) *ofdXML {
	_, err := io.WriteString(writer, xml.Header)
	return &ofdXML{encoder: xml.NewEncoder(writer), err: err}
}

// token 写入XML标记并保留首个错误
// 入参: token XML标记
func (x *ofdXML) token(token xml.Token) {
	if x.checkContext() {
		x.err = x.encoder.EncodeToken(token)
		x.referenceToken(token)
	}
}

// checkContext 保留首个编码或取消错误
// 返回: bool 是否可以继续编码
func (x *ofdXML) checkContext() bool {
	if x.err == nil && x.ctx != nil {
		x.err = x.ctx.Err()
	}
	return x.err == nil
}

// root 写入声明命名空间的根节点
// 入参: name 节点名, attrs 属性
func (x *ofdXML) root(name string, attrs ofdAttrs) {
	attrs.add("xmlns:ofd", ofdNamespace2016)
	x.start(name, attrs)
}

// start 写入节点起始标记
// 入参: name 节点名, attrs 属性
func (x *ofdXML) start(name string, attrs ofdAttrs) {
	x.token(xml.StartElement{Name: xml.Name{Local: "ofd:" + name}, Attr: attrs})
}

// end 写入节点结束标记
// 入参: name 节点名
func (x *ofdXML) end(name string) {
	x.token(xml.EndElement{Name: xml.Name{Local: "ofd:" + name}})
}

// text 写入文本节点
// 入参: name 节点名, value 文本
func (x *ofdXML) text(name, value string) {
	x.start(name, nil)
	x.token(xml.CharData(value))
	x.end(name)
}

// finish 完成XML输出
// 返回: error 错误信息
func (x *ofdXML) finish() error {
	if !x.checkContext() {
		return x.err
	}
	return x.encoder.Close()
}

// add 添加非空属性
// 入参: name 属性名, value 属性值
func (a *ofdAttrs) add(name, value string) {
	if value != "" {
		*a = append(*a, xml.Attr{Name: xml.Name{Local: name}, Value: value})
	}
}

// number 添加非零数值属性
// 入参: name 属性名, value 数值
func (a *ofdAttrs) number(name string, value float64) {
	if value != 0 {
		a.add(name, ofdNumber(value))
	}
}

// flag 添加显式布尔属性
// 入参: name 属性名, value 布尔值
func (a *ofdAttrs) flag(name string, value *bool) {
	if value != nil {
		a.add(name, strconv.FormatBool(*value))
	}
}

// alpha 添加显式透明度
// 入参: value 透明度
func (a *ofdAttrs) alpha(value *int) {
	if value != nil {
		a.add("Alpha", strconv.Itoa(*value))
	}
}

// encodeOFDXML 编码文档节点，用于保存和独立副本
// 入参: encode 节点写入方法
// 返回: []byte XML数据, error 错误信息
func encodeOFDXML(encode func(*ofdXML)) ([]byte, error) {
	return encodeOFDXMLContext(nil, encode)
}

// encodeXML 按本次保存上下文编码，不改变普通快照的输出
// 入参: encode 节点写入方法
// 返回: []byte XML数据, error 编码或取消错误
func (e *Editor) encodeXML(encode func(*ofdXML)) ([]byte, error) {
	var ctx context.Context
	if e.output != nil {
		ctx = e.output.ctx
	}
	return encodeOFDXMLContext(ctx, encode)
}

// encodeOFDXMLContext 编码文档节点，失败时不返回部分数据
// 入参: ctx 取消上下文，可为nil, encode 节点写入方法
// 返回: []byte XML数据, error 编码或取消错误
func encodeOFDXMLContext(ctx context.Context, encode func(*ofdXML)) ([]byte, error) {
	var data bytes.Buffer
	x := newOFDXML(&data)
	x.ctx = ctx
	if !x.checkContext() {
		return nil, x.err
	}
	encode(x)
	if err := x.finish(); err != nil {
		return nil, err
	}
	return data.Bytes(), nil
}

// object 按OFD规定顺序写出基本对象
// 入参: object 图形对象, root 是否为根节点
func (x *ofdXML) object(object GraphicObject, root bool) {
	if !x.checkContext() {
		return
	}
	attrs := x.attrs[:0]
	var fill, stroke *FillColor
	var actions []Action
	if root {
		attrs.add("xmlns:ofd", ofdNamespace2016)
	}
	switch object.Type {
	case "TextObject", "Text":
		obj := object.TextObject
		metadata, err := obj.textMetadata()
		if err != nil {
			x.err = err
			return
		}
		if metadata != "" {
			attrs.add("xmlns:ofdgo", textNamespace)
			attrs.add("ofdgo:Text", metadata)
		}
		attrs.add("ID", obj.ID)
		attrs.add("DrawParam", obj.DrawParam)
		attrs.add("Boundary", obj.Boundary)
		attrs.add("CTM", obj.CTM)
		attrs.add("Font", obj.Font)
		attrs.number("Size", obj.Size)
		attrs.number("HScale", obj.HScale)
		attrs.number("VScale", obj.VScale)
		attrs.add("Decoration", obj.Decoration)
		attrs.number("Weight", float64(obj.Weight))
		attrs.number("ReadDirection", float64(obj.ReadDirection))
		attrs.number("CharDirection", float64(obj.CharDirection))
		attrs.number("LineWidth", obj.LineWidth)
		if obj.LineWidthSet && obj.LineWidth == 0 {
			attrs.add("LineWidth", "0")
		}
		attrs.number("MiterLimit", obj.MiterLimit)
		if obj.MiterLimitSet && obj.MiterLimit == 0 {
			attrs.add("MiterLimit", "0")
		}
		attrs.add("Join", obj.Join)
		attrs.add("Cap", obj.Cap)
		if obj.dashPatternSet {
			attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "DashPattern"}, Value: obj.DashPattern})
		} else {
			attrs.add("DashPattern", obj.DashPattern)
		}
		if obj.DashOffset != nil {
			attrs.add("DashOffset", ofdNumber(*obj.DashOffset))
		}
		if obj.Italic {
			attrs.add("Italic", "true")
		}
		attrs.flag("Visible", obj.Visible)
		attrs.flag("Stroke", obj.Stroke)
		attrs.flag("Fill", obj.Fill)
		attrs.alpha(obj.Alpha)
		fill, stroke = obj.FillColor, (*FillColor)(obj.StrokeColor)
		actions = obj.Actions
	case "PathObject", "Path":
		obj := object.PathObject
		attrs.add("ID", obj.ID)
		attrs.add("DrawParam", obj.DrawParam)
		attrs.add("Boundary", obj.Boundary)
		attrs.add("CTM", obj.CTM)
		attrs.number("LineWidth", obj.LineWidth)
		if obj.LineWidthSet && obj.LineWidth == 0 {
			attrs.add("LineWidth", "0")
		}
		attrs.number("MiterLimit", obj.MiterLimit)
		if obj.MiterLimitSet && obj.MiterLimit == 0 {
			attrs.add("MiterLimit", "0")
		}
		attrs.add("Join", obj.Join)
		attrs.add("Cap", obj.Cap)
		attrs.add("Rule", obj.Rule)
		if obj.dashPatternSet {
			attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "DashPattern"}, Value: obj.DashPattern})
		} else {
			attrs.add("DashPattern", obj.DashPattern)
		}
		if obj.DashOffset != nil {
			attrs.add("DashOffset", ofdNumber(*obj.DashOffset))
		}
		attrs.flag("Visible", obj.Visible)
		attrs.flag("Stroke", obj.Stroke)
		attrs.flag("Fill", obj.Fill)
		attrs.alpha(obj.Alpha)
		fill, stroke = obj.FillColor, (*FillColor)(obj.StrokeColor)
		actions = obj.Actions
	case "ImageObject":
		obj := object.ImageObject
		attrs.add("ID", obj.ID)
		attrs.add("Boundary", obj.Boundary)
		attrs.add("CTM", obj.CTM)
		attrs.add("ResourceID", obj.ResourceID)
		attrs.add("ImageMask", obj.ImageMask)
		attrs.add("Substitution", obj.Substitution)
		attrs.flag("Visible", obj.Visible)
		attrs.alpha(obj.Alpha)
		actions = obj.Actions
	case "CompositeObject", "CompositeGraphicUnit":
		obj := object.CompositeGraphicUnit
		attrs.add("ID", obj.ID)
		attrs.add("Boundary", obj.Boundary)
		attrs.add("CTM", obj.CTM)
		attrs.add("ResourceID", obj.ResourceID)
		attrs.add("DrawParam", obj.DrawParam)
		attrs.flag("Visible", obj.Visible)
		attrs.alpha(obj.Alpha)
		actions = obj.Actions
	}
	x.attrs = attrs
	x.start(object.Type, attrs)
	x.actions(actions)
	if object.Type == "ImageObject" {
		ctm := object.ImageObject.CTM
		if ctm == "" && object.ImageObject.Clips.usesObjectMatrix() {
			box, _ := ParseBox(object.ImageObject.Boundary)
			ctm = (Matrix{a: box.W, d: box.H}).String()
		}
		x.clips(object.ImageObject.Clips, ctm)
		x.border(object.ImageObject.Border)
	} else if object.Type == "CompositeObject" || object.Type == "CompositeGraphicUnit" {
		x.clips(object.CompositeGraphicUnit.Clips, object.CompositeGraphicUnit.CTM)
	} else if object.Type == "PathObject" || object.Type == "Path" {
		x.clips(object.PathObject.Clips, object.PathObject.CTM)
		x.color("StrokeColor", stroke)
		x.color("FillColor", fill)
		x.text("AbbreviatedData", object.PathObject.AbbreviatedData)
	} else if object.Type == "TextObject" || object.Type == "Text" {
		x.clips(object.TextObject.Clips, object.TextObject.CTM)
		x.color("FillColor", fill)
		x.color("StrokeColor", stroke)
		offset, index := 0, 0
		for _, code := range object.TextObject.TextCode {
			if x.err != nil {
				return
			}
			end := offset + len(textCodeRunes(code.Value))
			for index < len(object.TextObject.CGTransform) && object.TextObject.CGTransform[index].CodePosition < end {
				if x.err != nil {
					return
				}
				transform := object.TextObject.CGTransform[index]
				attrs := x.attrs[:0]
				attrs.add("CodePosition", strconv.Itoa(transform.CodePosition-offset))
				attrs.add("CodeCount", strconv.Itoa(transform.CodeCount))
				attrs.add("GlyphCount", strconv.Itoa(transform.GlyphCount))
				x.attrs = attrs
				x.start("CGTransform", attrs)
				x.text("Glyphs", transform.Glyphs)
				x.end("CGTransform")
				index++
			}
			attrs := x.attrs[:0]
			attrs.add("X", code.X)
			attrs.add("Y", code.Y)
			attrs.add("DeltaX", code.DeltaX)
			attrs.add("DeltaY", code.DeltaY)
			x.attrs = attrs
			x.textCode(code.Value, attrs)
			offset = end
		}
	}
	x.end(object.Type)
}

// clips 写出标准裁剪，将旧式随动裁剪折算为区域变换
// 入参: clips 裁剪集合，nil不输出节点, ctm 对象变换矩阵
func (x *ofdXML) clips(clips *Clips, ctm string) {
	if clips == nil || !x.checkContext() {
		return
	}
	matrix := IdentityMatrix
	if clips.usesObjectMatrix() {
		matrix = NewMatrix(ctm)
	}
	x.start("Clips", nil)
	for _, clip := range clips.Clip {
		if x.err != nil {
			return
		}
		x.start("Clip", nil)
		for _, area := range clip.Area {
			if x.err != nil {
				return
			}
			var attrs ofdAttrs
			if clips.usesObjectMatrix() {
				area.CTM = matrix.Multiply(NewMatrix(area.CTM)).String()
			}
			attrs.add("CTM", area.CTM)
			attrs.add("DrawParam", area.DrawParam)
			for _, path := range area.Path {
				if x.err != nil {
					return
				}
				x.start("Area", attrs)
				x.object(GraphicObject{Type: "Path", PathObject: path}, false)
				x.end("Area")
			}
			for _, text := range area.Text {
				if x.err != nil {
					return
				}
				x.start("Area", attrs)
				x.object(GraphicObject{Type: "Text", TextObject: text}, false)
				x.end("Area")
			}
		}
		x.end("Clip")
	}
	x.end("Clips")
}

// textCode 使用XML字符引用转义空格，避免依赖阅读器保留原始空白
// 入参: value OFD文字内容, attrs 文字定位属性
func (x *ofdXML) textCode(value string, attrs ofdAttrs) {
	if !x.checkContext() {
		return
	}
	value = textCodeXMLValue(value)
	x.escaped.Reset()
	if x.err = xml.EscapeText(&x.escaped, []byte(value)); x.err != nil {
		return
	}
	data := x.escaped.Bytes()
	if next := bytes.IndexByte(data, ' '); next >= 0 {
		x.spaces.Reset()
		for replaced := 0; next >= 0; replaced++ {
			if replaced&255 == 0 && !x.checkContext() {
				return
			}
			x.spaces.Write(data[:next])
			x.spaces.WriteString("&#x20;")
			data = data[next+1:]
			next = bytes.IndexByte(data, ' ')
		}
		x.spaces.Write(data)
		data = x.spaces.Bytes()
	}
	content := struct {
		Text []byte `xml:",innerxml"`
	}{Text: data}
	x.err = x.encoder.EncodeElement(content, xml.StartElement{Name: xml.Name{Local: "ofd:TextCode"}, Attr: attrs})
	x.referenceToken(xml.StartElement{Name: xml.Name{Local: "ofd:TextCode"}, Attr: attrs})
	x.referenceToken(xml.CharData(value))
	x.referenceToken(xml.EndElement{Name: xml.Name{Local: "ofd:TextCode"}})
}

// color 写出纯色、渐变和图案，保留颜色空间与透明度
// 入参: name 节点名, color 颜色
func (x *ofdXML) color(name string, color *FillColor) {
	if color == nil || !x.checkContext() {
		return
	}
	var attrs ofdAttrs
	attrs.add("Value", color.Value)
	attrs.add("ColorSpace", color.ColorSpace)
	if color.Index != nil {
		attrs.add("Index", strconv.Itoa(*color.Index))
	}
	attrs.alpha(color.Alpha)
	x.start(name, attrs)
	if color.AxialShd != nil {
		shading := color.AxialShd
		x.shading("AxialShd", shadingAttrs(shading.MapType, shading.MapUnit, shading.Extend, shading.StartPoint, shading.EndPoint), shading.Segment)
	}
	if color.RadialShd != nil {
		shading := color.RadialShd
		attrs := shadingAttrs(shading.MapType, shading.MapUnit, shading.Extend, shading.StartPoint, shading.EndPoint)
		attrs.add("StartRadius", ofdNumber(shading.StartRadius))
		attrs.add("EndRadius", ofdNumber(shading.EndRadius))
		attrs.number("Eccentricity", shading.Eccentricity)
		attrs.number("Angle", shading.Angle)
		x.shading("RadialShd", attrs, shading.Segment)
	}
	if color.Pattern != nil {
		x.pattern(color.Pattern)
	}
	if color.GouraudShd != nil {
		node := color.GouraudShd
		x.meshShading("GouraudShd", node.Extend, 0, node.Point, node.BackColor)
	}
	if color.LaGouraudShd != nil {
		node := color.LaGouraudShd
		x.meshShading("LaGouraudShd", node.Extend, node.VerticesPerRow, node.Point, node.BackColor)
	}
	x.end(name)
}

// meshShading 写出网格控制点，仅非格构渐变保存共享边标志
// 入参: name 元素名称, extend 延伸标志, columns 格构列数, points 控制点, back 背景颜色
func (x *ofdXML) meshShading(name string, extend, columns int, points []ShdPoint, back *ShdColor) {
	var attrs ofdAttrs
	if extend != 0 {
		attrs.add("Extend", strconv.Itoa(extend))
	}
	if columns != 0 {
		attrs.add("VerticesPerRow", strconv.Itoa(columns))
	}
	x.start(name, attrs)
	for _, point := range points {
		if x.err != nil {
			return
		}
		var attrs ofdAttrs
		attrs.add("X", ofdNumber(point.X))
		attrs.add("Y", ofdNumber(point.Y))
		if columns == 0 && point.EdgeFlag != 0 {
			attrs.add("EdgeFlag", strconv.Itoa(point.EdgeFlag))
		}
		x.start("Point", attrs)
		x.element("Color", point.Color)
		x.end("Point")
	}
	if back != nil {
		x.element("BackColor", back)
	}
	x.end(name)
}

// shadingAttrs 编码轴向和径向渐变的共同属性
// 入参: mapType 映射方式, mapUnit 映射周期, extend 延伸方式, start 渐变起点, end 渐变终点
// 返回: ofdAttrs 属性集合
func shadingAttrs(mapType string, mapUnit float64, extend, start, end string) ofdAttrs {
	var attrs ofdAttrs
	attrs.add("MapType", mapType)
	attrs.number("MapUnit", mapUnit)
	attrs.add("Extend", extend)
	attrs.add("StartPoint", start)
	attrs.add("EndPoint", end)
	return attrs
}

// shading 写出渐变分段，保留未显式设置的位置
// 入参: name 渐变类型, attrs 属性, segments 色标
func (x *ofdXML) shading(name string, attrs ofdAttrs, segments []ShdSegment) {
	x.start(name, attrs)
	for _, segment := range segments {
		if x.err != nil {
			return
		}
		var attrs ofdAttrs
		if !segment.positionMissing {
			attrs.add("Position", ofdNumber(segment.Position))
		}
		x.start("Segment", attrs)
		x.element("Color", segment.Color)
		x.end("Segment")
	}
	x.end(name)
}

// element 写出具有标准XML标签的子树，显式声明OFD命名空间
// 入参: name 元素名称, value 元素内容
func (x *ofdXML) element(name string, value any) {
	if x.checkContext() {
		x.err = x.encoder.EncodeElement(value, xml.StartElement{Name: xml.Name{Space: ofdNamespace2016, Local: name}})
		x.referenceElement(name, value)
	}
}

// border 写出图片边框及其绘制颜色
// 入参: border 边框，nil不输出节点
func (x *ofdXML) border(border *ImageBorder) {
	if border == nil || !x.checkContext() {
		return
	}
	var attrs ofdAttrs
	if border.LineWidth != nil {
		attrs.add("LineWidth", ofdNumber(*border.LineWidth))
	}
	attrs.number("HorizonalCornerRadius", border.HorizonalCornerRadius)
	attrs.number("VerticalCornerRadius", border.VerticalCornerRadius)
	attrs.number("DashOffset", border.DashOffset)
	attrs.add("DashPattern", border.DashPattern)
	x.start("Border", attrs)
	x.color("BorderColor", (*FillColor)(border.BorderColor))
	x.end("Border")
}

// pattern 写出图案单元，保留对象绘制顺序
// 入参: pattern 图案
func (x *ofdXML) pattern(pattern *Pattern) {
	if !x.checkContext() {
		return
	}
	var attrs ofdAttrs
	attrs.number("Width", pattern.Width)
	attrs.number("Height", pattern.Height)
	attrs.number("XStep", pattern.XStep)
	attrs.number("YStep", pattern.YStep)
	attrs.add("ReflectMethod", pattern.ReflectMethod)
	attrs.add("RelativeTo", pattern.RelativeTo)
	attrs.add("CTM", pattern.CTM)
	x.start("Pattern", attrs)
	var content ofdAttrs
	content.add("Thumbnail", pattern.CellContent.Thumbnail)
	x.start("CellContent", content)
	for object := range pattern.CellContent.objects() {
		if x.err != nil {
			return
		}
		x.object(object, false)
	}
	x.end("CellContent")
	x.end("Pattern")
}

// actions 写出对象动作及精确点击区域，不检查链接可达性
// 入参: actions 动作列表
func (x *ofdXML) actions(actions []Action) {
	if len(actions) == 0 || !x.checkContext() {
		return
	}
	x.start("Actions", nil)
	for _, action := range actions {
		if x.err != nil {
			return
		}
		var attrs ofdAttrs
		attrs.add("Event", action.Event)
		x.start("Action", attrs)
		if action.Region != nil {
			x.start("Region", nil)
			for _, area := range action.Region.Area {
				if x.err != nil {
					return
				}
				x.start("Area", ofdAttrs{{Name: xml.Name{Local: "Start"}, Value: area.Start}})
				for _, command := range area.Command {
					if x.err != nil {
						return
					}
					var attrs ofdAttrs
					if command.Type == "Arc" && command.EllipseSize == "" {
						attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "EllipseSize"}})
					}
					for _, pair := range [][2]string{{"Point1", command.Point1}, {"Point2", command.Point2}, {"Point3", command.Point3}, {"EllipseSize", command.EllipseSize}, {"RotationAngle", command.RotationAngle}, {"LargeArc", command.LargeArc}, {"SweepDirection", command.SweepDirection}, {"EndPoint", command.EndPoint}} {
						attrs.add(pair[0], pair[1])
					}
					x.start(command.Type, attrs)
					x.end(command.Type)
				}
				x.end("Area")
			}
			x.end("Region")
		}
		if action.Goto != nil {
			x.start("Goto", nil)
			if dest := action.Goto.Dest; dest != nil {
				x.element("Dest", dest)
			}
			if action.Goto.Bookmark != nil {
				x.element("Bookmark", action.Goto.Bookmark)
			}
			x.end("Goto")
		}
		if action.URI != nil {
			x.element("URI", action.URI)
		}
		if action.GotoA != nil {
			x.element("GotoA", action.GotoA)
		}
		if action.Sound != nil {
			x.element("Sound", action.Sound)
		}
		if action.Movie != nil {
			x.element("Movie", action.Movie)
		}
		x.end("Action")
	}
	x.end("Actions")
}
