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
	"crypto"
	"encoding/xml"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

// SignaturePolicy 签名验签策略
// 空列表不限制已支持的算法或版本, RequiredFiles使用包内绝对路径
// RequireDocumentCoverage检查OFD.xml、文档目录及已知OFD引用可达的跨目录文件
// 仅排除当前Signature.xml和SignedValue及签名列表, 外置印章也须有引用摘要
// 此范围不表示历史修订范围或整个多文档包, 自定义扩展隐含引用应使用RequiredFiles补充
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

// Error 返回策略拒绝原因
// 返回: string 错误信息
func (e signaturePolicyRejection) Error() string { return e.message }

// rejectSignaturePolicy 构造可识别的策略拒绝错误
// 入参: format 消息模板, values 模板参数
// 返回: error 策略错误
func rejectSignaturePolicy(format string, values ...any) error {
	return signaturePolicyRejection{message: fmt.Sprintf(format, values...)}
}

// applySignaturePolicyError 将实际算法策略拒绝记入报告
// 入参: err 验签错误
func (report *SignatureVerifyReport) applySignaturePolicyError(err error) {
	var rejection signaturePolicyRejection
	if errors.As(err, &rejection) {
		report.PolicyChecked, report.PolicyOK, report.PolicyError = true, false, rejection.Error()
	}
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
	var err error
	if len(p.OFDVersions) != 0 && !signatureStringAllowed(ofdVersion, p.OFDVersions) {
		err = fmt.Errorf("OFD version rejected: %s", ofdVersion)
	}
	if sesVersion != 0 && len(p.SESVersions) != 0 {
		allowed := false
		for _, v := range p.SESVersions {
			allowed = allowed || v == sesVersion
		}
		if !allowed {
			err = fmt.Errorf("SES version rejected: %d", sesVersion)
		}
	}
	if e := signatureAlgorithmPolicy(p, method, digest); e != nil {
		err = e
	}
	if err != nil {
		report.PolicyOK, report.PolicyError = false, err.Error()
	}
}

