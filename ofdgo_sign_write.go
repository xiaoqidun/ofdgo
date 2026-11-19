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
	"crypto"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ErrSignatureDependency 表示移除操作会改变保留签名或其他版本使用的入口和保护文件
var ErrSignatureDependency = errors.New("signature dependency conflict")

// SignatureWriteMode 签署方式
type SignatureWriteMode uint8

// 签署输出模式
const (
	// SignatureAppend 追加签署，要求原活动签名有效且未保护签名列表
	SignatureAppend SignatureWriteMode = iota
	// SignatureReplace 重新签署，替换全部活动签名，保留原签名数据文件
	SignatureReplace
)

// SignatureWriteOptions 最终包签署选项，不修改Editor或调用方的原件
// SM2数字签名使用GB/T35275-2017消息结构；RSA/ECDSA使用裸签名，验签需WithSignatureCert
// Seal仅接受已有合法SES v4电子印章，使用SM2/SM3，不声明GM/T0031-2025兼容
// TrustRoots必须显式指定；证书吊销状态和外部业务授权由调用方在签署前检查
// Time为签署方声明的时间，不是可信时间戳，零值取当前时间；Lock保护签名列表，禁止后续追加签署
// ExistingVerifyOptions用于追加签署前和写出后的验证，可提供已有裸签名的验证证书
// ReaderOptions用于打开加密包；Encryption为空时继承阅读器的加密策略
// Compression在计算签名前优化图像，不改写已有签名保护的资源
// PrepareAllDocuments在多文档包首次签章且OFD.xml未受保护时，为包内其他尚无Signatures声明的文档预先声明空签名列表
type SignatureWriteOptions struct {
	Signer                crypto.Signer
	Certificate           []byte
	TrustRoots            [][]byte
	Intermediates         [][]byte
	Seal                  []byte
	Stamps                []SignatureStamp
	Mode                  SignatureWriteMode
	Time                  time.Time
	Provider              SignatureProvider
	Lock                  bool
	PrepareAllDocuments   bool
	ExistingVerifyOptions []SignatureVerifyOption
	ReaderOptions         []ReaderOption
	Encryption            *EncryptionOptions
	Compression           CompressionOptions
	progress              editorProgress
}

// PrepareDocumentSignatures 为包内尚无Signatures声明的各文档预先声明签名入口，便于多文档依次签署，支持撤销重做
// 返回: error 依赖冲突、读取或结构错误
func (e *Editor) PrepareDocumentSignatures() error {
	return e.PrepareDocumentSignaturesContext(context.Background())
}

// PrepareDocumentSignaturesContext 为包内各文档预先声明签名入口并响应取消，失败时不改变文档和历史
// 入参: ctx 取消上下文
// 返回: error 依赖冲突、读取、结构或取消错误
func (e *Editor) PrepareDocumentSignaturesContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	reader, err := e.readerContext(ctx, nil)
	if err != nil {
		return err
	}
	if reader.OFD == nil || len(reader.OFD.DocBody) == 0 {
		return fmt.Errorf("no docbody found")
	}
	pending := false
	for _, body := range reader.OFD.DocBody {
		if body.Signatures == "" {
			pending = true
			break
		}
	}
	if !pending {
		return nil
	}
	protected, _, err := reader.signatureRemovalProtected(ctx, "", nil, false)
	if err != nil {
		return err
	}
	if protected["OFD.xml"] {
		return fmt.Errorf("%w: protected file OFD.xml", ErrSignatureDependency)
	}
	ofdData, err := reader.readFileViewContext(ctx, "OFD.xml")
	if err != nil {
		return err
	}
	parts := make(map[string][]byte)
	updatedOFD, err := prepareDocumentSignatureEntries(ctx, reader, ofdData, parts, nil)
	if err != nil {
		return err
	}
	if updatedOFD == nil {
		return nil
	}
	parts["OFD.xml"] = updatedOFD
	for file, data := range reader.files {
		if _, changed := parts[file]; !changed {
			parts[file] = data
		}
	}
	reader.files = parts
	return e.commitResourceSnapshot(ctx, reader)
}

// RemoveSignature 移除当前文档版本的指定签名及外观，支持撤销重做
// 仅清理无其他引用的签名文件，依赖冲突时不修改文档
// 入参: id 签名标识
// 返回: error 标识、依赖或读取错误
func (e *Editor) RemoveSignature(id string) error {
	return e.RemoveSignatureContext(context.Background(), id)
}

// RemoveSignatureContext 移除指定签名，取消或失败时不改变文档和历史
// 入参: ctx 取消上下文, id 签名标识
// 返回: error 标识、依赖、读取或取消错误
func (e *Editor) RemoveSignatureContext(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("empty signature ID")
	}
	return e.removeSignatures(ctx, id)
}

// ClearSignatures 清空当前文档版本的签名及外观，保留其他文档和版本的引用
// 无签名时不产生历史记录，依赖冲突时不修改文档
// 返回: error 依赖或读取错误
func (e *Editor) ClearSignatures() error {
	return e.ClearSignaturesContext(context.Background())
}

// ClearSignaturesContext 清空当前签名，取消或失败时不改变文档和历史
// 入参: ctx 取消上下文
// 返回: error 依赖、读取或取消错误
func (e *Editor) ClearSignaturesContext(ctx context.Context) error {
	return e.removeSignatures(ctx, "")
}

// SignTo 从阅读器包内容创建独立快照，完成签署和回验后写出
// 含版本列表的文档需先通过ExtractVersion提取后签署
// 不采用可变的Doc/OFD模型；调用方应先将Editor写出为最终包
// 准备失败不写入writer；writer自身失败可能已写入部分输出，不保证目标原子替换
// 入参: writer 独立的输出目标，不得覆盖源文件, options 签署选项
// 返回: int64 写出字节数, error 错误信息
func (r *Reader) SignTo(writer io.Writer, options SignatureWriteOptions) (int64, error) {
	return r.SignToContext(context.Background(), writer, options)
}

// SignToContext 签署并回验独立包快照后写出，支持准备和输出阶段取消
// 入参: ctx 取消上下文, writer 独立输出目标, options 签署选项
// 返回: int64 写出字节数, error 签署、输出或取消错误
func (r *Reader) SignToContext(ctx context.Context, writer io.Writer, options SignatureWriteOptions) (int64, error) {
	if r == nil {
		return 0, fmt.Errorf("nil signature reader")
	}
	if writer == nil {
		return 0, fmt.Errorf("nil signature writer")
	}
	if err := sameOutputFile(r.sourceFile, writer); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	options.progress = options.progress.withContext(ctx)
	return signatureWriteOutput(ctx, r, writer, options)
}

