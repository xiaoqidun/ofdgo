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
	"encoding/asn1"
	"fmt"
	"math/big"
	"time"

	"github.com/emmansun/gmsm/smx509"
)

const (
	signTimestampContent = "1.2.840.113549.1.9.16.1.4"
	signTimestampAttr    = "1.2.840.113549.1.9.16.2.14"
	signContentCMS       = "1.2.840.113549.1.7.2"
	signAttrContentType  = "1.2.840.113549.1.9.3"
	signAttrESS          = "1.2.840.113549.1.9.16.2.12"
	signAttrESSV2        = "1.2.840.113549.1.9.16.2.47"
)

// SignatureTimestampRequest 外部时间戳证据请求
// Data为完整SignedValue文件字节, 回调返回的RFC3161令牌须绑定Data
type SignatureTimestampRequest struct {
	DocIndex    int
	SignatureID string
	Data        []byte
}

// SignatureTimestampOptions 离线RFC3161时间戳验证选项
// Tokens及Fetch返回的令牌绑定完整SignedValue, 内嵌令牌绑定所属签名值
// Fetch仅提供证据, 本库不主动联网, TrustCerts须由调用方显式提供
type SignatureTimestampOptions struct {
	Tokens       [][]byte
	TrustCerts   [][]byte
	Certificates [][]byte
	Fetch        func(SignatureTimestampRequest) ([][]byte, error)
	Required     bool
	PolicyOID    string
	Nonce        *big.Int
	VerifyTime   *time.Time
}

// SignatureTimestampReport 时间戳验证报告
// Time只表示令牌声明时间, 仅Valid为true时可作为可信时间使用
type SignatureTimestampReport struct {
	tsaName            asn1.RawValue
	Time               time.Time
	PolicyOID          string
	SerialNumber       string
	Nonce              *big.Int
	Signer             SignatureCertInfo
	BindingChecked     bool
	BindingOK          bool
	SignedValueChecked bool
	SignedValueOK      bool
	CertTrustChecked   bool
	CertTrustOK        bool
	CertTimeChecked    bool
	CertTimeOK         bool
	Valid              bool
	Error              string
}

// signatureTimestampEvidence 保存时间戳令牌及其绑定数据
type signatureTimestampEvidence struct {
	Token []byte
	Data  []byte
}

// WithSignatureTimestamp 设置离线时间戳验证
// 入参: options 时间戳选项
// 返回: SignatureVerifyOption 验签选项
func WithSignatureTimestamp(options SignatureTimestampOptions) SignatureVerifyOption {
	options.Tokens = cloneSignatureEvidence(options.Tokens)
	options.TrustCerts = appendSignatureCerts(nil, options.TrustCerts...)
	options.Certificates = appendSignatureCerts(nil, options.Certificates...)
	if options.Nonce != nil {
		options.Nonce = new(big.Int).Set(options.Nonce)
	}
	if options.VerifyTime != nil {
		t := *options.VerifyTime
		options.VerifyTime = &t
	}
	return func(o *signatureVerifyOptions) { o.Timestamp = &options }
}

// cloneSignatureEvidence 深拷贝离线证据
// 入参: values 原证据列表
// 返回: [][]byte 独立证据列表
func cloneSignatureEvidence(values [][]byte) [][]byte {
	out := make([][]byte, len(values))
	for i := range values {
		out[i] = bytes.Clone(values[i])
	}
	return out
}

// VerifySignatureTimestamp 验证RFC3161令牌与原始字节的绑定
// 入参: token DER编码TimeStampToken, data 被时间戳保护的原始字节, options 验证选项
// 返回: SignatureTimestampReport 验证报告
func VerifySignatureTimestamp(token, data []byte, options SignatureTimestampOptions) (report SignatureTimestampReport) {
	err := verifySignatureTimestamp(token, data, options, &report)
	if err != nil {
		report.Error = err.Error()
	}
	report.Valid = err == nil && report.BindingChecked && report.BindingOK && report.SignedValueChecked && report.SignedValueOK && report.CertTrustChecked && report.CertTrustOK && report.CertTimeChecked && report.CertTimeOK
	return report
}

