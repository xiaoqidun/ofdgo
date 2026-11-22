import { bindDropdownPointer, positionDropdown } from "./ofdgo_util.js";

const DATABASE = "ofdgo";
const SHA256_K = [
	0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
	0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
	0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
	0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
	0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
	0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
	0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
	0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
];

export class FontPicker {
	constructor(input, toggle, list, onChange, onOpen, groupFonts) {
		Object.assign(this, { input, toggle, list, onChange, onOpen, groupFonts, fonts: [], groups: [], records: [], options: new Map(), value: "", active: -1, open: false });
		input.addEventListener("input", () => this.show(input.value));
		input.addEventListener("focus", () => input.select());
		input.addEventListener("pointerdown", event => { if (event.button === 0 && !this.open) event.preventDefault(); });
		input.addEventListener("click", () => { if (!this.open) { input.focus(); input.select(); this.show(); } });
		input.addEventListener("keydown", event => this.keyDown(event));
		input.addEventListener("blur", () => { this.commitExact(); this.close(); });
		toggle.addEventListener("pointerdown", event => event.preventDefault());
		toggle.addEventListener("click", () => {
			if (this.open) this.close();
			else { input.focus(); input.select(); this.show(); }
		});
		bindDropdownPointer(list, index => this.choose(index));
		window.addEventListener("resize", () => this.close());
	}

	setFonts(fonts, selected, preserve = false) {
		const open = preserve && this.open;
		if (!open) this.close();
		this.fonts = fonts;
		this.records = null;
		this.options.clear();
		this.groups = fonts.map((_, index) => [index]);
		this.names = new Map();
		for (const font of fonts) {
			const name = (font.menuFamily || font.fullName || font.name).normalize("NFKC").toLocaleLowerCase();
			this.names.set(name, (this.names.get(name) || 0) + 1);
		}
		const key = font => font?.richKey || font?.id || font?.postscriptName;
		this.value = !fonts.length ? "" : selected === null ? "-1" : String(Math.max(0, fonts.findIndex(font => key(font) === key(selected))));
		if (open) {
			const active = this.matches[this.active]?.font;
			this.show(this.query, false);
			const index = active ? this.matches.findIndex(({ font }) => !font.disabled && key(font) === key(active)) : -1;
			if (index >= 0) this.activate(index);
		} else this.restore();
		if (this.groupFonts) this.grouping = this.groupFonts(fonts).then(groups => {
			if (this.fonts !== fonts) return;
			this.groups = groups;
			this.records = null;
			this.options.clear();
			this.names.clear();
			for (const group of groups) {
				const font = fonts[group[0]], name = (font.menuFamily || font.fullName || font.name).normalize("NFKC").toLocaleLowerCase();
				this.names.set(name, (this.names.get(name) || 0) + 1);
			}
			if (this.open) {
				const active = this.matches[this.active]?.index;
				this.show(this.query, false);
				const index = this.matches.findIndex(({ group, font }) => group.includes(active) && !font.disabled);
				if (index >= 0) this.activate(index);
			}
			else this.restore();
		}).catch(() => {});
	}

	setDisabled(disabled) {
		if (this.input.disabled !== disabled) this.input.disabled = disabled;
		if (this.toggle.disabled !== disabled) this.toggle.disabled = disabled;
		if (disabled && this.open) this.close();
	}

	restore() {
		const font = this.fonts[Number(this.value)];
		const value = font?.menuFamily || font?.fullName || font?.name || "";
		if (this.input.value !== value) {
			this.input.value = value;
			if (document.activeElement === this.input) this.input.select();
		}
		if (this.input.scrollLeft) this.input.scrollLeft = 0;
		const title = font ? [this.input.value, this.sourceLabel(font)].filter(Boolean).join(" · ") : "";
		if (this.input.title !== title) this.input.title = title;
	}

	sourceLabel(font) {
		const name = (font.menuFamily || font.fullName || font.name).normalize("NFKC").toLocaleLowerCase();
		if (this.names.get(name) < 2) return "";
		return font.embedded ? "内嵌" : font.file || ["stored", "upload"].includes(font.source) ? "上传" : "系统";
	}

