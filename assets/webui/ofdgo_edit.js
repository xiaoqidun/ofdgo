const PX_PER_MM = 96 / 25.4;

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

// selectionBounds keeps same-page objects together without changing their relative positions.
export function selectionBounds(items) {
	const x = Math.min(...items.map(item => item.x)), y = Math.min(...items.map(item => item.y));
	return { x, y, width: Math.max(...items.map(item => item.x + item.width)) - x,
		height: Math.max(...items.map(item => item.y + item.height)) - y };
}

// constrainedPoint preserves shape constraints without changing off-page geometry.
export function constrainedPoint(from, to, shape, shift) {
	let dx = to.x - from.x, dy = to.y - from.y;
	if (shift && shape === "line") {
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

// reshapeBox moves line endpoints or resizes basic shapes without scaling their stroke.
export function reshapeBox(item, dx, dy, handle, shift) {
	const box = item.geometry;
	if (!dx && !dy && !shift) return { ...box };
	if (item.shape === "line") {
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

// paintShape uses the same page-space geometry for creation and endpoint previews.
function paintShape(node, shape, box) {
	const { x, y, width, height } = box;
	const attributes = shape === "line" ? { x1: x, y1: y, x2: x + width, y2: y + height }
		: shape === "ellipse" ? { cx: x + width / 2, cy: y + height / 2, rx: width / 2, ry: height / 2 }
		: { x, y, width, height };
	for (const [key, value] of Object.entries(attributes)) {
		node.setAttribute(key, value);
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
		viewer.addEventListener("lostpointercapture", () => this.cancel());
		viewer.addEventListener("keydown", (event) => this.keyDown(event));
		viewer.addEventListener("keyup", (event) => this.modifierChange(event));
		viewer.addEventListener("dblclick", () => {
			const item = this.selected;
			if (this.enabled && item && !item.items && !this.input && !this.options.busy()) {
				this.options.onEdit(item);
			}
		});
		viewer.addEventListener("scroll", () => this.cancel());
		window.addEventListener("blur", () => this.cancel());
		window.addEventListener("resize", () => this.cancel());
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
		const pending = this.pendingSelection?.index === index;
		const selected = pending ? this.pendingSelection.ids || page.objects.slice(-1).map(item => item.id)
			: this.items().filter(item => item.index === index).map(item => item.id);
		const selection = [];
		this.pages.set(surface, { index, width: page.width, height: page.height });
		const layer = document.createElement("div");
		layer.className = "edit-layer";
		for (const object of page.objects) {
			const node = document.createElement("div");
			node.className = "edit-object";
			if (object.shape === "line") {
				node.classList.add("edit-line");
			}
			node.tabIndex = 0;
			node.setAttribute("role", "button");
			node.setAttribute("aria-label", { ImageObject: "图片对象", TextObject: "文字对象", PathObject: "图形对象" }[object.type]);
			const item = { ...object, index, page: { width: page.width, height: page.height }, node, surface };
			this.nodes.set(node, item);
			node.addEventListener("focus", () => this.select(item));
			const handles = object.shape === "line" ? ["start", "end"]
				: object.shape ? ["nw", "n", "ne", "e", "se", "s", "sw", "w"]
					: object.type === "TextObject" ? ["nw", "ne", "e", "se", "sw", "w"] : ["nw", "ne", "sw", "se"];
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
	}

	place(item, change = { x: 0, y: 0, scale: 1 }) {
		if (item.items) {
			for (const member of item.items) this.place(member, change);
			return;
		}
		if (item.shape) {
			this.placeShape(item, { x: item.geometry.x + change.x, y: item.geometry.y + change.y,
				width: item.geometry.width * change.scale, height: item.geometry.height * change.scale });
			return;
		}
		Object.assign(item.node.style, {
			left: `${(item.x + change.x) * PX_PER_MM}px`, top: `${(item.y + change.y) * PX_PER_MM}px`,
			width: `${item.width * change.scale * PX_PER_MM}px`, height: `${item.height * change.scale * PX_PER_MM}px`,
		});
	}

	// placeShape positions basic-shape handles at their geometry rather than the padded boundary.
	placeShape(item, box) {
		const x = Math.min(box.x, box.x + box.width), y = Math.min(box.y, box.y + box.height);
		Object.assign(item.node.style, {
			left: `${x * PX_PER_MM}px`, top: `${y * PX_PER_MM}px`,
			width: `${Math.abs(box.width) * PX_PER_MM}px`, height: `${Math.abs(box.height) * PX_PER_MM}px`,
		});
		if (item.shape === "line") {
			for (const handle of item.node.children) {
				const end = handle.dataset.corner === "end";
				handle.style.left = `${(box.x + (end ? box.width : 0) - x) * PX_PER_MM}px`;
				handle.style.top = `${(box.y + (end ? box.height : 0) - y) * PX_PER_MM}px`;
			}
		}
	}

	// items returns the current selection without treating a group as a document object.
	items() {
		return this.selected?.items || (this.selected ? [this.selected] : []);
	}

	// setSelection updates object highlights and exposes one same-page selection to the toolbar.
	setSelection(items) {
		for (const item of this.items()) {
			item.node.classList.remove("selected", "multi-selected");
			item.node.setAttribute("aria-pressed", "false");
		}
		this.selected = items.length < 2 ? items[0] || null : { ...selectionBounds(items), items,
			id: items.map(item => item.id), index: items[0].index, page: items[0].page, surface: items[0].surface };
		for (const item of items) {
			item.node.classList.add("selected");
			item.node.classList.toggle("multi-selected", items.length > 1);
			item.node.setAttribute("aria-pressed", "true");
		}
		this.options.onSelect(this.selected);
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
		this.cancel();
		this.pendingSelection = null;
		this.select(null);
	}

	start(event) {
		if (this.input) {
			if (event.target !== this.input.input) this.commitText();
			return;
		}
		if (!this.enabled || this.options.busy() || event.button !== 0 || this.drag) {
			return;
		}
		if (this.tool) {
			this.startShape(event);
			return;
		}
		const node = event.target.closest(".edit-object");
		if (node) {
			event.preventDefault();
		}
		this.viewer.focus({ preventScroll: true });
		const target = node ? this.nodes.get(node) : null;
		const additive = event.shiftKey || event.ctrlKey || event.metaKey || this.multiple;
		if (!target) {
			this.startMarquee(event, additive);
			return;
		}
		if (additive && !event.target.dataset.corner) {
			this.select(target, true);
			return;
		}
		if (!this.items().includes(target)) this.select(target);
		if (!node || this.options.busy()) {
			return;
		}
		const item = this.selected;
		this.drag = {
			item, pointerID: event.pointerId, corner: item.items ? "" : event.target.dataset.corner || "",
			rect: item.surface.getBoundingClientRect(), rotation: this.options.rotation(),
			clientX: event.clientX, clientY: event.clientY,
		};
		if (item.shape === "line" && this.drag.corner) {
			Object.assign(this.drag, this.createPreview(item.surface, item.page, item.shape, {
				fill: false, stroke: true, strokeColor: "var(--accent)", lineWidth: item.lineWidth,
			}));
		}
		this.viewer.setPointerCapture(event.pointerId);
	}

	move(event) {
		const drag = this.drag;
		if (!drag || drag.pointerID !== event.pointerId) {
			return;
		}
		event.preventDefault();
		drag.pointer = event;
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
		const from = pagePoint(drag.clientX, drag.clientY, drag.rect, drag.item.page, drag.rotation);
		const to = pagePoint(event.clientX, event.clientY, drag.rect, drag.item.page, drag.rotation);
		if (drag.item.type === "TextObject" && (drag.corner === "w" || drag.corner === "e")) {
			drag.box = reshapeBox({ geometry: drag.item }, to.x - from.x, 0, drag.corner, false);
			Object.assign(drag.item.node.style, { left: `${drag.box.x * PX_PER_MM}px`, width: `${drag.box.width * PX_PER_MM}px` });
			return;
		}
		if (drag.item.shape && drag.corner) {
			drag.box = reshapeBox(drag.item, to.x - from.x, to.y - from.y, drag.corner, event.shiftKey);
			this.placeShape(drag.item, drag.box || drag.item.geometry);
			if (drag.node) paintShape(drag.node, drag.item.shape, drag.box || drag.item.geometry);
			return;
		}
		drag.change = objectTransform(drag.item, to.x - from.x, to.y - from.y, drag.corner);
		this.place(drag.item, drag.change);
	}

	end(event) {
		const drag = this.drag;
		if (!drag || drag.pointerID !== event.pointerId) {
			return;
		}
		this.move(event);
		if (drag.marquee) {
			this.drag = null;
			drag.preview.remove();
			this.viewer.releasePointerCapture(drag.pointerID);
			return;
		}
		if (drag.shape) {
			this.drag = null;
			this.viewer.releasePointerCapture(drag.pointerID);
			if (drag.shape === "text") {
				const from = pagePoint(drag.clientX, drag.clientY, drag.rect, drag.page, drag.rotation);
				const x = Math.max(0, Math.min(from.x, drag.page.width - 1));
				const y = Math.max(0, Math.min(from.y, drag.page.height - 1));
				const box = drag.box || { x, y, width: Math.min(80, drag.page.width - x), height: 5 };
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
		if ((drag.item.shape || drag.item.type === "TextObject") && drag.corner && drag.box) {
			this.drag = null;
			this.viewer.releasePointerCapture(drag.pointerID);
			const geometry = drag.item.geometry || drag.item;
			const changed = Object.keys(drag.box).some((key) => drag.box[key] !== geometry[key]);
			const apply = drag.item.shape ? this.options.onReshape : this.options.onTextWidth;
			Promise.resolve(changed && apply(drag.item, drag.box)).finally(() => {
				drag.preview?.remove();
				this.place(drag.item);
			});
			return;
		}
		this.cancel();
		if (drag.change && (drag.change.x || drag.change.y || drag.change.scale !== 1)) {
			this.options.onTransform(drag.item, drag.change);
		}
	}

	cancel() {
		const drag = this.drag;
		this.drag = null;
		if (drag) {
			drag.preview?.remove();
			if (drag.marquee) this.setSelection(drag.before);
			if (drag.item) {
				this.place(drag.item);
			}
			if (this.viewer.hasPointerCapture(drag.pointerID)) {
				this.viewer.releasePointerCapture(drag.pointerID);
			}
		}
	}

	// startMarquee selects by page-space geometry, so rotation and zoom use the same hit tests.
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

	// moveMarquee includes intersecting objects while preserving an additive selection.
	moveMarquee(event, drag) {
		const from = pagePoint(drag.clientX, drag.clientY, drag.rect, drag.page, drag.rotation);
		const to = pagePoint(event.clientX, event.clientY, drag.rect, drag.page, drag.rotation);
		const box = { x: Math.min(from.x, to.x), y: Math.min(from.y, to.y), width: Math.abs(to.x-from.x), height: Math.abs(to.y-from.y) };
		paintShape(drag.node, "rectangle", box);
		if (Math.hypot(event.clientX-drag.clientX, event.clientY-drag.clientY) < 3) return;
		const items = [...drag.base];
		for (const node of drag.surface.querySelectorAll(".edit-object")) {
			const item = this.nodes.get(node);
			if (!items.includes(item) && item.x <= box.x+box.width && item.x+item.width >= box.x && item.y <= box.y+box.height && item.y+item.height >= box.y) {
				items.push(item);
			}
		}
		this.setSelection(items);
	}

	startShape(event) {
		const surface = event.target.closest(".page-surface") || (this.tool !== "text" && this.nearestSurface(event));
		const page = this.pages.get(surface);
		if (!page) {
			return;
		}
		event.preventDefault();
		this.viewer.focus({ preventScroll: true });
		const shape = this.tool;
		const style = this.options.drawStyle();
		if (shape === "line") {
			style.fill = false;
			style.stroke = true;
		}
		this.drag = { shape, page, surface, ...this.createPreview(surface, page, shape === "text" ? "rectangle" : shape, style), style, pointerID: event.pointerId,
			rect: surface.getBoundingClientRect(), rotation: this.options.rotation(),
			clientX: event.clientX, clientY: event.clientY };
		this.viewer.setPointerCapture(event.pointerId);
	}

	// nearestSurface assigns a pasteboard gesture to one mounted page for its entire duration.
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

	// createPreview provides a temporary SVG overlay without rerendering the page during a drag.
	createPreview(surface, page, shape, style) {
		const preview = document.createElementNS("http://www.w3.org/2000/svg", "svg");
		preview.classList.add("edit-preview");
		preview.setAttribute("viewBox", `0 0 ${page.width} ${page.height}`);
		const node = document.createElementNS("http://www.w3.org/2000/svg", shape === "rectangle" ? "rect" : shape);
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
			&& (drag.shape === "line" ? width > 0 || height > 0 : width > 0 && height > 0);
		drag.box = valid ? drag.shape === "line"
			? { x: from.x, y: from.y, width: to.x - from.x, height: to.y - from.y }
			: { x, y, width, height } : null;
		paintShape(drag.node, drag.shape === "text" ? "rectangle" : drag.shape, drag.shape === "line"
			? { x: from.x, y: from.y, width: to.x - from.x, height: to.y - from.y } : { x, y, width, height });
	}

	// modifierChange refreshes the preview even when Shift changes without pointer movement.
	modifierChange(event) {
		const pointer = this.drag?.pointer;
		if (event.key === "Shift" && pointer) {
			this.move({ pointerId: pointer.pointerId, clientX: pointer.clientX, clientY: pointer.clientY,
				shiftKey: event.shiftKey, preventDefault() {} });
		}
	}

	keyDown(event) {
		if (event.defaultPrevented || event.target?.closest("input, textarea, select, [contenteditable]")) return;
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
		if (this.input || !this.enabled || !this.selected || this.options.busy() || event.ctrlKey || event.metaKey || event.altKey || event.isComposing) {
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
			this.drag ? this.cancel() : this.clear();
		} else if (event.key === "Delete") {
			this.options.onDelete(this.selected);
		} else if (edit && !this.drag && !this.selected.items) {
			this.options.onEdit(this.selected);
		} else if (!this.drag && directions[event.key]) {
			const [x, y] = directions[event.key];
			const [dx, dy] = [[x, y], [y, -x], [-x, -y], [-y, x]][this.options.rotation() / 90];
			const step = event.shiftKey ? 10 : 1;
			this.options.onTransform(this.selected, { x: dx * step, y: dy * step, scale: 1 });
		}
	}

	editText(item, face) {
		this.cancel();
		this.closeText();
		this.select(item);
		document.fonts.add(face);
		const input = document.createElement("textarea");
		input.className = "edit-text";
		input.setAttribute("aria-label", "编辑文字");
		input.wrap = item.wrap ? "soft" : "off";
		input.spellcheck = false;
		input.value = item.text;
		Object.assign(input.style, {
			left: `${item.x * PX_PER_MM}px`, top: `${item.y * PX_PER_MM}px`,
			width: `${(item.wrap ? item.width : Math.max(item.width, Math.min(50, item.page.width - item.x))) * PX_PER_MM + 4}px`,
			minHeight: `${item.height * PX_PER_MM + 4}px`,
			fontFamily: `"${face.family}"`, fontSize: `${item.size * PX_PER_MM}px`,
			lineHeight: item.paragraphHeight || item.lineHeight ? `${(item.paragraphHeight || item.lineHeight) * PX_PER_MM}px` : "normal", color: item.color,
			textAlign: item.align || "left",
		});
		this.input = { input, item, face };
		item.surface.append(input);
		const resize = () => {
			input.style.height = "0px";
			input.style.height = `${input.scrollHeight + 2}px`;
		};
		input.addEventListener("input", () => {
			resize();
			this.options.onTextChange();
		});
		input.addEventListener("blur", () => this.commitText());
		input.addEventListener("keydown", (event) => {
			event.stopPropagation();
			if (event.isComposing || input.readOnly) {
				return;
			}
			if (event.key === "Escape") {
				event.preventDefault();
				this.closeText();
				this.viewer.focus({ preventScroll: true });
			} else if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) {
				event.preventDefault();
				this.commitText();
			}
		});
		resize();
		input.focus({ preventScroll: true });
		if (!item.draft) input.select();
	}

	async commitText() {
		const editing = this.input;
		if (!editing) return true;
		if (editing.input.readOnly) return false;
		if (editing.input.value === editing.item.text || editing.item.draft && !editing.input.value.trim()) {
			this.closeText();
			return true;
		}
		editing.input.readOnly = true;
		const saved = await this.options.onCommitText(editing.item, editing.input.value);
		if (this.input !== editing) {
			return Boolean(saved);
		}
		if (saved) {
			this.closeText();
		} else {
			editing.input.readOnly = false;
			editing.input.focus({ preventScroll: true });
		}
		return Boolean(saved);
	}

	closeText() {
		const editing = this.input;
		this.input = null;
		if (editing) {
			editing.input.remove();
			document.fonts.delete(editing.face);
			if (editing.item.draft && this.selected === editing.item) this.select(null);
			this.options.onTextChange();
		}
	}
}
