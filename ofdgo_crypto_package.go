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
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"
)

// EncryptPackage 对完整OFD包进行GM/T0099加密，保留全部条目的原始内容
// 先签后加密可保留对明文的验签能力；出错不返回部分密文，已有加密包须先解密
// 入参: data 明文OFD包, options 加密策略
// 返回: []byte 加密OFD包, error 错误信息
func EncryptPackage(data []byte, options EncryptionOptions) ([]byte, error) {
	r, err := NewReader(&Input{reader: bytes.NewReader(data), size: int64(len(data))})
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var output bytes.Buffer
	if _, err := encryptPackageToWriter(context.Background(), &output, r, options, nil); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
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
	XMLName xml.Name         `xml:"http://www.ofdspec.org/2016 DecyptSeed"`
	ID      string           `xml:"ID,attr"`
	Method  string           `xml:"EncryptCaseId,attr"`
	Users   []encryptionUser `xml:"UserInfo"`
	Params  encryptionParams `xml:"ExtendParams"`
}

// UnmarshalXML 解析密钥描述并兼容标准印刷错拼DecyptSeed与正拼DecryptSeed
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (seed *encryptionSeed) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if start.Name.Space != ofdNamespace2016 || (start.Name.Local != "DecyptSeed" && start.Name.Local != "DecryptSeed") {
		return fmt.Errorf("unexpected encryption seed element: %s", start.Name.Local)
	}
	var raw struct {
		ID     string           `xml:"ID,attr"`
		Method string           `xml:"EncryptCaseId,attr"`
		Users  []encryptionUser `xml:"UserInfo"`
		Params encryptionParams `xml:"ExtendParams"`
	}
	if err := d.DecodeElement(&raw, &start); err != nil {
		return err
	}
	*seed = encryptionSeed{ID: raw.ID, Method: raw.Method, Users: raw.Users, Params: raw.Params}
	return nil
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

// open 初始化包索引并在解析OFD根节点前解密
// 入参: options 阅读选项
// 返回: error 错误信息
func (r *Reader) open(options []ReaderOption) error {
	settings := readerOptions{provider: GMCryptoProvider{}}
	for _, option := range options {
		option(&settings)
	}
	r.resources = settings.resources
	if settings.provider == nil {
		settings.provider = GMCryptoProvider{}
	}
	defer func() {
		for i := range settings.credentials {
			clear(settings.credentials[i].Password)
		}
	}()
	if err := r.indexPackage(); err != nil {
		return err
	}
	encrypted := r.fileNames["Encryptions.xml"]
	if encrypted {
		if err := r.decryptPackage(settings); err != nil {
			return err
		}
	}
	if err := r.initRoot(); err != nil {
		if encrypted && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			return ErrInvalidCredentials
		}
		return err
	}
	return nil
}

// packageData 获取包内所有条目的实际字节，保留未识别文件
// 返回: map[string][]byte 包内条目, error 错误信息
func (r *Reader) packageData() (map[string][]byte, error) {
	parts := make(map[string][]byte, len(r.fileNames))
	for name := range r.fileNames {
		data, err := r.readFile(name)
		if err != nil {
			return nil, err
		}
		parts[name] = data
	}
	return parts, nil
}

