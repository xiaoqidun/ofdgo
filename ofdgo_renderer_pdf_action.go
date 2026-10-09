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
	"slices"

	"github.com/tdewolff/canvas"
	"github.com/xiaoqidun/pdfgo"
)

// pdfActionArea 保存互不重叠的点击区域及其有序动作
type pdfActionArea struct {
	Path    GeometryPath
	Actions []pdfNavigationAction
}

// addActions 按命中区域组合同一对象的点击动作，不合并其他对象的事件
// 入参: renderer 渲染器, page 页面索引, source 动作来源, bookmarks 书签, pageIndex 页面索引表, pages 页面数据
// 返回: error 区域、目标或取消错误
func (n *pdfNavigation) addActions(renderer *Renderer, page int, source actionSource, bookmarks map[string]Dest, pageIndex map[string]int, pages []RenderDocumentPage) error {
	ctx := renderer.outputContext()
	var areas []pdfActionArea
	for _, action := range source.Actions {
		if err := ctx.Err(); err != nil {
			return err
		}
		if action.Event != "CLICK" {
			continue
		}
		value, err := pdfNavigationTarget(action, bookmarks, pageIndex, pages)
		if err != nil {
			return err
		}
		if value == nil {
			continue
		}
		box, outline, err := renderer.actionLinkRegion(source, action)
		if err != nil {
			return err
		}
		if box.W <= 0 || box.H <= 0 {
			continue
		}
		path := geometryRectangle(box)
		if outline != "" {
			parsed, err := canvas.ParseSVGPath(outline)
			if err != nil {
				return err
			}
			if parsed.Len() > pdfLinkEdgeLimit {
				return fmt.Errorf("PDF link region exceeds edge limit")
			}
			parsed, err = flattenPDFLink(ctx, parsed)
			if err != nil {
				return err
			}
			path = *geometryFromCanvasPath(parsed.Transform(canvas.Identity.Scale(1, -1)))
		}
		areas, err = partitionPDFActions(ctx, renderer, areas, path, *value)
		if err != nil {
			return err
		}
	}
	for _, area := range areas {
		if err := ctx.Err(); err != nil {
			return err
		}
		box, err := area.Path.Bounds()
		if err != nil {
			return err
		}
		if box.W <= 0 || box.H <= 0 {
			continue
		}
		outline, err := area.Path.SVG()
		if err != nil {
			return err
		}
		region, err := pdfActionRegion(ctx, outline, pages[page].Box.H)
		if err != nil {
			return err
		}
		if region != nil && len(region.Quads) == 0 {
			continue
		}
		n.Link[page] = append(n.Link[page], pdfLink{Rect: pdfSourceRect(box, pages[page].Box.H), Region: region, Actions: area.Actions})
		n.exactLinks = true
	}
	return nil
}

// partitionPDFActions 将新增动作区域与已有区域分离，使每个区域具有唯一动作序列
// 入参: ctx 取消上下文, renderer 渲染器, areas 已有分区, path 新动作区域, action 新动作
// 返回: []pdfActionArea 新分区, error 几何、复杂度或取消错误
func partitionPDFActions(ctx context.Context, renderer *Renderer, areas []pdfActionArea, path GeometryPath, action pdfNavigationAction) ([]pdfActionArea, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(areas) == 1 && slices.Equal(areas[0].Path, path) {
		areas[0].Actions = append(areas[0].Actions, action)
		return areas, nil
	}
	remaining := path
	result := make([]pdfActionArea, 0, len(areas)+1)
	box, err := path.Bounds()
	if err != nil {
		return nil, err
	}
	for _, area := range areas {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		other, err := area.Path.Bounds()
		if err != nil {
			return nil, err
		}
		if box.X >= other.X+other.W || other.X >= box.X+box.W || box.Y >= other.Y+other.H || other.Y >= box.Y+box.H {
			result = append(result, area)
			continue
		}
		geometry, err := renderer.Geometry()
		if err != nil {
			return nil, err
		}
		common, err := combinePDFActionRegion(ctx, geometry, area.Path, path, GeometryIntersect)
		if err != nil {
			return nil, err
		}
		if len(common) == 0 {
			result = append(result, area)
			continue
		}
		only, err := combinePDFActionRegion(ctx, geometry, area.Path, path, GeometrySubtract)
		if err != nil {
			return nil, err
		}
		if len(only) != 0 {
			result = append(result, pdfActionArea{Path: only, Actions: area.Actions})
		}
		result = append(result, pdfActionArea{Path: common, Actions: append(slices.Clone(area.Actions), action)})
		remaining, err = combinePDFActionRegion(ctx, geometry, remaining, area.Path, GeometrySubtract)
		if err != nil {
			return nil, err
		}
		if len(result) > pdfLinkEdgeLimit {
			return nil, fmt.Errorf("PDF action partition exceeds region limit")
		}
	}
	if len(remaining) != 0 {
		result = append(result, pdfActionArea{Path: remaining, Actions: []pdfNavigationAction{action}})
	}
	if len(result) > pdfLinkEdgeLimit {
		return nil, fmt.Errorf("PDF action partition exceeds region limit")
	}
	edges := 0
	for _, area := range result {
		edges += len(area.Path)
		if edges > pdfLinkQuadLimit {
			return nil, fmt.Errorf("PDF action partition exceeds total edge limit")
		}
	}
	return result, ctx.Err()
}

// combinePDFActionRegion 对有界点击路径执行布尔运算，检查取消及中间结果规模
// 入参: ctx 取消上下文, geometry 几何后端, left 左路径, right 右路径, operation 运算类型
// 返回: GeometryPath 结果路径, error 几何、复杂度或取消错误
func combinePDFActionRegion(ctx context.Context, geometry GeometryBackend, left, right GeometryPath, operation GeometryOperation) (GeometryPath, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(left) > pdfLinkEdgeLimit || len(right) > pdfLinkEdgeLimit {
		return nil, fmt.Errorf("PDF action partition exceeds edge limit")
	}
	result, err := geometry.Combine(left, right, operation)
	if err != nil {
		return nil, err
	}
	if len(result) > pdfLinkEdgeLimit {
		return nil, fmt.Errorf("PDF action partition exceeds edge limit")
	}
	return result, ctx.Err()
}

// pdfNavigationValues 将输出页索引替换为实际PDF页面引用
// 入参: actions 有序导航动作, pages 输出页面引用
// 返回: []pdfgo.NavigationAction 原生导航动作
func pdfNavigationValues(actions []pdfNavigationAction, pages []pdfgo.Reference) []pdfgo.NavigationAction {
	values := make([]pdfgo.NavigationAction, 0, len(actions))
	for _, action := range actions {
		value := pdfgo.NavigationAction{URI: action.URI}
		if action.Target != nil {
			dest := action.Target.Destination
			dest.Page = pages[action.Target.Page]
			value.Destination = &dest
		}
		values = append(values, value)
	}
	return values
}
