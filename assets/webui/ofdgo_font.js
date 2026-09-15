const DATABASE = "ofdgo";

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
