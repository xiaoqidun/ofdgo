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
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
)

// ErrPrintDenied 表示文档不允许打印或请求份数超出声明上限
var ErrPrintDenied = errors.New("document printing is not permitted")

// PrintOptions 设置内容排版，长度以毫米为单位，比例1表示100%
// Pages为零基有序页码，nil表示全部，非nil空列表表示不选择页面；与PageRange互斥
// Mode支持single、multiple、booklet、poster；Scaling支持fit、shrink、actual、custom
// PaperWidth和PaperHeight为实际纸面尺寸，显式提供时不自动交换方向
// 两者同时为零时，单页沿用源尺寸，其他模式采用A4，小册子默认横向
// Parity支持all、odd、even，按源页码过滤；Sides支持all、front、back，按输出纸面过滤
// BookletSheets为每册纸张数，0表示整份一册；Binding支持left、right
// Rows和Columns仅用于多页，Gap为单元间隔，Overlap为海报相邻纸面的重叠
// Duplex支持simplex、longEdge、shortEdge；小册子默认shortEdge，其他模式默认simplex
// PrintableArea为已选设备当前方向下的可打印区域，Margins为该区域内的额外留白
// Reverse反转纸张输出顺序，保留每张纸内部的正背面与拼版顺序
// Copies和Collate描述设备作业，不展开重复内容；SheetFrom/SheetTo为一基纸张范围
// Contents支持documentAndAnnotations、document、annotations、forms，document保留表单外观
// PageOrder支持horizontal、horizontalReverse、vertical、verticalReverse
// BackRotate将背面绕纸张中心旋转180度；海报Labels保留底部5毫米绘制拼接标签
// Scale用于自定义缩放及海报分块，零值采用1；Rotate允许自动旋转页面以适应单元
// Raster将纸面光栅化，Grayscale以光栅方式输出灰度；DPI默认300，上限2400
// Borders绘制单元边框，CropMarks绘制裁切标记，TileOnlyLarge仅分块超大页面
type PrintOptions struct {
	Pages         []int        `json:"pages,omitempty"`
	PageRange     string       `json:"pageRange,omitempty"`
	Mode          string       `json:"mode,omitempty"`
	Scaling       string       `json:"scaling,omitempty"`
	PaperWidth    float64      `json:"paperWidth,omitempty"`
	PaperHeight   float64      `json:"paperHeight,omitempty"`
	Margins       PrintMargins `json:"margins"`
	PrintableArea *Box         `json:"printableArea,omitempty"`
	Duplex        string       `json:"duplex,omitempty"`
	BackRotate    bool         `json:"backRotate,omitempty"`
	Copies        int          `json:"copies,omitempty"`
	Collate       *bool        `json:"collate,omitempty"`
	SheetFrom     int          `json:"sheetFrom,omitempty"`
	SheetTo       int          `json:"sheetTo,omitempty"`
	Scale         float64      `json:"scale,omitempty"`
	Rotate        bool         `json:"rotate,omitempty"`
	Reverse       bool         `json:"reverse,omitempty"`
	Parity        string       `json:"parity,omitempty"`
	Sides         string       `json:"sides,omitempty"`
	Rows          int          `json:"rows,omitempty"`
	Columns       int          `json:"columns,omitempty"`
	// ColumnOrder 兼容纵向填充设置，仅在PageOrder为空时生效
	ColumnOrder   bool    `json:"columnOrder,omitempty"`
	PageOrder     string  `json:"pageOrder,omitempty"`
	Gap           float64 `json:"gap,omitempty"`
	Overlap       float64 `json:"overlap,omitempty"`
	BookletSheets int     `json:"bookletSheets,omitempty"`
	Binding       string  `json:"binding,omitempty"`
	// Annotations 兼容注解开关，仅在Contents为空时生效
	Annotations   *bool   `json:"annotations,omitempty"`
	Contents      string  `json:"contents,omitempty"`
	Raster        bool    `json:"raster,omitempty"`
	DPI           float64 `json:"dpi,omitempty"`
	Grayscale     bool    `json:"grayscale,omitempty"`
	Borders       bool    `json:"borders,omitempty"`
	CropMarks     bool    `json:"cropMarks,omitempty"`
	TileOnlyLarge bool    `json:"tileOnlyLarge,omitempty"`
	Labels        bool    `json:"labels,omitempty"`
}

