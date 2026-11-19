let printQueue = Promise.resolve();

export class PrintDialog {
	constructor(options) {
		Object.assign(this, options);
		const panel = this.panel || document.querySelector("#printPanel");
		this.el = Object.fromEntries(["Panel", "Form", "Settings", "Printer", "Properties", "Copies", "Collation", "Duplex", "BackReverse", "BackRotate", "Contents", "Scope", "Range", "Parity", "Reverse", "Mode", "Paper", "Width", "Height", "Orientation", "Scaling", "Scale", "MarginTop", "MarginRight", "MarginBottom", "MarginLeft", "Grid", "PageOrder", "Gap", "Binding", "BookletSheets", "SheetFrom", "SheetTo", "Overlap", "Sides", "Rotate", "Grayscale", "Borders", "CropMarks", "Raster", "DPI", "DPIValue", "TileOnlyLarge", "Labels", "Preview", "Previous", "Next", "PreviewNumber", "PageControl", "PageCount", "Status", "Spinner", "ProgressTrack", "ProgressBar", "Cancel", "Submit"].map(name => [name, name === "Panel" ? panel : panel.querySelector(`#print${name}`)]));
		this.printerOptions = this.el.Printer.innerHTML;
		this.paperOptions = this.el.Paper.innerHTML;
		this.settings = new WeakMap();
		this.queue = Promise.resolve();
		this.sequence = 0;
		this.urls = [];
		this.el.Settings.addEventListener("input", event => { if (event.target === this.el.Printer) this.choosePrinter(); else this.schedule(); });
		this.el.Properties.addEventListener("click", () => this.properties());
		this.el.Form.addEventListener("submit", event => { event.preventDefault(); this.print(); });
		this.el.Cancel.addEventListener("click", () => {
			if (this.running && this.waiting !== "native") { this.abortPrint(); return; }
			this.el.Panel.close();
		});
		this.el.Panel.addEventListener("close", () => this.close());
		this.el.Panel.addEventListener("cancel", event => {
			if (this.waiting === "native") { event.preventDefault(); return; }
			if (this.running) { event.preventDefault(); this.abortPrint(); }
		});
		this.el.Previous.addEventListener("click", () => this.preview(this.side - 1));
		this.el.Next.addEventListener("click", () => this.preview(this.side + 1));
		this.el.PreviewNumber.addEventListener("change", () => this.preview(Number(this.el.PreviewNumber.value) - 1));
		this.el.PreviewNumber.addEventListener("keydown", event => {
			if (event.key !== "Enter") return;
			event.preventDefault();
			if (!event.repeat) this.el.PreviewNumber.dispatchEvent(new Event("change"));
		});
		this.observer = new ResizeObserver(([entry]) => { this.previewSize = entry.contentRect; this.resize(); });
		this.observer.observe(this.el.Preview);
		this.printMedia = typeof window.matchMedia === "function" ? window.matchMedia("print") : null;
		this.browserPrintTimer = 0;
		this.browserPrintFallback = () => {
			if (this.waiting !== "browser") return;
			clearTimeout(this.browserPrintTimer);
			this.browserPrintTimer = setTimeout(() => {
				if (this.waiting === "browser" && !this.printMedia?.matches) this.finishPrint("打印窗口已关闭");
			}, 300);
		};
		this.browserPrintMediaChange = event => {
			if (!event.matches) this.browserPrintFallback();
		};
		this.browserPrintVisibility = () => {
			if (document.visibilityState === "visible") this.browserPrintFallback();
		};
		this.afterPrint = () => { if (this.waiting === "browser") this.finishPrint("打印窗口已关闭"); };
		this.beforePrint = () => {
			if (!this.context || !this.el.Panel.open) return;
			const root = document.querySelector("#ofdgoPrintDocument");
			if (!root?.children.length) {
				this.root = root || this.createRoot();
				this.root.textContent = "请先在OFDGo中打开打印预览，准备完整文档后打印。";
			}
		};
		window.addEventListener("afterprint", this.afterPrint);
		window.addEventListener("beforeprint", this.beforePrint);
	}

