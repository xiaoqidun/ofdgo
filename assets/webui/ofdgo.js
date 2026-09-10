const MM_TO_PX = 96 / 25.4;
const LOCAL_FONT_LOAD_LIMIT = 16;
const FONT_DATABASE = "ofdgo";
const fontChannel = typeof BroadcastChannel === "function" ? new BroadcastChannel(FONT_DATABASE) : null;
const COMPACT_LAYOUT = window.matchMedia("(max-width: 900px)");
const DEFAULT_IMAGE_DPI = 300;
const STATUS = {
	ready: "选择 OFD 文件",
	opening: "正在打开 OFD",
	engine: "正在准备引擎",
	recovering: "正在恢复引擎",
	fonts: "正在匹配字体",
	exporting: "正在导出文档",
	pageExporting: "正在导出单页",
};
const WASM_CALLBACKS = [
	"ofdgoOpen",
	"ofdgoConfigure",
	"ofdgoDocumentInfo",
	"ofdgoRenderPage",
	"ofdgoExportFormats",
	"ofdgoExportPage",
	"ofdgoExportPDF",
	"ofdgoFontFileMatches",
];

let wasmPromise = null;
let wasmModule = null;
let wasmRecoveryTimer = 0;
let fontDatabase = null;

const state = {
	ready: false,
	wasmExited: false,
	wasmSeq: 0,
	wasmRecovering: false,
	wasmRecoveries: 0,
	ofdBytes: null,
	fileName: "ofdgo.ofd",
	openSeq: 0,
	fontSeq: 0,
	localFonts: [],
	userFonts: [],
	fontSyncPending: false,
	fontSyncing: false,
	systemFontCatalog: [],
	systemFontCatalogLoaded: false,
	systemFontPermission: "prompt",
	doc: null,
	pageIndex: 0,
	scale: 1,
	fitMode: "width",
	continuous: false,
	renderAnnotations: true,
	pageCache: new Map(),
	pageInFlight: new Map(),
	pageRenderQueue: [],
	pageRenderRunning: false,
	pageObserver: null,
	visiblePages: new Set(),
	scrollFrame: 0,
	thumbnailCache: new Map(),
	thumbnailInFlight: new Set(),
	thumbnailObserver: null,
	visibleThumbnails: new Set(),
	exportFormats: [],
	showPages: !COMPACT_LAYOUT.matches,
	showMeta: !COMPACT_LAYOUT.matches,
};

const el = {
	ofdInput: document.querySelector("#ofdInput"),
	ofdButton: document.querySelector("#ofdButton"),
	fontInput: document.querySelector("#fontInput"),
	fontDirectoryInput: document.querySelector("#fontDirectoryInput"),
	togglePagesButton: document.querySelector("#togglePagesButton"),
	toggleMetaButton: document.querySelector("#toggleMetaButton"),
	fontAddButton: document.querySelector("#fontAddButton"),
	fontDirectoryButton: document.querySelector("#fontDirectoryButton"),
	localFontButton: document.querySelector("#localFontButton"),
	prevButton: document.querySelector("#prevButton"),
	nextButton: document.querySelector("#nextButton"),
	pageInput: document.querySelector("#pageInput"),
	pageTotal: document.querySelector("#pageTotal"),
	zoomOutButton: document.querySelector("#zoomOutButton"),
	zoomInButton: document.querySelector("#zoomInButton"),
	zoomLabel: document.querySelector("#zoomLabel"),
	fitButton: document.querySelector("#fitButton"),
	fitHeightButton: document.querySelector("#fitHeightButton"),
	continuousButton: document.querySelector("#continuousButton"),
	annotationButton: document.querySelector("#annotationButton"),
	imageDPI: document.querySelector("#imageDPI"),
	pageExportFormat: document.querySelector("#pageExportFormat"),
	exportPageButton: document.querySelector("#exportPageButton"),
	exportButton: document.querySelector("#exportButton"),
	emptyState: document.querySelector("#emptyState"),
	progressPanel: document.querySelector("#progressPanel"),
	progressLabel: document.querySelector("#progressLabel"),
	progressBar: document.querySelector("#progressBar"),
	pageFrame: document.querySelector("#pageFrame"),
	viewerPanel: document.querySelector(".viewer-panel"),
	svgHost: document.querySelector("#svgHost"),
	pageListPanel: document.querySelector(".page-list-panel"),
	pageList: document.querySelector("#pageList"),
	metaPanel: document.querySelector(".meta-panel"),
	appPanel: document.querySelector("#appPanel"),
	offlineStatus: document.querySelector("#offlineStatus"),
	refreshAppButton: document.querySelector("#refreshAppButton"),
	metaFile: document.querySelector("#metaFile"),
	metaTitle: document.querySelector("#metaTitle"),
	metaAuthor: document.querySelector("#metaAuthor"),
	metaVersion: document.querySelector("#metaVersion"),
	metaType: document.querySelector("#metaType"),
	metaFonts: document.querySelector("#metaFonts"),
	metaSignatures: document.querySelector("#metaSignatures"),
	signaturePanel: document.querySelector("#signaturePanel"),
	signatureSummary: document.querySelector("#signatureSummary"),
	signatureList: document.querySelector("#signatureList"),
	docFontList: document.querySelector("#docFontList"),
	docFontSummary: document.querySelector("#docFontSummary"),
	availableFontSummary: document.querySelector("#availableFontSummary"),
	fontPermissionHint: document.querySelector("#fontPermissionHint"),
	fontList: document.querySelector("#fontList"),
	statusText: document.querySelector("#statusText"),
};

el.ofdButton.addEventListener("click", openOFDFile);
el.togglePagesButton.addEventListener("click", () => toggleSidebar("pages"));
el.toggleMetaButton.addEventListener("click", () => toggleSidebar("meta"));
el.fontAddButton.addEventListener("click", () => openFontFile(el.fontInput));
el.fontDirectoryButton.addEventListener("click", () => openFontFile(el.fontDirectoryInput));
el.localFontButton.addEventListener("click", loadLocalFonts);
el.ofdInput.addEventListener("change", () => openOFD(el.ofdInput.files[0]));
el.fontInput.addEventListener("change", openSelectedFonts);
el.fontDirectoryInput.addEventListener("change", openSelectedFonts);
el.prevButton.addEventListener("click", () => renderPage(state.pageIndex - 1));
el.nextButton.addEventListener("click", () => renderPage(state.pageIndex + 1));
el.zoomOutButton.addEventListener("click", () => setScale(state.scale - 0.1));
el.zoomInButton.addEventListener("click", () => setScale(state.scale + 0.1));
el.fitButton.addEventListener("click", fitWidth);
el.fitHeightButton.addEventListener("click", fitHeight);
el.continuousButton.addEventListener("click", toggleContinuous);
el.annotationButton.addEventListener("click", toggleAnnotations);
el.pageExportFormat.addEventListener("change", () => updateDPIControl());
el.exportPageButton.addEventListener("click", exportCurrentPage);
el.exportButton.addEventListener("click", exportPDF);
el.refreshAppButton.addEventListener("click", refreshApplication);
el.pageInput.addEventListener("change", () => {
	const page = Number.parseInt(el.pageInput.value, 10);
	if (Number.isFinite(page)) {
		renderPage(page - 1);
	}
});
window.addEventListener("resize", resizeViewer);
COMPACT_LAYOUT.addEventListener("change", syncLayoutMode);
el.viewerPanel.addEventListener("scroll", () => {
	schedulePageSync();
});
el.viewerPanel.addEventListener("dblclick", openOFDFromViewer);
document.addEventListener("dragover", (event) => {
	if (event.dataTransfer.types.includes("Files")) {
		event.preventDefault();
		event.dataTransfer.dropEffect = document.body.hasAttribute("aria-busy") ? "none" : "copy";
	}
});
document.addEventListener("drop", openOFDFromDrop);
if (fontChannel) {
	fontChannel.onmessage = scheduleFontSync;
}
el.fontList.addEventListener("focusout", () => {
	if (state.fontSyncPending) {
		window.setTimeout(syncUserFonts, 0);
	}
});
document.addEventListener("visibilitychange", () => {
	if (!document.hidden) {
		scheduleFontSync();
	}
});

el.fontDirectoryButton.disabled = !("webkitdirectory" in el.fontDirectoryInput);
updateSidebarState();
boot();

async function openOFDFile() {
	if (document.body.hasAttribute("aria-busy")) {
		return;
	}
	await requestLocalFontsBeforeOpen();
	el.ofdInput.value = "";
	el.ofdInput.click();
}

async function openOFDFromViewer() {
	if (state.doc || document.body.hasAttribute("aria-busy")) {
		return;
	}
	await openOFDFile();
}

async function openOFDFromDrop(event) {
	if (!event.dataTransfer.types.includes("Files")) {
		return;
	}
	event.preventDefault();
	if (document.body.hasAttribute("aria-busy")) {
		return;
	}
	if (event.dataTransfer.files.length > 1) {
		setStatus("仅支持单文件拖入");
		return;
	}
	const file = event.dataTransfer.files[0];
	if (file && isOFDFile(file)) {
		await requestLocalFontsBeforeOpen();
	}
	await openOFD(file);
}

function openFontFile(input) {
	if (document.body.hasAttribute("aria-busy")) {
		return;
	}
	input.value = "";
	input.click();
}

function toggleSidebar(side) {
	if (side === "pages") {
		state.showPages = !state.showPages;
	} else if (side === "meta") {
		state.showMeta = !state.showMeta;
	}
	updateSidebarState();
	resizeViewer();
}

function updateSidebarState() {
	document.body.toggleAttribute("data-hide-pages", !state.showPages);
	document.body.toggleAttribute("data-hide-meta", !state.showMeta);
	el.pageListPanel.inert = !state.showPages;
	el.metaPanel.inert = !state.showMeta;
	el.togglePagesButton.setAttribute("aria-pressed", String(state.showPages));
	el.toggleMetaButton.setAttribute("aria-pressed", String(state.showMeta));
}

function syncLayoutMode(event) {
	state.showPages = !event.matches;
	state.showMeta = !event.matches;
	updateSidebarState();
	resizeViewer();
}

function resizeViewer() {
	if (state.doc) {
		applyFit(false);
		if (state.fitMode !== "free") {
			scrollToPage(state.pageIndex);
		}
	}
}

