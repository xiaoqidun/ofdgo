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
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"github.com/emmansun/gmsm/padding"
	"github.com/emmansun/gmsm/sm2"
	"github.com/emmansun/gmsm/sm3"
	"github.com/emmansun/gmsm/sm4"
	"github.com/emmansun/gmsm/smx509"
)

// 加密错误可通过errors.Is识别，不包含口令或私钥
var (
	ErrCredentialsRequired      = errors.New("document credentials required")
	ErrInvalidCredentials       = errors.New("invalid credentials or damaged encrypted document")
	ErrUnsupportedEncryption    = errors.New("unsupported document encryption")
	ErrEncryptionPolicyRequired = errors.New("explicit encryption policy required")
)

// Credentials 解密凭据，UserName用于筛选接收者，留空自动匹配，不参与口令派生；证书采用DER编码
// Password与Decrypter分别用于口令和证书方案，私钥可由外部密码设备持有
type Credentials struct {
	UserName    string
	Password    []byte
	Certificate []byte
	Decrypter   crypto.Decrypter
}

// CryptoProvider 提供GM/T0099的密码运算，不依赖渲染后端
// SM4采用CBC模式及PKCS#7填充，PasswordKey采用GB/T32918的SM3密钥派生
// EncryptKey返回SM2密文的ASN.1编码；签名和私钥解密使用crypto.Signer与crypto.Decrypter
// 提供者不得修改输入字节，返回结果由调用方持有
type CryptoProvider interface {
	EncryptSM4(key, iv, plaintext []byte) ([]byte, error)
	DecryptSM4(key, iv, ciphertext []byte) ([]byte, error)
	PasswordKey(password []byte) ([]byte, error)
	EncryptKey(certificate, key []byte) ([]byte, error)
}

// ReaderOption 阅读器的凭据、密码提供者与资源选项
type ReaderOption func(*readerOptions)

// EncryptionRecipient 证书加密接收者，Certificate为SM2公钥证书的DER编码
type EncryptionRecipient struct {
	UserName    string
	UserType    string
	Certificate []byte
}

// EncryptionOptions 设置GM/T0099加密，口令与证书方案二选一
// UserName标记口令接收者，不参与口令派生，留空不写入用户名
// 口令方案应使用足够长的随机口令；标准密钥派生不提供慢速口令哈希
type EncryptionOptions struct {
	Password   []byte
	UserName   string
	UserType   string
	Recipients []EncryptionRecipient
	Provider   CryptoProvider
}

// EncryptionInfo 不含敏感凭据的文档加密信息
// PolicyRequired表示无法继承原策略，保存前需显式设置加密方式
type EncryptionInfo struct {
	Encrypted      bool
	Method         string
	Users          []string
	Layers         int
	PolicyRequired bool
}

// GMCryptoProvider 使用纯Go国密实现，可在原生环境和WASM使用
type GMCryptoProvider struct{}

// WithCredentials 添加解密凭据，多次调用可解开多层加密
// 入参: credentials 解密凭据
// 返回: ReaderOption 阅读选项
func WithCredentials(credentials Credentials) ReaderOption {
	credentials.Password = bytes.Clone(credentials.Password)
	credentials.Certificate = bytes.Clone(credentials.Certificate)
	return func(options *readerOptions) {
		copy := credentials
		copy.Password = bytes.Clone(credentials.Password)
		copy.Certificate = bytes.Clone(credentials.Certificate)
		options.credentials = append(options.credentials, copy)
	}
}

// WithCryptoProvider 设置文档解密的密码提供者
// 入参: provider 密码提供者，nil使用内置实现
// 返回: ReaderOption 阅读选项
func WithCryptoProvider(provider CryptoProvider) ReaderOption {
	return func(options *readerOptions) { options.provider = provider }
}

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

// Encryption 获取文档加密状态，不暴露口令或私钥
// 返回: EncryptionInfo 加密状态
func (r *Reader) Encryption() EncryptionInfo {
	return r.encryption.summary()
}