	show(query = "", load = true) {
		if (this.input.disabled) return;
		this.query = query;
		const normalize = value => value.normalize("NFKC").toLocaleLowerCase().replace(/\s+/g, " ").trim();
		const terms = normalize(query).split(" ").filter(Boolean);
		if (!this.records) this.records = this.groups.map(group => ({ group, text: normalize(group.flatMap(index => {
			const font = this.fonts[index];
			return [font.menuFamily, font.menuStyle, font.fullName, font.name, font.family, font.style, font.postscriptName, ...(font.names || [])];
		}).filter(Boolean).join(" ")) }));
		this.matches = this.records.filter(({ text }) => terms.every(term => text.includes(term))).map(({ group }) => {
			const index = group.find(index => !this.fonts[index].bold && !this.fonts[index].italic && !this.fonts[index].disabled) ?? group[0];
			return { font: this.fonts[index], index, group };
		});
		const nodes = this.matches.map(({ font, index }) => {
			let option = this.options.get(index);
			if (!option) {
				option = document.createElement("div");
				option.className = "font-option";
				option.id = `${this.list.id}-${index}`;
				option.dataset.index = String(index);
				const name = document.createElement("span");
				name.textContent = font.menuFamily || font.fullName || font.name;
				option.append(name);
				const source = this.sourceLabel(font);
				if (source) {
					const label = document.createElement("span");
					label.className = "font-option-source";
					label.textContent = source;
					option.append(label);
				}
				option.title = [...new Set([font.fullName, font.name].filter(Boolean))].join(" · ");
				this.options.set(index, option);
			}
			option.setAttribute("data-disabled", String(Boolean(font.disabled)));
			return option;
		});
		const retained = new Set(nodes);
		for (const child of [...this.list.children]) if (!retained.has(child)) child.remove();
		nodes.forEach((node, index) => {
			if (this.list.children[index] !== node) this.list.insertBefore(node, this.list.children[index] || null);
		});
		if (!this.matches.length) {
			const empty = document.createElement("div");
			empty.className = "font-search-empty";
			empty.textContent = this.fonts.length ? "暂无匹配字体" : "暂无字体";
			this.list.append(empty);
		}
		if (!this.open) this.list.showPopover();
		this.open = true;
		this.position();
		const selected = this.matches.findIndex(({ font, group }) => !font.disabled && group.includes(Number(this.value)));
		this.activate(selected < 0 ? this.matches.findIndex(({ font }) => !font.disabled) : selected);
		if (load) this.onOpen?.();
	}

	position() {
		if (!this.open) return;
		if (!positionDropdown(this.list, this.input.parentElement)) this.close();
	}

	activate(index) {
		this.active = index;
		const option = this.list.children[index];
		if (this.selectedOption !== option) {
			this.selectedOption?.setAttribute("data-selected", "false");
			option?.setAttribute("data-selected", "true");
			this.selectedOption = option;
		}
		if (option) {
			option.scrollIntoView({ block: "nearest" });
		}
	}

	choose(index) {
		if (!this.fonts[index] || this.fonts[index].disabled) return;
		const changed = this.value !== String(index);
		this.value = String(index);
		this.close();
		if (changed) {
			const task = this.changing = Promise.resolve(this.onChange());
			return task.finally(() => { if (this.changing === task) this.changing = null; });
		}
	}

	commitExact() {
		const value = this.input.value.trim().toLocaleLowerCase();
		const matches = font => font && !font.disabled
			&& [font.menuFamily, font.fullName, font.name, font.postscriptName, ...(font.names || [])].some(name => name?.toLocaleLowerCase() === value);
		const selected = Number(this.value);
		const index = matches(this.fonts[selected]) ? selected : this.fonts.findIndex(matches);
		if (index >= 0) return this.choose(index);
	}

	prepareCommit(event) {
		if (this.open && (!event || ![this.input, this.toggle, this.list].some(node => node.contains(event.target)))) {
			this.commitExact();
			this.close();
		}
		return this.changing;
	}

	close() {
		if (this.open) { this.open = false; this.list.hidePopover(); }
		this.restore();
	}

	keyDown(event) {
		if (event.isComposing || event.key === "Tab" && (event.ctrlKey || event.metaKey || event.altKey)) return;
		if (!["Escape", "Enter", "ArrowDown", "ArrowUp", "Tab"].includes(event.key)) return;
		event.stopPropagation();
		if (event.key === "Escape") {
			event.preventDefault();
			this.close();
		} else if (event.key === "Enter") {
			event.preventDefault();
			if (this.open && this.active >= 0) this.choose(this.matches[this.active].index);
			else { this.commitExact(); this.close(); }
		} else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
			event.preventDefault();
			if (!this.open) { this.show(); return; }
			const direction = event.key === "ArrowDown" ? 1 : -1;
			for (let i = this.active + direction; i >= 0 && i < this.matches.length; i += direction) {
				if (!this.matches[i].font.disabled) { this.activate(i); break; }
			}
		} else if (event.key === "Tab") { this.commitExact(); this.close(); }
	}
}