	open(context) {
		if (this.context) return;
		const saved = this.settings.get(context.tab)?.get(context.document);
		this.context = context;
		this.restoring = true;
		this.prepared = false;
		this.layout = null;
		this.job = this.profile = null;
		this.side = 0;
		this.sequence++;
		this.el.Printer.innerHTML = this.printerOptions;
		this.el.Paper.innerHTML = this.paperOptions;
		this.el.Form.reset();
		this.orientation = saved?.orientation ?? this.el.Orientation.value;
		this.duplexBeforeBooklet = saved?.duplex ?? this.el.Duplex.value;
		this.scalingBeforeBooklet = saved?.scaling ?? this.el.Scaling.value;
		this.restore(saved);
		this.mode = this.el.Mode.value;
		if (!saved) this.el.Scope.value = context.selection.length ? "selected" : "all";
		this.el.Scope.querySelector('option[value="selected"]').disabled = !context.selection.length;
		this.el.Sides.value = this.manual() ? "front" : "all";
		if (!saved) this.el.Contents.value = context.annotations ? "documentAndAnnotations" : "document";
		this.status();
		this.el.Preview.replaceChildren();
		this.show(this.el.Panel);
		this.loadDevices(saved);
	}

	isDevice() { return this.el.Printer.value.startsWith("device:"); }
	manual() { return this.el.Duplex.value.startsWith("manual"); }

	async loadDevices(saved) {
		const sequence = ++this.sequence;
		const selected = saved?.values.Printer;
		this.loading = true;
		this.controls();
		const browser = this.el.Printer.querySelector('option[value="browser"]');
		browser.disabled = !!this.nativeHost || typeof window.print !== "function";
		if (browser.disabled) this.el.Printer.value = "pdf";
		try {
			if (this.nativeHost) {
				this.status("正在读取设备", true);
				const capabilities = await this.nativeHost.call("print.capabilities", {});
				if (!this.current(sequence)) return;
				this.capabilities = capabilities;
				browser.disabled = !capabilities.systemDialog;
				const printers = capabilities.direct ? await this.nativeHost.call("print.printers", {}) : [];
				if (!this.current(sequence)) return;
				for (const printer of printers) this.el.Printer.add(new Option(printer.name, `device:${printer.name}`));
				const printer = printers.find(printer => printer.default) || printers[0];
				this.el.Printer.value = printer ? `device:${printer.name}` : capabilities.systemDialog ? "browser" : "pdf";
			}
		} catch (error) { if (this.current(sequence)) this.status(error.message); }
		finally {
			if (this.current(sequence)) {
				this.loading = false;
				if ([...this.el.Printer.options].some(option => option.value === selected && !option.disabled)) this.el.Printer.value = selected;
				await this.choosePrinter(this.el.Printer.value === selected ? saved : null);
			}
		}
	}

	async choosePrinter(saved) {
		const sequence = ++this.sequence;
		const paper = this.el.Paper.value;
		this.layout = this.job = this.profile = null;
		this.loading = true;
		this.status();
		this.controls();
		if (!this.isDevice()) {
			this.el.Paper.innerHTML = this.paperOptions;
			this.el.Paper.value = paper;
			if (!this.el.Paper.value) {
				const [width, height] = paper.split(",").map(Number);
				if (width > 0 && height > 0 && Number.isFinite(width) && Number.isFinite(height)) {
					this.el.Paper.value = "custom";
					this.el.Width.value = width;
					this.el.Height.value = height;
				} else this.el.Paper.value = "210,297";
			}
		}
		this.el.Properties.hidden = true;
		try {
			if (this.isDevice()) {
				this.status("正在读取纸张", true);
				const profile = await this.nativeHost.call("print.query", {printer:this.el.Printer.value.slice(7), copies:1, duplex:"simplex"});
				if (!this.current(sequence)) return;
				this.profile = profile;
				this.el.Paper.replaceChildren(...profile.papers.map(paper => new Option(paper.name, `${paper.width},${paper.height}`)));
				if (profile.customPaper) this.el.Paper.add(new Option("自定尺寸", "custom"));
				this.el.Paper.value = `${profile.options.width},${profile.options.height}`;
				if (!this.el.Paper.value && profile.customPaper) { this.el.Paper.value = "custom"; this.el.Width.value = profile.options.width; this.el.Height.value = profile.options.height; }
				if (!this.el.Paper.value) throw new Error("设备默认纸张不在支持列表中");
				this.el.Orientation.value = profile.options.orientation;
				this.el.Copies.max = Math.min(999, profile.maxCopies || 999);
				this.el.Copies.value = 1;
				this.el.Grayscale.checked = profile.options.color === "grayscale";
				this.el.Grayscale.disabled = !profile.color;
				this.el.Properties.hidden = !profile.properties;
			} else this.el.Grayscale.disabled = false;
		} catch (error) { if (this.current(sequence)) { this.status(error.message); return; } }
		finally { if (this.current(sequence)) { this.loading = false; this.controls(); } }
		if (this.current(sequence)) {
			this.restore(saved);
			if (this.el.Scope.value === "selected" && !this.context.selection.length) this.el.Scope.value = "all";
			this.restoring = false;
			this.schedule();
		}
	}

