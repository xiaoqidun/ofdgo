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

const (
	// SignatureAppend 追加签署，要求原活动签名有效且未保护签名列表
	SignatureAppend SignatureWriteMode = iota
	// SignatureReplace 重新签署，替换全部活动签名，保留原签名数据文件
	SignatureReplace
)

// SignatureWriteMode 签署方式
type SignatureWriteMode uint8

// ErrSignatureDependency 表示移除操作会改变保留签名或其他版本使用的入口和保护文件
var ErrSignatureDependency = errors.New("signature dependency conflict")

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
	protected, dependencies, err := reader.signatureRemovalProtected(ctx, name, removed)
	if err != nil {
		return err
	}
	parts := make(map[string][]byte)
	if reader.versionInfo != nil {
		if remaining != 0 {
			return fmt.Errorf("%w: version shares signature list %s", ErrSignatureDependency, name)
		}
		candidates[name] = true
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

// signatureRemovalProtected 汇总保留签名的原文及其他文档的签名入口，不要求证书或私钥
// 入参: ctx 取消上下文, selected 当前签名列表, removed 待移除标识
// 返回: map[string]bool 保留路径, map[string]bool 非共享的摘要保护路径, error 读取、结构或取消错误
func (r *Reader) signatureRemovalProtected(ctx context.Context, selected string, removed map[string]bool) (map[string]bool, map[string]bool, error) {
	protected := make(map[string]bool)
	dependencies := make(map[string]bool)
	retained := make(map[string]bool)
	for index, body := range r.OFD.DocBody {
		if body.Signatures == "" {
			continue
		}
		name := signatureRefPath("OFD.xml", body.Signatures)
		current := index == r.documentIndex && r.versionInfo == nil
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

// SignatureWriteOptions 最终包签署选项，不修改Editor或调用方的原件
// SM2数字签名使用GB/T35275-2017消息结构；RSA/ECDSA使用裸签名，验签需WithSignatureCert
// Seal仅接受已有合法SES v4电子印章，使用SM2/SM3，不声明GM/T0031-2025兼容
// TrustRoots必须显式指定；证书吊销状态和外部业务授权由调用方在签署前检查
// Time为签署方声明的时间，不是可信时间戳，零值取当前时间；Lock保护签名列表，禁止后续追加签署
// ExistingVerifyOptions用于追加签署前和写出后的验证，可提供已有裸签名的验证证书
// ReaderOptions用于打开加密包；Encryption为空时继承阅读器的加密策略
// Compression在计算签名前优化图像，不改写已有签名保护的资源
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
	ExistingVerifyOptions []SignatureVerifyOption
	ReaderOptions         []ReaderOption
	Encryption            *EncryptionOptions
	Compression           CompressionOptions
	progress              editorProgress
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

// signatureIDSequence 签名及签章注释共用的编号上限，与文档图元编号相互独立
type signatureIDSequence uint64

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
	if options.Encryption == nil && editor.encryption != nil && editor.encryption.options == nil {
		return 0, ErrEncryptionPolicyRequired
	}
	snapshot, err := editor.outputSnapshot(ctx, WriteOptions{})
	if err != nil {
		return 0, err
	}
	defer snapshot.output.release()
	var plain editorSnapshotBuffer
	defer func() {
		for _, chunk := range plain.chunks {
			clear(chunk)
		}
	}()
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
	if writer == nil {
		return 0, fmt.Errorf("nil signature writer")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	options.progress = options.progress.withContext(ctx)
	return signatureWriteOutput(ctx, r, writer, options)
}

// signatureWriteOutput 签署后恢复加密策略，避免重复封装和解包
// 入参: ctx 取消上下文, r 阅读器, writer 输出目标, options 签署选项
// 返回: int64 写出字节数, error 签署、输出或取消错误
func signatureWriteOutput(ctx context.Context, r *Reader, writer io.Writer, options SignatureWriteOptions) (int64, error) {
	if r == nil {
		return 0, fmt.Errorf("nil signature reader")
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
	parts, err := signatureWritePartsContext(ctx, r)
	if err != nil {
		return 0, err
	}
	if encryption != nil {
		defer func() {
			for _, data := range parts {
				clear(data)
			}
		}()
	}
	if err := options.progress.report("sign", 0, 1); err != nil {
		return 0, err
	}
	if err := signatureWritePackage(ctx, parts, options, r.DocumentIndex()); err != nil {
		return 0, err
	}
	if err := options.progress.report("sign", 1, 1); err != nil {
		return 0, err
	}
	var count int64
	output := convertWriter{ctx: ctx, writer: writer, count: &count}
	if encryption != nil {
		data, err := encryptPackageParts(parts, *encryption, options.progress)
		if err != nil {
			return 0, err
		}
		defer clear(data)
		_, err = output.Write(data)
		return count, err
	}
	err = signatureWriteZIPTo(ctx, output, parts, options.Compression)
	return count, err
}

// signatureWritePartsContext 逐条读取可取消的独立包快照
// 入参: ctx 取消上下文, r 阅读器
// 返回: map[string][]byte 包文件, error 读取或取消错误
func signatureWritePartsContext(ctx context.Context, r *Reader) (map[string][]byte, error) {
	if r == nil {
		return nil, fmt.Errorf("nil signature reader")
	}
	if err := r.indexPackage(); err != nil {
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

// signatureWritePackage 完成输出优化后计算签名，不改写已有签名保护的资源
// 签名目录采用Sign_N，标识使用独立的sN序列
// 入参: ctx 取消上下文, parts 独立包文件, options 签署选项, index 文档索引
// 返回: error 签署或取消错误
func signatureWritePackage(ctx context.Context, parts map[string][]byte, options SignatureWriteOptions, index int) error {
	if err := options.Compression.Validate(); err != nil {
		return err
	}
	if options.Mode != SignatureAppend && options.Mode != SignatureReplace {
		return fmt.Errorf("unsupported signature write mode")
	}
	r := &Reader{files: parts, documentIndex: index}
	if err := r.initRoot(); err != nil {
		return err
	}
	doc, err := r.Doc()
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
	verifyOptions := append([]SignatureVerifyOption(nil), options.ExistingVerifyOptions...)
	verifyOptions = append(verifyOptions, WithSignatureCert(identity.cert.Raw))
	protected, err := signatureWriteProtected(ctx, r, parts, verifyOptions)
	if err != nil {
		return err
	}
	if options.Compression.Mode != CompressionUnchanged && doc.Signatures == "" && len(protected) == 0 {
		images, sizes, err := r.compressionImagePlan(ctx, options.Compression)
		if err != nil {
			return err
		}
		optimizer := &Editor{output: &outputOptimization{ctx: ctx, options: options.Compression, sizes: sizes, images: images}}
		optimizer.OnWriteProgress = func(stage string, completed, total int) error {
			return options.progress.report(stage, completed, total)
		}
		removed := make(map[string]bool)
		if err := optimizer.compressResourceParts(parts, nil, removed); err != nil {
			return err
		}
		for name := range parts {
			if removed[cleanPackagePath(name)] {
				delete(parts, name)
			}
		}
		r = &Reader{files: parts, documentIndex: index}
		if err := r.initRoot(); err != nil {
			return err
		}
		doc, err = r.Doc()
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
	if doc.Signatures != "" {
		data, ok := parts[listPath]
		if !ok {
			return fmt.Errorf("signature list not found: %s", listPath)
		}
		if err := xml.Unmarshal(data, &list); err != nil {
			return err
		}
	}
	oldCount := len(list.List)
	previousList, err := signatureWriteXML("Signatures", list)
	if err != nil {
		return err
	}
	if options.Mode == SignatureAppend && oldCount > 0 {
		reports, err := r.VerifyDocumentSignaturesContext(ctx, index, verifyOptions...)
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
	if doc.Signatures == "" {
		listPath = signatureWriteAvailable(parts, path.Join(r.RootDir, "Signs", "Signatures.xml"))
		root, err := parseEditorXML(parts["OFD.xml"])
		if err != nil {
			return err
		}
		body := root.childAt("DocBody", index)
		if body == nil {
			return fmt.Errorf("missing document body")
		}
		parts["OFD.xml"], err = editorXMLSetText(parts["OFD.xml"], body, [][2]string{{"Signatures", "/" + listPath}})
		if err != nil {
			return err
		}
	}
	if options.Mode == SignatureReplace {
		list.List = nil
		oldCount = 0
	}
	id, stamps, err := signatureWriteIDs(&list, listPath, parts, stamps)
	if err != nil {
		return err
	}
	directory := ""
	for n := 1; ; n++ {
		directory = path.Join(path.Dir(listPath), "Sign_"+strconv.Itoa(n))
		occupied := false
		for name := range parts {
			if name == directory || strings.HasPrefix(name, directory+"/") {
				occupied = true
				break
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
		parts[sealPath] = bytes.Clone(options.Seal)
	}
	list.List = append(list.List, Signature{ID: id, Type: kind, BaseLoc: "/" + sigPath})
	updatedList, err := signatureWriteXML("Signatures", list)
	if err != nil {
		return err
	}
	if original := parts[listPath]; len(original) != 0 {
		root, err := parseEditorXML(original)
		if err != nil {
			return err
		}
		updated, err := editorXMLMerge(original, root, previousList, updatedList)
		if err != nil {
			return err
		}
		updatedList = editorPatchXML(original, []editorXMLPatch{{root.start, root.end, updated}})
	}
	parts[listPath] = updatedList
	for name, before := range protected {
		if data, ok := parts[name]; !ok || !bytes.Equal(before, data) {
			return fmt.Errorf("another document signature protects %s", name)
		}
	}
	refs := SignatureReferences{CheckMethod: identity.digest}
	names := make([]string, 0, len(parts))
	for name := range parts {
		names = append(names, name)
	}
	slices.Sort(names)
	for index, name := range names {
		if err := options.progress.report("signature-references", index, len(names)); err != nil {
			return err
		}
		if name == listPath && !options.Lock {
			continue
		}
		digest, err := signatureDigest(identity.digest, parts[name])
		if err != nil {
			return err
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
	file := struct {
		Info  signatureWriteInfo `xml:"SignedInfo"`
		Value string             `xml:"SignedValue"`
	}{info, "/" + valuePath}
	parts[sigPath], err = signatureWriteXML("Signature", file)
	if err != nil {
		return err
	}
	parts[valuePath], err = identity.sign(parts[sigPath], "/"+sigPath, options)
	if err != nil {
		return err
	}
	check := &Reader{files: parts}
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

// signatureWriteProtected 收集其他文档的签名文件及保护内容，不受默认版本选择影响
// 入参: ctx 取消上下文, r 包阅读器, parts 原始包文件, options 既有签名的验证选项
// 返回: map[string][]byte 不可改写的文件, error 读取或验签错误
func signatureWriteProtected(ctx context.Context, r *Reader, parts map[string][]byte, options []SignatureVerifyOption) (map[string][]byte, error) {
	protected := make(map[string][]byte)
	retain := func(name string) error {
		if _, ok := protected[name]; ok {
			return nil
		}
		data, ok := parts[name]
		if !ok {
			return fmt.Errorf("signature file not found: %s", name)
		}
		protected[name] = bytes.Clone(data)
		return nil
	}
	for index := range r.OFD.DocBody {
		if index == r.documentIndex {
			continue
		}
		view := &Reader{files: parts, documentIndex: index, selectedVersion: new(string)}
		if err := view.initRoot(); err != nil {
			return nil, err
		}
		doc, err := view.docStructure()
		if err != nil {
			return nil, err
		}
		if doc.Signatures == "" {
			continue
		}
		reports, err := view.VerifyDocumentSignaturesContext(ctx, index, options...)
		if err != nil {
			return nil, err
		}
		listPath := view.ResPath(doc.Signatures)
		if err := retain(listPath); err != nil {
			return nil, err
		}
		for _, report := range reports {
			if !report.Valid {
				reason := report.Error
				if reason == "" {
					reason = "signature verification failed"
				}
				return nil, fmt.Errorf("cannot preserve document %d signature %s: %s", index, report.ID, reason)
			}
			sigPath := signatureRefPath(listPath, report.BaseLoc)
			file, err := parseSignatureFile(parts[sigPath])
			if err != nil {
				return nil, err
			}
			names := []string{sigPath, signatureRefPath(sigPath, file.SignedValue)}
			if file.SignedInfo.Seal.BaseLoc != "" {
				names = append(names, signatureRefPath(sigPath, file.SignedInfo.Seal.BaseLoc))
			}
			for _, ref := range report.References {
				names = append(names, ref.Path)
			}
			for _, name := range names {
				if err := retain(name); err != nil {
					return nil, err
				}
			}
		}
	}
	return protected, nil
}

// signatureWriteIDs 为签名和全部外观分配同一签章域的标识，不修改文档图元编号
// 入参: list 签名列表, listPath 列表路径, parts 包文件, stamps 外观
// 返回: string 签名标识, []SignatureStamp 外观, error 错误信息
func signatureWriteIDs(list *Signatures, listPath string, parts map[string][]byte, stamps []SignatureStamp) (string, []SignatureStamp, error) {
	used := make(map[string]bool)
	var sequence signatureIDSequence
	sequence.observe(list.MaxSignID)
	add := func(id string) error {
		if !ofdXMLIDValid(id) || used[id] {
			return fmt.Errorf("invalid or duplicate signature ID: %s", id)
		}
		used[id] = true
		sequence.observe(id)
		return nil
	}
	for _, ref := range list.List {
		if err := add(ref.ID); err != nil {
			return "", nil, err
		}
		file, err := parseSignatureFile(parts[signatureRefPath(listPath, ref.BaseLoc)])
		if err != nil {
			return "", nil, err
		}
		for _, stamp := range file.SignedInfo.StampAnnot {
			if err := add(stamp.ID); err != nil {
				return "", nil, err
			}
		}
	}
	for _, stamp := range stamps {
		if stamp.ID != "" {
			if err := add(stamp.ID); err != nil {
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

// signatureWriteAvailable 分配不覆盖原文件的路径
// 入参: parts 原包文件, name 候选路径
// 返回: string 可用路径
func signatureWriteAvailable(parts map[string][]byte, name string) string {
	original := name
	for i := 1; ; i++ {
		if _, found := parts[name]; !found {
			return name
		}
		name = path.Join(path.Dir(original), strconv.Itoa(i)+"_"+path.Base(original))
	}
}

// signatureWriteZIPTo 逐条封装已回验的文件，输出阶段响应取消
// 入参: ctx 取消上下文, writer 输出目标, parts 包文件, compression 压缩配置
// 返回: error 输出或取消错误
func signatureWriteZIPTo(ctx context.Context, writer io.Writer, parts map[string][]byte, compression CompressionOptions) error {
	w := zip.NewWriter(writer)
	configureArchive(w, compression)
	for _, name := range slices.Sorted(maps.Keys(parts)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := w.Create(name)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, imageInput{ReadCloser: io.NopCloser(bytes.NewReader(parts[name])), ctx: ctx}); err != nil {
			return err
		}
	}
	if err := w.Close(); err != nil {
		return err
	}
	return ctx.Err()
}

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
