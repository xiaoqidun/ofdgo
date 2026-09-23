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
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

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

// encryptionParams 保留未知扩展，避免默认另存时静默丢失其语义
type encryptionParams struct {
	Raw string `xml:",innerxml"`
}

// encryptionUser 保存单个接收者的公开信息及包装密钥
type encryptionUser struct {
	Name        string `xml:"UserName,attr"`
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
	if _, ok := r.fileNamesFold["encryptions.xml"]; ok {
		if err := r.decryptPackage(settings); err != nil {
			return err
		}
	}
	return r.initRoot()
}

// packageData 获取包内所有条目的实际字节，保留未识别文件
// 返回: map[string][]byte 包内条目, error 错误信息
func (r *Reader) packageData() (map[string][]byte, error) {
	parts := make(map[string][]byte, len(r.fileNamesFold))
	for _, name := range r.fileNamesFold {
		data, err := r.readFile(name)
		if err != nil {
			return nil, err
		}
		parts[name] = data
	}
	return parts, nil
}

// decryptPackage 在独立内存包内解密，不修改输入文件
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
	state := &encryptionState{info: EncryptionInfo{Encrypted: true, Layers: len(list.Records)}}
	list.Records, err = orderEncryptionRecords(list.Records)
	if err != nil {
		return err
	}
	for i := len(list.Records) - 1; i >= 0; i-- {
		record := list.Records[i]
		seedData, seedName, err := encryptionPart(parts, record.Seed)
		if err != nil {
			return err
		}
		var seed encryptionSeed
		if err := xml.Unmarshal(seedData, &seed); err != nil || seed.ID != record.ID || len(seed.Users) == 0 {
			return fmt.Errorf("%w: invalid encryption seed", ErrUnsupportedEncryption)
		}
		key, iv, credential, err := unwrapEncryptionKey(seed, settings)
		if err != nil {
			return err
		}
		mapData, mapName, err := encryptionPart(parts, record.Entries)
		var entries encryptionEntries
		if err == nil {
			entries, err = decodeEncryptionEntries(mapData, key, iv, settings.provider)
		}
		if err != nil || entries.ID != record.ID || len(entries.Entries) == 0 {
			clear(key)
			return ErrInvalidCredentials
		}
		decrypted := make(map[string][]byte, len(entries.Entries))
		used := make(map[string]bool)
		extraSeeds := make(map[string]bool)
		for _, entry := range entries.Entries {
			name := cleanPackagePath(entry.Path)
			fold := strings.ToLower(name)
			if !validPackagePath(name) || used[fold] {
				clear(key)
				return fmt.Errorf("ambiguous encrypted package path: %q", entry.Path)
			}
			entryKey, entryIV := key, iv
			if entry.Seed != "" && cleanPackagePath(entry.Seed) != seedName {
				seedData, extraSeed, seedErr := encryptionPart(parts, entry.Seed)
				var entrySeed encryptionSeed
				if seedErr == nil {
					seedErr = xml.Unmarshal(seedData, &entrySeed)
				}
				if seedErr == nil {
					entryKey, entryIV, _, seedErr = unwrapEncryptionKey(entrySeed, settings)
				}
				if seedErr != nil {
					clear(key)
					return seedErr
				}
				extraSeeds[extraSeed] = true
			}
			ciphertext, actual, err := encryptionPart(parts, entry.EncryptedPath)
			if err == nil {
				decrypted[name], err = settings.provider.DecryptSM4(entryKey, entryIV, ciphertext)
			}
			if entry.Seed != "" && cleanPackagePath(entry.Seed) != seedName {
				clear(entryKey)
			}
			if err != nil {
				clear(key)
				return ErrInvalidCredentials
			}
			delete(parts, actual)
			used[fold] = true
		}
		clear(key)
		delete(parts, seedName)
		delete(parts, mapName)
		for name := range extraSeeds {
			delete(parts, name)
		}
		remaining := make(map[string]string, len(parts))
		for name := range parts {
			remaining[strings.ToLower(name)] = name
		}
		for name, plain := range decrypted {
			if existing, ok := remaining[strings.ToLower(name)]; ok {
				delete(parts, existing)
			}
			parts[name] = plain
		}
		state.info.Method = seed.Method
		for _, user := range seed.Users {
			state.info.Users = append(state.info.Users, user.Name)
		}
		if len(list.Records) == 1 && len(extraSeeds) == 0 {
			state.options = inheritedEncryptionOptions(seed, credential, settings.provider)
		}
	}
	for name := range parts {
		if strings.ToLower(name) == "encryptions.xml" {
			delete(parts, name)
		}
	}
	r.Zip, r.files, r.encryption = nil, parts, state
	return nil
}

