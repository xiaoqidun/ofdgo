const PX_PER_MM = 96 / 25.4;
export const SVG_NAMESPACE = "http://www.w3.org/2000/svg";

const FORMAT_KEYS = ["font", "size", "color", "bold", "italic", "underline", "strikeout", "shadow", "background"];
const RICH_CLIPBOARD = "application/x-ofdgo-rich-text";
const graphemes = new Intl.Segmenter(undefined, { granularity: "grapheme" });

function sourceTextScale(editing) {
	if (!editing.source) return 1;
	const [a, b, c, d] = editing.frame.matrix;
	return Math.max(Math.hypot(a, b), Math.hypot(c, d)) || 1;
}

function textFrameBox(item) {
	return { x: 0, y: 0, width: item.paragraphWidth ?? item.textFrame.width, height: item.textFrame.height };
}

export function fontFaceStyle(data) {
	const style = { weight: "400", style: "normal" };
	const bytes = ArrayBuffer.isView(data) ? new DataView(data.buffer, data.byteOffset, data.byteLength) : new DataView(data);
	if (bytes.byteLength < 12) return style;
	const count = bytes.getUint16(4);
	for (let i = 0; i < count && 12 + (i + 1) * 16 <= bytes.byteLength; i++) {
		const record = 12 + i * 16;
		if (bytes.getUint32(record) !== 0x68656164) continue;
		const offset = bytes.getUint32(record + 8), length = bytes.getUint32(record + 12);
		if (length < 46 || offset + length > bytes.byteLength) return style;
		const flags = bytes.getUint16(offset + 44);
		return { weight: flags & 1 ? "700" : "400", style: flags & 2 ? "italic" : "normal" };
	}
	return style;
}

function graphemeRange(text, start, end) {
	const boundaries = [0, ...Array.from(graphemes.segment(text), segment => segment.index + segment.segment.length)];
	const from = boundaries.findLast(offset => offset <= start) ?? 0;
	return start === end ? [from, from] : [from, boundaries.find(offset => offset >= end) ?? text.length];
}

export function richTextModel(item) {
	return { runs: [{ text: item.text || "", format: { font: item.font || "", size: item.size, color: item.color || "#000000",
		bold: Boolean(item.bold), italic: Boolean(item.italic), underline: Boolean(item.underline), strikeout: Boolean(item.strikeout),
		shadow: Boolean(item.shadow), background: item.background || "" } }],
		layout: { wrap: Boolean(item.wrap), align: item.align || "left", lineHeight: item.paragraphHeight || 0,
			letterSpacing: item.letterSpacing || 0, leftIndent: item.leftIndent || 0, rightIndent: item.rightIndent || 0,
			firstLineIndent: item.firstLineIndent || 0, autoBidi: true } };
}

function paragraphRange(text, start, end) {
	const first = text.slice(0, start).split("\n").length - 1;
	return [first, end > start ? text.slice(0, end - 1).split("\n").length - 1 : first];
}

export function richTextAlignment(model, start = 0, end = model.runs.reduce((n, run) => n + run.text.length, 0)) {
	const text = model.runs.map(run => run.text).join("");
	const [first, last] = paragraphRange(text, start, end);
	const align = model.paragraphs?.[first]?.align || model.layout.align || "left";
	for (let i = first + 1; i <= last; i++) if ((model.paragraphs?.[i]?.align || model.layout.align || "left") !== align) return "";
	return align;
}

export function richTextLayout(model, start = 0, end = model.runs.reduce((n, run) => n + run.text.length, 0)) {
	const [first, last] = paragraphRange(model.runs.map(run => run.text).join(""), start, end);
	const value = { ...model.layout, ...model.paragraphs?.[first] };
	for (const key of ["lineHeight", "letterSpacing", "leftIndent", "rightIndent", "firstLineIndent"]) {
		value[key] ??= 0;
		for (let i = first + 1; i <= last; i++) if ((model.paragraphs?.[i]?.[key] ?? model.layout[key] ?? 0) !== value[key]) value[key] = undefined;
	}
	value.align = richTextAlignment(model, start, end);
	return value;
}

function replaceParagraphs(model, text, start, end, inserted, paragraphs) {
	if (!model.paragraphs?.length && !paragraphs?.length) return;
	const first = text.slice(0, start).split("\n").length - 1, last = text.slice(0, end).split("\n").length - 1;
	const current = model.paragraphs?.length ? model.paragraphs : text.split("\n").map(() => ({ align: "" }));
	model.paragraphs = [...current.slice(0, first),
		...inserted.split("\n").map((_, i) => ({ ...(paragraphs?.[i] || current[first]) })), ...current.slice(last + 1)];
}

function distributeParagraph(input, paragraph) {
	const probe = input.cloneNode(false), copy = paragraph.cloneNode(true);
	Object.assign(probe.style, { position: "fixed", transform: "none", visibility: "hidden", pointerEvents: "none", width: `${input.offsetWidth}px` });
	probe.contentEditable = "false";
	probe.append(copy);
	document.body.append(probe);
	try {
		const lines = [], range = document.createRange(), width = copy.clientWidth;
		const spacing = parseFloat(getComputedStyle(copy).letterSpacing) || 0;
		const indent = parseFloat(getComputedStyle(copy).textIndent) || 0;
		for (const span of copy.children) {
			const node = span.firstChild;
			if (node?.nodeType !== 3) continue;
			for (const part of graphemes.segment(node.textContent)) {
				range.setStart(node, part.index); range.setEnd(node, part.index + part.segment.length);
				const rect = range.getBoundingClientRect();
				let line = lines.at(-1);
				if (!line || rect.top >= line.bottom - 0.5 || rect.bottom <= line.top + 0.5) {
					line = { top: rect.top, bottom: rect.bottom, parts: [] }; lines.push(line);
				}
				line.parts.push({ text: part.segment, width: rect.width, style: span.style.cssText });
			}
		}
		if (!lines.length) return;
		paragraph.replaceChildren();
		for (const [index, line] of lines.entries()) {
			if (index) { const wrap = document.createElement("br"); wrap.dataset.textWrap = "true"; paragraph.append(wrap); }
			const row = document.createElement("span");
			row.style.whiteSpace = "pre";
			let end = line.parts.length;
			while (end && line.parts[end - 1].text === " ") end--;
			const length = line.parts.slice(0, end).reduce((sum, part) => sum + part.width, 0) - (end ? spacing : 0);
			const extra = end > 1 ? Math.max(0, width - (index ? 0 : indent) - length) / (end - 1) : 0;
			let previous;
			for (const [i, part] of line.parts.entries()) {
				const tracking = i < end - 1 ? spacing + extra : i === end - 1 ? 0 : spacing;
				const key = `${part.style}:${tracking}`;
				if (previous?.key === key) previous.node.firstChild.appendData(part.text);
				else {
					const span = document.createElement("span"); span.style.cssText = part.style;
					span.style.letterSpacing = `${tracking}px`; span.textContent = part.text; row.append(span);
					previous = {key, node:span};
				}
			}
			paragraph.append(row);
		}
	} finally { probe.remove(); }
}

function normalizeRuns(runs) {
	const result = [];
	for (const run of runs) {
		if (!run.text) continue;
		const format = Object.fromEntries(FORMAT_KEYS.map(key => [key, run.format[key] ?? (["font", "color", "background"].includes(key) ? "" : key === "size" ? 0 : false)]));
		const previous = result.at(-1);
		if (previous && FORMAT_KEYS.every(key => previous.format[key] === format[key])) previous.text += run.text;
		else result.push({ text: run.text, format });
	}
	const text = result.map(run => run.text).join(""), whole = [];
	let index = 0, end = result[0]?.text.length || 0;
	for (const part of graphemes.segment(text)) {
		while (part.index >= end && index < result.length - 1) end += result[++index].text.length;
		const format = result[index].format, previous = whole.at(-1);
		if (previous && FORMAT_KEYS.every(key => previous.format[key] === format[key])) previous.text += part.segment;
		else whole.push({ text: part.segment, format: { ...format } });
	}
	return whole;
}

function sliceRuns(runs, start, end) {
	let offset = 0;
	return runs.flatMap(run => {
		const text = run.text.slice(Math.max(0, start - offset), Math.max(0, end - offset));
		offset += run.text.length;
		return text ? [{ text, format: { ...run.format } }] : [];
	});
}

function textChange(before, after) {
	const sameFormat = (a, b) => a && b && FORMAT_KEYS.every(key => a.format[key] === b.format[key]);
	let first = 0, lastBefore = before.length - 1, lastAfter = after.length - 1, start = 0;
	let endBefore = before.reduce((length, run) => length + run.text.length, 0);
	let endAfter = after.reduce((length, run) => length + run.text.length, 0);
	while (sameFormat(before[first], after[first]) && before[first].text === after[first].text) start += before[first++].text.length;
	while (lastBefore >= first && lastAfter >= first && sameFormat(before[lastBefore], after[lastAfter]) && before[lastBefore].text === after[lastAfter].text) {
		endBefore -= before[lastBefore--].text.length;
		endAfter -= after[lastAfter--].text.length;
	}
	if (first <= lastBefore && first <= lastAfter && sameFormat(before[first], after[first])) {
		const left = before[first].text, right = after[first].text;
		let count = 0;
		while (count < left.length && count < right.length && left[count] === right[count]) count++;
		start += count;
	}
	if (first <= lastBefore && first <= lastAfter && sameFormat(before[lastBefore], after[lastAfter])) {
		const left = before[lastBefore].text, right = after[lastAfter].text;
		let count = 0;
		while (count < Math.min(left.length, right.length, endBefore - start, endAfter - start) && left[left.length - count - 1] === right[right.length - count - 1]) count++;
		endBefore -= count;
		endAfter -= count;
	}
	return { start, before: sliceRuns(before, start, endBefore), after: sliceRuns(after, start, endAfter) };
}

export class RichTextSession {
	constructor(editor, model, faces = new Map(), imports = [], resources = new Map()) {
		this.editor = editor;
		this.editing = editor.input;
		this.model = structuredClone(model);
		this.model.runs = normalizeRuns(model.runs);
		this.initial = JSON.stringify(this.model);
		this.recordedParagraphs = this.model.paragraphs;
		this.recordedLayout = this.model.layout;
		this.faces = faces;
		for (const face of faces.values()) document.fonts.add(face);
		this.imports = new Map(imports.map(font => [font.key, font.data]));
		this.resources = resources;
		this.choices = new Map();
		if (this.editing.item.fontChoice && imports.length === 1) this.choices.set(imports[0].key, this.editing.item.fontChoice);
		this.typing = { ...(model.runs[0]?.format || richTextModel(this.editing.item).runs[0].format) };
		this.caret = [0, 0];
		this.entries = [{ typing: { ...this.typing }, caret: [...this.caret] }];
		this.index = 0;
		this.selectionListener = () => {
			if (editor.input !== this.editing || this.rendering || this.editing.composing) return;
			const caret = editTextSelection(this.editing.input);
			if (!caret) return;
			if (caret.join() !== this.caret.join()) {
				this.caret = caret;
				this.typing = { ...this.formatAt(Math.min(...caret)) };
			}
			editor.options.onTextChange();
		};
		document.addEventListener("selectionchange", this.selectionListener);
	}

