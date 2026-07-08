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
	"crypto/subtle"
	"encoding/asn1"
	"encoding/base64"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strings"
)

// SignatureVerifyReport 签名验证报告
type SignatureVerifyReport struct {
	ID                string
	BaseLoc           string
	Type              SignType
	Provider          SignatureProvider
	Signer            string
	SignCert          SignatureCertInfo
	SealCert          SignatureCertInfo
	SealType          string
	SignatureMethod   string
	SignatureDateTime string
	DigestMethod      string
	References        []SignatureReferenceVerify
	Stamps            []SignatureStamp
	DigestOK          bool
	DataHashOK        bool
	SignedValueOK     bool
	SealOK            bool
	SealMatchOK       bool
	CertOK            bool
	Valid             bool
	Error             string
}

// SignatureCertInfo 签名证书信息
type SignatureCertInfo struct {
	Subject      string
	CommonName   string
	Organization string
	Issuer       string
	SerialNumber string
}

// SignatureReferenceVerify 签名保护文件验证结果
type SignatureReferenceVerify struct {
	FileRef    string
	Path       string
	CheckValue []byte
	Actual     []byte
	OK         bool
	Error      string
}

// signatureVerifyOptions 签名验证选项
type signatureVerifyOptions struct {
	SignCerts [][]byte
}

// SignatureVerifyOption 签名验证选项函数
type SignatureVerifyOption func(*signatureVerifyOptions)

// WithSignatureCert 添加数字签名验证证书
// 入参: cert DER或PEM编码证书
// 返回: SignatureVerifyOption 签名验证选项
func WithSignatureCert(cert []byte) SignatureVerifyOption {
	return func(o *signatureVerifyOptions) {
		o.SignCerts = append(o.SignCerts, parseSignatureCerts(cert)...)
	}
}

// WithSignatureCerts 添加多张数字签名验证证书
// 入参: certs DER或PEM编码证书列表
// 返回: SignatureVerifyOption 签名验证选项
func WithSignatureCerts(certs ...[]byte) SignatureVerifyOption {
	return func(o *signatureVerifyOptions) {
		for _, cert := range certs {
			o.SignCerts = append(o.SignCerts, parseSignatureCerts(cert)...)
		}
	}
}

// VerifySignaturesBytes 验证OFD字节数据签名
// 入参: data OFD字节数据, opts 签名验证选项
// 返回: []SignatureVerifyReport 签名验证报告, error 错误信息
func VerifySignaturesBytes(data []byte, opts ...SignatureVerifyOption) ([]SignatureVerifyReport, error) {
	return VerifySignaturesReader(bytes.NewReader(data), int64(len(data)), opts...)
}

// VerifySignaturesStream 验证OFD顺序流签名
// 入参: r IO顺序读取器, opts 签名验证选项
// 返回: []SignatureVerifyReport 签名验证报告, error 错误信息
func VerifySignaturesStream(r io.Reader, opts ...SignatureVerifyOption) ([]SignatureVerifyReport, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return VerifySignaturesBytes(data, opts...)
}

// VerifySignaturesReader 验证OFD读取器签名
// 入参: r IO读取器, size 数据大小, opts 签名验证选项
// 返回: []SignatureVerifyReport 签名验证报告, error 错误信息
func VerifySignaturesReader(r io.ReaderAt, size int64, opts ...SignatureVerifyOption) ([]SignatureVerifyReport, error) {
	reader, err := NewReader(r, size)
	if err != nil {
		return nil, err
	}
	return reader.VerifySignatures(opts...)
}

// VerifySignatures 验证文档签名
// 入参: opts 签名验证选项
// 返回: []SignatureVerifyReport 签名验证报告, error 错误信息
func (r *Reader) VerifySignatures(opts ...SignatureVerifyOption) ([]SignatureVerifyReport, error) {
	options := signatureVerifyOptions{}
	for _, opt := range opts {
		opt(&options)
	}
	doc, err := r.Doc()
	if err != nil {
		return nil, err
	}
	if doc.Signatures == "" {
		return nil, nil
	}
	sigListPath := r.ResPath(doc.Signatures)
	data, err := r.readFileExact(sigListPath)
	if err != nil {
		return nil, err
	}
	var signatures Signatures
	if err := xml.Unmarshal(data, &signatures); err != nil {
		return nil, err
	}
	reports := make([]SignatureVerifyReport, 0, len(signatures.List))
	for _, sigRef := range signatures.List {
		reports = append(reports, r.verifySignature(sigListPath, sigRef, &options))
	}
	return reports, nil
}