async function boot() {
	setBusy(true, "正在准备引擎", 8, STATUS.engine);
	try {
		if (!await prepareOffline()) {
			return;
		}
		setProgress("正在准备引擎", 8, STATUS.engine);
		await ensureWASM();
		let fontsRestored = true;
		try {
			await restoreUserFonts();
		} catch {
			fontsRestored = false;
		}
		setProgress("引擎准备完成", 70);
		loadExportFormats();
		setEmpty("选择 OFD 文件");
		updateFontSummary();
		renderFontList();
		updateControls();
		updateLocalFontButton();
		await refreshLocalFontPermission();
		setStatus(fontsRestored ? STATUS.ready : "字体读取失败");
		setBusy(false);
		if ("launchQueue" in window) {
			let opening = Promise.resolve();
			window.launchQueue.setConsumer(({ files }) => {
				if (!files.length) {
					return;
				}
				opening = opening.then(async () => {
					await openOFD(await files[0].getFile());
				}).catch((err) => showError(err, !state.doc));
			});
		}
	} catch (err) {
		setStatus("渲染引擎加载失败");
		setEmpty(String(err.message || err));
		setBusy(false);
		return;
	}
}

async function registerOffline() {
	const scope = new URL("./", location.href).href;
	let registration = await navigator.serviceWorker.getRegistration(scope);
	if (!registration || registration.scope !== scope) {
		registration = await navigator.serviceWorker.register("./ofdgo.sw.js", { updateViaCache: "none" });
	}
	const worker = registration.active || registration.installing || registration.waiting;
	await waitForWorker(worker, "activated");
	return registration;
}

async function prepareOffline() {
	if (!window.isSecureContext || !("serviceWorker" in navigator) || !("caches" in window) || !("locks" in navigator)) {
		return true;
	}
	el.appPanel.hidden = false;
	setProgress("正在准备离线", null, "正在准备离线");
	try {
		const registration = await registerOffline();
		const result = await requestOffline(registration.active, "prepare");
		if (result.reload) {
			location.reload();
			return false;
		}
		el.offlineStatus.textContent = "可离线";
	} catch {
		el.offlineStatus.textContent = "未就绪";
	}
	el.refreshAppButton.disabled = false;
	return true;
}

function waitForWorker(worker, target) {
	return new Promise((resolve, reject) => {
		function changed() {
			if (worker.state === target || worker.state === "activated") {
				worker.removeEventListener("statechange", changed);
				resolve();
			} else if (worker.state === "redundant") {
				worker.removeEventListener("statechange", changed);
				reject(new Error("离线服务启动失败"));
			}
		}
		worker.addEventListener("statechange", changed);
		changed();
	});
}

function requestOffline(worker, type) {
	return new Promise((resolve, reject) => {
		const channel = new MessageChannel();
		function close() {
			channel.port1.close();
			worker.removeEventListener("statechange", changed);
		}
		function changed() {
			if (worker.state === "redundant") {
				close();
				reject(new Error("离线服务已更新"));
			}
		}
		channel.port1.onmessage = ({ data }) => {
			close();
			if (data.ok) {
				resolve(data);
			} else {
				reject(new Error(data.error));
			}
		};
		worker.addEventListener("statechange", changed);
		worker.postMessage({ type }, [channel.port2]);
		changed();
	});
}

async function refreshApplication() {
	if (document.body.hasAttribute("aria-busy")) {
		return;
	}
	const temporaryFonts = state.userFonts.some((font) => font.source === "upload");
	const message = temporaryFonts ? "刷新后文件需重新打开，未保存字体需重新添加" : "刷新后文件需重新打开";
	if ((state.ofdBytes || temporaryFonts) && !window.confirm(message)) {
		return;
	}
	setBusy(true, "正在刷新应用", null, "正在刷新应用");
	el.refreshAppButton.disabled = true;
	const panels = document.querySelectorAll(".toolbar, .workspace");
	panels.forEach((panel) => { panel.inert = true; });
	try {
		if (navigator.storage?.persist) {
			await navigator.storage.persist().catch(() => false);
		}
		const scope = new URL("./", location.href).href;
		await navigator.locks.request(`ofdgo:${scope}:refresh`, { ifAvailable: true }, async (lock) => {
			if (!lock) {
				throw new Error("应用正在刷新");
			}
			const registration = await registerOffline();
			await registration.update();
			if (registration.installing) {
				await waitForWorker(registration.installing, "installed");
			}
			const worker = registration.waiting || registration.active;
			await requestOffline(worker, "refresh");
			await waitForWorker(worker, "activated");
			location.reload();
		});
	} catch (err) {
		setStatus(err.message === "应用正在刷新" ? err.message : "应用刷新失败");
	} finally {
		panels.forEach((panel) => { panel.inert = false; });
		el.refreshAppButton.disabled = false;
		setBusy(false);
	}
}

function loadExportFormats() {
	const formats = callWASM("ofdgoExportFormats") || [];
	state.exportFormats = formats;
	el.pageExportFormat.replaceChildren();
	for (const format of formats) {
		const option = document.createElement("option");
		option.value = format.value;
		option.textContent = format.label;
		el.pageExportFormat.append(option);
	}
	if (formats.length) {
		el.pageExportFormat.value = formats[0].value;
	}
	updateDPIControl();
}

async function ensureWASM() {
	if (state.ready && !state.wasmExited) {
		return;
	}
	if (!wasmPromise) {
		wasmPromise = loadWASM().finally(() => {
			wasmPromise = null;
		});
	}
	await wasmPromise;
}

async function loadWASM() {
	if (!globalThis.Go) {
		throw new Error("渲染引擎脚本缺失");
	}
	const wasmSeq = state.wasmSeq + 1;
	state.wasmSeq = wasmSeq;
	state.ready = false;
	state.wasmExited = false;
	clearWASMCallbacks();
	const go = new Go();
	if (!wasmModule) {
		setProgress("正在下载引擎", 16);
		const response = await fetch("./ofdgo.wasm");
		try {
			setProgress("正在编译引擎", 35);
			wasmModule = await WebAssembly.compileStreaming(response);
		} catch {
			const fallback = await fetch("./ofdgo.wasm");
			const bytes = await fallback.arrayBuffer();
			setProgress("正在编译引擎", 45);
			wasmModule = await WebAssembly.compile(bytes);
		}
	}
	setProgress("正在启动引擎", 58);
	const instance = await WebAssembly.instantiate(wasmModule, go.importObject);
	go.run(instance).then(() => {
		markWASMExited(wasmSeq);
	}).catch((err) => {
		markWASMExited(wasmSeq, err);
	});
	await waitFor(() => WASM_CALLBACKS.every((name) => typeof globalThis[name] === "function"));
	if (wasmSeq !== state.wasmSeq) {
		return;
	}
	state.ready = true;
	state.wasmExited = false;
}

function clearWASMCallbacks() {
	for (const name of WASM_CALLBACKS) {
		globalThis[name] = undefined;
	}
}

function markWASMExited(wasmSeq = state.wasmSeq, err) {
	if (wasmSeq !== state.wasmSeq) {
		return;
	}
	state.ready = false;
	state.wasmExited = true;
	if (err) {
		setStatus("渲染引擎异常，正在恢复");
	}
	scheduleWASMRecovery();
}

function scheduleWASMRecovery() {
	if (!state.ofdBytes || state.wasmRecovering || state.wasmRecoveries >= 2) {
		return;
	}
	window.clearTimeout(wasmRecoveryTimer);
	wasmRecoveryTimer = window.setTimeout(recoverWASM, 0);
}

async function recoverWASM() {
	if (state.wasmRecovering || !state.ofdBytes) {
		return;
	}
	state.wasmRecovering = true;
	state.wasmRecoveries += 1;
	const pageIndex = state.pageIndex;
	const fitMode = state.fitMode || "width";
	setBusy(true, "正在恢复引擎", 18, STATUS.recovering);
	try {
		await ensureWASM();
		await openDocument({
			pageIndex,
			fitMode,
			skipAutoFonts: true,
		});
	} catch (err) {
		showError(err, !state.doc);
	} finally {
		state.wasmRecovering = false;
		if (!state.doc) {
			setBusy(false);
		}
	}
}

function waitFor(predicate) {
	return new Promise((resolve, reject) => {
		const started = performance.now();
		const timer = window.setInterval(() => {
			if (predicate()) {
				window.clearInterval(timer);
				resolve();
				return;
			}
			if (performance.now() - started > 5000) {
				window.clearInterval(timer);
				reject(new Error("渲染引擎初始化超时"));
			}
		}, 20);
	});
}

async function openOFD(file) {
	if (!file) {
		return;
	}
	if (!isOFDFile(file)) {
		el.ofdInput.value = "";
		showError(new Error("选择 OFD 文件"), !state.doc);
		return;
	}
	state.wasmRecoveries = 0;
	setBusy(true, "正在读取 OFD", 10, STATUS.opening);
	try {
		state.ofdBytes = new Uint8Array(await file.arrayBuffer());
		state.fileName = file.name || "ofdgo.ofd";
		await openDocument({ pageIndex: 0, resetScroll: true });
	} catch (err) {
		showError(err, true);
		setBusy(false);
	}
}

function isOFDFile(file) {
	return /\.ofd$/i.test(file.name || "");
}

function openFontDatabase() {
	if (!fontDatabase) {
		fontDatabase = new Promise((resolve, reject) => {
			const request = indexedDB.open(FONT_DATABASE, 1);
			request.onupgradeneeded = () => {
				const store = request.result.createObjectStore("fonts", { keyPath: "id", autoIncrement: true });
				store.createIndex("checksum", "checksum", { unique: true });
			};
			request.onsuccess = () => {
				const db = request.result;
				db.onversionchange = () => {
					db.close();
					fontDatabase = null;
				};
				resolve(db);
			};
			request.onerror = () => reject(request.error);
		}).catch((err) => {
			fontDatabase = null;
			throw err;
		});
	}
	return fontDatabase;
}

