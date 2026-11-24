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
	"sync/atomic"

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
// Resources可选指定读取与单条目大小上限策略
type EncryptionOptions struct {
	Password   []byte
	UserName   string
	UserType   string
	Recipients []EncryptionRecipient
	Provider   CryptoProvider
	Resources  *Resources
}

// EncryptionInfo 不含敏感凭据的文档加密信息
// PolicyRequired表示无法继承原策略，保存前需显式设置加密方式
// Methods按从内到外顺序保留全部加密层的方案标识
type EncryptionInfo struct {
	Encrypted      bool
	Method         string
	Methods        []string
	Users          []string
	Layers         int
	PolicyRequired bool
}

// GMCryptoProvider 使用纯Go国密实现，可在原生环境和WASM使用
type GMCryptoProvider struct{}

// readerOptions 保存打开文档时的会话参数
type readerOptions struct {
	resources        *Resources
	credentials      []Credentials
	provider         CryptoProvider
	closer           io.Closer
	documentIndex    int
	hasDocumentIndex bool
	versionID        *string
}

// encryptionLease 管理加密策略口令缓冲的原子引用计数并在末次释放时清零
type encryptionLease struct {
	refs     atomic.Int32
	password []byte
}

// encryptedKeyPool 管理同一解密源及其派生视图的SM4密钥原子引用计数并在末次释放时清零
type encryptedKeyPool struct {
	refs atomic.Int32
	keys [][]byte
}

// encryptionHold 绑定单个Reader或Editor实例持有的口令与SM4密钥租约，保证释放幂等且并发安全
type encryptionHold struct {
	closed atomic.Bool
	pass   *encryptionLease
	keys   *encryptedKeyPool
}