// signatureAlgorithmPolicy 检查实际签名和摘要算法
// 入参: p 策略, method 签名算法, digest 摘要算法
// 返回: error 策略拒绝原因
func signatureAlgorithmPolicy(p *SignaturePolicy, method, digest string) error {
	if p == nil {
		return nil
	}
	if len(p.DigestMethods) != 0 {
		allowed := false
		for _, v := range p.DigestMethods {
			allowed = allowed || signatureDigestEquivalent(v, digest)
		}
		if !allowed {
			return rejectSignaturePolicy("digest algorithm rejected: %s", digest)
		}
	}
	if len(p.SignatureMethods) != 0 {
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
		h, err := signatureMethodHash(method, digest)
		if !isSM2SignatureMethod(method) && err == nil && (h == crypto.MD5 || h == crypto.SHA1) {
			return rejectSignaturePolicy("weak signature algorithm: %s", method)
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

// signatureAlgorithmEquivalent 比较签名算法别名与OID
// 入参: a 第一算法, b 第二算法, digest 摘要算法
// 返回: bool 是否为相同算法
func signatureAlgorithmEquivalent(a, b, digest string) bool {
	if isSM2SignatureMethod(a) && isSM2SignatureMethod(b) {
		return true
	}
	if !(isRSASignatureMethod(a) && isRSASignatureMethod(b) || isECDSASignatureMethod(a) && isECDSASignatureMethod(b)) {
		return false
	}
	x, err := signatureMethodHash(a, digest)
	y, err2 := signatureMethodHash(b, digest)
	return err == nil && err2 == nil && x == y
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

// applySignatureCoverage 区分引用摘要通过和文档覆盖完整
// 入参: report 验签报告, listPath 签名列表路径, o 验签选项
func (r *Reader) applySignatureCoverage(report *SignatureVerifyReport, listPath string, o *signatureVerifyOptions) {
	covered := make(map[string]bool)
	for _, ref := range report.References {
		if ref.Checked && ref.OK {
			covered[r.signatureCoveragePath(ref.Path)] = true
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
	required := make(map[string]bool)
	for _, name := range o.Policy.RequiredFiles {
		required[r.signatureCoveragePath(name)] = true
	}
	if o.Policy.RequireDocumentCoverage {
		required[r.signatureCoveragePath("OFD.xml")] = true
		excluded := map[string]bool{r.signatureCoveragePath(listPath): true}
		name := r.signatureCoveragePath(signatureRefPath(listPath, report.BaseLoc))
		data, err := r.readFile(name)
		if err != nil {
			report.CoverageOK = false
			report.CoverageError = err.Error()
			return
		}
		file, err := parseSignatureFile(data)
		if err != nil {
			report.CoverageOK = false
			report.CoverageError = err.Error()
			return
		}
		excluded[name] = true
		if file.SignedValue != "" {
			excluded[r.signatureCoveragePath(signatureRefPath(name, file.SignedValue))] = true
		}
		root := path.Dir(r.signatureCoveragePath(o.DocRoot))
		include := func(name string) {
			if (root == "." || strings.HasPrefix(name, root+"/")) && !excluded[name] && !strings.HasSuffix(name, "/") {
				required[name] = true
			}
		}
		for name, file := range r.fileIndex {
			if !file.FileInfo().IsDir() {
				include(name)
			}
		}
		for name := range r.files {
			include(name)
		}
		if err := r.signatureCoverageReferences(o.DocRoot, required); err != nil {
			report.CoverageOK, report.CoverageError = false, err.Error()
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

// signatureCoveragePath 复用阅读器大小写索引获取同一文件身份
// 入参: name 包内路径
// 返回: string 实际包内路径
func (r *Reader) signatureCoveragePath(name string) string {
	name = cleanPackagePath(name)
	if actual, ok := r.fileNamesFold[strings.ToLower(name)]; ok {
		return actual
	}
	return name
}

// signatureCoverageNode 收集OFD结构中的包内路径引用
type signatureCoverageNode struct {
	XMLName  xml.Name
	Attrs    []xml.Attr              `xml:",any,attr"`
	Text     string                  `xml:",chardata"`
	Children []signatureCoverageNode `xml:",any"`
}

// signatureCoverageReferences 收集标准OFD引用的传递闭包且包含跨文档资源
// 入参: root 文档入口, required 待补充的必需文件集合
// 返回: error 引用文件无法读取或解析时的错误
func (r *Reader) signatureCoverageReferences(root string, required map[string]bool) error {
	queue := []string{r.signatureCoveragePath(root)}
	seen := make(map[string]bool)
	for len(queue) != 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name], required[name] = true, true
		data, err := r.readFile(name)
		if err != nil {
			return err
		}
		if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("<")) {
			continue
		}
		var node signatureCoverageNode
		if err := xml.Unmarshal(data, &node); err != nil {
			return fmt.Errorf("coverage XML %s: %w", name, err)
		}
		base := ""
		if node.XMLName.Local == "Res" {
			for _, attr := range node.Attrs {
				if attr.Name.Local == "BaseLoc" {
					base = attr.Value
				}
			}
		}
		add := func(loc string, resource bool) {
			if strings.TrimSpace(loc) == "" {
				return
			}
			resBase := ""
			if resource {
				resBase = base
			}
			resolved := r.signatureCoveragePath(resolveResourcePath(name, resBase, loc))
			required[resolved] = true
			if !seen[resolved] {
				queue = append(queue, resolved)
			}
		}
		var walk func(signatureCoverageNode)
		walk = func(current signatureCoverageNode) {
			kind := current.XMLName.Local
			if kind == "Signatures" || kind == "Signature" {
				return
			}
			for _, attr := range current.Attrs {
				if attr.Name.Local == "BaseLoc" && kind != "Res" || attr.Name.Local == "Link" && kind == "DrawParam" {
					add(attr.Value, node.XMLName.Local == "Res")
				}
			}
			switch kind {
			case "PublicRes", "DocumentRes", "PageRes", "Annotations", "Attachments", "CustomTags", "Extensions", "FileLoc", "SchemaLoc":
				add(current.Text, false)
			case "FontFile", "MediaFile", "Profile":
				add(current.Text, true)
			}
			for _, child := range current.Children {
				walk(child)
			}
		}
		walk(node)
	}
	return nil
}