// PrintMargins 保存纸面四边的非负页边距
type PrintMargins struct {
	Top    float64 `json:"top"`
	Right  float64 `json:"right"`
	Bottom float64 `json:"bottom"`
	Left   float64 `json:"left"`
}

// PrintPlacement 将归一化源页面坐标映射到纸面，Transform按a、b、c、d、e、f排列
// Clip为纸面坐标裁剪；Page从零开始，同一页面可以多次放置
// Width和Height为归一化源页尺寸；Clipped表示变换后的内容超出裁剪区域
type PrintPlacement struct {
	Page      int        `json:"page"`
	Width     float64    `json:"width"`
	Height    float64    `json:"height"`
	Transform [6]float64 `json:"transform"`
	Clip      Box        `json:"clip"`
	Clipped   bool       `json:"clipped"`
}

// PrintSide 保存一个输出纸面，Number是过滤前的一基纸面序号，Sheet为一基纸张编号
// Back仅在双面作业中表示背面，TileRow/TileColumn为海报的一基拼接位置
type PrintSide struct {
	Number      int              `json:"number"`
	Sheet       int              `json:"sheet"`
	Back        bool             `json:"back"`
	Width       float64          `json:"width"`
	Height      float64          `json:"height"`
	Placements  []PrintPlacement `json:"placements"`
	TileRow     int              `json:"tileRow,omitempty"`
	TileColumn  int              `json:"tileColumn,omitempty"`
	TileRows    int              `json:"tileRows,omitempty"`
	TileColumns int              `json:"tileColumns,omitempty"`
	Label       string           `json:"label,omitempty"`
	LabelX      float64          `json:"labelX,omitempty"`
	LabelY      float64          `json:"labelY,omitempty"`
}

// PrintLayout 保存独立排版结果，SourcePages为选中源页数，CopiesLimit为文档声明的份数上限
// CopiesLimit为-1时无限制；纯布局计算不绑定文档，默认同样为-1
// Sheets为筛选后的纸张数；BackContent表示正背面筛选前存在含页面内容的背面
type PrintLayout struct {
	SourcePages int         `json:"sourcePages"`
	CopiesLimit int         `json:"copiesLimit"`
	Copies      int         `json:"copies"`
	Collate     bool        `json:"collate"`
	Duplex      string      `json:"duplex"`
	Sheets      int         `json:"sheets"`
	BackContent bool        `json:"backContent"`
	Sides       []PrintSide `json:"sides"`
}

// PrintDocument 固定排版和渲染配置，可串行输出多个纸面，Close后不可再渲染
// Renderer.PreparePrint借用输入，使用结束前不得关闭或修改源输入及外部字体
// Editor.PreparePrint创建独立编辑快照，不要求保留原编辑器；外部字体仍须可读
// 各方法以互斥锁串行执行，Layout返回独立副本；取消只中断当前输出，不销毁任务
type PrintDocument struct {
	mu       sync.Mutex
	renderer *Renderer
	layout   PrintLayout
	options  PrintOptions
	pages    *renderCache[int, []byte]
	closed   bool
}

// PlanPrint 仅根据源页面区域和选项计算打印布局，不读取文档或调用设备
// 入参: pages 按文档顺序排列的页面区域, options 打印内容选项
// 返回: PrintLayout 有序纸面, error 尺寸、页码或选项错误
func PlanPrint(pages []Box, options PrintOptions) (PrintLayout, error) {
	o, err := normalizePrintOptions(options)
	if err != nil {
		return PrintLayout{CopiesLimit: -1}, err
	}
	return planPrint(pages, o)
}

