import { setInputFiles } from "./ofdgo_util.js";

const engineRuntimes = new Map();
const READ_ONLY_OPERATIONS = new Set([
	"ofdgoActionList", "ofdgoAnnotationInfos", "ofdgoCheckDocument", "ofdgoCheckTextFont", "ofdgoDestinationView",
	"ofdgoDistributionAxes", "ofdgoDocumentInfo", "ofdgoDocumentNavigation", "ofdgoDocumentVersions", "ofdgoEditorFont",
	"ofdgoEditorInfo", "ofdgoExportAttachment", "ofdgoExportDocument", "ofdgoExportFormats", "ofdgoExportImages",
	"ofdgoExportInvoice", "ofdgoExportMedia", "ofdgoExportPage", "ofdgoExportResource", "ofdgoExportVersion",
	"ofdgoFontFace", "ofdgoFontFaces", "ofdgoFontMenu", "ofdgoFontPreferenceOrder", "ofdgoFontPreview",
	"ofdgoGetRichText", "ofdgoImagePreview", "ofdgoImages", "ofdgoInvoice", "ofdgoInvoices",
	"ofdgoLoadPDF", "ofdgoMatchFontFiles", "ofdgoOpenLinks", "ofdgoOutlineLinks", "ofdgoOutputFormats",
	"ofdgoPackageDocuments", "ofdgoPageSettings", "ofdgoPagesText", "ofdgoParsePageRange", "ofdgoPreviewImage",
	"ofdgoPreviewRichText", "ofdgoPreviewSignatureSeal", "ofdgoPreviewText", "ofdgoPreviewTextSelection", "ofdgoPrintPDF",
	"ofdgoPrintSide", "ofdgoReadAnnotation", "ofdgoRenderBackends", "ofdgoRenderPage", "ofdgoResourceLocations",
	"ofdgoResourceReferences", "ofdgoResourceTargets", "ofdgoResources", "ofdgoRichTextFonts", "ofdgoSVGFontData",
	"ofdgoSearchPage", "ofdgoSearchPages", "ofdgoSignaturePlacement", "ofdgoStructureObjects", "ofdgoStructureTargets",
	"ofdgoStructureXML", "ofdgoStyleResource", "ofdgoStyleTextFonts", "ofdgoTextSelection", "ofdgoValidateInvoice",
]);
const PDF_REPLACE_OPERATIONS = new Set([
	"ofdgoOpen", "ofdgoOpenPDF", "ofdgoCreateDocument",
]);
let engineModule = null;
let engineScheduler = null;
let engineSequence = 0;

function replacesPDFSession(name, args) {
	return PDF_REPLACE_OPERATIONS.has(name) || name === "ofdgoImportImages" && Number(args?.[2]) < 0;
}

class BrowserScheduler {
	constructor() {
		this.budget = Math.max(256, Math.min(2048, (navigator.deviceMemory || 2) * 256)) * 1024 * 1024;
		this.capacity = Math.max(1, Math.min((navigator.hardwareConcurrency || 2) - 1, Math.floor(this.budget / (128 << 20))));
		this.limit = Math.min(this.capacity, Math.max(1, Math.floor(this.budget / (512 << 20))));
		this.workers = new Set();
		this.queue = [];
		this.sequence = 0;
	}

	get memory() {
		return [...this.workers].reduce((total, worker) => total + Math.max(worker.memory, worker.reservation), 0);
	}

	active() {
		return [...this.workers].reduce((total, worker) => total + new Set([...worker.operations.values()].map(data => data.engineID)).size, 0);
	}

	owner(worker, data) {
		return worker.shared ? worker.sessions.get(data.engineID) : worker;
	}

	priority(task) {
		const base = Number(task.worker.batch || this.owner(task.worker, task.data)?.foreground === false) * 10;
		const data = task.data;
		if (typeof data.priority === "number") return base + data.priority;
		if (data.name === "ofdgoRenderPage") {
			if (typeof data.args?.[7] === "number") return base + data.args[7];
			return base + (data.args?.[6] ? 5 : data.preload ? 6 : 2);
		}
		if (data.background) return base + 7;
		if (data.preload) return base + 6;
		return base + 1;
	}

	submit(worker, data, transfer) {
		if (data.preload && this.owner(worker, data)?.pagesPaused) {
			worker.receive({data:{engineID:data.engineID, id:data.id, ok:false, canceled:true, error:"预加载已暂停"}});
			return;
		}
		this.queue.push({worker, data, transfer, queued:performance.now(), order:++this.sequence});
		this.drain();
	}

	send(task) {
		const {worker, data, transfer} = task;
		worker.requests.set(requestKey(worker, data), performance.now());
		worker.operations.set(requestKey(worker, data), data);
		try { worker.worker.postMessage(data, transfer); }
		catch (error) {
			worker.requests.delete(requestKey(worker, data));
			worker.operations.delete(requestKey(worker, data));
			worker.receive({data:{engineID:data.engineID, id:data.id, ok:false, error:error.message}});
		}
	}