// SignToOutput 通过输出事务签署并原子提交文档，取消或失败时自动中止事务
// 入参: ctx 取消上下文, output 输出目标, options 签署选项
// 返回: int64 写出字节数, error 签署、输出事务或取消错误
func (r *Reader) SignToOutput(ctx context.Context, output Output, options SignatureWriteOptions) (n int64, err error) {
	if r == nil {
		return 0, fmt.Errorf("nil signature reader")
	}
	if output == nil {
		return 0, ErrSaveTarget
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	tx, err := output.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Abort())
		}
	}()
	n, err = r.SignToContext(ctx, tx, options)
	if err != nil {
		return n, err
	}
	if err = ctx.Err(); err != nil {
		return n, err
	}
	return n, tx.Commit()
}

// SignPackage 签署最终OFD包，失败时返回nil且不修改输入
// 含版本列表的文档需先通过ExtractVersion提取后签署
// 入参: data 最终包字节, options 签署选项
// 返回: []byte 签署后的完整包, error 错误信息
func SignPackage(data []byte, options SignatureWriteOptions) ([]byte, error) {
	return SignPackageContext(context.Background(), data, options)
}

// SignPackageContext 签署独立包并响应取消，不修改输入
// 入参: ctx 取消上下文, data 包字节, options 签署选项
// 返回: []byte 签署后的完整包, error 签署或取消错误
func SignPackageContext(ctx context.Context, data []byte, options SignatureWriteOptions) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r, err := NewReader(&Input{reader: bytes.NewReader(data), size: int64(len(data))}, options.ReaderOptions...)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if _, err := r.SignToContext(ctx, &output, options); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// SignEditorTo 将编辑器序列化为最终文件后签署，保留或显式覆盖加密策略
// 含版本列表的文档需先通过ExtractVersion提取后签署
// 不修改编辑器状态；OnWriteProgress可在准备、签署和加密阶段取消，准备失败不写入目标
// 输出目标不得覆盖源文件；目标自身写入失败时调用方应丢弃部分输出
// 入参: writer 输出目标, editor 编辑器, options 签署选项
// 返回: int64 写出字节数, error 错误信息
func SignEditorTo(writer io.Writer, editor *Editor, options SignatureWriteOptions) (int64, error) {
	return SignEditorToContext(context.Background(), writer, editor, options)
}

// SignEditorToContext 序列化独立输出后签署，取消时保留编辑状态和历史
// 入参: ctx 取消上下文, writer 输出目标, editor 编辑器, options 签署选项
// 返回: int64 写出字节数, error 签署或取消错误
func SignEditorToContext(ctx context.Context, writer io.Writer, editor *Editor, options SignatureWriteOptions) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if writer == nil || editor == nil {
		return 0, fmt.Errorf("nil signature editor or writer")
	}
	if err := sameOutputFile(editor.sourceFile, writer); err != nil {
		return 0, err
	}
	if options.Encryption == nil && editor.encryption != nil && editor.encryption.options == nil {
		return 0, ErrEncryptionPolicyRequired
	}
	snapshot, err := editor.outputSnapshot(ctx, WriteOptions{})
	if err != nil {
		return 0, err
	}
	defer snapshot.output.release()
	var plain editorSnapshotBuffer
	defer func() { _ = plain.Close() }()
	if _, err := snapshot.writePlaintext(&plain); err != nil {
		return 0, err
	}
	r, err := NewReader(&Input{reader: &plain, size: plain.size})
	if err != nil {
		return 0, err
	}
	if editor.source != nil {
		r, err = r.Document(editor.source.reader.DocumentIndex())
		if err != nil {
			return 0, err
		}
	}
	r.encryption = editor.encryption
	options.progress = editorProgress(editor.OnWriteProgress)
	return r.SignToContext(ctx, writer, options)
}

// signatureWriteInfo 写出签名信息，省略数字签名中不存在的印章
type signatureWriteInfo struct {
	Provider          SignatureProvider   `xml:"Provider"`
	SignatureMethod   string              `xml:"SignatureMethod"`
	SignatureDateTime string              `xml:"SignatureDateTime"`
	References        SignatureReferences `xml:"References"`
	Stamps            []SignatureStamp    `xml:"StampAnnot,omitempty"`
	Seal              *SignatureSeal      `xml:"Seal,omitempty"`
}

// signatureWriteDocument 写出Signature.xml根节点结构
type signatureWriteDocument struct {
	Info  signatureWriteInfo `xml:"SignedInfo"`
	Value string             `xml:"SignedValue"`
}

// signatureIDSequence 签名及签章注释共用的编号上限，与文档图元编号相互独立
type signatureIDSequence uint64

// observe 记录已有签章标识的编号上限，其他命名形式保持不变
// 入参: id 已有标识
func (s *signatureIDSequence) observe(id string) {
	n, err := strconv.ParseUint(strings.TrimPrefix(id, "s"), 10, 64)
	if err == nil && n > uint64(*s) {
		*s = signatureIDSequence(n)
	}
}

// next 分配采用sNNN格式的签章标识
// 返回: string 新标识, error 编号耗尽错误
func (s *signatureIDSequence) next() (string, error) {
	if uint64(*s) == ^uint64(0) {
		return "", fmt.Errorf("signature ID exhausted")
	}
	*s++
	return "s" + strconv.FormatUint(uint64(*s), 10), nil
}

// prepareDocumentSignatureEntries 为OFD中缺少Signatures声明的DocBody补充空签名列表与声明
// 入参: ctx 取消上下文, r 阅读器, ofdData 原OFD.xml内容, overlay 输出覆盖层, customPaths 指定文档的预设路径，可为nil
// 返回: []byte 更新后的OFD.xml，未改动时为nil, error 读取或编码错误
func prepareDocumentSignatureEntries(ctx context.Context, r *Reader, ofdData []byte, overlay map[string][]byte, customPaths map[int]string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	emptyList, err := signatureWriteXML("Signatures", Signatures{})
	if err != nil {
		return nil, err
	}
	current := ofdData
	changed := false
	for index, body := range r.OFD.DocBody {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if body.Signatures != "" {
			continue
		}
		listPath := ""
		if customPaths != nil {
			listPath = customPaths[index]
		}
		if listPath == "" {
			docDir := signatureDocumentDirectory(ctx, r, index, body)
			listPath = signatureWriteAvailableInView(r, overlay, path.Join(docDir, "Signs", "Signatures.xml"))
			overlay[listPath] = bytes.Clone(emptyList)
		}
		root, err := parseEditorXMLContext(ctx, current)
		if err != nil {
			return nil, err
		}
		node := root.childAt("DocBody", index)
		if node == nil {
			return nil, fmt.Errorf("missing document body")
		}
		current, err = editorXMLSetText(current, node, [][2]string{{"Signatures", "/" + listPath}})
		if err != nil {
			return nil, err
		}
		changed = true
	}
	if !changed {
		return nil, nil
	}
	return current, nil
}