// planPrint 按已校验的选项计算纸面，供独立布局和打印任务共用
// 入参: pages 按文档顺序排列的页面区域, o 规范排版选项
// 返回: PrintLayout 有序纸面, error 尺寸、页码或排版错误
func planPrint(pages []Box, o PrintOptions) (PrintLayout, error) {
	result := PrintLayout{CopiesLimit: -1}
	if len(pages) == 0 {
		return result, fmt.Errorf("no pages to print")
	}
	for _, page := range pages {
		if !printPositive(page.W) || !printPositive(page.H) {
			return result, fmt.Errorf("invalid print page size")
		}
	}
	indices := o.Pages
	if o.PageRange != "" {
		var err error
		indices, err = ParsePageRange(o.PageRange, len(pages))
		if err != nil {
			return result, err
		}
	} else if o.Pages == nil {
		indices = make([]int, len(pages))
		for i := range indices {
			indices[i] = i
		}
	}
	selected := make([]int, 0, len(indices))
	for _, index := range indices {
		if index < 0 || index >= len(pages) {
			return result, fmt.Errorf("print page index %d outside document", index)
		}
		if o.Parity == "odd" && index%2 != 0 || o.Parity == "even" && index%2 == 0 {
			continue
		}
		selected = append(selected, index)
	}
	if len(selected) == 0 {
		return result, fmt.Errorf("no pages selected for printing")
	}
	result.SourcePages = len(selected)
	result.Copies, result.Collate, result.Duplex = o.Copies, *o.Collate, o.Duplex
	addSide := func(width, height float64, order []int, rows, columns int) error {
		area := printPageArea(width, height, o)
		cw, ch := (area.W-float64(columns-1)*o.Gap)/float64(columns), (area.H-float64(rows-1)*o.Gap)/float64(rows)
		if !printPositive(cw) || !printPositive(ch) {
			return fmt.Errorf("print margins or gap exceed paper size")
		}
		side := PrintSide{Width: width, Height: height, Placements: []PrintPlacement{}}
		for cell, index := range order {
			if index < 0 {
				continue
			}
			x, y := cell%columns, cell/columns
			if o.PageOrder == "vertical" || o.PageOrder == "verticalReverse" {
				x, y = cell/rows, cell%rows
			}
			if o.PageOrder == "horizontalReverse" || o.PageOrder == "verticalReverse" {
				x = columns - 1 - x
			}
			box := Box{X: area.X + float64(x)*(cw+o.Gap), Y: area.Y + float64(y)*(ch+o.Gap), W: cw, H: ch}
			placement := printPlacement(index, pages[index], box, o)
			for _, value := range placement.Transform {
				if math.IsNaN(value) || math.IsInf(value, 0) {
					return fmt.Errorf("invalid print placement")
				}
			}
			side.Placements = append(side.Placements, placement)
		}
		if o.Mode == "poster" && o.Labels && len(order) > 0 {
			side.Label = fmt.Sprintf("Page %d  Row 1/1  Column 1/1", order[0]+1)
			side.LabelX, side.LabelY = area.X+1, area.Y+area.H+3.5
		}
		result.Sides = append(result.Sides, side)
		return nil
	}
	w, h := o.PaperWidth, o.PaperHeight
	if w == 0 && o.Mode != "single" {
		w, h = 210, 297
		if o.Mode == "booklet" {
			w, h = h, w
		}
	}
	switch o.Mode {
	case "single":
		for _, index := range selected {
			pw, ph := w, h
			if pw == 0 {
				pw, ph = pages[index].W, pages[index].H
			}
			if err := addSide(pw, ph, []int{index}, 1, 1); err != nil {
				return PrintLayout{}, err
			}
		}
	case "multiple":
		count := o.Rows * o.Columns
		for start := 0; start < len(selected); start += count {
			if err := addSide(w, h, selected[start:min(start+count, len(selected))], o.Rows, o.Columns); err != nil {
				return PrintLayout{}, err
			}
		}
	case "booklet":
		chunk := ((len(selected) + 3) / 4) * 4
		if o.BookletSheets > 0 {
			chunk = o.BookletSheets * 4
		}
		for start := 0; start < len(selected); start += chunk {
			end := min(start+chunk, len(selected))
			order := make([]int, ((end-start+3)/4)*4)
			for i := range order {
				order[i] = -1
			}
			copy(order, selected[start:end])
			for i := 0; i < len(order)/4; i++ {
				for _, pair := range [][]int{{order[len(order)-1-i*2], order[i*2]}, {order[i*2+1], order[len(order)-2-i*2]}} {
					if o.Binding == "right" {
						slices.Reverse(pair)
					}
					if err := addSide(w, h, pair, 1, 2); err != nil {
						return PrintLayout{}, err
					}
				}
			}
		}
	case "poster":
		area := printPageArea(w, h, o)
		if area.W <= o.Overlap || area.H <= o.Overlap {
			return result, fmt.Errorf("poster overlap or margins exceed paper size")
		}
		for _, index := range selected {
			pw, ph := pages[index].W*o.Scale, pages[index].H*o.Scale
			if o.TileOnlyLarge && pw <= area.W && ph <= area.H {
				if err := addSide(w, h, []int{index}, 1, 1); err != nil {
					return PrintLayout{}, err
				}
				continue
			}
			columns, rows := max(1, math.Ceil((pw-o.Overlap)/(area.W-o.Overlap))), max(1, math.Ceil((ph-o.Overlap)/(area.H-o.Overlap)))
			if !printPositive(columns*rows) || columns*rows > 10000 || len(result.Sides)+int(columns*rows) > 10000 {
				return result, fmt.Errorf("too many poster sides")
			}
			for row := 0; row < int(rows); row++ {
				for column := 0; column < int(columns); column++ {
					m := [6]float64{o.Scale, 0, 0, o.Scale, area.X - float64(column)*(area.W-o.Overlap), area.Y - float64(row)*(area.H-o.Overlap)}
					side := PrintSide{Width: w, Height: h, TileRow: row + 1, TileColumn: column + 1, TileRows: int(rows), TileColumns: int(columns), Placements: []PrintPlacement{{Page: index, Width: pages[index].W, Height: pages[index].H, Transform: m, Clip: area}}}
					if o.Labels {
						side.Label = fmt.Sprintf("Page %d  Row %d/%d  Column %d/%d", index+1, row+1, int(rows), column+1, int(columns))
						side.LabelX, side.LabelY = area.X+1, area.Y+area.H+3.5
					}
					result.Sides = append(result.Sides, side)
				}
			}
		}
	}
	if o.Duplex != "simplex" {
		paired := make([]PrintSide, 0, len(result.Sides)+1)
		for _, side := range result.Sides {
			if len(paired)%2 == 1 {
				previous := paired[len(paired)-1]
				if previous.Width != side.Width || previous.Height != side.Height {
					paired = append(paired, PrintSide{Width: previous.Width, Height: previous.Height, Placements: []PrintPlacement{}})
				}
			}
			paired = append(paired, side)
		}
		if len(paired)%2 == 1 {
			previous := paired[len(paired)-1]
			paired = append(paired, PrintSide{Width: previous.Width, Height: previous.Height, Placements: []PrintPlacement{}})
		}
		result.Sides = paired
	}
	for i := range result.Sides {
		result.Sides[i].Number = i + 1
		result.Sides[i].Sheet = i + 1
		if o.Duplex != "simplex" {
			result.Sides[i].Sheet = i/2 + 1
			result.Sides[i].Back = i%2 == 1
		}
		if o.BackRotate && result.Sides[i].Back {
			for j := range result.Sides[i].Placements {
				placement := &result.Sides[i].Placements[j]
				m := placement.Transform
				placement.Transform = [6]float64{-m[0], -m[1], -m[2], -m[3], result.Sides[i].Width - m[4], result.Sides[i].Height - m[5]}
				placement.Clip.X = result.Sides[i].Width - placement.Clip.X - placement.Clip.W
				placement.Clip.Y = result.Sides[i].Height - placement.Clip.Y - placement.Clip.H
				area := printPageArea(result.Sides[i].Width, result.Sides[i].Height, o)
				x, y := math.Max(area.X, placement.Clip.X), math.Max(area.Y, placement.Clip.Y)
				right, bottom := math.Min(area.X+area.W, placement.Clip.X+placement.Clip.W), math.Min(area.Y+area.H, placement.Clip.Y+placement.Clip.H)
				if right-x < placement.Clip.W-1e-6 || bottom-y < placement.Clip.H-1e-6 {
					placement.Clipped = true
				}
				placement.Clip = Box{X: x, Y: y, W: math.Max(0, right-x), H: math.Max(0, bottom-y)}
			}
		}
	}
	lastSheet := result.Sides[len(result.Sides)-1].Sheet
	if o.SheetFrom > lastSheet || o.SheetTo > lastSheet {
		return result, fmt.Errorf("print sheet outside layout")
	}
	for _, side := range result.Sides {
		if side.Back && len(side.Placements) > 0 && (o.SheetFrom == 0 || side.Sheet >= o.SheetFrom) && (o.SheetTo == 0 || side.Sheet <= o.SheetTo) {
			result.BackContent = true
			break
		}
	}
	if o.Sides != "all" || o.SheetFrom != 0 || o.SheetTo != 0 {
		filtered := make([]PrintSide, 0, len(result.Sides))
		for _, side := range result.Sides {
			if o.SheetFrom > 0 && side.Sheet < o.SheetFrom || o.SheetTo > 0 && side.Sheet > o.SheetTo {
				continue
			}
			if o.Sides == "all" || o.Sides == "front" && !side.Back || o.Sides == "back" && side.Back {
				filtered = append(filtered, side)
			}
		}
		result.Sides = filtered
	}
	if len(result.Sides) == 0 {
		return result, fmt.Errorf("no print sides selected")
	}
	if o.Reverse {
		slices.Reverse(result.Sides)
		for i := 0; i+1 < len(result.Sides); i++ {
			if result.Sides[i].Sheet == result.Sides[i+1].Sheet {
				result.Sides[i], result.Sides[i+1] = result.Sides[i+1], result.Sides[i]
				i++
			}
		}
	}
	previous := 0
	for _, side := range result.Sides {
		if side.Sheet != previous {
			result.Sheets++
			previous = side.Sheet
		}
	}
	return result, nil
}