// encryptionState 保存解密来源与可继承的输出策略
type encryptionState struct {
	info    EncryptionInfo
	options *EncryptionOptions
	source  *encryptedPackageSource
	lease   *encryptionLease
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

// WithCloser 将额外资源的关闭职责交由Reader.Close接管，打开失败时不关闭
// 入参: closer 关闭器
// 返回: ReaderOption 阅读选项
func WithCloser(closer io.Closer) ReaderOption {
	return func(options *readerOptions) { options.closer = closer }
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

// newEncryptionLease 为口令缓冲创建初始引用计数为1的原子租约
// 入参: password 待托管的口令字节切片
// 返回: *encryptionLease 口令租约，空口令返回nil
func newEncryptionLease(password []byte) *encryptionLease {
	if len(password) == 0 {
		return nil
	}
	lease := &encryptionLease{password: password}
	lease.refs.Store(1)
	return lease
}

// newEncryptedKeyPool 为SM4密钥集合创建初始引用计数为1的原子密钥池
// 入参: keys 待托管的SM4密钥列表
// 返回: *encryptedKeyPool 密钥池，无密钥返回nil
func newEncryptedKeyPool(keys [][]byte) *encryptedKeyPool {
	if len(keys) == 0 {
		return nil
	}
	pool := &encryptedKeyPool{keys: keys}
	pool.refs.Store(1)
	return pool
}

// retainEncryptionHold 为新持有者递增口令与SM4密钥租约计数并返回独立释放句柄
// 入参: pass 口令租约, keys SM4密钥池
// 返回: *encryptionHold 独立租约句柄，两者皆空返回nil
func retainEncryptionHold(pass *encryptionLease, keys *encryptedKeyPool) *encryptionHold {
	if pass == nil && keys == nil {
		return nil
	}
	pass.retain()
	keys.retain()
	return &encryptionHold{pass: pass, keys: keys}
}

// newEncryptionState 创建加密状态并为非空口令策略建立初始租约
// 入参: info 加密元信息, options 加密策略, source 加密包源
// 返回: *encryptionState 加密状态
func newEncryptionState(info EncryptionInfo, options *EncryptionOptions, source *encryptedPackageSource) *encryptionState {
	var lease *encryptionLease
	if options != nil {
		lease = newEncryptionLease(options.Password)
	}
	return &encryptionState{
		info:    info,
		options: options,
		source:  source,
		lease:   lease,
	}
}

// cloneEncryptionState 克隆加密状态及策略口令副本，隔离阅读器与编辑器的生命周期
// 入参: state 原加密状态
// 返回: *encryptionState 独立加密状态副本
func cloneEncryptionState(state *encryptionState) *encryptionState {
	if state == nil {
		return nil
	}
	return newEncryptionState(cloneEditorData(state.info), cloneEncryptionOptions(state.options), state.source)
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

// retain 递增口令租约的引用计数
func (l *encryptionLease) retain() {
	if l != nil {
		l.refs.Add(1)
	}
}

// release 递减口令租约引用计数并在末次释放时清零口令缓冲
func (l *encryptionLease) release() {
	if l != nil && l.refs.Add(-1) == 0 {
		clear(l.password)
	}
}

// retain 递增SM4密钥池的引用计数
func (p *encryptedKeyPool) retain() {
	if p != nil {
		p.refs.Add(1)
	}
}

// release 递减SM4密钥池引用计数并在末次释放时清零全部SM4密钥
func (p *encryptedKeyPool) release() {
	if p != nil && p.refs.Add(-1) == 0 {
		for _, k := range p.keys {
			clear(k)
		}
	}
}

// release 幂等释放单个持有者占用的口令与SM4密钥租约
func (h *encryptionHold) release() {
	if h == nil || !h.closed.CompareAndSwap(false, true) {
		return
	}
	h.pass.release()
	h.keys.release()
}

// bindEncryptionHold 为当前阅读器绑定独立的口令与SM4密钥租约，先增后减避免误清零
func (r *Reader) bindEncryptionHold() {
	if r == nil {
		return
	}
	var pass *encryptionLease
	if r.encryption != nil {
		pass = r.encryption.lease
	}
	var keys *encryptedKeyPool
	if src := r.encryptedSource(); src != nil {
		keys = src.keys
	}
	if r.encHold != nil && !r.encHold.closed.Load() && r.encHold.pass == pass && r.encHold.keys == keys {
		return
	}
	next := retainEncryptionHold(pass, keys)
	old := r.encHold
	r.encHold = next
	old.release()
}

// adoptEncryptionHold 接管当前阅读器刚创建的初始口令与SM4密钥租约，不额外递增计数
func (r *Reader) adoptEncryptionHold() {
	if r == nil {
		return
	}
	var pass *encryptionLease
	if r.encryption != nil {
		pass = r.encryption.lease
	}
	var keys *encryptedKeyPool
	if src := r.encryptedSource(); src != nil {
		keys = src.keys
	}
	var next *encryptionHold
	if pass != nil || keys != nil {
		next = &encryptionHold{pass: pass, keys: keys}
	}
	old := r.encHold
	r.encHold = next
	old.release()
}

// syncEncryptionHold 同步编辑器持有的口令与源包SM4密钥租约，先增后减避免误清零
func (e *Editor) syncEncryptionHold() {
	if e == nil {
		return
	}
	var pass *encryptionLease
	if e.encryption != nil {
		pass = e.encryption.lease
	}
	var keys *encryptedKeyPool
	if e.source != nil && e.source.reader != nil {
		if src := e.source.reader.encryptedSource(); src != nil {
			keys = src.keys
		}
	}
	if keys == nil && e.ownedReader != nil {
		if src := e.ownedReader.encryptedSource(); src != nil {
			keys = src.keys
		}
	}
	if keys == nil && e.encHold != nil && !e.encHold.closed.Load() {
		keys = e.encHold.keys
	}
	if e.encHold != nil && !e.encHold.closed.Load() && e.encHold.pass == pass && e.encHold.keys == keys {
		return
	}
	next := retainEncryptionHold(pass, keys)
	old := e.encHold
	e.encHold = next
	old.release()
}

// SetEncryption 设置后续保存的加密策略，nil显式选择明文保存
// 打开单用户口令或证书加密文档时默认继承原策略；多人口令和多层加密需显式选择输出策略
// 新的加密密钥与初始向量在每次保存时独立生成，设置不改变文档内容或撤销历史
// 入参: options 加密策略
// 返回: error 错误信息
func (e *Editor) SetEncryption(options *EncryptionOptions) error {
	if options == nil {
		e.encryption = nil
		e.syncEncryptionHold()
		return nil
	}
	if err := validateEncryptionOptions(*options); err != nil {
		return err
	}
	info := EncryptionInfo{Encrypted: true, Method: "1.1.1", Methods: []string{"1.1.1"}, Layers: 1}
	if len(options.Password) != 0 {
		info.Users = []string{options.UserName}
	} else {
		info.Method = "1.1.2"
		info.Methods = []string{"1.1.2"}
		for _, recipient := range options.Recipients {
			info.Users = append(info.Users, recipient.UserName)
		}
	}
	next := newEncryptionState(info, cloneEncryptionOptions(options), nil)
	e.encryption = next
	e.syncEncryptionHold()
	next.lease.release()
	return nil
}

// summary 复制不含凭据的加密状态
// 返回: EncryptionInfo 加密信息摘要
func (s *encryptionState) summary() EncryptionInfo {
	if s == nil {
		return EncryptionInfo{}
	}
	info := s.info
	info.PolicyRequired = s.options == nil
	info.Methods = append([]string(nil), info.Methods...)
	info.Users = append([]string(nil), info.Users...)
	return info
}

// writeEncrypted 在全部准备及加密成功后写出结果，取消不产生输出
// 输出流自身写入失败时调用方仍需丢弃本次输出
// 入参: writer 目标写入器
// 返回: int64 写入字节数, error 写入错误
func (e *Editor) writeEncrypted(writer io.Writer) (int64, error) {
	if e.encryption.options == nil {
		return 0, ErrEncryptionPolicyRequired
	}
	if err := validateEncryptionOptions(*e.encryption.options); err != nil {
		return 0, err
	}
	ctx := context.Background()
	if e.output != nil && e.output.ctx != nil {
		ctx = e.output.ctx
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	res := e.encryption.options.Resources
	if res == nil {
		res = e.runtimeResources
	}
	limit := int64(editorSnapshotArchiveLimit)
	if res != nil && res.options.SnapshotBytes > 0 {
		limit = res.options.SnapshotBytes
	}
	plain := editorSnapshotBuffer{spillLimit: limit, encryptSpill: true}
	defer func() { _ = plain.Close() }()
	if _, err := e.writePlaintext(&plain); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var readerOpts []ReaderOption
	if res != nil {
		readerOpts = append(readerOpts, WithResources(res))
	}
	r, err := NewReaderContext(ctx, &Input{reader: &plain, size: plain.size}, readerOpts...)
	if err != nil {
		return 0, err
	}
	defer r.Close()
	out := editorSnapshotBuffer{spillLimit: limit}
	defer func() { _ = out.Close() }()
	if _, err := encryptPackageToWriter(ctx, &out, r, *e.encryption.options, editorProgress(e.OnWriteProgress).withContext(ctx)); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var count int64
	output := convertWriter{ctx: ctx, writer: writer, count: &count}
	_, err = io.Copy(output, io.NewSectionReader(&out, 0, out.size))
	return count, err
}
