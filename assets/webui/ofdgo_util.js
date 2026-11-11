export class VirtualList {
	constructor(container, render) {
		this.container = container;
		this.render = render;
		this.rows = new Map();
		this.content = document.createElement("div");
		this.content.style.position = "relative";
		this.observer = new ResizeObserver(() => {
			if (this.frame) return;
			this.frame = requestAnimationFrame(() => { this.frame = 0; this.measure(); });
		});
		this.observer.observe(container);
		container.addEventListener("scroll", () => {
			if (!this.view) return;
			this.view.top = container.scrollTop;
			this.update();
		});
		container.addEventListener("keydown", event => {
			if (event.key !== "Tab" || event.ctrlKey || event.altKey || event.metaKey || !this.view) return;
			const row = event.target.closest("[data-list-index]");
			if (!row) return;
			const buttons = [...row.querySelectorAll("button:not(:disabled)")];
			if (event.target !== buttons[event.shiftKey ? 0 : buttons.length - 1]) return;
			const index = Number(row.dataset.listIndex) + (event.shiftKey ? -1 : 1);
			if (index < 0 || index >= this.view.items.length) return;
			event.preventDefault();
			this.update(index);
			const next = this.rows.get(index).querySelectorAll("button:not(:disabled)");
			next[event.shiftKey ? next.length - 1 : 0]?.focus();
		});
	}

	set(view) {
		if (view && view === this.view) {
			if (this.offsets.length !== view.items.length + 1) this.layout(this.offsets.length <= view.items.length ? this.offsets.length - 1 : 0);
			this.measure();
			return;
		}
		if (this.view) this.view.top = this.container.scrollTop;
		for (const row of this.rows.values()) this.observer.unobserve(row);
		this.rows.clear();
		this.content.replaceChildren();
		this.view = view;
		if (!view) { this.container.replaceChildren(); return; }
		this.container.replaceChildren(this.content);
		view.heights ||= new Map();
		this.layout();
		this.container.scrollTop = view.top || 0;
		this.update();
	}

	layout(start = 0) {
		const view = this.view;
		if (!start) this.offsets = [0];
		for (let index = start; index < view.items.length; index++) this.offsets.push(this.offsets.at(-1) + (view.heights.get(view.items[index]) || 100));
		this.content.style.height = `${this.offsets.at(-1)}px`;
		for (const [index, row] of this.rows) row.style.top = `${this.offsets[index]}px`;
	}

	index(top) {
		let low = 0, high = this.view.items.length;
		while (low < high) {
			const middle = (low + high) >>> 1;
			if (this.offsets[middle + 1] <= top) low = middle + 1;
			else high = middle;
		}
		return low;
	}

	measure() {
		const view = this.view, width = this.container.clientWidth;
		if (!view || !width || !this.container.clientHeight) return;
		const anchor = this.index(this.container.scrollTop), offset = this.container.scrollTop - this.offsets[anchor];
		let changed = view.width !== width;
		if (changed) { view.width = width; view.heights.clear(); }
		for (const [index, row] of this.rows) {
			const height = parseFloat(getComputedStyle(row).height), item = view.items[index];
			if (view.heights.get(item) !== height) { view.heights.set(item, height); changed = true; }
		}
		if (changed) {
			this.layout();
			this.container.scrollTop = this.offsets[anchor] + offset;
			view.top = this.container.scrollTop;
		}
		this.update();
	}

	update(extra) {
		const view = this.view;
		if (!view || !this.container.clientHeight) return;
		const top = this.container.scrollTop, height = this.container.clientHeight;
		const first = this.index(Math.max(0, top - height)), last = Math.min(view.items.length, this.index(top + height * 2) + 1);
		const indices = new Set();
		for (let index = first; index < last; index++) indices.add(index);
		const active = document.activeElement;
		for (const [index, row] of this.rows) if (row.contains(active)) indices.add(index);
		if (extra !== undefined) indices.add(extra);
		for (const [index, row] of this.rows) if (!indices.has(index)) {
			this.observer.unobserve(row);
			row.remove();
			this.rows.delete(index);
		}
		let position = 0;
		for (const index of [...indices].sort((a, b) => a - b)) {
			let row = this.rows.get(index);
			if (!row) {
				row = this.render(view.items[index]);
				row.dataset.listIndex = index;
				Object.assign(row.style, {position: "absolute", top: `${this.offsets[index]}px`, left: "0", right: "0"});
				this.rows.set(index, row);
				this.observer.observe(row);
			}
			if (this.content.children[position] !== row) this.content.insertBefore(row, this.content.children[position] || null);
			position++;
		}
		if (active?.isConnected && this.content.contains(active) && document.activeElement !== active) active.focus({preventScroll: true});
	}
}

