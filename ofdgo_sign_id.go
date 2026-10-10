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
	"fmt"
	"strconv"
	"strings"
)

// signatureIDSequence 签名及签章注释共用的编号上限，与文档图元编号相互独立
type signatureIDSequence uint64

// observe 记录已有签章标识的编号上限，其他命名形式保持不变
// 入参: id 已有标识
func (s *signatureIDSequence) observe(id string) {
	n, err := strconv.ParseUint(strings.TrimPrefix(id, "s"), 10, 64)
	if err == nil && n > uint64(*s) {
		*s = signatureIDSequence(n)
	}
}

// next 分配采用sNNN格式的签章标识
// 返回: string 新标识, error 编号耗尽错误
func (s *signatureIDSequence) next() (string, error) {
	if uint64(*s) == ^uint64(0) {
		return "", fmt.Errorf("signature ID exhausted")
	}
	*s++
	return "s" + strconv.FormatUint(uint64(*s), 10), nil
}
