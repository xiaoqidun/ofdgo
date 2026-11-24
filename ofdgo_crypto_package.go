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
	"archive/zip"
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/emmansun/gmsm/padding"
	"github.com/emmansun/gmsm/sm4"
)

// encryptedEntryLayer 记录单个密文条目的一层SM4解密参数
type encryptedEntryLayer struct {
	key []byte
	iv  []byte
}

// encryptedEntrySpec 描述逻辑路径对应的底层ZIP条目与从外到内的解密链
type encryptedEntrySpec struct {
	file   *zip.File
	layers []encryptedEntryLayer
}

// encryptedPackageSource 保存解密索引映射，构建后只读并支持并发按需流式解密
type encryptedPackageSource struct {
	provider   CryptoProvider
	entryLimit int64
	entries    map[string]encryptedEntrySpec
	plain      map[string]*zip.File
	keys       *encryptedKeyPool
}

// sm4CBCDecryptReader 对底层密文流按固定块流式执行SM4-CBC解密并校验PKCS#7填充
type sm4CBCDecryptReader struct {
	src   io.ReadCloser
	mode  cipher.BlockMode
	buf   []byte
	plain []byte
	rem   [sm4.BlockSize * 2]byte
	remN  int
	eof   bool
	err   error
}

// decryptNode 描述解密阶段单个逻辑条目的来源与已累积解密层
type decryptNode struct {
	raw    []byte
	file   *zip.File
	layers []encryptedEntryLayer
}

// encryptionList 对应GM/T0099的加密入口
type encryptionList struct {
	XMLName xml.Name           `xml:"http://www.ofdspec.org/2016 Encryptions"`
	Records []encryptionRecord `xml:"EncryptInfo"`
}

// encryptionRecord 关联密钥描述和明密文映射
type encryptionRecord struct {
	ID       string             `xml:"ID,attr"`
	Relative string             `xml:"Relative,attr,omitempty"`
	Provider encryptionProvider `xml:"Provider"`
	Scope    string             `xml:"EncryptScope"`
	Date     string             `xml:"EncryptDate,omitempty"`
	Seed     string             `xml:"DecryptSeedLoc"`
	Entries  string             `xml:"EntriesMapLoc"`
}

// encryptionProvider 标识生成加密包的组件
type encryptionProvider struct {
	Name    string `xml:"Name,attr"`
	Version string `xml:"Version,attr"`
}

// encryptionSeed 保存标准的文件密钥包装信息
type encryptionSeed struct {
	XMLName xml.Name          `xml:"http://www.ofdspec.org/2016 DecyptSeed"`
	ID      string            `xml:"ID,attr"`
	Method  string            `xml:"EncryptCaseId,attr"`
	Users   []encryptionUser  `xml:"UserInfo"`
	Params  *encryptionParams `xml:"ExtendParams,omitempty"`
}

// encryptionParams 保留未知扩展，避免默认另存时静默丢失其语义
type encryptionParams struct {
	Raw string `xml:",innerxml"`
}

// encryptionUser 保存单个接收者的公开信息及包装密钥
type encryptionUser struct {
	Name        string `xml:"UserName,attr,omitempty"`
	Role        string `xml:"UserType,attr,omitempty"`
	Certificate string `xml:"UserCert,omitempty"`
	Key         string `xml:"EncryptedWK"`
	IV          string `xml:"IVValue"`
}

// encryptionEntries 保存明密文映射
type encryptionEntries struct {
	XMLName xml.Name
	ID      string            `xml:"ID,attr"`
	Entries []encryptionEntry `xml:"EncryptEntry"`
}

// encryptionEntry 对应一项包内文件加密映射
type encryptionEntry struct {
	Path          string `xml:"Path,attr"`
	EncryptedPath string `xml:"EPath,attr"`
	Seed          string `xml:"DecryptSeedLoc,attr,omitempty"`
}

// unwrappedCertKey 标识单次解密会话内已解包的证书包装密钥缓存键
type unwrappedCertKey struct {
	credIndex int
	wrapped   string
}

// EncryptPackage 对完整OFD包进行GM/T0099加密，保留全部条目的原始内容
// 先签后加密可保留对明文的验签能力；出错不返回部分密文，已有加密包须先解密
// 入参: data 明文OFD包, options 加密策略
// 返回: []byte 加密OFD包, error 错误信息
func EncryptPackage(data []byte, options EncryptionOptions) ([]byte, error) {
	var output bytes.Buffer
	if _, err := EncryptPackageTo(context.Background(), &Input{reader: bytes.NewReader(data), size: int64(len(data))}, &output, options); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// EncryptPackageTo 按取消上下文对明文OFD输入执行流式GM/T0099加密并写出到目标流
// 先签后加密可保留对明文的验签能力；不会关闭输入或输出流，出错时应丢弃部分输出
// 入参: ctx 取消上下文，nil使用后台上下文, input 明文输入源, writer 目标输出流, options 加密策略
// 返回: int64 写出字节数, error 错误信息
func EncryptPackageTo(ctx context.Context, input *Input, writer io.Writer, options EncryptionOptions) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if input == nil {
		return 0, fmt.Errorf("missing input")
	}
	if writer == nil {
		return 0, fmt.Errorf("nil output writer")
	}
	if err := sameOutputFile(input.info, writer); err != nil {
		return 0, err
	}
	var readerOpts []ReaderOption
	if options.Resources != nil {
		readerOpts = append(readerOpts, WithResources(options.Resources))
	}
	r, err := NewReaderContext(ctx, input, readerOpts...)
	if err != nil {
		return 0, err
	}
	defer r.Close()
	return encryptPackageToWriter(ctx, writer, r, options, nil)
}

// newSM4CBCDecryptReader 构造流式SM4-CBC解密读取器
// 入参: src 密文输入流, key 16字节密钥, iv 16字节初始向量
// 返回: io.ReadCloser 明文流, error 密钥或向量错误
func newSM4CBCDecryptReader(src io.ReadCloser, key, iv []byte) (io.ReadCloser, error) {
	block, err := sm4.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(iv) != block.BlockSize() {
		return nil, ErrInvalidCredentials
	}
	return &sm4CBCDecryptReader{
		src:  src,
		mode: cipher.NewCBCDecrypter(block, iv),
		buf:  make([]byte, 32*1024+sm4.BlockSize),
	}, nil
}

// encryptedEntryReadLimit 计算密文读取时的单条目字节上限，为多层PKCS#7填充预留额外空间
// 入参: limit 明文单条目上限, layers 解密层数
// 返回: int64 密文单条目上限
func encryptedEntryReadLimit(limit int64, layers int) int64 {
	if limit <= 0 || layers <= 0 {
		return limit
	}
	extra := int64(layers) * sm4.BlockSize
	if limit > math.MaxInt64-extra {
		return math.MaxInt64
	}
	return limit + extra
}