// signatureDocumentDirectory 推导指定DocBody的签名基础目录
// 入参: ctx 取消上下文, r 阅读器, index 文档索引, body 文档体
// 返回: string 文档目录路径
func signatureDocumentDirectory(ctx context.Context, r *Reader, index int, body DocBody) string {
	if body.DocRoot != "" {
		dir := path.Dir(cleanPackagePath(resolveResourcePath("OFD.xml", "", body.DocRoot)))
		if dir != "" && dir != "." {
			return dir
		}
	}
	if body.versioned {
		view := &Reader{Zip: r.Zip, encrypted: r.encryptedSource(), files: r.files, documentIndex: index, resources: r.resources}
		if view.initRootContext(ctx) == nil {
			if root, err := view.DocumentRootContext(ctx); err == nil {
				dir := path.Dir(cleanPackagePath(root))
				if dir != "" && dir != "." {
					return dir
				}
			}
		}
	}
	return "Doc_" + strconv.Itoa(index)
}

// removeSignatures 在独立快照中移除签名入口，校验保留依赖后提交历史
// 入参: ctx 取消上下文, id 签名标识，空值清空
// 返回: error 标识、依赖、结构或取消错误
func (e *Editor) removeSignatures(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	reader, err := e.readerContext(ctx, nil)
	if err != nil {
		return err
	}
	doc, err := reader.DocContext(ctx)
	if err != nil {
		return err
	}
	if doc.Signatures == "" {
		if id != "" {
			return fmt.Errorf("signature not found: %s", id)
		}
		return nil
	}
	name := reader.ResPath(doc.Signatures)
	data, err := reader.readFileViewContext(ctx, name)
	if err != nil {
		return err
	}
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return err
	}
	removed := make(map[string]bool)
	candidates := make(map[string]bool)
	var patches []editorXMLPatch
	remaining := 0
	for _, node := range root.children {
		if !node.matchesOFD("Signature") {
			continue
		}
		if id != "" && node.attr("ID") != id {
			remaining++
			continue
		}
		location := signatureRefPath(name, node.attr("BaseLoc"))
		files, _, err := reader.signatureRemovalFiles(ctx, location)
		if err != nil {
			return err
		}
		maps.Copy(candidates, files)
		removed[node.attr("ID")] = true
		patches = append(patches, editorXMLPatch{start: node.start, end: node.end})
	}
	if id != "" && !removed[id] {
		return fmt.Errorf("signature not found: %s", id)
	}
	listShared := false
	if reader.versionInfo != nil {
		listShared, err = reader.versionSignatureListShared(ctx, name)
		if err != nil {
			return err
		}
	}
	protected, dependencies, err := reader.signatureRemovalProtected(ctx, name, removed, reader.versionInfo != nil && !listShared)
	if err != nil {
		return err
	}
	parts := make(map[string][]byte)
	if reader.versionInfo != nil {
		if remaining != 0 {
			if listShared {
				return fmt.Errorf("%w: version shares signature list %s", ErrSignatureDependency, name)
			}
			if dependencies[name] {
				return fmt.Errorf("%w: retained signature protects %s", ErrSignatureDependency, name)
			}
			for file := range candidates {
				if dependencies[file] {
					return fmt.Errorf("%w: retained signature protects %s", ErrSignatureDependency, file)
				}
			}
			parts[name] = editorPatchXML(data, patches)
		} else {
			candidates[name] = true
		}
		used, err := reader.versionFileDependencies(ctx, reader.documentRoot, nil, nil)
		if err != nil {
			return err
		}
		removedFiles := maps.Clone(candidates)
		for file := range used {
			delete(removedFiles, file)
		}
		shared, err := reader.versionSharedFiles(ctx)
		if err != nil {
			return err
		}
		if err := reader.versionManifest(ctx, parts, nil, used, shared, removedFiles); err != nil {
			return err
		}
	} else {
		if dependencies[name] {
			return fmt.Errorf("%w: retained signature protects %s", ErrSignatureDependency, name)
		}
		for file := range candidates {
			if dependencies[file] {
				return fmt.Errorf("%w: retained signature protects %s", ErrSignatureDependency, file)
			}
		}
		versions, err := reader.DocumentVersionsContext(ctx)
		if err != nil {
			return err
		}
		for _, version := range versions {
			for _, file := range version.Files {
				if file.Location == name {
					return fmt.Errorf("%w: version %s uses %s", ErrSignatureDependency, version.ID, name)
				}
			}
		}
		entry, err := reader.readFileViewContext(ctx, "OFD.xml")
		if err != nil {
			return err
		}
		ofd, err := parseEditorXMLContext(ctx, entry)
		if err != nil {
			return err
		}
		node := ofd.childAt("DocBody", reader.documentIndex).child("Signatures")
		if remaining == 0 {
			parts["OFD.xml"] = editorPatchXML(entry, []editorXMLPatch{{start: node.start, end: node.end}})
			candidates[name] = true
		} else {
			target := name
			shared := false
			for index, body := range reader.OFD.DocBody {
				shared = shared || index != reader.documentIndex && body.Signatures != "" && signatureRefPath("OFD.xml", body.Signatures) == name
			}
			if shared {
				target = packageAvailableName(reader, parts, name)
				parts["OFD.xml"] = editorPatchXML(entry, []editorXMLPatch{editorXMLContent(entry, node, editorFontXMLText("/"+target))})
			}
			parts[target] = editorPatchXML(data, patches)
		}
	}
	for file, updated := range parts {
		if !protected[file] {
			continue
		}
		same, err := packageDataEqual(ctx, reader, file, updated)
		if err != nil {
			return err
		}
		if !same {
			return fmt.Errorf("%w: protected file %s", ErrSignatureDependency, file)
		}
	}
	for file, data := range reader.files {
		if _, changed := parts[file]; !changed {
			parts[file] = data
		}
	}
	reader.files = parts
	for file := range protected {
		delete(candidates, file)
	}
	if err := reader.retireVersionFiles(ctx, candidates); err != nil {
		return err
	}
	return e.commitResourceSnapshot(ctx, reader)
}