// Encryption 获取当前保存策略，不暴露口令或私钥
// 返回: EncryptionInfo 加密状态
func (e *Editor) Encryption() EncryptionInfo {
	return e.encryption.summary()
}

// SetEncryption 设置后续保存的加密策略，nil显式选择明文保存
// 打开单用户口令或证书加密文档时默认继承原策略；多人口令和多层加密需显式选择输出策略
// 新的加密密钥与初始向量在每次保存时独立生成，设置不改变文档内容或撤销历史
// 入参: options 加密策略
// 返回: error 错误信息
func (e *Editor) SetEncryption(options *EncryptionOptions) error {
	if options == nil {
		e.encryption = nil
		return nil
	}
	if err := validateEncryptionOptions(*options); err != nil {
		return err
	}
	info := EncryptionInfo{Encrypted: true, Method: "1.1.1", Layers: 1}
	if len(options.Password) != 0 {
		info.Users = []string{options.UserName}
	} else {
		info.Method = "1.1.2"
		for _, recipient := range options.Recipients {
			info.Users = append(info.Users, recipient.UserName)
		}
	}
	e.encryption = &encryptionState{info: info, options: cloneEncryptionOptions(options)}
	return nil
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

// readerOptions 保存打开文档时的会话参数
type readerOptions struct {
	resources        *Resources
	credentials      []Credentials
	provider         CryptoProvider
	documentIndex    int
	hasDocumentIndex bool
	versionID        *string
}

// encryptionState 保存解密来源与可继承的输出策略
type encryptionState struct {
	info    EncryptionInfo
	options *EncryptionOptions
	source  *encryptedPackageSource
}

// summary 复制不含凭据的加密状态
// 返回: EncryptionInfo 加密状态
func (s *encryptionState) summary() EncryptionInfo {
	if s == nil {
		return EncryptionInfo{}
	}
	info := s.info
	info.PolicyRequired = s.options == nil
	info.Users = append([]string(nil), info.Users...)
	return info
}

// writeEncrypted 在全部准备及加密成功后写出结果，取消不产生输出
// 输出流自身写入失败时调用方仍需丢弃本次输出
// 入参: writer 输出流
// 返回: int64 写入字节数, error 错误信息
func (e *Editor) writeEncrypted(writer io.Writer) (int64, error) {
	if e.encryption.options == nil {
		return 0, ErrEncryptionPolicyRequired
	}
	if err := validateEncryptionOptions(*e.encryption.options); err != nil {
		return 0, err
	}
	var plain editorSnapshotBuffer
	defer func() { _ = plain.Close() }()
	if _, err := e.writePlaintext(&plain); err != nil {
		return 0, err
	}
	r, err := NewReader(&Input{reader: &plain, size: plain.size})
	if err != nil {
		return 0, err
	}
	defer r.Close()
	ctx := context.Background()
	if e.output != nil && e.output.ctx != nil {
		ctx = e.output.ctx
	}
	var out editorSnapshotBuffer
	defer func() { _ = out.Close() }()
	if _, err := encryptPackageToWriter(ctx, &out, r, *e.encryption.options, editorProgress(e.OnWriteProgress)); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return io.Copy(writer, io.NewSectionReader(&out, 0, out.size))
}

// cloneEncryptionOptions 复制可继承的加密策略，密码提供者保持共享
// 入参: options 加密策略
// 返回: *EncryptionOptions 独立策略副本
func cloneEncryptionOptions(options *EncryptionOptions) *EncryptionOptions {
	if options == nil {
		return nil
	}
	copy := *options
	copy.Password = bytes.Clone(options.Password)
	copy.Recipients = append([]EncryptionRecipient(nil), options.Recipients...)
	for i := range copy.Recipients {
		copy.Recipients[i].Certificate = bytes.Clone(copy.Recipients[i].Certificate)
	}
	return &copy
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
