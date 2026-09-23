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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"fmt"
	"math/big"
	"reflect"
	"time"

	"github.com/emmansun/gmsm/pkcs8"
	"github.com/emmansun/gmsm/sm2"
	"github.com/emmansun/gmsm/sm3"
	"github.com/emmansun/gmsm/smx509"
)

// ParseSignatureIdentity 解析签署私钥和证书，并检查公私钥对应关系
// 支持普通PKCS8、SEC1、PKCS1以及加密PKCS8，不支持传统加密PEM
// 不加载系统信任或暗中信任该证书，返回的SM2/RSA私钥同时实现crypto.Decrypter
// 入参: certificate 证书DER或PEM, privateKey 私钥DER或PEM, password 可选的单一私钥口令
// 返回: crypto.Signer 签署器, []byte 证书DER, error 错误信息
func ParseSignatureIdentity(certificate, privateKey []byte, password ...[]byte) (crypto.Signer, []byte, error) {
	if len(password) > 1 {
		return nil, nil, fmt.Errorf("expected at most one private key password")
	}
	cert, err := parseSignatureCertificate(certificate)
	if err != nil {
		return nil, nil, err
	}
	der := privateKey
	encrypted := false
	if block, rest := pem.Decode(privateKey); block != nil {
		if len(bytes.TrimSpace(rest)) != 0 || len(block.Headers) != 0 {
			return nil, nil, fmt.Errorf("legacy encrypted PEM or multiple private keys are unsupported")
		}
		encrypted = block.Type == "ENCRYPTED PRIVATE KEY"
		if !encrypted && block.Type != "PRIVATE KEY" && block.Type != "RSA PRIVATE KEY" && block.Type != "EC PRIVATE KEY" {
			return nil, nil, fmt.Errorf("unsupported private key PEM type")
		}
		der = block.Bytes
	}
	var raw asn1.RawValue
	if rest, err := asn1.Unmarshal(der, &raw); err != nil || len(rest) != 0 || raw.Tag != asn1.TagSequence || raw.Class != asn1.ClassUniversal {
		return nil, nil, fmt.Errorf("invalid private key encoding")
	}
	var pass []byte
	if len(password) == 1 {
		pass = bytes.Clone(password[0])
		defer clear(pass)
	}
	if encrypted && len(pass) == 0 {
		return nil, nil, fmt.Errorf("private key password required")
	}
	key, err := pkcs8.ParsePKCS8PrivateKey(der, pass)
	if err != nil && !encrypted {
		key, err = smx509.ParsePKCS8PrivateKey(der)
		if err != nil {
			key, err = smx509.ParseTypedECPrivateKey(der)
		}
		if err != nil {
			key, err = x509.ParsePKCS1PrivateKey(der)
		}
	}
	if err != nil {
		return nil, nil, fmt.Errorf("invalid private key or incorrect password")
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, nil, fmt.Errorf("unsupported signature private key")
	}
	if _, _, err := signatureWriteAlgorithm(signer, cert); err != nil {
		return nil, nil, err
	}
	return signer, bytes.Clone(cert.Raw), nil
}

// signatureWriteKey 已验证签署身份
type signatureWriteKey struct {
	cert   *smx509.Certificate
	method string
	digest string
}

// signatureWriteIdentity 检查签署身份、证书信任与印章授权
// 制章证书按制章时间验证，不以签署时间代替制章时间
// 入参: options 签署选项
// 返回: *signatureWriteKey 已验证身份, error 错误信息
func signatureWriteIdentity(options SignatureWriteOptions) (*signatureWriteKey, error) {
	cert, err := parseSignatureCertificate(options.Certificate)
	if err != nil {
		return nil, err
	}
	method, digest, err := signatureWriteAlgorithm(options.Signer, cert)
	if err != nil {
		return nil, err
	}
	if err := signatureWriteTrust(cert, options.Time, options); err != nil {
		return nil, fmt.Errorf("signer certificate: %w", err)
	}
	if len(options.Seal) > 0 {
		if method != signMethodSM2SM3 {
			return nil, fmt.Errorf("SES v4 writing requires SM2/SM3")
		}
		var raw asn1.RawValue
		rest, err := asn1.Unmarshal(options.Seal, &raw)
		if err != nil || len(rest) != 0 || raw.Tag != asn1.TagSequence || raw.Class != asn1.ClassUniversal {
			return nil, fmt.Errorf("invalid SES seal DER; image files are not electronic seals")
		}
		if err := signatureWriteSealStructure(raw); err != nil {
			return nil, err
		}
		seal, err := parseSESSeal(raw)
		if err != nil {
			return nil, err
		}
		if seal.Info.Version != 4 || seal.SignAlg != signMethodSM2SM3 {
			return nil, fmt.Errorf("only SES v4 SM2/SM3 seals are supported")
		}
		if !sesCertInList(cert.Raw, seal.CertList) {
			return nil, fmt.Errorf("signer certificate is not authorized by the seal")
		}
		if seal.Info.CreateTime.IsZero() || seal.Info.ValidStart.IsZero() || seal.Info.ValidEnd.IsZero() || options.Time.Before(seal.Info.CreateTime) || options.Time.Before(seal.Info.ValidStart) || options.Time.After(seal.Info.ValidEnd) || seal.Info.ValidEnd.Before(seal.Info.ValidStart) {
			return nil, fmt.Errorf("seal is not valid at signature time")
		}
		maker, err := parseSignatureCertificate(seal.Cert)
		if err != nil {
			return nil, err
		}
		if err := signatureWriteTrust(maker, seal.Info.CreateTime, options); err != nil {
			return nil, fmt.Errorf("seal maker certificate: %w", err)
		}
		pub, ok := maker.PublicKey.(*ecdsa.PublicKey)
		if !ok || !sm2.IsSM2PublicKey(pub) || !sm2.VerifyASN1WithSM2(pub, nil, seal.SignData, seal.Signature) {
			return nil, fmt.Errorf("invalid seal maker signature")
		}
		mediaType, media := probeSealMedia(seal.PicData)
		if len(media) == 0 || mediaType != normalizeSealType(seal.PicType) {
			return nil, fmt.Errorf("invalid seal picture")
		}
	}
	return &signatureWriteKey{cert: cert, method: method, digest: digest}, nil
}

