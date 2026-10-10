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

import "context"

// addLifecycleActions 按事件归属收集打开动作，不受点击区域限制
// 入参: ctx 取消上下文, source 动作来源, page 页面索引，负值表示文档, bookmarks 书签, pageIndex 页面索引表, pages 页面数据
// 返回: error 目标或取消错误
func (n *pdfNavigation) addLifecycleActions(ctx context.Context, source actionSource, page int, bookmarks map[string]Dest, pageIndex map[string]int, pages []RenderDocumentPage) error {
	event := "PO"
	if page < 0 {
		event = "DO"
	}
	for _, action := range source.Actions {
		if err := ctx.Err(); err != nil {
			return err
		}
		if action.Event != event || source.Graphic {
			continue
		}
		value, err := n.actionTarget(action, page, source, bookmarks, pageIndex, pages)
		if err != nil {
			return err
		}
		if value == nil {
			continue
		}
		if action.Event == "DO" {
			n.Open = append(n.Open, *value)
		} else {
			n.PageOpen[page] = append(n.PageOpen[page], *value)
		}
		n.nativeWrite = true
	}
	return nil
}