	complete(worker, data) {
		if (data.memory) worker.memory = data.memory;
		if (data.id == null || data.type) return;
		const key = requestKey(worker, data);
		const started = worker.requests.get(key);
		const elapsed = data.elapsed ?? (started === undefined ? 0 : performance.now() - started);
		const operation = worker.operations.get(key), owner = this.owner(worker, data);
		if (worker.primary && started !== undefined && data.ok && owner) {
			if (operation?.name === "ofdgoOpen") owner.openTime = elapsed;
			if (operation?.name === "ofdgoRenderPage") owner.pageTime = owner.pageTime ? owner.pageTime * 0.75 + elapsed * 0.25 : elapsed;
		}
		worker.requests.delete(key);
		worker.operations.delete(key);
		if (this.memory > this.budget) this.limit = Math.max(1, this.limit - 1);
		else if (started !== undefined && data.ok && elapsed > 100 && this.memory + Math.max(128 << 20, worker.memory) < this.budget) this.limit = Math.min(this.capacity, this.limit + 1);
	}

	drain() {
		if (this.draining) return;
		this.draining = true;
		try {
			if (this.queue.length) for (const worker of [...this.workers].sort((a,b) => b.memory-a.memory)) {
				if (this.memory + (64 << 20) <= this.budget) break;
				if (!worker.requests.size) worker.reclaim?.();
			}
			this.queue.sort((a, b) => this.priority(a) - this.priority(b) || a.order - b.order);
			for (let i = 0; i < this.queue.length;) {
				const task = this.queue[i];
				if (task.worker.closed) { this.queue.splice(i, 1); continue; }
				const taskReadOnly = READ_ONLY_OPERATIONS.has(task.data.name);
				if (this.queue.some(previous => previous.worker === task.worker && previous.data.engineID === task.data.engineID && previous.order < task.order && (!taskReadOnly || !READ_ONLY_OPERATIONS.has(previous.data.name)))) { i++; continue; }
				const busy = [...task.worker.operations.values()].some(data => data.engineID === task.data.engineID && data.name !== "ofdgoSaveSnapshot");
				const snapshot = task.data.name === "ofdgoSaveSnapshot" && [...task.worker.operations.values()].some(data => data.engineID === task.data.engineID && data.name === "ofdgoSaveSnapshot");
				const active = this.active();
				const interactive = task.worker.primary && this.owner(task.worker, task.data)?.foreground === true && !task.data.preload && !task.data.background;
				const foregroundActive = [...this.workers].some(worker => [...worker.operations.values()].some(data => data.name !== "ofdgoSaveSnapshot" && worker.primary && this.owner(worker, data)?.foreground === true && !data.preload && !data.background));
				const continuing = task.worker.primary && [...task.worker.operations.values()].some(data => data.engineID === task.data.engineID);
				const available = continuing || active < this.limit || interactive && !foregroundActive && active < this.limit + 1;
				if (busy || snapshot || !available || !task.worker.primary && active && this.memory + (64 << 20) > this.budget) { i++; continue; }
				this.queue.splice(i, 1);
				this.send(task);
			}
		} finally { this.draining = false; }
	}

	cancel(worker, data) {
		const index = this.queue.findIndex(task => task.worker === worker && requestKey(worker, task.data) === requestKey(worker, data));
		if (index < 0) return false;
		const task = this.queue.splice(index, 1)[0];
		worker.receive({data:{engineID:task.data.engineID, id:data.id, ok:false, canceled:true, error:"操作已取消"}});
		this.drain();
		return true;
	}

	pause(worker, engineID) {
		const paused = this.queue.filter(task => task.worker === worker && task.data.engineID === engineID && task.data.preload);
		this.queue = this.queue.filter(task => !paused.includes(task));
		for (const task of paused) worker.receive({data:{engineID, id:task.data.id, ok:false, canceled:true, error:"预加载已暂停"}});
	}

	cancelPDF(worker, engineID) {
		const queued = this.queue.filter(task => task.worker === worker && task.data.engineID === engineID && task.data.name === "ofdgoLoadPDF");
		if (queued.length) {
			this.queue = this.queue.filter(task => !queued.includes(task));
			for (const task of queued) worker.receive({data:{engineID, id:task.data.id, ok:false, canceled:true, error:"操作已取消"}});
		}
		for (const data of worker.operations.values()) {
			if (data.engineID === engineID && data.name === "ofdgoLoadPDF") worker.worker.postMessage({type:"cancel", engineID, id:data.id});
		}
	}

	remove(worker) {
		this.workers.delete(worker);
		this.queue = this.queue.filter(task => task.worker !== worker);
		worker.requests.clear();
		worker.operations.clear();
		this.drain();
	}
}