// normalizePrintOptions 校验有限尺寸并填充公共默认值
// 入参: o 原始排版选项
// 返回: PrintOptions 独立规范选项, error 配置错误
func normalizePrintOptions(o PrintOptions) (PrintOptions, error) {
	if o.Mode == "" {
		o.Mode = "single"
	}
	if o.Scaling == "" {
		o.Scaling = "shrink"
	}
	if o.Parity == "" {
		o.Parity = "all"
	}
	if o.Sides == "" {
		o.Sides = "all"
	}
	if o.Binding == "" {
		o.Binding = "left"
	}
	if o.Duplex == "" {
		o.Duplex = "simplex"
		if o.Mode == "booklet" {
			o.Duplex = "shortEdge"
		}
	}
	if o.Copies == 0 {
		o.Copies = 1
	}
	if o.Collate == nil {
		collate := true
		o.Collate = &collate
	} else {
		collate := *o.Collate
		o.Collate = &collate
	}
	if o.PageOrder == "" {
		o.PageOrder = "horizontal"
		if o.ColumnOrder {
			o.PageOrder = "vertical"
		}
	}
	if o.Contents == "" {
		o.Contents = "documentAndAnnotations"
		if o.Annotations != nil && !*o.Annotations {
			o.Contents = "document"
		}
	}
	o.ColumnOrder, o.Annotations = false, nil
	if !slices.Contains([]string{"document", "documentAndAnnotations", "annotations", "forms"}, o.Contents) {
		return o, fmt.Errorf("invalid print contents")
	}
	if o.Scale == 0 {
		o.Scale = 1
	}
	if o.DPI == 0 {
		o.DPI = 300
	}
	if o.Rows == 0 {
		o.Rows = 1
	}
	if o.Columns == 0 {
		o.Columns = 2
	}
	if !slices.Contains([]string{"single", "multiple", "booklet", "poster"}, o.Mode) || !slices.Contains([]string{"fit", "shrink", "actual", "custom"}, o.Scaling) || !slices.Contains([]string{"all", "odd", "even"}, o.Parity) || !slices.Contains([]string{"all", "front", "back"}, o.Sides) || !slices.Contains([]string{"left", "right"}, o.Binding) || !slices.Contains([]string{"simplex", "longEdge", "shortEdge"}, o.Duplex) || !slices.Contains([]string{"horizontal", "horizontalReverse", "vertical", "verticalReverse"}, o.PageOrder) {
		return o, fmt.Errorf("invalid print mode")
	}
	if o.Copies < 1 || o.Copies > 999 || o.SheetFrom < 0 || o.SheetTo < 0 || o.SheetTo > 0 && o.SheetTo < o.SheetFrom || o.Duplex == "simplex" && (o.Mode == "booklet" || o.Sides != "all") {
		return o, fmt.Errorf("invalid print job options")
	}
	if o.PrintableArea != nil {
		area := *o.PrintableArea
		if !printPositive(area.W) || !printPositive(area.H) || !printPositive(area.X+1) || !printPositive(area.Y+1) || area.X < 0 || area.Y < 0 || o.PaperWidth <= 0 || area.X+area.W > o.PaperWidth+1e-6 || area.Y+area.H > o.PaperHeight+1e-6 {
			return o, fmt.Errorf("invalid printer printable area")
		}
		o.PrintableArea = &area
	}
	for _, v := range []float64{o.PaperWidth, o.PaperHeight, o.Margins.Top, o.Margins.Right, o.Margins.Bottom, o.Margins.Left, o.Gap, o.Overlap} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return o, fmt.Errorf("invalid print dimensions")
		}
	}
	if (o.PaperWidth == 0) != (o.PaperHeight == 0) || !printPositive(o.Scale) || o.Scale > 100 || !printPositive(o.DPI) || o.DPI > 2400 || o.Rows < 1 || o.Columns < 1 || o.Rows > 16 || o.Columns > 16 || o.BookletSheets < 0 || o.BookletSheets > 2500 || o.Pages != nil && o.PageRange != "" {
		return o, fmt.Errorf("invalid print options")
	}
	o.Pages = slices.Clone(o.Pages)
	return o, nil
}