// verifySignature 验证单个签名
// 入参: sigListPath 签名列表路径, sigRef 签名引用, options 验证选项
// 返回: SignatureVerifyReport 签名验证报告
func (r *Reader) verifySignature(sigListPath string, sigRef Signature, options *signatureVerifyOptions) SignatureVerifyReport {
	sigPath := signatureRefPath(sigListPath, sigRef.BaseLoc)
	report := SignatureVerifyReport{
		ID:          sigRef.ID,
		BaseLoc:     sigRef.BaseLoc,
		Type:        sigRef.Type,
		SealMatchOK: true,
	}
	sigData, err := r.readFileExact(sigPath)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	sigFile, err := parseSignatureFile(sigData)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	report.Provider = sigFile.SignedInfo.Provider
	report.SignatureMethod = sigFile.SignedInfo.SignatureMethod
	report.SignatureDateTime = sigFile.SignedInfo.SignatureDateTime
	report.DigestMethod = sigFile.SignedInfo.References.CheckMethod
	report.References = r.verifySignatureReferences(sigPath, sigFile.SignedInfo.References)
	report.Stamps = append(report.Stamps, sigFile.SignedInfo.StampAnnot...)
	report.DigestOK = referencesOK(report.References)
	signedValuePath := signatureRefPath(sigPath, sigFile.SignedValue)
	signedValue, err := r.readFileExact(signedValuePath)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	switch sigRef.Type {
	case SignTypeSign:
		result, err := verifyDigitalSignature(report.SignatureMethod, signedValue, sigData, options)
		if err != nil {
			report.Error = err.Error()
			return report
		}
		report.DataHashOK = result.DataHashOK
		report.SignedValueOK = result.SignedOK
		report.SealOK = true
		report.CertOK = result.CertOK
		report.SignCert = result.CertInfo
		report.Signer = result.CertInfo.CommonName
		report.Valid = report.DigestOK && report.DataHashOK && report.SignedValueOK && report.CertOK
		return report
	case "", SignTypeSeal:
	default:
		report.Error = fmt.Sprintf("unsupported signature type: %s", sigRef.Type)
		return report
	}
	sesResult, err := verifySESSignature(signedValue, sigData)
	if sesResult != nil {
		report.SignCert = sesResult.SignCert
		report.SealCert = sesResult.SealCert
		report.SealType = sesResult.SealType
		report.Signer = sesResult.SignCert.CommonName
	}
	if err != nil {
		report.Error = err.Error()
		return report
	}
	report.DataHashOK = sesResult.DataHashOK
	report.SignedValueOK = sesResult.SignedOK
	report.SealOK = sesResult.SealOK
	report.CertOK = sesResult.CertOK
	if sigFile.SignedInfo.Seal.BaseLoc != "" {
		sealPath := signatureRefPath(sigPath, sigFile.SignedInfo.Seal.BaseLoc)
		sealData, err := r.readFileExact(sealPath)
		if err != nil {
			report.Error = err.Error()
			return report
		}
		sig, err := parseSESSignature(signedValue)
		if err != nil {
			report.Error = err.Error()
			return report
		}
		report.SealMatchOK = bytes.Equal(sealData, sig.Seal.Raw)
	}
	report.Valid = report.DigestOK && report.DataHashOK && report.SignedValueOK && report.SealOK && report.SealMatchOK && report.CertOK
	return report
}

