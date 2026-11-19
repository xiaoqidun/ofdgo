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
	"crypto/x509/pkix"
	"encoding/asn1"
	"fmt"
	"slices"
	"time"

	"github.com/emmansun/gmsm/smx509"
)

// SignatureRevocationEvidence 离线吊销证据
type SignatureRevocationEvidence struct {
	CRLs          [][]byte
	OCSPResponses [][]byte
}

// SignatureRevocationRequest 外部吊销证据请求
type SignatureRevocationRequest struct {
	Certificate []byte
	Issuer      []byte
	VerifyTime  time.Time
}

// SignatureRevocationOptions 离线吊销检查选项
// 检查签署者及制章者证书, 不代表整条证书链或TSA的吊销状态
// CRL支持直接完整列表及增量合并, 尚不支持间接或带作用域列表
// Fetch仅提供DER证据, 本库不主动联网, 缺少NextUpdate时必须显式设置MaxAge
// VerifyTime缺省取验签指定时间或当前时间, 不采用签名自报时间
type SignatureRevocationOptions struct {
	SignatureRevocationEvidence
	Certificates [][]byte
	Fetch        func(SignatureRevocationRequest) (SignatureRevocationEvidence, error)
	Required     bool
	VerifyTime   *time.Time
	MaxAge       time.Duration
}

// SignatureRevocationReport 单张证书吊销检查结果
// Checked仅表示已取得经验证的状态结论, OK仅在状态为good时为true
type SignatureRevocationReport struct {
	Certificate SignatureCertInfo
	Source      string
	Status      string
	Checked     bool
	OK          bool
	ThisUpdate  time.Time
	NextUpdate  time.Time
	RevokedAt   time.Time
	Error       string
}

// VerifySignatureRevocation 验证单张证书的离线吊销证据
// 入参: cert 证书DER, issuer 直接颁发者DER, options 吊销选项
// 返回: SignatureRevocationReport 验证结果
func VerifySignatureRevocation(cert, issuer []byte, options SignatureRevocationOptions) SignatureRevocationReport {
	result := SignatureRevocationReport{Certificate: signatureCertInfo(cert), Status: "unknown"}
	c, err := parseSignatureCertificate(cert)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	i, err := parseSignatureCertificate(issuer)
	if err != nil || !i.IsCA || i.KeyUsage != 0 && i.KeyUsage&smx509.KeyUsageCertSign == 0 || !bytes.Equal(c.RawIssuer, i.RawSubject) {
		result.Error = "invalid revocation issuer"
		return result
	}
	valid, err := verifyCertificateSignature(c, issuer)
	if err != nil || !valid {
		result.Error = "certificate issuer signature mismatch"
		return result
	}
	at := time.Now()
	if options.VerifyTime != nil {
		at = *options.VerifyTime
	}
	if !timeInRange(at, i.NotBefore, i.NotAfter) || len(i.UnhandledCriticalExtensions) != 0 {
		result.Error = "invalid revocation issuer validity"
		return result
	}
	evidence := options.SignatureRevocationEvidence
	if options.Fetch != nil {
		fetched, err := options.Fetch(SignatureRevocationRequest{Certificate: bytes.Clone(cert), Issuer: bytes.Clone(issuer), VerifyTime: at})
		if err != nil {
			result.Error = err.Error()
			return result
		}
		evidence.CRLs = append(cloneSignatureEvidence(evidence.CRLs), fetched.CRLs...)
		evidence.OCSPResponses = append(cloneSignatureEvidence(evidence.OCSPResponses), fetched.OCSPResponses...)
	}
	var good *SignatureRevocationReport
	if len(evidence.CRLs) != 0 {
		item := verifySignatureCRLs(evidence.CRLs, c, i, at, options.MaxAge)
		if item.Checked && item.Status == "revoked" {
			return item
		}
		if item.OK {
			copy := item
			good = &copy
		} else if item.Error != "" {
			result.Error = item.Error
		}
	}
	for _, raw := range evidence.OCSPResponses {
		item := verifySignatureOCSP(raw, cert, issuer, at, options.MaxAge)
		if item.Checked && item.Status == "revoked" {
			return item
		}
		if item.OK {
			copy := item
			good = &copy
		} else if item.Error != "" {
			result.Error = item.Error
		}
		if item.Checked && item.Status == "unknown" && !result.Checked {
			result = item
		}
	}
	if good != nil {
		return *good
	}
	if result.Error == "" {
		result.Error = "usable revocation evidence not found"
	}
	return result
}