	async properties() {
		if (!this.isDevice() || this.running || this.loading) return;
		const sequence = this.sequence;
		const printer = this.el.Printer.value.slice(7);
		this.loading = true;
		this.status("正在打开属性", true);
		this.controls();
		let before = null;
		let after = null;
		let failed = null;
		try {
			before = await this.nativeHost.call("print.query", {printer}).catch(() => null);
			if (!this.current(sequence)) return;
			await this.nativeHost.call("print.properties", {printer});
			if (!this.current(sequence)) return;
			after = await this.nativeHost.call("print.query", {printer});
		} catch (error) {
			failed = error;
		} finally {
			if (this.current(sequence)) {
				this.loading = false;
				if (failed) {
					this.status(failed.message);
					this.controls();
				} else if (after) {
					this.syncDeviceProfile(before, after);
					this.controls();
					this.schedule();
				} else {
					this.controls();
				}
			}
		}
	}

	syncDeviceProfile(before, after) {
		const el = this.el;
		const currentPaper = el.Paper.value;
		const currentWidth = el.Width.value;
		const currentHeight = el.Height.value;
		el.Paper.replaceChildren(...after.papers.map(paper => new Option(paper.name, `${paper.width},${paper.height}`)));
		if (after.customPaper) el.Paper.add(new Option("自定尺寸", "custom"));
		const paperChanged = !before || after.options.width !== before.options.width || after.options.height !== before.options.height;
		if (!paperChanged) {
			el.Paper.value = currentPaper;
			if (el.Paper.value === "custom") {
				el.Width.value = currentWidth;
				el.Height.value = currentHeight;
			}
		}
		if (paperChanged || !el.Paper.value) {
			el.Paper.value = `${after.options.width},${after.options.height}`;
			if (!el.Paper.value && after.customPaper) {
				el.Paper.value = "custom";
				el.Width.value = after.options.width;
				el.Height.value = after.options.height;
			}
		}
		if (!el.Paper.value && el.Paper.options.length) el.Paper.selectedIndex = 0;
		if (before && after.options.orientation && after.options.orientation !== before.options.orientation && el.Mode.value !== "booklet") {
			el.Orientation.value = after.options.orientation;
		}
		if (before && after.options.duplex && after.options.duplex !== before.options.duplex) {
			if ([...el.Duplex.options].some(option => option.value === after.options.duplex)) el.Duplex.value = after.options.duplex;
		}
		const maxCopies = Math.min(999, after.maxCopies || 999);
		el.Copies.max = maxCopies;
		if (before && after.options.copies > 0 && after.options.copies !== before.options.copies) {
			el.Copies.value = Math.min(maxCopies, after.options.copies);
		} else if (Number(el.Copies.value) > maxCopies) {
			el.Copies.value = maxCopies;
		}
		el.Grayscale.disabled = !after.color;
		if (!after.color) el.Grayscale.checked = after.options.color === "grayscale";
		else if (before && after.options.color && after.options.color !== before.options.color) el.Grayscale.checked = after.options.color === "grayscale";
		el.Properties.hidden = !after.properties;
		this.profile = after;
	}