// verifySignatureTimestamp 检查令牌绑定、签名与TSA信任
// 入参: token 令牌DER, data 原始字节, o 验证选项, report 结果
// 返回: error 验证错误
func verifySignatureTimestamp(token, data []byte, o SignatureTimestampOptions, report *SignatureTimestampReport) error {
	oid, content, present, err := parseGBTContentInfoBytes(token)
	if err != nil || !present || oid != signContentCMS {
		return fmt.Errorf("invalid RFC3161 CMS token")
	}
	items, ok := asn1Children(content.Bytes)
	if !ok || len(items) < 4 {
		return fmt.Errorf("invalid timestamp signed data")
	}
	version, err := asn1Integer(items[0])
	if err != nil || version != 3 {
		return fmt.Errorf("unsupported timestamp CMS version")
	}
	oid, value, present, err := parseGBTContentInfo(items[2])
	if err != nil || !present || oid != signTimestampContent {
		return fmt.Errorf("invalid TSTInfo content type")
	}
	tst, err := asn1OctetString(value)
	if err != nil {
		return err
	}
	if err := verifyTimestampInfo(tst, data, o, report); err != nil {
		return err
	}
	certs := appendSignatureCerts(nil, o.Certificates...)
	idx := 3
	if items[idx].Class == asn1.ClassContextSpecific && items[idx].Tag == 0 {
		parsed, err := parseGBTCertificates(items[idx])
		if err != nil {
			return err
		}
		for _, cert := range parsed {
			certs = append(certs, cert.Raw)
		}
		idx++
	}
	if idx < len(items) && items[idx].Class == asn1.ClassContextSpecific && items[idx].Tag == 1 {
		idx++
	}
	if idx != len(items)-1 || items[idx].Tag != asn1.TagSet || items[idx].Class != asn1.ClassUniversal {
		return fmt.Errorf("invalid timestamp signer set")
	}
	signers, err := parseGBTSignerInfos(items[idx])
	if err != nil || len(signers) != 1 {
		return fmt.Errorf("timestamp requires one issuer-and-serial signer")
	}
	signer := signers[0]
	if err := signatureSignerDigest(signer.SignatureAlg, signer.DigestAlg); err != nil {
		return err
	}
	algorithms, ok := asn1Children(items[1].Bytes)
	if !ok || items[1].Tag != asn1.TagSet || len(algorithms) != 1 {
		return fmt.Errorf("invalid timestamp digest set")
	}
	digestAlg, err := parseGBTAlgorithm(algorithms[0])
	if err != nil || digestAlg != signer.DigestAlg {
		return fmt.Errorf("timestamp digest set mismatch")
	}
	if err := signatureAlgorithmPolicy(&SignaturePolicy{RejectWeakAlgorithms: true}, signer.SignatureAlg, signer.DigestAlg); err != nil {
		return err
	}
	attrs, err := signatureAuthenticatedAttributes(signer.AuthAttrs)
	if err != nil {
		return err
	}
	ct, err := asn1OIDString(attrs[signAttrContentType])
	if err != nil || ct != signTimestampContent {
		return fmt.Errorf("timestamp content-type attribute mismatch")
	}
	digest, err := signatureDigest(signer.DigestAlg, tst)
	if err != nil || !bytes.Equal(digest, signer.AttrDigest) {
		return fmt.Errorf("timestamp content digest mismatch")
	}
	var cert []byte
	for _, raw := range certs {
		c, err := parseSignatureCertificate(raw)
		if err == nil && bytes.Equal(c.RawIssuer, signer.Issuer) && c.SerialNumber.Cmp(signer.Serial) == 0 {
			if cert != nil && !bytes.Equal(cert, raw) {
				return fmt.Errorf("ambiguous TSA certificate")
			}
			cert = raw
		}
	}
	if cert == nil {
		return fmt.Errorf("TSA certificate not found")
	}
	if err := verifyTimestampESS(attrs, cert); err != nil {
		return err
	}
	c, err := parseSignatureCertificate(cert)
	if err != nil {
		return err
	}
	if len(report.tsaName.FullBytes) != 0 && (report.tsaName.Class != asn1.ClassContextSpecific || report.tsaName.Tag != 4 || !bytes.Equal(report.tsaName.Bytes, c.RawSubject)) {
		return fmt.Errorf("unsupported or mismatched TSTInfo TSA name")
	}
	critical := false
	for _, extension := range c.Extensions {
		if extension.Id.String() == "2.5.29.37" {
			critical = extension.Critical
		}
	}
	if !critical || len(c.UnknownExtKeyUsage) != 0 || len(c.ExtKeyUsage) != 1 || c.ExtKeyUsage[0] != smx509.ExtKeyUsageTimeStamping {
		return fmt.Errorf("TSA certificate requires exclusive critical timestamping usage")
	}
	report.Signer = signatureCertInfo(cert)
	report.SignedValueChecked = true
	if isSM2SignatureMethod(signer.SignatureAlg) {
		pub, err := parseSM2PublicKeyFromCert(cert)
		if err != nil {
			return err
		}
		report.SignedValueOK = verifySM2Signature(pub, nil, signer.AuthAttrs, signer.Signature)
	} else {
		report.SignedValueOK, err = verifyPublicKeySignature(signer.SignatureAlg, signer.DigestAlg, cert, signer.AuthAttrs, signer.Signature)
		if err != nil {
			return err
		}
	}
	if !report.SignedValueOK {
		return fmt.Errorf("invalid timestamp signature")
	}
	report.CertTimeChecked = true
	report.CertTimeOK = timeInRange(report.Time, c.NotBefore, c.NotAfter)
	if !report.CertTimeOK {
		return fmt.Errorf("TSA certificate not valid at timestamp time")
	}
	trusts := appendSignatureCerts(nil, o.TrustCerts...)
	if len(trusts) == 0 {
		return fmt.Errorf("timestamp trust certificates not provided")
	}
	report.CertTrustChecked = true
	report.CertTrustOK = verifySignatureCertificateChain(c, certs, trusts, report.Time, smx509.ExtKeyUsageTimeStamping) == nil
	if !report.CertTrustOK {
		return fmt.Errorf("untrusted TSA certificate")
	}
	return nil
}