// printPositive 检查正有限数
// 入参: v 待校验数值
// 返回: bool 是否为正有限数
func printPositive(v float64) bool {
	return v > 0 && !math.IsInf(v, 0) && !math.IsNaN(v)
}

// printPageArea 在设备可打印区域内应用额外留白，未知设备时使用整张纸
// 入参: w 纸面宽度, h 纸面高度, o 排版选项
// 返回: Box 应用额外边距后的毫米区域
func printPageArea(w, h float64, o PrintOptions) Box {
	area := Box{W: w, H: h}
	if o.PrintableArea != nil {
		area = *o.PrintableArea
	}
	area.X += o.Margins.Left
	area.Y += o.Margins.Top
	area.W -= o.Margins.Left + o.Margins.Right
	area.H -= o.Margins.Top + o.Margins.Bottom
	if o.Mode == "poster" && o.Labels {
		area.H -= 5
	}
	return area
}

// printPlacement 计算居中及自动旋转后的单元变换
// 入参: index 零基源页码, source 源页面区域, cell 纸面单元区域, o 排版选项
// 返回: PrintPlacement 页面放置及裁剪信息
func printPlacement(index int, source, cell Box, o PrintOptions) PrintPlacement {
	w, h := source.W, source.H
	rotated := o.Rotate && math.Min(cell.W/h, cell.H/w) > math.Min(cell.W/w, cell.H/h)
	if rotated {
		w, h = h, w
	}
	scale := 1.0
	switch o.Scaling {
	case "fit":
		scale = math.Min(cell.W/w, cell.H/h)
	case "shrink":
		scale = math.Min(1, math.Min(cell.W/w, cell.H/h))
	case "custom":
		scale = o.Scale
	}
	x, y := cell.X+(cell.W-w*scale)/2, cell.Y+(cell.H-h*scale)/2
	m := [6]float64{scale, 0, 0, scale, x, y}
	if rotated {
		m = [6]float64{0, scale, -scale, 0, x + source.H*scale, y}
	}
	clipped := w*scale > cell.W+1e-6 || h*scale > cell.H+1e-6
	return PrintPlacement{Page: index, Width: source.W, Height: source.H, Transform: m, Clip: cell, Clipped: clipped}
}