export function setInputFiles(input, files) {
	const view = input.ownerDocument.defaultView, transfer = new view.DataTransfer();
	const native = files.some(file => !(file instanceof view.File));
	for (const file of files) transfer.items.add(file instanceof view.File ? file : new view.File([], file.name, {type: file.type}));
	delete input.files;
	input.files = transfer.files;
	if (native) Object.defineProperty(input, "files", {configurable: true, get: () => input.value ? files : []});
}

export class DocumentDialogs {
	constructor({ document: root = document, targets = [], titlebar = null } = {}) {
		Object.assign(this, { document: root, targets, titlebar, owner: null, records: new Map(), tabs: new Map(), released: new WeakSet(), inert: new Map(), styles: new Map() });
		this.backdrop = root.createElement("div");
		this.backdrop.className = "document-dialog-backdrop";
		this.backdrop.hidden = true;
		Object.assign(this.backdrop.style, { position: "fixed", inset: "0", background: "var(--backdrop)", zIndex: "1000" });
		root.body.append(this.backdrop);
		root.addEventListener("keydown", event => {
			if (event.defaultPrevented || this.applicationOpen()) return;
			const panel = this.current()?.stack.at(-1);
			if (!panel) return;
			if (event.key === "Tab" && !event.ctrlKey && !event.altKey && !event.metaKey) {
				const selector = "button, input, select, textarea, a[href], [tabindex], [contenteditable=true]";
				const elements = [this.titlebar, panel].filter(Boolean).flatMap(root => [...root.querySelectorAll(selector)])
					.filter(node => node.tabIndex >= 0 && !node.disabled && !node.closest("[inert]") && node.getClientRects().length);
				const index = elements.indexOf(root.activeElement), direction = event.shiftKey ? -1 : 1;
				const target = index < 0 ? event.shiftKey ? elements.at(-1) : elements[0] : elements[(index + direction + elements.length) % elements.length];
				event.preventDefault();
				if (target) target.focus();
				else panel.focus();
				return;
			}
			if (event.key !== "Escape") return;
			event.preventDefault();
			event.stopPropagation();
			this.cancel(panel);
		}, true);
		this.backdrop.addEventListener("pointerdown", event => { event.preventDefault(); this.focus(); });
		root.defaultView.addEventListener("resize", () => this.paint());
	}

	register(panel, { shared = true, persistent = false } = {}) {
		if (this.records.has(panel)) return panel;
		const record = { show: panel.show.bind(panel), showModal: panel.showModal.bind(panel), close: panel.close.bind(panel), owner: null, application: false, shared, clicks: new WeakMap(), initial: shared && !persistent ? this.snapshot(panel) : null };
		this.records.set(panel, record);
		panel.showModal = panel.show = () => this.openFor(this.owner, panel);
		panel.close = value => {
			if (record.application) record.close(value);
			else this.closeFor(this.owner, panel, value);
		};
		panel.requestClose = value => {
			if (record.application) {
				const event = new this.document.defaultView.Event("cancel", { cancelable: true });
				if (panel.dispatchEvent(event)) record.close(value);
			} else this.cancel(panel, value);
		};
		panel.addEventListener("close", () => {
			if (record.application) { record.application = false; this.paint(); }
		});
		panel.addEventListener("click", event => { record.clicks.set(event, this.owner); }, true);
		panel.addEventListener("click", event => {
			if (!record.application && (record.clicks.get(event) !== this.owner || !this.current()?.stack.includes(panel))) event.preventDefault();
		});
		panel.addEventListener("submit", event => {
			if (record.application || event.defaultPrevented) return;
			const method = event.submitter?.getAttribute("formmethod") || event.target.getAttribute("method");
			if (method?.toLowerCase() !== "dialog") return;
			event.preventDefault();
			this.closeFor(this.owner, panel, event.submitter?.value || "");
		});
		return panel;
	}