	get text() { return this.model.runs.map(run => run.text).join(""); }
	get changed() { return JSON.stringify(this.model) !== this.initial; }
	get fonts() { return [...this.imports].filter(([key]) => this.model.runs.some(run => run.format.font === key)).map(([key, data]) => ({ key, data })); }
	selection() {
		const caret = editTextSelection(this.editing.input);
		if (caret) this.caret = caret;
		return graphemeRange(this.text, ...[...this.caret].sort((a, b) => a - b));
	}
	formatAt(position) {
		let offset = 0;
		for (const run of this.model.runs) {
			offset += run.text.length;
			if (position <= offset) return run.format;
		}
		return this.model.runs.at(-1)?.format || this.typing;
	}
	state() {
		const [start, end] = this.selection();
		if (start === end) return { ...this.typing };
		const runs = sliceRuns(this.model.runs, start, end);
		return Object.fromEntries(FORMAT_KEYS.map(key => [key, runs.every(run => run.format[key] === runs[0]?.format[key]) ? runs[0]?.format[key] : undefined]));
	}
	selectedRuns() {
		const [start, end] = this.selection();
		return start === end ? [{ text: "", format: { ...this.typing } }] : sliceRuns(this.model.runs, start, end);
	}
	remember() {
		this.selection();
		this.entries[this.index].caret = [...this.caret];
	}
	record(before) {
		const change = textChange(before, this.model.runs);
		const entry = { change, typing: { ...this.typing }, caret: [...this.caret] }, old = this.entries[this.index];
		const paragraphsChanged = JSON.stringify(this.recordedParagraphs) !== JSON.stringify(this.model.paragraphs);
		const layoutChanged = JSON.stringify(this.recordedLayout) !== JSON.stringify(this.model.layout);
		if (layoutChanged) entry.layout = { before: this.recordedLayout, after: this.model.layout };
		this.recordedLayout = this.model.layout;
		if (paragraphsChanged) entry.paragraphs = { before: this.recordedParagraphs, after: this.model.paragraphs };
		this.recordedParagraphs = this.model.paragraphs;
		const changed = change.before.length || change.after.length;
		if (!changed && !paragraphsChanged && !layoutChanged && JSON.stringify(entry.typing) === JSON.stringify(old.typing)) return;
		if (!changed) entry.change = null;
		this.entries.splice(++this.index, Infinity, entry);
		if (changed || paragraphsChanged || layoutChanged) this.preview();
	}
	undo(direction) {
		if (this.editing.composing || this.editing.saving) return;
		const entry = this.entries[this.index + direction];
		if (!entry) return;
		const change = (direction < 0 ? this.entries[this.index] : entry).change;
		if (change) {
			const removed = direction < 0 ? change.after : change.before, added = direction < 0 ? change.before : change.after;
			const end = change.start + removed.reduce((length, run) => length + run.text.length, 0);
			this.model.runs = normalizeRuns([...sliceRuns(this.model.runs, 0, change.start), ...added, ...sliceRuns(this.model.runs, end, this.text.length)]);
		}
		const paragraphs = (direction < 0 ? this.entries[this.index] : entry).paragraphs;
		if (paragraphs) this.model.paragraphs = direction < 0 ? paragraphs.before : paragraphs.after;
		const layout = (direction < 0 ? this.entries[this.index] : entry).layout;
		if (layout) this.model.layout = direction < 0 ? layout.before : layout.after;
		this.recordedLayout = this.model.layout;
		this.recordedParagraphs = this.model.paragraphs;
		this.index += direction;
		this.typing = { ...entry.typing };
		this.caret = [...entry.caret];
		this.render();
		this.editor.options.onTextChange();
		this.preview();
	}
	preview() {
		if (!this.editor.options.onPreviewRichText) return;
		clearTimeout(this.previewTimer);
		const sequence = this.previewSequence = (this.previewSequence || 0) + 1;
		this.previewTimer = setTimeout(async () => {
			const editing = this.editing;
			if (this.editor.input !== editing || editing.composing || editing.saving) return;
			const current = () => this.editor.input === editing && this.previewSequence === sequence && !editing.composing && !editing.saving;
			if (!this.model.runs.length) { this.editor.setTextError(null); return; }
			try {
				await this.editor.options.onPreviewRichText(editing.item, this.model, editing.frame.width, this.fonts);
				if (!current()) return;
				this.editor.setTextError(null);
			} catch (error) {
				if (!current()) return;
				this.editor.setTextError(error);
			}
		}, 300);
	}
	apply(values) {
		if (this.editing.composing || this.editing.saving) return;
		this.remember();
		const [start, end] = this.selection();
		const before = this.model.runs;
		const apply = format => typeof values === "function" ? values(format) : { ...format, ...values };
		this.caret = this.caret[0] > this.caret[1] ? [end, start] : [start, end];
		if (start !== end) this.model.runs = normalizeRuns([
			...sliceRuns(this.model.runs, 0, start),
			...sliceRuns(this.model.runs, start, end).map(run => ({ ...run, format: apply(run.format) })),
			...sliceRuns(this.model.runs, end, this.text.length),
		]);
		this.typing = apply(this.typing);
		this.record(before);
		this.render();
		this.editor.options.onTextChange();
	}
	align(value) {
		this.paragraph({ align: value });
	}
	paragraph(values) {
		if (this.editing.composing || this.editing.saving) return;
		this.remember();
		const [start, end] = this.selection(), [first, last] = paragraphRange(this.text, start, end);
		const { wrap, ...patch } = values;
		if (wrap !== undefined) this.model.layout = { ...this.model.layout, wrap };
		if (Object.keys(patch).length) this.model.paragraphs = this.text.split("\n").map((_, i) => ({ align: "", ...this.model.paragraphs?.[i], ...(i >= first && i <= last ? patch : {}) }));
		this.record(this.model.runs);
		this.render();
		this.editor.options.onTextChange();
	}
	replace(runs, paragraphs, range) {
		this.remember();
		const [start, end] = range || this.selection();
		const before = this.model.runs;
		replaceParagraphs(this.model, this.text, start, end, runs.map(run => run.text).join(""), paragraphs);
		this.model.runs = normalizeRuns([...sliceRuns(this.model.runs, 0, start), ...runs, ...sliceRuns(this.model.runs, end, this.text.length)]);
		this.caret = [start + runs.reduce((length, run) => length + run.text.length, 0)];
		this.caret.push(this.caret[0]);
		if (runs.length) this.typing = { ...runs.at(-1).format };
		this.record(before);
		this.render();
		this.editor.options.onTextChange();
	}
	readInput() {
		if (this.editing.composing) return;
		const value = editTextValue(this.editing.input), previous = this.text;
		if (value === previous) return;
		const caret = editTextSelection(this.editing.input) || this.caret;
		let start = 0, end = previous.length, nextEnd = value.length;
		const before = this.beforeCaret;
		if (before && value.startsWith(previous.slice(0, before[0])) && value.endsWith(previous.slice(before[1]))
			&& value.length >= previous.length - before[1] + before[0]) {
			start = before[0]; end = before[1]; nextEnd = value.length - (previous.length - end);
		} else {
			while (start < end && start < nextEnd && previous[start] === value[start]) start++;
			if (start && /[\uD800-\uDBFF]/.test(previous[start - 1])) start--;
			while (end > start && nextEnd > start && previous[end - 1] === value[nextEnd - 1]) { end--; nextEnd--; }
			if (end < previous.length && /[\uDC00-\uDFFF]/.test(previous[end])) { end++; nextEnd++; }
		}
		const runs = this.model.runs;
		replaceParagraphs(this.model, previous, start, end, value.slice(start, nextEnd));
		this.model.runs = normalizeRuns([...sliceRuns(this.model.runs, 0, start),
			{ text: value.slice(start, nextEnd), format: { ...this.typing } }, ...sliceRuns(this.model.runs, end, previous.length)]);
		this.caret = caret;
		this.beforeCaret = null;
		this.record(runs);
		this.render();
	}
	copy(event, cut = false) {
		if (this.editing.composing || this.editing.saving) return;
		const [start, end] = this.selection();
		if (start === end || !event.clipboardData) return;
		const runs = sliceRuns(this.model.runs, start, end), text = runs.map(run => run.text).join("");
		const keys = new Set(runs.map(run => run.format.font));
		const fonts = new Map([...keys].map(key => [key, this.imports.get(key) || this.resources.get(key)]).filter(([, data]) => data));
		const first = this.text.slice(0, start).split("\n").length - 1;
		const paragraphs = text.split("\n").map((_, i) => {
			const layout = { ...this.model.layout, ...this.model.paragraphs?.[first + i] };
			return Object.fromEntries(["align", "lineHeight", "letterSpacing", "leftIndent", "rightIndent", "firstLineIndent"].map(key => [key, key === "align" ? layout[key] || this.model.layout.align || "left" : layout[key] || 0]));
		});
		const clipboard = { token: crypto.randomUUID(), runs, text, paragraphs, scope: this.editor.options.textClipboardScope?.(),
			fonts, faces: new Map([...this.faces].filter(([key]) => keys.has(key))), choices: new Map([...this.choices].filter(([key]) => keys.has(key))) };
		if (this.editor.options.setTextClipboard) this.editor.options.setTextClipboard(clipboard);
		else this.editor.richClipboard = clipboard;
		event.preventDefault();
		event.clipboardData.setData("text/plain", text);
		event.clipboardData.setData(RICH_CLIPBOARD, clipboard.token);
		if (cut) this.replace([]);
	}
	async paste(event) {
		event.preventDefault();
		if (this.editing.composing || this.editing.saving) return;
		const text = event.clipboardData.getData("text/plain").replace(/\r\n?/g, "\n");
		const copy = this.editor.options.getTextClipboard ? this.editor.options.getTextClipboard() : this.editor.richClipboard;
		const sameScope = copy?.scope === this.editor.options.textClipboardScope?.();
		const internal = copy && (sameScope || copy.runs.every(run => copy.fonts.has(run.format.font)))
			&& event.clipboardData.getData(RICH_CLIPBOARD) === copy.token && text === copy.text;
		let runs;
		if (internal) {
			const keys = new Map(copy.runs.map(run => [run.format.font, sameScope ? run.format.font : `temp-font-${crypto.randomUUID()}`]));
			let faces = copy.faces;
			if (!sameScope) {
				const original = this.model.runs, paragraphs = this.model.paragraphs, caret = this.selection();
				faces = new Map(await Promise.all([...copy.fonts].map(async ([key, data]) =>
					[key, await new FontFace(`ofdgo-paste-${keys.get(key)}`, data, fontFaceStyle(data)).load()])));
				if (this.editor.input !== this.editing || this.editing.composing || this.editing.saving || this.model.runs !== original || this.model.paragraphs !== paragraphs || this.selection().join() !== caret.join()) return;
			}
			for (const [key, data] of copy.fonts) this.imports.set(keys.get(key), data);
			for (const [key, face] of faces) if (!this.faces.has(keys.get(key))) { this.faces.set(keys.get(key), face); document.fonts.add(face); }
			for (const [key, choice] of copy.choices) this.choices.set(keys.get(key), choice);
			runs = copy.runs.map(run => ({ text: run.text, format: { ...run.format, font: keys.get(run.format.font) } }));
		}
		this.replace(runs || [{ text, format: { ...this.typing } }], internal ? copy.paragraphs : null);
	}
	setFont(face, data, key) {
		key ||= `temp-font-${crypto.randomUUID()}`;
		const previous = this.faces.get(key);
		this.faces.set(key, face);
		if (previous && ![...this.faces.values()].includes(previous)) document.fonts.delete(previous);
		if (data) this.imports.set(key, data);
		document.fonts.add(face);
		this.apply({ font: key });
	}
	render() {
		this.rendering = true;
		const input = this.editing.input, layout = this.model.layout, lines = [[]], positions = [], distributed = [];
		const autoWidth = this.model.autoWidth && !layout.wrap;
		Object.assign(input.style, { whiteSpace: layout.wrap ? "pre-wrap" : "pre",
			lineHeight: layout.lineHeight ? `${layout.lineHeight * PX_PER_MM}px` : "normal",
			textIndent: `${(layout.firstLineIndent || 0) * PX_PER_MM}px`, letterSpacing: `${(layout.letterSpacing || 0) * PX_PER_MM}px` });
		for (const run of this.model.runs) {
			const pieces = run.text.split("\n");
			for (let i = 0; i < pieces.length; i++) {
				if (i) lines.push([]);
				lines.at(-1).push({ text: pieces[i], format: run.format });
			}
		}
		if (!lines[0].length) lines[0].push({ text: "", format: this.typing });
		this.editor.fitTextInput();
		const previous = this.renderedLines || [];
		let offset = 0;
		this.renderedLines = lines.map((parts, index) => {
			const layout = { ...this.model.layout, ...this.model.paragraphs?.[index] };
			const align = layout.align || this.model.layout.align || "left";
			const distribute = align === "distributed" && !CSS.supports("text-justify", "inter-character");
			const key = JSON.stringify([parts.map(part => [part.text, part.format, this.faces.get(part.format.font)?.family]), align, layout, distribute ? input.offsetWidth : null]);
			let paragraph = (!autoWidth || !distribute) && previous[index]?.key === key && previous[index].node.parentNode === input ? previous[index].node : null;
			if (!paragraph) {
				paragraph = document.createElement("div");
				for (const {text, format} of parts) {
					const span = document.createElement("span"), face = this.faces.get(format.font) || this.editing.face;
					Object.assign(span.style, { fontFamily: `"${face.family}"`, fontSize: `${format.size * PX_PER_MM}px`, color: format.color,
						fontWeight: format.bold ? "700" : face.weight || "400", fontStyle: format.italic ? "italic" : face.style || "normal",
						textDecoration: [format.underline && "underline", format.strikeout && "line-through"].filter(Boolean).join(" ") || "none",
						textShadow: format.shadow ? "0.06em 0.06em 0 #808080" : "none", backgroundColor: format.background || "transparent" });
					span.append(document.createTextNode(text));
					paragraph.append(span);
				}
				if (!paragraph.textContent) {
					paragraph.style.fontSize = paragraph.firstElementChild.style.fontSize;
					paragraph.append(document.createElement("br"));
				}
				Object.assign(paragraph.style, { minWidth: layout.wrap ? "" : "max-content", textAlign: distribute ? "left" : align === "distributed" ? "justify" : align,
					marginLeft: `${(layout.leftIndent || 0) * PX_PER_MM}px`, marginRight: `${(layout.rightIndent || 0) * PX_PER_MM}px`,
					lineHeight: layout.lineHeight ? `${layout.lineHeight * PX_PER_MM}px` : "normal", letterSpacing: `${(layout.letterSpacing || 0) * PX_PER_MM}px`, textIndent: `${(layout.firstLineIndent || 0) * PX_PER_MM}px`,
					textAlignLast: !distribute && align === "distributed" ? "justify" : "auto", textJustify: align === "distributed" ? "inter-character" : "auto" });
			}
			if (distribute && (autoWidth || previous[index]?.node !== paragraph)) distributed.push(paragraph);
			if (input.childNodes[index] !== paragraph) input.insertBefore(paragraph, input.childNodes[index] || null);
			return { key, node: paragraph };
		});
		while (input.childNodes.length > lines.length) input.lastChild.remove();
		if (autoWidth) input.style.width = getComputedStyle(input).width;
		for (const paragraph of distributed) distributeParagraph(input, paragraph);
		for (const [index, paragraph] of [...input.children].entries()) {
			if (index) offset++;
			const walker = document.createTreeWalker(paragraph, NodeFilter.SHOW_TEXT);
			let node;
			while ((node = walker.nextNode())) {
				positions.push({ node, start: offset, end: offset + node.textContent.length });
				offset += node.textContent.length;
			}
		}
		input.style.fontSize = this.model.runs.length ? "0px" : `${this.typing.size * PX_PER_MM}px`;
		input.style.caretColor = this.typing.color || "#000000";
		this.editing.range = null;
		input.focus({ preventScroll: true });
		restoreTextSelection(positions, this.caret);
		this.rendering = false;
	}
	destroy() {
		clearTimeout(this.previewTimer);
		document.removeEventListener("selectionchange", this.selectionListener);
		for (const face of this.faces.values()) document.fonts.delete(face);
	}
}

export function editTextValue(input) {
	let text = "", previous;
	for (const node of input.childNodes) {
		if (node.nodeName === "BR" && node.dataset.textWrap) continue;
		const block = node.nodeName === "DIV" || node.nodeName === "P";
		if (previous && (block || previous.nodeName === "DIV" || previous.nodeName === "P") && previous.nodeName !== "BR") text += "\n";
		text += node.nodeType === 3 ? node.textContent : node.nodeName === "BR" ? (node.nextSibling ? "\n" : "") : editTextValue(node);
		previous = node;
	}
	return text;
}

function editTextSelection(input, saved) {
	const selection = document.getSelection();
	const points = saved ? [[saved.startContainer, saved.startOffset], [saved.endContainer, saved.endOffset]]
		: selection?.rangeCount ? [[selection.anchorNode, selection.anchorOffset], [selection.focusNode, selection.focusOffset]] : null;
	if (!points || points.some(([node]) => !input.contains(node))) return null;
	return points.map(([node, position]) => {
		const range = document.createRange();
		range.selectNodeContents(input);
		range.setEnd(node, position);
		return editTextValue(range.cloneContents()).length;
	});
}

function restoreTextSelection(positions, caret, saved) {
	if (!caret || !positions.length) return;
	const endpoints = caret.map(point => {
		const entry = positions.find(entry => entry.end >= point) || positions.at(-1);
		return [entry.node, Math.max(0, Math.min(point - entry.start, entry.node.length))];
	});
	if (saved) {
		saved.setStart(...endpoints[0]);
		saved.setEnd(...endpoints[1]);
	} else document.getSelection().setBaseAndExtent(...endpoints[0], ...endpoints[1]);
}

export function pagePoint(x, y, rect, page, rotation) {
	const u = (x - rect.left) / rect.width;
	const v = (y - rect.top) / rect.height;
	const points = [[u, v], [v, 1 - u], [1 - u, 1 - v], [1 - v, u]];
	const point = points[rotation / 90];
	return { x: point[0] * page.width, y: point[1] * page.height };
}

export function objectTransform(box, dx, dy, corner = "") {
	if (!corner) {
		return { x: dx, y: dy, scale: 1 };
	}
	const left = corner.includes("w");
	const top = corner.includes("n");
	const x = left ? -dx : dx;
	const y = top ? -dy : dy;
	const minimum = Math.min(1, 1 / Math.min(box.width, box.height));
	const scale = Math.max(minimum, 1 + (x * box.width + y * box.height) / (box.width ** 2 + box.height ** 2));
	return { x: left ? box.width * (1 - scale) : 0, y: top ? box.height * (1 - scale) : 0, scale };
}

export function textTransform(box, dx, dy, corner) {
	if (corner !== "n" && corner !== "s") return objectTransform(box, dx, dy, corner);
	const top = corner === "n";
	const scale = Math.max(Math.min(1, 1 / box.height), 1 + (top ? -dy : dy) / box.height);
	return { x: box.width * (1 - scale) / 2,
		y: top ? box.height * (1 - scale) : 0, scale };
}

export function lineShape(shape) {
	return shape === "line" || shape === "arrow" || shape === "double-arrow";
}

