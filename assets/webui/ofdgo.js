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
const FIT_WIDTH_MARGIN = 72;
const FIT_HEIGHT_MARGIN = 86;
const COMPACT_LAYOUT = window.matchMedia("(max-width: 900px)");
const STATUS = {
	ready: "选择 OFD 文件开始阅读",
	opening: "正在打开 OFD",
	engine: "正在准备渲染引擎",
	recovering: "正在恢复渲染引擎",
	fonts: "正在匹配字体",
	exporting: "正在导出 PDF",
};
const WASM_CALLBACKS = [
	"ofdgoOpen",
	"ofdgoRenderPage",
	"ofdgoExportPDF",
	"ofdgoFontCandidates",
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
	doc: null,
	pageIndex: 0,
	scale: 1,
	fitMode: "width",
	pageCache: new Map(),
	pageInFlight: new Set(),
	pageObserver: null,
	scrollFrame: 0,
	thumbnailCache: new Map(),
	thumbnailInFlight: new Set(),
	thumbnailObserver: null,
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
	metaTitle: document.querySelector("#metaTitle"),
	metaAuthor: document.querySelector("#metaAuthor"),
	metaVersion: document.querySelector("#metaVersion"),
	metaType: document.querySelector("#metaType"),
	metaFonts: document.querySelector("#metaFonts"),
	docFontList: document.querySelector("#docFontList"),
	docFontSummary: document.querySelector("#docFontSummary"),
	availableFontSummary: document.querySelector("#availableFontSummary"),
	fontList: document.querySelector("#fontList"),
	statusText: document.querySelector("#statusText"),
};

el.ofdButton.addEventListener("click", () => el.ofdInput.click());
el.togglePagesButton.addEventListener("click", () => toggleSidebar("pages"));
el.toggleMetaButton.addEventListener("click", () => toggleSidebar("meta"));
el.fontAddButton.addEventListener("click", () => el.fontInput.click());
el.localFontButton.addEventListener("click", loadLocalFonts);
el.ofdInput.addEventListener("change", openSelectedOFD);
el.fontInput.addEventListener("change", openSelectedFonts);
el.prevButton.addEventListener("click", () => renderPage(state.pageIndex - 1));
el.nextButton.addEventListener("click", () => renderPage(state.pageIndex + 1));
el.zoomOutButton.addEventListener("click", () => setScale(state.scale - 0.1));
el.zoomInButton.addEventListener("click", () => setScale(state.scale + 0.1));
el.fitButton.addEventListener("click", fitWidth);
el.fitHeightButton.addEventListener("click", fitHeight);
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

updateSidebarState();
boot();

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
	setBusy(true, "准备渲染引擎", 8, STATUS.engine);
	try {
		await ensureWASM();
		setProgress("准备就绪", 70);
		setEmpty("选择 OFD 文件");
		updateFontSummary();
		renderFontList();
		updateControls();
		updateLocalFontButton();
		setStatus(STATUS.ready);
		setBusy(false);
	} catch (err) {
		setStatus("渲染引擎加载失败");
		setEmpty(String(err.message || err));
		setBusy(false);
		return;
	}
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
		setProgress("下载渲染引擎", 16);
		const response = await fetch("./ofdgo.wasm");
		try {
			setProgress("编译渲染引擎", 35);
			wasmModule = await WebAssembly.compileStreaming(response.clone());
		} catch {
			const bytes = await response.arrayBuffer();
			setProgress("编译渲染引擎", 45);
			wasmModule = await WebAssembly.compile(bytes);
		}
	}
	setProgress("启动渲染引擎", 58);
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
	setBusy(true, "恢复渲染引擎", 18, STATUS.recovering);
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
	state.wasmRecoveries = 0;
	setBusy(true, "读取 OFD 文件", 10, STATUS.opening);
	try {
		state.fileName = file.name || "ofdgo.ofd";
		state.ofdBytes = new Uint8Array(await file.arrayBuffer());
		await openDocument();
	} catch (err) {
		showError(err, true);
		setBusy(false);
	}
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
	setBusy(true, state.doc ? "匹配系统字体" : "请求系统字体授权", 12, state.doc ? STATUS.fonts : "正在请求系统字体授权");
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