// readLimitedZipEntry 按单条目字节上限读取ZIP条目原始数据，超限时返回ErrEntryTooLarge
// 入参: file ZIP条目, name 条目路径, limit 最大允许字节数，小于等于0表示不限制
// 返回: []byte 条目字节, error 读取或超限错误
func readLimitedZipEntry(file *zip.File, name string, limit int64) ([]byte, error) {
	if limit > 0 && file.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("package entry %s (%d bytes): %w", name, limit, ErrEntryTooLarge)
	}
	rc, err := file.Open()
	if err != nil {
		return nil, err
	}
	if limit > 0 {
		rc = &limitedEntryReader{ReadCloser: rc, name: name, remaining: limit, limit: limit}
	}
	data, readErr := io.ReadAll(rc)
	_ = rc.Close()
	if readErr != nil {
		return nil, readErr
	}
	return data, nil
}

// readDecryptNode 读取并逐层解密单个元数据节点字节
// 入参: node 逻辑节点, provider 密码提供者, name 条目路径, limit 单条目解压上限
// 返回: []byte 明文字节, error 读取、超限或解密错误
func readDecryptNode(node decryptNode, provider CryptoProvider, name string, limit int64) ([]byte, error) {
	var data []byte
	readLimit := encryptedEntryReadLimit(limit, len(node.layers))
	if node.file != nil {
		var err error
		data, err = readLimitedZipEntry(node.file, name, readLimit)
		if err != nil {
			return nil, err
		}
	} else {
		if readLimit > 0 && int64(len(node.raw)) > readLimit {
			return nil, fmt.Errorf("package entry %s (%d bytes): %w", name, readLimit, ErrEntryTooLarge)
		}
		data = bytes.Clone(node.raw)
	}
	for _, layer := range node.layers {
		plain, err := provider.DecryptSM4(layer.key, layer.iv, data)
		clear(data)
		if err != nil {
			return nil, ErrInvalidCredentials
		}
		data = plain
	}
	if limit > 0 && int64(len(data)) > limit {
		clear(data)
		return nil, fmt.Errorf("package entry %s (%d bytes): %w", name, limit, ErrEntryTooLarge)
	}
	return data, nil
}

// encryptionNode 按ST_Loc规则及大小写敏感路径查找加密条目节点
// 入参: nodes 包内节点映射, base 基准XML路径, name 引用路径
// 返回: decryptNode 节点, string 实际路径, error 错误信息
func encryptionNode(nodes map[string]decryptNode, base, name string) (decryptNode, string, error) {
	name = encryptionPath(base, name)
	if !validPackagePath(name) {
		return decryptNode{}, "", fmt.Errorf("invalid encryption path: %q", name)
	}
	if node, ok := nodes[name]; ok {
		return node, name, nil
	}
	return decryptNode{}, "", fmt.Errorf("missing encrypted entry: %s", name)
}

// encodeWrapIVParams 构造仅含独立密钥包装初始向量参数的扩展节点
// 入参: wrapIV 包装初始向量
// 返回: *encryptionParams 扩展参数节点
func encodeWrapIVParams(wrapIV []byte) *encryptionParams {
	return &encryptionParams{Raw: `<Parameter Name="WrapIV">` + base64.StdEncoding.EncodeToString(wrapIV) + `</Parameter>`}
}

// parseEncryptionParams 解析扩展参数中的独立密钥包装初始向量并标记是否存在未知扩展
// 入参: params 扩展参数节点
// 返回: []byte 包装初始向量, bool 是否存在未知扩展
func parseEncryptionParams(params *encryptionParams) ([]byte, bool) {
	if params == nil || strings.TrimSpace(params.Raw) == "" {
		return nil, false
	}
	decoder := xml.NewDecoder(strings.NewReader(`<root xmlns="` + ofdNamespace2016 + `">` + params.Raw + `</root>`))
	depth := 0
	var wrapIV []byte
	unknown := false
	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, true
		}
		switch node := tok.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				continue
			}
			if depth == 2 && node.Name.Local == "Parameter" && (node.Name.Space == "" || node.Name.Space == ofdNamespace2016) {
				name := ""
				extraAttr := false
				for _, attr := range node.Attr {
					if attr.Name.Space == "" && attr.Name.Local == "Name" {
						name = attr.Value
					} else if attr.Name.Space != "xmlns" && attr.Name.Local != "xmlns" {
						extraAttr = true
					}
				}
				if !extraAttr && name == "WrapIV" && len(wrapIV) == 0 {
					var text string
					if err := decoder.DecodeElement(&text, &node); err != nil {
						return nil, true
					}
					depth--
					decoded, err := decodeEncryptionBase64(strings.TrimSpace(text))
					if err != nil || len(decoded) != sm4.BlockSize {
						return nil, true
					}
					wrapIV = decoded
					continue
				}
			}
			unknown = true
			if err := decoder.Skip(); err != nil {
				return nil, true
			}
			depth--
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 1 && len(bytes.TrimSpace(node)) != 0 {
				unknown = true
			}
		}
	}
	return wrapIV, unknown
}

// sameEncryptionSeedPolicy 判断条目级密钥描述是否与主密钥描述共享一致接收者策略
// 入参: base 主密钥描述, item 条目密钥描述
// 返回: bool 策略是否一致
func sameEncryptionSeedPolicy(base, item encryptionSeed) bool {
	_, unknown := parseEncryptionParams(item.Params)
	if base.Method != item.Method || unknown || len(base.Users) != len(item.Users) {
		return false
	}
	for i := range base.Users {
		if base.Users[i].Name != item.Users[i].Name || base.Users[i].Role != item.Users[i].Role || base.Users[i].Certificate != item.Users[i].Certificate {
			return false
		}
	}
	return true
}

// decodeEncryptionEntries 读取标准允许的明文或密文明密文映射表
// 入参: data 映射文件, key 文件密钥, iv 初始向量, provider 密码实现
// 返回: encryptionEntries 映射表, error 错误信息
func decodeEncryptionEntries(data, key, iv []byte, provider CryptoProvider) (encryptionEntries, error) {
	var entries encryptionEntries
	valid := func() bool {
		return entries.XMLName.Space == ofdNamespace2016 && (entries.XMLName.Local == "EncryptEntries" || entries.XMLName.Local == "EncryptedEntries")
	}
	if err := xml.Unmarshal(data, &entries); err == nil && valid() {
		return entries, nil
	}
	plain, err := provider.DecryptSM4(key, iv, data)
	if err != nil {
		return encryptionEntries{}, err
	}
	defer clear(plain)
	entries = encryptionEntries{}
	if err := xml.Unmarshal(plain, &entries); err != nil || !valid() {
		return encryptionEntries{}, ErrInvalidCredentials
	}
	return entries, nil
}