function arrowPath(shape, box) {
	const { x, y, width: w, height: h } = box, length = Math.hypot(w, h);
	let path = `M${x} ${y}L${x + w} ${y + h}`;
	if (!length) return path;
	const head = Math.min(4, length / 3), ux = w / length, uy = h / length;
	for (const reverse of shape === "double-arrow" ? [false, true] : [false]) {
		const px = reverse ? x : x + w, py = reverse ? y : y + h, direction = reverse ? -1 : 1;
		const bx = px - ux * head * direction, by = py - uy * head * direction;
		path += `M${bx - uy * head * .45} ${by + ux * head * .45}L${px} ${py}L${bx + uy * head * .45} ${by - ux * head * .45}`;
	}
	return path;
}

export function selectionBounds(items) {
	let x = Infinity, y = Infinity, right = -Infinity, bottom = -Infinity;
	for (const item of items) {
		x = Math.min(x, item.x); y = Math.min(y, item.y);
		right = Math.max(right, item.x + item.width); bottom = Math.max(bottom, item.y + item.height);
	}
	return { x, y, width: right - x, height: bottom - y };
}

function resizeCorner(node, event) {
	let corner = event.target.dataset.corner || "", distance = Infinity;
	if (!corner) return corner;
	for (const handle of node.children) {
		if (!handle.dataset.corner) continue;
		const rect = handle.getBoundingClientRect();
		const next = (event.clientX - rect.left - rect.width / 2) ** 2 + (event.clientY - rect.top - rect.height / 2) ** 2;
		if (next < distance || next === distance && handle.dataset.corner === event.target.dataset.corner) {
			corner = handle.dataset.corner;
			distance = next;
		}
	}
	return corner;
}

export class AlignmentIndex {
	constructor(targets) {
		for (const [axis, size] of [["x", "width"], ["y", "height"]]) {
			this[axis] = targets.flatMap((target, index) => [0, 0.5, 1].map((anchor, order) => ({
				position: target[axis] + target[size] * anchor, target, order: index * 3 + order,
			}))).sort((a, b) => a.position - b.position || a.order - b.order);
		}
	}

	snap(box, tolerance) {
		const lines = [], offset = { x: 0, y: 0 };
		for (const [axis, size, cross, span] of [["x", "width", "y", "height"], ["y", "height", "x", "width"]]) {
			const anchors = this[axis];
			let best, distance = Infinity, rank = Infinity;
			for (let edge = 0; edge < 3; edge++) {
				const value = box[axis] + box[size] * edge / 2;
				let low = 0, high = anchors.length;
				while (low < high) {
					const middle = (low + high) >> 1;
					if (anchors[middle].position < value - tolerance[axis]) low = middle + 1;
					else high = middle;
				}
				for (let i = low; i < anchors.length && anchors[i].position <= value + tolerance[axis]; i++) {
					const anchor = anchors[i], delta = anchor.position - box[axis] - box[size] * edge / 2;
					const next = Math.abs(delta), order = anchor.order * 3 + edge;
					if (next > tolerance[axis] || next > distance || next === distance && order >= rank) continue;
					best = anchor;
					distance = next;
					rank = order;
					offset[axis] = delta;
				}
			}
			if (best) {
				const { position, target } = best;
				const from = Math.min(box[cross], target[cross]);
				const to = Math.max(box[cross] + box[span], target[cross] + target[span]);
				lines.push(axis === "x" ? `M${position} ${from}V${to}` : `M${from} ${position}H${to}`);
			}
		}
		return { ...offset, path: lines.join("") };
	}
}

function transformedBox(box, matrix) {
	const [a, b, c, d, e, f] = matrix;
	const points = [[box.x, box.y], [box.x + box.width, box.y], [box.x, box.y + box.height], [box.x + box.width, box.y + box.height]]
		.map(([x, y]) => [a * x + c * y + e, b * x + d * y + f]);
	const x = Math.min(...points.map(p => p[0])), y = Math.min(...points.map(p => p[1]));
	return { x, y, width: Math.max(...points.map(p => p[0])) - x, height: Math.max(...points.map(p => p[1])) - y };
}

function cropBox(box, bounds, dx, dy, corner) {
	if (!corner) return { ...box, x: Math.max(bounds.x, Math.min(box.x + dx, bounds.x + bounds.width - box.width)),
		y: Math.max(bounds.y, Math.min(box.y + dy, bounds.y + bounds.height - box.height)) };
	const next = reshapeBox({ geometry: box }, dx, dy, corner, false);
	const x = Math.max(bounds.x, next.x), y = Math.max(bounds.y, next.y);
	return { x, y, width: Math.min(bounds.x + bounds.width, next.x + next.width) - x,
		height: Math.min(bounds.y + bounds.height, next.y + next.height) - y };
}

export function constrainedPoint(from, to, shape, shift) {
	let dx = to.x - from.x, dy = to.y - from.y;
	if (shift && lineShape(shape)) {
		const directions = [[1, 0], [1, 1], [0, 1], [-1, 1], [-1, 0], [-1, -1], [0, -1], [1, -1]];
		const [x, y] = directions[(Math.round(Math.atan2(dy, dx) / (Math.PI / 4)) + 8) % 8];
		const length = (dx * x + dy * y) / (x * x + y * y);
		dx = x * length;
		dy = y * length;
	} else if (shift) {
		const size = Math.max(Math.abs(dx), Math.abs(dy));
		dx = (dx < 0 ? -1 : 1) * size;
		dy = (dy < 0 ? -1 : 1) * size;
	}
	return { x: from.x + dx, y: from.y + dy };
}

export function reshapeBox(item, dx, dy, handle, shift) {
	const box = item.geometry;
	if (!dx && !dy && !shift) return { ...box };
	if (lineShape(item.shape)) {
		const start = { x: box.x, y: box.y }, end = { x: box.x + box.width, y: box.y + box.height };
		const fixed = handle === "start" ? end : start, moving = handle === "start" ? start : end;
		const point = constrainedPoint(fixed, { x: moving.x + dx, y: moving.y + dy }, "line", shift);
		const from = handle === "start" ? point : fixed, to = handle === "start" ? fixed : point;
		return from.x === to.x && from.y === to.y ? null
			: { x: from.x, y: from.y, width: to.x - from.x, height: to.y - from.y };
	}
	if (shift && handle.length === 2) {
		const change = objectTransform(box, dx, dy, handle);
		return { x: box.x + change.x, y: box.y + change.y, width: box.width * change.scale, height: box.height * change.scale };
	}
	let x = box.x, y = box.y, right = x + box.width, bottom = y + box.height;
	const minWidth = Math.min(1, box.width), minHeight = Math.min(1, box.height);
	if (handle.includes("w")) x = Math.min(x + dx, right - minWidth);
	if (handle.includes("e")) right = Math.max(right + dx, x + minWidth);
	if (handle.includes("n")) y = Math.min(y + dy, bottom - minHeight);
	if (handle.includes("s")) bottom = Math.max(bottom + dy, y + minHeight);
	return { x, y, width: right - x, height: bottom - y };
}

export function reshapeFrame(frame, dx, dy, handle, shift) {
	const [a, b, c, d] = frame.matrix, determinant = a * d - b * c;
	return reshapeBox({ geometry: frame.box }, (d * dx - c * dy) / determinant, (a * dy - b * dx) / determinant, handle, shift);
}

function paintShape(node, shape, box) {
	const { x, y, width, height } = box;
	const attributes = lineShape(shape) && shape !== "line" ? { d: arrowPath(shape, box) }
		: shape === "line" ? { x1: x, y1: y, x2: x + width, y2: y + height }
		: shape === "ellipse" ? { cx: x + width / 2, cy: y + height / 2, rx: width / 2, ry: height / 2 }
		: { x, y, width, height };
	for (const [key, value] of Object.entries(attributes)) {
		node.setAttribute(key, value);
	}
}

export function canEditObject(item, capability) {
	if (!item) return false;
	const objects = item.selectionIDs ? [item] : item.items || [item];
	return objects.every(object => Boolean(object.capabilities?.[capability] || capability === "move" && object.capabilities?.transform));
}

export function missingGlyphMessage(diagnostic, action = "请更换字体") {
	if (!diagnostic) return "";
	const characters = Array.from(diagnostic.characters);
	const sample = characters.slice(0, 8).map(char => /[\p{C}\p{Z}\p{M}]/u.test(char)
		? `U+${char.codePointAt(0).toString(16).toUpperCase().padStart(4, "0")}` : char).join("、");
	return `字体缺字（${sample}${characters.length > 8 ? `等${characters.length}字` : ""}），${action}`;
}

export function objectEditReason(item) {
	const labels = { fontUnavailable: "字体不可用", unsupportedColor: "暂不支持此颜色", unsupportedStyle: "暂不支持此样式",
		unsupportedContainer: "暂不支持此对象结构", invalidObject: "对象数据异常" };
	const reasons = (item?.items || (item ? [item] : [])).map(object => {
		const reason = object.capabilities?.reason || "";
		if (!reason) return "";
		const message = labels[object.capabilities.reasonCode] || "暂不支持此对象特性";
		const available = object.capabilities.transform ? object.capabilities.paint ? "仍可移动、改色" : "仍可移动"
			: object.capabilities.replaceImage ? "仍可替换图片" : object.capabilities.copy && object.capabilities.delete ? "仍可复制、删除"
			: object.capabilities.delete ? "仍可删除" : "暂不可编辑";
		return object.capabilities.missingGlyphs ? missingGlyphMessage(object.capabilities.missingGlyphs, available) : `${message}，${available}`;
	});
	return [...new Set(reasons.filter(Boolean))].join(" · ");
}

export function selectedText(item) {
	if (item?.selectionIDs) return item;
	if (!item?.items) return item?.type === "TextObject" ? item : null;
	if (!item.items.every(member => member.type === "TextObject")) return null;
	const text = { ...item, type: "TextObject" };
	const formats = item.items.flatMap(member => member.richText ? member.richText.runs.map(run => run.format) : [{ ...member, ...richTextModel(member).runs[0].format }]);
	for (const key of ["font", "fontName", "size", "color", "bold", "italic", "underline", "strikeout", "shadow", "background"]) {
		const value = formats[0][key];
		text[key] = formats.every(format => format[key] === value) ? value : undefined;
	}
	const layouts = item.items.map(member => member.richText ? { ...richTextLayout(member.richText), wrap: Boolean(member.richText.layout.wrap) } : {
		align: member.align || "left", wrap: Boolean(member.wrap), lineHeight: member.paragraphHeight || 0,
		letterSpacing: member.letterSpacing || 0, leftIndent: member.leftIndent || 0, rightIndent: member.rightIndent || 0, firstLineIndent: member.firstLineIndent || 0,
	});
	for (const key of ["align", "wrap", "lineHeight", "letterSpacing", "leftIndent", "rightIndent", "firstLineIndent"]) {
		const values = layouts.map(layout => layout[key]);
		text[key === "lineHeight" ? "paragraphHeight" : key] = values.every(value => value === values[0]) ? values[0] : undefined;
	}
	return text;
}

export class ObjectIndex {
	constructor(items) {
		const build = items => {
			if (!items.length) return null;
			const bounds = selectionBounds(items);
			if (items.length <= 16) return { ...bounds, items };
			const axis = bounds.width >= bounds.height ? "x" : "y";
			items.sort((a, b) => a[axis] - b[axis]);
			const middle = items.length >> 1;
			return { ...bounds, left: build(items.slice(0, middle)), right: build(items.slice(middle)) };
		};
		this.root = build([...items]);
	}

	queryBox(box) {
		const result = [];
		const intersects = node => node.x <= box.x + box.width && node.x + node.width >= box.x
			&& node.y <= box.y + box.height && node.y + node.height >= box.y;
		const visit = node => {
			if (!node || !intersects(node)) return;
			if (node.items) {
				for (const item of node.items) {
					if (!item.parent && item.x >= box.x && item.x + item.width <= box.x + box.width
						&& item.y >= box.y && item.y + item.height <= box.y + box.height) result.push(item);
				}
			} else { visit(node.left); visit(node.right); }
		};
		visit(this.root);
		return result.sort((a, b) => a.order - b.order);
	}

	query(point, tolerance = 0) {
		const result = [];
		const contains = box => point.x >= box.x - tolerance && point.x <= box.x + box.width + tolerance
			&& point.y >= box.y - tolerance && point.y <= box.y + box.height + tolerance;
		const visit = node => {
			if (!node || !contains(node)) return;
			if (node.items) result.push(...node.items.filter(contains));
			else { visit(node.left); visit(node.right); }
		};
		visit(this.root);
		return result.sort((a, b) => b.order - a.order);
	}
}

export class CanvasEditor {
	constructor(viewer, options) {
		this.viewer = viewer;
		this.options = options;
		this.enabled = false;
		this.selected = null;
		this.multiple = false;
		this.nodes = new WeakMap();
		this.pages = new WeakMap();
		this.tool = "";
		viewer.addEventListener("pointerdown", (event) => this.start(event));
		viewer.addEventListener("pointermove", (event) => this.move(event));
		viewer.addEventListener("pointerup", (event) => this.end(event));
		viewer.addEventListener("pointercancel", () => this.cancel());
		viewer.addEventListener("contextmenu", event => {
			if (!this.enabled || this.input || this.options.busy()) return;
			const items = this.itemsAt(event);
			if (items.length > 1) {
				event.preventDefault();
				this.cancel();
				this.options.onPick?.(items);
			}
		});
		viewer.addEventListener("lostpointercapture", () => this.cancel());
		viewer.addEventListener("keydown", (event) => this.keyDown(event));
		viewer.addEventListener("keyup", (event) => this.keyUp(event));
		viewer.addEventListener("blur", () => this.commitNudge());
		viewer.addEventListener("dblclick", (event) => {
			const item = this.selected;
			const target = event.target === viewer ? document.elementFromPoint?.(event.clientX, event.clientY) : event.target;
			if (!event.altKey && this.enabled && item && !item.items && !this.input && !this.options.busy()
				&& target && (!target.dataset.corner || item.type === "TextObject")
				&& this.targetAt({ target, clientX: event.clientX, clientY: event.clientY }) === item) {
				this.options.onEdit(item, canEditObject(item, "children") ? pagePoint(event.clientX, event.clientY, item.surface.getBoundingClientRect(), item.page, this.options.rotation()) : null);
			}
		});
		viewer.addEventListener("scroll", () => this.scroll());
		window.addEventListener("blur", () => { this.cancel(); this.commitNudge(); });
		window.addEventListener("resize", () => this.cancel());
	}

	focus() {
		this.viewer.classList.remove("keyboard-focus");
		this.viewer.focus({ preventScroll: true });
	}

	setEnabled(enabled) {
		this.enabled = enabled;
		this.viewer.classList.toggle("editing-select", enabled);
		if (!enabled) {
			this.setTool("");
			this.clear();
		}
	}

	setTool(tool) {
		this.cancel();
		this.tool = tool;
		this.viewer.classList.toggle("editing-draw", Boolean(tool));
		if (tool) {
			this.select(null);
		}
		this.options.onTool?.(tool);
	}