async function queryLocalFonts() {
	const available = await window.queryLocalFonts();
	state.systemFontCatalog = available;
	return available;
}

async function loadDocumentLocalFonts(available, openSeq = state.openSeq) {
	const docFonts = state.doc?.fonts || [];
	const docNames = externalDocumentFontNames();
	if (!docFonts.length) {
		state.localFonts = [];
		setStatus("当前 OFD 未声明字体");
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
		setProgress(`读取系统字体 ${i + 1}/${selected.length}`, 20 + Math.round(i / Math.max(1, selected.length) * 60));
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
	if (el.availableFontSummary) {
		el.availableFontSummary.textContent = total ? `${enabled}/${total}` : "0";
	}
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
	for (const name of fontCandidateNames(names)) {
		const key = normalizeFontName(name);
		if (key) {
			keys.add(key);
		}
	}
	return keys;
}

function fontCandidateNames(names) {
	const source = (Array.isArray(names) ? names : [])
		.map((name) => String(name || "").trim())
		.filter((name) => name !== "");
	if (!source.length) {
		return [];
	}
	if (state.ready && !state.wasmExited && typeof globalThis.ofdgoFontCandidates === "function") {
		try {
			const candidates = callWASM("ofdgoFontCandidates", source);
			if (Array.isArray(candidates)) {
				return candidates;
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

function removeFont(id) {
	state.localFonts = state.localFonts.filter((font) => font.id !== id);
	state.userFonts = state.userFonts.filter((font) => font.id !== id);
}

function renderFontList() {
	if (!el.fontList) {
		return;
	}
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
	if (!el.localFontButton) {
		return;
	}
	const supported = canReadLocalFonts();
	el.localFontButton.disabled = !supported;
	el.localFontButton.title = supported ? "授权读取浏览器可访问的系统字体" : "当前浏览器不支持读取系统字体";
}

function canReadLocalFonts() {
	return typeof window.queryLocalFonts === "function";
}

async function openDocument(options = {}) {
	if (!state.ofdBytes) {
		return;
	}
	if (!state.ready || state.wasmExited) {
		await ensureWASM();
	}
	const openSeq = options.openSeq || (state.openSeq += 1);
	const autoFonts = !options.skipAutoFonts && state.systemFontCatalog.length > 0;
	if (autoFonts) {
		state.localFonts = [];
		updateFontSummary();
		renderFontList();
	}
	setBusy(true, "打开 OFD", 20, STATUS.opening);
	try {
		setProgress("解析 OFD", 52);
		await nextFrame();
		if (openSeq !== state.openSeq) {
			return;
		}
		const doc = callWASM("ofdgoOpen", state.ofdBytes, autoFonts ? uploadedFonts() : allFonts());
		if (openSeq !== state.openSeq) {
			return;
		}
		const pageCount = doc.pageCount || 0;
		const pageIndex = Math.min(Math.max(options.pageIndex || 0, 0), Math.max(pageCount - 1, 0));
		state.doc = doc;
		state.pageIndex = pageIndex;
		state.scale = 1;
		state.fitMode = options.fitMode || "width";
		if (!options.skipAutoFonts && state.systemFontCatalog.length > 0) {
			setProgress("匹配系统字体", 62, STATUS.fonts);
			if (await loadDocumentLocalFonts(state.systemFontCatalog, openSeq)) {
				if (openSeq !== state.openSeq) {
					return;
				}
				await openDocument({
					pageIndex,
					fitMode: state.fitMode,
					skipAutoFonts: true,
					openSeq,
				});
				return;
			}
		}
		if (openSeq !== state.openSeq) {
			return;
		}
		resetPageFlow();
		renderPageList();
		renderMeta();
		renderPageFlow();
		await nextFrame();
		applyFit(false);
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
		setProgress("渲染页面", 76);
	} else {
		setBusy(true, "渲染页面", 35, `正在渲染第 ${index + 1} 页`);
	}
	try {
		setCurrentPage(index);
		applyFit(false);
		if (options.scroll !== false) {
			scrollToPage(index);
		}
		await renderFlowPage(index, { throwError: true, openSeq });
		if (openSeq !== state.openSeq) {
			return;
		}
		if (options.scroll !== false) {
			scrollToPage(index);
		}
		queueNearbyPages(index, openSeq);
		updatePageListCurrent();
		updateControls();
		setStatus(pageStatus(index, pageCount));
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
	setBusy(true, "生成 PDF", 18, STATUS.exporting);
	try {
		await nextFrame();
		if (openSeq !== state.openSeq) {
			return;
		}
		setProgress("导出 PDF", 45);
		const result = callWASM("ofdgoExportPDF");
		if (openSeq !== state.openSeq) {
			return;
		}
		const bytes = base64ToBytes(result.base64);
		const blob = new Blob([bytes], { type: "application/pdf" });
		const link = document.createElement("a");
		const url = URL.createObjectURL(blob);
		link.href = url;
		link.download = pdfFileName();
		document.body.append(link);
		link.click();
		link.remove();
		window.setTimeout(() => URL.revokeObjectURL(url), 1000);
		setStatus(`PDF 已导出 ${formatBytes(result.size || bytes.length)}`);
	} catch (err) {
		if (openSeq === state.openSeq) {
			showError(err, false);
		}
	} finally {
		if (openSeq === state.openSeq) {
			setBusy(false);
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
		renderFlowPage(index, { openSeq });
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
				renderFlowPage(index, { openSeq });
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
	if (state.pageCache.has(index)) {
		return state.pageCache.get(index);
	}
	const key = `${openSeq}:${index}`;
	if (state.pageInFlight.has(key)) {
		return null;
	}
	state.pageInFlight.add(key);
	try {
		await nextFrame();
		if (openSeq !== state.openSeq) {
			return null;
		}
		const page = callWASM("ofdgoRenderPage", index);
		if (openSeq !== state.openSeq) {
			return null;
		}
		state.pageCache.set(index, page);
		cacheThumbnail(index, page.svg);
		mountPageSVG(index, page, openSeq);
		updateThumbnail(index, openSeq);
		return page;
	} catch (err) {
		if (openSeq === state.openSeq) {
			markFlowPageError(index, err);
		}
		if (options.throwError && openSeq === state.openSeq) {
			throw err;
		}
		return null;
	} finally {
		state.pageInFlight.delete(key);
	}
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
		renderFlowPage(i, { openSeq });
	}
}

function pageShell(index) {
	return el.svgHost.querySelector(`.page-shell[data-page-index="${index}"]`);
}

function scrollToPage(index) {
	const shell = pageShell(index);
	if (shell) {
		shell.scrollIntoView({ block: "start", inline: "nearest" });
	}
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
	const shells = Array.from(el.svgHost.querySelectorAll(".page-shell"));
	if (!shells.length) {
		return;
	}
	const viewerRect = el.viewerPanel.getBoundingClientRect();
	const targetY = viewerRect.top + Math.min(viewerRect.height * 0.45, 280);
	let nextIndex = state.pageIndex;
	let best = Number.POSITIVE_INFINITY;
	for (const shell of shells) {
		const rect = shell.getBoundingClientRect();
		const distance = Math.abs(rect.top - targetY);
		if (distance < best) {
			best = distance;
			nextIndex = Number.parseInt(shell.dataset.pageIndex, 10);
		}
	}
	if (Number.isFinite(nextIndex) && nextIndex !== state.pageIndex) {
		setCurrentPage(nextIndex);
		queueNearbyPages(nextIndex);
	}
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
	for (const item of el.pageList.querySelectorAll(".page-list-item")) {
		setPageItemCurrent(item, Number.parseInt(item.dataset.pageIndex, 10) === state.pageIndex);
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
		await nextFrame();
		if (openSeq !== state.openSeq) {
			return;
		}
		const page = callWASM("ofdgoRenderPage", index);
		if (openSeq !== state.openSeq) {
			return;
		}
		cacheThumbnail(index, page.svg);
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
	el.pageTotal.textContent = String(doc.pageCount || 0);
	renderDocumentFonts();
	renderFontList();
	updateLocalFontButton();
}

function renderDocumentFonts() {
	if (!el.docFontList) {
		return;
	}
	const fonts = state.doc?.fonts || [];
	el.docFontList.replaceChildren();
	if (el.docFontSummary) {
		el.docFontSummary.textContent = fontSummary(fonts);
	}
	if (!fonts.length) {
		const empty = document.createElement("div");
		empty.className = "font-empty";
		empty.textContent = "未声明字体";
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
		const detail = document.createElement("div");
		detail.className = "doc-font-detail";
		detail.textContent = fontDetail(font);

		head.append(name, badges);
		row.append(head, detail);
		fragment.append(row);
	}
	el.docFontList.append(fragment);
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
		return "已匹配";
	case "fallback":
		return "回退";
	default:
		return "缺失";
	}
}

function fontDetail(font) {
	const parts = [];
	if (font.familyName && font.familyName !== font.fontName) {
		parts.push(`族 ${font.familyName}`);
	}
	if (font.charset) {
		parts.push(`字符集 ${font.charset}`);
	}
	if (font.matched) {
		parts.push(font.embedded || font.status === "embedded" ? `内嵌 ${font.matched}` : `匹配 ${font.matched}`);
	}
	if (font.detail) {
		parts.push(font.detail);
	}
	return parts.join(" · ") || "-";
}

function fitWidth(updateStatus = true) {
	const page = currentPageInfo();
	if (!page) {
		return;
	}
	state.fitMode = "width";
	const available = Math.max(280, el.pageFrame.clientWidth - FIT_WIDTH_MARGIN);
	const width = Math.max(1, page.width * MM_TO_PX);
	setScale(available / width, updateStatus, "width");
}

function fitHeight(updateStatus = true) {
	const page = currentPageInfo();
	if (!page) {
		return;
	}
	state.fitMode = "height";
	const available = Math.max(220, el.viewerPanel.clientHeight - FIT_HEIGHT_MARGIN);
	const height = Math.max(1, page.height * MM_TO_PX);
	setScale(available / height, updateStatus, "height");
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
	state.fitMode = fitMode;
	state.scale = Math.min(4, Math.max(0.2, nextScale));
	layoutPages();
	el.zoomLabel.textContent = `${Math.round(state.scale * 100)}%`;
	if (updateStatus && state.doc) {
		setStatus(`第 ${state.pageIndex + 1} / ${state.doc.pageCount} 页`);
	}
	updateControls();
}

function currentPageInfo() {
	return state.doc?.pages?.[state.pageIndex] || null;
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
	el.exportButton.disabled = !hasDoc;
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

function pdfFileName() {
	const base = (state.fileName || state.doc?.title || "ofdgo").replace(/\.[^.]+$/, "");
	const safe = base.replace(/[\\/:*?"<>|]+/g, "_").trim() || "ofdgo";
	return `${safe}.pdf`;
}

function formatBytes(size) {
	if (!Number.isFinite(size) || size <= 0) {
		return "PDF";
	}
	if (size < 1024 * 1024) {
		return `${Math.round(size / 1024)} KB`;
	}
	return `${(size / 1024 / 1024).toFixed(1)} MB`;
}

function nextFrame() {
	return new Promise((resolve) => requestAnimationFrame(resolve));
}