	deviceOptions() {
		const [width, height] = this.el.Paper.value === "custom" ? [Number(this.el.Width.value), Number(this.el.Height.value)] : this.el.Paper.value.split(",").map(Number);
		const manual = this.manual(), partial = this.el.Sides.value !== "all";
		return {printer:this.el.Printer.value.slice(7), width, height, orientation:this.el.Orientation.value,
			copies:Number(this.el.Copies.value), collation:this.el.Collation.checked ? "collated" : "uncollated",
			duplex:manual || partial ? "simplex" : this.el.Duplex.value, color:this.el.Grayscale.checked ? "grayscale" : "color",
			scale:1, pagesPerSide:1, margins:{top:0,right:0,bottom:0,left:0},
			...(this.capabilities?.backgrounds ? {backgrounds:true} : {}), ...(this.capabilities?.headersFooters ? {headersFooters:false} : {})};
	}

	options(profile) {
		const el = this.el;
		const number = name => {
			const value = Number(el[name].value);
			if (!el[name].value.trim() || !Number.isFinite(value)) throw new Error("请输入有效数值");
			return value;
		};
		let [paperWidth, paperHeight] = el.Paper.value === "source" ? [0, 0] : el.Paper.value === "custom" ? [number("Width"), number("Height")] : el.Paper.value.split(",").map(Number);
		if (el.Orientation.value === "landscape") [paperWidth, paperHeight] = [paperHeight, paperWidth];
		if (profile) [paperWidth, paperHeight] = [profile.width, profile.height];
		const scope = el.Scope.value;
		const [rows, columns] = el.Grid.value.split(",").map(Number);
		if (scope === "selected" && !this.context.selection.length) throw new Error("未选择页面");
		if (scope === "range" && !el.Range.value.trim()) throw new Error("请输入页码范围");
		const manual = this.manual(), duplex = el.Duplex.value.replace("manualL", "l").replace("manualS", "s");
		if (manual && this.isDevice() && number("Copies") !== 1) throw new Error("手动双面每次仅支持1份");
		return {
			...(scope === "current" ? {pages:[this.context.page]} : scope === "selected" ? {pages:this.context.selection} : scope === "range" ? {pageRange:el.Range.value.trim()} : {}),
			mode:el.Mode.value, scaling:el.Scaling.value, paperWidth, paperHeight,
			margins:{top:number("MarginTop"), right:number("MarginRight"), bottom:number("MarginBottom"), left:number("MarginLeft")},
			...(profile ? {printableArea:{x:profile.printableArea.x, y:profile.printableArea.y, w:profile.printableArea.width, h:profile.printableArea.height}} : {}),
			copies:this.isDevice() ? number("Copies") : 1, collate:el.Collation.checked, duplex,
			scale:el.Scale.disabled ? 1 : number("Scale") / 100, rotate:el.Rotate.checked, reverse:el.Reverse.checked !== (manual && el.Sides.value === "back" && el.BackReverse.checked), parity:el.Parity.value, sides:el.Sides.value,
			backRotate:manual && el.BackRotate.checked,
			rows, columns, pageOrder:el.PageOrder.value, gap:el.Gap.disabled ? 0 : number("Gap"), binding:el.Binding.value, bookletSheets:!el.BookletSheets.disabled && el.BookletSheets.value ? number("BookletSheets") : 0, overlap:el.Overlap.disabled ? 0 : number("Overlap"),
			...(el.Mode.value === "booklet" ? {sheetFrom:el.SheetFrom.value ? number("SheetFrom") : 0, sheetTo:el.SheetTo.value ? number("SheetTo") : 0} : {}),
			contents:el.Contents.value, grayscale:el.Grayscale.checked, borders:el.Borders.checked, cropMarks:el.CropMarks.checked, raster:el.Raster.checked, dpi:number("DPI"), tileOnlyLarge:el.TileOnlyLarge.checked, labels:el.Labels.checked
		};
	}