// verifySignatureReferences 验证签名保护文件列表
// 入参: sigPath 签名文件路径, refs 签名保护文件列表
// 返回: []SignatureReferenceVerify 保护文件验证结果
func (r *Reader) verifySignatureReferences(sigPath string, refs SignatureReferences) []SignatureReferenceVerify {
	results := make([]SignatureReferenceVerify, 0, len(refs.Reference))
	for _, ref := range refs.Reference {
		results = append(results, r.verifySignatureReference(sigPath, refs.CheckMethod, ref))
	}
	return results
}

// verifySignatureReference 验证签名保护文件
// 入参: sigPath 签名文件路径, method 摘要算法, ref 保护文件引用
// 返回: SignatureReferenceVerify 保护文件验证结果
func (r *Reader) verifySignatureReference(sigPath, method string, ref SignatureReference) SignatureReferenceVerify {
	refPath := signatureRefPath(sigPath, ref.FileRef)
	result := SignatureReferenceVerify{
		FileRef: ref.FileRef,
		Path:    refPath,
	}
	checkValue, err := base64.StdEncoding.DecodeString(strings.TrimSpace(ref.CheckValue))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	data, err := r.readFileExact(refPath)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	actual, err := signatureDigest(method, data)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.CheckValue = checkValue
	result.Actual = actual
	result.OK = subtle.ConstantTimeCompare(checkValue, actual) == 1
	return result
}

// parseSignatureFile 解析签名文件
// 入参: data 签名文件XML数据
// 返回: *SignatureFile 签名文件结构, error 错误信息
func parseSignatureFile(data []byte) (*SignatureFile, error) {
	var sigFile SignatureFile
	if err := xml.Unmarshal(data, &sigFile); err != nil {
		return nil, err
	}
	raw, err := xmlElementRaw(data, "SignedInfo")
	if err != nil {
		return nil, err
	}
	sigFile.SignedInfo.Raw = raw
	return &sigFile, nil
}

// readFileExact 读取OFD包内文件
// 入参: name 包内文件路径
// 返回: []byte 文件数据, error 错误信息
func (r *Reader) readFileExact(name string) ([]byte, error) {
	name = cleanPackagePath(name)
	if f, ok := r.fileIndex[name]; ok {
		return readZipFile(f)
	}
	return nil, fmt.Errorf("file not found: %s", name)
}

// signatureDigest 计算签名摘要
// 入参: method 摘要算法, data 原文数据
// 返回: []byte 摘要值, error 错误信息
func signatureDigest(method string, data []byte) ([]byte, error) {
	if isSM3DigestMethod(method) {
		return signSM3(data), nil
	}
	return nil, fmt.Errorf("unsupported digest method: %s", method)
}

// signatureRefPath 解析签名文件引用路径
// 入参: basePath 基准路径, refPath 引用路径
// 返回: string 包内文件路径
func signatureRefPath(basePath, refPath string) string {
	p := strings.TrimSpace(refPath)
	p = strings.ReplaceAll(p, "\\", "/")
	if strings.HasPrefix(p, "/") {
		return cleanPackagePath(p)
	}
	return path.Clean(resolveResourcePath(basePath, "", p))
}

// referencesOK 判断保护文件摘要是否全部通过
// 入参: refs 保护文件验证结果
// 返回: bool 是否全部通过
func referencesOK(refs []SignatureReferenceVerify) bool {
	if len(refs) == 0 {
		return false
	}
	for _, ref := range refs {
		if !ref.OK {
			return false
		}
	}
	return true
}

// xmlElementRaw 提取XML元素原始字节
// 入参: data XML数据, localName 元素名称
// 返回: []byte 元素原始字节, error 错误信息
func xmlElementRaw(data []byte, localName string) ([]byte, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != localName {
			continue
		}
		end := int(dec.InputOffset())
		begin := bytes.LastIndex(data[:end], []byte("<"))
		if begin < 0 {
			return nil, fmt.Errorf("xml element not found: %s", localName)
		}
		depth := 1
		for depth > 0 {
			tok, err = dec.Token()
			if err != nil {
				return nil, err
			}
			switch tok.(type) {
			case xml.StartElement:
				depth++
			case xml.EndElement:
				depth--
			}
		}
		return append([]byte(nil), data[begin:int(dec.InputOffset())]...), nil
	}
	return nil, fmt.Errorf("xml element not found: %s", localName)
}

