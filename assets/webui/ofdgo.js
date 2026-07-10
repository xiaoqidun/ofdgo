const MM_TO_PX = 96 / 25.4;
const COMMON_FONT_NAMES = [
	"SimSun",
	"NSimSun",
	"SimHei",
	"KaiTi",
	"FangSong",
	"Microsoft YaHei",
	"PingFang SC",
	"Noto Sans CJK SC",
	"Source Han Sans SC",
	"FZXiaoBiaoSong-B05",
	"Arial",
	"Arial Black",
	"Helvetica",
	"Courier New",
	"Times New Roman",
];
const LOCAL_FONT_LOAD_LIMIT = 16;
const COMPACT_LAYOUT = window.matchMedia("(max-width: 900px)");
const DEFAULT_IMAGE_DPI = 300;
const STATUS = {
	ready: "选择 OFD 文件",
	opening: "正在打开 OFD",
	engine: "正在准备引擎",
	recovering: "正在恢复引擎",
	fonts: "正在匹配字体",
	exporting: "正在导出文档 PDF",
	pageExporting: "正在导出单页",
};
const WASM_CALLBACKS = [
	"ofdgoOpen",
	"ofdgoRenderPage",
	"ofdgoExportFormats",
	"ofdgoExportPage",
	"ofdgoExportPDF",
	"ofdgoFontSystemNames",
];

let wasmPromise = null;
let wasmModule = null;
let wasmRecoveryTimer = 0;

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
	systemFontCatalog: [],
	systemFontCatalogLoaded: false,
	systemFontPermission: "prompt",
	doc: null,
	pageIndex: 0,
	scale: 1,
	fitMode: "width",
	renderAnnotations: true,
	pageCache: new Map(),
	pageInFlight: new Map(),
	pageRenderQueue: [],
	pageRenderRunning: false,
	pageObserver: null,
	scrollFrame: 0,
	thumbnailCache: new Map(),
	thumbnailInFlight: new Set(),
	thumbnailObserver: null,
	exportFormats: [],
	showPages: !COMPACT_LAYOUT.matches,
	showMeta: !COMPACT_LAYOUT.matches,
};