// orderEncryptionRecords 按前驱关系排序加密层，拒绝环和缺失的前驱
// 入参: records 加密记录
// 返回: []encryptionRecord 从内到外的顺序, error 错误信息
func orderEncryptionRecords(records []encryptionRecord) ([]encryptionRecord, error) {
	index := make(map[string]encryptionRecord, len(records))
	for _, record := range records {
		if _, exists := index[record.ID]; exists || record.ID == "" {
			return nil, fmt.Errorf("%w: duplicate encryption operation", ErrUnsupportedEncryption)
		}
		index[record.ID] = record
	}
	states := make(map[string]uint8, len(records))
	ordered := make([]encryptionRecord, 0, len(records))
	for _, record := range records {
		chain := []encryptionRecord{}
		for record.ID != "" && states[record.ID] != 2 {
			if states[record.ID] == 1 {
				return nil, fmt.Errorf("%w: cyclic encryption operations", ErrUnsupportedEncryption)
			}
			states[record.ID] = 1
			chain = append(chain, record)
			if record.Relative == "" {
				break
			}
			previous, ok := index[record.Relative]
			if !ok {
				return nil, fmt.Errorf("%w: missing encryption predecessor", ErrUnsupportedEncryption)
			}
			record = previous
		}
		for i := len(chain) - 1; i >= 0; i-- {
			states[chain[i].ID] = 2
			ordered = append(ordered, chain[i])
		}
	}
	return ordered, nil
}

// unwrapEncryptionKey 按方案尝试会话凭据并复用口令派生与证书解包密钥，单条损坏接收者条目跳过继续尝试
// 入参: seed 密钥描述, settings 会话参数, passwordKeys 口令派生密钥缓存, certKeys 证书解包密钥缓存
// 返回: []byte 文件密钥, []byte 初始向量, int 命中凭据索引, error 解密错误
func unwrapEncryptionKey(seed encryptionSeed, settings readerOptions, passwordKeys [][]byte, certKeys map[unwrappedCertKey][]byte) ([]byte, []byte, int, error) {
	if seed.Method != "1.1.1" && seed.Method != "1.1.2" {
		return nil, nil, -1, ErrUnsupportedEncryption
	}
	seedWrapIV, _ := parseEncryptionParams(seed.Params)
	for credIndex, credential := range settings.credentials {
		for _, user := range seed.Users {
			if credential.UserName != "" && credential.UserName != user.Name {
				continue
			}
			wrapped, err := decodeEncryptionBase64(user.Key)
			if err != nil {
				continue
			}
			iv, err := decodeEncryptionBase64(user.IV)
			if err == nil && len(iv) == 0 {
				iv = make([]byte, 16)
			}
			if err != nil || len(iv) != 16 {
				continue
			}
			var key, certificate []byte
			if seed.Method == "1.1.1" && len(credential.Password) != 0 {
				kek := passwordKeys[credIndex]
				if kek == nil {
					var keyErr error
					kek, keyErr = settings.provider.PasswordKey(credential.Password)
					if keyErr != nil {
						return nil, nil, -1, keyErr
					}
					passwordKeys[credIndex] = kek
				}
				wrapIV := iv
				if len(seedWrapIV) == sm4.BlockSize {
					wrapIV = seedWrapIV
				}
				key, err = settings.provider.DecryptSM4(kek, wrapIV, wrapped)
			} else if seed.Method == "1.1.2" && credential.Decrypter != nil {
				certificate, err = decodeEncryptionBase64(user.Certificate)
				if err != nil || len(certificate) == 0 {
					continue
				}
				if len(credential.Certificate) != 0 && !bytes.Equal(credential.Certificate, certificate) {
					continue
				}
				cacheKey := unwrappedCertKey{credIndex: credIndex, wrapped: user.Key}
				if cached, ok := certKeys[cacheKey]; ok {
					return bytes.Clone(cached), iv, credIndex, nil
				}
				key, err = credential.Decrypter.Decrypt(rand.Reader, wrapped, nil)
				if err == nil && len(key) == 16 && certKeys != nil {
					certKeys[cacheKey] = bytes.Clone(key)
				}
			} else {
				continue
			}
			if err == nil && len(key) == 16 {
				return key, iv, credIndex, nil
			}
			clear(key)
		}
	}
	return nil, nil, -1, ErrInvalidCredentials
}

// inheritedEncryptionOptions 保留能够完整恢复的原接收者策略，仅全包加密支持继承
// 入参: record 加密记录, seed 密钥描述, credential 命中凭据, provider 密码实现
// 返回: *EncryptionOptions 可继承的加密策略
func inheritedEncryptionOptions(record encryptionRecord, seed encryptionSeed, credential Credentials, provider CryptoProvider) *EncryptionOptions {
	_, unknown := parseEncryptionParams(seed.Params)
	if !strings.EqualFold(strings.TrimSpace(record.Scope), "All") || unknown {
		return nil
	}
	options := &EncryptionOptions{Provider: provider}
	if seed.Method == "1.1.1" {
		if len(seed.Users) != 1 {
			return nil
		}
		options.UserName, options.UserType, options.Password = seed.Users[0].Name, seed.Users[0].Role, bytes.Clone(credential.Password)
		return options
	}
	for _, user := range seed.Users {
		certificate, err := decodeEncryptionBase64(user.Certificate)
		if err != nil || len(certificate) == 0 {
			return nil
		}
		options.Recipients = append(options.Recipients, EncryptionRecipient{UserName: user.Name, UserType: user.Role, Certificate: certificate})
	}
	return options
}

// decodeEncryptionBase64 过滤ASCII空白字符后解码Base64数据
// 入参: value 编码文本
// 返回: []byte 解码字节, error 解码错误
func decodeEncryptionBase64(value string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.ReplaceAll(value, " ", ""), "\t", ""))
}