// PreparePrint 固定当前阅读入口、版本及配置，底层输入由调用方保持存活
// 入参: ctx 准备上下文, options 内容排版选项
// 返回: *PrintDocument 独立打印任务, error 权限、布局或读取错误
func (r *Renderer) PreparePrint(ctx context.Context, options PrintOptions) (*PrintDocument, error) {
	if ctx == nil {
		return nil, fmt.Errorf("nil print context")
	}
	if r == nil || r.Reader == nil {
		return nil, fmt.Errorf("missing print renderer")
	}
	o, err := normalizePrintOptions(options)
	if err != nil {
		return nil, err
	}
	view, err := r.Fork(ctx)
	if err != nil {
		return nil, err
	}
	doc, err := view.Reader.DocContext(ctx)
	if err != nil {
		return nil, err
	}
	if !doc.Permissions.Print || doc.Permissions.Copies == 0 {
		return nil, ErrPrintDenied
	}
	if doc.Permissions.Copies > 0 && o.Copies > doc.Permissions.Copies {
		return nil, ErrPrintDenied
	}
	boxes := make([]Box, len(doc.Pages.Page))
	for i, page := range doc.Pages.Page {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		area, err := view.Reader.readPageHeaderContext(ctx, page)
		if err != nil {
			return nil, err
		}
		boxes[i], err = view.GetPageBox(&area)
		if err != nil {
			return nil, err
		}
	}
	layout, err := planPrint(boxes, o)
	if err != nil {
		return nil, err
	}
	layout.CopiesLimit = doc.Permissions.Copies
	view.printing = true
	view.printContents = o.Contents
	view.RenderAnnotations = true
	return &PrintDocument{renderer: view, layout: layout, options: o, pages: resourceCache[int, []byte](view.Reader.resources, 32<<20)}, nil
}