class BrowserWorker {
	constructor(options) {
		engineScheduler ||= new BrowserScheduler();
		this.primary = !!options.primary;
		this.shared = !!options.shared;
		this.engineID = this.shared ? null : `engine-${++engineSequence}`;
		this.sessions = new Map();
		this.closingSessions = new Set();
		this.batch = !options.primary && !options.page;
		this.requests = new Map();
		this.operations = new Map();
		this.foreground = !this.batch;
		this.pagesPaused = false;
		this.memory = 64 << 20;
		this.reservation = 0;
		this.closed = false;
		this.url = String(options.url || "./ofdgo_wasm.js");
		this.worker = new Worker(this.url);
		engineScheduler.workers.add(this);
		this.worker.onmessage = event => {
			if (this.closed || event.data.type === "module") return;
			engineScheduler.complete(this, event.data);
			if (event.data.type === "exit" && (!this.shared || !event.data.engineID)) this.terminate();
			try { this.receive(event); }
			finally { engineScheduler.drain(); }
		};
		this.worker.onerror = event => { this.terminate(); this.fail("onerror", event); };
		this.worker.onmessageerror = event => { this.terminate(); this.fail("onmessageerror", event); };
		this.worker.addEventListener("message", event => {
			if (event.data.type !== "module") return;
			event.stopImmediatePropagation();
			engineModule ||= WebAssembly.compileStreaming(fetch("./ofdgo.wasm")).catch(error => { engineModule = null; throw error; });
			engineModule.then(module => { if (!this.closed) this.worker.postMessage({ type:"module", module }); }, error => { if (!this.closed) this.worker.postMessage({ type:"module", error:error.message }); });
		});
		if (!this.shared) this.worker.postMessage({type:"open", engineID:this.engineID});
	}

	receive(event) {
		if (!event.data.engineID && !["progress", "exit"].includes(event.data.type)) return;
		if (!this.shared) {
			if (event.data.engineID && event.data.engineID !== this.engineID) return;
			this.onmessage?.(event);
			return;
		}
		if (event.data.type === "closed") {
			if (!this.closingSessions.delete(event.data.engineID)) return;
			for (const [key, data] of this.operations) if (data.engineID === event.data.engineID) {
				this.requests.delete(key);
				this.operations.delete(key);
			}
			if (!this.sessions.size && !this.closingSessions.size) this.terminate();
			return;
		}
		const targets = event.data.engineID ? [this.sessions.get(event.data.engineID)] : [...this.sessions.values()];
		for (const session of targets) session?.receive(event);
	}

	fail(name, event) {
		if (!this.shared) { this[name]?.(event); return; }
		for (const session of [...this.sessions.values()]) {
			session.terminate();
			session[name]?.(event);
		}
	}

	postMessage(data, transfer) {
		if (this.closed) throw new DOMException("引擎已关闭", "AbortError");
		if (!this.shared) data = {...data, engineID:this.engineID};
		if (data.type) {
			if (data.type === "cancel" && engineScheduler.cancel(this, data)) return;
			this.worker.postMessage(data, transfer);
		} else engineScheduler.submit(this, data, transfer);
	}

	setForeground(foreground) {
		this.foreground = !!foreground;
		this.pausePages(!this.foreground);
	}

	pausePages(paused = true) {
		this.pagesPaused = !!paused;
		if (this.pagesPaused) engineScheduler.pause(this, this.engineID);
		engineScheduler.drain();
	}

	terminate() {
		if (this.closed) return;
		this.closed = true;
		if (engineRuntimes.get(this.url) === this) engineRuntimes.delete(this.url);
		this.closingSessions.clear();
		this.worker.terminate();
		engineScheduler.remove(this);
	}
}

class BrowserSession {
	constructor(options) {
		const url = String(options.url || "./ofdgo_wasm.js");
		let runtime = engineRuntimes.get(url);
		if (!runtime || runtime.closed) {
			runtime = new BrowserWorker({primary:true, shared:true, url});
			engineRuntimes.set(url, runtime);
		}
		this.runtime = runtime;
		this.engineID = `engine-${++engineSequence}`;
		this.requests = new Set();
		this.foreground = true;
		this.pagesPaused = false;
		this.closed = false;
		runtime.sessions.set(this.engineID, this);
		runtime.postMessage({type:"open", engineID:this.engineID});
	}

	receive(event) {
		if (this.closed) return;
		if (event.data.id != null && !event.data.type) this.requests.delete(event.data.id);
		if (event.data.type === "exit") this.terminate();
		this.onmessage?.(event);
	}

	postMessage(data, transfer) {
		if (this.closed) throw new DOMException("引擎已关闭", "AbortError");
		if (!data.type) this.requests.add(data.id);
		this.runtime.postMessage({...data, engineID:this.engineID}, transfer);
	}

	setForeground(foreground) {
		this.foreground = !!foreground;
		this.pausePages(!this.foreground);
	}

	pausePages(paused = true) {
		this.pagesPaused = !!paused;
		if (this.pagesPaused) engineScheduler.pause(this.runtime, this.engineID);
		engineScheduler.drain();
	}

	terminate() {
		if (this.closed) return;
		this.closed = true;
		this.runtime.sessions.delete(this.engineID);
		engineScheduler.queue = engineScheduler.queue.filter(task => task.worker !== this.runtime || task.data.engineID !== this.engineID);
		this.requests.clear();
		if (!this.runtime.closed) {
			this.runtime.closingSessions.add(this.engineID);
			this.runtime.postMessage({type:"close", engineID:this.engineID});
		}
		engineScheduler.drain();
	}
}