	current(tab = this.owner) {
		return tab ? this.tabs.get(tab) : null;
	}

	entry(tab, panel) {
		if (!tab || this.released.has(tab)) throw new Error("Document dialog owner is unavailable");
		this.register(panel);
		if (!this.tabs.has(tab)) this.tabs.set(tab, { entries: new Map(), stack: [] });
		const entries = this.tabs.get(tab).entries;
		if (!entries.has(panel)) entries.set(panel, { snapshot: this.records.get(panel).initial, setup: [], updates: [], resolve: null, promise: null });
		return entries.get(panel);
	}

	setOwner(tab) {
		if (this.owner === tab) return;
		if (this.owner) this.suspend(this.owner);
		if (tab) this.restore(tab);
	}

	openFor(tab, panel, setup) {
		const entry = this.entry(tab, panel);
		if (setup) entry.setup.push(setup);
		const state = this.tabs.get(tab);
		state.stack = state.stack.filter(value => value !== panel);
		state.stack.push(panel);
		if (tab !== this.owner) return panel;
		this.unpaint();
		this.records.get(panel).owner = tab;
		this.prepare(entry, panel);
		if (!this.records.get(panel).shared) this.document.body.append(panel);
		if (!panel.open) this.records.get(panel).show();
		this.paint();
		this.focus();
		return panel;
	}

	request(panel, tab = this.owner, setup) {
		if (!tab || this.released.has(tab)) return Promise.resolve(null);
		const entry = this.entry(tab, panel);
		if (entry.promise) return entry.promise;
		entry.promise = new Promise(resolve => { entry.resolve = resolve; });
		const promise = entry.promise;
		this.openFor(tab, panel, setup);
		return promise;
	}

	updateFor(tab, panel, setup) {
		if (!tab || this.released.has(tab)) return false;
		const entry = this.current(tab)?.entries.get(panel);
		if (!entry) return false;
		if (tab === this.owner && this.records.get(panel).owner === tab) setup(panel);
		else entry.updates.push(setup);
		return true;
	}

	closeFor(tab, panel, value) {
		const state = this.current(tab), entry = state?.entries.get(panel);
		if (!state?.stack.includes(panel)) return;
		const visible = tab === this.owner;
		if (visible) {
			this.unpaint();
			if (value !== undefined) panel.returnValue = String(value);
			panel.removeAttribute("open");
			if (this.records.get(panel).initial) entry.snapshot = this.snapshot(panel);
		}
		const result = value === undefined ? visible ? panel.returnValue : entry.snapshot?.returnValue ?? panel.returnValue : String(value);
		state.stack = state.stack.filter(value => value !== panel);
		const resolve = entry.resolve;
		entry.resolve = entry.promise = null;
		entry.setup = [];
		if (visible) {
			this.paint();
			panel.dispatchEvent(new this.document.defaultView.Event("close"));
			this.focus();
		}
		resolve?.(result);
	}

	cancel(panel, value) {
		const event = new this.document.defaultView.Event("cancel", { cancelable: true });
		if (panel.dispatchEvent(event)) this.closeFor(this.owner, panel, value);
	}

	suspend(tab) {
		if (tab !== this.owner) return;
		this.unpaint();
		for (const [panel, entry] of this.current(tab)?.entries || []) {
			const record = this.records.get(panel);
			if (record.application || record.owner !== tab) continue;
			if (record.initial) entry.snapshot = this.snapshot(panel);
			else if (!record.shared) {
				entry.focus = panel.contains(this.document.activeElement) ? this.document.activeElement : null;
				panel.remove();
			}
			panel.removeAttribute("open");
		}
		this.owner = null;
		this.paint();
	}