// WithSignatureRevocation 设置离线吊销证据检查
// 入参: options 吊销选项
// 返回: SignatureVerifyOption 验签选项
func WithSignatureRevocation(options SignatureRevocationOptions) SignatureVerifyOption {
	options.CRLs = cloneSignatureEvidence(options.CRLs)
	options.OCSPResponses = cloneSignatureEvidence(options.OCSPResponses)
	options.Certificates = appendSignatureCerts(nil, options.Certificates...)
	if options.VerifyTime != nil {
		t := *options.VerifyTime
		options.VerifyTime = &t
	}
	return func(o *signatureVerifyOptions) { o.Revocation = &options }
}

// signatureEvidenceFresh 检查证据时间窗口及最大年龄
// 入参: thisUpdate 生效时间, nextUpdate 下次更新时间, at 验证时间, maxAge 最大年龄
// 返回: bool 证据是否新鲜
func signatureEvidenceFresh(thisUpdate, nextUpdate, at time.Time, maxAge time.Duration) bool {
	if thisUpdate.IsZero() || thisUpdate.After(at) || maxAge < 0 {
		return false
	}
	if maxAge > 0 && at.Sub(thisUpdate) > maxAge {
		return false
	}
	if nextUpdate.IsZero() {
		return maxAge > 0
	}
	return nextUpdate.After(thisUpdate) && at.Before(nextUpdate)
}

