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
	"crypto"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// SignatureWriteMode 签署方式
type SignatureWriteMode uint8

const (
	// SignatureAppend 追加签署，要求原活动签名有效且未保护签名列表
	SignatureAppend SignatureWriteMode = iota
	// SignatureReplace 重新签署，替换全部活动签名，保留原签名数据文件
	SignatureReplace
)

// SignatureWriteOptions 最终包签署选项，不修改Editor或调用方的原件
// SM2数字签名使用GB/T 35275-2017消息结构；RSA/ECDSA使用裸签名，验签需WithSignatureCert
// Seal仅接受已有合法SES v4电子印章，使用SM2/SM3，不声明GM/T 0031-2025兼容
// TrustRoots必须显式指定；证书吊销状态和外部业务授权由调用方在签署前检查
type SignatureWriteOptions struct {
	Signer        crypto.Signer
	Certificate   []byte
	TrustRoots    [][]byte
	Intermediates [][]byte
	Seal          []byte
	Stamps        []SignatureStamp
	Mode          SignatureWriteMode
	// Time是自报签署时间，不是可信时间戳；零值取当前时间
	Time     time.Time
	Provider SignatureProvider
	// Lock保护签名列表，禁止后续追加签署
	Lock bool
	// ExistingVerifyOptions用于追加前与写出后回验，例如提供其他旧裸签名的证书
	ExistingVerifyOptions []SignatureVerifyOption
	// ReaderOptions用于打开加密包；Encryption为空时继承阅读器的加密策略
	ReaderOptions []ReaderOption
	Encryption    *EncryptionOptions
	progress      editorProgress
}

// SignPackage 签署最终OFD包，失败时返回nil且不修改输入
// 入参: data 最终包字节, options 签署选项
// 返回: []byte 签署后的完整包, error 错误信息
func SignPackage(data []byte, options SignatureWriteOptions) ([]byte, error) {
	r, err := NewReader(bytes.NewReader(data), int64(len(data)), options.ReaderOptions...)
	if err != nil {
		return nil, err
	}
	return signatureWriteOutput(r, options)
}

// SignEditorTo 将编辑器序列化为最终文件后签署，保留或显式覆盖加密策略
// 不修改编辑器状态；OnWriteProgress可在准备、签署和加密阶段取消，准备失败不写入目标
// 输出目标不得覆盖源文件；目标自身写入失败时调用方应丢弃部分输出
// 入参: writer 输出目标, editor 编辑器, options 签署选项
// 返回: int64 写出字节数, error 错误信息
func SignEditorTo(writer io.Writer, editor *Editor, options SignatureWriteOptions) (int64, error) {
	if writer == nil || editor == nil {
		return 0, fmt.Errorf("nil signature editor or writer")
	}
	if options.Encryption == nil && editor.encryption != nil && editor.encryption.options == nil {
		return 0, ErrEncryptionPolicyRequired
	}
	var plain bytes.Buffer
	defer func() { clear(plain.Bytes()) }()
	if _, err := editor.writePlaintext(&plain); err != nil {
		return 0, err
	}
	r, err := NewReader(bytes.NewReader(plain.Bytes()), int64(plain.Len()))
	if err != nil {
		return 0, err
	}
	r.encryption = editor.encryption
	options.progress = editorProgress(editor.OnWriteProgress)
	return r.SignTo(writer, options)
}

// SignTo 从阅读器包内容创建独立快照，完成签署和回验后写出
// 不采用可变的Doc/OFD模型；调用方应先将Editor写出为最终包
// 准备失败不写入writer；writer自身失败可能已写入部分输出，不保证目标原子替换
// 入参: writer 独立的输出目标，不得覆盖源文件, options 签署选项
// 返回: int64 写出字节数, error 错误信息
func (r *Reader) SignTo(writer io.Writer, options SignatureWriteOptions) (int64, error) {
	if writer == nil {
		return 0, fmt.Errorf("nil signature writer")
	}
	data, err := signatureWriteOutput(r, options)
	if err != nil {
		return 0, err
	}
	n, err := writer.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return int64(n), err
}