	restore(tab) {
		if (!tab || this.released.has(tab)) return false;
		if (this.owner === tab) return true;
		if (this.owner) this.suspend(this.owner);
		if (!this.tabs.has(tab)) this.tabs.set(tab, { entries: new Map(), stack: [] });
		this.owner = tab;
		const state = this.current(tab);
		for (const [panel, entry] of state.entries) {
			const record = this.records.get(panel);
			if (record.application) continue;
			if (record.initial) this.applySnapshot(entry.snapshot, panel);
			else if (!record.shared) this.document.body.append(panel);
			record.owner = tab;
		}
		for (const [panel, entry] of state.entries) {
			if (this.records.get(panel).application) continue;
			for (const update of entry.updates.splice(0)) update(panel);
		}
		for (const panel of state.stack) {
			this.prepare(state.entries.get(panel), panel);
			if (!panel.open) this.records.get(panel).show();
		}
		this.paint();
		this.focus(true);
		return true;
	}

	release(tab) {
		if (!tab || this.released.has(tab)) return;
		const visible = this.owner === tab;
		if (visible) this.suspend(tab);
		const state = this.current(tab);
		this.tabs.delete(tab);
		this.released.add(tab);
		for (const entry of state?.entries.values() || []) entry.resolve?.(null);
		for (const [panel] of state?.entries || []) {
			const record = this.records.get(panel);
			if (!record.shared) {
				panel.remove();
				this.records.delete(panel);
				continue;
			}
			if (record.application || record.owner !== tab) continue;
			if (record.initial) this.applySnapshot(record.initial, panel);
			record.owner = null;
		}
		state?.entries.clear();
		if (state) state.stack.length = 0;
	}

	showApplication(panel) {
		this.register(panel);
		if ([...this.tabs.values()].some(state => state.stack.includes(panel))) throw new Error("Document dialog is already in use");
		const record = this.records.get(panel);
		record.application = true;
		try { record.showModal(); }
		catch (error) { record.application = false; throw error; }
	}

	applicationOpen() {
		return [...this.records.values()].some(record => record.application);
	}

	prepare(entry, panel) {
		for (const setup of entry.setup.splice(0)) setup(panel);
	}

	snapshot(panel) {
		const nodes = [panel, ...panel.querySelectorAll("*")].map(node => ({ node,
			children: [...node.childNodes], attributes: [...node.attributes].filter(attribute => node !== panel || attribute.name !== "open").map(attribute => [attribute.name, attribute.value]),
			value: "value" in node && node.type !== "file" ? node.value : undefined,
			checked: node.checked, indeterminate: node.indeterminate, selected: node.selected,
			files: node.type === "file" ? [...node.files] : undefined, scrollTop: node.scrollTop, scrollLeft: node.scrollLeft,
			selectionStart: node.selectionStart, selectionEnd: node.selectionEnd, selectionDirection: node.selectionDirection,
			text: [...node.childNodes].filter(child => child.nodeType === 3).map(child => [child, child.data]) }));
		const focus = panel.contains(this.document.activeElement) ? this.document.activeElement : null;
		const range = this.document.getSelection();
		const selection = !focus?.matches("input, textarea") && range?.anchorNode && panel.contains(range.anchorNode) && panel.contains(range.focusNode)
			? { anchor: range.anchorNode, start: range.anchorOffset, focus: range.focusNode, end: range.focusOffset } : null;
		return { nodes, focus, selection, returnValue: panel.returnValue };
	}

	applySnapshot(snapshot, panel) {
		panel.removeAttribute("open");
		for (const item of snapshot.nodes) {
			const { node, children, attributes } = item;
			if (node.childNodes.length !== children.length || children.some((child, index) => node.childNodes[index] !== child)) node.replaceChildren(...children);
			for (const attribute of [...node.attributes]) if (!attributes.some(([name]) => name === attribute.name)) node.removeAttribute(attribute.name);
			for (const [name, value] of attributes) if (node.getAttribute(name) !== value) node.setAttribute(name, value);
			for (const [text, value] of item.text) text.data = value;
		}
		for (const item of snapshot.nodes) {
			const { node } = item;
			if (item.value !== undefined && node.tagName !== "SELECT") node.value = item.value;
			for (const key of ["checked", "indeterminate", "selected"]) if (item[key] !== undefined) node[key] = item[key];
			if (item.files !== undefined) {
				setInputFiles(node, item.files);
			}
			if (item.selectionStart != null && typeof node.setSelectionRange === "function") node.setSelectionRange(item.selectionStart, item.selectionEnd, item.selectionDirection);
			node.scrollTop = item.scrollTop;
			node.scrollLeft = item.scrollLeft;
		}
		panel.returnValue = snapshot.returnValue;
	}