class BrowserEngine {
	constructor(options) {
		this.primary = createEngineWorker({primary:true, url:options.url});
		this.engineID = this.primary.engineID;
		this.workerURL = options.url;
		this.primary.onmessage = event => {
			const data = event.data;
			if (data.memory) this.memory = data.memory;
			if (this.source) this.parallelism = engineScheduler.limit;
			const rendered = data.id != null && !data.type && this.requests.delete(data.id);
			this.onmessage?.(event);
			if (rendered && data.ok) this.dispatchPages();
		};
		this.primary.onerror = event => this.onerror?.(event);
		this.primary.onmessageerror = event => this.onmessageerror?.(event);
		this.requests = new Map();
		this.fontRequests = new Map();
		this.primaryPages = new Set();
		this.slots = new Set();
		this.sequence = 0;
		this.revision = 0;
		this.memory = 0;
		this.parallelism = 1;
		this.budget = engineScheduler.budget;
		this.closed = false;
		this.source = null;
		this.pageSource = null;
		this.foreground = true;
		this.pagesPaused = false;
	}

	configurePages(source) {
		const current = this.pageSource;
		const fonts = (source?.fonts || []).map(font => font?.data !== undefined ? {name:font.name, data:font.data} : font);
		const sameNames = JSON.stringify(source?.fontNames || []) === JSON.stringify(current?.fontNames || []);
		const sameFonts = current && fonts.length === current.fonts.length && fonts.every((font, index) => font === current.fonts[index] || font?.data !== undefined && font.name === current.fonts[index]?.name && font.data === current.fonts[index]?.data);
		if (source && current && source.file === current.file && !!source.pdf === !!current.pdf && (source.document || 0) === current.document && (source.version || "") === current.version && source.annotations === current.annotations && source.revision === current.revision && sameFonts && sameNames) return;
		this.invalidatePages();
		if (!source || this.closed) return;
		this.pageSource = {...source, fonts, document:source.document || 0, version:source.version || ""};
		this.source = {...source, file: source.file instanceof Blob ? source.file : new Blob([source.file])};
		this.parallelism = engineScheduler.limit;
	}

	setForeground(foreground) {
		this.foreground = !!foreground;
		this.primary.setForeground(this.foreground);
		for (const slot of this.slots) slot.worker.setForeground(this.foreground);
		this.pausePages(!this.foreground);
	}

	pausePages(paused = true) {
		this.pagesPaused = !!paused;
		this.primary.pausePages(this.pagesPaused);
		for (const slot of this.slots) slot.worker.pausePages(this.pagesPaused);
	}

	invalidatePages() {
		this.revision++;
		this.primaryPages.clear();
		this.source = null;
		this.pageSource = null;
		this.parallelism = 1;
		this.startupTime = 0;
		this.primary.pageTime = 0;
		for (const slot of this.slots) this.closeSlot(slot);
	}

	closeSlot(slot, error = new DOMException("页面加载已取消", "AbortError")) {
		if (!this.slots.delete(slot)) return;
		slot.worker.terminate();
		for (const [id, request] of this.fontRequests) if (request.worker === slot.worker) this.fontRequests.delete(id);
		slot.rejectReady(error);
		for (const pending of slot.pending.values()) pending.reject(error);
		slot.pending.clear();
	}

	createSlot(source) {
		const started = performance.now();
		const worker = createEngineWorker({page:true, url:this.workerURL});
		worker.setForeground(this.foreground);
		worker.pausePages(this.pagesPaused);
		const slot = {worker, pending: new Map(), busy: false, memory: Math.max(128 << 20, this.memory), pages:new Set(), lastPage:0};
		worker.reservation = slot.memory;
		worker.reclaim = () => { if (!slot.busy) this.closeSlot(slot); };
		const ready = new Promise((resolve, reject) => {
			slot.rejectReady = reject;
			worker.onmessage = ({data}) => {
				if (data.memory) slot.memory = data.memory;
				if (this.source) this.parallelism = engineScheduler.limit;
				if (data.type === "ready") { resolve(); return; }
				if (data.type === "exit") { this.closeSlot(slot, new Error("页面加载失败")); return; }
				if (data.type === "font") {
					const request = `${slot.worker.engineID}:${data.request}`;
					this.fontRequests.set(request, {worker:slot.worker, request:data.request});
					this.onmessage?.({data:{...data, id:slot.requestID, request}});
					return;
				}
				if (data.type) return;
				const request = slot.pending.get(data.id);
				if (!request) return;
				slot.pending.delete(data.id);
				if (data.ok) request.resolve(data);
				else request.reject(Object.assign(new Error(data.error), {code:data.code, name:data.canceled ? "AbortError" : "Error"}));
			};
			worker.onerror = worker.onmessageerror = () => this.closeSlot(slot, new Error("页面加载失败"));
		});
		this.slots.add(slot);
		slot.ready = ready.then(async () => {
			let opened = source.pdf
				? await this.callSlot(slot, "ofdgoOpenPDF", [source.file, source.fonts, null, source.annotations, source.fontNames])
				: await this.callSlot(slot, "ofdgoOpen", [source.file, source.fonts, source.annotations]);
			if ((source.document || 0) !== (opened.data.documentIndex || 0)) opened = await this.callSlot(slot, "ofdgoSelectDocument", [source.document]);
			if ((source.version || "") !== (opened.data.documentVersion || "")) await this.callSlot(slot, "ofdgoSelectVersion", [source.version || ""]);
			if (this.slots.has(slot)) this.startupTime = performance.now() - started;
		});
		slot.ready.catch(() => {});
		return slot;
	}

