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
	"sync"

	"github.com/tdewolff/canvas"
)

// canvasFontLoadMu 保护绘图后端加载无名称字体时使用的共享状态
var canvasFontLoadMu sync.Mutex

// loadCanvasFont 串行加载绘图字体，不改动原始字体数据
// 入参: data 字体数据, index 集合索引, style 字体样式
// 返回: *canvas.Font 绘图字体, error 加载错误
func loadCanvasFont(data []byte, index int, style canvas.FontStyle) (*canvas.Font, error) {
	canvasFontLoadMu.Lock()
	defer canvasFontLoadMu.Unlock()
	return canvas.LoadFont(data, index, style)
}

// decodeFontContainer 展开字体封装，保留原始OpenType表及CFF2轮廓
// 入参: data 字体文件
// 返回: []byte OpenType数据, error 格式错误
func decodeFontContainer(data []byte) ([]byte, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("invalid font header")
	}
	switch string(data[:4]) {
	case "\x00\x01\x00\x00", "OTTO", "true", "ttcf":
		return data, nil
	case "wOFF":
		return decodeWOFF(data)
	case "wOF2":
		decoded, handled, err := decodeWOFF2(data)
		if handled {
			return decoded, err
		}
	}
	loaded, err := loadCanvasFont(data, 0, canvas.FontRegular)
	if err != nil {
		return nil, err
	}
	return serializeOTF(loaded.Tables)
}