	unpaint() {
		for (const [node, value] of this.inert) node.inert = value;
		this.inert.clear();
		for (const [panel, styles] of this.styles) for (const [name, [value, priority]] of styles) {
			if (value) panel.style.setProperty(name, value, priority);
			else panel.style.removeProperty(name);
		}
		this.styles.clear();
	}

	paint() {
		this.unpaint();
		const stack = this.current()?.stack || [];
		const top = stack.at(-1);
		this.backdrop.hidden = !top;
		if (!top) return;
		const inset = Math.max(0, this.titlebar?.getBoundingClientRect().bottom || 0);
		this.backdrop.style.top = `${inset}px`;
		this.backdrop.style.zIndex = String(1000 + (stack.length - 1) * 2);
		const disable = node => {
			if (!node || node === this.titlebar || node === this.backdrop || this.records.has(node)) return;
			if (node.contains(this.titlebar) || [...this.records.keys()].some(panel => node.contains(panel))) {
				for (const child of node.children) disable(child);
				return;
			}
			if (!this.inert.has(node)) this.inert.set(node, node.inert);
			node.inert = true;
		};
		for (const target of typeof this.targets === "function" ? this.targets() : this.targets) disable(target);
		for (const [index, panel] of stack.entries()) {
			if (panel !== top) { this.inert.set(panel, panel.inert); panel.inert = true; }
			const values = { position: "fixed", inset: `${inset}px 0 0`, zIndex: String(1001 + index * 2), maxHeight: `calc(100dvh - ${inset + 24}px)`, "--dialog-inset": `${inset}px` };
			const styles = new Map();
			for (const [key, value] of Object.entries(values)) {
				const name = key.replace(/[A-Z]/g, letter => `-${letter.toLowerCase()}`);
				styles.set(name, [panel.style.getPropertyValue(name), panel.style.getPropertyPriority(name)]);
				panel.style.setProperty(name, value);
			}
			this.styles.set(panel, styles);
		}
	}

	focus(restore = false) {
		const panel = this.current()?.stack.at(-1);
		if (!panel || this.applicationOpen()) return;
		const saved = restore ? this.current().entries.get(panel)?.snapshot?.focus || this.current().entries.get(panel)?.focus : null;
		const target = saved?.isConnected && panel.contains(saved) ? saved : panel.querySelector("[autofocus], button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]") || panel;
		if (restore || !panel.contains(this.document.activeElement)) target.focus({ preventScroll: true });
		const selection = restore && this.current().entries.get(panel)?.snapshot?.selection;
		if (selection?.anchor?.isConnected && selection.focus?.isConnected) this.document.getSelection().setBaseAndExtent(selection.anchor, selection.start, selection.focus, selection.end);
		if (restore) for (const item of this.current().entries.get(panel)?.snapshot?.nodes || []) { item.node.scrollTop = item.scrollTop; item.node.scrollLeft = item.scrollLeft; }
	}
}

export function positionDropdown(list, anchor, fit = false) {
	const rect = anchor.getBoundingClientRect(), viewport = window.visualViewport;
	const left = viewport?.offsetLeft || 0, top = viewport?.offsetTop || 0;
	const width = viewport?.width || window.innerWidth, height = viewport?.height || window.innerHeight;
	if (rect.right <= left || rect.left >= left + width || rect.bottom <= top || rect.top >= top + height) return false;
	const below = top + height - rect.bottom - 12, above = rect.top - top - 12;
	const limit = Math.min(280, Math.max(below, above));
	if (limit < 20) return false;
	const minimum = Math.min(rect.width, width - 16);
	Object.assign(list.style, { width: fit ? "max-content" : `${minimum}px`, minWidth: `${minimum}px`, maxWidth: `${width - 16}px`, maxHeight: `${limit}px` });
	const size = list.getBoundingClientRect();
	Object.assign(list.style, { left: `${Math.max(left + 8, Math.min(rect.left, left + width - size.width - 8))}px`,
		top: `${below >= size.height || below >= above ? rect.bottom + 4 : rect.top - size.height - 4}px` });
	return true;
}