// signatureRemovalFiles 区分签名自有文件和摘要保护内容，正文不作为删除候选
// 入参: ctx 取消上下文, name 签名描述路径
// 返回: map[string]bool 自有文件, map[string]bool 保护文件, error 读取或解析错误
func (r *Reader) signatureRemovalFiles(ctx context.Context, name string) (map[string]bool, map[string]bool, error) {
	data, err := r.readFileViewContext(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	var signature SignatureFile
	if err := xml.NewDecoder(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), ctx: ctx}).Decode(&signature); err != nil {
		return nil, nil, err
	}
	owned := map[string]bool{name: true}
	for _, location := range []string{signature.SignedValue, signature.SignedInfo.Seal.BaseLoc} {
		if location != "" {
			owned[signatureRefPath(name, location)] = true
		}
	}
	protected := make(map[string]bool)
	for _, reference := range signature.SignedInfo.References.Reference {
		protected[signatureRefPath(name, reference.FileRef)] = true
	}
	return owned, protected, ctx.Err()
}

// versionSignatureListShared 精确判断当前版本的签名列表是否实际被其他版本、主入口或其他文档共享
// 入参: ctx 取消上下文, name 签名列表包内路径
// 返回: bool 是否实际共享, error 读取或取消错误
func (r *Reader) versionSignatureListShared(ctx context.Context, name string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	clean := cleanPackagePath(name)
	if r.OFD.DocBody[r.documentIndex].DocRoot != "" {
		return true, nil
	}
	versions, err := r.DocumentVersionsContext(ctx)
	if err != nil {
		return false, err
	}
	for _, version := range versions {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if r.versionInfo != nil && version.ID == r.versionInfo.ID {
			continue
		}
		for _, file := range version.Files {
			if cleanPackagePath(file.Location) == clean {
				return true, nil
			}
		}
	}
	var otherIndexes []int
	for index := range r.DocumentCount() {
		if index != r.documentIndex {
			otherIndexes = append(otherIndexes, index)
		}
	}
	if len(otherIndexes) != 0 {
		otherFiles, err := r.documentFiles(ctx, otherIndexes...)
		if err != nil {
			return false, err
		}
		if otherFiles[clean] {
			return true, nil
		}
	}
	return false, ctx.Err()
}

// signatureRemovalProtected 汇总保留签名的原文及其他文档的签名入口，不要求证书或私钥
// 入参: ctx 取消上下文, selected 当前签名列表, removed 待移除标识, allowCurrentVersion 是否允许修改当前版本独享的签名列表
// 返回: map[string]bool 保留路径, map[string]bool 非共享的摘要保护路径, error 读取、结构或取消错误
func (r *Reader) signatureRemovalProtected(ctx context.Context, selected string, removed map[string]bool, allowCurrentVersion bool) (map[string]bool, map[string]bool, error) {
	protected := make(map[string]bool)
	dependencies := make(map[string]bool)
	retained := make(map[string]bool)
	for index, body := range r.OFD.DocBody {
		if body.Signatures == "" {
			continue
		}
		name := signatureRefPath("OFD.xml", body.Signatures)
		current := index == r.documentIndex && (r.versionInfo == nil || allowCurrentVersion)
		if !current {
			protected[name] = true
		}
		data, err := r.readFileViewContext(ctx, name)
		if err != nil {
			return nil, nil, err
		}
		root, err := parseEditorXMLContext(ctx, data)
		if err != nil {
			return nil, nil, err
		}
		for _, node := range root.children {
			if !node.matchesOFD("Signature") || current && name == selected && removed[node.attr("ID")] {
				continue
			}
			owned, refs, err := r.signatureRemovalFiles(ctx, signatureRefPath(name, node.attr("BaseLoc")))
			if err != nil {
				return nil, nil, err
			}
			maps.Copy(protected, owned)
			maps.Copy(retained, owned)
			maps.Copy(protected, refs)
			maps.Copy(dependencies, refs)
		}
	}
	for name := range retained {
		delete(dependencies, name)
	}
	return protected, dependencies, ctx.Err()
}