	mount(index, page, surface) {
		surface.querySelector(".edit-layer")?.remove();
		const crop = this.crop?.item.index === index ? this.crop : null;
		if (crop) this.closeCrop(true);
		const pending = this.pendingSelection?.index === index;
		const selected = new Set(pending ? this.pendingSelection.ids || page.objects.slice(-1).map(item => item.id)
			: this.items().filter(item => item.index === index).map(item => item.id));
		const selection = [], items = [];
		const layer = document.createElement("div");
		layer.className = "edit-layer";
		const artwork = new Map([...surface.querySelectorAll("[data-ofd-object], [data-ofd-child]")].map((node, order) => [node.getAttribute("data-ofd-object") || node.getAttribute("data-ofd-child"), { node, order }]));
		const paintOrder = object => artwork.get(object.id)?.order ?? artwork.get(object.id.split("/")[0])?.order ?? 0;
		const objects = [...page.objects].sort((a, b) => paintOrder(a) - paintOrder(b));
		for (const [order, object] of objects.entries()) {
			const node = document.createElement("div");
			node.className = "edit-object";
			node.classList.toggle("read-only", !canEditObject(object, "move"));
			node.title = objectEditReason(object);
			node.tabIndex = 0;
			const item = { ...object, order, index, page: { width: page.width, height: page.height }, node, surface };
			item.artwork = artwork.get(object.id)?.node;
			if (object.note) {
				const icon = document.createElement("span");
				icon.className = "edit-note-icon";
				icon.textContent = "\u24d8";
				node.append(icon);
			}
			if (object.type === "PathObject" && (object.shape || object.outline || object.contours?.length)) {
				node.classList.add("edit-contour");
				const svg = document.createElementNS(SVG_NAMESPACE, "svg");
				svg.classList.add("edit-outline");
				svg.setAttribute("preserveAspectRatio", "none");
				let contour;
				if (object.shape || object.oriented || !object.contours?.length) {
					const shape = object.shape || object.oriented?.kind;
					contour = document.createElementNS(SVG_NAMESPACE, shape === "line" ? "line" : shape === "ellipse" ? "ellipse" : shape === "rectangle" ? "rect" : "path");
					if (!shape) contour.setAttribute("d", object.outline);
					svg.append(contour);
				} else {
					for (const path of object.contours) {
						const contour = document.createElementNS(SVG_NAMESPACE, "path");
						contour.setAttribute("d", path.path);
						svg.append(contour);
					}
				}
				node.append(svg);
				item.contour = { svg, path: contour };
			}
			this.nodes.set(node, item);
			items.push(item);
			node.addEventListener("focus", () => {
				if (this.input) this.selectAfterText(item);
				else this.select(item);
			});
			this.place(item);
			layer.append(node);
			if (selected.has(object.id)) {
				selection.push(item);
			}
		}
		this.pages.set(surface, { index, width: page.width, height: page.height, hitIndex: new ObjectIndex(items) });
		surface.append(layer);
		if (pending || selected.size) this.setSelection(selection, true);
		if (pending) this.pendingSelection = null;
		if (crop && selection.length === 1 && selection[0].id === crop.item.id
			&& ["x", "y", "width", "height"].every(key => selection[0][key] === crop.item[key])) {
			this.startCrop(selection[0], crop.pagePreview);
			this.crop.box = { ...crop.box };
			this.paintCrop();
		} else if (crop) {
			crop.pagePreview.urls.forEach(url => URL.revokeObjectURL(url));
		}
	}

	place(item, change = { x: 0, y: 0, scale: 1 }) {
		if (item.items) {
			for (const member of item.items) this.place(member, {
				x: change.x + (member.x - item.x) * (change.scale - 1),
				y: change.y + (member.y - item.y) * (change.scale - 1), scale: change.scale,
			});
		}
		if (item.artwork) {
			const scale = change.scale, x = item.x * (1 - scale) + change.x, y = item.y * (1 - scale) + change.y;
			if (x || y || scale !== 1) item.artwork.setAttribute("transform", `translate(${x} ${y}) scale(${scale})`);
			else item.artwork.removeAttribute("transform");
		}
		if (item.oriented) {
			this.placeOriented(item, item.oriented.box, change);
			return;
		}
		if (item.shape) {
			this.placeShape(item, { x: item.geometry.x + change.x + (item.geometry.x - item.x) * (change.scale - 1),
				y: item.geometry.y + change.y + (item.geometry.y - item.y) * (change.scale - 1),
				width: item.geometry.width * change.scale, height: item.geometry.height * change.scale });
			return;
		}
		Object.assign(item.node.style, {
			left: `${(item.x + change.x) * PX_PER_MM}px`, top: `${(item.y + change.y) * PX_PER_MM}px`,
			width: `${item.width * change.scale * PX_PER_MM}px`, height: `${item.height * change.scale * PX_PER_MM}px`,
		});
		if (item.contour) item.contour.svg.setAttribute("viewBox", `${item.x} ${item.y} ${item.width} ${item.height}`);
		if (item.textFrame) this.placeTextHandles(item, textFrameBox(item), item, change.scale);
	}

	placeTextHandles(item, box, bounds, scale = 1) {
		const [a, b, c, d, e, f] = item.textFrame.matrix;
		for (const handle of item.node.children) {
			if (handle.dataset.corner !== "w" && handle.dataset.corner !== "e") continue;
			const x = box.x + (handle.dataset.corner === "e" ? box.width : 0), y = box.y + box.height / 2;
			handle.style.left = `${(a * x + c * y + e - bounds.x) * scale * PX_PER_MM}px`;
			handle.style.top = `${(b * x + d * y + f - bounds.y) * scale * PX_PER_MM}px`;
			handle.style.cursor = a === 0 ? "var(--resize-vertical, ns-resize)" : "var(--resize-horizontal, ew-resize)";
		}
	}

	placeOriented(item, box, change = { x: 0, y: 0, scale: 1 }) {
		const frame = item.oriented, bounds = transformedBox(box, frame.matrix), scale = change.scale;
		const [a, b, c, d, e, f] = frame.matrix;
		Object.assign(item.node.style, {
			left: `${(item.x + change.x + (bounds.x - item.x) * scale) * PX_PER_MM}px`,
			top: `${(item.y + change.y + (bounds.y - item.y) * scale) * PX_PER_MM}px`,
			width: `${bounds.width * scale * PX_PER_MM}px`, height: `${bounds.height * scale * PX_PER_MM}px`,
		});
		for (const handle of item.node.children) {
			const corner = handle.dataset.corner;
			if (!corner) continue;
			const x = box.x + box.width * (corner.includes("w") ? 0 : corner.includes("e") ? 1 : .5);
			const y = box.y + box.height * (corner.includes("n") ? 0 : corner.includes("s") ? 1 : .5);
			handle.style.left = `${(a * x + c * y + e - bounds.x) * scale * PX_PER_MM}px`;
			handle.style.top = `${(b * x + d * y + f - bounds.y) * scale * PX_PER_MM}px`;
		}
		const { svg, path } = item.contour;
		svg.setAttribute("viewBox", `${bounds.x} ${bounds.y} ${bounds.width} ${bounds.height}`);
		path.setAttribute("transform", `matrix(${frame.matrix.join(" ")})`);
		paintShape(path, frame.kind, box);
	}

	placeShape(item, box) {
		const x = Math.min(box.x, box.x + box.width), y = Math.min(box.y, box.y + box.height);
		Object.assign(item.node.style, {
			left: `${x * PX_PER_MM}px`, top: `${y * PX_PER_MM}px`,
			width: `${Math.abs(box.width) * PX_PER_MM}px`, height: `${Math.abs(box.height) * PX_PER_MM}px`,
		});
		if (lineShape(item.shape)) {
			for (const handle of item.node.children) {
				if (!handle.dataset.corner) continue;
				const end = handle.dataset.corner === "end";
				handle.style.left = `${(box.x + (end ? box.width : 0) - x) * PX_PER_MM}px`;
				handle.style.top = `${(box.y + (end ? box.height : 0) - y) * PX_PER_MM}px`;
			}
		}
		if (item.contour) {
			const { svg, path } = item.contour;
			const width = Math.abs(box.width), height = Math.abs(box.height);
			path.removeAttribute("transform");
			svg.setAttribute("viewBox", `0 0 ${width || 1} ${height || 1}`);
			const values = lineShape(item.shape) && item.shape !== "line" ? { d: arrowPath(item.shape, { x: box.x - x, y: box.y - y, width: box.width, height: box.height }) }
				: item.shape === "line" ? { x1: box.x - x, y1: box.y - y, x2: box.x + box.width - x, y2: box.y + box.height - y }
				: item.shape === "ellipse" ? { cx: width / 2, cy: height / 2, rx: width / 2, ry: height / 2 }
					: { x: 0, y: 0, width, height };
			for (const [key, value] of Object.entries(values)) path.setAttribute(key, String(value));
		}
	}

	items() {
		return this.selected?.items || (this.selected ? [this.selected] : []);
	}

	setSelection(items, restoring = false) {
		items = items.filter(item => !items.some(parent => parent !== item && item.id.startsWith(parent.id + "/")));
		this.cancelNudge();
		const previous = this.selected;
		if (this.crop && (items.length !== 1 || items[0] !== this.crop.item)) this.closeCrop();
		if (this.selected?.items) this.selected.node.remove();
		for (const item of this.items()) {
			item.node.classList.remove("selected", "multi-selected");
			item.node.setAttribute("data-pressed", "false");
			for (const handle of [...item.node.children]) {
				if (handle.dataset.corner) handle.remove();
			}
		}
		this.selected = items.length < 2 ? items[0] || null : { ...selectionBounds(items), items,
			id: items.map(item => item.id), index: items[0].index, page: items[0].page, surface: items[0].surface };
		if (items.length > 1) {
			const node = document.createElement("div");
			node.className = "edit-layer edit-selection";
			for (const corner of canEditObject(this.selected, "transform") ? ["nw", "ne", "sw", "se"] : []) {
				const handle = document.createElement("span");
				handle.className = `edit-handle edit-${corner}`;
				handle.dataset.corner = corner;
				node.append(handle);
			}
			this.selected.node = node;
			this.nodes.set(node, this.selected);
			this.place(this.selected);
			this.selected.surface.append(node);
		} else if (items.length === 1) {
			const item = items[0];
			const handles = !canEditObject(item, "transform") ? [] : lineShape(item.shape) ? ["start", "end"]
				: item.shape || item.oriented || canEditObject(item, "stretch") ? ["nw", "n", "ne", "e", "se", "s", "sw", "w"]
					: item.type === "TextObject" && canEditObject(item, "reflow") && canEditObject(item, "layoutKnown")
						? ["nw", "ne", "sw", "se", "w", "e"] : ["nw", "ne", "sw", "se"];
			for (const corner of handles) {
				const handle = document.createElement("span");
				handle.className = `edit-handle edit-${corner}`;
				handle.dataset.corner = corner;
				item.node.append(handle);
			}
			this.place(item);
		}
		for (const item of items) {
			item.node.classList.add("selected");
			item.node.classList.toggle("multi-selected", items.length > 1);
			item.node.setAttribute("data-pressed", "true");
		}
		for (const node of this.viewer.querySelectorAll(".edit-parent")) node.classList.remove("edit-parent");
		for (const item of items) {
			if (item.parent) this.pageItems(item.index).find(parent => parent.id === item.parent)?.node.classList.add("edit-parent");
		}
		this.options.onSelect(this.selected, previous, restoring);
	}

	select(item, add = false) {
		if (!add || !item || this.selected && item.index !== this.selected.index) {
			this.setSelection(item ? [item] : []);
			return;
		}
		const items = this.items();
		this.setSelection(items.includes(item) ? items.filter(member => member !== item) : [...items, item]);
	}

	selectParent() {
		const item = this.selected;
		if (!item?.parent) return false;
		const parent = this.pageItems(item.index).find(parent => parent.id === item.parent);
		if (!parent) return false;
		this.select(parent);
		this.focus();
		return true;
	}

	selectAll(index) {
		if (!this.enabled || this.input || this.crop || this.drag || this.nudge || this.nudgeCommit || this.options.busy()) return;
		this.setTool("");
		document.getSelection()?.removeAllRanges();
		this.setSelection(this.pageItems(index));
		this.focus();
	}

	clear() {
		this.closeText();
		this.closeCrop();
		this.cancel();
		this.pendingSelection = null;
		this.select(null);
	}

	start(event) {
		if (event.target === this.viewer) {
			const rect = this.viewer.getBoundingClientRect();
			const left = rect.left + (this.viewer.clientLeft || 0), top = rect.top + (this.viewer.clientTop || 0);
			if (event.clientX < left || event.clientX >= left + this.viewer.clientWidth
				|| event.clientY < top || event.clientY >= top + this.viewer.clientHeight) return;
		}
		if (this.nudge) {
			this.commitNudge();
			return;
		}
		if (this.input) {
			if (event.button === 0 && !this.input.input.contains(event.target)) {
				if (this.input.composing) this.commitText();
				else {
					event.preventDefault();
					this.selectAfterText(this.targetAt(event), this.pages.get(event.target.closest(".page-surface"))?.index);
				}
			}
			return;
		}
		if (!this.enabled || this.options.busy() || this.nudgeCommit || event.button !== 0 || this.drag) {
			return;
		}
		if (this.crop) {
			if (!event.target.closest(".edit-crop")) {
				this.commitCrop();
				return;
			}
			event.preventDefault();
			this.focus();
			const { item, box } = this.crop;
			this.drag = { crop: true, box: { ...box }, pointerID: event.pointerId, corner: resizeCorner(this.crop.node, event),
				page: item.page, rect: item.surface.getBoundingClientRect(), rotation: this.options.rotation(), clientX: event.clientX, clientY: event.clientY };
			this.viewer.setPointerCapture(event.pointerId);
			return;
		}
		if (this.tool) {
			const page = this.pages.get(event.target.closest(".page-surface"));
			if (page) this.options.onPage?.(page.index);
			if (this.tool.startsWith("erase-")) this.startErase(event);
			else if (this.tool.startsWith("pen-")) this.startInk(event);
			else this.startShape(event);
			return;
		}
		if (event.altKey && !event.shiftKey && !event.ctrlKey && !event.metaKey && !event.target.dataset.corner) {
			this.cycleSelection(event);
			return;
		}
		const target = this.targetAt(event), node = target?.node;
		const page = this.pages.get(event.target.closest(".page-surface"));
		if (page) this.options.onPage?.(page.index);
		if (event.target.closest(".edit-object, .edit-selection") || target) event.preventDefault();
		this.focus();
		const additive = event.shiftKey || event.ctrlKey || event.metaKey || this.multiple;
		if (!target) {
			this.startMarquee(event, additive);
			return;
		}
		if (additive && !event.target.dataset.corner) {
			this.select(target, true);
			return;
		}
		if (target !== this.selected && !this.items().includes(target)) this.select(target);
		if (!node || this.options.busy()) {
			return;
		}
		const item = this.selected;
		if (!canEditObject(item, "move")) return;
		this.drag = {
			item, pointerID: event.pointerId, corner: !item.items || target === item ? resizeCorner(node, event) : "",
			rect: item.surface.getBoundingClientRect(), rotation: this.options.rotation(),
			clientX: event.clientX, clientY: event.clientY,
		};
		const extent = this.drag.extent = document.createElement("div");
		Object.assign(extent.style, { position: "absolute", left: "0", top: "0", pointerEvents: "none",
			width: `${this.viewer.scrollWidth}px`, height: `${this.viewer.scrollHeight}px` });
		this.viewer.append(extent);
		if (!this.drag.corner) {
			const selected = this.items();
			const targets = [...item.surface.querySelectorAll(".edit-object")]
				.map(node => this.nodes.get(node)).filter(member => !selected.some(item => item === member || item.id.startsWith(member.id + "/") || member.id.startsWith(item.id + "/")));
			targets.push({ x: 0, y: 0, ...item.page });
			this.drag.alignment = new AlignmentIndex(targets);
		}
		this.viewer.setPointerCapture(event.pointerId);
	}