// PreparePrint 从独立编辑快照创建打印任务，不修改源编辑器或撤销记录
// 入参: ctx 准备上下文, options 排版选项, rendererOptions 字体及后端配置
// 返回: *PrintDocument 打印任务, error 快照或准备错误
func (e *Editor) PreparePrint(ctx context.Context, options PrintOptions, rendererOptions ...RendererOption) (*PrintDocument, error) {
	if ctx == nil || e == nil {
		return nil, fmt.Errorf("missing print editor or context")
	}
	snapshot, err := e.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	reader, err := snapshot.Reader(ctx)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	renderer := NewRenderer(reader, rendererOptions...)
	return renderer.PreparePrint(ctx, options)
}

// Layout 返回可供第三方预览和检查的独立布局副本
// 返回: PrintLayout 独立布局副本
func (p *PrintDocument) Layout() PrintLayout {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := p.layout
	result.Sides = slices.Clone(result.Sides)
	for i := range result.Sides {
		result.Sides[i].Placements = slices.Clone(result.Sides[i].Placements)
	}
	return result
}

// Reconfigure 在相同文档快照上创建新的打印任务，不改变原任务的布局
// 入参: ctx 准备上下文, options 新排版选项
// 返回: *PrintDocument 独立打印任务, error 权限、布局或读取错误
func (p *PrintDocument) Reconfigure(ctx context.Context, options PrintOptions) (*PrintDocument, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, fmt.Errorf("print document is closed")
	}
	return p.renderer.PreparePrint(ctx, options)
}

// Close 释放任务持有的派生缓存和快照，不关闭调用方借入的输入
// 返回: error 资源释放错误
func (p *PrintDocument) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	p.pages.clear()
	p.renderer.ClearCache()
	err := p.renderer.Reader.Close()
	p.renderer = nil
	return err
}