async function fontTransaction(mode, action) {
	const db = await openFontDatabase();
	return new Promise((resolve, reject) => {
		const tx = db.transaction("fonts", mode);
		let request;
		tx.oncomplete = () => {
			if (mode === "readwrite") {
				fontChannel?.postMessage(null);
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

function listStoredFonts() {
	return fontTransaction("readonly", (store) => store.getAll());
}

async function addStoredFonts(fonts) {
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
	await fontTransaction("readwrite", (store) => {
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

function updateStoredFont(id, changes) {
	return fontTransaction("readwrite", (store) => {
		const request = store.get(id);
		request.onsuccess = () => {
			if (request.result) {
				store.put({ ...request.result, ...changes });
			}
		};
	});
}

function deleteStoredFont(id) {
	return fontTransaction("readwrite", (store) => store.delete(id));
}

async function openSelectedFonts(event) {
	if (document.body.hasAttribute("aria-busy")) {
		return;
	}
	const input = event.currentTarget;
	const files = Array.from(input.files || []).filter((file) => (
		!input.webkitdirectory || /\.(ttf|otf|ttc)$/i.test(file.name)
	));
	input.value = "";
	if (!files.length) {
		setStatus("暂无字体文件");
		return;
	}
	setBusy(true, "正在读取字体", 10, STATUS.fonts);
	try {
		const fonts = [];
		for (let i = 0; i < files.length; i += 1) {
			setProgress(`正在读取字体 ${i + 1}/${files.length}`, 10 + Math.round(i / files.length * 60));
			const file = files[i];
			fonts.push(createFontRecord(file.name, new Uint8Array(await file.arrayBuffer()), "upload"));
		}
		let saved = true;
		try {
			await addStoredFonts(fonts);
		} catch {
			saved = false;
			state.userFonts.push(...fonts);
		}
		const changed = saved ? await restoreUserFonts() : true;
		if (saved && navigator.storage?.persist) {
			navigator.storage.persist().catch(() => false);
		}
		if (changed) {
			await applyFontChange();
		}
		setStatus(saved ? "字体保存完成" : "字体仅限本次");
	} catch (err) {
		showError(err, false);
	} finally {
		setBusy(false);
	}
}

async function loadLocalFonts() {
	if (document.body.hasAttribute("aria-busy")) {
		return;
	}
	if (!canReadLocalFonts()) {
		setStatus("不支持读取系统字体");
		return;
	}
	setBusy(true, state.doc ? "正在匹配字体" : "正在请求授权", 12, state.doc ? STATUS.fonts : "正在请求授权");
	try {
		await nextFrame();
		const available = await queryLocalFonts();
		if (!state.doc) {
			setStatus(available.length ? `系统字体已授权 ${available.length} 个` : "未读取到系统字体");
			return;
		}
		if (await loadDocumentLocalFonts(available)) {
			await applyFontChange({ skipAutoFonts: true });
		}
	} catch (err) {
		if (err && err.name === "NotAllowedError") {
			setStatus("系统字体未授权");
		} else {
			setStatus(String(err.message || err));
		}
	} finally {
		setBusy(false);
	}
}

async function requestLocalFontsBeforeOpen() {
	if (!canReadLocalFonts() || state.systemFontCatalogLoaded || state.systemFontPermission === "denied") {
		return;
	}
	setBusy(true, "正在请求授权", 12, "正在请求授权");
	try {
		const available = await queryLocalFonts();
		setStatus(available.length ? `系统字体已授权 ${available.length} 个` : "未读取到系统字体");
	} catch (err) {
		if (err && err.name === "NotAllowedError") {
			setStatus("系统字体未授权");
			return;
		}
		setStatus(String(err.message || err));
	} finally {
		setBusy(false);
	}
}

async function queryLocalFonts() {
	try {
		const available = await window.queryLocalFonts();
		state.systemFontCatalog = available;
		state.systemFontCatalogLoaded = true;
		state.systemFontPermission = "granted";
		updateFontPermissionHint();
		return available;
	} catch (err) {
		if (err && err.name === "NotAllowedError") {
			state.systemFontPermission = "denied";
			updateFontPermissionHint();
		}
		throw err;
	}
}

async function autoLoadDocumentLocalFonts(openSeq) {
	if (!externalDocumentFontNames().length || !canReadLocalFonts() || state.systemFontPermission === "denied") {
		return false;
	}
	setProgress("正在匹配字体", 62, STATUS.fonts);
	try {
		const available = state.systemFontCatalogLoaded ? state.systemFontCatalog : await queryLocalFonts();
		if (openSeq !== state.openSeq) {
			return false;
		}
		return await loadDocumentLocalFonts(available, openSeq);
	} catch (err) {
		if (err && err.name === "NotAllowedError") {
			setStatus("系统字体未授权");
			return false;
		}
		setStatus(String(err.message || err));
		return false;
	}
}

async function loadDocumentLocalFonts(available, openSeq = state.openSeq) {
	const docFonts = state.doc?.fonts || [];
	const docNames = externalDocumentFontNames();
	if (!docFonts.length) {
		state.localFonts = [];
		setStatus("暂无字体");
		updateFontSummary();
		renderFontList();
		return false;
	}
	if (!docNames.length) {
		state.localFonts = [];
		setStatus("文档字体均为内嵌");
		updateFontSummary();
		renderFontList();
		return false;
	}
	const docLoadLimit = Math.min(LOCAL_FONT_LOAD_LIMIT, Math.max(4, docNames.length * 3));
	let selected = selectLocalFonts(available, docNames, docLoadLimit);
	if (!selected.length) {
		selected = selectLocalFonts(available, [], 6);
	}
	const emptyStatus = available.length === 0 ? "未读取到系统字体" : "未匹配到所需字体";
	const fonts = [];
	for (let i = 0; i < selected.length; i += 1) {
		setProgress(`正在读取字体 ${i + 1}/${selected.length}`, 20 + Math.round(i / selected.length * 60));
		const item = selected[i];
		const blob = await item.blob();
		if (openSeq !== state.openSeq) {
			return false;
		}
		fonts.push(createFontRecord(localFontName(item), new Uint8Array(await blob.arrayBuffer()), "browser"));
		if (openSeq !== state.openSeq) {
			return false;
		}
	}
	if (openSeq !== state.openSeq) {
		return false;
	}
	state.localFonts = fonts;
	setStatus(fonts.length ? `系统字体已加载 ${fonts.length} 个` : emptyStatus);
	updateFontSummary();
	renderFontList();
	return fonts.length > 0;
}

function uniqueLocalFonts(fonts) {
	const seen = new Set();
	const selected = [];
	for (const font of fonts) {
		const key = normalizeFontName(localFontName(font));
		if (!key || seen.has(key)) {
			continue;
		}
		seen.add(key);
		selected.push(font);
	}
	return selected;
}

function selectLocalFonts(fonts, names, limit) {
	const available = new Map(uniqueLocalFonts(fonts).map((font) => [localFontName(font), font]));
	const matched = callWASM("ofdgoFontFileMatches", [...available.keys()], names);
	return matched.slice(0, limit).map((name) => available.get(name));
}

async function fontData(fonts) {
	const result = [];
	for (const font of fonts) {
		if (!font.enabled) {
			continue;
		}
		if (font.data instanceof Blob) {
			font.data = new Uint8Array(await font.data.arrayBuffer());
		}
		result.push({ name: font.name, data: font.data });
	}
	return result;
}

async function restoreUserFonts() {
	const stored = await listStoredFonts();
	const previous = new Map(state.userFonts.map((font) => [font.id, font]));
	const fonts = stored.map((font) => ({
		...font,
		data: previous.get(font.id)?.checksum === font.checksum ? previous.get(font.id).data : font.data,
		source: "stored",
	}));
	fonts.push(...state.userFonts.filter((font) => font.source === "upload"));
	if (fonts.length === state.userFonts.length && fonts.every((font, index) => {
		const old = state.userFonts[index];
		return font.id === old.id && font.name === old.name && font.enabled === old.enabled && font.checksum === old.checksum;
	})) {
		return false;
	}
	state.userFonts = fonts;
	return true;
}

function scheduleFontSync() {
	state.fontSyncPending = true;
	syncUserFonts();
}

async function syncUserFonts() {
	if (!state.ready || !state.fontSyncPending || state.fontSyncing || document.hidden || document.body.hasAttribute("aria-busy") || document.activeElement?.classList.contains("font-name-input")) {
		return;
	}
	state.fontSyncPending = false;
	state.fontSyncing = true;
	setBusy(true, "正在同步字体", null);
	try {
		if (await restoreUserFonts()) {
			await applyFontChange();
		}
		if (!state.doc) {
			setStatus(STATUS.ready);
		}
	} catch {
		setStatus("字体读取失败");
	} finally {
		state.fontSyncing = false;
		setBusy(false);
	}
}

function updateFontSummary() {
	const fonts = fontRecords();
	const total = fonts.length;
	const enabled = fonts.filter((font) => font.enabled).length;
	el.availableFontSummary.textContent = total ? `${enabled}/${total}` : "0";
}

function createFontRecord(name, data, source) {
	state.fontSeq += 1;
	return {
		id: `${source}-${state.fontSeq}`,
		name: name || "font.ttf",
		data,
		enabled: true,
		source,
	};
}

function fontRecords() {
	return [...state.localFonts, ...state.userFonts];
}

function externalDocumentFontNames() {
	const names = [];
	for (const font of state.doc?.fonts || []) {
		if (font.embedded || font.status === "embedded") {
			continue;
		}
		names.push(font.fontName, font.familyName);
	}
	return names;
}

function localFontName(font) {
	const name = font.fullName || font.family || font.postscriptName || "local-font";
	return `${name}.ttf`;
}

function normalizeFontName(name = "") {
	return String(name)
		.toLowerCase()
		.replace(/\.[^.]+$/, "")
		.replace(/[\s_\-()（）]/g, "");
}

async function applyFontChange(options = {}) {
	updateFontSummary();
	renderFontList();
	if (!state.ofdBytes || !state.doc) {
		return;
	}
	await openDocument({
		pageIndex: state.pageIndex,
		fitMode: state.fitMode,
		scale: state.scale,
		skipAutoFonts: options.skipAutoFonts !== false,
		reuseSession: true,
		fontsChanged: true,
	});
}

async function toggleAnnotations() {
	state.renderAnnotations = !state.renderAnnotations;
	updateAnnotationButton();
	if (!state.ofdBytes || !state.doc) {
		return;
	}
	await openDocument({
		pageIndex: state.pageIndex,
		fitMode: state.fitMode,
		skipAutoFonts: true,
		reuseSession: true,
	});
}

function removeFont(id) {
	state.localFonts = state.localFonts.filter((font) => font.id !== id);
	state.userFonts = state.userFonts.filter((font) => font.id !== id);
}

async function changeFont(font, changes) {
	if (document.body.hasAttribute("aria-busy")) {
		return;
	}
	setBusy(true, "正在更新字体", null, STATUS.fonts);
	try {
		if (font.source === "stored") {
			if (changes) {
				await updateStoredFont(font.id, changes);
			} else {
				await deleteStoredFont(font.id);
			}
			await restoreUserFonts();
		} else if (changes) {
			Object.assign(font, changes);
		} else {
			removeFont(font.id);
		}
		await applyFontChange();
		if (!state.doc) {
			setStatus(STATUS.ready);
		}
	} catch {
		renderFontList();
		setStatus("字体更新失败");
	} finally {
		setBusy(false);
	}
}

function renderFontList() {
	el.fontList.replaceChildren();
	const fonts = fontRecords();
	if (!fonts.length) {
		const empty = document.createElement("div");
		empty.className = "font-empty";
		empty.textContent = "暂无字体";
		el.fontList.append(empty);
		return;
	}
	const fragment = document.createDocumentFragment();
	for (const font of fonts) {
		const row = document.createElement("div");
		row.className = "font-row";
		row.dataset.fontId = font.id;

		const source = document.createElement("span");
		source.className = "font-source";
		source.textContent = fontSourceText(font.source);

		const enabled = document.createElement("input");
		enabled.type = "checkbox";
		enabled.checked = font.enabled;
		enabled.title = "启用字体";

		const enabledText = document.createElement("span");
		enabledText.textContent = font.enabled ? "启用" : "停用";
		enabled.addEventListener("change", async () => {
			await changeFont(font, { enabled: enabled.checked });
		});

		const name = document.createElement("input");
		name.className = "font-name-input";
		name.value = font.name;
		name.title = "字体文件名或匹配名";
		name.addEventListener("change", async () => {
			const next = name.value.trim();
			if (!next) {
				name.value = font.name;
				return;
			}
			await changeFont(font, { name: next });
		});

		const main = document.createElement("div");
		main.className = "font-main";
		main.append(source, name);

		const toggle = document.createElement("label");
		toggle.className = "font-toggle";
		toggle.append(enabled, enabledText);

		const remove = document.createElement("button");
		remove.className = "font-delete";
		remove.type = "button";
		remove.title = "删除字体";
		remove.textContent = "×";
		remove.addEventListener("click", async () => {
			await changeFont(font);
		});

		const actions = document.createElement("div");
		actions.className = "font-actions";
		actions.append(toggle, remove);

		row.append(main, actions);
		fragment.append(row);
	}
	el.fontList.append(fragment);
}

function fontSourceText(source) {
	switch (source) {
	case "browser":
		return "系统";
	default:
		return "上传";
	}
}

function updateLocalFontButton() {
	const supported = canReadLocalFonts();
	el.localFontButton.disabled = !supported;
	el.localFontButton.title = supported ? "读取系统字体" : "不支持读取系统字体";
	updateFontPermissionHint();
}

function updateFontPermissionHint() {
	el.fontPermissionHint.hidden = !canReadLocalFonts() || state.systemFontPermission === "granted";
}

function canReadLocalFonts() {
	return typeof window.queryLocalFonts === "function";
}

async function refreshLocalFontPermission() {
	if (!canReadLocalFonts() || !navigator.permissions?.query) {
		updateFontPermissionHint();
		return;
	}
	try {
		const permission = await navigator.permissions.query({ name: "local-fonts" });
		state.systemFontPermission = permission.state;
		permission.onchange = () => {
			state.systemFontPermission = permission.state;
			updateFontPermissionHint();
		};
	} catch {
		state.systemFontPermission = "prompt";
	}
	updateFontPermissionHint();
}

async function openDocument(options = {}) {
	if (!state.ofdBytes) {
		return;
	}
	if (!state.ready || state.wasmExited) {
		await ensureWASM();
	}
	const openSeq = options.openSeq || (state.openSeq += 1);
	const resetLocalFonts = !options.skipAutoFonts;
	if (resetLocalFonts) {
		state.localFonts = [];
		updateFontSummary();
		renderFontList();
	}
	setBusy(true, "正在打开 OFD", 20, STATUS.opening);
	try {
		setProgress("正在解析 OFD", 52);
		await nextFrame();
		if (openSeq !== state.openSeq) {
			return;
		}
		const fonts = !options.reuseSession || options.fontsChanged
			? await fontData(resetLocalFonts ? state.userFonts : fontRecords()) : null;
		if (openSeq !== state.openSeq) {
			return;
		}
		const doc = options.reuseSession
			? callWASM("ofdgoConfigure", fonts, state.renderAnnotations)
			: callWASM("ofdgoOpen", state.ofdBytes, fonts, state.renderAnnotations);
		if (openSeq !== state.openSeq) {
			return;
		}
		const pageCount = doc.pageCount || 0;
		const pageIndex = Math.min(Math.max(options.pageIndex || 0, 0), Math.max(pageCount - 1, 0));
		state.doc = doc;
		state.pageIndex = pageIndex;
		state.scale = options.scale || 1;
		if (!options.fitMode) {
			setContinuous(window.matchMedia("(max-width: 640px)").matches);
		}
		state.fitMode = options.fitMode || (!state.continuous && pageCount === 1 ? "height" : "width");
		if (!options.skipAutoFonts && await autoLoadDocumentLocalFonts(openSeq)) {
			if (openSeq !== state.openSeq) {
				return;
			}
			await openDocument({
				pageIndex,
				fitMode: state.fitMode,
				resetScroll: options.resetScroll,
				skipAutoFonts: true,
				openSeq,
				reuseSession: true,
				fontsChanged: true,
			});
			return;
		}
		if (openSeq !== state.openSeq) {
			return;
		}
		resetPageFlow();
		renderPageList();
		renderMeta();
		renderPageFlow();
		if (options.resetScroll) {
			el.viewerPanel.scrollLeft = 0;
			el.viewerPanel.scrollTop = 0;
			el.pageListPanel.scrollLeft = 0;
			el.pageListPanel.scrollTop = 0;
			el.metaPanel.scrollLeft = 0;
			el.metaPanel.scrollTop = 0;
		}
		applyFit(false);
		await nextFrame();
		await renderPage(pageIndex, { keepBusy: true, scroll: false, openSeq });
		queueNearbyPages(pageIndex, openSeq);
		loadDocumentDetails(openSeq);
	} catch (err) {
		if (openSeq !== state.openSeq) {
			return;
		}
		showError(err, true);
	} finally {
		if (openSeq === state.openSeq) {
			setBusy(false);
		}
	}
}

async function loadDocumentDetails(openSeq) {
	await waitForPaint();
	if (openSeq !== state.openSeq) {
		return;
	}
	try {
		const info = callWASM("ofdgoDocumentInfo");
		if (openSeq === state.openSeq) {
			Object.assign(state.doc, info, { detailsPending: false });
			renderMeta();
		}
	} catch (err) {
		if (openSeq === state.openSeq) {
			showError(err, false);
		}
	}
}

async function renderPage(index, options = {}) {
	if (!state.doc) {
		return;
	}
	const openSeq = options.openSeq || state.openSeq;
	if (openSeq !== state.openSeq) {
		return;
	}
	const pageCount = state.doc.pageCount || 0;
	if (index < 0 || index >= pageCount) {
		return;
	}
	if (options.keepBusy) {
		setProgress("正在渲染页面", 76);
	} else {
		setBusy(true, "正在渲染页面", 35, "正在渲染页面");
	}
	try {
		setCurrentPage(index);
		if (options.fit !== false) {
			applyFit(false);
		}
		if (options.scroll !== false) {
			scrollToPage(index);
		}
		await renderFlowPage(index, { throwError: true, openSeq, priority: 0 });
		if (openSeq !== state.openSeq) {
			return;
		}
		if (options.scroll !== false) {
			scrollToPage(index);
		}
		queueNearbyPages(index, openSeq);
	} catch (err) {
		if (openSeq === state.openSeq) {
			showError(err, false);
		}
	} finally {
		if (!options.keepBusy && openSeq === state.openSeq) {
			setBusy(false);
		}
	}
}

async function exportPDF() {
	if (!state.doc) {
		return;
	}
	const openSeq = state.openSeq;
	setExportControlsDisabled(true);
	setBusy(true, "正在准备 PDF", 18, STATUS.exporting);
	try {
		await waitForPaint();
		if (openSeq !== state.openSeq) {
			return;
		}
		setProgress("正在生成 PDF", 45);
		await waitForPaint();
		if (openSeq !== state.openSeq) {
			return;
		}
		const result = callWASM("ofdgoExportPDF");
		if (openSeq !== state.openSeq) {
			return;
		}
		setProgress("正在保存 PDF", 86);
		const bytes = result.bytes;
		downloadBytes(bytes, "application/pdf", pdfFileName());
		setStatus(`PDF 已导出 ${formatBytes(result.size || bytes.length)}`);
	} catch (err) {
		if (openSeq === state.openSeq) {
			showError(err, false);
		}
	} finally {
		if (openSeq === state.openSeq) {
			setBusy(false);
			updateControls();
		}
	}
}

async function exportCurrentPage() {
	if (!state.doc) {
		return;
	}
	const format = el.pageExportFormat.value;
	if (!format) {
		return;
	}
	const info = exportFormatInfo(format);
	const label = info?.label || String(format || "").toUpperCase();
	const openSeq = state.openSeq;
	setExportControlsDisabled(true);
	setBusy(true, `正在准备 ${label}`, 18, STATUS.pageExporting);
	try {
		await waitForPaint();
		if (openSeq !== state.openSeq) {
			return;
		}
		setProgress(`正在生成 ${label}`, 45);
		await waitForPaint();
		if (openSeq !== state.openSeq) {
			return;
		}
		const dpi = exportFormatUsesDPI(format) ? currentImageDPI() : 0;
		const result = callWASM("ofdgoExportPage", state.pageIndex, format, dpi);
		if (openSeq !== state.openSeq) {
			return;
		}
		setProgress(`正在保存 ${result.label || label}`, 86);
		const bytes = result.bytes;
		downloadBytes(bytes, result.mime || info?.mime || "application/octet-stream", pageFileName(result.extension || info?.extension || format));
		setStatus(`${result.label || label} 已导出 ${formatBytes(result.size || bytes.length, result.label || label)}`);
	} catch (err) {
		if (openSeq === state.openSeq) {
			showError(err, false);
		}
	} finally {
		if (openSeq === state.openSeq) {
			setBusy(false);
			updateControls();
		}
	}
}

function renderPageFlow() {
	el.svgHost.replaceChildren();
	el.viewerPanel.classList.remove("single-page-fits-height");
	if (!state.doc) {
		return;
	}
	if (state.pageObserver) {
		state.pageObserver.disconnect();
	}
	const fragment = document.createDocumentFragment();
	const targets = [];
	for (const page of state.doc.pages || []) {
		const shell = document.createElement("div");
		shell.className = "page-shell";
		shell.dataset.pageIndex = String(page.index);

		const surface = document.createElement("div");
		surface.className = "page-surface";

		const placeholder = document.createElement("div");
		placeholder.className = "page-placeholder";
		placeholder.textContent = `第 ${page.index + 1} 页`;

		shell.append(surface, placeholder);
		fragment.append(shell);
		targets.push([shell, page.index]);
	}
	el.svgHost.append(fragment);
	layoutPages();
	for (const [shell, index] of targets) {
		observeFlowPage(shell, index);
	}
	el.emptyState.hidden = true;
	el.pageFrame.hidden = false;
}

function resetPageFlow() {
	state.pageCache.clear();
	state.visiblePages.clear();
	for (const task of state.pageInFlight.values()) {
		task.resolve(null);
	}
	state.pageRenderQueue = [];
	state.pageInFlight.clear();
	resetThumbnails();
	if (state.pageObserver) {
		state.pageObserver.disconnect();
		state.pageObserver = null;
	}
}

function observeFlowPage(shell, index) {
	const openSeq = state.openSeq;
	if (index < 4 && index !== state.pageIndex) {
		renderFlowPage(index, { openSeq, priority: 3 });
	}
	const observer = flowPageObserver();
	if (observer) {
		observer.observe(shell);
	}
}

function flowPageObserver() {
	if (!("IntersectionObserver" in window)) {
		return null;
	}
	if (!state.pageObserver) {
		const openSeq = state.openSeq;
		state.pageObserver = new IntersectionObserver((entries) => {
			if (openSeq !== state.openSeq) {
				return;
			}
			for (const entry of entries) {
				const index = Number.parseInt(entry.target.dataset.pageIndex, 10);
				if (!entry.isIntersecting) {
					state.visiblePages.delete(index);
					if (entry.target.classList.contains("rendered")) {
						entry.target.querySelector(".page-surface").replaceChildren();
						entry.target.classList.remove("rendered");
					}
					continue;
				}
				state.visiblePages.add(index);
				renderFlowPage(index, { openSeq, priority: 4 });
			}
		}, {
			root: el.viewerPanel,
			rootMargin: "600px 0px",
		});
	}
	return state.pageObserver;
}

async function renderFlowPage(index, options = {}) {
	const openSeq = options.openSeq || state.openSeq;
	if (openSeq !== state.openSeq) {
		return null;
	}
	try {
		const page = await loadPageData(index, { openSeq, priority: options.priority ?? 2 });
		if (openSeq !== state.openSeq) {
			return null;
		}
		if (page) {
			const shell = pageShell(index);
			if (!shell?.classList.contains("rendered") && (!state.pageObserver || state.visiblePages.has(index) || index === state.pageIndex)) {
				mountPageSVG(index, page, openSeq);
			}
			updateThumbnail(index, openSeq);
		}
		return page;
	} catch (err) {
		if (openSeq === state.openSeq) {
			markFlowPageError(index, err);
		}
		if (options.throwError && openSeq === state.openSeq) {
			throw err;
		}
		return null;
	}
}

function loadPageData(index, options = {}) {
	const openSeq = options.openSeq || state.openSeq;
	if (openSeq !== state.openSeq) {
		return Promise.resolve(null);
	}
	if (state.pageCache.has(index)) {
		return Promise.resolve(state.pageCache.get(index));
	}
	const key = `${openSeq}:${index}`;
	const priority = options.priority ?? 3;
	const current = state.pageInFlight.get(key);
	if (current) {
		current.priority = Math.min(current.priority, priority);
		return current.promise;
	}
	let resolve;
	let reject;
	const promise = new Promise((done, fail) => {
		resolve = done;
		reject = fail;
	});
	const task = { key, index, openSeq, priority, resolve, reject, promise };
	state.pageInFlight.set(key, task);
	state.pageRenderQueue.push(task);
	schedulePageRender();
	return promise;
}

function schedulePageRender() {
	if (state.pageRenderRunning) {
		return;
	}
	state.pageRenderRunning = true;
	requestAnimationFrame(processPageRenderQueue);
}

async function processPageRenderQueue() {
	try {
		while (state.pageRenderQueue.length) {
			state.pageRenderQueue.sort(comparePageRenderTask);
			const task = state.pageRenderQueue.shift();
			let delayed = false;
			try {
				if (task.openSeq !== state.openSeq) {
					task.resolve(null);
					continue;
				}
				if (state.pageCache.has(task.index)) {
					task.resolve(state.pageCache.get(task.index));
					continue;
				}
				await nextFrame();
				if (task.openSeq !== state.openSeq) {
					task.resolve(null);
					continue;
				}
				if (shouldDelayPageTask(task)) {
					state.pageRenderQueue.push(task);
					delayed = true;
					continue;
				}
				const page = callWASM("ofdgoRenderPage", task.index);
				if (task.openSeq === state.openSeq) {
					state.pageCache.set(task.index, page);
					cacheThumbnail(task.index, page.svg);
				}
				task.resolve(page);
			} catch (err) {
				task.reject(err);
			} finally {
				if (!delayed) {
					state.pageInFlight.delete(task.key);
				}
			}
		}
	} finally {
		state.pageRenderRunning = false;
		if (state.pageRenderQueue.length) {
			schedulePageRender();
		}
	}
}

function shouldDelayPageTask(task) {
	return state.pageRenderQueue.some((next) => next.openSeq === state.openSeq && comparePageRenderTask(next, task) < 0);
}

function comparePageRenderTask(a, b) {
	return a.priority - b.priority || Math.abs(a.index - state.pageIndex) - Math.abs(b.index - state.pageIndex);
}

function mountPageSVG(index, page, openSeq = state.openSeq) {
	if (openSeq !== state.openSeq) {
		return;
	}
	const shell = pageShell(index);
	if (!shell) {
		return;
	}
	const surface = shell.querySelector(".page-surface");
	const svg = parseSVG(page.svg, `p${openSeq}-${index}`);
	svg.classList.add("ofd-svg");
	surface.replaceChildren(svg);
	shell.classList.add("rendered");
	if (state.doc?.pages?.[index]) {
		layoutPageShell(shell, state.doc.pages[index]);
	}
}

function markFlowPageError(index, err) {
	const shell = pageShell(index);
	if (!shell) {
		return;
	}
	shell.classList.add("error");
	const placeholder = shell.querySelector(".page-placeholder");
	if (placeholder) {
		placeholder.textContent = String(err.message || err);
	}
}

function queueNearbyPages(index, openSeq = state.openSeq) {
	for (let i = Math.max(0, index - 1); i <= Math.min((state.doc?.pageCount || 1) - 1, index + 2); i += 1) {
		renderFlowPage(i, { openSeq, priority: 2 });
	}
}

function pageShell(index) {
	return el.svgHost.querySelector(`.page-shell[data-page-index="${index}"]`);
}

function scrollToPage(index) {
	const shell = pageShell(index);
	if (shell) {
		if (state.fitMode === "height" && !state.continuous) {
			shell.scrollIntoView({ block: "center", inline: "nearest" });
		} else {
			const viewerRect = el.viewerPanel.getBoundingClientRect();
			const shellRect = shell.getBoundingClientRect();
			const top = el.viewerPanel.scrollTop + shellRect.top - viewerRect.top - pageBlockSpace();
			const maxTop = Math.max(0, el.viewerPanel.scrollHeight - el.viewerPanel.clientHeight);
			el.viewerPanel.scrollTop = Math.min(maxTop, Math.max(0, top));
		}
		centerPageInline(shell);
	}
}

function centerPageInline(shell) {
	const viewerRect = el.viewerPanel.getBoundingClientRect();
	const shellRect = shell.getBoundingClientRect();
	el.viewerPanel.scrollLeft += shellRect.left + shellRect.width / 2 - viewerRect.left - el.viewerPanel.clientWidth / 2;
}

function schedulePageSync() {
	if (state.scrollFrame || !state.doc) {
		return;
	}
	state.scrollFrame = requestAnimationFrame(() => {
		state.scrollFrame = 0;
		syncCurrentPageFromScroll();
	});
}

function syncCurrentPageFromScroll() {
	const shell = pageShellFromView();
	if (!shell) {
		return;
	}
	const nextIndex = Number.parseInt(shell.dataset.pageIndex, 10);
	if (Number.isFinite(nextIndex) && nextIndex !== state.pageIndex) {
		setCurrentPage(nextIndex);
		queueNearbyPages(nextIndex);
	}
}

function pageShellFromView() {
	const rect = el.viewerPanel.getBoundingClientRect();
	const x = rect.left + rect.width / 2;
	if (state.continuous) {
		if (el.viewerPanel.scrollTop + el.viewerPanel.clientHeight >= el.viewerPanel.scrollHeight - 1) {
			const shell = pageShell(state.pageIndex);
			const bounds = shell?.getBoundingClientRect();
			return bounds && bounds.top >= rect.top - 1 && bounds.bottom <= rect.bottom + 1
				? shell : el.svgHost.lastElementChild;
		}
		return pageShellAtPoint(x, rect.top + 1);
	}
	return pageShellAtPoint(x, rect.top + rect.height * 0.45)
		|| pageShellAtPoint(x, rect.top + rect.height * 0.25)
		|| pageShellAtPoint(x, rect.top + rect.height * 0.65);
}

function pageShellAtPoint(x, y) {
	return document.elementFromPoint(x, y)?.closest?.(".page-shell") || null;
}

function setCurrentPage(index) {
	state.pageIndex = index;
	if (state.fitMode === "width") {
		state.scale = fitWidthScale(currentPageInfo());
		el.zoomLabel.textContent = `${Math.round(state.scale * 100)}%`;
	}
	updatePageListCurrent();
	updateControls();
	if (state.doc) {
		setStatus(pageStatus(index, state.doc.pageCount));
	}
}

function updatePageListCurrent() {
	const current = el.pageList.querySelector(".page-list-item[aria-current]");
	if (current) {
		if (Number.parseInt(current.dataset.pageIndex, 10) === state.pageIndex) {
			return;
		}
		current.removeAttribute("aria-current");
	}
	const next = el.pageList.querySelector(`.page-list-item[data-page-index="${state.pageIndex}"]`);
	if (next) {
		setPageItemCurrent(next, true);
	}
}

function setPageItemCurrent(item, current) {
	if (current) {
		item.setAttribute("aria-current", "page");
		return;
	}
	item.removeAttribute("aria-current");
}

function layoutPages() {
	if (!state.doc) {
		return;
	}
	for (const page of state.doc.pages || []) {
		const shell = pageShell(page.index);
		if (shell) {
			layoutPageShell(shell, page);
		}
	}
}

function layoutPageShell(shell, page) {
	const width = Math.max(1, page.width * MM_TO_PX);
	const height = Math.max(1, page.height * MM_TO_PX);
	const scale = state.fitMode === "width" ? state.scale * currentPageInfo().width / page.width : state.scale;
	shell.style.width = `${width * scale}px`;
	shell.style.height = `${height * scale}px`;
	const surface = shell.querySelector(".page-surface");
	if (surface) {
		surface.style.width = `${width}px`;
		surface.style.height = `${height}px`;
		surface.style.transform = `scale(${scale})`;
	}
}

function parseSVG(svgText, prefix = "") {
	const parsed = new DOMParser().parseFromString(svgText, "image/svg+xml");
	const error = parsed.querySelector("parsererror");
	if (error) {
		throw new Error(error.textContent.trim());
	}
	const svg = document.importNode(parsed.documentElement, true);
	prefixSVGIds(svg, prefix);
	return svg;
}

function prefixSVGIds(svg, prefix) {
	if (!prefix) {
		return;
	}
	const idMap = new Map();
	for (const node of svg.querySelectorAll("[id]")) {
		const id = node.getAttribute("id");
		if (!id) {
			continue;
		}
		const next = `${prefix}-${id}`;
		idMap.set(id, next);
		node.setAttribute("id", next);
	}
	if (idMap.size === 0) {
		return;
	}
	const replaceRef = (value) => {
		if (!value) {
			return value;
		}
		let next = value.replace(/url\(#([^)]+)\)/g, (match, id) => {
			const mapped = idMap.get(id);
			return mapped ? `url(#${mapped})` : match;
		});
		if (next.startsWith("#")) {
			const mapped = idMap.get(next.slice(1));
			if (mapped) {
				next = `#${mapped}`;
			}
		}
		return next;
	};
	const attrs = ["clip-path", "fill", "filter", "href", "marker-end", "marker-mid", "marker-start", "mask", "stroke", "style", "xlink:href"];
	for (const node of svg.querySelectorAll("*")) {
		for (const attr of attrs) {
			if (node.hasAttribute(attr)) {
				node.setAttribute(attr, replaceRef(node.getAttribute(attr)));
			}
		}
	}
}

function renderPageList() {
	el.pageList.replaceChildren();
	if (!state.doc) {
		return;
	}
	if (state.thumbnailObserver) {
		state.thumbnailObserver.disconnect();
	}
	const fragment = document.createDocumentFragment();
	const thumbnailTargets = [];
	const openSeq = state.openSeq;
	for (const page of state.doc.pages || []) {
		const button = document.createElement("button");
		button.type = "button";
		button.className = "page-list-item";
		button.dataset.pageIndex = String(page.index);
		button.title = `${formatSize(page.width)} x ${formatSize(page.height)} mm`;
		setPageItemCurrent(button, page.index === state.pageIndex);
		if (page.width > 0 && page.height > 0) {
			button.style.setProperty("--thumb-ratio", `${page.width} / ${page.height}`);
		}

		const thumb = document.createElement("span");
		thumb.className = "thumb-paper";
		thumb.setAttribute("aria-hidden", "true");
		setThumbnailContent(thumb, state.thumbnailCache.get(page.index), page.index, openSeq);

		const label = document.createElement("span");
		label.className = "thumb-label";
		label.textContent = `第 ${page.index + 1} 页`;

		const size = document.createElement("span");
		size.className = "thumb-size";
		size.textContent = `${formatSize(page.width)} x ${formatSize(page.height)} mm`;

		button.append(thumb, label, size);
		button.addEventListener("click", () => renderPage(page.index));
		fragment.append(button);
		thumbnailTargets.push([button, page.index]);
	}
	el.pageList.append(fragment);
	for (const [button, index] of thumbnailTargets) {
		observeThumbnail(button, index, openSeq);
	}
}

function resetThumbnails() {
	state.thumbnailCache.clear();
	state.visibleThumbnails.clear();
	state.thumbnailInFlight.clear();
	if (state.thumbnailObserver) {
		state.thumbnailObserver.disconnect();
		state.thumbnailObserver = null;
	}
}

function cacheThumbnail(index, svgText) {
	if (typeof svgText === "string" && svgText) {
		state.thumbnailCache.set(index, svgText);
	}
}

function setThumbnailContent(container, svgText, index, openSeq = state.openSeq) {
	const renderKey = `${openSeq}:${index}`;
	if (svgText && container.dataset.renderKey === renderKey) {
		return;
	}
	delete container.dataset.renderKey;
	container.replaceChildren();
	if (!svgText) {
		container.classList.add("pending");
		container.textContent = String(index + 1);
		return;
	}
	try {
		const svg = parseSVG(svgText, `t${openSeq}-${index}`);
		svg.classList.add("thumb-svg");
		svg.setAttribute("aria-hidden", "true");
		container.classList.remove("pending", "error");
		container.append(svg);
		container.dataset.renderKey = renderKey;
	} catch {
		container.classList.add("error");
		container.textContent = String(index + 1);
	}
}

function observeThumbnail(button, index, openSeq = state.openSeq) {
	const observer = thumbnailObserver();
	if (observer) {
		observer.observe(button);
	}
	if (index === state.pageIndex || index < 6) {
		renderThumbnail(index, openSeq);
		return;
	}
	if (observer) {
		return;
	}
	if (index < 8) {
		renderThumbnail(index, openSeq);
	}
}

function thumbnailObserver() {
	if (!("IntersectionObserver" in window)) {
		return null;
	}
	if (!state.thumbnailObserver) {
		const openSeq = state.openSeq;
		state.thumbnailObserver = new IntersectionObserver((entries) => {
			if (openSeq !== state.openSeq) {
				return;
			}
			for (const entry of entries) {
				const index = Number.parseInt(entry.target.dataset.pageIndex, 10);
				if (!entry.isIntersecting) {
					state.visibleThumbnails.delete(index);
					const thumb = entry.target.querySelector(".thumb-paper");
					if (thumb.dataset.renderKey) {
						setThumbnailContent(thumb, "", index, openSeq);
					}
					continue;
				}
				state.visibleThumbnails.add(index);
				renderThumbnail(index, openSeq);
			}
		}, {
			root: el.pageListPanel,
			rootMargin: "180px 0px",
		});
	}
	return state.thumbnailObserver;
}

async function renderThumbnail(index, openSeq = state.openSeq) {
	if (openSeq !== state.openSeq || !state.doc) {
		return;
	}
	if (state.thumbnailCache.has(index)) {
		updateThumbnail(index, openSeq);
		return;
	}
	const key = `${openSeq}:${index}`;
	if (state.thumbnailInFlight.has(key)) {
		return;
	}
	state.thumbnailInFlight.add(key);
	try {
		const page = await loadPageData(index, { openSeq, priority: 5 });
		if (openSeq !== state.openSeq) {
			return;
		}
		if (!page) {
			return;
		}
		updateThumbnail(index, openSeq);
	} catch {
		if (openSeq === state.openSeq) {
			markThumbnailError(index);
		}
	} finally {
		state.thumbnailInFlight.delete(key);
	}
}

function updateThumbnail(index, openSeq = state.openSeq) {
	if (openSeq !== state.openSeq || (state.thumbnailObserver && !state.visibleThumbnails.has(index) && index !== state.pageIndex)) {
		return;
	}
	const thumb = el.pageList.querySelector(`[data-page-index="${index}"] .thumb-paper`);
	if (thumb) {
		setThumbnailContent(thumb, state.thumbnailCache.get(index), index, openSeq);
	}
}

function markThumbnailError(index) {
	const thumb = el.pageList.querySelector(`[data-page-index="${index}"] .thumb-paper`);
	if (thumb) {
		thumb.classList.add("error");
		thumb.textContent = String(index + 1);
	}
}

function renderMeta() {
	const doc = state.doc || {};
	document.title = `OFDGo WebUI - ${state.fileName}`;
	el.metaFile.textContent = state.fileName;
	el.metaTitle.textContent = doc.title || "-";
	el.metaAuthor.textContent = doc.author || "-";
	el.metaVersion.textContent = doc.version || "-";
	el.metaType.textContent = doc.docType || "-";
	el.metaFonts.textContent = String(doc.fontCount || 0);
	el.metaSignatures.textContent = doc.detailsPending ? "正在检查" : String(doc.signatureCount || 0);
	el.pageTotal.textContent = String(doc.pageCount || 0);
	renderSignatures();
	renderDocumentFonts();
	renderFontList();
	updateLocalFontButton();
}

function renderSignatures() {
	const signatures = state.doc?.signatures || [];
	const showPanel = Boolean(state.doc?.signatureError || signatures.length);
	el.signaturePanel.hidden = !showPanel;
	el.signatureList.replaceChildren();
	el.signatureSummary.textContent = signatureSummary(signatures);
	if (!showPanel) {
		return;
	}
	if (state.doc?.signatureError) {
		const empty = document.createElement("div");
		empty.className = "font-empty";
		empty.textContent = state.doc.signatureError;
		el.signatureList.append(empty);
		return;
	}
	const fragment = document.createDocumentFragment();
	for (const signature of signatures) {
		const row = document.createElement("div");
		row.className = `signature-row ${signature.status}`;

		const head = document.createElement("div");
		head.className = "signature-head";

		const name = signatureNameNode(signature);

		const badges = document.createElement("div");
		badges.className = "signature-badges";
		badges.append(fontBadge(signature.status === "valid" ? "通过" : signature.status === "invalid" ? "异常" : "未验", signature.status));

		head.append(name, badges);
		row.append(head);
		appendSignatureLine(row, "编号", signature.id);
		appendSignatureLine(row, "版本", signature.version);
		appendSignatureLine(row, "章图", signature.sealType);
		appendSignatureLine(row, "章号", signature.sealId);
		appendSignatureLine(row, "章名", signature.sealName);
		appendSignatureLine(row, "厂商", signature.sealVendor);
		appendSignatureLine(row, "签者", signature.signer);
		appendSignatureLine(row, "时间", formatSignatureTime(signature.signatureDateTime));
		appendSignatureLine(row, "机构", signatureAgency(signature));
		appendSignatureCheck(row, "原文", signature.dataHashChecked, signature.dataHashOK);
		appendSignatureCheck(row, "签名", signature.signedValueChecked, signature.signedValueOK);
		if (signature.type !== "Sign") {
			appendSignatureCheck(row, "章验", signature.sealChecked, signature.sealOK);
			appendSignaturePolicy(row, "一致", signature.sealMatchChecked, signature.sealMatchOK);
		}
		appendSignatureCheck(row, "证书", signature.certChecked, signature.certOK);
		appendSignaturePolicy(row, "签期", signature.signatureTimeChecked, signature.signatureTimeOK);
		appendSignaturePolicy(row, "章期", signature.sealTimeChecked, signature.sealTimeOK);
		if (signature.sealCertTimeChecked) {
			appendSignatureLine(row, "制期", signature.sealCertTimeOK ? "有效" : "失效", signature.sealCertTimeOK ? "ok" : "");
		}
		appendSignaturePolicy(row, "时效", signature.certTimeChecked, signature.certTimeOK);
		appendSignatureCheck(row, "信任", signature.certTrustChecked, signature.certTrustOK);
		appendSignatureLine(row, "保护", signatureReferenceText(signature), signatureReferenceStatus(signature));
		appendSignatureLine(row, "算法", signature.signatureMethod);
		appendSignatureLine(row, "散列", signature.digestMethod);
		appendSignatureLine(row, "序号", signature.signSerial);
		appendSignatureLine(row, "主体", signature.signSubject && signature.signSubject !== signature.signer ? signature.signSubject : "");
		appendSignatureLine(row, "颁发", signature.signIssuer);
		appendSignatureLine(row, "章证", signature.sealSubject);
		appendSignatureLine(row, "错误", signature.error, "fail");
		fragment.append(row);
	}
	el.signatureList.append(fragment);
}

function signatureSummary(signatures) {
	if (!signatures.length) {
		return "0";
	}
	const invalid = signatures.filter((signature) => signature.status === "invalid").length;
	const unchecked = signatures.filter((signature) => signature.status === "unchecked").length;
	const parts = [String(signatures.length)];
	if (invalid) {
		parts.push(`异常 ${invalid}`);
	}
	if (unchecked) {
		parts.push(`未验 ${unchecked}`);
	}
	return parts.length > 1 ? parts.join(" · ") : `通过 ${signatures.length}`;
}

function signatureNameNode(signature) {
	const stamps = signature.stamps || [];
	if (!stamps.length) {
		const name = document.createElement("div");
		name.className = "signature-name";
		name.textContent = "签名";
		return name;
	}
	const name = document.createElement("div");
	name.className = "signature-name signature-name-with-stamps";

	const button = document.createElement("button");
	button.type = "button";
	button.className = "signature-name-button";
	button.textContent = "签名";
	button.addEventListener("click", () => focusSignatureStamp(stamps[0]));
	name.append(button, signatureStampGroup(stamps));
	return name;
}

function signatureStampGroup(stamps) {
	const group = document.createElement("span");
	group.className = "signature-stamp-group";
	group.append("（");
	for (const [index, stamp] of stamps.entries()) {
		if (index > 0) {
			group.append("、");
		}
		const button = document.createElement("button");
		button.type = "button";
		button.className = "signature-stamp-link";
		button.textContent = `第${index + 1}处`;
		button.addEventListener("click", () => focusSignatureStamp(stamp));
		group.append(button);
	}
	group.append("）");
	return group;
}

function signatureAgency(signature) {
	if (signature.company && signature.provider) {
		return `${signature.company} · ${signature.provider}`;
	}
	return signature.company || signature.provider || "";
}

function signatureReferenceText(signature) {
	const passed = signature.referencePassed || 0;
	const count = signature.referenceCount || 0;
	if (!count) {
		return "";
	}
	const status = signatureReferenceStatus(signature);
	return `${status === "ok" ? "通过" : status === "fail" ? "失败" : "未验"} ${passed}/${count}`;
}

function signatureReferenceStatus(signature) {
	const count = signature.referenceCount || 0;
	if (!count) {
		return "";
	}
	if (signature.referencePassed < signature.referenceChecked) {
		return "fail";
	}
	return signature.referenceChecked === count ? "ok" : "";
}

function formatSignatureTime(value) {
	const text = String(value || "").trim();
	return text.replace(/^(\d{4})(\d{2})(\d{2})(\d{2})(\d{2})(\d{2})(\.\d+)?(Z|[+-]\d{2}:?\d{2})?$/, "$1-$2-$3 $4:$5:$6$7$8").replace("T", " ");
}

function appendSignatureLine(row, label, value, status = "") {
	if (!value && value !== 0) {
		return;
	}
	const line = document.createElement("div");
	line.className = status ? `signature-line ${status}` : "signature-line";

	const key = document.createElement("span");
	key.className = "signature-label";
	key.textContent = label;

	const text = document.createElement("span");
	text.className = "signature-value";
	text.textContent = String(value);

	line.append(key, text);
	row.append(line);
}

function appendSignatureCheck(row, label, checked, ok) {
	appendSignatureLine(row, label, checked ? ok ? "通过" : "失败" : "未验", checked ? ok ? "ok" : "fail" : "");
}

function appendSignaturePolicy(row, label, checked, ok) {
	if (!checked) {
		return;
	}
	appendSignatureCheck(row, label, checked, ok);
}

async function focusSignatureStamp(stamp) {
	const pageIndex = (stamp.page || 0) - 1;
	if (!state.doc || pageIndex < 0) {
		return;
	}
	await renderPage(pageIndex, { fit: false, scroll: false });
	await nextFrame();
	highlightSignatureStamp(stamp);
	setStatus(`签名已定位 第 ${stamp.page} 页`);
}

function highlightSignatureStamp(stamp) {
	clearStampHighlights();
	const pageIndex = (stamp.page || 0) - 1;
	const shell = pageShell(pageIndex);
	if (!shell || !stamp.width || !stamp.height) {
		return;
	}
	const mark = document.createElement("div");
	mark.className = "stamp-highlight";
	mark.style.left = `${stamp.x * MM_TO_PX * state.scale}px`;
	mark.style.top = `${stamp.y * MM_TO_PX * state.scale}px`;
	mark.style.width = `${stamp.width * MM_TO_PX * state.scale}px`;
	mark.style.height = `${stamp.height * MM_TO_PX * state.scale}px`;
	shell.append(mark);
	mark.scrollIntoView({ block: "nearest", inline: "nearest" });
	window.setTimeout(() => mark.remove(), 1800);
}

function clearStampHighlights() {
	for (const mark of el.svgHost.querySelectorAll(".stamp-highlight")) {
		mark.remove();
	}
}

function renderDocumentFonts() {
	const fonts = state.doc?.fonts || [];
	el.docFontList.replaceChildren();
	el.docFontSummary.textContent = fontSummary(fonts);
	if (!fonts.length) {
		const empty = document.createElement("div");
		empty.className = "font-empty";
		empty.textContent = "暂无字体";
		el.docFontList.append(empty);
		return;
	}
	const fragment = document.createDocumentFragment();
	for (const font of fonts) {
		const row = document.createElement("div");
		row.className = `doc-font-row ${font.status || "missing"}`;

		const head = document.createElement("div");
		head.className = "doc-font-head";

		const name = document.createElement("div");
		name.className = "doc-font-name";
		name.textContent = font.fontName || font.familyName || font.id || "-";

		const badges = document.createElement("div");
		badges.className = "doc-font-badges";
		badges.append(fontBadge(statusText(font.status), font.status || "missing"));
		if (font.embedded && font.status !== "embedded") {
			badges.append(fontBadge("内嵌", "embedded"));
		}
		const resultText = fontResult(font);
		const metaText = fontMeta(font);

		head.append(name, badges);
		row.append(head);
		appendFontDetail(row, metaText);
		appendFontDetail(row, resultText);
		fragment.append(row);
	}
	el.docFontList.append(fragment);
}

function appendFontDetail(row, text) {
	if (!text) {
		return;
	}
	const detail = document.createElement("div");
	detail.className = "doc-font-detail";
	detail.textContent = text;
	detail.title = text;
	row.append(detail);
}

function fontBadge(text, status) {
	const badge = document.createElement("span");
	badge.className = `font-badge ${status}`;
	badge.textContent = text;
	return badge;
}

function fontSummary(fonts) {
	if (!fonts.length) {
		return "0";
	}
	const missing = fonts.filter((font) => font.status === "missing").length;
	const fallback = fonts.filter((font) => font.status === "fallback").length;
	if (missing || fallback) {
		return `${fonts.length} · 缺失 ${missing} · 回退 ${fallback}`;
	}
	return `共 ${fonts.length}`;
}

function pageStatus(index, pageCount) {
	return `第 ${index + 1} / ${pageCount} 页`;
}

function statusText(status) {
	switch (status) {
	case "pending":
		return "待查";
	case "embedded":
		return "内嵌";
	case "matched":
		return "匹配";
	case "fallback":
		return "回退";
	default:
		return "缺失";
	}
}

function fontResult(font) {
	const parts = [];
	if (font.matched) {
		parts.push(font.embedded || font.status === "embedded" ? `内嵌 ${font.matched}` : `匹配 ${font.matched}`);
	}
	if (font.detail) {
		parts.push(font.detail);
	}
	return parts.join(" · ");
}

function fontMeta(font) {
	const parts = [];
	if (font.familyName && font.familyName !== font.fontName) {
		parts.push(`字族 ${font.familyName}`);
	}
	if (font.charset) {
		parts.push(`字集 ${font.charset}`);
	}
	return parts.join(" · ");
}

function setContinuous(continuous) {
	state.continuous = continuous;
	el.pageFrame.classList.toggle("continuous", state.continuous);
	el.continuousButton.setAttribute("aria-pressed", String(state.continuous));
}

function toggleContinuous() {
	const anchor = state.fitMode === "free" ? scaleAnchor() : null;
	setContinuous(!state.continuous);
	clearStampHighlights();
	if (state.fitMode === "free") {
		restoreScaleAnchor(anchor);
	} else {
		applyFit(false);
		scrollToPage(state.pageIndex);
	}
}

function fitWidth(updateStatus = true) {
	const page = currentPageInfo();
	if (!page) {
		return;
	}
	setScale(fitWidthScale(page), updateStatus, "width");
	if (updateStatus) {
		scrollToPage(state.pageIndex);
	}
}

function fitWidthScale(page) {
	const space = pageSpace();
	const width = Math.max(1, page.width * MM_TO_PX);
	let available = Math.max(1, el.viewerPanel.clientWidth - space * 2);
	if (!viewerHasVerticalScrollbar()) {
		const height = state.continuous
			? state.doc.pages.reduce((total, item) => total + available * item.height / item.width, 0)
			: Math.max(1, page.height * MM_TO_PX) * (available / width);
		if (height > Math.max(1, el.viewerPanel.clientHeight - space * 2)) {
			available = Math.max(1, available - scrollbarWidth());
		}
	}
	return Math.min(4, Math.max(0.2, available / width));
}

function fitHeight(updateStatus = true) {
	const page = currentPageInfo();
	if (!page) {
		return;
	}
	const space = pageSpace();
	let availableWidth = Math.max(1, el.viewerPanel.clientWidth - space * 2);
	let availableHeight = Math.max(1, el.viewerPanel.clientHeight - space * 2);
	const width = Math.max(1, page.width * MM_TO_PX);
	const height = Math.max(1, page.height * MM_TO_PX);
	if (state.continuous) {
		availableWidth = Math.max(1, el.viewerPanel.offsetWidth);
		availableHeight = Math.max(1, el.viewerPanel.offsetHeight);
		const contentWidth = state.doc.pages.reduce((max, item) => Math.max(max, item.width), 0) * MM_TO_PX;
		const contentHeight = state.doc.pages.reduce((total, item) => total + item.height, 0) * MM_TO_PX;
		const scale = Math.min(availableWidth / width, availableHeight / height);
		const needsVerticalScrollbar = scale > availableHeight / contentHeight;
		const needsHorizontalScrollbar = scale > availableWidth / contentWidth;
		if (needsVerticalScrollbar || needsHorizontalScrollbar) {
			const scrollbar = scrollbarWidth();
			availableWidth = Math.max(1, availableWidth - (needsVerticalScrollbar ? scrollbar : 0));
			availableHeight = Math.max(1, availableHeight - (needsHorizontalScrollbar ? scrollbar : 0));
			const nextScale = Math.min(availableWidth / width, availableHeight / height);
			if (!needsVerticalScrollbar && nextScale > availableHeight / contentHeight) {
				availableWidth = Math.max(1, availableWidth - scrollbar);
			}
			if (!needsHorizontalScrollbar && nextScale > availableWidth / contentWidth) {
				availableHeight = Math.max(1, availableHeight - scrollbar);
			}
		}
	}
	setScale(Math.min(availableWidth / width, availableHeight / height), updateStatus, "height");
	if (updateStatus) {
		scrollToPage(state.pageIndex);
	}
}

function applyFit(updateStatus = true) {
	if (state.fitMode === "height") {
		fitHeight(updateStatus);
		return;
	}
	if (state.fitMode === "width") {
		fitWidth(updateStatus);
	}
}

function setScale(nextScale, updateStatus = true, fitMode = "free") {
	const anchor = updateStatus && fitMode === "free" ? scaleAnchor() : null;
	const scale = Math.min(4, Math.max(0.2, nextScale));
	const layoutChanged = scale !== state.scale || fitMode !== state.fitMode;
	state.fitMode = fitMode;
	state.scale = scale;
	if (layoutChanged) {
		clearStampHighlights();
		layoutPages();
	}
	updateFitSpace();
	if (layoutChanged) {
		restoreScaleAnchor(anchor);
	}
	el.zoomLabel.textContent = `${Math.round(state.scale * 100)}%`;
	if (updateStatus && state.doc) {
		setStatus(`第 ${state.pageIndex + 1} / ${state.doc.pageCount} 页`);
	}
	updateControls();
}

function scaleAnchor() {
	const shell = pageShellFromView() || pageShell(state.pageIndex);
	if (!shell) {
		return null;
	}
	const viewerRect = el.viewerPanel.getBoundingClientRect();
	const shellRect = shell.getBoundingClientRect();
	return {
		index: Number.parseInt(shell.dataset.pageIndex, 10),
		x: (viewerRect.left + viewerRect.width / 2 - shellRect.left) / Math.max(1, shellRect.width),
		y: (viewerRect.top + viewerRect.height * 0.45 - shellRect.top) / Math.max(1, shellRect.height),
	};
}

function restoreScaleAnchor(anchor) {
	if (!anchor) {
		return;
	}
	const shell = pageShell(anchor.index);
	if (!shell) {
		return;
	}
	const viewerRect = el.viewerPanel.getBoundingClientRect();
	const shellRect = shell.getBoundingClientRect();
	el.viewerPanel.scrollLeft += shellRect.left + shellRect.width * anchor.x - viewerRect.left - viewerRect.width / 2;
	el.viewerPanel.scrollTop += shellRect.top + shellRect.height * anchor.y - viewerRect.top - viewerRect.height * 0.45;
}

function currentPageInfo() {
	return state.doc?.pages?.[state.pageIndex] || null;
}

function pageSpace() {
	return Number.parseFloat(getComputedStyle(el.pageFrame).paddingLeft) || 0;
}

function pageBlockSpace() {
	return Number.parseFloat(getComputedStyle(el.pageFrame).paddingTop) || pageSpace();
}

function viewerHasVerticalScrollbar() {
	return el.viewerPanel.scrollHeight > el.viewerPanel.clientHeight;
}

function scrollbarWidth() {
	const probe = document.createElement("div");
	probe.style.position = "absolute";
	probe.style.width = "100px";
	probe.style.height = "100px";
	probe.style.overflow = "scroll";
	probe.style.left = "-9999px";
	document.body.append(probe);
	const width = probe.offsetWidth - probe.clientWidth;
	probe.remove();
	return width;
}

function updateFitSpace() {
	const page = currentPageInfo();
	if (!page || state.fitMode === "free") {
		el.viewerPanel.classList.remove("single-page-fits-height");
		el.pageFrame.style.removeProperty("--fit-space");
		el.pageFrame.style.removeProperty("--fit-gap");
		return;
	}
	const shell = pageShell(state.pageIndex);
	const height = shell ? shell.getBoundingClientRect().height : Math.max(1, page.height * MM_TO_PX * state.scale);
	const base = pageSpace();
	el.viewerPanel.classList.toggle("single-page-fits-height", state.doc.pageCount === 1 && height <= el.viewerPanel.clientHeight - base * 2);
	const space = state.continuous ? 0 : Math.max(base, (el.viewerPanel.clientHeight - height) / 2);
	const gap = space > base ? space + 1 : space;
	el.pageFrame.style.setProperty("--fit-space", `${space}px`);
	el.pageFrame.style.setProperty("--fit-gap", `${gap}px`);
}

function updateControls() {
	const hasDoc = Boolean(state.doc);
	const pageCount = state.doc ? state.doc.pageCount : 0;
	el.prevButton.disabled = !hasDoc || state.pageIndex <= 0;
	el.nextButton.disabled = !hasDoc || state.pageIndex >= pageCount - 1;
	el.pageInput.disabled = !hasDoc;
	el.pageInput.max = String(pageCount || 1);
	el.pageInput.value = String(hasDoc ? state.pageIndex + 1 : 0);
	el.zoomOutButton.disabled = !hasDoc || state.scale <= 0.2;
	el.zoomInButton.disabled = !hasDoc || state.scale >= 4;
	el.fitButton.disabled = !hasDoc;
	el.fitHeightButton.disabled = !hasDoc;
	el.continuousButton.disabled = !hasDoc;
	el.fitButton.toggleAttribute("aria-pressed", hasDoc && state.fitMode === "width");
	el.fitHeightButton.toggleAttribute("aria-pressed", hasDoc && state.fitMode === "height");
	updateAnnotationButton();
	el.pageExportFormat.disabled = !hasDoc || !state.exportFormats.length;
	el.exportPageButton.disabled = !hasDoc || !state.exportFormats.length;
	el.exportButton.disabled = !hasDoc;
	updateDPIControl();
}

function updateAnnotationButton() {
	el.annotationButton.setAttribute("aria-pressed", String(state.renderAnnotations));
	el.annotationButton.title = state.renderAnnotations ? "关闭注解" : "开启注解";
	el.annotationButton.setAttribute("aria-label", el.annotationButton.title);
}

function setExportControlsDisabled(disabled) {
	el.exportButton.disabled = disabled;
	el.exportPageButton.disabled = disabled;
	el.pageExportFormat.disabled = disabled;
	updateDPIControl(disabled);
}

function callWASM(name, ...args) {
	const fn = globalThis[name];
	if (typeof fn !== "function") {
		throw new Error("渲染引擎未初始化");
	}
	if (state.wasmExited) {
		scheduleWASMRecovery();
		throw new Error(state.wasmRecovering ? "渲染引擎正在恢复" : "渲染引擎已退出，正在恢复");
	}
	let payload;
	try {
		payload = fn(...args);
	} catch (err) {
		const message = String(err.message || err);
		if (message.includes("Go program has already exited")) {
			markWASMExited(state.wasmSeq);
			throw new Error("渲染引擎已退出，正在恢复");
		}
		throw err;
	}
	const result = typeof payload === "string" ? JSON.parse(payload) : payload;
	if (!result.ok) {
		throw new Error(result.error || "WASM 调用失败");
	}
	return result.data;
}

function showError(err, empty = !state.doc) {
	const message = String(err.message || err);
	setStatus(message);
	if (empty) {
		setEmpty(message);
	}
}

function setBusy(busy, text = "", percent = 0, status = "") {
	document.body.toggleAttribute("aria-busy", busy);
	el.fontList.inert = busy;
	if (!busy) {
		el.progressPanel.hidden = true;
		if (state.fontSyncPending) {
			window.setTimeout(syncUserFonts, 0);
		}
		return;
	}
	el.progressPanel.hidden = false;
	setProgress(text, percent, status);
}

function setProgress(text = "", percent = 0, status = "") {
	if (text) {
		el.progressLabel.textContent = text;
	}
	if (status) {
		setStatus(status);
	}
	el.progressBar.parentElement.hidden = percent === null;
	if (percent === null) {
		return;
	}
	const value = Math.max(0, Math.min(100, percent));
	el.progressBar.style.width = `${value}%`;
	el.progressBar.setAttribute("aria-valuenow", String(value));
}

function setStatus(text) {
	el.statusText.textContent = text;
}

function setEmpty(text) {
	el.emptyState.hidden = false;
	el.pageFrame.hidden = true;
	el.emptyState.textContent = text;
}

function formatSize(value) {
	if (!Number.isFinite(value) || value <= 0) {
		return "-";
	}
	return value.toFixed(1);
}

function downloadBytes(bytes, mime, name) {
	const blob = new Blob([bytes], { type: mime });
	const link = document.createElement("a");
	const url = URL.createObjectURL(blob);
	link.href = url;
	link.download = name;
	document.body.append(link);
	link.click();
	link.remove();
	window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}

function baseFileName() {
	const base = (state.fileName || state.doc?.title || "ofdgo").replace(/\.[^.]+$/, "");
	return base.replace(/[\\/:*?"<>|]+/g, "_").trim() || "ofdgo";
}

function pdfFileName() {
	return `${baseFileName()}.pdf`;
}

function pageFileName(extension) {
	const page = String(state.pageIndex + 1).padStart(String(state.doc?.pageCount || 1).length, "0");
	return `${baseFileName()}_p${page}.${extension || "bin"}`;
}

function exportFormatInfo(value) {
	return state.exportFormats.find((format) => format.value === value) || null;
}

function exportFormatUsesDPI(value) {
	return value === "png" || value === "jpg";
}

function updateDPIControl(disabled = false) {
	el.imageDPI.disabled = disabled || !state.doc || !exportFormatUsesDPI(el.pageExportFormat.value);
}

function currentImageDPI() {
	return Number.parseFloat(el.imageDPI.value) || DEFAULT_IMAGE_DPI;
}

function formatBytes(size, fallback = "PDF") {
	if (!Number.isFinite(size) || size <= 0) {
		return fallback;
	}
	if (size < 1024 * 1024) {
		return `${Math.round(size / 1024)} KB`;
	}
	return `${(size / 1024 / 1024).toFixed(1)} MB`;
}

function nextFrame() {
	return new Promise((resolve) => requestAnimationFrame(resolve));
}

function waitForPaint() {
	return new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
}