// decodeEncryptionEntries 读取标准允许的明文或密文明密文映射表
// 入参: data 映射文件, key 文件密钥, iv 初始向量, provider 密码实现
// 返回: encryptionEntries 映射表, error 错误信息
func decodeEncryptionEntries(data, key, iv []byte, provider CryptoProvider) (encryptionEntries, error) {
	var entries encryptionEntries
	valid := func() bool {
		return entries.XMLName.Space == ofdNamespace && (entries.XMLName.Local == "EncryptEntries" || entries.XMLName.Local == "EncryptedEntries")
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

// encryptionPart 按统一大小写规则读取加密元数据引用
// 入参: parts 包内条目, name 引用路径
// 返回: []byte 条目内容, string 实际路径, error 错误信息
func encryptionPart(parts map[string][]byte, name string) ([]byte, string, error) {
	name = cleanPackagePath(name)
	if !validPackagePath(name) {
		return nil, "", fmt.Errorf("invalid encryption path: %q", name)
	}
	if data, ok := parts[name]; ok {
		return data, name, nil
	}
	for actual, data := range parts {
		if strings.ToLower(actual) == strings.ToLower(name) {
			return data, actual, nil
		}
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

// unwrapEncryptionKey 按方案尝试会话凭据，不向错误信息写入口令
// 入参: seed 密钥描述, settings 会话凭据
// 返回: []byte 文件密钥, []byte 初始向量, Credentials 命中凭据, error 错误信息
func unwrapEncryptionKey(seed encryptionSeed, settings readerOptions) ([]byte, []byte, Credentials, error) {
	if seed.Method != "1.1.1" && seed.Method != "1.1.2" {
		return nil, nil, Credentials{}, ErrUnsupportedEncryption
	}
	for _, credential := range settings.credentials {
		for _, user := range seed.Users {
			if credential.UserName != "" && credential.UserName != user.Name {
				continue
			}
			wrapped, err := base64.StdEncoding.DecodeString(user.Key)
			if err != nil {
				return nil, nil, Credentials{}, ErrInvalidCredentials
			}
			iv, err := base64.StdEncoding.DecodeString(user.IV)
			if user.IV == "" {
				iv = make([]byte, 16)
			}
			if err != nil || len(iv) != 16 {
				return nil, nil, Credentials{}, ErrInvalidCredentials
			}
			var key []byte
			if seed.Method == "1.1.1" && len(credential.Password) != 0 {
				kek, keyErr := settings.provider.PasswordKey(credential.Password)
				if keyErr != nil {
					return nil, nil, Credentials{}, keyErr
				}
				key, err = settings.provider.DecryptSM4(kek, iv, wrapped)
				clear(kek)
			} else if seed.Method == "1.1.2" && credential.Decrypter != nil {
				certificate, decodeErr := base64.StdEncoding.DecodeString(user.Certificate)
				if decodeErr != nil || len(certificate) == 0 {
					return nil, nil, Credentials{}, ErrInvalidCredentials
				}
				if len(credential.Certificate) != 0 && !bytes.Equal(credential.Certificate, certificate) {
					continue
				}
				key, err = credential.Decrypter.Decrypt(rand.Reader, wrapped, nil)
			} else {
				continue
			}
			if err == nil && len(key) == 16 {
				return key, iv, credential, nil
			}
			clear(key)
		}
	}
	return nil, nil, Credentials{}, ErrInvalidCredentials
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
		certificate, err := base64.StdEncoding.DecodeString(user.Certificate)
		if err != nil {
			return nil
		}
		options.Recipients = append(options.Recipients, EncryptionRecipient{UserName: user.Name, UserType: user.Role, Certificate: certificate})
	}
	return options
}

// EncryptPackage 对完整OFD包进行GM/T0099加密，保留全部条目的原始内容
// 先签后加密可保留对明文的验签能力；出错不返回部分密文，已有加密包须先解密
// 入参: data 明文OFD包, options 加密策略
// 返回: []byte 加密OFD包, error 错误信息
func EncryptPackage(data []byte, options EncryptionOptions) ([]byte, error) {
	r, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	parts, err := r.packageData()
	if err != nil {
		return nil, err
	}
	return encryptPackageParts(parts, options, nil)
}

// encryptPackageParts 加密所有原条目，再生成标准入口、密钥描述与映射
// 入参: parts 明文条目, options 加密策略, progress 取消与进度检查
// 返回: []byte 加密包, error 错误信息
func encryptPackageParts(parts map[string][]byte, options EncryptionOptions, progress editorProgress) ([]byte, error) {
	if err := validateEncryptionOptions(options); err != nil {
		return nil, err
	}
	provider := options.Provider
	if provider == nil {
		provider = GMCryptoProvider{}
	}
	key, iv, identifier := make([]byte, 16), make([]byte, 16), make([]byte, 16)
	for _, buffer := range [][]byte{key, iv, identifier} {
		if _, err := rand.Read(buffer); err != nil {
			return nil, err
		}
	}
	defer clear(key)
	id := "E" + hex.EncodeToString(identifier)
	seed := encryptionSeed{ID: id, Method: "1.1.1"}
	if len(options.Password) != 0 {
		kek, err := provider.PasswordKey(options.Password)
		if err != nil {
			return nil, err
		}
		wrapped, err := provider.EncryptSM4(kek, iv, key)
		clear(kek)
		if err != nil {
			return nil, err
		}
		name := options.UserName
		if name == "" {
			name = "User"
		}
		seed.Users = []encryptionUser{{Name: name, Role: options.UserType, Key: base64.StdEncoding.EncodeToString(wrapped), IV: base64.StdEncoding.EncodeToString(iv)}}
	} else {
		seed.Method = "1.1.2"
		for _, recipient := range options.Recipients {
			certificate, err := ParseEncryptionCertificate(recipient.Certificate)
			if err != nil {
				return nil, err
			}
			wrapped, err := provider.EncryptKey(certificate, key)
			if err != nil {
				return nil, err
			}
			seed.Users = append(seed.Users, encryptionUser{Name: recipient.UserName, Role: recipient.UserType, Certificate: base64.StdEncoding.EncodeToString(certificate), Key: base64.StdEncoding.EncodeToString(wrapped), IV: base64.StdEncoding.EncodeToString(iv)})
		}
	}
	entries := encryptionEntries{XMLName: xml.Name{Space: ofdNamespace, Local: "EncryptEntries"}, ID: id}
	cipherParts := make(map[string][]byte, len(parts)+3)
	for i, name := range slices.Sorted(maps.Keys(parts)) {
		if err := progress.report("encrypt", i, len(parts)); err != nil {
			return nil, err
		}
		encryptedName := fmt.Sprintf("Encrypt/Files/%d.dat", i)
		data, err := provider.EncryptSM4(key, iv, parts[name])
		if err != nil {
			return nil, err
		}
		cipherParts[encryptedName] = data
		entries.Entries = append(entries.Entries, encryptionEntry{Path: "/" + name, EncryptedPath: "/" + encryptedName})
	}
	mapData, err := xml.Marshal(entries)
	if err != nil {
		return nil, err
	}
	mapData, err = provider.EncryptSM4(key, iv, mapData)
	if err != nil {
		return nil, err
	}
	cipherParts["Encrypt/Entries.dat"] = mapData
	seedData, err := xml.Marshal(seed)
	if err != nil {
		return nil, err
	}
	cipherParts["Encrypt/DecryptSeed.dat"] = seedData
	list := encryptionList{Records: []encryptionRecord{{ID: id, Provider: encryptionProvider{Name: "OFDGo", Version: "1.0"}, Scope: "All", Date: time.Now().Format(time.RFC3339), Seed: "/Encrypt/DecryptSeed.dat", Entries: "/Encrypt/Entries.dat"}}}
	listData, err := xml.Marshal(list)
	if err != nil {
		return nil, err
	}
	cipherParts["Encryptions.xml"] = listData
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	for _, name := range slices.Sorted(maps.Keys(cipherParts)) {
		entry, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write(cipherParts[name]); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	if err := progress.report("encrypt", len(parts), len(parts)); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
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