// signatureCertInfo 解析签名证书信息
// 入参: data DER编码证书
// 返回: SignatureCertInfo 签名证书信息
func signatureCertInfo(data []byte) SignatureCertInfo {
	var cert struct {
		TBSCertificate     asn1.RawValue
		SignatureAlgorithm asn1.RawValue
		SignatureValue     asn1.BitString
	}
	rest, err := asn1.Unmarshal(data, &cert)
	if err != nil || len(rest) != 0 {
		return SignatureCertInfo{}
	}
	items, ok := asn1Children(cert.TBSCertificate.Bytes)
	if !ok {
		return SignatureCertInfo{}
	}
	idx := 0
	if len(items) > 0 && items[0].Class == asn1.ClassContextSpecific && items[0].Tag == 0 {
		idx++
	}
	if len(items) <= idx+4 {
		return SignatureCertInfo{}
	}
	serial, _ := asn1IntegerBig(items[idx])
	subject := certificateNameValues(items[idx+4])
	issuer := certificateNameValues(items[idx+2])
	info := SignatureCertInfo{
		Subject:      certificateNameString(subject),
		CommonName:   certificateNameFirst(subject, "2.5.4.3"),
		Organization: certificateNameFirst(subject, "2.5.4.10"),
		Issuer:       certificateNameString(issuer),
	}
	if serial != nil {
		info.SerialNumber = serial.String()
	}
	return info
}

// certificateNameValues 解析证书名称字段
// 入参: raw 名称原始值
// 返回: map[string][]string OID字段列表
func certificateNameValues(raw asn1.RawValue) map[string][]string {
	out := make(map[string][]string)
	sets, ok := asn1Children(raw.Bytes)
	if !ok {
		return out
	}
	for _, set := range sets {
		attrs, ok := asn1Children(set.Bytes)
		if !ok {
			continue
		}
		for _, attr := range attrs {
			items, ok := asn1Children(attr.Bytes)
			if !ok || len(items) < 2 {
				continue
			}
			oid, err := asn1OIDString(items[0])
			if err != nil {
				continue
			}
			if value := strings.TrimSpace(asn1String(items[1])); value != "" {
				out[oid] = append(out[oid], value)
			}
		}
	}
	return out
}

// certificateNameFirst 获取证书名称字段首值
// 入参: values OID字段列表, oid 字段OID
// 返回: string 字段值
func certificateNameFirst(values map[string][]string, oid string) string {
	if items := values[oid]; len(items) > 0 {
		return items[0]
	}
	return ""
}

// certificateNameString 格式化证书名称
// 入参: values OID字段列表
// 返回: string 证书名称
func certificateNameString(values map[string][]string) string {
	var parts []string
	for _, item := range []struct {
		OID   string
		Label string
	}{
		{"2.5.4.3", "CN"},
		{"2.5.4.10", "O"},
		{"2.5.4.11", "OU"},
		{"2.5.4.6", "C"},
		{"2.5.4.8", "ST"},
		{"2.5.4.7", "L"},
		{"2.5.4.5", "SN"},
		{"1.2.840.113549.1.9.1", "E"},
	} {
		for _, value := range values[item.OID] {
			parts = append(parts, item.Label+"="+value)
		}
	}
	return strings.Join(parts, ", ")
}

// parseSignatureCerts 解析签名验证证书
// 入参: data DER或PEM编码证书
// 返回: [][]byte DER编码证书列表
func parseSignatureCerts(data []byte) [][]byte {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil
	}
	var certs [][]byte
	rest := data
	hasPEM := false
	for {
		block, next := pem.Decode(rest)
		if block == nil {
			break
		}
		hasPEM = true
		if block.Type == "CERTIFICATE" {
			certs = append(certs, append([]byte(nil), block.Bytes...))
		}
		rest = next
	}
	if len(certs) != 0 {
		return certs
	}
	if hasPEM {
		return nil
	}
	return [][]byte{append([]byte(nil), data...)}
}