	contains(item, point, tolerance = 0) {
		if (point.x < item.x - tolerance || point.x > item.x + item.width + tolerance || point.y < item.y - tolerance || point.y > item.y + item.height + tolerance) return false;
		if (!item.contours) return point.x >= item.x && point.x <= item.x + item.width && point.y >= item.y && point.y <= item.y + item.height;
		this.hitContext ||= document.createElement("canvas").getContext("2d");
		item.hitPaths ||= item.contours.map(contour => ({ path: new Path2D(contour.path), rule: contour.evenOdd ? "evenodd" : "nonzero" }));
		this.hitContext.lineWidth = tolerance * 2;
		return item.hitPaths.some(({ path, rule }) => this.hitContext.isPointInPath(path, point.x, point.y, rule)
			|| tolerance > 0 && this.hitContext.isPointInStroke(path, point.x, point.y));
	}

	targetAt(event) {
		if (event.target === this.viewer) {
			const target = document.elementFromPoint?.(event.clientX, event.clientY);
			if (!target || target === this.viewer) return;
			event = { target, clientX: event.clientX, clientY: event.clientY };
		}
		const direct = this.nodes.get(event.target.closest(".edit-object, .edit-selection"));
		return event.target.dataset.corner || direct && !direct.contours && (!direct.parent || this.items().some(selected => selected.id.startsWith(direct.parent + "/"))) ? direct : this.itemsAt(event)[0];
	}

	itemsAt(event) {
		const surface = event.target.closest(".page-surface");
		const page = this.pages.get(surface);
		if (!page) return [];
		const rect = surface.getBoundingClientRect(), rotation = this.options.rotation();
		const point = pagePoint(event.clientX, event.clientY, rect, page, rotation);
		const tolerance = 3 * page.width / (rotation % 180 ? rect.height : rect.width);
		return page.hitIndex.query(point, tolerance).filter(item => !item.parent || this.items().some(selected => selected.id.startsWith(item.parent + "/"))).filter(item => item.type === "TextObject"
			? point.x >= item.x - tolerance && point.x <= item.x + item.width + tolerance
				&& point.y >= item.y - tolerance && point.y <= item.y + item.height + tolerance
			: this.contains(item, point, tolerance));
	}

	async selectAfterText(target, index) {
		if (!await this.commitText() || this.input || !this.enabled || this.options.busy() || this.tool) return;
		if (index !== undefined) this.options.onPage?.(index);
		if (target) {
			const item = this.pageItems(target.index).find(item => item.id === target.id);
			if (item) this.select(item);
		} else if (index !== undefined) this.select(null);
		this.focus();
	}

	pageItems(index) {
		return [...this.viewer.querySelectorAll(".edit-object")].map(node => this.nodes.get(node)).filter(item => item?.index === index);
	}

	cycleSelection(event) {
		const items = this.itemsAt(event);
		if (!items.length) return;
		event.preventDefault();
		this.focus();
		this.select(items[(items.indexOf(this.selected) + 1) % items.length]);
	}

	move(event) {
		const drag = this.drag;
		if (!drag || drag.pointerID !== event.pointerId) {
			return;
		}
		event.preventDefault();
		drag.pointer = event;
		if (drag.ink) {
			this.moveInk(event, drag);
			return;
		}
		if (drag.erase) {
			this.moveErase(event, drag);
			return;
		}
		if (drag.crop) {
			const from = pagePoint(drag.clientX, drag.clientY, drag.rect, drag.page, drag.rotation);
			const to = pagePoint(event.clientX, event.clientY, drag.rect, drag.page, drag.rotation);
			this.crop.box = cropBox(drag.box, this.crop.item.imageBounds, to.x - from.x, to.y - from.y, drag.corner);
			this.paintCrop();
			return;
		}
		if (drag.shape) {
			this.moveShape(event, drag);
			return;
		}
		if (drag.marquee) {
			this.moveMarquee(event, drag);
			this.autoScroll(drag);
			return;
		}
		if (!drag.change && !drag.box && Math.hypot(event.clientX - drag.clientX, event.clientY - drag.clientY) < 3) {
			return;
		}
		const from = drag.origin ||= pagePoint(drag.clientX, drag.clientY, drag.rect, drag.item.page, drag.rotation);
		const to = pagePoint(event.clientX, event.clientY, drag.rect, drag.item.page, drag.rotation);
		this.autoScroll(drag);
		if (drag.item.type === "TextObject" && (drag.corner === "w" || drag.corner === "e")) {
			if (drag.item.textFrame) {
				const frame = drag.item.textFrame, [a, b, c, d] = frame.matrix;
				const dx = (d * (to.x - from.x) - c * (to.y - from.y)) / (a * d - b * c);
				const box = reshapeBox({ geometry: textFrameBox(drag.item) }, dx, 0, drag.corner, false);
				drag.box = { x: drag.item.x + box.x, width: box.width };
				const bounds = transformedBox(box, frame.matrix);
				Object.assign(drag.item.node.style, { left: `${bounds.x * PX_PER_MM}px`, top: `${bounds.y * PX_PER_MM}px`,
					width: `${bounds.width * PX_PER_MM}px`, height: `${bounds.height * PX_PER_MM}px` });
				this.placeTextHandles(drag.item, box, bounds);
				return;
			}
			drag.box = reshapeBox({ geometry: drag.item }, to.x - from.x, 0, drag.corner, false);
			Object.assign(drag.item.node.style, { left: `${drag.box.x * PX_PER_MM}px`, width: `${drag.box.width * PX_PER_MM}px` });
			return;
		}
		if (canEditObject(drag.item, "stretch") && drag.corner.length === 1) {
			drag.box = reshapeBox({ geometry: drag.item }, to.x - from.x, to.y - from.y, drag.corner, false);
			const sx = drag.box.width / drag.item.width, sy = drag.box.height / drag.item.height;
			Object.assign(drag.item.node.style, { left: `${drag.box.x * PX_PER_MM}px`, top: `${drag.box.y * PX_PER_MM}px`,
				width: `${drag.box.width * PX_PER_MM}px`, height: `${drag.box.height * PX_PER_MM}px` });
			drag.item.artwork?.setAttribute("transform", `translate(${drag.box.x - drag.item.x * sx} ${drag.box.y - drag.item.y * sy}) scale(${sx} ${sy})`);
			return;
		}
		if (drag.item.oriented && drag.corner) {
			const frame = drag.item.oriented;
			drag.box = reshapeFrame(frame, to.x - from.x, to.y - from.y, drag.corner, event.shiftKey);
			this.placeOriented(drag.item, drag.box);
			const matrix = new DOMMatrix(frame.matrix), sx = drag.box.width / frame.box.width, sy = drag.box.height / frame.box.height;
			const transform = matrix.translate(drag.box.x - frame.box.x * sx, drag.box.y - frame.box.y * sy).scale(sx, sy).multiply(matrix.inverse());
			drag.item.artwork?.setAttribute("transform", `matrix(${[transform.a, transform.b, transform.c, transform.d, transform.e, transform.f].join(" ")})`);
			return;
		}
		if (drag.item.shape && drag.corner) {
			drag.box = reshapeBox(drag.item, to.x - from.x, to.y - from.y, drag.corner, event.shiftKey);
			const original = drag.item.geometry, box = drag.box || original;
			this.placeShape(drag.item, box);
			let a, b = 0, c = 0, d;
			if (lineShape(drag.item.shape)) {
				const length = original.width ** 2 + original.height ** 2;
				a = d = (box.width * original.width + box.height * original.height) / length;
				b = (box.height * original.width - box.width * original.height) / length;
				c = -b;
			} else {
				a = box.width / original.width;
				d = box.height / original.height;
			}
			const x = box.x - a * original.x - c * original.y, y = box.y - b * original.x - d * original.y;
			drag.item.artwork?.setAttribute("transform", `matrix(${a} ${b} ${c} ${d} ${x} ${y})`);
			if (lineShape(drag.item.shape) && drag.item.shape !== "line") {
				const path = drag.item.contour.path;
				path.setAttribute("d", arrowPath(drag.item.shape, original));
				path.setAttribute("transform", `matrix(${a} ${b} ${c} ${d} ${x - Math.min(box.x, box.x + box.width)} ${y - Math.min(box.y, box.y + box.height)})`);
			}
			return;
		}
		const transform = drag.item.type === "TextObject" ? textTransform : objectTransform;
		drag.change = transform(drag.item, to.x - from.x, to.y - from.y, drag.corner);
		if (!drag.corner) {
			drag.axis = event.shiftKey ? Math.abs(drag.change.x) >= Math.abs(drag.change.y) ? "x" : "y" : "";
			if (drag.axis) drag.change[drag.axis === "x" ? "y" : "x"] = 0;
			this.snap(drag, event.altKey);
		}
		this.place(drag.item, drag.change);
	}

	scroll() {
		const drag = this.drag;
		if (!drag || !drag.item && !drag.marquee || !drag.pointer) {
			this.cancel();
			return;
		}
		drag.origin ||= pagePoint(drag.clientX, drag.clientY, drag.rect, drag.item?.page || drag.page, drag.rotation);
		drag.rect = (drag.item?.surface || drag.surface).getBoundingClientRect();
		this.move(drag.pointer);
	}

	autoScroll(drag) {
		if (drag.scrolling) return;
		drag.scrolling = true;
		requestAnimationFrame(time => {
			drag.scrolling = false;
			if (this.drag !== drag) return;
			const viewer = this.viewer, rect = viewer.getBoundingClientRect(), pointer = drag.pointer;
			const speed = (value, start, size) => value < start + 32 ? Math.max(-1, (value - start - 32) / 32)
				: value > start + size - 32 ? Math.min(1, (value - start - size + 32) / 32) : 0;
			const elapsed = Math.min(32, time - (drag.scrollTime ?? time - 16));
			drag.scrollTime = time;
			const left = viewer.scrollLeft, top = viewer.scrollTop;
			const x = speed(pointer.clientX, rect.left, viewer.clientWidth) * elapsed;
			const y = speed(pointer.clientY, rect.top, viewer.clientHeight) * elapsed;
			if (this.options.scrollBy) this.options.scrollBy(x, y);
			else { viewer.scrollLeft += x; viewer.scrollTop += y; }
			if (viewer.scrollLeft !== left || viewer.scrollTop !== top) this.scroll();
			else drag.scrollTime = null;
		});
	}

	snap(drag, bypass = false) {
		const { item, rect, rotation, change } = drag;
		const { x, y, path } = bypass ? { x: 0, y: 0, path: "" } : drag.alignment.snap({ ...item, x: item.x + change.x, y: item.y + change.y }, {
			x: drag.axis === "y" ? 0 : 4 * item.page.width / (rotation % 180 ? rect.height : rect.width),
			y: drag.axis === "x" ? 0 : 4 * item.page.height / (rotation % 180 ? rect.width : rect.height),
		});
		change.x += x;
		change.y += y;
		if (!drag.guides && path) {
			drag.guides = this.createPreview(item.surface, item.page, "path", {
				fill: false, stroke: true, strokeColor: "var(--accent)", lineWidth: 1,
			});
			drag.guides.node.setAttribute("vector-effect", "non-scaling-stroke");
			drag.guides.node.setAttribute("stroke-dasharray", "4 3");
		}
		drag.guides?.node.setAttribute("d", path);
	}

	end(event) {
		const drag = this.drag;
		if (!drag || drag.pointerID !== event.pointerId) {
			return;
		}
		this.move(event);
		if (drag.ink) {
			this.drag = null;
			this.viewer.releasePointerCapture(drag.pointerID);
			if (drag.pressure && drag.points.every(point => point.pressure === 0)) {
				drag.preview.remove();
				return;
			}
			Promise.resolve(this.options.onDrawInk(drag.page.index, drag.points, drag.style, drag.pressure)).finally(() => drag.preview.remove());
			return;
		}
		if (drag.erase) {
			const items = drag.erase === "erase-object" ? [...drag.erased] : drag.box
				? drag.items.filter(item => item.x < drag.box.x + drag.box.width && item.x + item.width > drag.box.x
					&& item.y < drag.box.y + drag.box.height && item.y + item.height > drag.box.y) : [];
			this.cancel();
			if (items.length && (!drag.points || drag.points.length >= 3)) this.options.onErase(items, drag.box, drag.points);
			return;
		}
		if (drag.crop) {
			this.drag = null;
			this.viewer.releasePointerCapture(drag.pointerID);
			return;
		}
		if (drag.marquee) {
			this.drag = null;
			drag.preview.remove();
			this.viewer.releasePointerCapture(drag.pointerID);
			return;
		}
		if (drag.shape) {
			this.drag = null;
			this.viewer.releasePointerCapture(drag.pointerID);
			if (drag.annotation) {
				const point = pagePoint(drag.clientX, drag.clientY, drag.rect, drag.page, drag.rotation);
				const box = drag.box || { ...point, width: drag.annotation === "watermark" ? 0 : drag.annotation === "note" ? 5 : 40, height: drag.annotation === "watermark" ? 0 : drag.annotation === "note" ? 5 : 12 };
				drag.preview.remove();
				this.setTool("");
				this.options.onDrawAnnotation(drag.page.index, drag.annotation, box);
				return;
			}
			if (drag.shape === "text") {
				const from = pagePoint(drag.clientX, drag.clientY, drag.rect, drag.page, drag.rotation);
				const x = Math.max(0, Math.min(from.x, drag.page.width - 1));
				const y = Math.max(0, Math.min(from.y, drag.page.height - 1));
				const box = drag.box ? { ...drag.box, wrap: true } : { x, y, width: Math.min(80, drag.page.width - x), height: 5, wrap: false };
				drag.preview.remove();
				this.setTool("");
				this.options.onDrawText(drag.page.index, box, drag.surface, drag.page);
				return;
			}
			if (!drag.box) {
				drag.preview.remove();
				return;
			}
			this.setTool("");
			Promise.resolve(this.options.onDraw(drag.page.index, drag.shape, drag.box, drag.style))
				.finally(() => drag.preview.remove());
			return;
		}
		if (drag.corner && drag.box) {
			this.drag = null;
			drag.extent?.remove();
			this.viewer.releasePointerCapture(drag.pointerID);
			const frame = drag.item.textFrame;
			const geometry = drag.item.oriented?.box || drag.item.geometry || (frame ? { x: drag.item.x, width: textFrameBox(drag.item).width } : drag.item);
			const changed = Object.keys(drag.box).some((key) => drag.box[key] !== geometry[key]);
			const apply = drag.item.shape || drag.item.oriented ? this.options.onReshape : canEditObject(drag.item, "stretch") ? this.options.onResize : this.options.onTextWidth;
			Promise.resolve(changed && apply(drag.item, drag.box)).finally(() => {
				drag.preview?.remove();
				this.place(drag.item);
			});
			return;
		}
		if (drag.change && (drag.change.x || drag.change.y || drag.change.scale !== 1)) {
			this.drag = null;
			drag.extent?.remove();
			drag.guides?.preview.remove();
			this.viewer.releasePointerCapture(drag.pointerID);
			Promise.resolve(this.options.onTransform(drag.item, drag.change)).finally(() => this.place(drag.item));
		} else this.cancel();
	}