// signatureWriteOutput 签署后恢复加密策略，禁止加密来源静默输出明文
// 入参: r 阅读器, options 签署选项
// 返回: []byte 输出包, error 错误信息
func signatureWriteOutput(r *Reader, options SignatureWriteOptions) ([]byte, error) {
	if r == nil {
		return nil, fmt.Errorf("nil signature reader")
	}
	encryption := options.Encryption
	if encryption == nil && r.encryption != nil {
		encryption = r.encryption.options
		if encryption == nil {
			return nil, ErrEncryptionPolicyRequired
		}
	}
	if encryption != nil {
		if err := validateEncryptionOptions(*encryption); err != nil {
			return nil, err
		}
	}
	parts, err := signatureWriteParts(r)
	if err != nil {
		return nil, err
	}
	if encryption != nil {
		defer func() {
			for _, data := range parts {
				clear(data)
			}
		}()
	}
	if err := options.progress.report("sign", 0, 1); err != nil {
		return nil, err
	}
	data, err := signatureWritePackage(parts, options)
	if err != nil {
		return nil, err
	}
	if err := options.progress.report("sign", 1, 1); err != nil {
		clear(data)
		return nil, err
	}
	if encryption != nil {
		defer clear(data)
		if options.progress != nil {
			signed, err := NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				return nil, err
			}
			files, err := signatureWriteParts(signed)
			if err != nil {
				return nil, err
			}
			defer func() {
				for _, data := range files {
					clear(data)
				}
			}()
			return encryptPackageParts(files, *encryption, options.progress)
		}
		return EncryptPackage(data, *encryption)
	}
	return data, nil
}

// signatureWriteParts 使用阅读器统一路径规则读取独立包快照
// 入参: r 阅读器
// 返回: map[string][]byte 包文件, error 错误信息
func signatureWriteParts(r *Reader) (map[string][]byte, error) {
	if r == nil {
		return nil, fmt.Errorf("nil signature reader")
	}
	if err := r.indexPackage(); err != nil {
		return nil, err
	}
	return r.packageData()
}