// verifySignatureOCSP 验证OCSP签名、授权、颁发者绑定及状态，完整支持国密SM2/SM3与标准X.509算法
// 入参: raw 响应DER, cert 证书, issuer 颁发者, at 验证时间, maxAge 最大年龄
// 返回: SignatureRevocationReport 吊销结果
func verifySignatureOCSP(raw, cert, issuer []byte, at time.Time, maxAge time.Duration) (result SignatureRevocationReport) {
	result = SignatureRevocationReport{Certificate: signatureCertInfo(cert), Source: "OCSP", Status: "unknown"}
	err := func() error {
		c, err := parseSignatureCertificate(cert)
		if err != nil {
			return fmt.Errorf("unsupported OCSP certificate: %w", err)
		}
		parent, err := parseSignatureCertificate(issuer)
		if err != nil {
			return err
		}
		var outer struct {
			Status asn1.Enumerated
			Bytes  struct {
				Type     asn1.ObjectIdentifier
				Response []byte
			} `asn1:"optional,explicit,tag:0"`
		}
		rest, err := asn1.Unmarshal(raw, &outer)
		if err != nil || len(rest) != 0 || outer.Status != 0 || outer.Bytes.Type.String() != "1.3.6.1.5.5.7.48.1.1" || len(outer.Bytes.Response) == 0 {
			return fmt.Errorf("invalid OCSP response")
		}
		var basic struct {
			TBSResponseData    asn1.RawValue
			SignatureAlgorithm pkix.AlgorithmIdentifier
			Signature          asn1.BitString
			Certs              []asn1.RawValue `asn1:"optional,explicit,tag:0"`
		}
		rest, err = asn1.Unmarshal(outer.Bytes.Response, &basic)
		if err != nil || len(rest) != 0 || basic.TBSResponseData.Tag != asn1.TagSequence || basic.Signature.BitLength%8 != 0 {
			return fmt.Errorf("invalid basic OCSP response")
		}
		var data struct {
			Version    int `asn1:"optional,explicit,tag:0"`
			Responder  asn1.RawValue
			ProducedAt time.Time `asn1:"generalized"`
			Responses  []asn1.RawValue
			Extensions []pkix.Extension `asn1:"optional,explicit,tag:1"`
		}
		rest, err = asn1.Unmarshal(basic.TBSResponseData.FullBytes, &data)
		if err != nil || len(rest) != 0 || data.Version != 0 {
			return fmt.Errorf("invalid OCSP response data")
		}
		for _, ext := range data.Extensions {
			if ext.Critical {
				return fmt.Errorf("unsupported critical OCSP extension")
			}
		}
		var rawResponderName, responderKeyHash []byte
		switch {
		case data.Responder.Class == asn1.ClassContextSpecific && data.Responder.Tag == 1 && len(data.Responder.Bytes) > 0:
			rawResponderName = data.Responder.Bytes
		case data.Responder.Class == asn1.ClassContextSpecific && data.Responder.Tag == 2:
			rest, err := asn1.Unmarshal(data.Responder.Bytes, &responderKeyHash)
			if err != nil || len(rest) != 0 || len(responderKeyHash) == 0 {
				return fmt.Errorf("invalid OCSP responder key hash")
			}
		default:
			return fmt.Errorf("invalid OCSP responder ID")
		}
		var responder *smx509.Certificate
		if ocspMatchResponderID(parent, rawResponderName, responderKeyHash) {
			responder = parent
		}
		for _, rawCert := range basic.Certs {
			candidate, err := smx509.ParseCertificate(rawCert.FullBytes)
			if err != nil {
				return fmt.Errorf("invalid OCSP responder certificate: %w", err)
			}
			if responder == nil && ocspMatchResponderID(candidate, rawResponderName, responderKeyHash) {
				responder = candidate
			}
		}
		if responder == nil {
			return fmt.Errorf("OCSP responder mismatch")
		}
		if !bytes.Equal(responder.Raw, parent.Raw) {
			if !bytes.Equal(responder.RawIssuer, parent.RawSubject) || parent.CheckSignature(responder.SignatureAlgorithm, responder.RawTBSCertificate, responder.Signature) != nil {
				return fmt.Errorf("OCSP responder is not issued by certificate issuer")
			}
			if !slices.Contains(responder.ExtKeyUsage, smx509.ExtKeyUsageOCSPSigning) || responder.KeyUsage != 0 && responder.KeyUsage&smx509.KeyUsageDigitalSignature == 0 {
				return fmt.Errorf("unauthorized OCSP responder")
			}
		}
		if len(responder.UnhandledCriticalExtensions) != 0 || !timeInRange(at, responder.NotBefore, responder.NotAfter) || !timeInRange(data.ProducedAt, responder.NotBefore, responder.NotAfter) {
			return fmt.Errorf("OCSP responder certificate not valid")
		}
		if err := verifyOCSPResponseSignature(responder, basic.SignatureAlgorithm.Algorithm.String(), basic.TBSResponseData.FullBytes, basic.Signature.RightAlign()); err != nil {
			return err
		}
		status, thisUpdate, nextUpdate, revokedAt, err := parseOCSPSingleResponse(data.Responses, c, parent)
		if err != nil {
			return err
		}
		result.ThisUpdate, result.NextUpdate, result.RevokedAt = thisUpdate, nextUpdate, revokedAt
		if !signatureEvidenceFresh(thisUpdate, nextUpdate, at, maxAge) || data.ProducedAt.After(at) || data.ProducedAt.Before(thisUpdate) {
			return fmt.Errorf("stale or future OCSP response")
		}
		switch status {
		case 0:
			result.Checked, result.OK, result.Status = true, true, "good"
		case 1:
			if revokedAt.IsZero() || revokedAt.After(thisUpdate) {
				return fmt.Errorf("invalid OCSP revocation time")
			}
			result.Checked, result.Status = true, "revoked"
		case 2:
			result.Checked = true
		default:
			return fmt.Errorf("unsupported OCSP status")
		}
		return nil
	}()
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

// ocspPublicKeyBytes 提取证书SubjectPublicKeyInfo中的公钥比特串
// 入参: cert 证书
// 返回: []byte 公钥字节, error 解析错误
func ocspPublicKeyBytes(cert *smx509.Certificate) ([]byte, error) {
	var spki struct {
		Algorithm pkix.AlgorithmIdentifier
		Key       asn1.BitString
	}
	rest, err := asn1.Unmarshal(cert.RawSubjectPublicKeyInfo, &spki)
	if err != nil || len(rest) != 0 {
		return nil, fmt.Errorf("invalid certificate public key info")
	}
	return spki.Key.RightAlign(), nil
}

// ocspMatchResponderID 匹配OCSP响应者名称或公钥哈希（支持SHA-1与国密SM3）
// 入参: cert 候选证书, rawName 响应者名称DER, keyHash 响应者公钥摘要
// 返回: bool 是否匹配
func ocspMatchResponderID(cert *smx509.Certificate, rawName, keyHash []byte) bool {
	if len(rawName) != 0 {
		return bytes.Equal(rawName, cert.RawSubject)
	}
	keyBytes, err := ocspPublicKeyBytes(cert)
	if err != nil {
		return false
	}
	return bytes.Equal(keyHash, signatureHashBytes(crypto.SHA1, keyBytes)) || len(keyHash) == 32 && bytes.Equal(keyHash, signSM3(keyBytes))
}

// verifyOCSPResponseSignature 验证BasicOCSPResponse签名值
// 入参: responder 响应者证书, method 签名算法OID, tbs 待签名原文, sig 签名值
// 返回: error 验证错误
func verifyOCSPResponseSignature(responder *smx509.Certificate, method string, tbs, sig []byte) error {
	if isSM2SignatureMethod(method) {
		pub, err := parseSM2PublicKeyFromCert(responder.Raw)
		if err != nil {
			return err
		}
		if !verifySM2Signature(pub, nil, tbs, sig) {
			return fmt.Errorf("invalid OCSP SM2 signature")
		}
		return nil
	}
	if method == "1.3.101.112" {
		if err := responder.CheckSignature(smx509.PureEd25519, tbs, sig); err != nil {
			return fmt.Errorf("invalid OCSP Ed25519 signature")
		}
		return nil
	}
	ok, err := verifyPublicKeySignature(method, "", responder.Raw, tbs, sig)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("invalid OCSP signature")
	}
	return nil
}

