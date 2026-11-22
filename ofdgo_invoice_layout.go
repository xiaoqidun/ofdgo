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
	"maps"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// InvoiceLayout 指定发票信息页的字体资源及毫米尺寸，不代表税务机关的固定票样
// Font必填；其余零值依次使用4毫米字号、210毫米宽、297毫米高、15毫米页边距
type InvoiceLayout struct {
	Font       string  `json:"font"`
	FontSize   float64 `json:"fontSize"`
	PageWidth  float64 `json:"pageWidth"`
	PageHeight float64 `json:"pageHeight"`
	Margin     float64 `json:"margin"`
}

// InvoiceBinding 将一个完整字段绑定至当前文档的文字对象，页码及明细索引从0开始
// Field使用number、buyer.name、items.name等规范字段名，非明细字段的Item必须为-1
// 原始事实使用facts.N或items.fields.N，N为原始事实索引，不根据名称合并不同上下文
type InvoiceBinding struct {
	Field    string `json:"field"`
	Item     int    `json:"item"`
	Page     int    `json:"page"`
	ObjectID string `json:"objectId"`
}

// invoiceLayoutField 保存票面字段及独立标签
type invoiceLayoutField struct {
	field, label, value string
	item                int
}

// invoiceAppearanceMatches 比较票面Unicode字符序列，仅忽略回车与换行，保留全部空格
// 不校验显式换行、软折行、位置或可见性，字形索引文本无法确定原文时返回false
// 入参: text 票面文字对象, value 结构化字段原文
// 返回: bool 忽略回车与换行后的字符序列是否一致
func invoiceAppearanceMatches(text TextObject, value string) bool {
	var content strings.Builder
	for _, code := range text.TextCode {
		if code.Index != "" {
			return false
		}
		content.WriteString(string(textCodeRunes(code.Value)))
	}
	actual := strings.ReplaceAll(strings.ReplaceAll(content.String(), "\r", ""), "\n", "")
	expected := strings.ReplaceAll(strings.ReplaceAll(value, "\r", ""), "\n", "")
	return actual == expected
}

// invoiceLayoutFields 按稳定顺序展开公共字段及明细
// 入参: invoice 发票数据
// 返回: []invoiceLayoutField 字段列表
func invoiceLayoutFields(invoice Invoice) []invoiceLayoutField {
	fields := []invoiceLayoutField{
		{"kind", "票种", invoice.Kind, -1}, {"standard", "规范", invoice.Standard, -1},
		{"version", "版本", invoice.Version, -1}, {"number", "发票号码", invoice.Number, -1},
		{"code", "发票代码", invoice.Code, -1}, {"date", "开票日期", invoice.Date, -1},
		{"currency", "币种", invoice.Currency, -1}, {"status", "状态", invoice.Status, -1},
	}
	for _, party := range []struct {
		prefix, label string
		value         InvoiceParty
	}{{"buyer", "购买方", invoice.Buyer}, {"seller", "销售方", invoice.Seller}} {
		for _, field := range []invoiceLayoutField{{"name", "名称", party.value.Name, -1}, {"taxId", "税号", party.value.TaxID, -1}, {"address", "地址", party.value.Address, -1}, {"phone", "电话", party.value.Phone, -1}, {"bank", "开户行", party.value.Bank, -1}, {"account", "账号", party.value.Account, -1}} {
			field.field, field.label = party.prefix+"."+field.field, party.label+field.label
			fields = append(fields, field)
		}
	}
	for i, item := range invoice.Items {
		for _, field := range []invoiceLayoutField{{"name", "名称", item.Name, i}, {"specification", "规格", item.Specification, i}, {"unit", "单位", item.Unit, i}, {"quantity", "数量", item.Quantity, i}, {"unitPrice", "单价", item.UnitPrice, i}, {"amount", "金额", item.Amount, i}, {"taxRate", "税率", item.TaxRate, i}, {"taxAmount", "税额", item.TaxAmount, i}} {
			field.field, field.label = "items."+field.field, fmt.Sprintf("明细%d %s", i+1, field.label)
			fields = append(fields, field)
		}
	}
	fields = append(fields, invoiceLayoutField{"amount", "不含税金额", invoice.Amount, -1}, invoiceLayoutField{"taxAmount", "税额", invoice.TaxAmount, -1}, invoiceLayoutField{"totalAmount", "价税合计", invoice.TotalAmount, -1}, invoiceLayoutField{"remarks", "备注", invoice.Remarks, -1})
	for i, fact := range invoice.Facts {
		if invoiceLayoutFactVisible(invoice, fact) {
			fields = append(fields, invoiceLayoutField{fmt.Sprintf("facts.%d", i), fact.Name, fact.Value, -1})
		}
	}
	for i, item := range invoice.Items {
		for j, fact := range item.Fields {
			duplicate := false
			for _, existing := range invoice.Facts {
				if reflect.DeepEqual(existing, fact) {
					duplicate = true
					break
				}
			}
			if !duplicate && invoiceLayoutFactVisible(invoice, fact) {
				fields = append(fields, invoiceLayoutField{fmt.Sprintf("items.fields.%d", j), fmt.Sprintf("明细%d %s", i+1, fact.Name), fact.Value, i})
			}
		}
	}
	return fields
}