// verifyTimestampInfo 验证TSTInfo及可选nonce和策略
// 入参: data TSTInfo字节, bound 被保护字节, o 验证选项, report 结果
// 返回: error 验证错误
func verifyTimestampInfo(data, bound []byte, o SignatureTimestampOptions, report *SignatureTimestampReport) error {
	var raw asn1.RawValue
	rest, err := asn1.Unmarshal(data, &raw)
	if err != nil || len(rest) != 0 || raw.Tag != asn1.TagSequence {
		return fmt.Errorf("invalid TSTInfo")
	}
	items, ok := asn1Children(raw.Bytes)
	if !ok || len(items) < 5 {
		return fmt.Errorf("invalid TSTInfo fields")
	}
	v, err := asn1Integer(items[0])
	if err != nil || v != 1 {
		return fmt.Errorf("unsupported TSTInfo version")
	}
	report.PolicyOID, err = asn1OIDString(items[1])
	if err != nil {
		return err
	}
	if o.PolicyOID != "" && o.PolicyOID != report.PolicyOID {
		return fmt.Errorf("timestamp policy mismatch")
	}
	imprint, ok := asn1Children(items[2].Bytes)
	if !ok || len(imprint) != 2 {
		return fmt.Errorf("invalid timestamp imprint")
	}
	method, err := parseGBTAlgorithm(imprint[0])
	if err != nil {
		return err
	}
	if err := signatureAlgorithmPolicy(&SignaturePolicy{RejectWeakAlgorithms: true}, "", method); err != nil {
		return err
	}
	want, err := asn1OctetString(imprint[1])
	if err != nil {
		return err
	}
	got, err := signatureDigest(method, bound)
	if err != nil {
		return err
	}
	report.BindingChecked, report.BindingOK = true, bytes.Equal(want, got)
	if !report.BindingOK {
		return fmt.Errorf("timestamp message imprint mismatch")
	}
	serial, err := asn1IntegerBig(items[3])
	if err != nil || serial.Sign() < 0 {
		return fmt.Errorf("invalid timestamp serial")
	}
	report.SerialNumber = serial.String()
	if items[4].Tag != asn1.TagGeneralizedTime {
		return fmt.Errorf("invalid timestamp generalized time")
	}
	report.Time, err = asn1Time(items[4])
	if err != nil {
		return err
	}
	now := time.Now()
	if o.VerifyTime != nil {
		now = *o.VerifyTime
	}
	if report.Time.After(now) {
		return fmt.Errorf("timestamp is in the future")
	}
	previous := -1
	for _, item := range items[5:] {
		order := -1
		switch {
		case item.Class == asn1.ClassUniversal && item.Tag == asn1.TagSequence:
			order = 0
			var accuracy struct {
				Seconds int `asn1:"optional"`
				Millis  int `asn1:"optional,tag:0"`
				Micros  int `asn1:"optional,tag:1"`
			}
			rest, err := asn1.Unmarshal(item.FullBytes, &accuracy)
			if err != nil || len(rest) != 0 || accuracy.Seconds < 0 || accuracy.Millis < 0 || accuracy.Millis > 999 || accuracy.Micros < 0 || accuracy.Micros > 999 {
				return fmt.Errorf("invalid timestamp accuracy")
			}
		case item.Class == asn1.ClassUniversal && item.Tag == asn1.TagBoolean:
			order = 1
			var ordering bool
			if _, err := asn1.Unmarshal(item.FullBytes, &ordering); err != nil {
				return err
			}
		case item.Class == asn1.ClassUniversal && item.Tag == asn1.TagInteger:
			order = 2
			report.Nonce, err = asn1IntegerBig(item)
			if err != nil || report.Nonce.Sign() < 0 {
				return fmt.Errorf("invalid timestamp nonce")
			}
		case item.Class == asn1.ClassContextSpecific && item.Tag == 0:
			order = 3
			report.tsaName, err = asn1Explicit(item)
			if err != nil {
				return err
			}
		case item.Class == asn1.ClassContextSpecific && item.Tag == 1:
			return fmt.Errorf("timestamp extensions not supported")
		default:
			return fmt.Errorf("invalid timestamp optional field")
		}
		if order <= previous {
			return fmt.Errorf("duplicate or unordered timestamp field")
		}
		previous = order
	}
	if o.Nonce != nil && (report.Nonce == nil || report.Nonce.Cmp(o.Nonce) != 0) {
		return fmt.Errorf("timestamp nonce mismatch")
	}
	return nil
}