	callSlot(slot, name, args, preload = false) {
		return new Promise((resolve, reject) => {
			if (!this.slots.has(slot)) { reject(new DOMException("页面加载已取消", "AbortError")); return; }
			const id = --this.sequence;
			slot.pending.set(id, {resolve, reject});
			try { slot.worker.postMessage({id, name, args, preload}); }
			catch (error) { slot.pending.delete(id); reject(error); }
		});
	}

	pageSlot(data) {
		if (this.primaryPages.has(data.args[0])) return null;
		const preferred = [...this.slots].find(slot => !slot.busy && slot.pages.has(data.args[0]));
		const pending = [...this.requests.values()].filter(slot => slot === null).length - Number(this.requests.has(data.id));
		if (!preferred && pending <= 0) return null;
		let slot = preferred || [...this.slots].filter(slot => !slot.busy).sort((a,b) => Math.abs(a.lastPage-data.args[0])-Math.abs(b.lastPage-data.args[0]))[0];
		const worthwhile = (this.primary.pageTime || 0) * pending >= Math.max(50, this.startupTime || this.primary.openTime || 0);
		if (!slot && worthwhile && this.foreground && !this.pagesPaused && this.slots.size < this.parallelism - 1
			&& engineScheduler.memory + Math.max(128 << 20, this.memory) <= this.budget) slot = this.createSlot(this.source);
		return slot;
	}

	dispatchPages() {
		if (this.closed || !this.source || !this.foreground || this.pagesPaused) return;
		const draining = engineScheduler.draining;
		engineScheduler.draining = true;
		try {
			const queued = engineScheduler.queue
				.filter(task => task.worker === this.primary.runtime && task.data.engineID === this.engineID)
				.sort((a, b) => a.order - b.order);
			const candidates = [];
			for (const task of queued) {
				if (!READ_ONLY_OPERATIONS.has(task.data.name)) break;
				if (task.data.name === "ofdgoRenderPage") candidates.push(task);
			}
			candidates.sort((a, b) => engineScheduler.priority(a) - engineScheduler.priority(b) || a.order - b.order);
			for (const task of candidates) {
				if (this.requests.get(task.data.id) !== null) continue;
				if (this.primaryPages.has(task.data.args[0])) continue;
				let slot;
				try { slot = this.pageSlot(task.data); } catch { break; }
				if (!slot) break;
				const index = engineScheduler.queue.indexOf(task);
				if (index >= 0) engineScheduler.queue.splice(index, 1);
				this.primary.requests.delete(task.data.id);
				this.renderSlot(slot, task.data, task.transfer);
			}
		} finally { engineScheduler.draining = draining; }
	}

	postMessage(data, transfer) {
		if (this.closed) return;
		if (data.type === "font" && typeof data.request === "string") {
			const request = this.fontRequests.get(data.request);
			if (request) {
				this.fontRequests.delete(data.request);
				request.worker.postMessage({...data, request:request.request}, transfer);
			}
			return;
		}
		if (!data.type && data.preload && this.pagesPaused) {
			this.onmessage?.({data:{engineID:this.engineID, id:data.id, ok:false, canceled:true, error:"预加载已暂停"}});
			return;
		}
		if (data.type === "cancel") {
			const slot = this.requests.get(data.id);
			if (slot && slot !== this.primary) this.closeSlot(slot);
			else this.primary.postMessage(data);
			return;
		}
		if (!data.type && !READ_ONLY_OPERATIONS.has(data.name)) {
			if (replacesPDFSession(data.name, data.args)) engineScheduler.cancelPDF(this.primary.runtime, this.engineID);
			this.invalidatePages();
		}
		const slot = data.name === "ofdgoRenderPage" && this.source ? this.pageSlot(data) : null;
		if (!slot) {
			if (data.name === "ofdgoRenderPage") this.requests.set(data.id, null);
			this.primary.postMessage(data, transfer);
			return;
		}
		this.renderSlot(slot, data, transfer);
	}

	renderSlot(slot, data, transfer) {
		slot.busy = true;
		slot.requestID = data.id;
		this.requests.set(data.id, slot);
		const revision = this.revision;
		let fallback = false;
		slot.ready.then(() => this.callSlot(slot, data.name, data.args, !!data.preload)).then(result => {
			if (revision !== this.revision) throw new DOMException("页面加载已取消", "AbortError");
			const annotations = typeof result.data.annotations === "string" ? (JSON.parse(result.data.annotations || "null") || []) : (result.data.annotations || []);
			if (this.source.pdf && (annotations.length || result.data.links?.length)) {
				this.primaryPages.add(data.args[0]);
				this.requests.set(data.id, this.primary);
				fallback = true;
				this.primary.postMessage(data, transfer);
				return;
			}
			slot.lastPage = data.args[0];
			slot.pages.delete(slot.lastPage);
			slot.pages.add(slot.lastPage);
			if (slot.pages.size > 16) slot.pages.delete(slot.pages.values().next().value);
			if (!this.closed) this.onmessage?.({data: {...result, engineID:this.engineID, id: data.id}});
		}).catch(error => {
			if (!this.closed && revision === this.revision && error.name !== "AbortError") {
				this.closeSlot(slot, error);
				this.parallelism = Math.max(1, this.slots.size + 1);
				this.requests.set(data.id, this.primary);
				fallback = true;
				this.primary.postMessage(data, transfer);
				return;
			}
			if (!this.closed) this.onmessage?.({data: {engineID:this.engineID, id: data.id, ok: false, error: error.message, canceled: error.name === "AbortError"}});
		}).finally(() => {
			if (!fallback) this.requests.delete(data.id);
			slot.busy = false;
			slot.worker.reservation = 0;
			const memory = engineScheduler.memory;
			if (memory > this.budget) { this.closeSlot(slot); this.parallelism = Math.max(1, this.slots.size + 1); }
			this.dispatchPages();
			engineScheduler.drain();
		});
	}