export class FontManager {
	constructor({ onChange, onPermissionChange, host = null }) {
		this.host = host;
		this.userFonts = [];
		this.localFonts = [];
		this.catalog = [];
		this.catalogLoaded = false;
		this.catalogPromise = null;
		this.permission = "prompt";
		this.sequence = 0;
		this.database = null;
		this.onPermissionChange = onPermissionChange;
		this.channel = typeof BroadcastChannel === "function" ? new BroadcastChannel(DATABASE) : null;
		if (this.channel) {
			this.channel.onmessage = onChange;
		}
	}

	openDatabase() {
		if (!this.database) {
			this.database = new Promise((resolve, reject) => {
				const request = indexedDB.open(DATABASE, 1);
				request.onupgradeneeded = () => {
					const store = request.result.createObjectStore("fonts", { keyPath: "id", autoIncrement: true });
					store.createIndex("checksum", "checksum", { unique: true });
				};
				request.onsuccess = () => {
					const db = request.result;
					db.onversionchange = () => {
						db.close();
						this.database = null;
					};
					resolve(db);
				};
				request.onerror = () => reject(request.error);
			}).catch((err) => {
				this.database = null;
				throw err;
			});
		}
		return this.database;
	}

	async transaction(mode, action) {
		const db = await this.openDatabase();
		return new Promise((resolve, reject) => {
			const tx = db.transaction("fonts", mode);
			let request;
			tx.oncomplete = () => {
				if (mode === "readwrite") {
					this.channel?.postMessage(null);
				}
				resolve(request?.result);
			};
			tx.onabort = () => reject(tx.error || new Error("字体保存失败"));
			try {
				request = action(tx.objectStore("fonts"));
			} catch (err) {
				tx.abort();
				reject(err);
			}
		});
	}

	listStored() {
		return this.transaction("readonly", (store) => store.getAll());
	}

	async store(fonts) {
		const records = new Map();
		for (const font of fonts) {
			const checksum = await sha256Hex(font.data);
			if (!records.has(checksum)) {
				records.set(checksum, {
					name: font.name,
					data: new Blob([font.data]),
					enabled: font.enabled,
					checksum,
				});
			}
		}
		await this.transaction("readwrite", (store) => {
			for (const font of records.values()) {
				const request = store.index("checksum").getKey(font.checksum);
				request.onsuccess = () => {
					if (request.result === undefined) {
						store.add(font);
					}
				};
			}
		});
	}

	updateStored(id, changes) {
		return this.transaction("readwrite", (store) => {
			const request = store.get(id);
			request.onsuccess = () => {
				if (request.result) {
					store.put({ ...request.result, ...changes });
				}
			};
		});
	}

	deleteStored(id) {
		return this.transaction("readwrite", (store) => store.delete(id));
	}

	queryLocal() {
		if (this.catalogPromise) return this.catalogPromise;
		const task = (async () => {
			try {
				const available = this.host ? await this.host.fonts() : await window.queryLocalFonts();
				this.catalog = this.host ? available : available.map(font => ({ family: font.family, fullName: font.fullName,
					postscriptName: font.postscriptName, style: font.style, blob: () => font.blob() }));
				this.catalogLoaded = true;
				this.permission = "granted";
				this.onPermissionChange();
				return this.catalog;
			} catch (err) {
				if (err && err.name === "NotAllowedError") {
					this.permission = "denied";
					this.onPermissionChange();
				}
				throw err;
			} finally {
				if (this.catalogPromise === task) this.catalogPromise = null;
			}
		})();
		this.catalogPromise = task;
		return task;
	}

	async files(fonts) {
		const result = [];
		for (const font of fonts) {
			if (!font.enabled) {
				continue;
			}
			result.push({ name: font.name, data: await this.read(font) });
		}
		return result;
	}

	async read(font) {
		if (font.nativeFile) return font.nativeFile;
		if (font.blob) {
			const blob = await font.blob();
			return new Uint8Array(await blob.arrayBuffer());
		}
		if (font.data instanceof Blob) {
			font.data = new Uint8Array(await font.data.arrayBuffer());
		}
		return font.data;
	}