// parseOCSPSingleResponse 校验唯一匹配的CertID并提取目标证书状态与时间窗口
// 入参: responses 响应项列表, cert 目标证书, issuer 颁发者证书
// 返回: int 状态码, time.Time 生效时间, time.Time 下次更新时间, time.Time 吊销时间, error 绑定或编码错误
func parseOCSPSingleResponse(responses []asn1.RawValue, cert, issuer *smx509.Certificate) (int, time.Time, time.Time, time.Time, error) {
	issuerKeyBytes, err := ocspPublicKeyBytes(issuer)
	if err != nil {
		return 0, time.Time{}, time.Time{}, time.Time{}, err
	}
	matched := 0
	issuerMismatch := false
	var hit []asn1.RawValue
	for _, raw := range responses {
		fields, ok := asn1Children(raw.Bytes)
		if !ok || len(fields) < 3 {
			return 0, time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("invalid OCSP single response")
		}
		id, ok := asn1Children(fields[0].Bytes)
		if !ok || len(id) != 4 {
			return 0, time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("invalid OCSP CertID")
		}
		hashAlg, err := parseGBTAlgorithm(id[0])
		if err != nil {
			return 0, time.Time{}, time.Time{}, time.Time{}, err
		}
		serial, err := asn1IntegerBig(id[3])
		if err != nil {
			return 0, time.Time{}, time.Time{}, time.Time{}, err
		}
		if serial.Cmp(cert.SerialNumber) != 0 {
			continue
		}
		nameHash, err := asn1OctetString(id[1])
		if err != nil {
			return 0, time.Time{}, time.Time{}, time.Time{}, err
		}
		keyHash, err := asn1OctetString(id[2])
		if err != nil {
			return 0, time.Time{}, time.Time{}, time.Time{}, err
		}
		expectedName, expectedKey, err := ocspIssuerHashes(hashAlg, issuer.RawSubject, issuerKeyBytes)
		if err != nil {
			return 0, time.Time{}, time.Time{}, time.Time{}, err
		}
		if !bytes.Equal(nameHash, expectedName) || !bytes.Equal(keyHash, expectedKey) {
			issuerMismatch = true
			continue
		}
		matched++
		hit = fields
	}
	if matched != 1 {
		if matched == 0 && issuerMismatch {
			return 0, time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("OCSP issuer hash mismatch")
		}
		return 0, time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("ambiguous OCSP certificate status")
	}
	thisUpdate, err := asn1Time(hit[2])
	if err != nil {
		return 0, time.Time{}, time.Time{}, time.Time{}, err
	}
	var nextUpdate, revokedAt time.Time
	for _, extra := range hit[3:] {
		switch {
		case extra.Class == asn1.ClassContextSpecific && extra.Tag == 0:
			var t time.Time
			rest, err := asn1.Unmarshal(extra.Bytes, &t)
			if err != nil || len(rest) != 0 {
				return 0, time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("invalid OCSP nextUpdate")
			}
			nextUpdate = t
		case extra.Class == asn1.ClassContextSpecific && extra.Tag == 1:
			var exts []pkix.Extension
			rest, err := asn1.Unmarshal(extra.Bytes, &exts)
			if err != nil || len(rest) != 0 {
				return 0, time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("invalid OCSP single extensions")
			}
			for _, ext := range exts {
				if ext.Critical {
					return 0, time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("unsupported critical OCSP single extension")
				}
			}
		default:
			return 0, time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("unexpected OCSP single response field")
		}
	}
	statusField := hit[1]
	if statusField.Class != asn1.ClassContextSpecific {
		return 0, time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("invalid OCSP status tag")
	}
	switch statusField.Tag {
	case 0:
		if len(statusField.Bytes) != 0 {
			return 0, time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("invalid OCSP good status")
		}
		return 0, thisUpdate, nextUpdate, revokedAt, nil
	case 1:
		revokedInfo, ok := asn1Children(statusField.Bytes)
		if !ok || len(revokedInfo) == 0 {
			return 0, time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("invalid OCSP revoked info")
		}
		revokedAt, err = asn1Time(revokedInfo[0])
		if err != nil {
			return 0, time.Time{}, time.Time{}, time.Time{}, err
		}
		return 1, thisUpdate, nextUpdate, revokedAt, nil
	case 2:
		if len(statusField.Bytes) != 0 {
			return 0, time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("invalid OCSP unknown status")
		}
		return 2, thisUpdate, nextUpdate, revokedAt, nil
	default:
		return 0, time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("unsupported OCSP status")
	}
}