	schedule() {
		const el = this.el, mode = el.Mode.value;
		const source = el.Paper.querySelector('option[value="source"]');
		if (source) source.disabled = mode !== "single";
		if (mode !== "single" && el.Paper.value === "source") el.Paper.value = "210,297";
		if (mode === "booklet" && this.mode !== "booklet") {
			this.orientation = el.Orientation.value;
			this.duplexBeforeBooklet = el.Duplex.value;
			this.scalingBeforeBooklet = el.Scaling.value;
			el.Scaling.value = "shrink";
			el.Duplex.value = this.isDevice() && !this.profile?.duplex ? "manualShortEdge" : "shortEdge";
		} else if (mode !== "booklet" && this.mode === "booklet") {
			el.Orientation.value = this.orientation;
			el.Duplex.value = this.duplexBeforeBooklet;
			el.Scaling.value = this.scalingBeforeBooklet;
		}
		if (mode === "booklet") el.Orientation.value = "landscape";
		this.mode = mode;
		if (this.isDevice() && !this.profile?.duplex && ["longEdge", "shortEdge"].includes(el.Duplex.value)) el.Duplex.value = el.Duplex.value === "longEdge" ? "manualLongEdge" : "manualShortEdge";
		if (mode === "booklet" && el.Duplex.value === "simplex") el.Duplex.value = this.isDevice() && !this.profile?.duplex ? "manualShortEdge" : "shortEdge";
		if (el.Duplex.value === "simplex") el.Sides.value = "all";
		const isManual = this.manual();
		el.Sides.querySelector('option[value="all"]').disabled = isManual;
		if (isManual && el.Sides.value === "all") el.Sides.value = "front";
		for (const option of el.Duplex.options) option.disabled = this.isDevice() && !this.profile?.duplex && ["longEdge", "shortEdge"].includes(option.value) || mode === "booklet" && ["simplex", "longEdge", "manualLongEdge"].includes(option.value);
		el.Orientation.disabled = mode === "booklet" || el.Paper.value === "source";
		const rows = {device:this.isDevice(), sides:el.Duplex.value !== "simplex", manual:isManual, range:el.Scope.value === "range", paper:el.Paper.value === "custom", scale:el.Scaling.value === "custom" || mode === "poster", multiple:mode === "multiple", gap:mode === "multiple" || mode === "booklet", booklet:mode === "booklet", poster:mode === "poster"};
		for (const row of el.Panel.querySelectorAll("[data-print-row]")) {
			row.hidden = !rows[row.dataset.printRow];
			for (const input of row.querySelectorAll("input, select")) input.disabled = row.hidden;
		}
		el.Scaling.disabled = mode === "poster" || mode === "booklet";
		el.DPI.disabled = !el.Raster.checked && !el.Grayscale.checked;
		const sequence = ++this.sequence;
		this.layout = null;
		this.job = null;
		this.status();
		this.controls();
		clearTimeout(this.timer);
		this.timer = setTimeout(() => {
			this.queue = this.queue.catch(() => {}).then(() => this.refresh(sequence));
		}, 160);
	}

	async refresh(sequence) {
		if (!this.current(sequence)) return;
		const context = this.context;
		try {
			if (!this.el.Form.checkValidity()) {
				this.status("请检查打印设置");
				return;
			}
			this.status("正在准备预览", true);
			const deviceOptions = this.isDevice() ? this.deviceOptions() : null;
			const profile = deviceOptions ? await this.nativeHost.call("print.query", deviceOptions) : null;
			if (!this.current(sequence)) return;
			this.profile = profile;
			const options = this.options(profile);
			const layout = await context.call(this.prepared ? "ofdgoLayoutPrint" : "ofdgoPreparePrint", options);
			if (!this.current(sequence)) return;
			this.prepared = true;
			this.layout = layout;
			if (this.isDevice()) this.el.Copies.max = Math.min(999, this.profile.maxCopies || 999, layout.copiesLimit > 0 ? layout.copiesLimit : 999);
			await this.preview(Math.min(this.side, layout.sides.length - 1));
			if (!this.current(sequence) || this.layout !== layout) return;
			this.job = {deviceOptions, fingerprint:profile?.fingerprint, manual:this.manual(), sides:options.sides, backContent:layout.backContent};
			this.status();
			if (layout.sides.some(side => side.placements.some(placement => placement.clipped))) this.status("内容超出可打印区域");
			if (this.manual() && options.sides === "back" && !layout.backContent) this.status("背面为空白");
		} catch (error) {
			if (this.current(sequence)) this.status(error.code === "printDenied" ? "文档禁止打印" : error.message);
		} finally {
			if (this.current(sequence)) this.controls();
		}
	}