	cancel() {
		const drag = this.drag;
		this.drag = null;
		if (drag) {
			drag.extent?.remove();
			for (const item of drag.erased || []) item.node.classList.remove("erasing");
			if (drag.crop) {
				this.crop.box = drag.box;
				this.paintCrop();
			}
			drag.preview?.remove();
			drag.guides?.preview.remove();
			if (drag.marquee) this.setSelection(drag.before);
			if (drag.item) {
				this.place(drag.item);
			}
			if (this.viewer.hasPointerCapture(drag.pointerID)) {
				this.viewer.releasePointerCapture(drag.pointerID);
			}
		}
	}

	startInk(event) {
		const surface = event.target.closest(".page-surface") || this.nearestSurface(event), page = this.pages.get(surface);
		if (!page || !this.options.canInsert(page.index)) return;
		event.preventDefault();
		this.focus();
		const style = { ...this.options.drawStyle(), fill: false, stroke: true };
		const pressure = this.tool === "pen-pressure";
		const preview = this.createPreview(surface, page, "path", pressure ? { ...style, fill: true, fillColor: style.strokeColor, stroke: false } : style);
		preview.node.setAttribute("stroke-linecap", "round");
		preview.node.setAttribute("stroke-linejoin", "round");
		this.drag = { ink: true, pressure, points: [], parts: [], page, surface, style, ...preview, pointerID: event.pointerId,
			rect: surface.getBoundingClientRect(), rotation: this.options.rotation() };
		this.viewer.setPointerCapture(event.pointerId);
		this.moveInk(event, this.drag);
	}

	moveInk(event, drag) {
		const coalesced = event.getCoalescedEvents?.() || [];
		for (const sample of coalesced.length ? coalesced : [event]) {
			const point = pagePoint(sample.clientX, sample.clientY, drag.rect, drag.page, drag.rotation);
			const last = drag.points.at(-1);
			point.pressure = sample.pointerType === "pen" ? event.type === "pointerup" ? last?.pressure ?? sample.pressure : sample.pressure : 0.5;
			if (last && Math.hypot(point.x-last.x, point.y-last.y) < 0.05 && Math.abs(point.pressure-last.pressure) < 0.01) continue;
			drag.points.push(point);
			if (!drag.pressure) {
				drag.parts.push(`${last ? "L" : "M"}${point.x} ${point.y}`);
			} else {
				const radius = drag.style.lineWidth * point.pressure / 2;
				if (radius > 0) drag.parts.push(`M${point.x+radius} ${point.y}a${radius} ${radius} 0 1 1 ${-2*radius} 0a${radius} ${radius} 0 1 1 ${2*radius} 0Z`);
				if (last) {
					const r = drag.style.lineWidth * last.pressure / 2, dx = point.x-last.x, dy = point.y-last.y, length = Math.hypot(dx,dy);
					if ((radius > 0 || r > 0) && length > Math.abs(radius-r)) {
						const x = dx/length, y = dy/length, a = (r-radius)/length, b = Math.sqrt(1-a*a);
						const nx = a*x-b*y, ny = a*y+b*x, mx = a*x+b*y, my = a*y-b*x;
						drag.parts.push(`M${last.x+r*mx} ${last.y+r*my}L${point.x+radius*mx} ${point.y+radius*my}L${point.x+radius*nx} ${point.y+radius*ny}L${last.x+r*nx} ${last.y+r*ny}Z`);
					}
				}
			}
		}
		const dot = drag.points.length === 1 && !drag.pressure ? `l0.001 0` : "";
		drag.node.setAttribute("d", drag.parts.join(" ") + dot);
	}

	startErase(event) {
		const surface = event.target.closest(".page-surface") || this.nearestSurface(event);
		const page = this.pages.get(surface);
		if (!page) return;
		event.preventDefault();
		this.focus();
		this.drag = { erase: this.tool, page, surface, items: [...surface.querySelectorAll(".edit-object")].map(node => this.nodes.get(node)).filter(item => !item.parent),
			erased: new Set(), shape: "rectangle", pointerID: event.pointerId, clientX: event.clientX, clientY: event.clientY,
			rect: surface.getBoundingClientRect(), rotation: this.options.rotation() };
		if (this.tool === "erase-region") Object.assign(this.drag, this.createPreview(surface, page, "rectangle",
			{ fill: true, fillColor: "var(--accent-soft)", stroke: true, strokeColor: "var(--accent)", lineWidth: 0.2 }));
		if (this.tool === "erase-path") {
			Object.assign(this.drag, this.createPreview(surface, page, "path",
				{ fill: true, fillColor: "var(--accent-soft)", stroke: true, strokeColor: "var(--accent)", lineWidth: 0.2 }), { points: [] });
			this.drag.node.setAttribute("fill-rule", "evenodd");
		}
		this.viewer.setPointerCapture(event.pointerId);
		this.moveErase(event, this.drag);
	}

	moveErase(event, drag) {
		if (drag.erase === "erase-region") { this.moveShape(event, drag); return; }
		const point = pagePoint(event.clientX, event.clientY, drag.rect, drag.page, drag.rotation);
		if (drag.points) {
			const last = drag.lastPointer;
			if (last && Math.hypot(event.clientX - last.x, event.clientY - last.y) < 2) return;
			drag.lastPointer = { x: event.clientX, y: event.clientY };
			drag.points.push(point);
			const box = drag.box || { x: point.x, y: point.y, width: 0, height: 0 };
			const right = Math.max(box.x + box.width, point.x), bottom = Math.max(box.y + box.height, point.y);
			drag.box = { x: Math.min(box.x, point.x), y: Math.min(box.y, point.y) };
			Object.assign(drag.box, { width: right - drag.box.x, height: bottom - drag.box.y });
			drag.node.setAttribute("d", drag.points.map((point, index) => `${index ? "L" : "M"}${point.x} ${point.y}`).join(" ") + " Z");
			return;
		}
		const item = [...drag.items].reverse().find(item => this.contains(item, point, 3 * drag.page.width / (drag.rotation % 180 ? drag.rect.height : drag.rect.width)));
		if (item) { drag.erased.add(item); item.node.classList.add("erasing"); }
	}

	startMarquee(event, additive) {
		const surface = event.target.closest(".page-surface"), page = this.pages.get(surface);
		if (!page) {
			this.select(null);
			return;
		}
		event.preventDefault();
		const before = this.items();
		const base = additive ? before.filter(item => item.index === page.index) : [];
		this.setSelection(base);
		this.drag = { marquee: true, page, surface, before, base,
			...this.createPreview(surface, page, "rectangle", { fill: true, fillColor: "var(--accent-soft)", stroke: true, strokeColor: "var(--accent)", lineWidth: 0.2 }),
			pointerID: event.pointerId, clientX: event.clientX, clientY: event.clientY,
			rect: surface.getBoundingClientRect(), rotation: this.options.rotation() };
		this.viewer.setPointerCapture(event.pointerId);
	}

	moveMarquee(event, drag) {
		const from = drag.origin ||= pagePoint(drag.clientX, drag.clientY, drag.rect, drag.page, drag.rotation);
		const to = pagePoint(event.clientX, event.clientY, drag.rect, drag.page, drag.rotation);
		const box = { x: Math.min(from.x, to.x), y: Math.min(from.y, to.y), width: Math.abs(to.x-from.x), height: Math.abs(to.y-from.y) };
		paintShape(drag.node, "rectangle", box);
		if (Math.hypot(event.clientX-drag.clientX, event.clientY-drag.clientY) < 3) return;
		const items = [...drag.base];
		const selected = new Set(items);
		for (const item of drag.page.hitIndex.queryBox(box)) {
			if (!selected.has(item)) items.push(item);
		}
		this.setSelection(items);
	}

	startShape(event) {
		const surface = event.target.closest(".page-surface") || (this.tool !== "text" && this.nearestSurface(event));
		const page = this.pages.get(surface);
		if (!page || !this.options.canInsert(page.index)) {
			return;
		}
		event.preventDefault();
		this.focus();
		const annotation = this.tool.startsWith("annotation:") ? this.tool.slice(11) : "";
		const shape = annotation ? "rectangle" : this.tool;
		const style = this.options.drawStyle();
		if (annotation === "watermark") {
			Object.assign(style, { fill: false, stroke: true, strokeColor: "var(--accent)", lineWidth: 1 });
		}
		if (lineShape(shape)) {
			style.fill = false;
			style.stroke = true;
		}
		this.drag = { shape, annotation, page, surface, ...this.createPreview(surface, page, shape === "text" ? "rectangle" : shape, style), style, pointerID: event.pointerId,
			rect: surface.getBoundingClientRect(), rotation: this.options.rotation(),
			clientX: event.clientX, clientY: event.clientY };
		if (annotation === "watermark") this.drag.node.setAttribute("vector-effect", "non-scaling-stroke");
		this.viewer.setPointerCapture(event.pointerId);
	}

	nearestSurface(event) {
		let nearest = null, distance = Infinity;
		for (const surface of this.viewer.querySelectorAll(".page-surface")) {
			if (!this.pages.has(surface)) continue;
			const rect = surface.getBoundingClientRect();
			const next = Math.hypot(Math.max(rect.left - event.clientX, 0, event.clientX - rect.right),
				Math.max(rect.top - event.clientY, 0, event.clientY - rect.bottom));
			if (next < distance) {
				nearest = surface;
				distance = next;
			}
		}
		return nearest;
	}

	createPreview(surface, page, shape, style) {
		const preview = document.createElementNS(SVG_NAMESPACE, "svg");
		preview.classList.add("edit-preview");
		preview.setAttribute("viewBox", `0 0 ${page.width} ${page.height}`);
		const node = document.createElementNS(SVG_NAMESPACE, lineShape(shape) && shape !== "line" ? "path" : shape === "rectangle" ? "rect" : shape);
		node.setAttribute("fill", style.fill ? style.fillColor : "none");
		node.setAttribute("stroke", style.stroke ? style.strokeColor : "none");
		node.setAttribute("stroke-width", style.lineWidth);
		preview.append(node);
		surface.append(preview);
		return { preview, node };
	}

	moveShape(event, drag) {
		const point = (x, y) => {
			const p = pagePoint(x, y, drag.rect, drag.page, drag.rotation);
			return drag.shape === "text" ? { x: Math.max(0, Math.min(p.x, drag.page.width)), y: Math.max(0, Math.min(p.y, drag.page.height)) } : p;
		};
		const from = point(drag.clientX, drag.clientY);
		const to = constrainedPoint(from, point(event.clientX, event.clientY), drag.shape, event.shiftKey && drag.shape !== "text");
		const x = Math.min(from.x, to.x), y = Math.min(from.y, to.y);
		const width = Math.abs(to.x - from.x), height = drag.shape === "text" ? Math.max(5, Math.abs(to.y - from.y)) : Math.abs(to.y - from.y);
		const valid = Math.hypot(event.clientX - drag.clientX, event.clientY - drag.clientY) >= 3
			&& (lineShape(drag.shape) ? width > 0 || height > 0 : width > 0 && height > 0);
		drag.box = valid ? lineShape(drag.shape)
			? { x: from.x, y: from.y, width: to.x - from.x, height: to.y - from.y }
			: { x, y, width, height } : null;
		paintShape(drag.node, drag.shape === "text" ? "rectangle" : drag.shape, lineShape(drag.shape)
			? { x: from.x, y: from.y, width: to.x - from.x, height: to.y - from.y } : { x, y, width, height });
	}

	modifierChange(event) {
		const pointer = this.drag?.pointer;
		if ((event.key === "Shift" || event.key === "Alt") && pointer) {
			this.move({ pointerId: pointer.pointerId, clientX: pointer.clientX, clientY: pointer.clientY,
				shiftKey: event.shiftKey, altKey: event.altKey, preventDefault() {} });
		}
	}

	keyUp(event) {
		this.modifierChange(event);
		if (this.nudge?.keys.delete(event.key) && !this.nudge.keys.size) return this.commitNudge();
	}

	nudgeChanged() {
		return Boolean(this.nudge && (this.nudge.x || this.nudge.y));
	}

	cancelNudge() {
		const nudge = this.nudge;
		this.nudge = null;
		if (nudge) {
			this.place(nudge.item);
			this.options.onNudgeChange?.();
		}
	}

	commitNudge() {
		if (this.nudgeCommit) return this.nudgeCommit;
		const nudge = this.nudge;
		if (!nudge) return Promise.resolve(true);
		this.nudge = null;
		this.nudgeCommit = Promise.resolve().then(() => nudge.x || nudge.y
			? this.options.onTransform(nudge.item, { x: nudge.x, y: nudge.y, scale: 1 }) : true).finally(() => {
			this.place(nudge.item);
			this.nudgeCommit = null;
			this.options.onNudgeChange?.();
		});
		return this.nudgeCommit;
	}

