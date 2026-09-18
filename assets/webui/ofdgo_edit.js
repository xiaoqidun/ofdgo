const PX_PER_MM = 96 / 25.4;

export function editTextValue(input) {
	let text = "", previous;
	for (const node of input.childNodes) {
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
	const x = Math.min(...items.map(item => item.x)), y = Math.min(...items.map(item => item.y));
	return { x, y, width: Math.max(...items.map(item => item.x + item.width)) - x,
		height: Math.max(...items.map(item => item.y + item.height)) - y };
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

function alignmentSnap(box, targets, tolerance) {
	const lines = [];
	const offset = { x: 0, y: 0 };
	for (const [axis, size, cross, span] of [["x", "width", "y", "height"], ["y", "height", "x", "width"]]) {
		let distance = Infinity, line;
		for (const target of targets) {
			for (const anchor of [0, 0.5, 1]) {
				const position = target[axis] + target[size] * anchor;
				for (const edge of [0, 0.5, 1]) {
					const delta = position - box[axis] - box[size] * edge;
					if (Math.abs(delta) > tolerance[axis] || Math.abs(delta) >= distance) continue;
					distance = Math.abs(delta);
					offset[axis] = delta;
					const from = Math.min(box[cross], target[cross]);
					const to = Math.max(box[cross] + box[span], target[cross] + target[span]);
					line = axis === "x" ? `M${position} ${from}V${to}` : `M${from} ${position}H${to}`;
				}
			}
		}
		if (line) lines.push(line);
	}
	return { ...offset, path: lines.join("") };
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
	const objects = item.items || [item];
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
	return [...new Set(reasons.filter(Boolean))].join("；");
}

export function selectedText(item) {
	if (!item?.items) return item?.type === "TextObject" ? item : null;
	if (!item.items.every(member => member.type === "TextObject")) return null;
	const text = { ...item, type: "TextObject" };
	for (const key of ["font", "fontName", "size", "color"]) {
		const value = item.items[0][key];
		text[key] = item.items.every(member => member[key] === value) ? value : undefined;
	}
	return text;
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
			if (!event.altKey && this.enabled && item && !item.items && !this.input && !this.options.busy()) {
				this.options.onEdit(item);
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
		const selected = pending ? this.pendingSelection.ids || page.objects.slice(-1).map(item => item.id)
			: this.items().filter(item => item.index === index).map(item => item.id);
		const selection = [];
		this.pages.set(surface, { index, width: page.width, height: page.height });
		const layer = document.createElement("div");
		layer.className = "edit-layer";
		const artwork = new Map([...surface.querySelectorAll("[data-ofd-object], [data-ofd-child]")].map((node, order) => [node.getAttribute("data-ofd-object") || node.getAttribute("data-ofd-child"), { node, order }]));
		const objects = [...page.objects].sort((a, b) => (artwork.get(a.id)?.order ?? 0) - (artwork.get(b.id)?.order ?? 0));
		for (const object of objects) {
			const node = document.createElement("div");
			node.className = "edit-object";
			node.classList.toggle("read-only", !canEditObject(object, "move"));
			node.title = objectEditReason(object);
			if (lineShape(object.shape)) {
				node.classList.add("edit-line");
			}
			node.tabIndex = 0;
			node.setAttribute("role", "button");
			node.setAttribute("aria-label", { ImageObject: "图片对象", TextObject: "文字对象", PathObject: "图形对象", CompositeObject: "复合对象", CompositeGraphicUnit: "复合对象", Annotation: "注解对象" }[object.type]);
			const item = { ...object, index, page: { width: page.width, height: page.height }, node, surface };
			item.artwork = artwork.get(object.id)?.node;
			if (object.note) {
				const icon = document.createElement("span");
				icon.className = "edit-note-icon";
				icon.setAttribute("aria-hidden", "true");
				icon.textContent = "\u24d8";
				node.append(icon);
			}
			if (object.type === "PathObject" && (object.shape || object.outline || object.contours?.length)) {
				node.classList.add("edit-contour");
				const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
				svg.classList.add("edit-outline");
				svg.setAttribute("aria-hidden", "true");
				svg.setAttribute("preserveAspectRatio", "none");
				let contour;
				if (object.shape || object.oriented || !object.contours?.length) {
					const shape = object.shape || object.oriented?.kind;
					contour = document.createElementNS("http://www.w3.org/2000/svg", shape === "line" ? "line" : shape === "ellipse" ? "ellipse" : shape === "rectangle" ? "rect" : "path");
					if (!shape) contour.setAttribute("d", object.outline);
					svg.append(contour);
				} else {
					for (const path of object.contours) {
						const contour = document.createElementNS("http://www.w3.org/2000/svg", "path");
						contour.setAttribute("d", path.path);
						svg.append(contour);
					}
				}
				node.append(svg);
				item.contour = { svg, path: contour };
			}
			this.nodes.set(node, item);
			node.addEventListener("focus", () => this.select(item));
			const handles = !canEditObject(object, "transform") ? [] : lineShape(object.shape) ? ["start", "end"]
				: object.shape || object.oriented || canEditObject(object, "stretch") ? ["nw", "n", "ne", "e", "se", "s", "sw", "w"]
					: object.type === "TextObject" && canEditObject(object, "reflow") && canEditObject(object, "layoutKnown")
						? ["nw", "n", "ne", "e", "se", "s", "sw", "w"] : ["nw", "ne", "sw", "se"];
			for (const corner of handles) {
				const handle = document.createElement("span");
				handle.className = `edit-handle edit-${corner}`;
				handle.dataset.corner = corner;
				node.append(handle);
			}
			this.place(item);
			layer.append(node);
			if (selected.includes(object.id)) {
				selection.push(item);
			}
		}
		surface.append(layer);
		if (selected.length) this.setSelection(selection);
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
		if (item.textFrame) {
			const { matrix: [a, b, c, d, e, f], width, height } = item.textFrame;
			for (const handle of item.node.children) {
				if (handle.dataset.corner !== "w" && handle.dataset.corner !== "e") continue;
				const x = handle.dataset.corner === "e" ? width : 0;
				handle.style.left = `${(a * x + c * height / 2 + e - item.x) * change.scale * PX_PER_MM}px`;
				handle.style.top = `${(b * x + d * height / 2 + f - item.y) * change.scale * PX_PER_MM}px`;
				handle.style.cursor = a === 0 ? "var(--resize-vertical, ns-resize)" : "var(--resize-horizontal, ew-resize)";
			}
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

	setSelection(items) {
		this.cancelNudge();
		const previous = this.selected;
		if (this.crop && (items.length !== 1 || items[0] !== this.crop.item)) this.closeCrop();
		if (this.selected?.items) this.selected.node.remove();
		for (const item of this.items()) {
			item.node.classList.remove("selected", "multi-selected");
			item.node.setAttribute("aria-pressed", "false");
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
		}
		for (const item of items) {
			item.node.classList.add("selected");
			item.node.classList.toggle("multi-selected", items.length > 1);
			item.node.setAttribute("aria-pressed", "true");
		}
		this.options.onSelect(this.selected, previous);
	}

	select(item, add = false) {
		if (!add || !item || this.selected && item.index !== this.selected.index) {
			this.setSelection(item ? [item] : []);
			return;
		}
		const items = this.items();
		this.setSelection(items.includes(item) ? items.filter(member => member !== item) : [...items, item]);
	}

	clear() {
		this.closeText();
		this.closeCrop();
		this.cancel();
		this.pendingSelection = null;
		this.select(null);
	}

	start(event) {
		if (this.nudge) {
			this.commitNudge();
			return;
		}
		if (this.input) {
			if (!this.input.input.contains(event.target)) this.commitText();
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
			if (this.tool.startsWith("erase-")) this.startErase(event);
			else if (this.tool.startsWith("pen-")) this.startInk(event);
			else this.startShape(event);
			return;
		}
		if (event.altKey && !event.shiftKey && !event.ctrlKey && !event.metaKey && !event.target.dataset.corner) {
			this.cycleSelection(event);
			return;
		}
		let node = event.target.closest(".edit-object, .edit-selection");
		const direct = this.nodes.get(node);
		const target = event.target.dataset.corner || direct && !direct.contours ? direct : this.itemsAt(event)[0];
		if (node || target) event.preventDefault();
		node = target?.node;
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
		if (!this.drag.corner) {
			const selected = this.items();
			this.drag.targets = [...item.surface.querySelectorAll(".edit-object")]
				.map(node => this.nodes.get(node)).filter(member => !selected.includes(member));
			this.drag.targets.push({ x: 0, y: 0, ...item.page });
		}
		if (lineShape(item.shape) && this.drag.corner) {
			Object.assign(this.drag, this.createPreview(item.surface, item.page, item.shape, {
				fill: false, stroke: true, strokeColor: "var(--accent)", lineWidth: item.lineWidth,
			}));
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

	itemsAt(event) {
		const surface = event.target.closest(".page-surface");
		const page = this.pages.get(surface);
		if (!page) return [];
		const rect = surface.getBoundingClientRect(), rotation = this.options.rotation();
		const point = pagePoint(event.clientX, event.clientY, rect, page, rotation);
		const tolerance = 3 * page.width / (rotation % 180 ? rect.height : rect.width);
		return [...surface.querySelectorAll(".edit-object")].map(node => this.nodes.get(node)).reverse().filter(item => this.contains(item, point, tolerance));
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
			return;
		}
		if (!drag.change && !drag.box && Math.hypot(event.clientX - drag.clientX, event.clientY - drag.clientY) < 3) {
			return;
		}
		const from = drag.origin ||= pagePoint(drag.clientX, drag.clientY, drag.rect, drag.item.page, drag.rotation);
		const to = pagePoint(event.clientX, event.clientY, drag.rect, drag.item.page, drag.rotation);
		if (drag.item.type === "TextObject" && (drag.corner === "w" || drag.corner === "e")) {
			if (drag.item.textFrame) {
				const frame = drag.item.textFrame, [a, b, c, d] = frame.matrix;
				const dx = (d * (to.x - from.x) - c * (to.y - from.y)) / (a * d - b * c);
				const box = reshapeBox({ geometry: { x: 0, y: 0, width: frame.width, height: frame.height } }, dx, 0, drag.corner, false);
				drag.box = { x: drag.item.x + box.x, width: box.width };
				const bounds = transformedBox(box, frame.matrix);
				Object.assign(drag.item.node.style, { left: `${bounds.x * PX_PER_MM}px`, top: `${bounds.y * PX_PER_MM}px`,
					width: `${bounds.width * PX_PER_MM}px`, height: `${bounds.height * PX_PER_MM}px` });
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
			this.placeShape(drag.item, drag.box || drag.item.geometry);
			if (drag.node) paintShape(drag.node, drag.item.shape, drag.box || drag.item.geometry);
			return;
		}
		const transform = drag.item.type === "TextObject" ? textTransform : objectTransform;
		drag.change = transform(drag.item, to.x - from.x, to.y - from.y, drag.corner);
		if (!drag.corner) {
			drag.axis = event.shiftKey ? Math.abs(drag.change.x) >= Math.abs(drag.change.y) ? "x" : "y" : "";
			if (drag.axis) drag.change[drag.axis === "x" ? "y" : "x"] = 0;
			this.snap(drag, event.altKey);
			this.autoScroll(drag);
		}
		this.place(drag.item, drag.change);
	}

	scroll() {
		const drag = this.drag;
		if (!drag?.item || drag.corner || !drag.pointer) {
			this.cancel();
			return;
		}
		drag.origin ||= pagePoint(drag.clientX, drag.clientY, drag.rect, drag.item.page, drag.rotation);
		drag.rect = drag.item.surface.getBoundingClientRect();
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
			viewer.scrollLeft += speed(pointer.clientX, rect.left, viewer.clientWidth) * elapsed;
			viewer.scrollTop += speed(pointer.clientY, rect.top, viewer.clientHeight) * elapsed;
			if (viewer.scrollLeft !== left || viewer.scrollTop !== top) this.scroll();
			else drag.scrollTime = null;
		});
	}

	snap(drag, bypass = false) {
		const { item, rect, rotation, change } = drag;
		const { x, y, path } = bypass ? { x: 0, y: 0, path: "" } : alignmentSnap({ ...item, x: item.x + change.x, y: item.y + change.y }, drag.targets, {
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
			this.viewer.releasePointerCapture(drag.pointerID);
			const geometry = drag.item.oriented?.box || drag.item.geometry || (drag.item.textFrame ? { x: drag.item.x, width: drag.item.textFrame.width } : drag.item);
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
			drag.guides?.preview.remove();
			this.viewer.releasePointerCapture(drag.pointerID);
			Promise.resolve(this.options.onTransform(drag.item, drag.change)).finally(() => this.place(drag.item));
		} else this.cancel();
	}

	cancel() {
		const drag = this.drag;
		this.drag = null;
		if (drag) {
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
		this.drag = { erase: this.tool, page, surface, items: [...surface.querySelectorAll(".edit-object")].map(node => this.nodes.get(node)),
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
		const from = pagePoint(drag.clientX, drag.clientY, drag.rect, drag.page, drag.rotation);
		const to = pagePoint(event.clientX, event.clientY, drag.rect, drag.page, drag.rotation);
		const box = { x: Math.min(from.x, to.x), y: Math.min(from.y, to.y), width: Math.abs(to.x-from.x), height: Math.abs(to.y-from.y) };
		paintShape(drag.node, "rectangle", box);
		if (Math.hypot(event.clientX-drag.clientX, event.clientY-drag.clientY) < 3) return;
		const items = [...drag.base];
		for (const node of drag.surface.querySelectorAll(".edit-object")) {
			const item = this.nodes.get(node);
			if (!items.includes(item) && item.x >= box.x && item.x+item.width <= box.x+box.width && item.y >= box.y && item.y+item.height <= box.y+box.height) {
				items.push(item);
			}
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
		const preview = document.createElementNS("http://www.w3.org/2000/svg", "svg");
		preview.classList.add("edit-preview");
		preview.setAttribute("viewBox", `0 0 ${page.width} ${page.height}`);
		const node = document.createElementNS("http://www.w3.org/2000/svg", lineShape(shape) && shape !== "line" ? "path" : shape === "rectangle" ? "rect" : shape);
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

	keyDown(event) {
		if (event.defaultPrevented || event.target?.closest("input, textarea, select, [contenteditable]")) return;
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
		if (event.key === "Escape" && !this.input && this.enabled && !this.options.busy() && !this.nudge && this.options.onExitScope?.()) {
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

	editText(item, face, source = null) {
		this.cancel();
		this.closeText();
		this.select(item);
		document.fonts.add(face);
		const input = document.createElement("div");
		input.className = "edit-text";
		input.setAttribute("aria-label", "编辑文字");
		input.setAttribute("role", "textbox");
		input.setAttribute("aria-multiline", "true");
		input.contentEditable = "true";
		input.spellcheck = false;
		for (const text of item.text.split("\n")) {
			const paragraph = document.createElement("div");
			if (text) paragraph.textContent = text;
			else paragraph.append(document.createElement("br"));
			input.append(paragraph);
		}
		const left = item.leftIndent || 0, right = item.rightIndent || 0;
		const frame = item.textFrame || { width: item.width, height: item.height, matrix: [1, 0, 0, 1, item.x, item.y] };
		const [a, b, c, d, e, f] = frame.matrix;
		const autoSize = !source && !item.wrap && (!item.align || item.align === "left");
		Object.assign(input.style, {
			left: "0px", top: "0px",
			width: autoSize ? "max-content" : `${(frame.width - left - right) * PX_PER_MM + 4}px`,
			minWidth: autoSize ? "calc(1em + 4px)" : "0px",
			minHeight: autoSize ? "0px" : `${frame.height * PX_PER_MM + 4}px`,
			transform: `matrix(${a},${b},${c},${d},${(e + a * left) * PX_PER_MM},${(f + b * left) * PX_PER_MM})`,
			whiteSpace: item.wrap ? "pre-wrap" : "pre",
			fontFamily: `"${face.family}"`, fontSize: `${item.size * PX_PER_MM}px`,
			lineHeight: item.paragraphHeight || item.lineHeight ? `${(item.paragraphHeight || item.lineHeight) * PX_PER_MM}px` : "normal", color: item.color,
			textAlign: item.align || "left",
			letterSpacing: `${(item.letterSpacing || 0) * PX_PER_MM}px`,
			textIndent: `${(item.firstLineIndent || 0) * PX_PER_MM}px`,
		});
		this.input = { input, item, face, frame, source: Boolean(source), fontChoice: item.fontChoice };
		const editing = this.input;
		if (source) editing.history = { entries: [{ value: item.text }], index: 0 };
		item.artwork?.classList.add("edit-text-source");
		item.node.classList.add("edit-text-source");
		item.surface.append(input);
		if (source) this.paintSourceText(source);
		input.addEventListener("input", () => {
			this.recordSourceText();
			this.options.onTextChange();
			this.previewSourceText();
		});
		input.addEventListener("compositionstart", () => {
			this.rememberSourceSelection();
			editing.composing = true;
		});
		input.addEventListener("compositionend", () => {
			editing.composing = false;
			this.recordSourceText();
			Promise.resolve().then(() => {
				if (this.input === editing && editing.commitAfterComposition) this.commitText();
				else this.previewSourceText();
			});
		});
		input.addEventListener("paste", event => {
			event.preventDefault();
			document.execCommand("insertText", false, event.clipboardData.getData("text/plain"));
		});
		input.addEventListener("beforeinput", event => {
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
			const selection = document.getSelection();
			if (selection?.rangeCount && input.contains(selection.anchorNode)) editing.range = selection.getRangeAt(0).cloneRange();
			if (!this.options.textControls?.includes(event.relatedTarget)) this.commitText();
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
			if ((event.ctrlKey || event.metaKey) && !event.altKey && !event.shiftKey && event.key.toLowerCase() === "s") return;
			event.stopPropagation();
			if (event.isComposing || editing.composing || editing.saving) {
				return;
			}
			if (editing.source && (event.ctrlKey || event.metaKey) && !event.altKey && ["z", "y"].includes(event.key.toLowerCase())) {
				event.preventDefault();
				this.restoreSourceText(event.key.toLowerCase() === "y" || event.shiftKey ? 1 : -1);
			} else if (event.key === "Escape") {
				event.preventDefault();
				this.closeText();
				this.focus();
			} else if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) {
				event.preventDefault();
				this.commitText();
			}
		});
		input.focus({ preventScroll: true });
		if (!item.draft) document.getSelection().selectAllChildren(input);
	}

	rememberSourceSelection() {
		const editing = this.input;
		if (editing?.history && !editing.composing) editing.history.entries[editing.history.index].caret = editTextSelection(editing.input);
	}

	recordSourceText() {
		const editing = this.input;
		if (!editing?.history || editing.composing) return;
		const { history, input } = editing, value = editTextValue(input);
		if (history.entries[history.index].value === value) return;
		history.entries.splice(++history.index, Infinity, { value, caret: editTextSelection(input) });
	}

	restoreSourceText(direction) {
		const editing = this.input, history = editing?.history;
		if (!history || editing.composing || editing.saving) return;
		const index = history.index + direction;
		if (index < 0 || index >= history.entries.length) return;
		history.index = index;
		const entry = history.entries[index], fragment = document.createDocumentFragment(), positions = [];
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
		if (!editing?.source || editing.composing || editing.saving) return;
		const value = editTextValue(editing.input), font = editing.fontData, size = editing.style?.size || editing.item.size;
		const current = () => this.input === editing && !editing.composing && !editing.saving && editing.fontData === font
			&& (editing.style?.size || editing.item.size) === size && editTextValue(editing.input) === value;
		try {
			const source = await this.options.onPreviewText(editing.item, value, font, size);
			if (!current()) return;
			editing.input.removeAttribute("aria-invalid");
			editing.input.removeAttribute("title");
			this.paintSourceText(source);
		} catch (err) {
			if (!current()) return;
			editing.input.setAttribute("aria-invalid", "true");
			editing.input.title = err.message;
		}
	}

	paintSourceText(source) {
		const editing = this.input, { input, item, face, frame } = editing;
		const caret = editTextSelection(input, editing.range);
		const size = (editing.style?.size || item.size) * PX_PER_MM;
		const context = document.createElement("canvas").getContext("2d");
		context.font = `${source.italic ? "italic" : "normal"} ${source.weight || 400} ${size}px "${face.family}"`;
		const metrics = context.measureText("Mg"), baseline = (size + metrics.fontBoundingBoxAscent - metrics.fontBoundingBoxDescent) / 2;
		Object.assign(input.style, { lineHeight: `${size}px`, fontWeight: String(source.weight || 400), fontStyle: source.italic ? "italic" : "normal" });
		const fragment = document.createDocumentFragment(), positions = [];
		let line, lineIndex = -1, naturalX = 0, position = 0;
		let left = Infinity, top = Infinity, right = -Infinity, bottom = -Infinity;
		for (const run of source.runs) {
			if (!line || run.line) {
				if (line) position++;
				line = document.createElement("div");
				fragment.append(line);
				lineIndex++;
				naturalX = 0;
			}
			let x = run.x * PX_PER_MM, y = run.y * PX_PER_MM, index = 0;
			if (!run.text) {
				left = Math.min(left, x);
				top = Math.min(top, y - baseline);
				right = Math.max(right, x);
				bottom = Math.max(bottom, y + size - baseline);
			}
			for (const char of run.text) {
				const span = document.createElement("span"), node = document.createTextNode(char);
				span.append(node);
				Object.assign(span.style, { display: "inline-block", opacity: String((item.alpha ?? 255) / 255), transformOrigin: "left top", transform: `translate(${x - naturalX}px,${y - baseline - lineIndex * size}px) scaleX(${source.scale})` });
				line.append(span);
				positions.push({ node, start: position, end: position + char.length });
				position += char.length;
				const advance = context.measureText(char).width;
				left = Math.min(left, x);
				top = Math.min(top, y - baseline);
				right = Math.max(right, x + advance * source.scale);
				bottom = Math.max(bottom, y + size - baseline);
				naturalX += advance;
				x += run.dx?.length ? run.dx[Math.min(index, run.dx.length - 1)] * PX_PER_MM : run.dy?.length ? 0 : advance * source.scale;
				y += run.dy?.length ? run.dy[Math.min(index, run.dy.length - 1)] * PX_PER_MM : 0;
				index++;
			}
			if (!line.hasChildNodes()) {
				const node = document.createTextNode("");
				line.append(node, document.createElement("br"));
				positions.push({ node, start: position, end: position });
			}
		}
		for (const line of fragment.children) line.style.transform = `translate(${-left}px,${-top}px)`;
		input.replaceChildren(fragment);
		const [a, b, c, d, e, f] = frame.matrix;
		Object.assign(input.style, {
			width: `${Math.max(size, right - left) + 4}px`,
			minHeight: "0px", height: `${bottom - top + 4}px`,
			transform: `matrix(${a},${b},${c},${d},${e * PX_PER_MM + a * left + c * top},${f * PX_PER_MM + b * left + d * top})`,
		});
		restoreTextSelection(positions, caret, editing.range);
	}

	setTextFont(face, data) {
		const editing = this.input;
		document.fonts.add(face);
		document.fonts.delete(editing.face);
		editing.face = face;
		editing.fontData = data;
		editing.input.style.fontFamily = `"${face.family}"`;
		this.options.onTextChange();
		editing.input.focus({ preventScroll: true });
		this.previewSourceText();
	}

	setTextStyle(values) {
		const editing = this.input;
		const style = { ...editing.style, ...values };
		for (const key of Object.keys(style)) if (style[key] === editing.item[key]) delete style[key];
		editing.style = Object.keys(style).length ? style : null;
		const item = { ...editing.item, ...editing.style };
		editing.input.style.fontSize = `${item.size * PX_PER_MM}px`;
		editing.input.style.color = item.color;
		this.options.onTextChange();
		if ("size" in values) this.previewSourceText();
	}

	textChanged() {
		const editing = this.input;
		if (!editing) return false;
		const value = editTextValue(editing.input);
		return (!editing.item.draft || Boolean(value.trim()))
			&& (value !== editing.item.text || Boolean(editing.fontData || editing.style));
	}

	async commitText() {
		const editing = this.input;
		if (!editing) return true;
		if (editing.saving) return false;
		if (editing.composing) {
			editing.commitAfterComposition = true;
			return false;
		}
		editing.commitAfterComposition = false;
		const value = editTextValue(editing.input);
		if (!this.textChanged()) {
			this.closeText();
			return true;
		}
		editing.saving = true;
		editing.input.contentEditable = "false";
		const saved = await this.options.onCommitText(editing.style ? { ...editing.item, ...editing.style } : editing.item, value, editing.fontData, editing.style?.color ?? null);
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
		this.input = null;
		if (editing) {
			editing.input.remove();
			editing.item.artwork?.classList.remove("edit-text-source");
			editing.item.node.classList.remove("edit-text-source");
			document.fonts.delete(editing.face);
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