// signatureWriteSealStructure 限定SES v4印章结构，拒绝旧版容器伪装和无效图像尺寸
// 入参: raw 印章DER节点
// 返回: error 错误信息
func signatureWriteSealStructure(raw asn1.RawValue) error {
	items, ok := asn1Children(raw.Bytes)
	if !ok || len(items) != 4 || items[0].Tag != asn1.TagSequence || items[1].Tag != asn1.TagOctetString || items[2].Tag != asn1.TagOID || items[3].Tag != asn1.TagBitString {
		return fmt.Errorf("unsupported SES v4 seal structure")
	}
	for _, item := range items {
		if item.Class != asn1.ClassUniversal {
			return fmt.Errorf("invalid SES v4 seal tag")
		}
	}
	info, ok := asn1Children(items[0].Bytes)
	if !ok || len(info) < 4 || len(info) > 5 {
		return fmt.Errorf("invalid SES v4 seal info")
	}
	header, ok := asn1Children(info[0].Bytes)
	if !ok || len(header) != 3 || header[0].Tag != asn1.TagIA5String || header[2].Tag != asn1.TagIA5String || info[1].Tag != asn1.TagIA5String {
		return fmt.Errorf("invalid SES v4 seal header")
	}
	picture, ok := asn1Children(info[3].Bytes)
	if !ok || len(picture) != 4 || picture[0].Tag != asn1.TagIA5String || picture[1].Tag != asn1.TagOctetString {
		return fmt.Errorf("invalid SES v4 picture structure")
	}
	w, err := asn1Integer(picture[2])
	if err != nil || w <= 0 {
		return fmt.Errorf("invalid SES v4 picture width")
	}
	h, err := asn1Integer(picture[3])
	if err != nil || h <= 0 {
		return fmt.Errorf("invalid SES v4 picture height")
	}
	return nil
}

// signatureWriteAlgorithm 校验证书、公钥匹配和支持的安全算法
// 入参: signer 签署器, cert 证书
// 返回: string 签名算法OID, string 摘要OID, error 错误信息
func signatureWriteAlgorithm(signer crypto.Signer, cert *smx509.Certificate) (string, string, error) {
	if signer == nil || reflect.ValueOf(signer).Kind() == reflect.Ptr && reflect.ValueOf(signer).IsNil() {
		return "", "", fmt.Errorf("nil signature signer")
	}
	pub := signer.Public()
	match, ok := cert.PublicKey.(interface{ Equal(crypto.PublicKey) bool })
	if !ok || !match.Equal(pub) {
		return "", "", fmt.Errorf("signature certificate and private key do not match")
	}
	switch key := pub.(type) {
	case *rsa.PublicKey:
		if key.N == nil || key.N.BitLen() < 2048 || key.E < 65537 {
			return "", "", fmt.Errorf("RSA signature key must be at least 2048 bits with exponent at least 65537")
		}
		return "1.2.840.113549.1.1.11", "2.16.840.1.101.3.4.2.1", nil
	case *ecdsa.PublicKey:
		if sm2.IsSM2PublicKey(key) {
			return signMethodSM2SM3, signDigestSM3, nil
		}
		if key.Curve != elliptic.P256() && key.Curve != elliptic.P384() && key.Curve != elliptic.P521() {
			return "", "", fmt.Errorf("unsupported ECDSA signature curve")
		}
		return "1.2.840.10045.4.3.2", "2.16.840.1.101.3.4.2.1", nil
	default:
		return "", "", fmt.Errorf("unsupported signature public key")
	}
}

// signatureWriteTrust 校验证书用途、有效期和调用方显式指定的信任链
// 入参: cert 证书, at 验证时间, options 签署选项
// 返回: error 错误信息
func signatureWriteTrust(cert *smx509.Certificate, at time.Time, options SignatureWriteOptions) error {
	if cert.KeyUsage != 0 && cert.KeyUsage&(smx509.KeyUsageDigitalSignature|smx509.KeyUsageContentCommitment) == 0 {
		return fmt.Errorf("certificate does not authorize digital signatures")
	}
	if len(options.TrustRoots) == 0 {
		return fmt.Errorf("explicit signature trust roots are required")
	}
	return verifySignatureCertificateChain(cert, options.Intermediates, options.TrustRoots, at, smx509.ExtKeyUsageAny)
}