// invoiceLayoutFactVisible 排除上下文、单位及已明确映射的相同字段值
// 入参: invoice 发票数据, fact 原始业务事实
// 返回: bool 是否需要单独显示
func invoiceLayoutFactVisible(invoice Invoice, fact InvoiceFact) bool {
	if fact.Nil || fact.Value == "" || fact.Namespace == invoiceXBRL && (fact.Name == "context" || fact.Name == "unit") {
		return false
	}
	if binding, ok := invoiceEDRMFactBinding(invoice, fact); ok {
		value, err := invoiceBindingValue(invoice, binding)
		raw := fact.Value
		if binding.Field == "date" {
			if date, err := time.Parse("2006年01月02日", raw); err == nil {
				raw = date.Format("2006-01-02")
			}
		}
		if err == nil && value == raw {
			return false
		}
	}
	kind, _ := invoiceTaxonomy(fact.Namespace)
	if kind != "" && kind == invoice.Kind {
		if field := invoiceXMLField(&invoice, kind, fact.Name); field != nil && *field == fact.Value {
			return false
		}
		for _, item := range invoice.Items {
			if field := invoiceItemField(&item, kind, fact.Name); field != nil && *field == fact.Value {
				return false
			}
		}
	}
	if fact.Namespace == "" {
		var path strings.Builder
		valid := true
		for _, segment := range strings.Split(strings.TrimPrefix(fact.Source.NodePath, "/"), "/") {
			name, index, ok := strings.Cut(segment, "[")
			if !ok || !strings.HasPrefix(name, "{}") || !strings.HasSuffix(index, "]") {
				valid = false
				break
			}
			path.WriteByte('/')
			path.WriteString(strings.TrimPrefix(name, "{}"))
		}
		if valid {
			if path.String() == "/EInvoice/Header/Version" && fact.Value == invoice.Version {
				return false
			}
			if field, _ := invoicePlainField(&invoice, nil, path.String()); field != nil && *field == fact.Value {
				return false
			}
			for _, item := range invoice.Items {
				if field, _ := invoicePlainField(&invoice, &item, path.String()); field != nil && *field == fact.Value {
					return false
				}
			}
		}
	}
	return true
}

// invoiceEDRMFactBinding 按2019版完整路径识别摘要与原始字段的同一语义
// 入参: value 发票，fact 原始字段
// 返回: 公共字段关联及是否明确映射
func invoiceEDRMFactBinding(value Invoice, fact InvoiceFact) (InvoiceBinding, bool) {
	if value.Standard != InvoiceStandardEDRM2019 && value.Standard != ofdNamespace2016 || fact.Namespace != value.Standard {
		return InvoiceBinding{}, false
	}
	var path strings.Builder
	nodePath := strings.ReplaceAll(fact.Source.NodePath, "{"+InvoiceStandardEDRM2019+"}", "{}")
	if value.Standard == ofdNamespace2016 {
		nodePath = strings.Replace(nodePath, "/root[1]", "/eInvoice[1]", 1)
		nodePath = strings.ReplaceAll(nodePath, "/", "/{}")
	}
	for _, segment := range strings.Split(strings.TrimPrefix(nodePath, "/"), "/") {
		name, index, ok := strings.Cut(segment, "[")
		if !ok || !strings.HasPrefix(name, "{}") || !strings.HasSuffix(index, "]") {
			return InvoiceBinding{}, false
		}
		path.WriteByte('/')
		path.WriteString(strings.TrimPrefix(name, "{}"))
	}
	probe := Invoice{Items: []InvoiceItem{{}}}
	field, _ := invoiceEDRMField(&probe, &probe.Items[0], path.String())
	if field == nil {
		return InvoiceBinding{}, false
	}
	*field = "mapped"
	for _, candidate := range invoiceLayoutFields(probe) {
		if candidate.value != "mapped" {
			continue
		}
		if candidate.item < 0 {
			return InvoiceBinding{Field: candidate.field, Item: -1}, true
		}
		for i, item := range value.Items {
			for _, existing := range item.Fields {
				if existing.Source.NodePath == fact.Source.NodePath {
					return InvoiceBinding{Field: candidate.field, Item: i}, true
				}
			}
		}
	}
	return InvoiceBinding{}, false
}

