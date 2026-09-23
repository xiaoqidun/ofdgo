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
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/rand"
	"fmt"

	"github.com/emmansun/gmsm/padding"
	"github.com/emmansun/gmsm/sm2"
	"github.com/emmansun/gmsm/sm3"
	"github.com/emmansun/gmsm/sm4"
	"github.com/emmansun/gmsm/smx509"
)

// GMCryptoProvider 使用纯Go国密实现，可在原生环境和WASM使用
type GMCryptoProvider struct{}

// EncryptSM4 使用SM4-CBC及PKCS#7填充加密
// 入参: key 密钥, iv 初始向量, plaintext 明文
// 返回: []byte 密文, error 错误信息
func (GMCryptoProvider) EncryptSM4(key, iv, plaintext []byte) ([]byte, error) {
	block, err := sm4.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(iv) != block.BlockSize() {
		return nil, fmt.Errorf("invalid SM4 IV length")
	}
	out := padding.NewPKCS7Padding(sm4.BlockSize).Pad(bytes.Clone(plaintext))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, out)
	return out, nil
}

// DecryptSM4 解密SM4-CBC并校验PKCS#7填充
// 入参: key 密钥, iv 初始向量, ciphertext 密文
// 返回: []byte 明文, error 错误信息
func (GMCryptoProvider) DecryptSM4(key, iv, ciphertext []byte) ([]byte, error) {
	block, err := sm4.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(iv) != block.BlockSize() || len(ciphertext) == 0 || len(ciphertext)%block.BlockSize() != 0 {
		return nil, ErrInvalidCredentials
	}
	out := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, ciphertext)
	plain, err := padding.NewPKCS7Padding(sm4.BlockSize).ConstantTimeUnpad(out)
	if err != nil {
		clear(out)
		return nil, ErrInvalidCredentials
	}
	return plain, nil
}

// PasswordKey 按GB/T32918派生16字节口令密钥
// 入参: password UTF-8口令
// 返回: []byte 密钥, error 错误信息
func (GMCryptoProvider) PasswordKey(password []byte) ([]byte, error) {
	if len(password) == 0 {
		return nil, ErrCredentialsRequired
	}
	return sm3.Kdf(password, sm4.BlockSize), nil
}

// EncryptKey 使用SM2公钥证书封装文件密钥
// 入参: certificate DER证书, key 文件密钥
// 返回: []byte ASN.1密文, error 错误信息
func (GMCryptoProvider) EncryptKey(certificate, key []byte) ([]byte, error) {
	cert, err := parseEncryptionCertificate(certificate)
	if err != nil {
		return nil, err
	}
	return sm2.EncryptASN1(rand.Reader, cert.PublicKey.(*ecdsa.PublicKey), key)
}

// ParseEncryptionCertificate 解析单张SM2加密证书并返回DER副本
// 仅检查格式、算法和密钥用途，接收者身份及信任应由调用方确认
// 入参: certificate PEM或DER证书
// 返回: []byte DER证书, error 错误信息
func ParseEncryptionCertificate(certificate []byte) ([]byte, error) {
	cert, err := parseEncryptionCertificate(certificate)
	if err != nil {
		return nil, err
	}
	return bytes.Clone(cert.Raw), nil
}

// parseEncryptionCertificate 解析并检查加密证书
// 入参: certificate PEM或DER证书
// 返回: *smx509.Certificate 证书, error 错误信息
func parseEncryptionCertificate(certificate []byte) (*smx509.Certificate, error) {
	cert, err := parseSignatureCertificate(certificate)
	if err != nil {
		return nil, err
	}
	public, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !sm2.IsSM2PublicKey(public) {
		return nil, fmt.Errorf("SM2 encryption certificate required")
	}
	if cert.KeyUsage != 0 && cert.KeyUsage&(smx509.KeyUsageKeyEncipherment|smx509.KeyUsageDataEncipherment|smx509.KeyUsageKeyAgreement) == 0 {
		return nil, fmt.Errorf("certificate does not permit encryption")
	}
	return cert, nil
}
