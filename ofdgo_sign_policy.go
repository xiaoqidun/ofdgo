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
	"crypto"
	"encoding/xml"
	"errors"
	"fmt"
	"maps"
	"path"
	"sort"
	"strings"
)

// SignaturePolicy 签名验签策略
// 空列表不限制已支持的算法或版本，RequiredFiles使用包内绝对路径
// RequireDocumentCoverage检查OFD.xml、文档目录、选中版本清单及已知OFD引用可达的跨目录文件
// 仅豁免签名列表以及当前与后续签名位于独立签名子目录或标准签名路径的描述、签名值和外置印章
// 此范围不覆盖其他多文档条目，自定义扩展隐含引用应使用RequiredFiles补充
type SignaturePolicy struct {
	OFDVersions             []string
	SESVersions             []int
	SignedDataVersions      []int
	DigestMethods           []string
	SignatureMethods        []string
	RejectWeakAlgorithms    bool
	RequiredFiles           []string
	RequireDocumentCoverage bool
	RequireTimestamp        bool
	RequireRevocation       bool
}

// signaturePolicyRejection 区分策略拒绝与解析失败
type signaturePolicyRejection struct{ message string }

// signatureCoverageNode 收集OFD结构中的包内路径引用
type signatureCoverageNode struct {
	XMLName  xml.Name
	Attrs    []xml.Attr              `xml:",any,attr"`
	Text     string                  `xml:",chardata"`
	Children []signatureCoverageNode `xml:",any"`
}

// WithSignaturePolicy 设置签名策略
// 入参: policy 签名策略
// 返回: SignatureVerifyOption 验签选项
func WithSignaturePolicy(policy SignaturePolicy) SignatureVerifyOption {
	policy.OFDVersions = append([]string(nil), policy.OFDVersions...)
	policy.SESVersions = append([]int(nil), policy.SESVersions...)
	policy.SignedDataVersions = append([]int(nil), policy.SignedDataVersions...)
	policy.DigestMethods = append([]string(nil), policy.DigestMethods...)
	policy.SignatureMethods = append([]string(nil), policy.SignatureMethods...)
	policy.RequiredFiles = append([]string(nil), policy.RequiredFiles...)
	return func(o *signatureVerifyOptions) { o.Policy = &policy }
}

// rejectSignaturePolicy 构造可识别的策略拒绝错误
// 入参: format 消息模板, values 模板参数
// 返回: error 策略错误
func rejectSignaturePolicy(format string, values ...any) error {
	return signaturePolicyRejection{message: fmt.Sprintf(format, values...)}
}

// signatureAlgorithmPolicy 检查实际签名和摘要算法
// 入参: p 策略, method 签名算法, digest 摘要算法
// 返回: error 策略拒绝原因
func signatureAlgorithmPolicy(p *SignaturePolicy, method, digest string) error {
	if p == nil {
		return nil
	}
	method = strings.TrimSpace(method)
	if len(p.DigestMethods) != 0 {
		allowed := false
		for _, v := range p.DigestMethods {
			allowed = allowed || signatureDigestEquivalent(v, digest)
		}
		if !allowed {
			return rejectSignaturePolicy("digest algorithm rejected: %s", digest)
		}
	}
	if method != "" && len(p.SignatureMethods) != 0 {
		allowed := false
		for _, v := range p.SignatureMethods {
			allowed = allowed || signatureAlgorithmEquivalent(v, method, digest)
		}
		if !allowed {
			return rejectSignaturePolicy("signature algorithm rejected: %s", method)
		}
	}
	if p.RejectWeakAlgorithms {
		h, ok := signatureDigestHash(digest)
		if ok && (h == crypto.MD5 || h == crypto.SHA1) {
			return rejectSignaturePolicy("weak digest algorithm: %s", digest)
		}
		if method != "" {
			h, err := signatureMethodHash(method, digest)
			if !isSM2SignatureMethod(method) && err == nil && (h == crypto.MD5 || h == crypto.SHA1) {
				return rejectSignaturePolicy("weak signature algorithm: %s", method)
			}
		}
	}
	return nil
}