// invoiceBindingValue 读取明确字段与明细索引，不接受未知字段或隐式索引
// 入参: invoice 发票数据, binding 字段绑定
// 返回: string 字段原文, error 字段或索引错误
func invoiceBindingValue(invoice Invoice, binding InvoiceBinding) (string, error) {
	if strings.HasPrefix(binding.Field, "facts.") && binding.Item == -1 {
		index, err := strconv.Atoi(strings.TrimPrefix(binding.Field, "facts."))
		if err == nil && index >= 0 && index < len(invoice.Facts) && binding.Field == fmt.Sprintf("facts.%d", index) {
			return invoice.Facts[index].Value, nil
		}
	}
	if strings.HasPrefix(binding.Field, "items.") {
		if binding.Item >= 0 && binding.Item < len(invoice.Items) {
			item := invoice.Items[binding.Item]
			if strings.HasPrefix(binding.Field, "items.fields.") {
				index, err := strconv.Atoi(strings.TrimPrefix(binding.Field, "items.fields."))
				if err == nil && index >= 0 && index < len(item.Fields) && binding.Field == fmt.Sprintf("items.fields.%d", index) {
					return item.Fields[index].Value, nil
				}
			}
			switch binding.Field {
			case "items.name":
				return item.Name, nil
			case "items.specification":
				return item.Specification, nil
			case "items.unit":
				return item.Unit, nil
			case "items.quantity":
				return item.Quantity, nil
			case "items.unitPrice":
				return item.UnitPrice, nil
			case "items.amount":
				return item.Amount, nil
			case "items.taxRate":
				return item.TaxRate, nil
			case "items.taxAmount":
				return item.TaxAmount, nil
			}
		}
	} else if binding.Item == -1 {
		switch binding.Field {
		case "kind":
			return invoice.Kind, nil
		case "standard":
			return invoice.Standard, nil
		case "version":
			return invoice.Version, nil
		case "number":
			return invoice.Number, nil
		case "code":
			return invoice.Code, nil
		case "date":
			return invoice.Date, nil
		case "currency":
			return invoice.Currency, nil
		case "status":
			return invoice.Status, nil
		case "amount":
			return invoice.Amount, nil
		case "taxAmount":
			return invoice.TaxAmount, nil
		case "totalAmount":
			return invoice.TotalAmount, nil
		case "remarks":
			return invoice.Remarks, nil
		}
		prefix, name, ok := strings.Cut(binding.Field, ".")
		if ok && (prefix == "buyer" || prefix == "seller") {
			party := invoice.Buyer
			if prefix == "seller" {
				party = invoice.Seller
			}
			switch name {
			case "name":
				return party.Name, nil
			case "taxId":
				return party.TaxID, nil
			case "address":
				return party.Address, nil
			case "phone":
				return party.Phone, nil
			case "bank":
				return party.Bank, nil
			case "account":
				return party.Account, nil
			}
		}
	}
	return "", fmt.Errorf("invalid invoice binding field %q item %d", binding.Field, binding.Item)
}

// invoiceLayoutText 按实际字形及步进检查折行容量，并将负侧承移入原框
// 入参: text 普通横向文字, value 字段原文, metrics 字体度量
// 返回: float64 内容高度, error 缺字或越界错误
func invoiceLayoutText(text *TextObject, value string, metrics FontMetrics) (float64, error) {
	box, err := text.TextFrame()
	if err != nil {
		return 0, err
	}
	if value == "" {
		return 0, fmt.Errorf("empty invoice field requires removing its binding and text object")
	}
	if strings.Trim(value, "\r\n") == "" {
		return 0, fmt.Errorf("invoice text contains no display characters")
	}
	if err := LayoutText(text, value, TextLayout{Wrap: true}, metrics); err != nil {
		return 0, err
	}
	outlines, ok := metrics.(FontOutlines)
	if !ok {
		return 0, fmt.Errorf("invoice layout requires glyph outlines")
	}
	unit := text.Size / float64(metrics.UnitsPerEm())
	_, descender, _ := metrics.VerticalMetrics()
	left, top, right, bottom := 0.0, 0.0, 0.0, 0.0
	for _, code := range text.TextCode {
		x, err := strconv.ParseFloat(code.X, 64)
		if err != nil {
			return 0, err
		}
		y, err := strconv.ParseFloat(code.Y, 64)
		if err != nil {
			return 0, err
		}
		bottom = math.Max(bottom, y+float64(descender)*unit)
		for _, char := range textCodeRunes(code.Value) {
			glyph := metrics.GlyphIndex(char)
			if glyph == 0 {
				return 0, fmt.Errorf("invoice font has no glyph for %U", char)
			}
			var bounds Box
			if provider, ok := metrics.(FontGlyphBounds); ok {
				bounds, err = provider.GlyphBounds(glyph, text.Size)
			} else {
				var outline GeometryPath
				outline, err = outlines.GlyphOutline(glyph, text.Size)
				if err == nil {
					bounds, err = outline.Bounds()
				}
			}
			if err != nil {
				return 0, err
			}
			left, top = math.Min(left, x+bounds.X), math.Min(top, y+bounds.Y)
			right, bottom = math.Max(right, x+bounds.X+bounds.W), math.Max(bottom, y+bounds.Y+bounds.H)
			x += float64(metrics.GlyphAdvance(glyph)) * unit
			right = math.Max(right, x)
		}
	}
	if right-left > box.W+1e-6 || bottom-top > box.H+1e-6 {
		return 0, fmt.Errorf("invoice text exceeds binding frame")
	}
	for i := range text.TextCode {
		x, _ := strconv.ParseFloat(text.TextCode[i].X, 64)
		y, _ := strconv.ParseFloat(text.TextCode[i].Y, 64)
		text.TextCode[i].X, text.TextCode[i].Y = ofdNumber(x-left), ofdNumber(y-top)
	}
	return math.Ceil((bottom-top)*1e6) / 1e6, nil
}