// ocspIssuerHashes 按CertID摘要算法计算颁发者主题与公钥摘要
// 入参: method 摘要算法标识, rawSubject 颁发者主题DER, keyBytes 颁发者公钥比特串
// 返回: []byte 主题摘要, []byte 公钥摘要, error 不支持的算法错误
func ocspIssuerHashes(method string, rawSubject, keyBytes []byte) ([]byte, []byte, error) {
	if isSM3DigestMethod(method) {
		return signSM3(rawSubject), signSM3(keyBytes), nil
	}
	h, ok := signatureDigestHash(method)
	if !ok || h == crypto.MD5 {
		return nil, nil, fmt.Errorf("unsupported OCSP CertID digest: %s", method)
	}
	return signatureHashBytes(h, rawSubject), signatureHashBytes(h, keyBytes), nil
}

// applySignatureRevocation 检查签署者及制章者的离线吊销证据
// 入参: o 验签选项, certs 目标证书, extra 辅助证书
func (report *SignatureVerifyReport) applySignatureRevocation(o *signatureVerifyOptions, certs, extra [][]byte) {
	var options SignatureRevocationOptions
	if o.Revocation != nil {
		options = *o.Revocation
	}
	if o.Policy != nil {
		options.Required = options.Required || o.Policy.RequireRevocation
	}
	if o.Revocation == nil && !options.Required {
		return
	}
	at := time.Now()
	if o.VerifyTime != nil {
		at = *o.VerifyTime
	}
	if options.VerifyTime != nil {
		at = *options.VerifyTime
	}
	options.VerifyTime = &at
	pool := appendSignatureCerts(nil, options.Certificates...)
	pool = append(pool, extra...)
	pool = append(pool, o.SignCerts...)
	pool = append(pool, o.TrustCerts...)
	pool = compactSignatureCerts(pool)
	for _, raw := range compactSignatureCerts(certs) {
		result := SignatureRevocationReport{Certificate: signatureCertInfo(raw), Status: "unknown", Error: "revocation issuer not found"}
		cert, err := parseSignatureCertificate(raw)
		if err == nil {
			for _, issuerRaw := range pool {
				issuer, err := parseSignatureCertificate(issuerRaw)
				if err != nil || !bytes.Equal(cert.RawIssuer, issuer.RawSubject) {
					continue
				}
				ok, err := verifyCertificateSignature(cert, issuerRaw)
				if err != nil || !ok {
					continue
				}
				result = VerifySignatureRevocation(raw, issuerRaw, options)
				break
			}
		}
		report.Revocations = append(report.Revocations, result)
	}
	if len(report.Revocations) != 0 {
		report.RevocationChecked, report.RevocationOK = true, true
		for _, result := range report.Revocations {
			report.RevocationOK = report.RevocationOK && result.Checked && result.OK
		}
	}
	if options.Required && !report.RevocationChecked {
		report.PolicyChecked, report.PolicyOK, report.PolicyError = true, false, "required revocation evidence missing"
	}
}