// streamEncryptSM4CBC 按固定块流式执行SM4-CBC加密及PKCS#7填充并直接写出目标流
// 入参: ctx 取消上下文, dst 密文写入器, src 明文读取器, key 加密密钥, iv 初始向量
// 返回: error 加密或取消错误
func streamEncryptSM4CBC(ctx context.Context, dst io.Writer, src io.Reader, key, iv []byte) error {
	block, err := sm4.NewCipher(key)
	if err != nil {
		return err
	}
	if len(iv) != block.BlockSize() {
		return fmt.Errorf("invalid SM4 IV length")
	}
	mode := cipher.NewCBCEncrypter(block, iv)
	buf := make([]byte, 32*1024)
	defer clear(buf)
	pending := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := src.Read(buf[pending:])
		if n > 0 {
			total := pending + n
			blocks := (total / sm4.BlockSize) * sm4.BlockSize
			if blocks > 0 {
				mode.CryptBlocks(buf[:blocks], buf[:blocks])
				if _, writeErr := dst.Write(buf[:blocks]); writeErr != nil {
					return writeErr
				}
				rem := total - blocks
				copy(buf[:rem], buf[blocks:total])
				pending = rem
			} else {
				pending = total
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	pad := byte(sm4.BlockSize - pending)
	for i := pending; i < sm4.BlockSize; i++ {
		buf[i] = pad
	}
	mode.CryptBlocks(buf[:sm4.BlockSize], buf[:sm4.BlockSize])
	_, err = dst.Write(buf[:sm4.BlockSize])
	return err
}

// encryptPackageToWriter 流式逐条加密阅读器中的包条目并直接写出ZIP归档
// 口令与证书模式均为主密钥包装、映射表与每个文件生成独立随机初始向量
// 入参: ctx 取消上下文, writer 目标写入器, r 文档阅读器, options 加密选项, progress 进度回调
// 返回: int64 写入字节数, error 加密或写入错误
func encryptPackageToWriter(ctx context.Context, writer io.Writer, r *Reader, options EncryptionOptions, progress editorProgress) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if r == nil {
		return 0, fmt.Errorf("nil encryption reader")
	}
	if err := validateEncryptionOptions(options); err != nil {
		return 0, err
	}
	if len(r.fileNames) == 0 {
		if err := r.indexPackageContext(ctx); err != nil {
			return 0, err
		}
	}
	provider := options.Provider
	if provider == nil {
		provider = GMCryptoProvider{}
	}
	_, isDefaultProvider := provider.(GMCryptoProvider)
	key, mapIV, identifier := make([]byte, 16), make([]byte, 16), make([]byte, 16)
	for _, buffer := range [][]byte{key, mapIV, identifier} {
		if _, err := rand.Read(buffer); err != nil {
			return 0, err
		}
	}
	defer clear(key)
	defer clear(mapIV)
	id := "E" + hex.EncodeToString(identifier)
	seed := encryptionSeed{ID: id, Method: "1.1.1"}
	var kek []byte
	passwordMode := len(options.Password) != 0
	if passwordMode {
		var err error
		kek, err = provider.PasswordKey(options.Password)
		if err != nil {
			return 0, err
		}
		defer clear(kek)
		wrapIV := make([]byte, 16)
		if _, err := rand.Read(wrapIV); err != nil {
			return 0, err
		}
		defer clear(wrapIV)
		wrapped, err := provider.EncryptSM4(kek, wrapIV, key)
		if err != nil {
			return 0, err
		}
		seed.Users = []encryptionUser{{Name: options.UserName, Role: options.UserType, Key: base64.StdEncoding.EncodeToString(wrapped), IV: base64.StdEncoding.EncodeToString(mapIV)}}
		seed.Params = encodeWrapIVParams(wrapIV)
	} else {
		seed.Method = "1.1.2"
		for _, recipient := range options.Recipients {
			certificate, err := ParseEncryptionCertificate(recipient.Certificate)
			if err != nil {
				return 0, err
			}
			wrapped, err := provider.EncryptKey(certificate, key)
			if err != nil {
				return 0, err
			}
			seed.Users = append(seed.Users, encryptionUser{Name: recipient.UserName, Role: recipient.UserType, Certificate: base64.StdEncoding.EncodeToString(certificate), Key: base64.StdEncoding.EncodeToString(wrapped), IV: base64.StdEncoding.EncodeToString(mapIV)})
		}
	}
	names := slices.Sorted(maps.Keys(r.fileNames))
	entries := encryptionEntries{XMLName: xml.Name{Space: ofdNamespace2016, Local: "EncryptEntries"}, ID: id, Entries: make([]encryptionEntry, 0, len(names))}
	entryIVs := make([][]byte, len(names))
	defer func() {
		for _, entryIV := range entryIVs {
			clear(entryIV)
		}
	}()
	for i, name := range names {
		encryptedName := fmt.Sprintf("Encrypt/Files/%d.dat", i)
		entryIV := make([]byte, 16)
		if _, err := rand.Read(entryIV); err != nil {
			return 0, err
		}
		entryIVs[i] = entryIV
		entry := encryptionEntry{Path: "/" + name, EncryptedPath: "/" + encryptedName, Seed: fmt.Sprintf("/Encrypt/Seeds/%d.dat", i)}
		entries.Entries = append(entries.Entries, entry)
	}
	plainMap, err := xml.Marshal(entries)
	if err != nil {
		return 0, err
	}
	mapData, err := provider.EncryptSM4(key, mapIV, plainMap)
	clear(plainMap)
	if err != nil {
		return 0, err
	}
	seedData, err := xml.Marshal(seed)
	if err != nil {
		return 0, err
	}
	list := encryptionList{Records: []encryptionRecord{{ID: id, Provider: encryptionProvider{Name: "OFDGo", Version: "1.0"}, Scope: "All", Date: time.Now().Format(time.RFC3339), Seed: "/Encrypt/DecryptSeed.dat", Entries: "/Encrypt/Entries.dat"}}}
	listData, err := xml.Marshal(list)
	if err != nil {
		return 0, err
	}
	if err := progress.report("encrypt", 0, len(names)); err != nil {
		return 0, err
	}
	var count int64
	counting := convertWriter{ctx: ctx, writer: writer, count: &count}
	archive := zip.NewWriter(counting)
	writeStoreEntry := func(entryName string, payload []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		dst, createErr := archive.CreateHeader(&zip.FileHeader{Name: entryName, Method: zip.Store})
		if createErr != nil {
			return createErr
		}
		_, writeErr := dst.Write(payload)
		return writeErr
	}
	if err := writeStoreEntry("Encrypt/DecryptSeed.dat", seedData); err != nil {
		return count, err
	}
	if err := writeStoreEntry("Encrypt/Entries.dat", mapData); err != nil {
		return count, err
	}
	for i, name := range names {
		if i > 0 {
			if err := progress.report("encrypt", i, len(names)); err != nil {
				return count, err
			}
		}
		if err := ctx.Err(); err != nil {
			return count, err
		}
		entryPath := fmt.Sprintf("Encrypt/Files/%d.dat", i)
		if isDefaultProvider {
			rc, openErr := r.openFile(name)
			if openErr != nil {
				return count, openErr
			}
			dst, createErr := archive.CreateHeader(&zip.FileHeader{Name: entryPath, Method: zip.Store})
			if createErr != nil {
				_ = rc.Close()
				return count, createErr
			}
			encErr := streamEncryptSM4CBC(ctx, dst, rc, key, entryIVs[i])
			closeErr := rc.Close()
			if encErr != nil {
				return count, encErr
			}
			if closeErr != nil {
				return count, closeErr
			}
			continue
		}
		_, borrowed := r.files[cleanPackagePath(name)]
		plain, readErr := r.readFileViewContext(ctx, name)
		if readErr != nil {
			return count, readErr
		}
		cipherData, encErr := provider.EncryptSM4(key, entryIVs[i], plain)
		if !borrowed {
			clear(plain)
		}
		if encErr != nil {
			return count, encErr
		}
		if err := writeStoreEntry(entryPath, cipherData); err != nil {
			return count, err
		}
	}
	for i, entryIV := range entryIVs {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		ivText := base64.StdEncoding.EncodeToString(entryIV)
		entrySeed := encryptionSeed{ID: id, Method: seed.Method}
		if passwordMode {
			entryWrapIV := make([]byte, 16)
			if _, randErr := rand.Read(entryWrapIV); randErr != nil {
				return count, randErr
			}
			wrapped, encErr := provider.EncryptSM4(kek, entryWrapIV, key)
			if encErr != nil {
				clear(entryWrapIV)
				return count, encErr
			}
			entrySeed.Users = []encryptionUser{{Name: options.UserName, Role: options.UserType, Key: base64.StdEncoding.EncodeToString(wrapped), IV: ivText}}
			entrySeed.Params = encodeWrapIVParams(entryWrapIV)
			clear(entryWrapIV)
		} else {
			entrySeed.Users = make([]encryptionUser, len(seed.Users))
			for j, user := range seed.Users {
				user.IV = ivText
				entrySeed.Users[j] = user
			}
		}
		entrySeedData, marshalErr := xml.Marshal(entrySeed)
		if marshalErr != nil {
			return count, marshalErr
		}
		if err := writeStoreEntry(fmt.Sprintf("Encrypt/Seeds/%d.dat", i), entrySeedData); err != nil {
			return count, err
		}
	}
	if err := writeStoreEntry("Encryptions.xml", listData); err != nil {
		return count, err
	}
	if err := archive.Close(); err != nil {
		return count, err
	}
	if err := progress.report("encrypt", len(names), len(names)); err != nil {
		return count, err
	}
	return count, ctx.Err()
}

// validateEncryptionOptions 校验输出方案，禁止空口令或混用口令与证书方案
// 入参: options 加密策略
// 返回: error 错误信息
func validateEncryptionOptions(options EncryptionOptions) error {
	if (len(options.Password) != 0) == (len(options.Recipients) != 0) {
		return fmt.Errorf("choose password or certificate encryption")
	}
	if options.UserType != "" && options.UserType != "User" && options.UserType != "Owner" {
		return fmt.Errorf("invalid encryption user type")
	}
	seen := make(map[string]bool)
	for _, recipient := range options.Recipients {
		if recipient.UserName == "" || seen[recipient.UserName] || len(recipient.Certificate) == 0 {
			return fmt.Errorf("invalid encryption recipient")
		}
		if recipient.UserType != "" && recipient.UserType != "User" && recipient.UserType != "Owner" {
			return fmt.Errorf("invalid encryption recipient type")
		}
		seen[recipient.UserName] = true
	}
	return nil
}

// encryptionPath 按ST_Loc规则相对基准XML解析包内路径并保留越界特征供校验拦截
// 入参: base 基准XML路径, loc 引用路径
// 返回: string 规范化路径
func encryptionPath(base, loc string) string {
	trimmed := strings.ReplaceAll(strings.TrimSpace(loc), "\\", "/")
	if strings.HasPrefix(trimmed, "/") {
		return cleanPackagePath(trimmed)
	}
	if trimmed == "" {
		return "."
	}
	return resolveResourcePath(base, "", trimmed)
}

// preserveInnerEncryptionRecord 保留全包解密层尚未处理的更内层加密元数据及明文映射引用的密文和条目种子节点
// 入参: dst 下一轮节点表, nodes 当前节点表, decrypted 本层解密节点表, record 更内层加密记录, provider 密码提供者, limit 单条目解压上限
// 返回: bool 内层映射表是否已按明文XML完整解析
func preserveInnerEncryptionRecord(dst, nodes, decrypted map[string]decryptNode, record encryptionRecord, provider CryptoProvider, limit int64) bool {
	for _, meta := range []string{record.Seed, record.Entries} {
		metaName := encryptionPath("Encryptions.xml", meta)
		if validPackagePath(metaName) {
			if metaNode, ok := nodes[metaName]; ok {
				dst[metaName] = metaNode
			}
		}
	}
	entriesName := encryptionPath("Encryptions.xml", record.Entries)
	if !validPackagePath(entriesName) {
		return false
	}
	entriesNode, ok := decrypted[entriesName]
	if !ok {
		entriesNode, ok = nodes[entriesName]
	}
	if !ok {
		return false
	}
	entriesData, err := readDecryptNode(entriesNode, provider, entriesName, limit)
	if err != nil {
		return false
	}
	var innerEntries encryptionEntries
	if xml.Unmarshal(entriesData, &innerEntries) != nil || innerEntries.XMLName.Space != ofdNamespace2016 || (innerEntries.XMLName.Local != "EncryptEntries" && innerEntries.XMLName.Local != "EncryptedEntries") {
		return false
	}
	for _, entry := range innerEntries.Entries {
		for _, ref := range []string{entry.EncryptedPath, entry.Seed} {
			if strings.TrimSpace(ref) == "" {
				continue
			}
			refName := encryptionPath(entriesName, ref)
			if validPackagePath(refName) {
				if refNode, exists := nodes[refName]; exists {
					dst[refName] = refNode
				}
			}
		}
	}
	return true
}

// clearKeys 清理加密源中保存的全部SM4解密密钥缓冲
func (s *encryptedPackageSource) clearKeys() {
	if s == nil {
		return
	}
	if s.keys != nil {
		for _, key := range s.keys.keys {
			clear(key)
		}
		return
	}
	for _, spec := range s.entries {
		for i := range spec.layers {
			clear(spec.layers[i].key)
		}
	}
}

// has 判断逻辑路径是否存在于加密包映射中
// 入参: name 包内路径
// 返回: bool 是否存在
func (s *encryptedPackageSource) has(name string) bool {
	if s == nil {
		return false
	}
	clean := cleanPackagePath(name)
	if _, ok := s.plain[clean]; ok {
		return true
	}
	_, ok := s.entries[clean]
	return ok
}

// size 返回条目的底层未压缩大小估算值
// 入参: name 包内路径
// 返回: uint64 估算字节数, bool 是否存在
func (s *encryptedPackageSource) size(name string) (uint64, bool) {
	if s == nil {
		return 0, false
	}
	clean := cleanPackagePath(name)
	if f, ok := s.plain[clean]; ok {
		return f.UncompressedSize64, true
	}
	if spec, ok := s.entries[clean]; ok && spec.file != nil {
		return spec.file.UncompressedSize64, true
	}
	return 0, false
}

// filter 按保留集合派生新的只读加密源，空结果也返回非nil实例避免回退到未过滤源
// 入参: keep 允许保留的规范化路径集合
// 返回: *encryptedPackageSource 派生加密源
func (s *encryptedPackageSource) filter(keep map[string]bool) *encryptedPackageSource {
	if s == nil {
		return nil
	}
	next := &encryptedPackageSource{
		provider:   s.provider,
		entryLimit: s.entryLimit,
		entries:    make(map[string]encryptedEntrySpec),
		plain:      make(map[string]*zip.File),
		keys:       s.keys,
	}
	for name, spec := range s.entries {
		if keep[name] {
			next.entries[name] = spec
		}
	}
	for name, f := range s.plain {
		if keep[name] {
			next.plain[name] = f
		}
	}
	return next
}

// omit 按剔除集合派生新的只读加密源，空结果也返回非nil实例避免回退到未过滤源
// 入参: drop 待剔除的规范化路径集合
// 返回: *encryptedPackageSource 派生加密源
func (s *encryptedPackageSource) omit(drop map[string]bool) *encryptedPackageSource {
	if s == nil {
		return nil
	}
	next := &encryptedPackageSource{
		provider:   s.provider,
		entryLimit: s.entryLimit,
		entries:    make(map[string]encryptedEntrySpec, len(s.entries)),
		plain:      make(map[string]*zip.File, len(s.plain)),
		keys:       s.keys,
	}
	for name, spec := range s.entries {
		if !drop[name] {
			next.entries[name] = spec
		}
	}
	for name, f := range s.plain {
		if !drop[name] {
			next.plain[name] = f
		}
	}
	return next
}

// names 返回排序后的全部逻辑路径列表
// 返回: []string 逻辑路径列表
func (s *encryptedPackageSource) names() []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.plain)+len(s.entries))
	for name := range s.plain {
		out = append(out, name)
	}
	for name := range s.entries {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// open 按需打开逻辑路径对应的明文或流式SM4解密流，并发安全
// 入参: name 包内路径
// 返回: io.ReadCloser 明文数据流, error 读取或解密错误
func (s *encryptedPackageSource) open(name string) (io.ReadCloser, error) {
	if s == nil {
		return nil, fmt.Errorf("file not found: %s: %w", name, os.ErrNotExist)
	}
	clean := cleanPackagePath(name)
	if f, ok := s.plain[clean]; ok {
		return f.Open()
	}
	spec, ok := s.entries[clean]
	if !ok || spec.file == nil {
		return nil, fmt.Errorf("file not found: %s: %w", name, os.ErrNotExist)
	}
	_, isDefault := s.provider.(GMCryptoProvider)
	if s.provider == nil || isDefault {
		rc, err := spec.file.Open()
		if err != nil {
			return nil, err
		}
		current := rc
		for _, layer := range spec.layers {
			next, err := newSM4CBCDecryptReader(current, layer.key, layer.iv)
			if err != nil {
				_ = current.Close()
				return nil, err
			}
			current = next
		}
		return current, nil
	}
	data, readErr := readLimitedZipEntry(spec.file, clean, encryptedEntryReadLimit(s.entryLimit, len(spec.layers)))
	if readErr != nil {
		return nil, readErr
	}
	for _, layer := range spec.layers {
		plain, decErr := s.provider.DecryptSM4(layer.key, layer.iv, data)
		clear(data)
		if decErr != nil {
			return nil, ErrInvalidCredentials
		}
		data = plain
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// materializeArchive 将加密源流式解密并物化为受溢出预算控制的只读ZIP快照
// 入参: ctx 取消上下文, limit 内存溢出阈值
// 返回: *zip.Reader 只读明文归档, error 读取或取消错误
func (s *encryptedPackageSource) materializeArchive(ctx context.Context, limit int64) (*zip.Reader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data := &editorSnapshotBuffer{spillLimit: limit, encryptSpill: true}
	cleanupData := true
	defer func() {
		if cleanupData {
			_ = data.Close()
		}
	}()
	var count int64
	archive := zip.NewWriter(convertWriter{ctx: ctx, writer: data, count: &count})
	buffer := make([]byte, 32*1024)
	for _, name := range s.names() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rc, err := s.open(name)
		if err != nil {
			return nil, err
		}
		method := uint16(zip.Store)
		if strings.HasSuffix(strings.ToLower(name), ".xml") {
			method = zip.Deflate
		}
		entry, createErr := archive.CreateHeader(&zip.FileHeader{Name: name, Method: method})
		if createErr != nil {
			_ = rc.Close()
			return nil, createErr
		}
		_, copyErr := io.CopyBuffer(entry, imageInput{ReadCloser: rc, ctx: ctx}, buffer)
		closeErr := rc.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	reader, err := newSnapshotZipReader(data)
	if err != nil {
		return nil, err
	}
	cleanupData = false
	return reader, nil
}

// Read 分块流式读取并解密明文字节，结束时校验PKCS#7填充
// 入参: p 输出缓冲
// 返回: int 读取字节数, error 读取或校验错误
func (r *sm4CBCDecryptReader) Read(p []byte) (int, error) {
	for {
		if len(r.plain) != 0 {
			n := copy(p, r.plain)
			r.plain = r.plain[n:]
			return n, nil
		}
		if r.err != nil {
			return 0, r.err
		}
		n := copy(r.buf, r.rem[:r.remN])
		r.remN = 0
		for n < sm4.BlockSize*2 && !r.eof {
			k, readErr := r.src.Read(r.buf[n:])
			n += k
			if readErr == io.EOF {
				r.eof = true
				break
			}
			if readErr != nil {
				r.err = readErr
				return 0, readErr
			}
		}
		if !r.eof {
			ready := ((n - 1) / sm4.BlockSize) * sm4.BlockSize
			r.remN = copy(r.rem[:], r.buf[ready:n])
			r.mode.CryptBlocks(r.buf[:ready], r.buf[:ready])
			r.plain = r.buf[:ready]
			continue
		}
		if n == 0 || n%sm4.BlockSize != 0 {
			r.err = ErrInvalidCredentials
			return 0, r.err
		}
		r.mode.CryptBlocks(r.buf[:n], r.buf[:n])
		unpadded, unpadErr := padding.NewPKCS7Padding(sm4.BlockSize).ConstantTimeUnpad(r.buf[:n])
		if unpadErr != nil {
			clear(r.buf[:n])
			r.err = ErrInvalidCredentials
			return 0, r.err
		}
		r.plain = unpadded
		r.err = io.EOF
	}
}

// Close 清理内部明文缓冲并关闭底层密文流
// 返回: error 关闭错误
func (r *sm4CBCDecryptReader) Close() error {
	clear(r.buf)
	clear(r.rem[:])
	r.plain = nil
	if r.src != nil {
		return r.src.Close()
	}
	return nil
}

// UnmarshalXML 解析密钥描述并兼容标准印刷错拼DecyptSeed与正拼DecryptSeed
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (seed *encryptionSeed) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if start.Name.Space != ofdNamespace2016 || (start.Name.Local != "DecyptSeed" && start.Name.Local != "DecryptSeed") {
		return fmt.Errorf("unexpected encryption seed element: %s", start.Name.Local)
	}
	var raw struct {
		ID     string            `xml:"ID,attr"`
		Method string            `xml:"EncryptCaseId,attr"`
		Users  []encryptionUser  `xml:"UserInfo"`
		Params *encryptionParams `xml:"ExtendParams"`
	}
	if err := d.DecodeElement(&raw, &start); err != nil {
		return err
	}
	*seed = encryptionSeed{ID: raw.ID, Method: raw.Method, Users: raw.Users, Params: raw.Params}
	return nil
}

// openContext 按取消上下文初始化包索引并在解析OFD根节点前解密
// 入参: ctx 取消上下文, options 阅读选项
// 返回: error 错误信息
func (r *Reader) openContext(ctx context.Context, options []ReaderOption) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	settings := readerOptions{provider: GMCryptoProvider{}}
	for _, option := range options {
		if option != nil {
			option(&settings)
		}
	}
	r.resources = settings.resources
	if settings.provider == nil {
		settings.provider = GMCryptoProvider{}
	}
	defer func() {
		for i := range settings.credentials {
			clear(settings.credentials[i].Password)
		}
		if err != nil {
			r.encHold.release()
			r.encHold = nil
			r.encryption = nil
			r.encrypted = nil
		}
	}()
	if err := r.indexPackageContext(ctx); err != nil {
		return err
	}
	encrypted := r.fileNames["Encryptions.xml"]
	if encrypted {
		if err := r.decryptPackageContext(ctx, settings); err != nil {
			return err
		}
	}
	if err := r.initRootContext(ctx); err != nil {
		if encrypted && !errors.Is(err, ErrInvalidCredentials) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, ErrEntryTooLarge) && !errors.Is(err, ErrUnsupportedEncryption) {
			return fmt.Errorf("%w: %w", ErrInvalidCredentials, err)
		}
		return err
	}
	if settings.hasDocumentIndex {
		if settings.documentIndex < 0 || settings.documentIndex >= r.DocumentCount() {
			return fmt.Errorf("document index %d out of range", settings.documentIndex)
		}
		r.documentIndex = settings.documentIndex
	}
	if settings.versionID != nil {
		r.selectedVersion = settings.versionID
		if _, err := r.DocumentRootContext(ctx); err != nil {
			return err
		}
	}
	r.closer = settings.closer
	return nil
}

