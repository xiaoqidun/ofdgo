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
	"errors"
)

// 加密错误可通过errors.Is识别，不包含口令或私钥
var (
	ErrCredentialsRequired      = errors.New("document credentials required")
	ErrInvalidCredentials       = errors.New("invalid credentials or damaged encrypted document")
	ErrUnsupportedEncryption    = errors.New("unsupported document encryption")
	ErrEncryptionPolicyRequired = errors.New("explicit encryption policy required")
)

// Credentials 解密凭据，UserName为空时尝试匹配用户；证书采用DER编码
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

// ReaderOption 阅读器的凭据与密码提供者选项
type ReaderOption func(*readerOptions)

// readerOptions 保存打开文档时的会话参数
type readerOptions struct {
	credentials []Credentials
	provider    CryptoProvider
}

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

// EncryptionRecipient 证书加密接收者，Certificate为SM2公钥证书的DER编码
type EncryptionRecipient struct {
	UserName    string
	UserType    string
	Certificate []byte
}

// EncryptionOptions 设置GM/T0099加密，口令与证书方案二选一
// 口令方案应使用足够长的随机口令；标准密钥派生不提供慢速口令哈希
type EncryptionOptions struct {
	Password   []byte
	UserName   string
	UserType   string
	Recipients []EncryptionRecipient
	Provider   CryptoProvider
}

// EncryptionInfo 不含敏感凭据的文档加密信息
type EncryptionInfo struct {
	Encrypted bool
	Method    string
	Users     []string
	Layers    int
}

// encryptionState 保存解密来源与可继承的输出策略
type encryptionState struct {
	info    EncryptionInfo
	options *EncryptionOptions
}

// Encryption 获取文档加密状态，不暴露口令或私钥
// 返回: EncryptionInfo 加密状态
func (r *Reader) Encryption() EncryptionInfo {
	if r.encryption == nil {
		return EncryptionInfo{}
	}
	info := r.encryption.info
	info.Users = append([]string(nil), info.Users...)
	return info
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