	current(sequence = this.sequence) { return sequence === this.sequence && this.context && !this.context.tab.closed; }

	async preview(index) {
		if (!this.layout || this.running) return;
		if (!Number.isInteger(index) || index < 0 || index >= this.layout.sides.length) { this.controls(); return; }
		const sequence = this.sequence, preview = this.previewSequence = (this.previewSequence || 0) + 1;
		this.side = index;
		this.controls();
		try {
			let {svg} = await this.context.call("ofdgoPrintSide", index);
			if (!this.current(sequence) || preview !== this.previewSequence) return;
			if (this.profile) {
				const area = this.profile.printableArea;
				const end = svg.lastIndexOf("</svg>");
				if (end >= 0) svg = `${svg.slice(0, end)}<rect x="${area.x}" y="${area.y}" width="${area.width}" height="${area.height}" fill="none" stroke="#88949e" stroke-width="0.2" stroke-dasharray="1 1"/>${svg.slice(end)}`;
			}
			const url = URL.createObjectURL(new Blob([svg], {type:"image/svg+xml"}));
			const img = new Image();
			img.src = url;
			try { await img.decode(); } catch (error) { URL.revokeObjectURL(url); throw error; }
			if (!this.current(sequence) || preview !== this.previewSequence) { URL.revokeObjectURL(url); return; }
			if (this.previewURL) URL.revokeObjectURL(this.previewURL);
			this.previewURL = url;
			this.el.Preview.replaceChildren(img);
			this.resize();
		} catch (error) { if (this.current(sequence) && preview === this.previewSequence) { this.status(error.message); this.layout = null; this.controls(); } }
	}

	resize() {
		const img = this.el.Preview.querySelector("img"), side = this.layout?.sides[this.side];
		if (!img || !side || !this.previewSize) return;
		const factor = Math.min(this.previewSize.width / side.width, this.previewSize.height / side.height);
		img.style.width = `${Math.max(1, side.width * factor)}px`;
		img.style.height = `${Math.max(1, side.height * factor)}px`;
	}

	status(text = "", busy = false, completed = 0, total = 0) {
		this.el.Status.textContent = total ? `${text} · ${completed} / ${total}` : text;
		this.el.Spinner.hidden = !busy;
		this.el.ProgressTrack.hidden = !busy || !total;
		this.el.ProgressBar.style.width = `${total ? completed / total * 100 : 0}%`;
	}

	controls() {
		const count = this.layout?.sides.length || 0, busy = !!this.running || !!this.loading;
		this.el.DPIValue.textContent = this.el.DPI.value;
		this.el.Settings.disabled = busy;
		this.el.Submit.disabled = !count || !this.job || busy;
		if (this.el.Printer.value !== "pdf" && this.manual() && this.el.Sides.value === "back" && !this.layout?.backContent) this.el.Submit.disabled = true;
		this.el.Previous.disabled = busy || !count || this.side === 0;
		this.el.Next.disabled = busy || !count || this.side + 1 === count;
		this.el.PreviewNumber.disabled = busy || !count;
		this.el.PreviewNumber.max = count || 1;
		this.el.PreviewNumber.value = count ? this.side + 1 : 1;
		this.el.PageCount.textContent = String(count);
		this.el.PageControl.style.setProperty("--page-width", `${String(count || 1).length + 0.5}ch`);
		this.el.Cancel.textContent = busy && this.waiting !== "native" ? "取消" : "关闭";
		this.el.Cancel.disabled = this.waiting === "native";
	}

	restore(saved) {
		for (const [name, value] of Object.entries(saved?.values || {})) {
			if (name === "Printer") continue;
			const node = this.el[name];
			if (node.type === "checkbox") node.checked = value;
			else if (!node.options || [...node.options].some(option => option.value === value)) node.value = value;
		}
	}