	terminate() {
		if (this.closed) return;
		this.closed = true;
		this.invalidatePages();
		this.primary.terminate();
		this.requests.clear();
	}
}

class NativeFile {
	constructor(value) {
		Object.assign(this, value);
		if (value.handle) this.handle = new NativeHandle(nativeHost, value.handle);
		this.byteLength = this.size;
		this.type ||= "application/octet-stream";
		if (value.nativeFile) nativeHost?.resources?.register(this, {file: value.nativeFile}, this);
	}

	async arrayBuffer(signal) {
		const response = await fetch(this.url, {signal});
		if (!response.ok) throw new Error("读取文件失败");
		return response.arrayBuffer();
	}

	toJSON() {
		return {nativeFile:this.nativeFile, size:this.size, name:this.name};
	}

	async text() {
		return new TextDecoder().decode(await this.arrayBuffer());
	}
}

class NativeHandle {
	constructor(host, value) {
		this.host = host;
		Object.assign(this, value);
		if (value.nativeHandle) host?.resources?.register(this, {handle: value.nativeHandle}, this);
	}

	toJSON() {
		return {nativeHandle:this.nativeHandle, name:this.name, kind:this.kind};
	}

	async getFile() {
		return new NativeFile(await this.host.call("file", {handle:this.nativeHandle}));
	}

	async getFileHandle(name, options = {}) {
		return new NativeHandle(this.host, await this.host.call("child", {handle:this.nativeHandle, name, create:!!options.create, directory:false}));
	}

	async getDirectoryHandle(name, options = {}) {
		return new NativeHandle(this.host, await this.host.call("child", {handle:this.nativeHandle, name, create:!!options.create, directory:true}));
	}

	removeEntry(name, options = {}) {
		return this.host.call("remove", {handle:this.nativeHandle, name, recursive:!!options.recursive});
	}

	async createWritable() {
		const {writer} = await this.host.call("write.begin", {handle:this.nativeHandle});
		return {
			write: data => this.host.call("write.data", {writer, data}),
			close: () => this.host.call("write.commit", {writer}),
			abort: () => this.host.call("write.abort", {writer}),
		};
	}
}

class NativeHost {
	constructor(bridge) {
		this.bridge = bridge;
		this.parallelism = 1;
		this.cacheBytes = 192 * 1024 * 1024;
		this.reading = 0;
		this.readSeq = 0;
		this.readQueue = [];
		this.resources = typeof FinalizationRegistry === "function" ? new FinalizationRegistry(entry => {
			this.call("release", entry).catch(() => {});
		}) : null;
	}

	call(name, value, signal) {
		return this.bridge.call(`ofdgo.host.${name}`, value, {signal});
	}

	async readResource(file, signal, priority = 3) {
		let acquired = false;
		try {
			signal?.throwIfAborted();
			if (this.reading >= this.parallelism) await new Promise((resolve, reject) => {
				const entry = {
					priority,
					order: ++this.readSeq,
					next: () => {
						signal?.removeEventListener("abort", cancel);
						acquired = true;
						resolve();
					},
				};
				const cancel = () => {
					const index = this.readQueue.indexOf(entry);
					if (index >= 0) this.readQueue.splice(index, 1);
					reject(signal.reason);
				};
				this.readQueue.push(entry);
				signal?.addEventListener("abort", cancel, {once:true});
			});
			else {
				this.reading++;
				acquired = true;
			}
			signal?.throwIfAborted();
			return await file.arrayBuffer(signal);
		} finally {
			await this.call("release", {file:file.nativeFile}).catch(() => {});
			if (acquired) {
				if (this.readQueue.length) {
					let best = 0;
					for (let index = 1; index < this.readQueue.length; index++) {
						const candidate = this.readQueue[index], current = this.readQueue[best];
						if (candidate.priority < current.priority || candidate.priority === current.priority && candidate.order < current.order) best = index;
					}
					const [entry] = this.readQueue.splice(best, 1);
					entry.next();
				} else this.reading--;
			}
		}
	}

	async decode(value, signal, priority = 3) {
		const reads = [];
		const visit = (item, field, replace) => {
			if (!item || typeof item !== "object" || item instanceof Blob || item instanceof ArrayBuffer || ArrayBuffer.isView(item)) return;
			if (item.nativeFile) {
				const file = new NativeFile(item);
				if (field === "file") replace(file);
				else reads.push(this.readResource(file, signal, priority).then(bytes => replace(field === "blob" ? new Blob([bytes], {type:item.type}) : new Uint8Array(bytes))));
				return;
			}
			for (const [child, content] of Object.entries(item)) {
				if (content && typeof content === "object") visit(content, Array.isArray(item) ? "" : child, result => item[child] = result);
			}
		};
		visit(value, "", result => value = result);
		const results = await Promise.allSettled(reads);
		const failed = results.find(result => result.status === "rejected");
		if (failed) throw failed.reason;
		return value;
	}

