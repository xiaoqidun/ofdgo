const DATABASE = "ofdgo";

export class FontPicker {
	constructor(input, toggle, list, onChange, onOpen) {
		Object.assign(this, { input, toggle, list, onChange, onOpen, fonts: [], value: "", active: -1, open: false });
		input.addEventListener("input", () => this.show(input.value));
		input.addEventListener("focus", () => input.select());
		input.addEventListener("click", () => { if (!this.open) this.show(); });
		input.addEventListener("keydown", event => this.keyDown(event));
		input.addEventListener("blur", () => { this.commitExact(); this.close(); });
		toggle.addEventListener("pointerdown", event => event.preventDefault());
		toggle.addEventListener("click", () => {
			if (this.open) this.close();
			else { input.focus(); this.show(); }
		});
		list.addEventListener("pointerdown", event => {
			if (event.target.closest("[role=option]")) event.preventDefault();
		});
		list.addEventListener("click", event => {
			const option = event.target.closest("[role=option]");
			if (option) this.choose(Number(option.dataset.index));
		});
		window.addEventListener("resize", () => this.close());
	}

	setFonts(fonts, selected) {
		this.close();
		this.fonts = fonts;
		const key = font => font?.id || font?.postscriptName;
		this.value = fonts.length ? String(Math.max(0, fonts.findIndex(font => key(font) === key(selected)))) : "";
		this.restore();
	}

	setDisabled(disabled) {
		this.input.disabled = this.toggle.disabled = disabled;
		if (disabled) this.close();
	}

	restore() {
		const font = this.fonts[Number(this.value)];
		this.input.value = font?.fullName || font?.name || "";
		this.input.setAttribute("aria-expanded", String(this.open));
		this.input.removeAttribute("aria-activedescendant");
	}

	show(query = "") {
		if (this.input.disabled) return;
		const normalize = value => value.normalize("NFKC").toLocaleLowerCase().replace(/\s+/g, " ").trim();
		const terms = normalize(query).split(" ").filter(Boolean);
		this.matches = this.fonts.map((font, index) => ({ font, index })).filter(({ font }) => {
			const name = normalize([font.fullName, font.name, font.family, font.style, font.postscriptName, ...(font.names || [])].filter(Boolean).join(" "));
			return terms.every(term => name.includes(term));
		});
		this.list.replaceChildren();
		for (const { font, index } of this.matches) {
			const option = document.createElement("div");
			option.id = `${this.list.id}-${index}`;
			option.dataset.index = String(index);
			option.setAttribute("role", "option");
			option.setAttribute("aria-disabled", String(Boolean(font.disabled)));
			option.textContent = font.fullName || font.name;
			this.list.append(option);
		}
		if (!this.matches.length) {
			const empty = document.createElement("div");
			empty.className = "font-search-empty";
			empty.textContent = "无匹配字体";
			this.list.append(empty);
		}
		if (!this.open) this.list.showPopover();
		this.open = true;
		this.input.setAttribute("aria-expanded", "true");
		this.position();
		const selected = this.matches.findIndex(({ font, index }) => !font.disabled && String(index) === this.value);
		this.activate(selected < 0 ? this.matches.findIndex(({ font }) => !font.disabled) : selected);
		this.onOpen?.();
	}

	position() {
		if (!this.open) return;
		const rect = this.input.getBoundingClientRect();
		if (rect.right <= 0 || rect.left >= window.innerWidth) { this.close(); return; }
		const width = Math.min(Math.max(rect.width, 260), window.innerWidth - 16);
		Object.assign(this.list.style, { left: `${Math.max(8, Math.min(rect.left, window.innerWidth - width - 8))}px`,
			top: `${rect.bottom + 4}px`, width: `${width}px`, maxHeight: `${Math.max(40, Math.min(280, window.innerHeight - rect.bottom - 12))}px` });
	}

	activate(index) {
		this.active = index;
		[...this.list.children].forEach((option, i) => option.setAttribute("aria-selected", String(i === index)));
		const option = this.list.children[index];
		if (option) {
			this.input.setAttribute("aria-activedescendant", option.id);
			option.scrollIntoView({ block: "nearest" });
		} else this.input.removeAttribute("aria-activedescendant");
	}

	choose(index) {
		if (!this.fonts[index] || this.fonts[index].disabled) return;
		const changed = this.value !== String(index);
		this.value = String(index);
		this.close();
		if (changed) return this.onChange();
	}

	commitExact() {
		const value = this.input.value.trim().toLocaleLowerCase();
		const matches = font => font && !font.disabled
			&& [font.fullName, font.name, font.postscriptName, ...(font.names || [])].some(name => name?.toLocaleLowerCase() === value);
		const selected = Number(this.value);
		const index = matches(this.fonts[selected]) ? selected : this.fonts.findIndex(matches);
		if (index >= 0) this.choose(index);
	}

	close() {
		if (this.open) { this.open = false; this.list.hidePopover(); }
		this.restore();
	}

	keyDown(event) {
		if (event.isComposing) return;
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
	constructor({ onChange, onPermissionChange }) {
		this.userFonts = [];
		this.localFonts = [];
		this.catalog = [];
		this.catalogLoaded = false;
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
			const digest = await crypto.subtle.digest("SHA-256", font.data);
			const checksum = Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("");
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

	async queryLocal() {
		try {
			const available = await window.queryLocalFonts();
			this.catalog = available;
			this.catalogLoaded = true;
			this.permission = "granted";
			this.onPermissionChange();
			return available;
		} catch (err) {
			if (err && err.name === "NotAllowedError") {
				this.permission = "denied";
				this.onPermissionChange();
			}
			throw err;
		}
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

	async loadFaces(inspect) {
		for (const file of this.userFonts) {
			if (!file.enabled || file.faces) continue;
			try { file.faces = await inspect(await this.read(file)); }
			catch { file.faces = []; }
		}
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

	canReadLocal() {
		return typeof window.queryLocalFonts === "function";
	}

	async refreshPermission() {
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