// decryptPackage 在独立内存包内解密，不修改输入文件
// 同层全部映射解密成功后清理密文，全包加密层清理外层未映射明文残留
// 入参: settings 会话凭据和密码实现
// 返回: error 错误信息
func (r *Reader) decryptPackage(settings readerOptions) error {
	data, err := r.readFile("Encryptions.xml")
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
	parts, err := r.packageData()
	if err != nil {
		return err
	}
	passwordKeys := make([][]byte, len(settings.credentials))
	defer func() {
		for _, k := range passwordKeys {
			clear(k)
		}
	}()
	state := &encryptionState{info: EncryptionInfo{Encrypted: true, Layers: len(list.Records)}}
	list.Records, err = orderEncryptionRecords(list.Records)
	if err != nil {
		return err
	}
	outerDecrypted := make(map[string][]byte)
	for i := len(list.Records) - 1; i >= 0; i-- {
		record := list.Records[i]
		seedData, seedName, err := encryptionPart(parts, "Encryptions.xml", record.Seed)
		if err != nil {
			return err
		}
		var seed encryptionSeed
		if err := xml.Unmarshal(seedData, &seed); err != nil || seed.ID != record.ID || len(seed.Users) == 0 {
			return fmt.Errorf("%w: invalid encryption seed", ErrUnsupportedEncryption)
		}
		key, iv, credIndex, err := unwrapEncryptionKey(seed, settings, passwordKeys)
		if err != nil {
			return err
		}
		mapData, mapName, err := encryptionPart(parts, "Encryptions.xml", record.Entries)
		var entries encryptionEntries
		if err == nil {
			entries, err = decodeEncryptionEntries(mapData, key, iv, settings.provider)
		}
		if err != nil || entries.ID != record.ID || len(entries.Entries) == 0 {
			clear(key)
			return ErrInvalidCredentials
		}
		decrypted := make(map[string][]byte, len(entries.Entries))
		used := make(map[string]bool, len(entries.Entries))
		extraSeeds := make(map[string]bool)
		canInherit := true
		for _, entry := range entries.Entries {
			name := encryptionPath(mapName, entry.Path)
			if !validPackagePath(name) || used[name] {
				clear(key)
				return fmt.Errorf("ambiguous encrypted package path: %q", entry.Path)
			}
			entryKey, entryIV := key, iv
			entrySeedName := ""
			if strings.TrimSpace(entry.Seed) != "" {
				entrySeedName = encryptionPath(mapName, entry.Seed)
			}
			if entrySeedName != "" && entrySeedName != seedName {
				entrySeedData, extraSeed, seedErr := encryptionPart(parts, mapName, entry.Seed)
				var entrySeed encryptionSeed
				if seedErr == nil {
					seedErr = xml.Unmarshal(entrySeedData, &entrySeed)
				}
				var entryCredIndex int
				if seedErr == nil {
					entryKey, entryIV, entryCredIndex, seedErr = unwrapEncryptionKey(entrySeed, settings, passwordKeys)
				}
				if seedErr != nil {
					clear(key)
					return seedErr
				}
				if entryCredIndex != credIndex || !sameEncryptionSeedPolicy(seed, entrySeed) {
					canInherit = false
				}
				extraSeeds[extraSeed] = true
			}
			ciphertext, _, err := encryptionPart(parts, mapName, entry.EncryptedPath)
			if err == nil {
				decrypted[name], err = settings.provider.DecryptSM4(entryKey, entryIV, ciphertext)
			}
			if entrySeedName != "" && entrySeedName != seedName {
				clear(entryKey)
			}
			if err != nil {
				clear(key)
				return ErrInvalidCredentials
			}
			used[name] = true
		}
		clear(key)
		for _, entry := range entries.Entries {
			encPath := encryptionPath(mapName, entry.EncryptedPath)
			delete(parts, encPath)
			delete(outerDecrypted, encPath)
		}
		delete(parts, seedName)
		delete(parts, mapName)
		delete(outerDecrypted, seedName)
		delete(outerDecrypted, mapName)
		for name := range extraSeeds {
			delete(parts, name)
			delete(outerDecrypted, name)
		}
		if strings.EqualFold(strings.TrimSpace(record.Scope), "All") {
			nextParts := make(map[string][]byte, len(decrypted)+len(outerDecrypted))
			for j := 0; j < i; j++ {
				preserveInnerEncryptionRecord(nextParts, parts, decrypted, list.Records[j])
			}
			maps.Copy(nextParts, outerDecrypted)
			maps.Copy(nextParts, decrypted)
			parts = nextParts
		} else {
			maps.Copy(parts, decrypted)
		}
		maps.Copy(outerDecrypted, decrypted)
		state.info.Method = seed.Method
		for _, user := range seed.Users {
			state.info.Users = append(state.info.Users, user.Name)
		}
		if len(list.Records) == 1 && canInherit {
			state.options = inheritedEncryptionOptions(seed, settings.credentials[credIndex], settings.provider)
		}
	}
	delete(parts, "Encryptions.xml")
	r.Zip, r.files, r.encryption = nil, parts, state
	return nil
}