	async release(value, options = {}, visited = new Set()) {
		if (!value || typeof value !== "object" || value instanceof Blob || value instanceof ArrayBuffer || ArrayBuffer.isView(value) || value === this || visited.has(value)) return;
		visited.add(value);
		const temporaryOnly = !!options.temporaryOnly;
		const includeHandle = options.includeHandle !== false;
		const exclude = options.exclude;
		if (typeof value.svgUrl === "string" && value.svgUrl) {
			if (value.svgUrl.startsWith("blob:")) URL.revokeObjectURL(value.svgUrl);
			else await this.call("release", {svgUrl:value.svgUrl}).catch(() => {});
		}
		if (value.nativeHandle) {
			if (!temporaryOnly && !exclude?.has(value.nativeHandle)) {
				this.resources?.unregister(value);
				await this.call("release", {handle:value.nativeHandle}).catch(() => {});
			}
			return;
		}
		if (value.nativeFile) {
			if ((!temporaryOnly || value.temporary) && !exclude?.has(value.nativeFile)) {
				this.resources?.unregister(value);
				await this.call("release", {file:value.nativeFile}).catch(() => {});
			}
			if (!temporaryOnly && includeHandle && value.handle?.nativeHandle && !exclude?.has(value.handle.nativeHandle)) {
				this.resources?.unregister(value.handle);
				await this.call("release", {handle:value.handle.nativeHandle}).catch(() => {});
			}
			return;
		}
		const items = Array.isArray(value) ? value : Object.values(value);
		for (const item of items) await this.release(item, options, visited);
	}

	worker() {
		const host = this;
		let closed = false;
		let unsubscribe;
		let pending = Promise.resolve();
		let termination = null;
		const readers = new Set();
		const inputs = new Set();
		const requests = new Map();
		const completions = new Map();
		const emit = data => {
			if (data.type === "result") { completions.get(data.id)?.(data.result); return; }
			if (!closed) worker.onmessage?.({data:{...data, engineID:worker.engineID}});
		};
		const ready = host.call("engine.open", {}).then(({engine, parallelism, cacheBytes}) => {
			worker.engineID = engine;
			if (closed) return engine;
			host.parallelism = Math.max(1, parallelism || 1);
			host.cacheBytes = cacheBytes || host.cacheBytes;
			unsubscribe = host.bridge.on(`ofdgo.engine.${engine}`, emit);
			emit({type:"ready"});
			return engine;
		}).catch(error => { if (!closed) worker.onerror?.({message:error.message}); throw error; });
		const worker = {
			engineID:null,
			foreground:true,
			pagesPaused:false,
			onmessage:null,
			onerror:null,
			configurePages() {},
			setForeground(foreground) {
				worker.foreground = !!foreground;
				worker.pausePages(!worker.foreground);
			},
			pausePages(paused = true) {
				worker.pagesPaused = !!paused;
			},
			postMessage(data) {
				if (closed) return;
				if (data.type === "cancel") {
					requests.get(data.id)?.controller.abort();
					return;
				}
				if (data.type === "font") return;
				if (["ofdgoDetectFormat", "ofdgoOpen", "ofdgoOpenPDF"].includes(data.name)) {
					const input = data.args?.[0];
					if (input?.nativeFile && !input.temporary) inputs.add(input.nativeFile);
				}
				const parallel = READ_ONLY_OPERATIONS.has(data.name);
				if (replacesPDFSession(data.name, data.args)) {
					for (const request of requests.values()) {
						if (request.name === "ofdgoLoadPDF") request.controller.abort();
					}
				}
				const controller = new AbortController();
				requests.set(data.id, {controller, name:data.name});
				const priority = typeof data.priority === "number" ? data.priority
					: data.name === "ofdgoRenderPage" ? (typeof data.args?.[7] === "number" ? data.args[7] : data.args?.[6] ? 5 : 2)
					: data.background ? 7 : data.preload ? 6 : 3;
				const barrier = parallel ? pending : Promise.all([pending, ...readers]);
				const operation = barrier.then(async () => {
					const engine = await ready;
					if (closed) return;
					if (data.preload && worker.pagesPaused) throw new DOMException("预加载已暂停", "AbortError");
					controller.signal.throwIfAborted();
					const args = {engine, id:data.id, name:data.name, args:data.args};
					let result;
					if (data.name === "ofdgoConvertFile" || data.name === "ofdgoPackFiles") {
						const completed = new Promise(resolve => completions.set(data.id, resolve));
						const cancel = () => host.call("engine.cancel", {engine, id:data.id}).catch(() => {});
						controller.signal.addEventListener("abort", cancel, {once:true});
						try {
							await host.call("engine.start", args);
							if (controller.signal.aborted) cancel();
							result = await completed;
							await host.call("engine.finish", {engine, id:data.id});
						} finally {
							controller.signal.removeEventListener("abort", cancel);
							completions.delete(data.id);
						}
					} else result = await host.call("engine.call", args, controller.signal);
					if (closed) { await host.release(result, {temporaryOnly:true}); return; }
					let decoded;
					try {
						controller.signal.throwIfAborted();
						decoded = await host.decode(result, controller.signal, priority);
						controller.signal.throwIfAborted();
					}
					catch (error) { await host.release(result, {temporaryOnly:true}); throw error; }
					if (closed) { await host.release(result, {temporaryOnly:true}); return; }
					emit({id:data.id, ...decoded});
				}).catch(error => emit({id:data.id, ok:false, error:error.message, code:error.code, canceled:error.name === "AbortError"})).finally(() => requests.delete(data.id));
				if (parallel) {
					readers.add(operation);
					operation.finally(() => readers.delete(operation));
				} else if (data.name !== "ofdgoSaveSnapshot") pending = operation;
			},
			terminate() {
				if (closed) return termination;
				closed = true;
				for (const request of requests.values()) request.controller.abort();
				termination = ready.then(engine => host.call("engine.close", {engine})).catch(() => {}).finally(async () => {
					unsubscribe?.();
					for (const resolve of completions.values()) resolve({ok:false, canceled:true, error:"会话已关闭"});
					completions.clear();
					await Promise.all([...inputs].map(file => host.call("release", {file}).catch(() => {})));
					inputs.clear();
				});
				return termination;
			},
		};
		ready.catch(() => {});
		return worker;
	}