// decryptPackageContext 按取消上下文建立加密条目的按需流式解密映射，仅解析加密元数据，不将大密文常驻内存
// 同层全部映射校验成功后清理密文索引，全包加密层清理外层未映射明文残留
// 入参: ctx 取消上下文, settings 会话凭据和密码实现
// 返回: error 错误信息
func (r *Reader) decryptPackageContext(ctx context.Context, settings readerOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := r.readFileViewContext(ctx, "Encryptions.xml")
	if err != nil {
		return err
	}
	var list encryptionList
	if err := xml.Unmarshal(data, &list); err != nil || len(list.Records) == 0 {
		return fmt.Errorf("%w: invalid Encryptions.xml", ErrUnsupportedEncryption)
	}
	if len(settings.credentials) == 0 {
		return ErrCredentialsRequired
	}
	limit := r.entryLimit()
	nodes := make(map[string]decryptNode, len(r.fileIndex)+len(r.files))
	for name, f := range r.fileIndex {
		nodes[name] = decryptNode{file: f}
	}
	for name, raw := range r.files {
		nodes[name] = decryptNode{raw: raw}
	}
	passwordKeys := make([][]byte, len(settings.credentials))
	certKeys := make(map[unwrappedCertKey][]byte)
	state := &encryptionState{info: EncryptionInfo{Encrypted: true, Layers: len(list.Records), Methods: make([]string, len(list.Records))}}
	var allocatedKeys [][]byte
	delivered := false
	defer func() {
		for _, k := range passwordKeys {
			clear(k)
		}
		for _, k := range certKeys {
			clear(k)
		}
		if !delivered {
			for _, k := range allocatedKeys {
				clear(k)
			}
			if state.options != nil {
				clear(state.options.Password)
			}
		}
	}()
	list.Records, err = orderEncryptionRecords(list.Records)
	if err != nil {
		return err
	}
	_, isDefaultProvider := settings.provider.(GMCryptoProvider)
	outerDecrypted := make(map[string]decryptNode)
	deferredScopeAll := false
	type cachedEntrySeed struct {
		key        []byte
		iv         []byte
		credIndex  int
		samePolicy bool
	}
	for i := len(list.Records) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		record := list.Records[i]
		seedNode, seedName, err := encryptionNode(nodes, "Encryptions.xml", record.Seed)
		if err != nil {
			return err
		}
		seedData, err := readDecryptNode(seedNode, settings.provider, seedName, limit)
		if err != nil {
			return err
		}
		var seed encryptionSeed
		if err := xml.Unmarshal(seedData, &seed); err != nil || seed.ID != record.ID || len(seed.Users) == 0 {
			return fmt.Errorf("%w: invalid encryption seed", ErrUnsupportedEncryption)
		}
		key, iv, credIndex, err := unwrapEncryptionKey(seed, settings, passwordKeys, certKeys)
		if err != nil {
			return err
		}
		mapNode, mapName, err := encryptionNode(nodes, "Encryptions.xml", record.Entries)
		var entries encryptionEntries
		if err == nil {
			mapData, readErr := readDecryptNode(mapNode, settings.provider, mapName, limit)
			if readErr != nil {
				err = readErr
			} else {
				entries, err = decodeEncryptionEntries(mapData, key, iv, settings.provider)
			}
		}
		if err != nil {
			clear(key)
			if errors.Is(err, ErrEntryTooLarge) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return ErrInvalidCredentials
		}
		if entries.ID != record.ID || len(entries.Entries) == 0 {
			clear(key)
			return ErrInvalidCredentials
		}
		decrypted := make(map[string]decryptNode, len(entries.Entries))
		used := make(map[string]bool, len(entries.Entries))
		extraSeeds := make(map[string]bool)
		seedCache := make(map[string]cachedEntrySeed)
		canInherit := true
		for _, entry := range entries.Entries {
			if err := ctx.Err(); err != nil {
				clear(key)
				for _, item := range seedCache {
					clear(item.key)
				}
				return err
			}
			name := encryptionPath(mapName, entry.Path)
			if !validPackagePath(name) || used[name] {
				clear(key)
				for _, item := range seedCache {
					clear(item.key)
				}
				return fmt.Errorf("ambiguous encrypted package path: %q", entry.Path)
			}
			entryKey, entryIV := key, iv
			entrySeedName := ""
			if strings.TrimSpace(entry.Seed) != "" {
				entrySeedName = encryptionPath(mapName, entry.Seed)
			}
			if entrySeedName != "" && entrySeedName != seedName {
				cached, found := seedCache[entrySeedName]
				if !found {
					entrySeedNode, extraSeed, seedErr := encryptionNode(nodes, mapName, entry.Seed)
					var entrySeedData []byte
					if seedErr == nil {
						entrySeedData, seedErr = readDecryptNode(entrySeedNode, settings.provider, extraSeed, limit)
					}
					var entrySeed encryptionSeed
					if seedErr == nil {
						seedErr = xml.Unmarshal(entrySeedData, &entrySeed)
					}
					if seedErr == nil && (entrySeed.ID != record.ID || len(entrySeed.Users) == 0) {
						seedErr = fmt.Errorf("%w: invalid encryption seed", ErrUnsupportedEncryption)
					}
					var eKey, eIV []byte
					var entryCredIndex int
					if seedErr == nil {
						eKey, eIV, entryCredIndex, seedErr = unwrapEncryptionKey(entrySeed, settings, passwordKeys, certKeys)
					}
					if seedErr != nil {
						clear(key)
						for _, item := range seedCache {
							clear(item.key)
						}
						return seedErr
					}
					cached = cachedEntrySeed{key: eKey, iv: eIV, credIndex: entryCredIndex, samePolicy: entryCredIndex == credIndex && sameEncryptionSeedPolicy(seed, entrySeed)}
					seedCache[entrySeedName] = cached
					extraSeeds[extraSeed] = true
				}
				if !cached.samePolicy {
					canInherit = false
				}
				entryKey, entryIV = cached.key, cached.iv
			}
			srcNode, _, err := encryptionNode(nodes, mapName, entry.EncryptedPath)
			if err != nil {
				clear(key)
				for _, item := range seedCache {
					clear(item.key)
				}
				return ErrInvalidCredentials
			}
			if isDefaultProvider && len(srcNode.layers) == 0 && srcNode.file != nil {
				if srcNode.file.UncompressedSize64 == 0 || srcNode.file.UncompressedSize64%sm4.BlockSize != 0 {
					clear(key)
					for _, item := range seedCache {
						clear(item.key)
					}
					return ErrInvalidCredentials
				}
			}
			layers := make([]encryptedEntryLayer, len(srcNode.layers)+1)
			copy(layers, srcNode.layers)
			clonedKey := bytes.Clone(entryKey)
			allocatedKeys = append(allocatedKeys, clonedKey)
			layers[len(srcNode.layers)] = encryptedEntryLayer{key: clonedKey, iv: bytes.Clone(entryIV)}
			decrypted[name] = decryptNode{raw: srcNode.raw, file: srcNode.file, layers: layers}
			used[name] = true
		}
		clear(key)
		for _, item := range seedCache {
			clear(item.key)
		}
		for _, entry := range entries.Entries {
			encPath := encryptionPath(mapName, entry.EncryptedPath)
			delete(nodes, encPath)
			delete(outerDecrypted, encPath)
		}
		delete(nodes, seedName)
		delete(nodes, mapName)
		delete(outerDecrypted, seedName)
		delete(outerDecrypted, mapName)
		for name := range extraSeeds {
			delete(nodes, name)
			delete(outerDecrypted, name)
		}
		if strings.EqualFold(strings.TrimSpace(record.Scope), "All") {
			nextNodes := make(map[string]decryptNode, len(decrypted)+len(outerDecrypted))
			resolvedInner := true
			for j := 0; j < i; j++ {
				if !preserveInnerEncryptionRecord(nextNodes, nodes, decrypted, list.Records[j], settings.provider, limit) {
					resolvedInner = false
				}
			}
			if resolvedInner {
				maps.Copy(nextNodes, outerDecrypted)
				maps.Copy(nextNodes, decrypted)
				nodes = nextNodes
			} else {
				deferredScopeAll = true
				maps.Copy(nodes, decrypted)
			}
		} else {
			maps.Copy(nodes, decrypted)
		}
		maps.Copy(outerDecrypted, decrypted)
		state.info.Method = seed.Method
		state.info.Methods[i] = seed.Method
		for _, user := range seed.Users {
			state.info.Users = append(state.info.Users, user.Name)
		}
		if len(list.Records) == 1 && canInherit {
			state.options = inheritedEncryptionOptions(record, seed, settings.credentials[credIndex], settings.provider)
		}
	}
	if deferredScopeAll {
		nodes = outerDecrypted
	}
	delete(nodes, "Encryptions.xml")
	src := &encryptedPackageSource{
		provider:   settings.provider,
		entryLimit: limit,
		entries:    make(map[string]encryptedEntrySpec),
		plain:      make(map[string]*zip.File),
	}
	var rawFiles map[string][]byte
	var poolKeys [][]byte
	keptKeys := make(map[*byte]bool)
	for name, node := range nodes {
		if err := ctx.Err(); err != nil {
			src.clearKeys()
			return err
		}
		if node.file != nil {
			if len(node.layers) == 0 {
				src.plain[name] = node.file
			} else {
				src.entries[name] = encryptedEntrySpec{file: node.file, layers: node.layers}
				for i := range node.layers {
					if key := node.layers[i].key; len(key) > 0 && !keptKeys[&key[0]] {
						keptKeys[&key[0]] = true
						poolKeys = append(poolKeys, key)
					}
				}
			}
			continue
		}
		plain, decErr := readDecryptNode(node, settings.provider, name, limit)
		if decErr != nil {
			src.clearKeys()
			return decErr
		}
		if rawFiles == nil {
			rawFiles = make(map[string][]byte)
		}
		rawFiles[name] = plain
	}
	if len(src.entries) != 0 || len(src.plain) != 0 {
		src.keys = newEncryptedKeyPool(poolKeys)
		state.source = src
		r.encrypted = src
	} else {
		r.encrypted = nil
	}
	if state.options != nil {
		state.lease = newEncryptionLease(state.options.Password)
	}
	for _, key := range allocatedKeys {
		if len(key) > 0 && !keptKeys[&key[0]] {
			clear(key)
		}
	}
	r.zip, r.files, r.encryption = nil, rawFiles, state
	r.adoptEncryptionHold()
	delivered = true
	return nil
}

// setEncryptedSource 设置派生加密源并克隆加密状态，避免原地修改原共享状态
// 入参: src 派生加密源
func (r *Reader) setEncryptedSource(src *encryptedPackageSource) {
	if r == nil {
		return
	}
	r.encrypted = src
	if r.encryption != nil {
		cloned := *r.encryption
		cloned.source = src
		r.encryption = &cloned
	}
}