const el = {
	ofdInput: document.querySelector("#ofdInput"),
	ofdButton: document.querySelector("#ofdButton"),
	fontInput: document.querySelector("#fontInput"),
	togglePagesButton: document.querySelector("#togglePagesButton"),
	toggleMetaButton: document.querySelector("#toggleMetaButton"),
	fontAddButton: document.querySelector("#fontAddButton"),
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
el.fontAddButton.addEventListener("click", openFontFile);
el.localFontButton.addEventListener("click", loadLocalFonts);
el.ofdInput.addEventListener("change", openSelectedOFD);
el.fontInput.addEventListener("change", openSelectedFonts);
el.prevButton.addEventListener("click", () => renderPage(state.pageIndex - 1));
el.nextButton.addEventListener("click", () => renderPage(state.pageIndex + 1));
el.zoomOutButton.addEventListener("click", () => setScale(state.scale - 0.1));
el.zoomInButton.addEventListener("click", () => setScale(state.scale + 0.1));
el.fitButton.addEventListener("click", fitWidth);
el.fitHeightButton.addEventListener("click", fitHeight);
el.annotationButton.addEventListener("click", toggleAnnotations);
el.pageExportFormat.addEventListener("change", () => updateDPIControl());
el.exportPageButton.addEventListener("click", exportCurrentPage);
el.exportButton.addEventListener("click", exportPDF);
el.pageInput.addEventListener("change", () => {
	const page = Number.parseInt(el.pageInput.value, 10);
	if (Number.isFinite(page)) {
		renderPage(page - 1);
	}
});
window.addEventListener("resize", () => {
	if (state.doc) {
		applyFit(false);
	}
});
COMPACT_LAYOUT.addEventListener("change", syncLayoutMode);
el.viewerPanel.addEventListener("scroll", () => {
	schedulePageSync();
});
el.viewerPanel.addEventListener("dblclick", openOFDFromViewer);

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

function openFontFile() {
	el.fontInput.value = "";
	el.fontInput.click();
}

function toggleSidebar(side) {
	if (side === "pages") {
		state.showPages = !state.showPages;
	} else if (side === "meta") {
		state.showMeta = !state.showMeta;
	}
	updateSidebarState();
	if (state.doc) {
		applyFit(false);
	}
}

function updateSidebarState() {
	document.body.toggleAttribute("data-hide-pages", !state.showPages);
	document.body.toggleAttribute("data-hide-meta", !state.showMeta);
	el.togglePagesButton.setAttribute("aria-pressed", String(state.showPages));
	el.toggleMetaButton.setAttribute("aria-pressed", String(state.showMeta));
}

function syncLayoutMode(event) {
	state.showPages = !event.matches;
	state.showMeta = !event.matches;
	updateSidebarState();
	if (state.doc) {
		applyFit(false);
	}
}

async function boot() {
	setBusy(true, "正在准备引擎", 8, STATUS.engine);
	try {
		await ensureWASM();
		setProgress("引擎准备完成", 70);
		loadExportFormats();
		setEmpty("选择 OFD 文件");
		updateFontSummary();
		renderFontList();
		updateControls();
		updateLocalFontButton();
		await refreshLocalFontPermission();
		setStatus(STATUS.ready);
		setBusy(false);
	} catch (err) {
		setStatus("渲染引擎加载失败");
		setEmpty(String(err.message || err));
		setBusy(false);
		return;
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
			wasmModule = await WebAssembly.compileStreaming(response.clone());
		} catch {
			const bytes = await response.arrayBuffer();
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

async function openSelectedOFD() {
	const file = el.ofdInput.files[0];
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
		state.fileName = file.name || "ofdgo.ofd";
		state.ofdBytes = new Uint8Array(await file.arrayBuffer());
		await openDocument({ pageIndex: 0, resetScroll: true });
	} catch (err) {
		showError(err, true);
		setBusy(false);
	}
}

function isOFDFile(file) {
	return /\.ofd$/i.test(file.name || "");
}

async function openSelectedFonts() {
	const files = Array.from(el.fontInput.files || []);
	if (!files.length) {
		return;
	}
	const fonts = await Promise.all(files.map(async (file) => (
		createFontRecord(file.name, new Uint8Array(await file.arrayBuffer()), "upload")
	)));
	state.userFonts.push(...fonts);
	el.fontInput.value = "";
	await applyFontChange();
}

async function loadLocalFonts() {
	if (!canReadLocalFonts()) {
		setStatus("当前浏览器不支持读取系统字体");
		return;
	}
	setBusy(true, state.doc ? "正在匹配字体" : "正在请求授权", 12, state.doc ? STATUS.fonts : "正在请求授权");
	try {
		await nextFrame();
		const available = await queryLocalFonts();
		if (!state.doc) {
			setStatus(available.length ? `已授权 ${available.length} 个系统字体` : "未读取到系统字体");
			return;
		}
		if (await loadDocumentLocalFonts(available)) {
			await applyFontChange({ skipAutoFonts: true });
		}
	} catch (err) {
		if (err && err.name === "NotAllowedError") {
			setStatus("未授权读取系统字体");
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
		setStatus(available.length ? `已授权 ${available.length} 个系统字体` : "未读取到系统字体");
	} catch (err) {
		if (err && err.name === "NotAllowedError") {
			setStatus("未授权读取系统字体");
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
			setStatus("未授权读取系统字体");
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
	if (docFonts.length && !docNames.length) {
		state.localFonts = [];
		setStatus("OFD 字体均为内嵌");
		updateFontSummary();
		renderFontList();
		return false;
	}
	const docWanted = fontNameKeys(docNames);
	const docLoadLimit = docNames.length ? Math.min(LOCAL_FONT_LOAD_LIMIT, Math.max(4, docNames.length * 3)) : 6;
	let selected = selectLocalFonts(available, docWanted, docLoadLimit);
	if (!selected.length) {
		const fallbackWanted = fontNameKeys(COMMON_FONT_NAMES);
		selected = selectLocalFonts(available, fallbackWanted, 6);
	}
	const emptyStatus = available.length === 0 ? "未读取到系统字体" : "未匹配到文档所需字体";
	const fonts = [];
	for (let i = 0; i < selected.length; i += 1) {
		setProgress(`正在读取字体 ${i + 1}/${selected.length}`, 20 + Math.round(i / Math.max(1, selected.length) * 60));
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
	state.localFonts = mergeFontRecords([], fonts);
	setStatus(fonts.length ? `已加载 ${fonts.length} 个系统字体` : emptyStatus);
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

function selectLocalFonts(fonts, wanted, limit) {
	const ranked = [];
	for (const font of fonts) {
		const rank = localFontMatchRank(font, wanted);
		if (!rank) {
			continue;
		}
		ranked.push({
			font,
			rank,
			styleRank: localFontStyleRank(font),
			name: normalizeFontName(localFontName(font)),
		});
	}
	ranked.sort((a, b) => (
		a.rank - b.rank ||
		a.styleRank - b.styleRank ||
		a.name.localeCompare(b.name)
	));
	return uniqueLocalFonts(ranked.map((item) => item.font)).slice(0, limit);
}

function allFonts() {
	return fontData(fontRecords());
}

function uploadedFonts() {
	return fontData(state.userFonts);
}

function fontData(fonts) {
	return fonts
		.filter((font) => font.enabled)
		.map((font) => ({
			name: font.name,
			data: font.data,
		}));
}

function updateFontSummary() {
	const total = fontRecords().length;
	const enabled = fontRecords().filter((font) => font.enabled).length;
	el.availableFontSummary.textContent = total ? `${enabled}/${total}` : "0";
}

function createFontRecord(name, data, source) {
	state.fontSeq += 1;
	return {
		id: `${source}-${state.fontSeq}`,
		name: name || "font.ttf",
		data,
		source,
		enabled: true,
	};
}

function mergeFontRecords(oldFonts, newFonts) {
	const seen = new Set(oldFonts.map((font) => normalizeFontName(font.name)));
	const merged = [...oldFonts];
	for (const font of newFonts) {
		const key = normalizeFontName(font.name);
		if (!key || seen.has(key)) {
			continue;
		}
		seen.add(key);
		merged.push(font);
	}
	return merged;
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

function fontNameKeys(names) {
	const keys = new Set();
	for (const name of fontSystemNames(names)) {
		const key = normalizeFontName(name);
		if (key) {
			keys.add(key);
		}
	}
	return keys;
}

function fontSystemNames(names) {
	const source = (Array.isArray(names) ? names : [])
		.map((name) => String(name || "").trim())
		.filter((name) => name !== "");
	if (!source.length) {
		return [];
	}
	if (state.ready && !state.wasmExited && typeof globalThis.ofdgoFontSystemNames === "function") {
		try {
			const names = callWASM("ofdgoFontSystemNames", source);
			if (Array.isArray(names)) {
				return names;
			}
		} catch {
			return source;
		}
	}
	return source;
}

function localFontMatchRank(font, wanted) {
	let best = 0;
	for (const name of [font.family, font.fullName, font.postscriptName, font.style]) {
		const key = normalizeFontName(name);
		if (!key) {
			continue;
		}
		for (const wantedKey of wanted) {
			let rank = 0;
			if (key === wantedKey) {
				rank = 1;
			} else if (key.startsWith(wantedKey) || wantedKey.startsWith(key)) {
				rank = 2;
			} else if (key.includes(wantedKey) || wantedKey.includes(key)) {
				rank = 3;
			}
			if (rank && (!best || rank < best)) {
				best = rank;
			}
		}
	}
	return best;
}

function localFontStyleRank(font) {
	const key = normalizeFontName(font.style);
	if (!key || key === "regular" || key === "normal") {
		return 0;
	}
	if (key.includes("bold") || key.includes("italic")) {
		return 2;
	}
	return 1;
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
		skipAutoFonts: options.skipAutoFonts !== false,
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
	});
}

function removeFont(id) {
	state.localFonts = state.localFonts.filter((font) => font.id !== id);
	state.userFonts = state.userFonts.filter((font) => font.id !== id);
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
			font.enabled = enabled.checked;
			enabledText.textContent = font.enabled ? "启用" : "停用";
			await applyFontChange();
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
			font.name = next;
			await applyFontChange();
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
			removeFont(font.id);
			await applyFontChange();
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
	el.localFontButton.title = supported ? "授权读取浏览器可访问的系统字体" : "当前浏览器不支持读取系统字体";
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
		const doc = callWASM("ofdgoOpen", state.ofdBytes, resetLocalFonts ? uploadedFonts() : allFonts(), state.renderAnnotations);
		if (openSeq !== state.openSeq) {
			return;
		}
		const pageCount = doc.pageCount || 0;
		const pageIndex = Math.min(Math.max(options.pageIndex || 0, 0), Math.max(pageCount - 1, 0));
		state.doc = doc;
		state.pageIndex = pageIndex;
		state.scale = options.scale || 1;
		state.fitMode = options.fitMode || "width";
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
	setBusy(true, "正在准备文档 PDF", 18, STATUS.exporting);
	try {
		await waitForPaint();
		if (openSeq !== state.openSeq) {
			return;
		}
		setProgress("正在生成文档 PDF", 45);
		await waitForPaint();
		if (openSeq !== state.openSeq) {
			return;
		}
		const result = callWASM("ofdgoExportPDF");
		if (openSeq !== state.openSeq) {
			return;
		}
		setProgress("正在保存文档 PDF", 86);
		const bytes = base64ToBytes(result.base64);
		downloadBytes(bytes, "application/pdf", pdfFileName());
		setStatus(`文档 PDF 已导出 ${formatBytes(result.size || bytes.length)}`);
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
		const bytes = base64ToBytes(result.base64);
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
		return;
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
			for (const entry of entries) {
				if (!entry.isIntersecting) {
					continue;
				}
				const index = Number.parseInt(entry.target.dataset.pageIndex, 10);
				state.pageObserver.unobserve(entry.target);
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
		const page = await loadPageData(index, { openSeq, priority: options.priority || 2 });
		if (openSeq !== state.openSeq) {
			return null;
		}
		if (page) {
			const shell = pageShell(index);
			if (!shell?.classList.contains("rendered")) {
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
	const priority = options.priority || 3;
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
		if (state.fitMode === "height") {
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
	return pageShellAtPoint(x, rect.top + rect.height * 0.45)
		|| pageShellAtPoint(x, rect.top + rect.height * 0.25)
		|| pageShellAtPoint(x, rect.top + rect.height * 0.65);
}

function pageShellAtPoint(x, y) {
	return document.elementFromPoint(x, y)?.closest?.(".page-shell") || null;
}

function setCurrentPage(index) {
	state.pageIndex = index;
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
	shell.style.width = `${width * state.scale}px`;
	shell.style.height = `${height * state.scale}px`;
	const surface = shell.querySelector(".page-surface");
	if (surface) {
		surface.style.width = `${width}px`;
		surface.style.height = `${height}px`;
		surface.style.transform = `scale(${state.scale})`;
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
	} catch {
		container.classList.add("error");
		container.textContent = String(index + 1);
	}
}

function observeThumbnail(button, index, openSeq = state.openSeq) {
	if (state.thumbnailCache.has(index)) {
		return;
	}
	if (index === state.pageIndex || index < 6) {
		renderThumbnail(index, openSeq);
		return;
	}
	const observer = thumbnailObserver();
	if (observer) {
		observer.observe(button);
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
			for (const entry of entries) {
				if (!entry.isIntersecting) {
					continue;
				}
				state.thumbnailObserver.unobserve(entry.target);
				renderThumbnail(Number.parseInt(entry.target.dataset.pageIndex, 10), openSeq);
			}
		}, {
			root: el.pageListPanel,
			rootMargin: "180px 0px",
		});
	}
	return state.thumbnailObserver;
}

async function renderThumbnail(index, openSeq = state.openSeq) {
	if (openSeq !== state.openSeq || !state.doc || state.thumbnailCache.has(index)) {
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
	el.metaTitle.textContent = doc.title || "-";
	el.metaAuthor.textContent = doc.author || "-";
	el.metaVersion.textContent = doc.version || "-";
	el.metaType.textContent = doc.docType || "-";
	el.metaFonts.textContent = String(doc.fontCount || 0);
	el.metaSignatures.textContent = String(doc.signatureCount || 0);
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
		row.className = `signature-row ${signature.integrityValid ? "valid" : "invalid"}`;

		const head = document.createElement("div");
		head.className = "signature-head";

		const name = signatureNameNode(signature);

		const badges = document.createElement("div");
		badges.className = "signature-badges";
		badges.append(fontBadge(signature.integrityValid ? "完整" : "异常", signature.integrityValid ? "valid" : "invalid"));

		head.append(name, badges);
		row.append(head);
		appendSignatureLine(row, "编号", signature.id);
		appendSignatureLine(row, "版本", signature.version);
		appendSignatureLine(row, "章图", signature.sealType);
		appendSignatureLine(row, "签者", signature.signer);
		appendSignatureLine(row, "时间", formatSignatureTime(signature.signatureDateTime));
		appendSignatureLine(row, "机构", signatureAgency(signature));
		appendSignatureCheck(row, "原文", signature.dataHashOK);
		appendSignatureCheck(row, "签名", signature.signedValueOK);
		if (signature.type !== "Sign") {
			appendSignatureCheck(row, "章验", signature.sealOK);
			appendSignatureCheck(row, "一致", signature.sealMatchOK);
		}
		appendSignatureCheck(row, "证书", signature.certOK);
		appendSignaturePolicy(row, "时效", signature.certTimeChecked, signature.certTimeOK);
		appendSignaturePolicy(row, "信任", signature.certTrustChecked, signature.certTrustOK);
		appendSignatureLine(row, "保护", signatureReferenceText(signature), signatureReferenceStatus(signature));
		appendSignatureLine(row, "序号", signature.signSerial);
		appendSignatureLine(row, "算法", signature.signatureMethod);
		appendSignatureLine(row, "散列", signature.digestMethod);
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
	const invalid = signatures.filter((signature) => !signature.integrityValid).length;
	if (invalid) {
		return `${signatures.length} · 异常 ${invalid}`;
	}
	return `通过 ${signatures.length}`;
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
	return `${passed === count ? "通过" : "失败"} ${passed}/${count}`;
}

function signatureReferenceStatus(signature) {
	const count = signature.referenceCount || 0;
	if (!count) {
		return "";
	}
	return signature.referencePassed === count ? "ok" : "fail";
}

function formatSignatureTime(value) {
	const text = String(value || "").trim();
	const digits = text.match(/^(\d{4})(\d{2})(\d{2})(\d{2})(\d{2})(\d{2})$/);
	if (digits) {
		return `${digits[1]}-${digits[2]}-${digits[3]} ${digits[4]}:${digits[5]}:${digits[6]}`;
	}
	return text.replace("T", " ").replace(/(?:Z|[+-]\d{2}:?\d{2})$/, "");
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

function appendSignatureCheck(row, label, ok) {
	appendSignatureLine(row, label, ok ? "通过" : "失败", ok ? "ok" : "fail");
}

function appendSignaturePolicy(row, label, checked, ok) {
	if (!checked) {
		return;
	}
	appendSignatureCheck(row, label, ok);
}

async function focusSignatureStamp(stamp) {
	const pageIndex = (stamp.page || 0) - 1;
	if (!state.doc || pageIndex < 0) {
		return;
	}
	await renderPage(pageIndex, { fit: false, scroll: false });
	await nextFrame();
	highlightSignatureStamp(stamp);
	setStatus(stamp.page ? `已定位签名外观 第 ${stamp.page} 页` : "已定位签名外观");
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

function fitWidth(updateStatus = true) {
	const page = currentPageInfo();
	if (!page) {
		return;
	}
	const space = pageSpace();
	const width = Math.max(1, page.width * MM_TO_PX);
	const height = Math.max(1, page.height * MM_TO_PX);
	let available = Math.max(1, el.viewerPanel.clientWidth - space * 2);
	if (!viewerHasVerticalScrollbar() && height * (available / width) > Math.max(1, el.viewerPanel.clientHeight - space * 2)) {
		available = Math.max(1, available - scrollbarWidth());
	}
	setScale(available / width, updateStatus, "width");
	if (updateStatus) {
		scrollToPage(state.pageIndex);
	}
}

function fitHeight(updateStatus = true) {
	const page = currentPageInfo();
	if (!page) {
		return;
	}
	const space = pageSpace();
	const availableWidth = Math.max(1, el.viewerPanel.clientWidth - space * 2);
	const availableHeight = Math.max(1, el.viewerPanel.clientHeight - space * 2);
	const width = Math.max(1, page.width * MM_TO_PX);
	const height = Math.max(1, page.height * MM_TO_PX);
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
	const scaleChanged = Math.abs(scale - state.scale) > 0.001;
	state.fitMode = fitMode;
	state.scale = scale;
	if (scaleChanged) {
		clearStampHighlights();
		layoutPages();
	}
	updateFitSpace();
	if (scaleChanged) {
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
		el.pageFrame.style.removeProperty("--fit-space");
		el.pageFrame.style.removeProperty("--fit-gap");
		return;
	}
	const shell = pageShell(state.pageIndex);
	const height = shell ? shell.getBoundingClientRect().height : Math.max(1, page.height * MM_TO_PX * state.scale);
	const base = pageSpace();
	const space = Math.max(base, (el.viewerPanel.clientHeight - height) / 2);
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
	el.pageInput.value = String(hasDoc ? state.pageIndex + 1 : 1);
	el.zoomOutButton.disabled = !hasDoc || state.scale <= 0.2;
	el.zoomInButton.disabled = !hasDoc || state.scale >= 4;
	el.fitButton.disabled = !hasDoc;
	el.fitHeightButton.disabled = !hasDoc;
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
	el.annotationButton.title = state.renderAnnotations ? "关闭注解渲染" : "开启注解渲染";
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
	const result = JSON.parse(payload);
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
	if (!busy) {
		el.progressPanel.hidden = true;
		return;
	}
	el.progressPanel.hidden = false;
	if (status) {
		setStatus(status);
	}
	setProgress(text, percent);
}

function setProgress(text = "", percent = 0, status = "") {
	if (text) {
		el.progressLabel.textContent = text;
	}
	if (status) {
		setStatus(status);
	}
	const value = Math.max(0, Math.min(100, percent));
	el.progressBar.style.width = `${value}%`;
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

function base64ToBytes(base64) {
	const binary = atob(base64 || "");
	const bytes = new Uint8Array(binary.length);
	for (let i = 0; i < binary.length; i += 1) {
		bytes[i] = binary.charCodeAt(i);
	}
	return bytes;
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