	async savePicker(options) {
		return new NativeHandle(this, await this.call("save", options));
	}

	async directoryPicker() {
		return new NativeHandle(this, await this.call("directory", {}));
	}

	async createTempDirectory() {
		return new NativeHandle(this, await this.call("temp.directory", {}));
	}

	removeTempDirectory(dir) {
		if (!dir) return Promise.resolve();
		return this.call("temp.remove", { id: dir.nativeHandle || dir });
	}

	async fonts() {
		return (await this.call("fonts", {})).map(({file, ...font}) => ({...font, nativeFile:new NativeFile(file)}));
	}

	async start(onFiles, onError) {
		let launching = Promise.resolve();
		this.bridge.on("ofdgo.launch", values => {
			launching = launching.then(async () => {
				try { await onFiles(values.map(value => new NativeFile(value)), true); }
				finally { await this.call("launch.done", {}); }
			}).catch(onError);
		});
		this.bridge.on("ofdgo.files", async values => {
			try { await onFiles(values.map(value => new NativeFile(value))); }
			catch (error) { onError(error); }
		});
		this.bridge.on("ofdgo.error", message => onError(new Error(message)));
		const title = document.querySelector("title");
		if (title) new MutationObserver(() => this.call("title", {title:document.title}).catch(() => {})).observe(title, {childList:true});
		await this.call("ready", {});
	}

	async discardFiles(files) {
		await Promise.all(files.filter(file => file.drop && file.nativeFile).map(file => this.call("drop.release", {file:file.nativeFile})));
	}

	installFileInputs(onError) {
		let opening = false;
		document.addEventListener("click", async event => {
			const input = event.target;
			if (!(input instanceof HTMLInputElement) || input.type !== "file" || input.disabled) return;
			event.preventDefault();
			if (opening) return;
			opening = true;
			try {
				const files = (await this.call("open", {multiple:input.multiple, directory:input.webkitdirectory, accept:input.accept})).map(value => new NativeFile(value));
				if (!files.length) { input.dispatchEvent(new Event("cancel", {bubbles:true})); return; }
				setInputFiles(input, files);
				input.dispatchEvent(new Event("input", {bubbles:true}));
				input.dispatchEvent(new Event("change", {bubbles:true}));
			} catch (error) {
				if (error.name === "AbortError") input.dispatchEvent(new Event("cancel", {bubbles:true}));
				else onError(error);
			} finally {
				opening = false;
			}
		}, true);
	}

	async download(data, mime, name) {
		const handle = await this.savePicker({suggestedName:name});
		try {
			const writer = await handle.createWritable();
			try { await writer.write(data); await writer.close(); }
			catch (error) { await writer.abort().catch(() => {}); throw error; }
		} finally {
			await this.release(handle);
		}
	}
}

export const nativeHost = globalThis.guigo ? new NativeHost(globalThis.guigo) : null;

export function engineLimits() {
	engineScheduler ||= new BrowserScheduler();
	return {parallelism: engineScheduler.capacity, memoryBytes: engineScheduler.budget};
}

export function createEngineWorker(options = {}) {
	if (nativeHost) return nativeHost.worker();
	if (options.page || options.batch) return new BrowserWorker(options);
	if (options.primary) return new BrowserSession(options);
	return new BrowserEngine(options);
}

export async function documentBytes(file) {
	if (file?.nativeFile) return file;
	if (!nativeHost && file instanceof File) return file;
	if (file instanceof Blob) return new Response(file.stream()).blob();
	return new Uint8Array(await file.arrayBuffer());
}

export function saveFilePicker(options) {
	return nativeHost ? nativeHost.savePicker(options) : window.showSaveFilePicker ? window.showSaveFilePicker(options) : null;
}

export function directoryPicker() {
	return nativeHost ? nativeHost.directoryPicker() : window.showDirectoryPicker({mode:"readwrite"});
}

function requestKey(worker, data) {
	return worker.shared ? JSON.stringify([data.engineID, data.id]) : data.id;
}