export function bindDropdownPointer(list, choose) {
	list.addEventListener("pointerdown", event => {
		if (event.pointerType !== "touch" && event.target.closest("[data-index]")) event.preventDefault();
	});
	list.addEventListener("mousedown", event => { if (event.target.closest("[data-index]")) event.preventDefault(); });
	list.addEventListener("click", event => {
		const option = event.target.closest("[data-index]");
		if (option) choose(Number(option.dataset.index));
	});
}

export class SelectPicker {
	constructor(onOpen) {
		Object.assign(this, { onOpen, select: null, options: [], active: -1, selected: -1, query: "", queryTime: 0, frame: 0, touchClick: false });
		this.list = document.createElement("div");
		this.list.className = "dropdown-list";
		this.list.setAttribute("popover", "manual");
		bindDropdownPointer(this.list, index => this.choose(index));
		window.addEventListener("pointerdown", event => {
			if (event.button !== 0) return;
			this.touchClick = false;
			const select = event.target.closest("select");
			if (select && !select.matches(":disabled")) event.preventDefault();
			if (this.select && event.target !== this.select && !this.list.contains(event.target)) this.close();
		}, true);
		window.addEventListener("pointerup", event => {
			const select = event.target.closest("select");
			if (event.pointerType !== "touch" || !select || select.matches(":disabled")) return;
			event.preventDefault();
			if (this.select === select) this.close();
			else this.show(select);
			this.touchClick = true;
		}, true);
		window.addEventListener("mousedown", event => {
			if (event.button === 0 && event.target.closest("select")) event.preventDefault();
		}, true);
		window.addEventListener("click", event => {
			const select = event.target.closest("select");
			if (!select || select.matches(":disabled")) return;
			event.preventDefault();
			if (this.touchClick) { this.touchClick = false; return; }
			if (this.select === select) this.close();
			else this.show(select);
		}, true);
		window.addEventListener("keydown", event => this.keyDown(event), true);
		window.addEventListener("focusin", event => {
			if (this.select && event.target !== this.select && !this.list.contains(event.target)) this.close();
		});
		window.addEventListener("scroll", event => {
			if (this.select && !this.list.contains(event.target)) this.position();
		}, true);
		window.addEventListener("resize", () => this.close());
		window.addEventListener("blur", () => this.close());
		window.addEventListener("pagehide", () => this.close());
		document.addEventListener("close", event => {
			if (this.select && event.target.contains(this.select)) this.close();
		}, true);
		document.addEventListener("reset", event => {
			if (this.select?.form === event.target) this.close();
		}, true);
		document.addEventListener("change", event => {
			if (event.target === this.select) this.render();
		}, true);
	}

	show(select) {
		this.close();
		this.onOpen?.();
		select.focus({ preventScroll: true });
		if (!select.isConnected || select.matches(":disabled")) return;
		this.select = select;
		(select.closest("dialog") || document.body).append(this.list);
		this.render();
		if (!this.select) return;
		this.list.showPopover();
		this.position();
		if (!this.select) return;
		this.activate(this.active);
		this.observer = new MutationObserver(() => this.render());
		this.observer.observe(select, { subtree: true, childList: true, characterData: true, attributes: true });
		this.watch();
	}

	render() {
		const select = this.select;
		if (!select) return;
		const previous = this.options[this.active]?.option, changed = this.selected !== select.selectedIndex;
		this.selected = select.selectedIndex;
		this.options = [...select.options].filter(option => !option.hidden && !option.parentElement.hidden).map(option => ({
			option, disabled: option.disabled || option.parentElement.tagName === "OPTGROUP" && option.parentElement.disabled,
		}));
		if (!this.options.length) { this.close(); return; }
		this.list.replaceChildren();
		let group = null;
		for (const [index, { option, disabled }] of this.options.entries()) {
			if (option.parentElement.tagName === "OPTGROUP" && group !== option.parentElement) {
				group = option.parentElement;
				const heading = document.createElement("div");
				heading.className = "dropdown-group";
				heading.textContent = group.label;
				this.list.append(heading);
			}
			const row = document.createElement("div");
			row.className = "dropdown-option";
			row.dataset.index = String(index);
			row.dataset.disabled = String(disabled);
			const label = document.createElement("span");
			label.textContent = option.label;
			row.append(label);
			row.title = option.title;
			this.list.append(row);
		}
		const selected = this.options.findIndex(({ option, disabled }) => !disabled && option === (changed ? select.selectedOptions[0] : previous));
		this.activate(selected < 0 ? this.options.findIndex(({ disabled }) => !disabled) : selected);
		if (this.list.matches(":popover-open")) this.position();
	}