// signatureWritePackage 基于未重新编码的文件字节创建签名
// 签名目录采用Sign_N兼容OFDRW容器，标识使用独立的sN签章域
// 入参: parts 独立包文件, options 签署选项
// 返回: []byte 完整包, error 错误信息
func signatureWritePackage(parts map[string][]byte, options SignatureWriteOptions) ([]byte, error) {
	if options.Mode != SignatureAppend && options.Mode != SignatureReplace {
		return nil, fmt.Errorf("unsupported signature write mode")
	}
	r := &Reader{files: parts}
	if err := r.initRoot(); err != nil {
		return nil, err
	}
	if len(r.OFD.DocBody) != 1 {
		return nil, fmt.Errorf("signature writing requires exactly one document")
	}
	doc, err := r.Doc()
	if err != nil {
		return nil, err
	}
	if options.Time.IsZero() {
		options.Time = time.Now()
	}
	options.Time = options.Time.UTC().Truncate(time.Second)
	identity, err := signatureWriteIdentity(options)
	if err != nil {
		return nil, err
	}
	stamps, err := signatureWriteStamps(r, options.Stamps, len(options.Seal) != 0)
	if err != nil {
		return nil, err
	}
	var list Signatures
	listPath := r.ResPath(doc.Signatures)
	if doc.Signatures != "" {
		data, ok := parts[listPath]
		if !ok {
			return nil, fmt.Errorf("signature list not found: %s", listPath)
		}
		if err := xml.Unmarshal(data, &list); err != nil {
			return nil, err
		}
	}
	oldCount := len(list.List)
	verifyOptions := append([]SignatureVerifyOption(nil), options.ExistingVerifyOptions...)
	verifyOptions = append(verifyOptions, WithSignatureCert(identity.cert.Raw))
	if options.Mode == SignatureAppend && oldCount > 0 {
		reports, err := r.VerifySignatures(verifyOptions...)
		if err != nil {
			return nil, err
		}
		ids := make(map[string]bool)
		for _, report := range reports {
			if !report.Valid || report.StampPositionError != "" || ids[report.ID] || report.ID == "" {
				return nil, fmt.Errorf("existing signature %s is invalid: %s", report.ID, report.Error)
			}
			ids[report.ID] = true
			for _, ref := range report.References {
				if ref.Path == listPath {
					return nil, fmt.Errorf("signature %s protects the signature list", report.ID)
				}
			}
		}
	}
	if doc.Signatures == "" {
		listPath = signatureWriteAvailable(parts, path.Join(r.RootDir, "Signs", "Signatures.xml"))
		root, err := parseEditorXML(parts["OFD.xml"])
		if err != nil {
			return nil, err
		}
		body := root.child("DocBody")
		if body == nil {
			return nil, fmt.Errorf("missing document body")
		}
		parts["OFD.xml"] = editorXMLSetText(parts["OFD.xml"], body, [][2]string{{"Signatures", "/" + listPath}})
	}
	if options.Mode == SignatureReplace {
		list.List = nil
		oldCount = 0
	}
	id, stamps, err := signatureWriteIDs(&list, listPath, parts, stamps)
	if err != nil {
		return nil, err
	}
	directory := ""
	for n := 1; ; n++ {
		directory = path.Join(path.Dir(listPath), "Sign_"+strconv.Itoa(n))
		occupied := false
		for name := range parts {
			if strings.EqualFold(name, directory) || strings.HasPrefix(strings.ToLower(name), strings.ToLower(directory)+"/") {
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
	parts[listPath], err = signatureWriteXML("Signatures", list)
	if err != nil {
		return nil, err
	}
	refs := SignatureReferences{CheckMethod: identity.digest}
	names := make([]string, 0, len(parts))
	for name := range parts {
		names = append(names, name)
	}
	slices.Sort(names)
	for index, name := range names {
		if err := options.progress.report("signature-references", index, len(names)); err != nil {
			return nil, err
		}
		if name == listPath && !options.Lock {
			continue
		}
		digest, err := signatureDigest(identity.digest, parts[name])
		if err != nil {
			return nil, err
		}
		refs.Reference = append(refs.Reference, SignatureReference{FileRef: "/" + name, CheckValue: base64.StdEncoding.EncodeToString(digest)})
	}
	if err := options.progress.report("signature-references", len(names), len(names)); err != nil {
		return nil, err
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
		return nil, err
	}
	parts[valuePath], err = identity.sign(parts[sigPath], "/"+sigPath, options)
	if err != nil {
		return nil, err
	}
	output, err := signatureWriteZIP(parts)
	if err != nil {
		return nil, err
	}
	check, err := NewReader(bytes.NewReader(output), int64(len(output)))
	if err != nil {
		return nil, err
	}
	reports, err := check.VerifySignatures(verifyOptions...)
	if err != nil {
		return nil, err
	}
	if len(reports) != oldCount+1 {
		return nil, fmt.Errorf("signature verification count mismatch")
	}
	for _, report := range reports {
		if !report.Valid || report.StampPositionError != "" {
			return nil, fmt.Errorf("written signature %s failed verification: %s", report.ID, report.Error)
		}
	}
	return output, nil
}

// signatureWriteIDs 为签名和全部外观分配同一签章域的标识，不修改文档图元编号
// 入参: list 签名列表, listPath 列表路径, parts 包文件, stamps 外观
// 返回: string 签名标识, []SignatureStamp 外观, error 错误信息
func signatureWriteIDs(list *Signatures, listPath string, parts map[string][]byte, stamps []SignatureStamp) (string, []SignatureStamp, error) {
	used := make(map[string]bool)
	var maximum uint64
	track := func(id string) {
		n, err := strconv.ParseUint(strings.TrimPrefix(id, "s"), 10, 64)
		if err == nil && n > maximum {
			maximum = n
		}
	}
	track(list.MaxSignID)
	add := func(id string) error {
		if !signatureWriteIDValid(id) || used[id] {
			return fmt.Errorf("invalid or duplicate signature ID: %s", id)
		}
		used[id] = true
		track(id)
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
	next := func() (string, error) {
		if maximum == ^uint64(0) {
			return "", fmt.Errorf("signature ID exhausted")
		}
		maximum++
		return "s" + strconv.FormatUint(maximum, 10), nil
	}
	id, err := next()
	if err != nil {
		return "", nil, err
	}
	for i := range stamps {
		if stamps[i].ID == "" {
			stamps[i].ID, err = next()
			if err != nil {
				return "", nil, err
			}
		}
	}
	list.MaxSignID = "s" + strconv.FormatUint(maximum, 10)
	return id, stamps, nil
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

// signatureWriteXML 编码带OFD命名空间的签名文件
// 入参: name 根节点名称, value 内容
// 返回: []byte XML, error 错误信息
func signatureWriteXML(name string, value any) ([]byte, error) {
	var out bytes.Buffer
	out.WriteString(xml.Header)
	err := xml.NewEncoder(&out).EncodeElement(value, xml.StartElement{Name: xml.Name{Space: ofdNamespace, Local: name}})
	return out.Bytes(), err
}

// signatureWriteAvailable 分配不覆盖原文件的路径
// 入参: parts 原包文件, name 候选路径
// 返回: string 可用路径
func signatureWriteAvailable(parts map[string][]byte, name string) string {
	original := name
	for i := 1; ; i++ {
		found := false
		for existing := range parts {
			if strings.EqualFold(existing, name) {
				found = true
				break
			}
		}
		if !found {
			return name
		}
		name = path.Join(path.Dir(original), strconv.Itoa(i)+"_"+path.Base(original))
	}
}

// signatureWriteZIP 写出完整包，不重新编码受保护文件
// 入参: parts 文件集合
// 返回: []byte 包字节, error 错误信息
func signatureWriteZIP(parts map[string][]byte) ([]byte, error) {
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	names := make([]string, 0, len(parts))
	for name := range parts {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		f, err := w.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(parts[name]); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