// invoiceSlotText 用字体步进及轮廓填充单一原基线槽，保留可搜索文字和显式字形映射
// 入参: ctx 取消上下文, text 目标对象, value 剩余文字, offset 原文偏移, boundaries 字素边界, x和y 原点, right 右界, box 原框, visible 原可见范围, obstacles 外部障碍, metrics 字体, mapping 原映射
// 返回: int 已容纳码点数, error 可递延缺字, error 度量错误
func invoiceSlotText(ctx context.Context, text *TextObject, value []rune, offset int, boundaries map[int]bool, x, y, right float64, box, visible Box, obstacles []Box, metrics FontMetrics, mapping map[rune]uint16) (int, error, error) {
	if metrics.UnitsPerEm() == 0 {
		return 0, nil, fmt.Errorf("invalid invoice font units")
	}
	unit, cursor := text.Size/float64(metrics.UnitsPerEm()), x
	scale := text.HScale
	if scale == 0 {
		scale = 1
	}
	count := 0
	var missing error
	var advances, glyphs []string
	for i, char := range value {
		if err := ctx.Err(); err != nil {
			return 0, nil, err
		}
		glyph := mapping[char]
		if glyph == 0 {
			glyph = metrics.GlyphIndex(char)
		}
		if glyph == 0 {
			missing = missingGlyphError(text.Font, []rune{char})
			break
		}
		var bounds Box
		var err error
		if provider, ok := metrics.(FontGlyphBounds); ok {
			bounds, err = provider.GlyphBounds(glyph, text.Size)
		} else if outlines, ok := metrics.(FontOutlines); ok {
			var outline GeometryPath
			outline, err = outlines.GlyphOutline(glyph, text.Size)
			if err == nil {
				bounds, err = outline.Bounds()
			}
		} else {
			return 0, nil, fmt.Errorf("invoice slot requires glyph outline metrics")
		}
		if err != nil {
			return 0, nil, err
		}
		advance := float64(metrics.GlyphAdvance(glyph)) * unit * scale
		bounds.X, bounds.W = bounds.X*scale, bounds.W*scale
		if !finite(advance) || advance < 0 || !finite(bounds.X) || !finite(bounds.Y) || !finite(bounds.W) || !finite(bounds.H) {
			return 0, nil, fmt.Errorf("invalid invoice glyph geometry")
		}
		if cursor+advance > right+1e-6 || cursor+bounds.X < -1e-6 || cursor+bounds.X+bounds.W > right+1e-6 || y+bounds.Y < -1e-6 || y+bounds.Y+bounds.H > box.H+1e-6 {
			break
		}
		blocked := false
		if bounds.W > 0 && bounds.H > 0 {
			for _, obstacle := range obstacles {
				left := math.Max(box.X+cursor+bounds.X, obstacle.X)
				top := math.Max(box.Y+y+bounds.Y, obstacle.Y)
				right := math.Min(box.X+cursor+bounds.X+bounds.W, obstacle.X+obstacle.W)
				bottom := math.Min(box.Y+y+bounds.Y+bounds.H, obstacle.Y+obstacle.H)
				if right > left+1e-6 && bottom > top+1e-6 && (visible.W <= 0 || visible.H <= 0 || left < visible.X-1e-6 || top < visible.Y-1e-6 || right > visible.X+visible.W+1e-6 || bottom > visible.Y+visible.H+1e-6) {
					blocked = true
					break
				}
			}
		}
		if blocked {
			break
		}
		advances = append(advances, ofdNumber(advance))
		glyphs = append(glyphs, strconv.Itoa(int(glyph)))
		cursor += advance
		if boundaries[offset+i+1] {
			count = i + 1
		}
	}
	text.TextCode = []TextCode{{X: text.TextCode[0].X, Y: text.TextCode[0].Y, Value: escapeOFDText(string(value[:count]))}}
	text.CGTransform, text.layout = nil, nil
	if count > 0 {
		text.TextCode[0].DeltaX = strings.Join(advances[:count-1], " ")
		text.CGTransform = []CGTransform{{CodePosition: 0, CodeCount: count, GlyphCount: count, Glyphs: strings.Join(glyphs[:count], " ")}}
	}
	return count, missing, nil
}