	keyDown(event, toolbar = false) {
		if (event.defaultPrevented || !toolbar && event.target?.closest("input, textarea, select, [contenteditable]")) return;
		if (this.crop && !this.options.busy()) {
			if (event.key === "Escape" || event.key === "Enter") {
				event.preventDefault();
				event.stopPropagation();
				if (event.key === "Enter") this.commitCrop();
				else if (this.drag) this.cancel();
				else this.closeCrop();
			}
			return;
		}
		this.modifierChange(event);
		if (event.key === "Escape" && this.drag && !this.tool && !this.options.busy()) {
			event.preventDefault();
			event.stopPropagation();
			this.cancel();
			return;
		}
		if (event.key === "Escape" && this.tool && !this.options.busy()) {
			event.preventDefault();
			event.stopPropagation();
			this.setTool("");
			return;
		}
		if (event.key === "Escape" && !this.input && this.enabled && !this.options.busy() && !this.nudge && this.selectParent()) {
			event.preventDefault();
			event.stopPropagation();
			return;
		}
		if (this.input || !this.enabled || !this.selected || this.options.busy() || this.nudgeCommit || event.ctrlKey || event.metaKey || event.altKey || event.isComposing) {
			return;
		}
		if (event.key === "Tab" && !this.drag && !this.nudge) {
			const items = this.pageItems(this.selected.index);
			if (items.length) {
				event.preventDefault();
				event.stopPropagation();
				this.select(items[(items.indexOf(this.selected) + (event.shiftKey ? -1 : 1) + items.length) % items.length]);
			}
			return;
		}
		const directions = { ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] };
		const edit = event.key === "Enter" || event.key === "F2";
		if (event.key !== "Escape" && event.key !== "Delete" && !edit && !directions[event.key]) {
			return;
		}
		event.preventDefault();
		event.stopPropagation();
		if (event.key === "Escape") {
			this.nudge ? this.cancelNudge() : this.drag ? this.cancel() : this.clear();
		} else if (event.key === "Delete" && canEditObject(this.selected, "delete")) {
			if (this.nudge) this.commitNudge().then(saved => { if (saved && this.selected) this.options.onDelete(this.selected); });
			else this.options.onDelete(this.selected);
		} else if (edit && !this.drag && !this.selected.items) {
			if (this.nudge) this.commitNudge().then(saved => { if (saved && this.selected) this.options.onEdit(this.selected); });
			else this.options.onEdit(this.selected);
		} else if (!this.drag && directions[event.key] && canEditObject(this.selected, "move")) {
			const [x, y] = directions[event.key];
			const [dx, dy] = [[x, y], [y, -x], [-x, -y], [-y, x]][this.options.rotation() / 90];
			const step = event.shiftKey ? 10 : 1;
			const nudge = this.nudge ||= { item: this.selected, x: 0, y: 0, keys: new Set() };
			nudge.keys.add(event.key);
			nudge.x += dx * step;
			nudge.y += dy * step;
			this.place(nudge.item, { x: nudge.x, y: nudge.y, scale: 1 });
			this.options.onNudgeChange?.();
		}
	}

	editText(item, face, source = null, rich = null, faces = null) {
		this.cancel();
		this.closeText();
		if (!item.selectionIDs) this.select(item);
		for (const face of faces?.values() || []) document.fonts.add(face);
		document.fonts.add(face);
		const input = document.createElement("div");
		input.className = "edit-text";
		input.setAttribute("dir", "auto");
		input.contentEditable = "true";
		input.spellcheck = false;
		for (const text of item.text.split("\n")) {
			const paragraph = document.createElement("div");
			paragraph.setAttribute("dir", "auto");
			if (text) paragraph.textContent = text;
			else paragraph.append(document.createElement("br"));
			input.append(paragraph);
		}
		const left = rich ? 0 : item.leftIndent || 0;
		const frame = { ...(rich?.frame || item.textFrame || { width: item.width, height: item.height, matrix: [1, 0, 0, 1, item.x, item.y] }) };
		if (!rich?.frame && item.paragraphWidth != null) frame.width = item.paragraphWidth;
		const [a, b, c, d, e, f] = frame.matrix;
		Object.assign(input.style, {
			left: "0px", top: "0px",
			transform: `matrix(${a},${b},${c},${d},${(e + a * left) * PX_PER_MM},${(f + b * left) * PX_PER_MM})`,
			whiteSpace: item.wrap ? "pre-wrap" : "pre",
			unicodeBidi: "plaintext",
			fontFamily: `"${face.family}"`, fontSize: `${item.size * PX_PER_MM}px`,
			lineHeight: item.paragraphHeight || item.lineHeight ? `${(item.paragraphHeight || item.lineHeight) * PX_PER_MM}px` : "normal", color: item.color,
			textAlign: item.align || "left",
			letterSpacing: `${(item.letterSpacing || 0) * PX_PER_MM}px`,
			textIndent: `${(item.firstLineIndent || 0) * PX_PER_MM}px`,
		});
		this.input = { input, item, face, faces, frame, source: Boolean(source), fontChoice: item.fontChoice };
		const editing = this.input;
		if (rich) editing.rich = new RichTextSession(this, rich.model, rich.faces, rich.fonts, rich.resources);
		if (source) {
			editing.selectionListener = () => this.paintSourceSelection();
			document.addEventListener("selectionchange", editing.selectionListener);
		}
		if (source) editing.history = { entries: [{ value: item.text, face, fontChoice: item.fontChoice }], index: 0 };
		item.artwork?.classList.add("edit-text-source");
		item.node.classList.add("edit-text-source");
		for (const member of item.selectionIDs ? item.items : []) {
			member.artwork?.classList.add("edit-text-source");
			member.node.classList.add("edit-text-source");
		}
		item.surface.append(input);
		this.fitTextInput();
		if (source) this.paintSourceText(source);
		input.addEventListener("input", () => {
			editing.rich?.readInput();
			this.recordSourceText();
			this.options.onTextChange();
			this.previewSourceText();
		});
		input.addEventListener("compositionstart", () => {
			editing.rich?.remember();
			if (editing.rich) editing.rich.beforeCaret = editing.rich.selection();
			this.rememberSourceSelection();
			editing.composing = true;
			if (editing.source) this.paintSourceDraft();
		});
		input.addEventListener("compositionend", () => {
			editing.composing = false;
			editing.rich?.readInput();
			this.recordSourceText();
			this.options.onTextChange();
			Promise.resolve().then(() => {
				if (this.input === editing && editing.commitAfterComposition) this.commitText();
				else this.previewSourceText();
			});
		});
		input.addEventListener("paste", event => {
			if (editing.rich) {
				editing.rich.paste(event).catch(error => { if (this.input === editing) this.setTextError(error); });
				return;
			}
			event.preventDefault();
			const copy = this.options.getTextClipboard ? this.options.getTextClipboard() : this.richClipboard;
			if (copy && event.clipboardData.getData(RICH_CLIPBOARD) === copy.token && event.clipboardData.getData("text/plain") === copy.text && this.options.onRichInput) {
				const data = new Map([RICH_CLIPBOARD, "text/plain"].map(key => [key, event.clipboardData.getData(key)]));
				const value = editTextValue(input), caret = editTextSelection(input, editing.range);
				Promise.resolve(this.options.onRichInput()).then(ready => {
					if (ready && this.input === editing && editing.rich.text === value && (!caret || editing.rich.selection().every((at, i) => at === caret[i])))
						return editing.rich.paste({ preventDefault() {}, clipboardData: { getData: key => data.get(key) || "" } });
				}).catch(error => { if (this.input === editing) this.setTextError(error); });
				return;
			}
			document.execCommand("insertText", false, event.clipboardData.getData("text/plain"));
		});
		input.addEventListener("beforeinput", event => {
			if (editing.rich) {
				if (event.inputType === "historyUndo" || event.inputType === "historyRedo") {
					event.preventDefault(); editing.rich.undo(event.inputType === "historyUndo" ? -1 : 1); return;
				}
				if (!editing.composing && ["deleteContentBackward", "deleteContentForward"].includes(event.inputType) && input.querySelector("br[data-text-wrap]")) {
					event.preventDefault();
					const rich = editing.rich, range = rich.selection();
					if (range[0] === range[1]) {
						const bounds = [0, ...Array.from(graphemes.segment(rich.text), part => part.index + part.segment.length)];
						if (event.inputType === "deleteContentBackward") range[0] = bounds.findLast(at => at < range[0]) ?? 0;
						else range[1] = bounds.find(at => at > range[1]) ?? rich.text.length;
					}
					rich.replace([], null, range); return;
				}
				if (!editing.composing) {
					editing.rich.remember();
					const range = event.getTargetRanges()[0];
					editing.rich.beforeCaret = range ? editTextSelection(input, range) : editing.rich.selection();
				}
			}
			if (editing.source && (event.inputType === "historyUndo" || event.inputType === "historyRedo")) {
				event.preventDefault();
				this.restoreSourceText(event.inputType === "historyUndo" ? -1 : 1);
				return;
			}
			this.rememberSourceSelection();
			if (event.inputType.startsWith("format")) event.preventDefault();
			if (event.inputType === "insertLineBreak") {
				event.preventDefault();
				document.execCommand("insertParagraph");
			}
		});
		input.addEventListener("blur", (event) => {
			editing.rich?.remember();
			const selection = document.getSelection();
			if (selection?.rangeCount && input.contains(selection.anchorNode)) editing.range = selection.getRangeAt(0).cloneRange();
			if (event.relatedTarget?.closest(".edit-object") && this.viewer.contains(event.relatedTarget)) return;
			if (!this.options.textControls?.some(control => control === event.relatedTarget || control.contains(event.relatedTarget))
				&& !this.options.keepTextFocus?.() && !this.options.busy()) this.commitText();
		});
		input.addEventListener("focus", () => {
			if (editing.range) {
				const selection = document.getSelection();
				selection.removeAllRanges();
				selection.addRange(editing.range);
				editing.range = null;
			}
		});
		input.addEventListener("keydown", (event) => {
			if (event.key === "F3" && !event.ctrlKey && !event.metaKey && !event.altKey) return;
			if ((event.ctrlKey || event.metaKey) && !event.altKey && (["s", "w", "tab"].includes(event.key.toLowerCase())
				|| !event.shiftKey && ["f", "o", "p"].includes(event.key.toLowerCase()))) return;
			event.stopPropagation();
			if (event.isComposing || editing.composing || editing.saving) {
				return;
			}
			if ((editing.rich || this.options.onTextFormat) && (event.ctrlKey || event.metaKey) && !event.altKey && ["b", "i", "u"].includes(event.key.toLowerCase())) {
				event.preventDefault();
				const key = { b: "bold", i: "italic", u: "underline" }[event.key.toLowerCase()];
				if (this.options.onTextFormat) this.options.onTextFormat(key);
				else editing.rich.apply({ [key]: editing.rich.state()[key] !== true });
			} else if ((editing.source || editing.rich) && (event.ctrlKey || event.metaKey) && !event.altKey && ["z", "y"].includes(event.key.toLowerCase())) {
				event.preventDefault();
				this.textUndo(event.key.toLowerCase() === "y" || event.shiftKey ? 1 : -1);
			} else if (event.key === "Escape") {
				event.preventDefault();
				this.closeText();
				this.focus();
			} else if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) {
				event.preventDefault();
				this.commitText();
			}
		});
		input.addEventListener("copy", event => this.copyText(event));
		input.addEventListener("cut", event => this.copyText(event, true));
		if (editing.rich) editing.rich.render();
		input.focus({ preventScroll: true });
		if (!item.draft) document.getSelection().selectAllChildren(input);
		if (editing.rich) editing.rich.selection();
		if (source) this.paintSourceSelection();
		this.options.onTextChange();
	}

	textUndo(direction) {
		if (this.input?.rich) this.input.rich.undo(direction);
		else this.restoreSourceText(direction);
	}
	copyText(event, cut = false) {
		const editing = this.input;
		if (!editing || editing.composing || editing.saving) return;
		if (editing.rich) return editing.rich.copy(event, cut);
		if (editing.item.selectionIDs) return;
		const range = editTextSelection(editing.input, editing.range);
		if (!range || range[0] === range[1]) return;
		const [start, end] = range.sort((a, b) => a - b), text = editTextValue(editing.input).slice(start, end);
		const model = richTextModel({ ...editing.item, ...editing.style, text });
		const font = editing.fontData || editing.fontResource;
		const clipboard = { token: crypto.randomUUID(), text, runs: model.runs, scope: this.options.textClipboardScope?.(),
			fonts: new Map(font ? [[editing.item.font, font]] : []), faces: new Map([[editing.item.font, editing.face]]), choices: new Map(),
			paragraphs: text.split("\n").map(() => ({ align: "left" })) };
		if (this.options.setTextClipboard) this.options.setTextClipboard(clipboard);
		else this.richClipboard = clipboard;
		event.preventDefault();
		event.clipboardData.setData("text/plain", text);
		event.clipboardData.setData(RICH_CLIPBOARD, clipboard.token);
		if (cut) document.execCommand("delete");
	}

	fitTextInput() {
		const editing = this.input;
		if (!editing || editing.source) return;
		const { input, frame, item, rich } = editing;
		const layout = rich?.model.layout || item;
		const width = Math.max(0, frame.width - (rich ? 0 : layout.leftIndent || 0) - (rich ? 0 : layout.rightIndent || 0)) * PX_PER_MM + 4;
		Object.assign(input.style, { width: rich?.model.autoWidth && !layout.wrap ? "max-content" : `${width}px`, minWidth: "4px", minHeight: "0px", height: "auto" });
	}

	setTextError(error) {
		const editing = this.input;
		if (!editing) return;
		const previous = editing.error || "";
		editing.error = error ? String(error.message || error) : "";
		if (editing.error) {
			editing.input.setAttribute("data-invalid", "true");
			editing.input.title = editing.error;
		} else {
			editing.input.removeAttribute("data-invalid");
			editing.input.removeAttribute("title");
		}
		if (editing.error || previous) this.options.onTextError?.(editing.error, previous);
	}

	promoteText(rich) {
		const editing = this.input;
		if (!editing || editing.rich || editing.composing || editing.saving) return false;
		const caret = editTextSelection(editing.input, editing.range) || [0, 0];
		const value = editTextValue(editing.input);
		const session = new RichTextSession(this, rich.model, rich.faces, rich.fonts, rich.resources);
		const base = { ...session.typing }, fonts = new Map();
		const current = { value, style: editing.style, face: editing.face, fontData: editing.fontData, fontChoice: editing.fontChoice, caret };
		const values = editing.history ? editing.history.entries.slice(0, editing.history.index + 1) : [{ value: editing.item.text }];
		for (const entry of [...values, current]) {
			const format = { ...base, ...entry.style };
			if (entry.fontData) {
				let key = fonts.get(entry.fontData);
				if (!key) {
					key = `temp-font-${crypto.randomUUID()}`; fonts.set(entry.fontData, key);
					session.imports.set(key, entry.fontData); session.faces.set(key, entry.face);
					document.fonts.add(entry.face);
					if (entry.fontChoice) session.choices.set(key, entry.fontChoice);
				}
				format.font = key;
			}
			const before = session.model.runs;
			session.model.runs = normalizeRuns([{ text: entry.value, format }]);
			session.typing = format;
			session.caret = entry.caret || caret;
			session.record(before);
		}
		if (rich.frame) {
			editing.frame = rich.frame;
			editing.item = { ...editing.item, richReflow: rich.reflow, textFrame: rich.frame };
		}
		editing.rich = session;
		editing.source = false;
		document.removeEventListener("selectionchange", editing.selectionListener);
		editing.history = null;
		editing.range = null;
		const [a, b, c, d, e, f] = editing.frame.matrix, left = 0;
		editing.input.style.transform = `matrix(${a},${b},${c},${d},${(e + a * left) * PX_PER_MM},${(f + b * left) * PX_PER_MM})`;
		session.render();
		session.preview();
		this.options.onTextChange();
		return true;
	}

	rememberSourceSelection() {
		const editing = this.input;
		if (editing?.history && !editing.composing) editing.history.entries[editing.history.index].caret = editTextSelection(editing.input);
	}

	recordSourceText() {
		const editing = this.input;
		if (!editing?.history || editing.composing) return;
		const { history, input } = editing, value = editTextValue(input);
		const previous = history.entries[history.index];
		if (previous.value === value && previous.fontData === editing.fontData && previous.style === editing.style && !!previous.reflow === !!editing.reflow) return;
		history.entries.splice(++history.index, Infinity, { value, caret: editTextSelection(input), face: editing.face,
			fontData: editing.fontData, fontChoice: editing.fontChoice, style: editing.style, reflow: editing.reflow });
	}

	restoreSourceText(direction) {
		const editing = this.input, history = editing?.history;
		if (!history || editing.composing || editing.saving) return;
		const index = history.index + direction;
		if (index < 0 || index >= history.entries.length) return;
		history.index = index;
		const entry = history.entries[index], fragment = document.createDocumentFragment(), positions = [];
		if (entry.face) {
			if (![...editing.faces?.values() || []].includes(editing.face)) document.fonts.delete(editing.face);
			document.fonts.add(entry.face);
			Object.assign(editing, { face: entry.face, fontData: entry.fontData, fontChoice: entry.fontChoice, style: entry.style, reflow: entry.reflow });
			editing.input.style.fontFamily = `"${entry.face.family}"`;
			editing.input.style.fontSize = `${(entry.style?.size || editing.item.size) * PX_PER_MM * sourceTextScale(editing)}px`;
			editing.input.style.color = entry.style?.color || editing.item.color;
		}
		let position = 0;
		for (const text of entry.value.split("\n")) {
			const paragraph = document.createElement("div");
			const node = document.createTextNode(text);
			paragraph.append(node);
			positions.push({ node, start: position, end: position + text.length });
			if (!text) paragraph.append(document.createElement("br"));
			fragment.append(paragraph);
			position += text.length + 1;
		}
		editing.input.replaceChildren(fragment);
		restoreTextSelection(positions, entry.caret);
		this.options.onTextChange();
		this.previewSourceText();
	}

	async previewSourceText() {
		const editing = this.input;
		if (!editing?.source || editing.saving) return;
		this.paintSourceDraft();
		if (editing.composing) return;
		const value = editTextValue(editing.input), font = editing.fontData;
		const size = editing.style?.size ?? (editing.item.selectionIDs ? null : editing.item.size), color = editing.style?.color ?? null, reflow = !!editing.reflow;
		if (value === "") {
			this.setTextError(null);
			return;
		}
		const current = () => this.input === editing && !editing.rich && !editing.composing && !editing.saving && editing.fontData === font
			&& (editing.style?.size ?? (editing.item.selectionIDs ? null : editing.item.size)) === size
			&& (editing.style?.color ?? null) === color && !!editing.reflow === reflow && editTextValue(editing.input) === value;
		try {
			const source = await this.options.onPreviewText(editing.item, value, font, size, reflow, color);
			if (!current()) return;
			this.setTextError(null);
			this.paintSourceText(source);
		} catch (err) {
			if (!current()) return;
			this.setTextError(err);
		}
	}

	paintSourceDraft() {
		const editing = this.input, input = editing.input;
		editing.glyphPositions = null;
		for (const span of input.querySelectorAll("[data-glyph]")) span.removeAttribute("data-glyph");
		if (editing.composing) return;
		const value = editTextValue(input), caret = editTextSelection(input, editing.range);
		const fragment = document.createDocumentFragment(), positions = [];
		let position = 0;
		for (const text of value.split("\n")) {
			const line = document.createElement("div"), node = document.createTextNode(text);
			line.append(node);
			if (!text) line.append(document.createElement("br"));
			fragment.append(line);
			positions.push({ node, start: position, end: position + text.length });
			position += text.length + 1;
		}
		input.replaceChildren(fragment);
		Object.assign(input.style, { width: "max-content", height: "auto", minWidth: value ? "0px" : `${(editing.style?.size || editing.item.size) * PX_PER_MM * sourceTextScale(editing) + 4}px` });
		restoreTextSelection(positions, caret, editing.range);
	}

	paintSourceText(source) {
		const editing = this.input, { input, item, face, frame } = editing;
		const caret = editTextSelection(input, editing.range);
		const displayScale = sourceTextScale(editing), unit = PX_PER_MM * displayScale;
		const size = (editing.style?.size || item.size) * unit;
		const context = document.createElement("canvas").getContext("2d");
		context.font = `${source.italic ? "italic" : "normal"} ${source.weight || 400} ${size}px "${face.family}"`;
		Object.assign(input.style, { fontSize: `${size}px`, lineHeight: `${size}px`, fontWeight: String(source.weight || 400), fontStyle: source.italic ? "italic" : "normal" });
		const fragment = document.createDocumentFragment(), positions = [], glyphPositions = [];
		let line, lineIndex = -1, naturalX = 0, position = 0, glyphIndex = 0;
		let left = Infinity, top = Infinity, right = -Infinity, bottom = -Infinity;
		for (const run of source.runs) {
			const runFace = editing.fontData ? face : editing.faces?.get(run.font) || face;
			const runSize = (editing.style?.size ?? run.size ?? editing.item.size) * unit;
			const weight = (run.weight ?? source.weight) || 400, italic = run.italic ?? source.italic;
			const scale = (run.scale ?? source.scale) || 1, vscale = (run.vscale ?? source.vscale) || 1;
			const rotation = run.rotation ?? source.rotation ?? 0, angle = rotation * Math.PI / 180;
			const cos = Math.cos(angle), sin = Math.sin(angle);
			context.font = `${italic ? "italic" : "normal"} ${weight} ${runSize}px "${runFace.family}"`;
			const metrics = context.measureText("Mg"), baseline = metrics.fontBoundingBoxAscent, lineHeight = baseline + metrics.fontBoundingBoxDescent;
			if (!line || run.line) {
				if (line) position++;
				line = document.createElement("div");
				line.style.height = `${size}px`;
				fragment.append(line);
				lineIndex++;
				naturalX = 0;
			}
			let x = run.x * unit, y = run.y * unit, index = 0;
			if (!run.text) {
				left = Math.min(left, x);
				top = Math.min(top, y - baseline);
				right = Math.max(right, x);
				bottom = Math.max(bottom, y + lineHeight - baseline);
			}
			const chars = [...run.text];
			while (index < chars.length) {
				const count = source.glyphCounts?.[glyphIndex] || 1;
				const char = chars.slice(index, index + count).join("");
				x = run.positions[index][0] * unit;
				y = run.positions[index][1] * unit;
				const span = document.createElement("span"), node = document.createTextNode(char);
				span.append(node);
				const glyph = source.glyphs?.[glyphIndex];
				if (glyph) {
					span.dataset.glyph = glyph;
					glyphPositions.push({ span, start: position, end: position + char.length });
				}
				Object.assign(span.style, { display: "inline-block", verticalAlign: "top", fontFamily: `"${runFace.family}"`, fontSize: `${runSize}px`, lineHeight: `${lineHeight}px`,
					fontWeight: String(weight), fontStyle: italic ? "italic" : "normal", color: editing.style?.color ?? run.color ?? item.color,
					opacity: String((run.alpha ?? item.alpha ?? 255) / 255), transformOrigin: `left ${baseline}px`,
					transform: `translate(${x - naturalX}px,${y - baseline - lineIndex * size}px) rotate(${rotation}deg) scale(${scale},${vscale})` });
				line.append(span);
				positions.push({ node, start: position, end: position + char.length });
				position += char.length;
				const advance = context.measureText(glyph || char).width;
				if (glyph) {
					const naturalAdvance = [...char].reduce((width, value) => width + context.measureText(value).width, 0);
					span.style.width = `${advance}px`;
					span.style.letterSpacing = `${(advance - naturalAdvance) / count}px`;
				}
				for (const dx of [0, advance * scale]) {
					for (const dy of [-baseline * vscale, (lineHeight - baseline) * vscale]) {
						const px = x + dx * cos - dy * sin, py = y + dx * sin + dy * cos;
						left = Math.min(left, px);
						top = Math.min(top, py);
						right = Math.max(right, px);
						bottom = Math.max(bottom, py);
					}
				}
				naturalX += advance;
				index += count;
				glyphIndex += count;
			}
			if (!line.hasChildNodes()) {
				const node = document.createTextNode("");
				line.append(node, document.createElement("br"));
				positions.push({ node, start: position, end: position });
			}
		}
		if (!Number.isFinite(left)) { left = top = 0; right = bottom = size; }
		for (const line of fragment.children) line.style.transform = `translate(${-left}px,${-top}px)`;
		input.replaceChildren(fragment);
		const [a, b, c, d, e, f] = frame.matrix.map((value, index) => index < 4 ? value / displayScale : value);
		Object.assign(input.style, {
			width: `${right - left + 4}px`, minWidth: "0px",
			minHeight: "0px", height: `${bottom - top + 4}px`,
			transform: `matrix(${a},${b},${c},${d},${e * PX_PER_MM + a * left + c * top},${f * PX_PER_MM + b * left + d * top})`,
		});
		restoreTextSelection(positions, caret, editing.range);
		editing.glyphPositions = glyphPositions;
		this.paintSourceSelection();
	}

	paintSourceSelection() {
		const editing = this.input;
		if (!editing?.glyphPositions) return;
		const caret = editTextSelection(editing.input, editing.range), start = caret ? Math.min(...caret) : 0, end = caret ? Math.max(...caret) : 0;
		for (const glyph of editing.glyphPositions) glyph.span.toggleAttribute("data-selected", glyph.start < end && glyph.end > start);
	}

	setTextFont(face, data, key) {
		const editing = this.input;
		if (editing.rich) { editing.rich.setFont(face, data, key); return; }
		document.fonts.add(face);
		if (![...editing.faces?.values() || []].includes(editing.face)) document.fonts.delete(editing.face);
		editing.face = face;
		editing.fontData = data;
		editing.input.style.fontFamily = `"${face.family}"`;
		this.recordSourceText();
		this.options.onTextChange();
		editing.input.focus({ preventScroll: true });
		this.previewSourceText();
	}

	setTextStyle(values) {
		const editing = this.input;
		if (editing.rich) { editing.rich.apply(values); return; }
		const style = { ...editing.style, ...values };
		for (const key of Object.keys(style)) if (!editing.item.selectionIDs && style[key] === editing.item[key]) delete style[key];
		editing.style = Object.keys(style).length ? style : null;
		const item = { ...editing.item, ...editing.style };
		editing.input.style.fontSize = `${item.size * PX_PER_MM * sourceTextScale(editing)}px`;
		editing.input.style.color = item.color;
		this.recordSourceText();
		this.fitTextInput();
		this.options.onTextChange();
		if ("size" in values || editing.item.selectionIDs) this.previewSourceText();
	}

	textChanged() {
		const editing = this.input;
		if (!editing) return false;
		if (editing.rich) return Boolean(editing.refreshPending) || (!editing.item.draft || Boolean(editing.rich.text.trim())) && editing.rich.changed;
		const value = editTextValue(editing.input);
		return (!editing.item.draft || Boolean(value.trim()))
			&& (value !== editing.item.text || Boolean(editing.fontData || editing.style || editing.reflow));
	}

	async commitText() {
		const editing = this.input;
		if (!editing) return true;
		if (editing.saving || editing.preparingCommit) return false;
		if (editing.composing) {
			editing.commitAfterComposition = true;
			return false;
		}
		editing.commitAfterComposition = false;
		const preparation = this.options.prepareTextCommit?.();
		if (preparation) {
			editing.preparingCommit = true;
			try { await preparation; }
			finally { editing.preparingCommit = false; }
			if (this.input !== editing) return !this.input;
		}
		const value = editTextValue(editing.input);
		if (!this.textChanged()) {
			this.closeText();
			return true;
		}
		editing.saving = true;
		editing.input.contentEditable = "false";
		let saved = false;
		try {
			saved = value === "" && !editing.item.draft && !editing.item.selectionIDs ? await this.options.onDelete(editing.item)
				: editing.rich ? await this.options.onCommitRichText(editing.item, editing.rich.model, editing.rich.fonts)
				: await this.options.onCommitText(editing.style ? { ...editing.item, ...editing.style } : editing.item, value, editing.fontData, editing.style?.color ?? null, !!editing.reflow);
		} catch (error) {
			if (this.input === editing) this.setTextError(error);
		}
		if (this.input !== editing) {
			return Boolean(saved);
		}
		if (saved) {
			this.closeText();
		} else {
			editing.saving = false;
			editing.input.contentEditable = "true";
			editing.input.focus({ preventScroll: true });
		}
		return Boolean(saved);
	}

	closeText() {
		const editing = this.input;
		this.setTextError(null);
		this.input = null;
		if (editing) {
			if (editing.selectionListener) document.removeEventListener("selectionchange", editing.selectionListener);
			editing.rich?.destroy();
			editing.input.remove();
			editing.item.artwork?.classList.remove("edit-text-source");
			editing.item.node.classList.remove("edit-text-source");
			for (const member of editing.item.selectionIDs ? editing.item.items : []) {
				member.artwork?.classList.remove("edit-text-source");
				member.node.classList.remove("edit-text-source");
			}
			for (const face of editing.faces?.values() || []) document.fonts.delete(face);
			document.fonts.delete(editing.face);
			if (editing.item.selectionIDs && this.selected === editing.item) this.setSelection(editing.item.items);
			if (editing.item.draft && this.selected === editing.item) this.select(null);
			this.options.onTextChange();
		}
	}

	startCrop(item, pagePreview) {
		this.cancel();
		this.closeCrop();
		this.setTool("");
		const bounds = item.imageBounds;
		const x = Math.max(item.x, bounds.x), y = Math.max(item.y, bounds.y);
		const box = { x, y, width: Math.min(item.x + item.width, bounds.x + bounds.width) - x,
			height: Math.min(item.y + item.height, bounds.y + bounds.height) - y };
		const original = item.surface.querySelector(".ofd-svg");
		original.replaceWith(pagePreview.svg);
		const { preview, node: mask } = this.createPreview(item.surface, item.page, "path", { fill: true, fillColor: "rgba(255,255,255,0.72)" });
		mask.setAttribute("fill-rule", "evenodd");
		const node = document.createElement("div");
		node.className = "edit-layer edit-crop";
		for (const corner of ["nw", "n", "ne", "e", "se", "s", "sw", "w"]) {
			const handle = document.createElement("span");
			handle.className = `edit-handle edit-${corner}`;
			handle.dataset.corner = corner;
			node.append(handle);
		}
		item.surface.append(node);
		item.surface.classList.add("cropping");
		this.crop = { item, box, initialBox: { ...box }, node, mask, preview, original, pagePreview };
		this.paintCrop();
		this.focus();
	}

	paintCrop() {
		const { item, box, node, mask } = this.crop;
		const rectangle = b => `M${b.x} ${b.y}h${b.width}v${b.height}h${-b.width}Z`;
		mask.setAttribute("d", rectangle(item.imageBounds) + rectangle(box));
		Object.assign(node.style, { left: `${box.x * PX_PER_MM}px`, top: `${box.y * PX_PER_MM}px`, width: `${box.width * PX_PER_MM}px`, height: `${box.height * PX_PER_MM}px` });
		this.options.onCropChange();
	}

	cropChanged() {
		return Boolean(this.crop && ["x", "y", "width", "height"].some(key => this.crop.box[key] !== this.crop.initialBox[key]));
	}

	async commitCrop() {
		if (!this.crop) return true;
		if (this.options.busy()) return false;
		if (!this.cropChanged()) {
			this.closeCrop();
			return true;
		}
		const crop = this.crop;
		const saved = await this.options.onCrop(crop.item, crop.box);
		if (saved && this.crop === crop) this.closeCrop();
		return Boolean(saved);
	}

	closeCrop(keepPreview = false) {
		if (!this.crop) return;
		if (this.drag?.crop) this.cancel();
		const crop = this.crop;
		this.crop = null;
		crop.node.remove();
		crop.preview.remove();
		crop.pagePreview.svg.replaceWith(crop.original);
		if (!keepPreview) crop.pagePreview.urls.forEach(url => URL.revokeObjectURL(url));
		crop.item.surface.classList.remove("cropping");
		this.options.onCropChange();
	}
}