// sameEncryptionSeedPolicy 判断条目级密钥描述是否与主密钥描述共享一致接收者策略
// 入参: base 主密钥描述, item 条目密钥描述
// 返回: bool 是否一致
func sameEncryptionSeedPolicy(base, item encryptionSeed) bool {
	if base.Method != item.Method || strings.TrimSpace(item.Params.Raw) != "" || len(base.Users) != len(item.Users) {
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

// encryptionPart 按ST_Loc规则及大小写敏感路径读取加密元数据引用
// 入参: parts 包内条目, base 基准XML路径, name 引用路径
// 返回: []byte 条目内容, string 实际路径, error 错误信息
func encryptionPart(parts map[string][]byte, base, name string) ([]byte, string, error) {
	name = encryptionPath(base, name)
	if !validPackagePath(name) {
		return nil, "", fmt.Errorf("invalid encryption path: %q", name)
	}
	if data, ok := parts[name]; ok {
		return data, name, nil
	}
	return nil, "", fmt.Errorf("missing encrypted entry: %s", name)
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

// unwrapEncryptionKey 按方案尝试会话凭据并复用口令派生密钥，不向错误信息写入口令
// 入参: seed 密钥描述, settings 会话凭据, passwordKeys 会话口令密钥缓存
// 返回: []byte 文件密钥, []byte 初始向量, int 命中凭据索引, error 错误信息
func unwrapEncryptionKey(seed encryptionSeed, settings readerOptions, passwordKeys [][]byte) ([]byte, []byte, int, error) {
	if seed.Method != "1.1.1" && seed.Method != "1.1.2" {
		return nil, nil, -1, ErrUnsupportedEncryption
	}
	for credIndex, credential := range settings.credentials {
		for _, user := range seed.Users {
			if credential.UserName != "" && credential.UserName != user.Name {
				continue
			}
			wrapped, err := decodeEncryptionBase64(user.Key)
			if err != nil {
				return nil, nil, -1, ErrInvalidCredentials
			}
			iv, err := decodeEncryptionBase64(user.IV)
			if err == nil && len(iv) == 0 {
				iv = make([]byte, 16)
			}
			if err != nil || len(iv) != 16 {
				return nil, nil, -1, ErrInvalidCredentials
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
				key, err = settings.provider.DecryptSM4(kek, iv, wrapped)
			} else if seed.Method == "1.1.2" && credential.Decrypter != nil {
				certificate, err = decodeEncryptionBase64(user.Certificate)
				if err != nil || len(certificate) == 0 {
					return nil, nil, -1, ErrInvalidCredentials
				}
				if len(credential.Certificate) != 0 && !bytes.Equal(credential.Certificate, certificate) {
					continue
				}
				key, err = credential.Decrypter.Decrypt(rand.Reader, wrapped, nil)
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

// inheritedEncryptionOptions 保留能够完整恢复的原接收者策略
// 入参: seed 原密钥描述, credential 命中凭据, provider 密码实现
// 返回: *EncryptionOptions 可继承策略，nil表示需调用方显式选择
func inheritedEncryptionOptions(seed encryptionSeed, credential Credentials, provider CryptoProvider) *EncryptionOptions {
	if strings.TrimSpace(seed.Params.Raw) != "" {
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
// 入参: value Base64编码文本
// 返回: []byte 解码字节, error 解码错误
func decodeEncryptionBase64(value string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.ReplaceAll(value, " ", ""), "\t", ""))
}

// encryptPackageToWriter 流式逐条加密阅读器中的包条目并直接写出ZIP归档
// 口令模式下为每个文件生成独立初始向量与条目种子，证书模式下保持单种子避免硬件设备多次私钥解密
// 入参: ctx 取消上下文, writer 输出流, r 明文包阅读器, options 加密策略, progress 取消与进度检查
// 返回: int64 写出字节数, error 错误信息
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
	key, iv, identifier := make([]byte, 16), make([]byte, 16), make([]byte, 16)
	for _, buffer := range [][]byte{key, iv, identifier} {
		if _, err := rand.Read(buffer); err != nil {
			return 0, err
		}
	}
	defer clear(key)
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
		wrapped, err := provider.EncryptSM4(kek, iv, key)
		if err != nil {
			return 0, err
		}
		seed.Users = []encryptionUser{{Name: options.UserName, Role: options.UserType, Key: base64.StdEncoding.EncodeToString(wrapped), IV: base64.StdEncoding.EncodeToString(iv)}}
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
			seed.Users = append(seed.Users, encryptionUser{Name: recipient.UserName, Role: recipient.UserType, Certificate: base64.StdEncoding.EncodeToString(certificate), Key: base64.StdEncoding.EncodeToString(wrapped), IV: base64.StdEncoding.EncodeToString(iv)})
		}
	}
	names := slices.Sorted(maps.Keys(r.fileNames))
	entries := encryptionEntries{XMLName: xml.Name{Space: ofdNamespace2016, Local: "EncryptEntries"}, ID: id, Entries: make([]encryptionEntry, 0, len(names))}
	var entryIVs [][]byte
	if passwordMode {
		entryIVs = make([][]byte, len(names))
	}
	for i, name := range names {
		encryptedName := fmt.Sprintf("Encrypt/Files/%d.dat", i)
		entry := encryptionEntry{Path: "/" + name, EncryptedPath: "/" + encryptedName}
		if passwordMode {
			entryIV := make([]byte, 16)
			if _, err := rand.Read(entryIV); err != nil {
				return 0, err
			}
			entryIVs[i] = entryIV
			entry.Seed = fmt.Sprintf("/Encrypt/Seeds/%d.dat", i)
		}
		entries.Entries = append(entries.Entries, entry)
	}
	plainMap, err := xml.Marshal(entries)
	if err != nil {
		return 0, err
	}
	mapData, err := provider.EncryptSM4(key, iv, plainMap)
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
		plain, borrowed, readErr := r.readFileData(name)
		if readErr != nil {
			return count, readErr
		}
		entryIV := iv
		if passwordMode {
			entryIV = entryIVs[i]
		}
		cipherData, encErr := provider.EncryptSM4(key, entryIV, plain)
		if !borrowed {
			clear(plain)
		}
		if encErr != nil {
			return count, encErr
		}
		if err := writeStoreEntry(fmt.Sprintf("Encrypt/Files/%d.dat", i), cipherData); err != nil {
			return count, err
		}
	}
	if passwordMode {
		for i, entryIV := range entryIVs {
			wrapped, encErr := provider.EncryptSM4(kek, entryIV, key)
			if encErr != nil {
				return count, encErr
			}
			entrySeed := encryptionSeed{
				ID:     id,
				Method: "1.1.1",
				Users:  []encryptionUser{{Name: options.UserName, Role: options.UserType, Key: base64.StdEncoding.EncodeToString(wrapped), IV: base64.StdEncoding.EncodeToString(entryIV)}},
			}
			entrySeedData, marshalErr := xml.Marshal(entrySeed)
			if marshalErr != nil {
				return count, marshalErr
			}
			if err := writeStoreEntry(fmt.Sprintf("Encrypt/Seeds/%d.dat", i), entrySeedData); err != nil {
				return count, err
			}
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

// preserveInnerEncryptionRecord 保留全包解密层尚未处理的更内层加密元数据及明文映射引用的密文和条目种子
// 入参: dst 下一轮包条目, parts 当前包条目, decrypted 本层解密结果, record 更内层加密记录
func preserveInnerEncryptionRecord(dst, parts, decrypted map[string][]byte, record encryptionRecord) {
	for _, meta := range []string{record.Seed, record.Entries} {
		metaName := encryptionPath("Encryptions.xml", meta)
		if validPackagePath(metaName) {
			if metaData, ok := parts[metaName]; ok {
				dst[metaName] = metaData
			}
		}
	}
	entriesName := encryptionPath("Encryptions.xml", record.Entries)
	if !validPackagePath(entriesName) {
		return
	}
	entriesData, ok := decrypted[entriesName]
	if !ok {
		entriesData, ok = parts[entriesName]
	}
	if !ok {
		return
	}
	var innerEntries encryptionEntries
	if xml.Unmarshal(entriesData, &innerEntries) != nil || innerEntries.XMLName.Space != ofdNamespace2016 || (innerEntries.XMLName.Local != "EncryptEntries" && innerEntries.XMLName.Local != "EncryptedEntries") {
		return
	}
	for _, entry := range innerEntries.Entries {
		for _, ref := range []string{entry.EncryptedPath, entry.Seed} {
			if strings.TrimSpace(ref) == "" {
				continue
			}
			refName := encryptionPath(entriesName, ref)
			if validPackagePath(refName) {
				if refData, exists := parts[refName]; exists {
					dst[refName] = refData
				}
			}
		}
	}
}