// invoiceCheckReflowReferences 检查当前文档快照中指向待重排页的标准导航及已识别标引
// 排除本次发票自身的标引，不推测未知扩展中的引用，也不读取其他文档或历史入口
// 入参: ctx 取消上下文, e 当前编辑器, ownedPageIDs 待替换页面标识, ownStructureIndex 自有标引索引，-1表示无
// 返回: error 引用冲突、结构读取或取消错误
func invoiceCheckReflowReferences(ctx context.Context, e *Editor, ownedPageIDs []string, ownStructureIndex int) error {
	if ctx == nil || e == nil {
		return fmt.Errorf("editor and context are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	owned := make(map[string]bool, len(ownedPageIDs))
	for _, id := range ownedPageIDs {
		key := editorResourceID(id)
		if key == "" {
			return fmt.Errorf("invalid generated invoice page identifier")
		}
		owned[key] = true
	}
	reader, err := e.Reader(ctx)
	if err != nil {
		return err
	}
	defer reader.Close()
	structures, err := reader.StructureContext(ctx)
	if err != nil {
		return err
	}
	if ownStructureIndex < -1 || ownStructureIndex >= len(structures) {
		return fmt.Errorf("invoice structure index out of range")
	}
	var checkStructure func(*StructureNode) error
	checkStructure = func(node *StructureNode) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if node == nil {
			return nil
		}
		for _, reference := range node.References {
			if owned[editorResourceID(reference.PageID)] {
				return fmt.Errorf("generated invoice page is referenced by another structure")
			}
		}
		for i := range node.Children {
			if err := checkStructure(&node.Children[i]); err != nil {
				return err
			}
		}
		return nil
	}
	for index, structure := range structures {
		if index == ownStructureIndex {
			continue
		}
		if structure.Error != "" {
			return fmt.Errorf("cannot inspect invoice reflow references: %s", structure.Error)
		}
		if err := checkStructure(structure.Root); err != nil {
			return err
		}
	}
	_, files, err := reader.versionDependencies(ctx, reader.documentRoot, nil, nil)
	if err != nil {
		return err
	}
	var walk func(*editorXML) error
	walk = func(node *editorXML) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !node.matchesOFD(node.name.Local) {
			return nil
		}
		if node.matchesOFD("Data") && node.parent != nil && node.parent.matchesOFD("Extension") {
			return nil
		}
		if node.matchesOFD("Dest") && node.parent != nil && (node.parent.matchesOFD("Goto") || node.parent.matchesOFD("Bookmark") || node.parent.matchesOFD("OutlineElem")) && owned[editorResourceID(node.attr("PageID"))] {
			return fmt.Errorf("generated invoice page is referenced by navigation")
		}
		if node.matchesOFD("Extension") && owned[editorResourceID(node.attr("RefId"))] {
			return fmt.Errorf("generated invoice page is referenced by an extension")
		}
		for _, child := range node.children {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	for _, file := range slices.Sorted(maps.Keys(files)) {
		data, err := reader.readFileViewContext(ctx, file)
		if err != nil {
			return err
		}
		root, err := parseEditorXMLContext(ctx, data)
		if err != nil {
			return err
		}
		if err := walk(root); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// AppendInvoicePages 追加可搜索的发票信息页，字段内折行、字段间分页，不修改已有页面
// 每字段对应一个完整文字对象，单字段超过页面容量时失败；绑定持久化由调用方处理
// 入参: ctx 取消上下文, invoice 发票数据, options 字体与页面尺寸
// 返回: []InvoiceBinding 新增字段绑定, error 排版或字体错误
func (e *Editor) AppendInvoicePages(ctx context.Context, invoice Invoice, options InvoiceLayout) ([]InvoiceBinding, error) {
	if e == nil || ctx == nil {
		return nil, fmt.Errorf("editor and context are required")
	}
	if options.Font == "" {
		return nil, fmt.Errorf("invoice font resource is required")
	}
	if options.FontSize == 0 {
		options.FontSize = 4
	}
	if options.PageWidth == 0 {
		options.PageWidth = 210
	}
	if options.PageHeight == 0 {
		options.PageHeight = 297
	}
	if options.Margin == 0 {
		options.Margin = 15
	}
	for _, value := range []float64{options.FontSize, options.PageWidth, options.PageHeight, options.Margin} {
		if !finite(value) || value <= 0 {
			return nil, fmt.Errorf("invoice layout dimensions must be positive and finite")
		}
	}
	width, height := options.PageWidth-2*options.Margin, options.PageHeight-2*options.Margin
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("invoice margins leave no page area")
	}
	var bindings []InvoiceBinding
	err := e.Transaction(func(editor *Editor) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		metrics, err := editor.editorFont(options.Font)
		if err != nil {
			return err
		}
		page, y := -1, options.Margin
		for _, field := range invoiceLayoutFields(invoice) {
			if err := ctx.Err(); err != nil {
				return err
			}
			if field.value == "" {
				continue
			}
			labelWidth := math.Min(width*.4, options.FontSize*8)
			gap := options.FontSize
			label := TextObject{Font: options.Font, Size: options.FontSize, Boundary: fmt.Sprintf("0 0 %s %s", ofdNumber(labelWidth), ofdNumber(height))}
			value := TextObject{Font: options.Font, Size: options.FontSize, Boundary: fmt.Sprintf("0 0 %s %s", ofdNumber(width-labelWidth-gap), ofdNumber(height))}
			labelHeight, err := invoiceLayoutText(&label, field.label, metrics)
			if err != nil {
				return fmt.Errorf("invoice label %s: %w", field.field, err)
			}
			valueHeight, err := invoiceLayoutText(&value, field.value, metrics)
			if err != nil {
				return fmt.Errorf("invoice field %s: %w", field.field, err)
			}
			rowHeight := math.Max(labelHeight, valueHeight)
			if page < 0 || y+rowHeight > options.PageHeight-options.Margin {
				page, err = editor.AddPage(options.PageWidth, options.PageHeight)
				if err != nil {
					return err
				}
				y = options.Margin
			}
			label.Boundary = fmt.Sprintf("%s %s %s %s", ofdNumber(options.Margin), ofdNumber(y), ofdNumber(labelWidth), ofdNumber(rowHeight))
			value.Boundary = fmt.Sprintf("%s %s %s %s", ofdNumber(options.Margin+labelWidth+gap), ofdNumber(y), ofdNumber(width-labelWidth-gap), ofdNumber(rowHeight))
			if _, err := editor.AddObject(page, GraphicObject{Type: "TextObject", TextObject: label}); err != nil {
				return err
			}
			id, err := editor.AddObject(page, GraphicObject{Type: "TextObject", TextObject: value})
			if err != nil {
				return err
			}
			bindings = append(bindings, InvoiceBinding{Field: field.field, Item: field.item, Page: page, ObjectID: id})
			y += rowHeight + options.FontSize*.5
		}
		if len(bindings) == 0 {
			return fmt.Errorf("invoice has no display fields")
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	return bindings, nil
}

// SyncInvoicePages 原子更新显式绑定的普通横向文字，不新增、移动或缩小对象
// 不接受重复绑定、空文本、缺失对象及超出原框的内容；可嵌入上层事务与结构化数据一起提交
// 入参: ctx 取消上下文, invoice 发票数据, bindings 当前文档字段绑定
// 返回: error 绑定、字体或容量错误
func (e *Editor) SyncInvoicePages(ctx context.Context, invoice Invoice, bindings []InvoiceBinding) error {
	if e == nil || ctx == nil {
		return fmt.Errorf("editor and context are required")
	}
	return e.Transaction(func(editor *Editor) error {
		objects, fields := make(map[string]bool), make(map[string]bool)
		for _, binding := range bindings {
			if err := ctx.Err(); err != nil {
				return err
			}
			objectKey := fmt.Sprintf("%d/%s", binding.Page, editorResourceID(binding.ObjectID))
			fieldKey := fmt.Sprintf("%d/%s", binding.Item, binding.Field)
			if binding.ObjectID == "" || objects[objectKey] || fields[fieldKey] {
				return fmt.Errorf("ambiguous invoice binding %q", binding.Field)
			}
			objects[objectKey], fields[fieldKey] = true, true
			value, err := invoiceBindingValue(invoice, binding)
			if err != nil {
				return err
			}
			page, err := editor.page(binding.Page)
			if err != nil {
				return err
			}
			matches := 0
			for _, layer := range page.Content.Layer {
				for _, candidate := range layer.Objects {
					if sameResourceID(editorObjectID(candidate), binding.ObjectID) {
						matches++
					}
				}
			}
			if matches != 1 {
				return fmt.Errorf("invoice binding %q requires one text object", binding.Field)
			}
			object, err := editor.Object(binding.Page, binding.ObjectID)
			if err != nil {
				return err
			}
			if object.Type != "TextObject" {
				return fmt.Errorf("invoice binding %q is not text", binding.Field)
			}
			text := &object.TextObject
			if text.CTM != "" || text.Clips != nil || text.Italic || text.Weight > 400 || text.DrawParam != "" || text.Stroke != nil && *text.Stroke || text.HScale != 0 && text.HScale != 1 {
				return fmt.Errorf("invoice binding %q requires plain untransformed text", binding.Field)
			}
			metrics, err := editor.editorFont(text.Font)
			if err != nil {
				return err
			}
			if _, err := invoiceLayoutText(text, value, metrics); err != nil {
				return fmt.Errorf("invoice field %s: %w", binding.Field, err)
			}
			if err := editor.UpdateObject(binding.Page, binding.ObjectID, object); err != nil {
				return err
			}
		}
		return ctx.Err()
	})
}

// layoutInvoiceReferenceSlots 按原图元框和基线分配变长字段，空TextCode保留原ID和原框，不借用跨列空间或改变引用
// 入参: ctx 取消上下文, bindings 有序图元槽位, before 原文字, value 新文字, font 字体覆盖, fontData 字体数据
// 返回: error 引用、字体、布局或容量错误
func (e *Editor) layoutInvoiceReferenceSlots(ctx context.Context, bindings []InvoiceBinding, before, value, font string, fontData []byte) error {
	if len(bindings) == 0 || !utf8.ValidString(value) || !invoiceWriteText(value) || strings.ContainsAny(value, "\r\n\t") {
		return fmt.Errorf("invoice slot layout requires valid single-flow text and existing references")
	}
	type slot struct {
		binding InvoiceBinding
		object  GraphicObject
		box     Box
		x, y    float64
		right   float64
		visible Box
	}
	slots := make([]slot, len(bindings))
	mappings := make(map[string]map[rune]uint16)
	var original strings.Builder
	for i, binding := range bindings {
		if err := ctx.Err(); err != nil {
			return err
		}
		if binding.Page != bindings[0].Page {
			return fmt.Errorf("invoice slot layout cannot infer flow across pages")
		}
		page, err := e.page(binding.Page)
		if err != nil {
			return err
		}
		matches := 0
		for _, layer := range page.Content.Layer {
			for _, object := range layer.Objects {
				if sameResourceID(editorObjectID(object), binding.ObjectID) {
					matches++
				}
			}
		}
		object, err := e.Object(binding.Page, binding.ObjectID)
		if err != nil || matches != 1 || object.Type != "TextObject" {
			return fmt.Errorf("invoice slot requires one direct text object")
		}
		text := &object.TextObject
		if text.CTM != "" || text.Clips != nil || text.ReadDirection != 0 || text.CharDirection != 0 || text.Italic || text.Weight > 400 || text.DrawParam != "" || text.Stroke != nil && *text.Stroke || !finite(text.HScale) || text.HScale < 0 || len(text.TextCode) == 0 {
			return fmt.Errorf("invoice slot requires plain horizontal text without transforms")
		}
		box, err := creationBox(text.Boundary)
		if err != nil {
			return err
		}
		x, err := strconv.ParseFloat(text.TextCode[0].X, 64)
		if err != nil || !finite(x) || x < 0 || x > box.W {
			return fmt.Errorf("invoice slot has an invalid horizontal origin")
		}
		y, err := strconv.ParseFloat(text.TextCode[0].Y, 64)
		if err != nil || !finite(y) || y < 0 || y > box.H || !finite(text.Size) || text.Size <= 0 {
			return fmt.Errorf("invoice slot has an invalid baseline or size")
		}
		for _, code := range text.TextCode {
			if code.Index != "" || code.DeltaY != "" || code.Y != "" && code.Y != text.TextCode[0].Y {
				return fmt.Errorf("invoice slot has unsupported glyph indexes or multiple baselines")
			}
		}
		old := text.Text()
		if strings.ContainsAny(old, "\r\n\t") {
			return fmt.Errorf("invoice slot contains multiple text lines")
		}
		original.WriteString(old)
		if font == "" && len(fontData) == 0 {
			if err := validateTextGlyphs(*text, 1<<16); err != nil {
				return err
			}
			if mappings[text.Font] == nil {
				mappings[text.Font] = make(map[rune]uint16)
			}
			runes := []rune(old)
			for _, transform := range text.CGTransform {
				glyphs := strings.Fields(transform.Glyphs)
				if transform.CodeCount != transform.GlyphCount || len(glyphs) != transform.CodeCount {
					return fmt.Errorf("invoice combined glyph layout requires explicit Font or FontData")
				}
				for j, encoded := range glyphs {
					glyph, err := strconv.ParseUint(encoded, 10, 16)
					at := transform.CodePosition + j
					if err != nil || glyph == 0 || at < 0 || at >= len(runes) {
						return fmt.Errorf("invalid invoice glyph mapping")
					}
					if previous, exists := mappings[text.Font][runes[at]]; exists && previous != uint16(glyph) {
						return fmt.Errorf("ambiguous invoice glyph mapping requires explicit Font or FontData")
					}
					mappings[text.Font][runes[at]] = uint16(glyph)
				}
			}
		}
		slots[i] = slot{binding: binding, object: object, box: box, x: x, y: y, right: box.W}
		if i == 0 {
			continue
		}
		previous := &slots[i-1]
		step := math.Max(previous.object.TextObject.Size, text.Size)
		if math.Abs(box.Y+y-previous.box.Y-previous.y) <= step*.1 {
			start := box.X + x
			if start <= previous.box.X+previous.x || start-(previous.box.X+previous.box.W) > step {
				return fmt.Errorf("invoice references cross columns or reverse reading order")
			}
			previous.right = math.Min(previous.right, start-previous.box.X)
		} else if math.Abs(box.X+x-slots[0].box.X-slots[0].x) > step*.25 || box.Y < previous.box.Y+previous.box.H-1e-6 || box.Y-(previous.box.Y+previous.box.H) > step*.5 {
			return fmt.Errorf("invoice references do not form adjacent aligned text rows")
		}
	}
	if original.String() != before {
		return fmt.Errorf("invoice slot text changed")
	}
	reader, err := e.Reader(ctx)
	if err != nil {
		return err
	}
	defer reader.Close()
	renderer := e.newRenderer(reader)
	selected := make(map[string]int, len(slots))
	for i, current := range slots {
		selected[editorResourceID(current.binding.ObjectID)] = i
	}
	var obstacles []Box
	page, err := e.page(bindings[0].Page)
	if err != nil {
		return err
	}
	for _, layer := range page.Content.Layer {
		for _, object := range layer.Objects {
			if err := ctx.Err(); err != nil {
				return err
			}
			if object.Type != "TextObject" && object.Type != "ImageObject" && object.Type != "CompositeObject" && object.Type != "CompositeGraphicUnit" {
				continue
			}
			box, err := renderer.ObjectBounds(object, layer.DrawParam)
			if err != nil {
				return fmt.Errorf("cannot verify invoice slot neighbours: %w", err)
			}
			if index, ok := selected[editorResourceID(editorObjectID(object))]; ok {
				slots[index].visible = box
			} else if box.W > 0 && box.H > 0 {
				obstacles = append(obstacles, box)
			}
		}
	}
	if len(fontData) != 0 {
		var err error
		font, err = e.AddFont(FontFile{Data: fontData}, 0)
		if err != nil {
			return err
		}
	}
	runes, offset := []rune(value), 0
	boundaries := richTextBoundaries(value)
	var missing error
	for i := range slots {
		if err := ctx.Err(); err != nil {
			return err
		}
		current := &slots[i]
		text := &current.object.TextObject
		if font != "" {
			text.Font = font
		}
		metrics, err := e.editorFont(text.Font)
		if err != nil {
			return err
		}
		end := len(runes)
		if i == 0 && (text.Text() == "¥" || text.Text() == "￥") && offset < end && string(runes[offset]) == text.Text() {
			end = offset + 1
		}
		count, glyphErr, err := invoiceSlotText(ctx, text, runes[offset:end], offset, boundaries, current.x, current.y, current.right, current.box, current.visible, obstacles, metrics, mappings[text.Font])
		if err != nil {
			return err
		}
		if count > 0 {
			missing = nil
		}
		if glyphErr != nil {
			missing = glyphErr
		}
		offset += count
	}
	if offset != len(runes) {
		if missing != nil {
			return fmt.Errorf("invoice slot requires Font or FontData with the missing characters: %w", missing)
		}
		return fmt.Errorf("invoice text exceeds original reference slot capacity")
	}
	layers := copyEditorPage(e.pages[bindings[0].Page]).Content.Layer
	for _, current := range slots {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := validateEditorGeometry(current.object); err != nil {
			return err
		}
		if current.object.TextObject.Text() != "" {
			checked := cloneEditorData(current.object.TextObject)
			if err := e.prepareText(&checked); err != nil {
				return err
			}
		}
		for i := range layers {
			for j := range layers[i].Objects {
				if sameResourceID(editorObjectID(layers[i].Objects[j]), current.binding.ObjectID) {
					layers[i].Objects[j] = current.object
				}
			}
		}
	}
	e.replaceLayers(bindings[0].Page, layers)
	return ctx.Err()
}