// signatureAuthenticatedAttributes 拒绝重复属性和非DER排序
// 入参: data 签名属性DER集合
// 返回: map[string]asn1.RawValue 属性映射, error 解析错误
func signatureAuthenticatedAttributes(data []byte) (map[string]asn1.RawValue, error) {
	var raw asn1.RawValue
	rest, err := asn1.Unmarshal(data, &raw)
	if err != nil || len(rest) != 0 || raw.Class != asn1.ClassUniversal || raw.Tag != asn1.TagSet {
		return nil, fmt.Errorf("invalid signed attributes")
	}
	attrs, ok := asn1Children(raw.Bytes)
	if !ok {
		return nil, fmt.Errorf("invalid signed attributes")
	}
	out := make(map[string]asn1.RawValue)
	var previous []byte
	for _, attr := range attrs {
		if bytes.Compare(previous, attr.FullBytes) > 0 {
			return nil, fmt.Errorf("non-DER signed attributes")
		}
		previous = attr.FullBytes
		items, ok := asn1Children(attr.Bytes)
		if !ok || len(items) != 2 || items[1].Tag != asn1.TagSet {
			return nil, fmt.Errorf("invalid signed attribute")
		}
		oid, err := asn1OIDString(items[0])
		if err != nil {
			return nil, err
		}
		if _, exists := out[oid]; exists {
			return nil, fmt.Errorf("duplicate signed attribute: %s", oid)
		}
		values, ok := asn1Children(items[1].Bytes)
		if !ok || len(values) != 1 {
			return nil, fmt.Errorf("signed attribute must have one value")
		}
		out[oid] = values[0]
	}
	return out, nil
}

// verifyTimestampESS 验证ESS证书摘要及颁发者序列号绑定
// 入参: attrs 签名属性, cert TSA证书DER
// 返回: error 绑定错误
func verifyTimestampESS(attrs map[string]asn1.RawValue, cert []byte) error {
	found := false
	for _, oid := range []string{signAttrESS, signAttrESSV2} {
		value, exists := attrs[oid]
		if !exists {
			continue
		}
		found = true
		outer, ok := asn1Children(value.Bytes)
		if !ok || len(outer) < 1 || len(outer) > 2 {
			return fmt.Errorf("invalid ESS signing certificate")
		}
		certs, ok := asn1Children(outer[0].Bytes)
		if !ok || len(certs) == 0 {
			return fmt.Errorf("empty ESS certificate list")
		}
		fields, ok := asn1Children(certs[0].Bytes)
		if !ok || len(fields) == 0 {
			return fmt.Errorf("invalid ESS certificate identifier")
		}
		method := "SHA1"
		idx := 0
		if oid == signAttrESSV2 {
			method = "SHA256"
			if fields[0].Tag == asn1.TagSequence {
				var err error
				method, err = parseGBTAlgorithm(fields[0])
				if err != nil {
					return err
				}
				idx++
			}
		}
		if len(fields) < idx+1 || len(fields) > idx+2 {
			return fmt.Errorf("invalid ESS certificate fields")
		}
		want, err := asn1OctetString(fields[idx])
		if err != nil {
			return err
		}
		got, err := signatureDigest(method, cert)
		if err != nil || !bytes.Equal(want, got) {
			return fmt.Errorf("ESS certificate hash mismatch")
		}
		if len(fields) == idx+2 {
			issuer, ok := asn1Children(fields[idx+1].Bytes)
			if !ok || len(issuer) != 2 {
				return fmt.Errorf("invalid ESS issuer serial")
			}
			serial, err := asn1IntegerBig(issuer[1])
			if err != nil {
				return err
			}
			c, err := parseSignatureCertificate(cert)
			if err != nil {
				return err
			}
			names, ok := asn1Children(issuer[0].Bytes)
			matched := false
			for _, name := range names {
				matched = matched || name.Class == asn1.ClassContextSpecific && name.Tag == 4 && bytes.Equal(name.Bytes, c.RawIssuer)
			}
			if !ok || !matched || serial.Cmp(c.SerialNumber) != 0 {
				return fmt.Errorf("ESS issuer serial mismatch")
			}
		}
	}
	if !found {
		return fmt.Errorf("timestamp ESS certificate binding missing")
	}
	return nil
}