	editorFonts() {
		return [...this.userFonts.filter(font => font.enabled).flatMap(file => {
			if (!file.faces) return [file];
			if (!file.faces.length) return [{ ...file, disabled: true }];
			return file.faces.map(face => ({ ...face, id: face.index ? `${file.id}:${face.index}` : file.id, name: file.name, file }));
		}), ...this.catalog];
	}

	async loadFaces(inspect, current = () => true) {
		while (current()) {
			const file = this.userFonts.find(font => font.enabled && !font.faces);
			if (!file) break;
			try { file.faces = await inspect(await this.read(file)); }
			catch { if (!current()) return; file.faces = []; }
		}
		const pending = this.catalog.filter(font => font.blob && !font.menuFamily && !font.metadataFailed);
		await Promise.all(Array.from({ length: Math.min(4, pending.length) }, async () => {
			while (current() && pending.length) {
				const font = pending.shift();
				try {
					const faces = await inspect(await this.read(font));
					const face = faces.find(face => face.postscriptName === font.postscriptName);
					if (face) Object.assign(font, { index: face.index, menuFamily: face.menuFamily, menuStyle: face.menuStyle, familyKey: face.familyKey, bold: face.bold, italic: face.italic, names: face.names });
					else font.metadataFailed = true;
				} catch { if (current()) font.metadataFailed = true; }
			}
		}));
	}

	async defaultFont(order, inspect, current = () => true) {
		const fonts = this.editorFonts().filter(font => !font.disabled);
		if (!fonts.length) return;
		const families = [["DengXian", "等线"], ["Microsoft YaHei", "微软雅黑"], ["PingFang SC", "苹方-简", "苹方 简体"],
			["Noto Sans SC", "Noto Sans CJK SC"], ["Source Han Sans SC", "Source Han Sans CN", "思源黑体", "思源黑体 CN"]];
		const indices = await order(fonts, families);
		let fallback = fonts.length;
		for (const index of indices) {
			if (!current()) return;
			try {
				await inspect(fonts[index], "中文汉字编辑水印，。！？");
				if (current()) return fonts[index];
			} catch (error) {
				if (error.missingGlyphs) fallback = Math.min(fallback, index);
			}
		}
		if (current()) return fonts[fallback];
	}

	async add(fonts) {
		let saved = true;
		try {
			await this.store(fonts);
		} catch {
			saved = false;
			this.userFonts.push(...fonts);
		}
		const changed = saved ? await this.restore() : true;
		if (saved && navigator.storage?.persist) {
			navigator.storage.persist().catch(() => false);
		}
		return { saved, changed };
	}

	async change(font, changes) {
		if (font.source === "stored") {
			if (changes) {
				await this.updateStored(font.id, changes);
			} else {
				await this.deleteStored(font.id);
			}
			await this.restore();
		} else if (changes) {
			Object.assign(font, changes);
		} else {
			this.localFonts = this.localFonts.filter((item) => item.id !== font.id);
			this.userFonts = this.userFonts.filter((item) => item.id !== font.id);
		}
	}

	async restore() {
		const stored = await this.listStored();
		const previous = new Map(this.userFonts.map((font) => [font.id, font]));
		const fonts = stored.map((font) => ({
			...font,
			data: previous.get(font.id)?.checksum === font.checksum ? previous.get(font.id).data : font.data,
			faces: previous.get(font.id)?.checksum === font.checksum ? previous.get(font.id).faces : undefined,
			source: "stored",
		}));
		fonts.push(...this.userFonts.filter((font) => font.source === "upload"));
		if (fonts.length === this.userFonts.length && fonts.every((font, index) => {
			const old = this.userFonts[index];
			return font.id === old.id && font.name === old.name && font.enabled === old.enabled && font.checksum === old.checksum;
		})) {
			return false;
		}
		this.userFonts = fonts;
		return true;
	}

	record(name, data, source) {
		this.sequence += 1;
		return {
			id: `${source}-${this.sequence}`,
			name: name || "font.ttf",
			data,
			enabled: true,
			source,
		};
	}

	records() {
		return [...this.localFonts, ...this.userFonts];
	}

	localName(font) {
		const name = font.fullName || font.family || font.postscriptName || "local-font";
		return `${name}.ttf`;
	}