// signatureStringAllowed 检查值是否在允许列表中
// 入参: value 待检查值, values 允许列表
// 返回: bool 是否允许
func signatureStringAllowed(value string, values []string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

// signatureDigestEquivalent 比较摘要算法别名与OID
// 入参: a 第一算法, b 第二算法
// 返回: bool 是否为相同算法
func signatureDigestEquivalent(a, b string) bool {
	if isSM3DigestMethod(a) && isSM3DigestMethod(b) {
		return true
	}
	x, ok := signatureDigestHash(a)
	y, ok2 := signatureDigestHash(b)
	return ok && ok2 && x == y
}

// signatureAlgorithmEquivalent 比较策略允许的签名算法与实际签名算法是否匹配
// 入参: a 策略允许算法, b 实际签名算法, digest 实际摘要算法
// 返回: bool 是否匹配
func signatureAlgorithmEquivalent(a, b, digest string) bool {
	if isSM2SignatureMethod(a) && isSM2SignatureMethod(b) {
		return true
	}
	if !(isRSASignatureMethod(a) && isRSASignatureMethod(b) || isECDSASignatureMethod(a) && isECDSASignatureMethod(b)) {
		return false
	}
	x, errA := signatureMethodHash(a, "")
	y, errB := signatureMethodHash(b, "")
	if errA != nil {
		return true
	}
	if errB == nil {
		return x == y
	}
	d, ok := signatureDigestHash(digest)
	return ok && x == d
}

// signatureSignerDigest 验证SignerInfo中的摘要与签名算法一致
// 入参: method 签名算法, digest 摘要算法
// 返回: error 不一致时的错误
func signatureSignerDigest(method, digest string) error {
	if isSM2SignatureMethod(method) {
		if !isSM3DigestMethod(digest) {
			return fmt.Errorf("SM2 requires SM3 signer digest")
		}
		return nil
	}
	h, err := signatureMethodHash(method, digest)
	d, ok := signatureDigestHash(digest)
	if err != nil || !ok || h != d {
		return fmt.Errorf("signer digest and signature algorithm mismatch")
	}
	return nil
}

// canExcludeSubsequentSignature 判断后续签名描述文件是否允许从覆盖范围中豁免
// 入参: listPath 签名列表路径, sigPath 后续签名描述路径, docRootDir 文档根目录
// 返回: bool 是否允许豁免
func canExcludeSubsequentSignature(listPath, sigPath, docRootDir string) bool {
	if inDocumentContentSubdir(sigPath, docRootDir) {
		return false
	}
	if isSignatureSubdir(path.Dir(listPath), docRootDir) {
		return inSignatureSubdir(sigPath, listPath, sigPath, docRootDir, true)
	}
	return true
}

// canExcludeSignatureValue 判断签名值路径是否允许从文档覆盖扫描中豁免
// 入参: listPath 签名列表路径, sigPath 签名描述路径, valLoc 签名值引用路径, docRootDir 文档根目录, docRequired 文档必需结构与资源集合, after 是否为后续签名
// 返回: string 规范化签名值路径, bool 是否允许豁免
func canExcludeSignatureValue(listPath, sigPath, valLoc, docRootDir string, docRequired map[string]bool, after bool) (string, bool) {
	if strings.TrimSpace(valLoc) == "" {
		return "", false
	}
	valPath := cleanPackagePath(signatureRefPath(sigPath, valLoc))
	ext := strings.ToLower(path.Ext(valPath))
	if !validPackagePath(valPath) || valPath == "OFD.xml" || docRequired[valPath] || ext == ".xml" || ext == ".pdf" || ext == ".ofd" || inDocumentContentSubdir(valPath, docRootDir) {
		return "", false
	}
	if inSignatureSubdir(valPath, listPath, sigPath, docRootDir, after) {
		return valPath, true
	}
	if after && (isSignatureSubdir(path.Dir(listPath), docRootDir) || isSignatureSubdir(path.Dir(sigPath), docRootDir)) {
		return "", false
	}
	switch ext {
	case ".dat", ".sig", ".p7s", ".bin", ".ber", ".der":
		return valPath, true
	default:
		return "", false
	}
}

// canExcludeSignatureSeal 判断签名引用的外置印章路径是否允许从文档目录扫描中豁免
// 入参: listPath 签名列表路径, sigPath 签名描述路径, sealLoc 印章引用路径, docRootDir 文档根目录, docRequired 文档必需结构与资源集合, after 是否为后续签名
// 返回: string 规范化印章路径, bool 是否允许豁免
func canExcludeSignatureSeal(listPath, sigPath, sealLoc, docRootDir string, docRequired map[string]bool, after bool) (string, bool) {
	if strings.TrimSpace(sealLoc) == "" {
		return "", false
	}
	sealPath := cleanPackagePath(signatureRefPath(sigPath, sealLoc))
	ext := strings.ToLower(path.Ext(sealPath))
	if !validPackagePath(sealPath) || sealPath == "OFD.xml" || docRequired[sealPath] || ext == ".xml" || ext == ".pdf" || inDocumentContentSubdir(sealPath, docRootDir) {
		return "", false
	}
	if inSignatureSubdir(sealPath, listPath, sigPath, docRootDir, after) {
		return sealPath, true
	}
	if after && (isSignatureSubdir(path.Dir(listPath), docRootDir) || isSignatureSubdir(path.Dir(sigPath), docRootDir)) {
		return "", false
	}
	if ext == ".esl" || ext == ".dat" {
		return sealPath, true
	}
	return "", false
}

// inSignatureSubdir 判断目标路径是否位于独立于文档根目录的受控签名子目录内
// 入参: target 目标路径, listPath 签名列表路径, sigPath 签名描述路径, docRootDir 文档根目录, after 是否为后续签名
// 返回: bool 是否位于独立签名子目录
func inSignatureSubdir(target, listPath, sigPath, docRootDir string, after bool) bool {
	listDir := path.Dir(listPath)
	if isSignatureSubdir(listDir, docRootDir) && strings.HasPrefix(target, listDir+"/") {
		return true
	}
	sigDir := path.Dir(sigPath)
	if !isSignatureSubdir(sigDir, docRootDir) || !strings.HasPrefix(target, sigDir+"/") {
		return false
	}
	if !after {
		return true
	}
	if isSignatureSubdir(listDir, docRootDir) {
		return false
	}
	base := strings.ToLower(path.Base(sigDir))
	return base == "sign" || base == "signs" || base == "signatures" || strings.HasPrefix(base, "sign_") || strings.HasPrefix(base, "sig_") || strings.HasPrefix(base, "signature_")
}

// isSignatureSubdir 判断目录是否为独立于文档根及业务资源子树的目录
// 入参: dir 待判断目录, docRootDir 文档根目录
// 返回: bool 是否为独立签名子目录
func isSignatureSubdir(dir, docRootDir string) bool {
	if dir == "" || dir == "." || dir == docRootDir || strings.HasPrefix(docRootDir+"/", dir+"/") {
		return false
	}
	return !inDocumentContentSubdir(dir+"/x", docRootDir)
}

// inDocumentContentSubdir 判断路径是否位于文档标准正文、资源或版本子目录内
// 入参: target 包内路径, docRootDir 文档根目录
// 返回: bool 是否位于业务子目录
func inDocumentContentSubdir(target, docRootDir string) bool {
	rel := target
	if docRootDir != "." {
		if !strings.HasPrefix(target, docRootDir+"/") {
			return false
		}
		rel = strings.TrimPrefix(target, docRootDir+"/")
	}
	first, _, found := strings.Cut(rel, "/")
	if !found {
		return false
	}
	switch strings.ToLower(first) {
	case "pages", "res", "attachs", "annots", "annotations", "customtags", "extensions", "temps", "versions", "tpls", "tags", "exts":
		return true
	default:
		return false
	}
}

// Error 返回策略拒绝原因
// 返回: string 错误信息
func (e signaturePolicyRejection) Error() string { return e.message }

// appendSignaturePolicyError 追加签名策略错误并避免覆盖既有原因
// 入参: msg 策略错误文本
func (report *SignatureVerifyReport) appendSignaturePolicyError(msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	report.PolicyChecked = true
	report.PolicyOK = false
	if report.PolicyError == "" {
		report.PolicyError = msg
		return
	}
	for _, part := range strings.Split(report.PolicyError, "; ") {
		if part == msg {
			return
		}
	}
	report.PolicyError += "; " + msg
}

// applySignaturePolicyError 将实际算法策略拒绝记入报告
// 入参: err 验签错误
func (report *SignatureVerifyReport) applySignaturePolicyError(err error) {
	var rejection signaturePolicyRejection
	if errors.As(err, &rejection) {
		report.appendSignaturePolicyError(rejection.Error())
	}
}

// applySignaturePolicy 应用版本和算法限制
// 入参: o 验签选项, ofdVersion OFD版本, sesVersion SES版本, method 签名算法, digest 摘要算法
func (report *SignatureVerifyReport) applySignaturePolicy(o *signatureVerifyOptions, ofdVersion string, sesVersion int, method, digest string) {
	if o.Policy == nil {
		return
	}
	if !report.PolicyChecked {
		report.PolicyChecked, report.PolicyOK = true, true
	}
	p := o.Policy
	if len(p.OFDVersions) != 0 && !signatureStringAllowed(ofdVersion, p.OFDVersions) {
		report.appendSignaturePolicyError(fmt.Sprintf("OFD version rejected: %s", ofdVersion))
	}
	if sesVersion != 0 && len(p.SESVersions) != 0 {
		allowed := false
		for _, v := range p.SESVersions {
			allowed = allowed || v == sesVersion
		}
		if !allowed {
			report.appendSignaturePolicyError(fmt.Sprintf("SES version rejected: %d", sesVersion))
		}
	}
	if err := signatureAlgorithmPolicy(p, method, digest); err != nil {
		report.appendSignaturePolicyError(err.Error())
	}
}

// applySignatureCoverage 区分引用摘要通过和文档覆盖完整
// 入参: ctx 取消上下文, report 验签报告, listPath 签名列表路径, sigList 已解析签名列表, sigFile 当前签名描述, o 验签选项
func (r *Reader) applySignatureCoverage(ctx context.Context, report *SignatureVerifyReport, listPath string, sigList *Signatures, sigFile *SignatureFile, o *signatureVerifyOptions) {
	if ctx == nil {
		ctx = context.Background()
	}
	covered := make(map[string]bool)
	failed := make(map[string]bool)
	for _, ref := range report.References {
		p := cleanPackagePath(ref.Path)
		if ref.Checked && ref.OK && ref.Error == "" {
			if !failed[p] {
				covered[p] = true
			}
		} else {
			failed[p] = true
			delete(covered, p)
		}
	}
	for name := range covered {
		report.CoveredFiles = append(report.CoveredFiles, name)
	}
	sort.Strings(report.CoveredFiles)
	if o.Policy == nil || !o.Policy.RequireDocumentCoverage && len(o.Policy.RequiredFiles) == 0 {
		return
	}
	report.CoverageChecked, report.CoverageOK = true, true
	if err := ctx.Err(); err != nil {
		report.CoverageOK, report.CoverageError = false, err.Error()
		return
	}
	required := make(map[string]bool)
	for _, name := range o.Policy.RequiredFiles {
		required[cleanPackagePath(name)] = true
	}
	if o.Policy.RequireDocumentCoverage {
		root := path.Dir(cleanPackagePath(o.DocRoot))
		cleanList := cleanPackagePath(listPath)
		contentRequired := map[string]bool{"OFD.xml": true}
		if r.ofd != nil && o.DocIndex >= 0 && o.DocIndex < len(r.ofd.DocBody) {
			if cover := strings.TrimSpace(r.ofd.DocBody[o.DocIndex].DocInfo.Cover); cover != "" {
				coverPath := cleanPackagePath(resolveResourcePath("OFD.xml", "", cover))
				if validPackagePath(coverPath) {
					contentRequired[coverPath] = true
				}
			}
		}
		if err := r.signatureCoverageReferences(ctx, o.DocRoot, contentRequired); err != nil {
			report.CoverageOK, report.CoverageError = false, err.Error()
		}
		docRequired := make(map[string]bool, len(contentRequired))
		for item := range contentRequired {
			docRequired[item] = true
		}
		if r.versionInfo != nil && strings.TrimSpace(r.versionInfo.Location) != "" {
			versionPath := cleanPackagePath(r.versionInfo.Location)
			if validPackagePath(versionPath) {
				contentRequired[versionPath] = true
				docRequired[versionPath] = true
				if err := r.signatureCoverageReferences(ctx, versionPath, docRequired); err != nil && report.CoverageError == "" {
					report.CoverageOK, report.CoverageError = false, err.Error()
				}
			}
		} else if r.ofd != nil && o.DocIndex >= 0 && o.DocIndex < len(r.ofd.DocBody) && r.ofd.DocBody[o.DocIndex].versioned {
			versions, err := r.DocumentVersionsContext(ctx)
			if err != nil {
				report.CoverageOK = false
				if report.CoverageError == "" {
					report.CoverageError = err.Error()
				}
			} else {
				for _, ver := range versions {
					verPath := cleanPackagePath(ver.Location)
					if validPackagePath(verPath) {
						docRequired[verPath] = true
						if err := r.signatureCoverageReferences(ctx, verPath, docRequired); err != nil && report.CoverageError == "" {
							report.CoverageOK, report.CoverageError = false, err.Error()
						}
					}
				}
			}
		}
		excluded := make(map[string]bool)
		if !contentRequired[cleanList] {
			excluded[cleanList] = true
		}
		loadSigFile := func(target string) (*SignatureFile, error) {
			if o.sigFileCache != nil {
				if cached, ok := o.sigFileCache[target]; ok {
					return cached, nil
				}
			}
			raw, err := r.readFileViewContext(ctx, target)
			if err != nil {
				return nil, err
			}
			parsed, err := parseSignatureFile(raw)
			if err != nil {
				return nil, err
			}
			if o.sigFileCache != nil {
				o.sigFileCache[target] = parsed
			}
			return parsed, nil
		}
		name := cleanPackagePath(signatureRefPath(listPath, report.BaseLoc))
		file := sigFile
		if file == nil {
			var err error
			file, err = loadSigFile(name)
			if err != nil {
				report.CoverageOK = false
				report.CoverageError = err.Error()
				return
			}
		} else if o.sigFileCache != nil {
			o.sigFileCache[name] = file
		}
		if !contentRequired[name] {
			excluded[name] = true
		}
		if valPath, ok := canExcludeSignatureValue(cleanList, name, file.SignedValue, root, contentRequired, false); ok {
			excluded[valPath] = true
		}
		if sealPath, ok := canExcludeSignatureSeal(cleanList, name, file.SignedInfo.Seal.BaseLoc, root, contentRequired, false); ok {
			excluded[sealPath] = true
		}
		list := sigList
		if list == nil {
			if listData, err := r.readFileViewContext(ctx, cleanList); err == nil {
				var parsed Signatures
				if xml.Unmarshal(listData, &parsed) == nil {
					list = &parsed
				}
			}
		}
		if list != nil {
			var versionScope *versionSignatureScope
			if r.versionInfo != nil {
				versionScope, _ = r.buildVersionSignatureScope(ctx, cleanList, *list)
			}
			after := false
			for _, entry := range list.List {
				if err := ctx.Err(); err != nil {
					report.CoverageOK, report.CoverageError = false, err.Error()
					return
				}
				entryPath := cleanPackagePath(signatureRefPath(listPath, entry.BaseLoc))
				if versionScope != nil && !r.versionSignatureMatches(ctx, versionScope, entry) {
					if validPackagePath(entryPath) && !contentRequired[entryPath] {
						excluded[entryPath] = true
						if entryFile, err := loadSigFile(entryPath); err == nil {
							if valPath := cleanPackagePath(signatureRefPath(entryPath, entryFile.SignedValue)); validPackagePath(valPath) && !contentRequired[valPath] {
								excluded[valPath] = true
							}
							if sealPath := cleanPackagePath(signatureRefPath(entryPath, entryFile.SignedInfo.Seal.BaseLoc)); validPackagePath(sealPath) && !contentRequired[sealPath] {
								excluded[sealPath] = true
							}
						}
					}
					continue
				}
				if !after {
					if entryPath == name && (report.ID == "" || entry.ID == report.ID) {
						after = true
					}
					continue
				}
				if !validPackagePath(entryPath) || entryPath == "OFD.xml" || contentRequired[entryPath] || !strings.EqualFold(path.Ext(entryPath), ".xml") {
					continue
				}
				if entryFile, err := loadSigFile(entryPath); err == nil && canExcludeSubsequentSignature(cleanList, entryPath, root) {
					if !docRequired[entryPath] || inSignatureSubdir(entryPath, cleanList, entryPath, root, true) {
						excluded[entryPath] = true
					}
					if valPath, ok := canExcludeSignatureValue(cleanList, entryPath, entryFile.SignedValue, root, contentRequired, true); ok {
						if !docRequired[valPath] || inSignatureSubdir(valPath, cleanList, entryPath, root, true) {
							excluded[valPath] = true
						}
					}
					if sealPath, ok := canExcludeSignatureSeal(cleanList, entryPath, entryFile.SignedInfo.Seal.BaseLoc, root, contentRequired, true); ok {
						if !docRequired[sealPath] || inSignatureSubdir(sealPath, cleanList, entryPath, root, true) {
							excluded[sealPath] = true
						}
					}
				}
			}
		}
		for item := range docRequired {
			if !excluded[item] || contentRequired[item] {
				required[item] = true
			}
		}
		if r.versionInfo == nil {
			otherOwned := make(map[string]bool)
			multiDoc := r.ofd != nil && len(r.ofd.DocBody) > 1
			if multiDoc {
				var otherIndexes []int
				for idx := range r.ofd.DocBody {
					if idx != o.DocIndex {
						otherIndexes = append(otherIndexes, idx)
					}
				}
				if deps, _, err := r.documentDependencies(ctx, otherIndexes...); err == nil {
					maps.Copy(otherOwned, deps)
				}
			}
			include := func(item string) {
				clean := cleanPackagePath(item)
				if clean == "" || clean == "." || strings.HasSuffix(item, "/") || excluded[clean] || otherOwned[clean] {
					return
				}
				if !multiDoc || root == "." || strings.HasPrefix(clean, root+"/") {
					required[clean] = true
				}
			}
			for item, fileInfo := range r.fileIndex {
				if !fileInfo.FileInfo().IsDir() {
					include(item)
				}
			}
			for item := range r.files {
				include(item)
			}
			if src := r.encryptedSource(); src != nil {
				for _, item := range src.names() {
					include(item)
				}
			}
		}
	}
	for name := range required {
		if !covered[name] {
			report.UncoveredFiles = append(report.UncoveredFiles, name)
		}
	}
	sort.Strings(report.UncoveredFiles)
	report.CoverageOK = report.CoverageOK && len(report.UncoveredFiles) == 0
}

// signatureCoverageReferences 收集标准OFD引用的传递闭包且包含跨文档资源与版本清单
// 入参: ctx 取消上下文, root 文档或版本入口, required 待补充的必需文件集合
// 返回: error 引用文件无法读取、解析或取消时的错误
func (r *Reader) signatureCoverageReferences(ctx context.Context, root string, required map[string]bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	type pendingResFile struct {
		id        string
		resolved  string
		structure bool
	}
	queue := []string{cleanPackagePath(root)}
	seen := make(map[string]bool)
	usedIDs := make(map[string]bool)
	resDeps := make(map[string][]string)
	var pending []pendingResFile
	recordID := func(ownerID, raw string) {
		id := editorResourceID(raw)
		if id == "" {
			return
		}
		if ownerID == "" {
			usedIDs[id] = true
		} else {
			resDeps[ownerID] = append(resDeps[ownerID], id)
		}
	}
	for {
		for len(queue) != 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			name := queue[0]
			queue = queue[1:]
			if seen[name] {
				continue
			}
			seen[name], required[name] = true, true
			data, err := r.readFileViewContext(ctx, name)
			if err != nil {
				return err
			}
			trimmed := bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
			if !bytes.HasPrefix(bytes.TrimSpace(trimmed), []byte("<")) {
				continue
			}
			var node signatureCoverageNode
			if err := xml.Unmarshal(trimmed, &node); err != nil {
				return fmt.Errorf("coverage XML %s: %w", name, err)
			}
			isRes := node.XMLName.Local == "Res"
			base := ""
			if isRes {
				for _, attr := range node.Attrs {
					if attr.Name.Local == "BaseLoc" {
						base = attr.Value
					}
				}
			}
			add := func(loc, resItemID string, resource, structure bool) {
				if strings.TrimSpace(loc) == "" {
					return
				}
				resBase := ""
				if resource {
					resBase = base
				}
				resolved := cleanPackagePath(resolveResourcePath(name, resBase, loc))
				if !validPackagePath(resolved) {
					return
				}
				if resource && resItemID != "" {
					pending = append(pending, pendingResFile{id: resItemID, resolved: resolved, structure: structure})
					return
				}
				required[resolved] = true
				if structure && !seen[resolved] {
					queue = append(queue, resolved)
				}
			}
			var walk func(signatureCoverageNode, string, string, int)
			walk = func(current signatureCoverageNode, parent, resItemID string, depth int) {
				kind := current.XMLName.Local
				if !ofdXMLChild(node.XMLName, current.XMLName) || classifyOFDNamespace(current.XMLName.Space) == ofdXMLUnknown && current.XMLName.Space != node.XMLName.Space || kind == "Signatures" || kind == "Signature" || kind == "Data" && parent == "Extension" {
					return
				}
				if isRes && depth == 2 && editorResourceElement(parent) == kind {
					for _, attr := range current.Attrs {
						if attr.Name.Space == "" && attr.Name.Local == "ID" {
							resItemID = editorResourceID(attr.Value)
						}
					}
				}
				for _, attr := range current.Attrs {
					if attr.Name.Space != "" {
						continue
					}
					if attr.Name.Local != "ID" && editorResourceReferenceKind(attr.Name.Local, isRes) == editorReferenceID {
						recordID(resItemID, attr.Value)
					}
					if attr.Name.Local == "BaseLoc" && kind != "Res" || attr.Name.Local == "Link" && kind == "DrawParam" {
						add(attr.Value, resItemID, isRes, true)
					} else if attr.Name.Local == "Profile" && kind == "ColorSpace" {
						add(attr.Value, resItemID, true, false)
					}
				}
				if editorResourceReferenceKind(kind, isRes) == editorReferenceID {
					recordID(resItemID, current.Text)
				}
				switch kind {
				case "PublicRes", "DocumentRes", "PageRes", "Annotations", "Attachments", "CustomTags", "Extensions":
					add(current.Text, "", false, true)
				case "DocRoot":
					if node.XMLName.Local == "DocVersion" {
						add(current.Text, "", false, true)
					}
				case "File":
					if parent == "FileList" && node.XMLName.Local == "DocVersion" {
						add(current.Text, "", false, false)
					}
				case "FileLoc":
					add(current.Text, "", false, parent == "Page" && node.XMLName.Local == "Annotations")
				case "SchemaLoc", "ExtendData":
					add(current.Text, "", false, false)
				case "FontFile", "MediaFile", "Profile":
					add(current.Text, resItemID, true, false)
				}
				for _, child := range current.Children {
					walk(child, kind, resItemID, depth+1)
				}
			}
			walk(node, "", "", 0)
		}
		expanded := true
		for expanded {
			expanded = false
			for id := range usedIDs {
				for _, dep := range resDeps[id] {
					if !usedIDs[dep] {
						usedIDs[dep] = true
						expanded = true
					}
				}
			}
		}
		var remaining []pendingResFile
		progressed := false
		for _, item := range pending {
			if usedIDs[item.id] {
				required[item.resolved] = true
				if item.structure && !seen[item.resolved] {
					queue = append(queue, item.resolved)
					progressed = true
				}
			} else {
				remaining = append(remaining, item)
			}
		}
		pending = remaining
		if !progressed {
			break
		}
	}
	return nil
}