// signatureTimestampAttributes 提取签名值中的RFC3161未认证属性
// 入参: raw 未认证属性ASN.1值
// 返回: [][]byte 时间戳令牌, error 解析错误
func signatureTimestampAttributes(raw asn1.RawValue) ([][]byte, error) {
	if len(raw.FullBytes) == 0 {
		return nil, nil
	}
	attrs, ok := asn1Children(raw.Bytes)
	if !ok {
		return nil, fmt.Errorf("invalid timestamp attributes")
	}
	var tokens [][]byte
	for _, attr := range attrs {
		items, ok := asn1Children(attr.Bytes)
		if !ok || len(items) != 2 {
			return nil, fmt.Errorf("invalid unsigned attribute")
		}
		oid, err := asn1OIDString(items[0])
		if err != nil {
			return nil, err
		}
		if oid != signTimestampAttr {
			continue
		}
		if len(tokens) != 0 {
			return nil, fmt.Errorf("duplicate timestamp attribute")
		}
		values, ok := asn1Children(items[1].Bytes)
		if !ok || items[1].Tag != asn1.TagSet || len(values) == 0 {
			return nil, fmt.Errorf("empty timestamp attribute")
		}
		for _, value := range values {
			tokens = append(tokens, bytes.Clone(value.FullBytes))
		}
	}
	return tokens, nil
}

// applySignatureEvidence 验证内嵌及外部时间戳并应用吊销策略
// 入参: o 验签选项, signedValue 签名文件字节, embedded 内嵌令牌, certs 签署证书, extra 辅助证书
func (report *SignatureVerifyReport) applySignatureEvidence(o *signatureVerifyOptions, signedValue []byte, embedded []signatureTimestampEvidence, certs, extra [][]byte) {
	var options SignatureTimestampOptions
	if o.Timestamp != nil {
		options = *o.Timestamp
	}
	if o.Policy != nil {
		options.Required = options.Required || o.Policy.RequireTimestamp
	}
	evidence := append([]signatureTimestampEvidence(nil), embedded...)
	for _, token := range options.Tokens {
		evidence = append(evidence, signatureTimestampEvidence{Token: token, Data: signedValue})
	}
	if options.Fetch != nil {
		tokens, err := options.Fetch(SignatureTimestampRequest{DocIndex: report.DocIndex, SignatureID: report.ID, Data: bytes.Clone(signedValue)})
		if err != nil {
			report.Timestamps = append(report.Timestamps, SignatureTimestampReport{Error: err.Error()})
		}
		for _, token := range tokens {
			evidence = append(evidence, signatureTimestampEvidence{Token: token, Data: signedValue})
		}
	}
	for _, item := range evidence {
		report.Timestamps = append(report.Timestamps, VerifySignatureTimestamp(item.Token, item.Data, options))
	}
	if len(report.Timestamps) != 0 {
		report.TimestampChecked, report.TimestampOK = true, true
		for _, result := range report.Timestamps {
			report.TimestampOK = report.TimestampOK && result.Valid
		}
	}
	if options.Required && !report.TimestampChecked {
		report.PolicyChecked, report.PolicyOK = true, false
		report.PolicyError = "required timestamp evidence missing"
	}
	report.applySignatureRevocation(o, certs, extra)
}