	remember() {
		const context = this.context;
		if (!context || this.restoring) return;
		let settings = this.settings.get(context.tab);
		if (!settings) this.settings.set(context.tab, settings = new Map());
		const values = {};
		for (const [name, node] of Object.entries(this.el)) if (node.closest("#printSettings") && name !== "Sides" && node.matches("input, select")) values[name] = node.type === "checkbox" ? node.checked : node.value;
		settings.set(context.document, {values, orientation:this.orientation, duplex:this.duplexBeforeBooklet, scaling:this.scalingBeforeBooklet});
	}

	createRoot() {
		const root = document.createElement("div");
		root.id = "ofdgoPrintDocument";
		document.body.append(root);
		return root;
	}

	watchBrowserPrint() {
		this.clearBrowserPrintWatch();
		window.addEventListener("focus", this.browserPrintFallback);
		document.addEventListener("visibilitychange", this.browserPrintVisibility);
		this.printMedia?.addEventListener?.("change", this.browserPrintMediaChange);
	}

	clearBrowserPrintWatch() {
		clearTimeout(this.browserPrintTimer);
		this.browserPrintTimer = 0;
		window.removeEventListener("focus", this.browserPrintFallback);
		document.removeEventListener("visibilitychange", this.browserPrintVisibility);
		this.printMedia?.removeEventListener?.("change", this.browserPrintMediaChange);
	}

	async print() {
		if (!this.layout || !this.job || this.running) return;
		this.previewSequence++;
		if (this.el.Printer.value === "pdf") return this.pdf();
		const sequence = this.sequence, context = this.context;
		const job = this.job;
		this.running = "print";
		this.controls();
		this.status("正在等待打印", true);
		const previous = printQueue;
		let release;
		printQueue = new Promise(resolve => { release = resolve; });
		await previous;
		if (!this.current(sequence)) { release(); return; }
		this.releasePrint = release;
		try {
			this.root = document.querySelector("#ofdgoPrintDocument") || this.createRoot();
			for (const img of this.root.querySelectorAll("img")) img.src = "";
			this.root.replaceChildren();
			const style = document.createElement("style");
			style.textContent = this.layout.sides.map((side, i) => `@page ofdgoPrint${i}{size:${side.width}mm ${side.height}mm;margin:0}`).join("");
			this.root.append(style);
			const raster = this.el.Raster.checked || this.el.Grayscale.checked;
			let totalBytes = 0;
			this.status("正在准备纸面", true, 0, this.layout.sides.length);
			for (let i = 0; i < this.layout.sides.length; i++) {
				if (!this.current(sequence)) throw new DOMException("打印已取消", "AbortError");
				const {svg} = await context.call("ofdgoPrintSide", i);
				if (!this.current(sequence)) throw new DOMException("打印已取消", "AbortError");
				totalBytes += svg.length;
				const deferDecode = raster && this.layout.sides.length > 8 || totalBytes > 32 * 1024 * 1024;
				const url = URL.createObjectURL(new Blob([svg], {type:"image/svg+xml"}));
				this.urls.push(url);
				const img = new Image(), section = document.createElement("section"), side = this.layout.sides[i];
				img.decoding = deferDecode ? "async" : "sync";
				const loaded = deferDecode ? new Promise((resolve, reject) => {
					img.addEventListener("load", resolve, {once:true});
					img.addEventListener("error", () => reject(new Error("纸面加载失败")), {once:true});
				}) : null;
				img.src = url;
				section.append(img);
				section.style.cssText = `page:ofdgoPrint${i};width:${side.width}mm;height:${side.height}mm`;
				this.root.append(section);
				if (deferDecode) {
					if (!img.complete) await loaded;
					if ((i + 1) % 8 === 0) await new Promise(resolve => setTimeout(resolve, 0));
				} else {
					await img.decode();
				}
				if (this.current(sequence)) this.status("正在准备纸面", true, i + 1, this.layout.sides.length);
			}
			await document.fonts.ready;
			if (!this.current(sequence)) throw new DOMException("打印已取消", "AbortError");
			if (this.el.Panel.open) {
				this.previousTitle = document.title;
				document.title = this.printTitle = context.name;
			}
			this.waiting = this.nativeHost ? "native" : "browser";
			this.controls();
			this.status(job.deviceOptions ? "正在提交打印" : "正在打开打印", true);
			if (job.deviceOptions) {
				const result = await this.nativeHost.call("print", {...job.deviceOptions, fingerprint:job.fingerprint});
				this.finishPrint(result?.submitted ? "打印已提交" : "打印作业未被接受");
				if (this.current(sequence) && result?.submitted && job.manual && job.sides === "front") {
					if (job.backContent) { this.el.Sides.value = "back"; this.schedule(); }
					else this.status("背面为空白");
				}
			} else if (this.nativeHost) {
				const capabilities = await this.nativeHost.call("print.capabilities", {});
				if (!this.current(sequence)) throw new DOMException("打印已取消", "AbortError");
				if (!capabilities.systemDialog) throw new Error("当前运行时不支持系统打印");
				if (!capabilities.dialogCompletion) {
					this.waiting = "browser";
					this.controls();
					this.watchBrowserPrint();
				}
				await this.nativeHost.call("print.dialog", {kind:"system"});
				if (capabilities.dialogCompletion) this.finishPrint("打印窗口已关闭");
			} else if (typeof window.print === "function") {
				this.watchBrowserPrint();
				window.print();
			}
			else throw new Error("当前浏览器不支持打印");
		} catch (error) {
			if (this.releasePrint === release) this.finishPrint();
			if (this.current(sequence)) {
				this.status(error.name === "AbortError" ? "打印已取消" : error.message);
			}
		} finally {
			if (this.current(sequence)) {
				if (!this.waiting) this.running = false;
				this.controls();
			}
		}
	}