	localEntries(fonts = this.catalog) {
		const entries = new Map();
		const byLower = new Map();
		const disabled = new Set(this.localFonts.filter(font => !font.enabled).map(font => font.name));
		for (const font of fonts) {
			if (disabled.has(this.localName(font))) continue;
			const style = font.style || "";
			const bold = typeof font.bold === "boolean" ? font.bold : /\b(?:bold|semibold|demibold|extrabold|ultrabold|black|heavy)\b|粗体|加粗|特粗/i.test(style);
			const italic = typeof font.italic === "boolean" ? font.italic : /\b(?:italic|oblique)\b|斜体/i.test(style);
			const priority = !bold && !italic ? 4 : bold && !italic ? 3 : !bold ? 2 : 1;
			const familyStyle = [font.family, font.style].filter(Boolean).join(" ").trim();
			const names = [this.localName(font), font.family && `${font.family}.ttf`, font.postscriptName && `${font.postscriptName}.ttf`,
				familyStyle && `${familyStyle}.ttf`, ...(font.names || []).map(name => name && `${name}.ttf`)];
			for (const name of names) {
				if (!name) continue;
				const key = name.toLowerCase();
				const previous = byLower.get(key);
				if (!previous || priority > previous.priority) {
					if (previous && previous.name !== name) entries.delete(previous.name);
					entries.set(name, font);
					byLower.set(key, { name, priority });
				}
			}
		}
		return entries;
	}

	canReadLocal() {
		return !!this.host || typeof window.queryLocalFonts === "function";
	}

	async refreshPermission() {
		if (this.host) {
			this.permission = "granted";
			this.onPermissionChange();
			return;
		}
		if (!this.canReadLocal() || !navigator.permissions?.query) {
			this.onPermissionChange();
			return;
		}
		try {
			const permission = await navigator.permissions.query({ name: "local-fonts" });
			this.permission = permission.state;
			permission.onchange = () => {
				this.permission = permission.state;
				this.onPermissionChange();
			};
		} catch {
			this.permission = "prompt";
		}
		this.onPermissionChange();
	}
}

function sha256JS(bytes) {
	const total = ((bytes.length + 9 + 63) >> 6) << 6;
	const padded = new Uint8Array(total);
	padded.set(bytes);
	padded[bytes.length] = 0x80;
	const view = new DataView(padded.buffer);
	const bits = bytes.length * 8;
	view.setUint32(total - 8, Math.floor(bits / 0x100000000));
	view.setUint32(total - 4, bits >>> 0);
	let h0 = 0x6a09e667, h1 = 0xbb67ae85, h2 = 0x3c6ef372, h3 = 0xa54ff53a;
	let h4 = 0x510e527f, h5 = 0x9b05688c, h6 = 0x1f83d9ab, h7 = 0x5be0cd19;
	const w = new Uint32Array(64);
	const rotr = (x, n) => (x >>> n) | (x << (32 - n));
	for (let offset = 0; offset < total; offset += 64) {
		for (let i = 0; i < 16; i++) w[i] = view.getUint32(offset + i * 4);
		for (let i = 16; i < 64; i++) {
			const s0 = rotr(w[i - 15], 7) ^ rotr(w[i - 15], 18) ^ (w[i - 15] >>> 3);
			const s1 = rotr(w[i - 2], 17) ^ rotr(w[i - 2], 19) ^ (w[i - 2] >>> 10);
			w[i] = (w[i - 16] + s0 + w[i - 7] + s1) >>> 0;
		}
		let a = h0, b = h1, c = h2, d = h3, e = h4, f = h5, g = h6, h = h7;
		for (let i = 0; i < 64; i++) {
			const t1 = (h + (rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25)) + ((e & f) ^ (~e & g)) + SHA256_K[i] + w[i]) >>> 0;
			const t2 = ((rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22)) + ((a & b) ^ (a & c) ^ (b & c))) >>> 0;
			h = g; g = f; f = e; e = (d + t1) >>> 0;
			d = c; c = b; b = a; a = (t1 + t2) >>> 0;
		}
		h0 = (h0 + a) >>> 0; h1 = (h1 + b) >>> 0; h2 = (h2 + c) >>> 0; h3 = (h3 + d) >>> 0;
		h4 = (h4 + e) >>> 0; h5 = (h5 + f) >>> 0; h6 = (h6 + g) >>> 0; h7 = (h7 + h) >>> 0;
	}
	return [h0, h1, h2, h3, h4, h5, h6, h7].map(value => value.toString(16).padStart(8, "0")).join("");
}

async function sha256Hex(data) {
	const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
	if (crypto?.subtle) {
		const digest = await crypto.subtle.digest("SHA-256", bytes);
		return Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, "0")).join("");
	}
	return sha256JS(bytes);
}
