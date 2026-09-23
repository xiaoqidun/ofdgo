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
	"io"
)

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
		name := options.UserName
		if name == "" {
			name = "User"
		}
		info.Users = []string{name}
	} else {
		info.Method = "1.1.2"
		for _, recipient := range options.Recipients {
			info.Users = append(info.Users, recipient.UserName)
		}
	}
	e.encryption = &encryptionState{info: info, options: cloneEncryptionOptions(options)}
	return nil
}

// writeEncrypted 在全部准备及加密成功后写出结果，取消不产生输出
// 输出流自身写入失败时调用方仍需丢弃本次输出
// 入参: writer 输出流
// 返回: int64 写入字节数, error 错误信息
func (e *Editor) writeEncrypted(writer io.Writer) (int64, error) {
	if e.encryption.options == nil {
		return 0, ErrEncryptionPolicyRequired
	}
	var plaintext bytes.Buffer
	if _, err := e.writePlaintext(&plaintext); err != nil {
		return 0, err
	}
	defer clear(plaintext.Bytes())
	r, err := NewReader(bytes.NewReader(plaintext.Bytes()), int64(plaintext.Len()))
	if err != nil {
		return 0, err
	}
	defer r.Close()
	parts, err := r.packageData()
	if err != nil {
		return 0, err
	}
	defer func() {
		for _, data := range parts {
			clear(data)
		}
	}()
	data, err := encryptPackageParts(parts, *e.encryption.options, editorProgress(e.OnWriteProgress))
	if err != nil {
		return 0, err
	}
	return io.Copy(writer, bytes.NewReader(data))
}