	abortPrint() {
		const exporting = this.running === "pdf";
		this.sequence++;
		if (this.context) this.cancel(this.context.tab);
		this.finishPrint(exporting ? "导出已取消" : "打印已取消");
	}

	finishPrint(message = "") {
		this.clearBrowserPrintWatch();
		if (this.previousTitle !== undefined) {
			if (document.title === this.printTitle) document.title = this.previousTitle;
			delete this.previousTitle;
		}
		if (this.releasePrint && this.root) {
			for (const img of this.root.querySelectorAll("img")) img.src = "";
			this.root.replaceChildren();
		}
		for (const url of this.urls) URL.revokeObjectURL(url);
		this.urls = [];
		this.releasePrint?.();
		this.releasePrint = null;
		this.status(message);
		this.waiting = this.running = false;
		this.controls();
		if (this.destroyed) window.removeEventListener("afterprint", this.afterPrint);
	}

	async pdf() {
		if (!this.layout || this.running) return;
		const sequence = this.sequence, context = this.context;
		const name = `${context.name.replace(/\.[^.]+$/, "")}-打印.pdf`;
		this.running = "pdf";
		this.controls();
		this.status("正在生成文件", true);
		let file = null;
		try {
			file = this.savePicker ? await this.savePicker({suggestedName:name, types:[{description:"PDF", accept:{"application/pdf":[".pdf"]}}]}) : null;
			if (!this.current(sequence)) return;
			const result = await context.call("ofdgoPrintPDF", file);
			if (this.current(sequence)) {
				if (result.blob) await this.download(result.blob, result.mime || "application/pdf", name);
				if (this.current(sequence)) this.status("文件已生成");
			}
		} catch (error) {
			if (this.current(sequence)) this.status(error.name === "AbortError" ? "导出已取消" : error.message);
		} finally {
			await this.release?.(file);
			if (this.current(sequence)) { this.running = false; this.controls(); }
		}
	}

	close() {
		const context = this.context;
		this.remember();
		this.sequence++;
		clearTimeout(this.timer);
		if (context) this.cancel(context.tab);
		if (!this.waiting) this.finishPrint();
		if (this.previewURL) URL.revokeObjectURL(this.previewURL);
		this.previewURL = null;
		this.el.Preview.replaceChildren();
		this.context = null;
		this.queue = this.queue.catch(() => {}).then(() => context?.call("ofdgoDiscardPrint").catch(() => {}));
	}

	destroy() {
		this.destroyed = true;
		this.clearBrowserPrintWatch();
		this.close();
		this.observer.disconnect();
		window.removeEventListener("beforeprint", this.beforePrint);
		if (!this.waiting) window.removeEventListener("afterprint", this.afterPrint);
	}
}
