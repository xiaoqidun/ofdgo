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
	"context"
	"fmt"

	"github.com/xiaoqidun/pdfgo"
)

// soundAction 保留音量、循环及等待语义，PDF同步播放同时限制交互
// 入参: action OFD音频动作
// 返回: *pdfNavigationAction PDF音频动作, error 资源或参数错误
func (n *pdfNavigation) soundAction(action Sound) (*pdfNavigationAction, error) {
	volume := 100
	if action.Volume != nil {
		volume = *action.Volume
	}
	if volume < 0 || volume > 100 {
		return nil, fmt.Errorf("invalid sound volume")
	}
	file := n.audio[action.ResourceID]
	if file == nil {
		media, err := n.source.Media(action.ResourceID)
		if err != nil {
			return nil, err
		}
		if media.Type != "Audio" {
			return nil, fmt.Errorf("sound resource is not audio: %s", action.ResourceID)
		}
		if n.audio == nil {
			n.audio = make(map[string]*pdfgo.FileSpecification)
		}
		file = n.mediaFile(media)
		n.audio[action.ResourceID] = file
	}
	return &pdfNavigationAction{Sound: &pdfgo.SoundAction{
		Sound: pdfgo.Sound{File: file}, Volume: float64(volume) / 100,
		Repeat: action.Repeat, Synchronous: action.Synchronous && !action.Repeat,
	}}, nil
}

// loadAudio 在绘制后读取自描述音频文件，原始内容不作采样转换
// 入参: ctx 取消上下文
// 返回: error 资源读取或取消错误
func (n *pdfNavigation) loadAudio(ctx context.Context) error {
	return n.loadMediaFiles(ctx, n.audio)
}