// signatureWriteOutput 签署后恢复加密策略，未修改资源走流式直通避免全包解压重压缩
// 入参: ctx 取消上下文, r 阅读器, writer 输出目标, options 签署选项
// 返回: int64 写出字节数, error 签署、输出或取消错误
func signatureWriteOutput(ctx context.Context, r *Reader, writer io.Writer, options SignatureWriteOptions) (int64, error) {
	if r == nil {
		return 0, fmt.Errorf("nil signature reader")
	}
	if err := options.Compression.Validate(); err != nil {
		return 0, err
	}
	if r.documentIndex >= 0 && r.documentIndex < r.DocumentCount() && r.OFD.DocBody[r.documentIndex].versioned {
		return 0, fmt.Errorf("in-package version signing is not supported; extract the version before signing")
	}
	encryption := options.Encryption
	if encryption == nil && r.encryption != nil {
		encryption = r.encryption.options
		if encryption == nil {
			return 0, ErrEncryptionPolicyRequired
		}
	}
	if encryption != nil {
		if err := validateEncryptionOptions(*encryption); err != nil {
			return 0, err
		}
	}
	if r.OFD == nil {
		if err := r.initRootContext(ctx); err != nil {
			return 0, err
		}
	} else if err := r.indexPackageContext(ctx); err != nil {
		return 0, err
	}
	baseZip := r.Zip
	encSource := r.encryptedSource()
	var overlay map[string][]byte
	hasSignatures, err := r.hasOutputSignatures(ctx)
	if err != nil {
		return 0, err
	}
	if options.Compression.Mode != CompressionUnchanged && !hasSignatures {
		parts, err := signatureWritePartsContext(ctx, r)
		if err != nil {
			return 0, err
		}
		baseZip = nil
		encSource = nil
		overlay = parts
	} else {
		overlay = make(map[string][]byte, len(r.files)+8)
		for name, data := range r.files {
			overlay[name] = bytes.Clone(data)
		}
	}
	if encryption != nil {
		defer func() {
			for _, data := range overlay {
				clear(data)
			}
		}()
	}
	if err := options.progress.report("sign", 0, 1); err != nil {
		return 0, err
	}
	if err := signatureWriteOverlayPackage(ctx, baseZip, encSource, overlay, r.resources, options, r.DocumentIndex()); err != nil {
		return 0, err
	}
	if err := options.progress.report("sign", 1, 1); err != nil {
		return 0, err
	}
	if encryption != nil {
		encReader := &Reader{Zip: baseZip, encrypted: encSource, files: overlay, resources: r.resources}
		if err := encReader.indexPackageContext(ctx); err != nil {
			return 0, err
		}
		var staged editorSnapshotBuffer
		defer func() { _ = staged.Close() }()
		if _, err := encryptPackageToWriter(ctx, &staged, encReader, *encryption, options.progress); err != nil {
			return 0, err
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		var count int64
		output := convertWriter{ctx: ctx, writer: writer, count: &count}
		_, err := io.Copy(output, io.NewSectionReader(&staged, 0, staged.size))
		return count, err
	}
	var count int64
	output := convertWriter{ctx: ctx, writer: writer, count: &count}
	err = signatureWriteZIPOverlayTo(ctx, output, baseZip, encSource, overlay, options.Compression)
	return count, err
}

// signatureWritePartsContext 逐条读取可取消的独立包快照
// 入参: ctx 取消上下文, r 阅读器
// 返回: map[string][]byte 包文件, error 读取或取消错误
func signatureWritePartsContext(ctx context.Context, r *Reader) (map[string][]byte, error) {
	if r == nil {
		return nil, fmt.Errorf("nil signature reader")
	}
	if err := r.indexPackageContext(ctx); err != nil {
		return nil, err
	}
	parts := make(map[string][]byte, len(r.fileNames))
	for name := range r.fileNames {
		data, err := r.readFileViewContext(ctx, name)
		if err != nil {
			return nil, err
		}
		parts[name] = bytes.Clone(data)
	}
	return parts, ctx.Err()
}

// signatureWriteOverlayPackage 基于只读底包与增量覆盖层流式计算摘要、生成签名并完成回验
// 入参: ctx 取消上下文, baseZip 源只读ZIP归档, encSource 加密按需解密源, overlay 修改与新增文件覆盖层, resources 资源选项, options 签署选项, index 文档索引
// 返回: error 签署或取消错误
func signatureWriteOverlayPackage(ctx context.Context, baseZip *zip.Reader, encSource *encryptedPackageSource, overlay map[string][]byte, resources *Resources, options SignatureWriteOptions, index int) error {
	if err := options.Compression.Validate(); err != nil {
		return err
	}
	if options.Mode != SignatureAppend && options.Mode != SignatureReplace {
		return fmt.Errorf("unsupported signature write mode")
	}
	r := &Reader{Zip: baseZip, encrypted: encSource, files: overlay, documentIndex: index, resources: resources}
	if err := r.initRootContext(ctx); err != nil {
		return err
	}
	doc, err := r.DocContext(ctx)
	if err != nil {
		return err
	}
	if options.Time.IsZero() {
		options.Time = time.Now()
	}
	options.Time = options.Time.UTC().Truncate(time.Second)
	identity, err := signatureWriteIdentity(options)
	if err != nil {
		return err
	}
	digestCache := make(map[[2]string][]byte, len(r.fileNames)+8)
	var parsedExisting signatureVerifyOptions
	for _, opt := range options.ExistingVerifyOptions {
		if opt != nil {
			opt(&parsedExisting)
		}
	}
	existingVerifyOptions := append([]SignatureVerifyOption{withSignatureDigestCache(digestCache)}, options.ExistingVerifyOptions...)
	existingVerifyOptions = append(existingVerifyOptions, WithSignatureCert(identity.cert.Raw))
	verifyOptions := slices.Clone(existingVerifyOptions)
	if len(options.Intermediates) != 0 {
		verifyOptions = append(verifyOptions, WithSignatureCerts(options.Intermediates...))
	}
	if len(parsedExisting.TrustCerts) != 0 && len(options.TrustRoots) != 0 {
		verifyOptions = append(verifyOptions, WithSignatureTrustCerts(options.TrustRoots...))
	}
	protected, err := signatureWriteProtectedNames(ctx, r, baseZip, encSource, overlay, resources, verifyOptions)
	if err != nil {
		return err
	}
	if options.Compression.Mode != CompressionUnchanged && baseZip == nil && encSource == nil && doc.Signatures == "" && len(protected) == 0 {
		images, sizes, err := r.compressionImagePlan(ctx, options.Compression)
		if err != nil {
			return err
		}
		optimizer := &Editor{backends: defaultRenderBackends(), runtimeResources: resources, source: &editorSource{reader: r}, output: &outputOptimization{ctx: ctx, options: options.Compression, sizes: sizes, images: images}}
		optimizer.OnWriteProgress = func(stage string, completed, total int) error {
			return options.progress.report(stage, completed, total)
		}
		optParts := make(map[string][]byte)
		removed, err := optimizer.compactResourceReferences(optParts, r, editorProgress(optimizer.OnWriteProgress), nil, nil)
		if err != nil {
			return err
		}
		if removed == nil {
			removed = make(map[string]bool)
		}
		if err := optimizer.compressResourceReferences(optParts, r, removed, nil); err != nil {
			return err
		}
		if err := optimizer.compactPackageFonts(ctx, optParts, removed, nil); err != nil {
			return err
		}
		maps.Copy(overlay, optParts)
		for name := range overlay {
			if removed[cleanPackagePath(name)] {
				delete(overlay, name)
			}
		}
		clear(digestCache)
		r = &Reader{files: overlay, documentIndex: index, resources: resources}
		if err := r.initRootContext(ctx); err != nil {
			return err
		}
		doc, err = r.DocContext(ctx)
		if err != nil {
			return err
		}
	}
	stamps, err := signatureWriteStamps(r, options.Stamps, len(options.Seal) != 0)
	if err != nil {
		return err
	}
	var list Signatures
	listPath := r.ResPath(doc.Signatures)
	var originalListData []byte
	if doc.Signatures != "" {
		data, err := r.readFileViewContext(ctx, listPath)
		if err != nil {
			return fmt.Errorf("signature list not found: %s", listPath)
		}
		originalListData = bytes.Clone(data)
		if err := xml.Unmarshal(originalListData, &list); err != nil {
			return err
		}
	}
	oldCount := len(list.List)
	previousList, err := signatureWriteXML("Signatures", list)
	if err != nil {
		return err
	}
	if options.Mode == SignatureAppend && oldCount > 0 {
		reports, err := r.VerifyDocumentSignaturesContext(ctx, index, existingVerifyOptions...)
		if err != nil {
			return err
		}
		ids := make(map[string]bool)
		for _, report := range reports {
			if !report.Valid || report.StampPositionError != "" || ids[report.ID] || report.ID == "" {
				return fmt.Errorf("existing signature %s is invalid: %s", report.ID, report.Error)
			}
			ids[report.ID] = true
			for _, ref := range report.References {
				if ref.Path == listPath {
					return fmt.Errorf("signature %s protects the signature list", report.ID)
				}
			}
		}
	}
	originalOFDData, err := r.readFileViewContext(ctx, "OFD.xml")
	if err != nil {
		return err
	}
	originalOFDData = bytes.Clone(originalOFDData)
	modifiedNames := make(map[string][]byte)
	if doc.Signatures == "" {
		listPath = signatureWriteAvailableInView(r, overlay, path.Join(r.RootDir, "Signs", "Signatures.xml"))
		if options.PrepareAllDocuments && len(r.OFD.DocBody) > 1 && !protected["OFD.xml"] {
			updatedOFD, err := prepareDocumentSignatureEntries(ctx, r, originalOFDData, overlay, map[int]string{index: listPath})
			if err != nil {
				return err
			}
			if updatedOFD != nil {
				modifiedNames["OFD.xml"] = originalOFDData
				overlay["OFD.xml"] = updatedOFD
				deleteSignatureDigestCachePath(digestCache, "OFD.xml")
			}
		} else {
			root, err := parseEditorXML(originalOFDData)
			if err != nil {
				return err
			}
			body := root.childAt("DocBody", index)
			if body == nil {
				return fmt.Errorf("missing document body")
			}
			updatedOFD, err := editorXMLSetText(originalOFDData, body, [][2]string{{"Signatures", "/" + listPath}})
			if err != nil {
				return err
			}
			modifiedNames["OFD.xml"] = originalOFDData
			overlay["OFD.xml"] = updatedOFD
			deleteSignatureDigestCachePath(digestCache, "OFD.xml")
		}
	} else if options.PrepareAllDocuments && len(r.OFD.DocBody) > 1 && !protected["OFD.xml"] {
		updatedOFD, err := prepareDocumentSignatureEntries(ctx, r, originalOFDData, overlay, nil)
		if err != nil {
			return err
		}
		if updatedOFD != nil {
			modifiedNames["OFD.xml"] = originalOFDData
			overlay["OFD.xml"] = updatedOFD
			deleteSignatureDigestCachePath(digestCache, "OFD.xml")
		}
	}
	if options.Mode == SignatureReplace {
		list.List = nil
		oldCount = 0
	}
	id, stamps, err := signatureWriteIDsInView(ctx, &list, listPath, r, stamps)
	if err != nil {
		return err
	}
	directory := ""
	for n := 1; ; n++ {
		directory = path.Join(path.Dir(listPath), "Sign_"+strconv.Itoa(n))
		occupied := false
		for name := range r.fileNames {
			if name == directory || strings.HasPrefix(name, directory+"/") {
				occupied = true
				break
			}
		}
		if !occupied {
			for name := range overlay {
				if name == directory || strings.HasPrefix(name, directory+"/") {
					occupied = true
					break
				}
			}
		}
		if !occupied {
			break
		}
	}
	sigPath, valuePath, sealPath := path.Join(directory, "Signature.xml"), path.Join(directory, "SignedValue.dat"), path.Join(directory, "Seal.esl")
	kind := SignTypeSign
	if len(options.Seal) > 0 {
		kind = SignTypeSeal
		overlay[sealPath] = bytes.Clone(options.Seal)
	}
	list.List = append(list.List, Signature{ID: id, Type: kind, BaseLoc: "/" + sigPath})
	updatedList, err := signatureWriteXML("Signatures", list)
	if err != nil {
		return err
	}
	if len(originalListData) != 0 {
		root, err := parseEditorXML(originalListData)
		if err != nil {
			return err
		}
		updated, err := editorXMLMerge(originalListData, root, previousList, updatedList)
		if err != nil {
			return err
		}
		updatedList = editorPatchXML(originalListData, []editorXMLPatch{{root.start, root.end, updated}})
		modifiedNames[listPath] = originalListData
	}
	overlay[listPath] = updatedList
	deleteSignatureDigestCachePath(digestCache, listPath)
	for name, before := range modifiedNames {
		if protected[name] && !bytes.Equal(before, overlay[name]) {
			return fmt.Errorf("another document signature protects %s", name)
		}
	}
	for _, created := range []string{listPath, sigPath, valuePath, sealPath} {
		if _, wasExisting := modifiedNames[created]; !wasExisting && protected[created] {
			return fmt.Errorf("another document signature protects %s", created)
		}
	}
	scoped := &Reader{Zip: baseZip, encrypted: encSource, files: overlay, documentIndex: index, resources: resources}
	if err := scoped.initRootContext(ctx); err != nil {
		return err
	}
	refs := SignatureReferences{CheckMethod: identity.digest}
	var allowed map[string]bool
	if len(scoped.OFD.DocBody) > 1 {
		allowed = map[string]bool{"OFD.xml": true}
		if cover := strings.TrimSpace(scoped.OFD.DocBody[index].DocInfo.Cover); cover != "" {
			allowed[cleanPackagePath(resolveResourcePath("OFD.xml", "", cover))] = true
		}
		docRoot := cleanPackagePath(scoped.documentRoot)
		if docRoot == "" || docRoot == "." {
			docRoot, err = scoped.DocumentRootContext(ctx)
			if err != nil {
				return err
			}
			docRoot = cleanPackagePath(docRoot)
		}
		docDir := path.Dir(docRoot)
		for name := range scoped.fileNames {
			if docDir == "." || strings.HasPrefix(name, docDir+"/") {
				allowed[name] = true
			}
		}
		if err := scoped.signatureCoverageReferences(docRoot, allowed); err != nil {
			return err
		}
		for i := 0; i < oldCount; i++ {
			entryPath := cleanPackagePath(signatureRefPath(listPath, list.List[i].BaseLoc))
			allowed[entryPath] = true
			if entryData, readErr := scoped.readFileViewContext(ctx, entryPath); readErr == nil {
				if entryFile, parseErr := parseSignatureFile(entryData); parseErr == nil {
					if entryFile.SignedValue != "" {
						allowed[cleanPackagePath(signatureRefPath(entryPath, entryFile.SignedValue))] = true
					}
					if entryFile.SignedInfo.Seal.BaseLoc != "" {
						allowed[cleanPackagePath(signatureRefPath(entryPath, entryFile.SignedInfo.Seal.BaseLoc))] = true
					}
				}
			}
		}
		if kind == SignTypeSeal {
			allowed[sealPath] = true
		}
		if options.Lock {
			allowed[listPath] = true
		}
	}
	names := make([]string, 0, len(scoped.fileNames))
	for name := range scoped.fileNames {
		if allowed != nil && !allowed[name] {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	algKey, hasAlgKey := canonicalSignatureDigestKey(identity.digest)
	for idx, name := range names {
		if err := options.progress.report("signature-references", idx, len(names)); err != nil {
			return err
		}
		if name == listPath && !options.Lock {
			continue
		}
		cleanName := cleanPackagePath(name)
		var digest []byte
		if hasAlgKey {
			if cached, found := digestCache[[2]string{cleanName, algKey}]; found {
				digest = bytes.Clone(cached)
			}
		}
		if digest == nil {
			if data, ok := overlay[cleanName]; ok {
				digest, err = signatureDigest(identity.digest, data)
			} else {
				digest, err = scoped.streamSignatureDigest(ctx, identity.digest, cleanName)
			}
			if err != nil {
				return err
			}
			if hasAlgKey {
				digestCache[[2]string{cleanName, algKey}] = bytes.Clone(digest)
			}
		}
		refs.Reference = append(refs.Reference, SignatureReference{FileRef: "/" + name, CheckValue: base64.StdEncoding.EncodeToString(digest)})
	}
	if err := options.progress.report("signature-references", len(names), len(names)); err != nil {
		return err
	}
	provider := options.Provider
	if provider.ProviderName == "" {
		provider.ProviderName = "ofdgo"
	}
	info := signatureWriteInfo{Provider: provider, SignatureMethod: identity.method, SignatureDateTime: options.Time.Format(time.RFC3339), References: refs, Stamps: stamps}
	if kind == SignTypeSeal {
		info.Seal = &SignatureSeal{BaseLoc: "/" + sealPath}
	}
	overlay[sigPath], err = signatureWriteXML("Signature", signatureWriteDocument{Info: info, Value: "/" + valuePath})
	if err != nil {
		return err
	}
	overlay[valuePath], err = identity.sign(overlay[sigPath], "/"+sigPath, options)
	if err != nil {
		return err
	}
	check := &Reader{Zip: baseZip, encrypted: encSource, files: overlay, resources: resources}
	if err := check.initRootContext(ctx); err != nil {
		return err
	}
	reports, err := check.VerifyDocumentSignaturesContext(ctx, index, verifyOptions...)
	if err != nil {
		return err
	}
	if len(reports) != oldCount+1 {
		return fmt.Errorf("signature verification count mismatch")
	}
	for _, report := range reports {
		if !report.Valid || report.StampPositionError != "" {
			return fmt.Errorf("written signature %s failed verification: %s", report.ID, report.Error)
		}
	}
	return ctx.Err()
}

// deleteSignatureDigestCachePath 移除被改写路径对应的全部缓存摘要
// 入参: cache 摘要缓存, name 包内路径
func deleteSignatureDigestCachePath(cache map[[2]string][]byte, name string) {
	clean := cleanPackagePath(name)
	for key := range cache {
		if key[0] == clean {
			delete(cache, key)
		}
	}
}

// signatureWriteProtectedNames 校验并收集其他文档及历史版本签名的全部受保护路径，不整块克隆大附件内容
// 入参: ctx 取消上下文, r 包阅读器, baseZip 底包归档, encSource 加密按需解密源, overlay 覆盖层, resources 资源选项, options 既有签名的验证选项
// 返回: map[string]bool 不可改写的包内路径集合, error 读取或验签错误
func signatureWriteProtectedNames(ctx context.Context, r *Reader, baseZip *zip.Reader, encSource *encryptedPackageSource, overlay map[string][]byte, resources *Resources, options []SignatureVerifyOption) (map[string]bool, error) {
	protected := make(map[string]bool)
	retain := func(view *Reader, name string) error {
		clean := cleanPackagePath(name)
		if protected[clean] {
			return nil
		}
		if !view.fileNames[clean] {
			return fmt.Errorf("signature file not found: %s", name)
		}
		protected[clean] = true
		return nil
	}
	collectView := func(view *Reader, index int) error {
		if err := view.initRootContext(ctx); err != nil {
			return err
		}
		doc, err := view.docStructureContext(ctx)
		if err != nil {
			return err
		}
		if doc.Signatures == "" {
			return nil
		}
		reports, err := view.VerifyDocumentSignaturesContext(ctx, index, options...)
		if err != nil {
			return err
		}
		if len(reports) == 0 {
			return nil
		}
		listPath := view.ResPath(doc.Signatures)
		if err := retain(view, listPath); err != nil {
			return err
		}
		for _, report := range reports {
			if !report.Valid {
				reason := report.Error
				if reason == "" {
					reason = "signature verification failed"
				}
				return fmt.Errorf("cannot preserve document %d signature %s: %s", index, report.ID, reason)
			}
			sigPath := signatureRefPath(listPath, report.BaseLoc)
			sigData, err := view.readFileViewContext(ctx, sigPath)
			if err != nil {
				return err
			}
			file, err := parseSignatureFile(sigData)
			if err != nil {
				return err
			}
			names := []string{sigPath, signatureRefPath(sigPath, file.SignedValue)}
			if file.SignedInfo.Seal.BaseLoc != "" {
				names = append(names, signatureRefPath(sigPath, file.SignedInfo.Seal.BaseLoc))
			}
			for _, ref := range report.References {
				names = append(names, ref.Path)
			}
			for _, name := range names {
				if err := retain(view, name); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for index, body := range r.OFD.DocBody {
		if index == r.documentIndex || body.Signatures == "" {
			continue
		}
		if body.DocRoot != "" {
			mainView := &Reader{Zip: baseZip, encrypted: encSource, files: overlay, documentIndex: index, selectedVersion: new(string), resources: resources}
			if err := collectView(mainView, index); err != nil {
				return nil, err
			}
		}
		if body.versioned {
			baseView := &Reader{Zip: baseZip, encrypted: encSource, files: overlay, documentIndex: index, resources: resources}
			if err := baseView.initRootContext(ctx); err != nil {
				return nil, err
			}
			versions, err := baseView.DocumentVersionsContext(ctx)
			if err != nil {
				return nil, err
			}
			for _, version := range versions {
				versionID := version.ID
				verView := &Reader{Zip: baseZip, encrypted: encSource, files: overlay, documentIndex: index, selectedVersion: &versionID, resources: resources}
				if err := collectView(verView, index); err != nil {
					return nil, err
				}
			}
		}
	}
	return protected, nil
}

// signatureWriteIDsInView 按各自XML文档作用域判重签章标识并递增全局编号，不修改文档图元编号
// 入参: ctx 取消上下文, list 签名列表, listPath 列表路径, view 包读取视图, stamps 外观
// 返回: string 签名标识, []SignatureStamp 外观, error 错误信息
func signatureWriteIDsInView(ctx context.Context, list *Signatures, listPath string, view *Reader, stamps []SignatureStamp) (string, []SignatureStamp, error) {
	var sequence signatureIDSequence
	sequence.observe(list.MaxSignID)
	add := func(used map[string]bool, id string) error {
		if !ofdXMLIDValid(id) || used[id] {
			return fmt.Errorf("invalid or duplicate signature ID: %s", id)
		}
		used[id] = true
		sequence.observe(id)
		return nil
	}
	listUsed := make(map[string]bool, len(list.List))
	for _, ref := range list.List {
		if err := add(listUsed, ref.ID); err != nil {
			return "", nil, err
		}
		sigData, err := view.readFileViewContext(ctx, signatureRefPath(listPath, ref.BaseLoc))
		if err != nil {
			return "", nil, err
		}
		file, err := parseSignatureFile(sigData)
		if err != nil {
			return "", nil, err
		}
		sigUsed := make(map[string]bool, len(file.SignedInfo.StampAnnot))
		for _, stamp := range file.SignedInfo.StampAnnot {
			if err := add(sigUsed, stamp.ID); err != nil {
				return "", nil, err
			}
		}
	}
	newUsed := make(map[string]bool, len(stamps))
	for _, stamp := range stamps {
		if stamp.ID != "" {
			if err := add(newUsed, stamp.ID); err != nil {
				return "", nil, err
			}
		}
	}
	id, err := sequence.next()
	if err != nil {
		return "", nil, err
	}
	for i := range stamps {
		if stamps[i].ID == "" {
			stamps[i].ID, err = sequence.next()
			if err != nil {
				return "", nil, err
			}
		}
	}
	list.MaxSignID = "s" + strconv.FormatUint(uint64(sequence), 10)
	return id, stamps, nil
}

// signatureWriteXML 编码带OFD命名空间的签名文件
// 入参: name 根节点名称, value 内容
// 返回: []byte XML, error 错误信息
func signatureWriteXML(name string, value any) ([]byte, error) {
	var out bytes.Buffer
	out.WriteString(xml.Header)
	err := xml.NewEncoder(&out).EncodeElement(value, xml.StartElement{Name: xml.Name{Space: ofdNamespace2016, Local: name}})
	return out.Bytes(), err
}

// signatureWriteAvailableInView 在底包与覆盖层中分配不冲突的可用路径
// 入参: view 底包读取器, overlay 覆盖层, name 候选路径
// 返回: string 可用路径
func signatureWriteAvailableInView(view *Reader, overlay map[string][]byte, name string) string {
	original := name
	for i := 1; ; i++ {
		clean := cleanPackagePath(name)
		_, inOverlay := overlay[clean]
		if !inOverlay && !view.fileNames[clean] {
			return clean
		}
		name = path.Join(path.Dir(original), strconv.Itoa(i)+"_"+path.Base(original))
	}
}

// signatureWriteZIPOverlayTo 写出已回验包内容，对未修改ZIP源条目采用OpenRaw与CreateRaw原样直通
// 入参: ctx 取消上下文, writer 输出目标, baseZip 源ZIP归档, encSource 加密按需解密源, overlay 覆盖与新增文件, compression 压缩配置
// 返回: error 输出或取消错误
func signatureWriteZIPOverlayTo(ctx context.Context, writer io.Writer, baseZip *zip.Reader, encSource *encryptedPackageSource, overlay map[string][]byte, compression CompressionOptions) error {
	all := make(map[string]*zip.File)
	namesSet := make(map[string]bool, len(overlay))
	if baseZip != nil {
		for _, zf := range baseZip.File {
			if zf.FileInfo().IsDir() {
				continue
			}
			clean := cleanPackagePath(zf.Name)
			all[clean] = zf
			namesSet[clean] = true
		}
	} else if encSource != nil {
		for _, name := range encSource.names() {
			namesSet[name] = true
		}
	}
	for name := range overlay {
		namesSet[cleanPackagePath(name)] = true
	}
	names := slices.Sorted(maps.Keys(namesSet))
	w := zip.NewWriter(writer)
	configureArchive(w, compression)
	var copyBuf []byte
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if data, changed := overlay[name]; changed {
			f, err := w.Create(name)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), ctx: ctx}); err != nil {
				return err
			}
			continue
		}
		if zf, ok := all[name]; ok {
			if compression.Mode == CompressionUnchanged {
				rawReader, err := zf.OpenRaw()
				if err != nil {
					return err
				}
				fh := zf.FileHeader
				fh.Name = name
				fh.Comment = ""
				fh.Flags &^= 0x800
				fh.NonUTF8 = false
				prepareOutputHeader(&fh, zf.Method, zf.UncompressedSize64, zf.CompressedSize64, zf.CRC32)
				rawWriter, err := w.CreateRaw(&fh)
				if err != nil {
					return err
				}
				if copyBuf == nil {
					copyBuf = make([]byte, 32<<10)
				}
				if _, err := io.CopyBuffer(rawWriter, imageInput{ReadCloser: io.NopCloser(rawReader), ctx: ctx}, copyBuf); err != nil {
					return err
				}
				continue
			}
			rc, err := zf.Open()
			if err != nil {
				return err
			}
			f, err := w.Create(name)
			if err != nil {
				_ = rc.Close()
				return err
			}
			if copyBuf == nil {
				copyBuf = make([]byte, 32<<10)
			}
			_, copyErr := io.CopyBuffer(f, imageInput{ReadCloser: rc, ctx: ctx}, copyBuf)
			closeErr := rc.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			continue
		}
		if encSource != nil && encSource.has(name) {
			rc, err := encSource.open(name)
			if err != nil {
				return err
			}
			f, err := w.Create(name)
			if err != nil {
				_ = rc.Close()
				return err
			}
			if copyBuf == nil {
				copyBuf = make([]byte, 32<<10)
			}
			_, copyErr := io.CopyBuffer(f, imageInput{ReadCloser: rc, ctx: ctx}, copyBuf)
			closeErr := rc.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			continue
		}
		return fmt.Errorf("missing package file: %s", name)
	}
	if err := w.Close(); err != nil {
		return err
	}
	return ctx.Err()
}