	watch() {
		const select = this.select;
		if (!select) return;
		if (!select.isConnected || select.matches(":disabled") || !select.getClientRects().length || select.closest("[hidden], [inert], dialog:not([open])")) {
			this.close();
			return;
		}
		if (this.selected !== select.selectedIndex) this.render();
		if (this.select) this.frame = requestAnimationFrame(() => this.watch());
	}

	position() {
		if (this.select && !positionDropdown(this.list, this.select, true)) this.close();
	}

	activate(index) {
		this.active = index;
		for (const row of this.list.querySelectorAll(".dropdown-option")) {
			row.dataset.selected = String(Number(row.dataset.index) === index);
			if (row.dataset.selected === "true" && this.list.matches(":popover-open")) row.scrollIntoView({ block: "nearest" });
		}
	}

	choose(index) {
		const select = this.select, item = this.options[index];
		if (!select || select.matches(":disabled") || !item || item.disabled || !select.contains(item.option)) return;
		const changed = select.selectedIndex !== item.option.index;
		select.selectedIndex = item.option.index;
		this.close();
		if (changed) {
			select.dispatchEvent(new Event("input", { bubbles: true }));
			select.dispatchEvent(new Event("change", { bubbles: true }));
		}
	}

	close() {
		if (!this.select) return;
		this.observer?.disconnect();
		this.observer = null;
		cancelAnimationFrame(this.frame);
		this.frame = 0;
		this.list.hidePopover();
		this.list.remove();
		this.list.replaceChildren();
		this.select = null;
		this.options = [];
		this.active = this.selected = -1;
		this.query = "";
		this.queryTime = 0;
	}

	keyDown(event) {
		if (event.defaultPrevented || event.isComposing) return;
		this.touchClick = false;
		if (event.ctrlKey || event.metaKey || event.key === "Tab") { this.close(); return; }
		const select = event.target.closest("select");
		if (!select || select.matches(":disabled") || event.altKey && !["ArrowDown", "ArrowUp"].includes(event.key)) return;
		const key = event.key, open = this.select === select;
		if (key === "Escape") {
			if (!open) return;
			this.close();
		} else if (key === "F4" || event.altKey) {
			if (open) this.close();
			else this.show(select);
		} else if (["Enter", " "].includes(key)) {
			if (open) this.choose(this.active);
			else this.show(select);
		} else if (["ArrowDown", "ArrowUp", "Home", "End", "PageDown", "PageUp"].includes(key)) {
			if (!open) this.show(select);
			if (this.select && (open || !["ArrowDown", "ArrowUp"].includes(key))) {
				const available = this.options.flatMap(({ disabled }, index) => disabled ? [] : [index]);
				const at = available.indexOf(this.active), direction = ["ArrowUp", "PageUp"].includes(key) ? -1 : 1;
				const step = key.startsWith("Page") ? Math.max(1, Math.floor(this.list.clientHeight / (this.list.querySelector(".dropdown-option")?.offsetHeight || 32)) - 1) : 1;
				const next = key === "Home" ? 0 : key === "End" ? available.length - 1 : Math.max(0, Math.min(available.length - 1, at + direction * step));
				this.activate(available[next] ?? -1);
			}
		} else if (key.length === 1) {
			if (!open) this.show(select);
			const now = performance.now();
			this.query = (now - this.queryTime < 700 ? this.query : "") + key.toLocaleLowerCase();
			this.queryTime = now;
			const repeated = [...this.query].every(value => value === this.query[0]), query = repeated ? this.query[0] : this.query;
			const start = repeated ? this.active + 1 : Math.max(0, this.active);
			for (let offset = 0; offset < this.options.length; offset++) {
				const index = (start + offset) % this.options.length, item = this.options[index];
				if (!item.disabled && item.option.label.trim().toLocaleLowerCase().startsWith(query)) { this.activate(index); break; }
			}
		} else return;
		event.preventDefault();
		event.stopImmediatePropagation();
	}
}