// sign 编码数字签名或SES v4签章
// 入参: data 最终Signature.xml原文, property 包内绝对路径, options 签署选项
// 返回: []byte 签名值, error 错误信息
func (key *signatureWriteKey) sign(data []byte, property string, options SignatureWriteOptions) ([]byte, error) {
	if len(options.Seal) == 0 {
		if key.method != signMethodSM2SM3 {
			return key.signMessage(data, options.Signer)
		}
		digest := sm3.Sum(data)
		signature, err := key.signMessage(digest[:], options.Signer)
		if err != nil {
			return nil, err
		}
		return signatureWriteSignedData(key.cert, digest[:], signature)
	}
	digest := sm3.Sum(data)
	tbs, err := asn1.Marshal(struct {
		Version  int
		Seal     asn1.RawValue
		Time     time.Time `asn1:"generalized"`
		Hash     asn1.BitString
		Property string `asn1:"ia5"`
	}{4, asn1.RawValue{FullBytes: options.Seal}, options.Time, asn1.BitString{Bytes: digest[:], BitLength: 256}, property})
	if err != nil {
		return nil, err
	}
	signature, err := key.signMessage(tbs, options.Signer)
	if err != nil {
		return nil, err
	}
	return asn1.Marshal(struct {
		TBS         asn1.RawValue
		Certificate []byte
		Algorithm   asn1.ObjectIdentifier
		Signature   asn1.BitString
	}{asn1.RawValue{FullBytes: tbs}, key.cert.Raw, asn1.ObjectIdentifier{1, 2, 156, 10197, 1, 501}, asn1.BitString{Bytes: signature, BitLength: len(signature) * 8}})
}

// signMessage 通过crypto.Signer调用成熟实现或硬件签署器
// SM2签署器必须接受gmsm的SM2SignerOption，接收原文并计算默认用户标识的ZA
// 入参: data 原文, signer 签署器
// 返回: []byte 签名值, error 错误信息
func (key *signatureWriteKey) signMessage(data []byte, signer crypto.Signer) ([]byte, error) {
	if key.method == signMethodSM2SM3 {
		return signer.Sign(rand.Reader, data, sm2.NewSM2SignerOption(true, nil))
	}
	digest := sha256.Sum256(data)
	return signer.Sign(rand.Reader, digest[:], crypto.SHA256)
}

// signatureWriteSignedData 编码带签署证书的SM2数字签名消息
// 入参: cert 证书, digest 文档描述摘要, signature SM2签名值
// 返回: []byte SignedData编码, error 错误信息
func signatureWriteSignedData(cert *smx509.Certificate, digest, signature []byte) ([]byte, error) {
	type contentInfo struct {
		Type    asn1.ObjectIdentifier
		Content asn1.RawValue
	}
	type signerInfo struct {
		Version         int
		IssuerAndSerial struct {
			Issuer asn1.RawValue
			Serial *big.Int
		}
		Digest    pkix.AlgorithmIdentifier
		Algorithm pkix.AlgorithmIdentifier
		Signature []byte
	}
	digestAlg := pkix.AlgorithmIdentifier{Algorithm: asn1.ObjectIdentifier{1, 2, 156, 10197, 1, 401}, Parameters: asn1.NullRawValue}
	signer := signerInfo{Version: 1, Digest: digestAlg, Algorithm: pkix.AlgorithmIdentifier{Algorithm: asn1.ObjectIdentifier{1, 2, 156, 10197, 1, 301, 1}}, Signature: signature}
	signer.IssuerAndSerial.Issuer = asn1.RawValue{FullBytes: cert.RawIssuer}
	signer.IssuerAndSerial.Serial = cert.SerialNumber
	content, err := asn1.Marshal(digest)
	if err != nil {
		return nil, err
	}
	sd, err := asn1.Marshal(struct {
		Version      int
		Digests      []pkix.AlgorithmIdentifier `asn1:"set"`
		Content      contentInfo
		Certificates asn1.RawValue
		Signers      []signerInfo `asn1:"set"`
	}{1, []pkix.AlgorithmIdentifier{digestAlg}, contentInfo{asn1.ObjectIdentifier{1, 2, 156, 10197, 6, 1, 4, 2, 1}, asn1.RawValue{Class: 2, Tag: 0, IsCompound: true, Bytes: content}}, asn1.RawValue{Class: 2, Tag: 0, IsCompound: true, Bytes: cert.Raw}, []signerInfo{signer}})
	if err != nil {
		return nil, err
	}
	return asn1.Marshal(contentInfo{asn1.ObjectIdentifier{1, 2, 156, 10197, 6, 1, 4, 2, 2}, asn1.RawValue{Class: 2, Tag: 0, IsCompound: true, Bytes: sd}})
}
