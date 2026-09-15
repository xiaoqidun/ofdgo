const PX_PER_MM = 96 / 25.4;

export function pagePoint(x, y, rect, page, rotation) {
	const u = (x - rect.left) / rect.width;
	const v = (y - rect.top) / rect.height;
	const points = [[u, v], [v, 1 - u], [1 - u, 1 - v], [1 - v, u]];
	const point = points[rotation / 90];
	return { x: point[0] * page.width, y: point[1] * page.height };
}

export function objectTransform(box, page, dx, dy, corner = "") {
	if (!corner) {
		return {
			x: Math.max(-box.x, Math.min(dx, Math.max(-box.x, page.width - box.x - box.width))),
			y: Math.max(-box.y, Math.min(dy, Math.max(-box.y, page.height - box.y - box.height))),
			scale: 1,
		};
	}
	const left = corner.includes("w");
	const top = corner.includes("n");
	const x = left ? -dx : dx;
	const y = top ? -dy : dy;
	const limit = Math.min(
		(left ? box.x + box.width : page.width - box.x) / box.width,
		(top ? box.y + box.height : page.height - box.y) / box.height,
	);
	const scale = Math.max(Math.min(1 / Math.min(box.width, box.height), limit), Math.min(limit,
		1 + (x * box.width + y * box.height) / (box.width ** 2 + box.height ** 2)));
	return { x: left ? box.width * (1 - scale) : 0, y: top ? box.height * (1 - scale) : 0, scale };
}

export class CanvasEditor {
	constructor(viewer, options) {
		this.viewer = viewer;
		this.options = options;
		this.enabled = false;
		this.selected = null;
		this.nodes = new WeakMap();
		viewer.addEventListener("pointerdown", (event) => this.start(event));
		viewer.addEventListener("pointermove", (event) => this.move(event));
		viewer.addEventListener("pointerup", (event) => this.end(event));
		viewer.addEventListener("pointercancel", () => this.cancel());
		viewer.addEventListener("lostpointercapture", () => this.cancel());
		viewer.addEventListener("keydown", (event) => this.keyDown(event));
		viewer.addEventListener("dblclick", () => {
			const item = this.selected;
			if (this.enabled && item && !item.image && !this.options.busy()) {
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
			this.clear();
		}
	}

	mount(index, page, surface) {
		const layer = document.createElement("div");
		layer.className = "edit-layer";
		for (const object of page.objects) {
			const node = document.createElement("div");
			node.className = "edit-object";
			node.tabIndex = 0;
			node.setAttribute("role", "button");
			node.setAttribute("aria-label", object.image ? "图片对象" : "文字对象");
			const item = { ...object, index, page: { width: page.width, height: page.height }, node, surface };
			this.nodes.set(node, item);
			node.addEventListener("focus", () => this.select(item));
			this.place(item);
			if (object.image) {
				for (const corner of ["nw", "ne", "sw", "se"]) {
					const handle = document.createElement("span");
					handle.className = `edit-handle edit-${corner}`;
					handle.dataset.corner = corner;
					node.append(handle);
				}
			}
			layer.append(node);
			if (this.selected?.index === index && this.selected.id === object.id) {
				this.select(item);
			}
		}
		surface.append(layer);
		if (this.selectLast && layer.lastElementChild) {
			this.selectLast = false;
			this.select(this.nodes.get(layer.lastElementChild));
		}
	}

	place(item, change = { x: 0, y: 0, scale: 1 }) {
		Object.assign(item.node.style, {
			left: `${(item.x + change.x) * PX_PER_MM}px`, top: `${(item.y + change.y) * PX_PER_MM}px`,
			width: `${item.width * change.scale * PX_PER_MM}px`, height: `${item.height * change.scale * PX_PER_MM}px`,
		});
	}

	select(item) {
		this.selected?.node.classList.remove("selected");
		this.selected = item;
		item?.node.classList.add("selected");
		this.options.onSelect(item);
	}

	clear() {
		this.cancel();
		this.selectLast = false;
		this.select(null);
	}

	start(event) {
		if (!this.enabled || this.options.busy() || event.button !== 0 || this.drag) {
			return;
		}
		const node = event.target.closest(".edit-object");
		if (!node) {
			this.select(null);
			return;
		}
		event.preventDefault();
		this.select(this.nodes.get(node));
		this.viewer.focus({ preventScroll: true });
		const item = this.selected;
		this.drag = {
			item, pointerID: event.pointerId, corner: event.target.dataset.corner || "",
			rect: item.surface.getBoundingClientRect(), rotation: this.options.rotation(),
			clientX: event.clientX, clientY: event.clientY,
		};
		this.viewer.setPointerCapture(event.pointerId);
	}

	move(event) {
		const drag = this.drag;
		if (!drag || drag.pointerID !== event.pointerId) {
			return;
		}
		event.preventDefault();
		if (!drag.change && Math.hypot(event.clientX - drag.clientX, event.clientY - drag.clientY) < 3) {
			return;
		}
		const from = pagePoint(drag.clientX, drag.clientY, drag.rect, drag.item.page, drag.rotation);
		const to = pagePoint(event.clientX, event.clientY, drag.rect, drag.item.page, drag.rotation);
		drag.change = objectTransform(drag.item, drag.item.page, to.x - from.x, to.y - from.y, drag.corner);
		this.place(drag.item, drag.change);
	}

	end(event) {
		const drag = this.drag;
		if (!drag || drag.pointerID !== event.pointerId) {
			return;
		}
		this.move(event);
		this.cancel();
		if (drag.change && (drag.change.x || drag.change.y || drag.change.scale !== 1)) {
			this.options.onTransform(drag.item, drag.change);
		}
	}

	cancel() {
		const drag = this.drag;
		this.drag = null;
		if (drag) {
			this.place(drag.item);
			if (this.viewer.hasPointerCapture(drag.pointerID)) {
				this.viewer.releasePointerCapture(drag.pointerID);
			}
		}
	}

	keyDown(event) {
		if (!this.enabled || !this.selected || this.options.busy() || event.ctrlKey || event.metaKey || event.altKey || event.isComposing) {
			return;
		}
		const directions = { ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] };
		const edit = (event.key === "Enter" || event.key === "F2") && !this.selected.image;
		if (event.key !== "Escape" && event.key !== "Delete" && !edit && !directions[event.key]) {
			return;
		}
		event.preventDefault();
		event.stopPropagation();
		if (event.key === "Escape") {
			this.drag ? this.cancel() : this.clear();
		} else if (event.key === "Delete") {
			this.options.onDelete(this.selected);
		} else if (edit && !this.drag) {
			this.options.onEdit(this.selected);
		} else if (!this.drag) {
			const [x, y] = directions[event.key];
			const [dx, dy] = [[x, y], [y, -x], [-x, -y], [-y, x]][this.options.rotation() / 90];
			const step = event.shiftKey ? 10 : 1;
			const change = objectTransform(this.selected, this.selected.page,
				dx * step, dy * step);
			if (change.x || change.y) {
				this.options.onTransform(this.selected, change);
			}
		}
	}
}
