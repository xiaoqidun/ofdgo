const MM_TO_PX = 96 / 25.4;
const FONT_DATABASE = "ofdgo";
const fontChannel = typeof BroadcastChannel === "function" ? new BroadcastChannel(FONT_DATABASE) : null;
const COMPACT_LAYOUT = window.matchMedia("(max-width: 900px)");
const DEFAULT_IMAGE_DPI = 300;
const PAGE_CACHE_LIMIT = 16;
const PAGE_CACHE_BYTES = 32 * 1024 * 1024;
const STATUS = {
	ready: "选择 OFD 文件",
	opening: "正在打开文档",
	engine: "正在准备引擎",
	recovering: "正在恢复引擎",
	fonts: "正在匹配字体",
	exporting: "正在导出文档",
	pageExporting: "正在导出单页",
};
const wasmRequests = new Map();

let wasmPromise = null;
let wasmWorker = null;
let wasmRequestID = 0;
let wasmRecoveryTimer = 0;
let fontDatabase = null;
let textMeasure = null;

const state = {
	ready: false,
	wasmExited: false,
	wasmSeq: 0,
	wasmRecovering: false,
	wasmRecoveries: 0,
	exporting: false,
	exportRequestID: 0,
	ofdBytes: null,
	fileName: "ofdgo.ofd",
	openSeq: 0,
	pageSeq: 0,
	fontSeq: 0,
	searchSeq: 0,
	searchQuery: "",
	searchMatches: [],
	searchIndex: -1,
	navigationScroll: new Map(),
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
	rotation: 0,
	panMode: false,
	pan: null,
	fitMode: "width",
	continuous: false,
	renderAnnotations: true,
	pageCache: new Map(),
	svgFonts: new Map(),
	svgImages: new Map(),
	selectedPages: new Set(),
	documentSelection: null,
	pageInFlight: new Map(),
	pageRenderQueue: [],
	pageRenderRunning: false,
	pageObserver: null,
	visiblePages: new Set(),
	scrollFrame: 0,
	thumbnailInFlight: new Set(),
	thumbnailObserver: null,
	visibleThumbnails: new Set(),
	exportFormats: [],
	exportPages: null,
	exportBackdrop: false,
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
	rotateButton: document.querySelector("#rotateButton"),
	panButton: document.querySelector("#panButton"),
	continuousButton: document.querySelector("#continuousButton"),
	annotationButton: document.querySelector("#annotationButton"),
	imageDPI: document.querySelector("#imageDPI"),
	exportFormat: document.querySelector("#exportFormat"),
	exportPageButton: document.querySelector("#exportPageButton"),
	exportButton: document.querySelector("#exportButton"),
	exportPanel: document.querySelector("#exportPanel"),
	exportForm: document.querySelector("#exportForm"),
	exportAll: document.querySelector("#exportAll"),
	exportSpecified: document.querySelector("#exportSpecified"),
	exportRangeRow: document.querySelector("#exportRangeRow"),
	exportRange: document.querySelector("#exportRange"),
	exportRangeStatus: document.querySelector("#exportRangeStatus"),
	exportCancel: document.querySelector("#exportCancel"),
	exportSubmit: document.querySelector("#exportSubmit"),
	emptyState: document.querySelector("#emptyState"),
	progressPanel: document.querySelector("#progressPanel"),
	progressLabel: document.querySelector("#progressLabel"),
	cancelExportButton: document.querySelector("#cancelExportButton"),
	progressBar: document.querySelector("#progressBar"),
	pageFrame: document.querySelector("#pageFrame"),
	viewerPanel: document.querySelector(".viewer-panel"),
	svgHost: document.querySelector("#svgHost"),
	pageListPanel: document.querySelector(".page-list-panel"),
	pageListTitle: document.querySelector("#pageListTitle"),
	navigationTabs: document.querySelector("#navigationTabs"),
	pagesTab: document.querySelector("#pagesTab"),
	outlinesTab: document.querySelector("#outlinesTab"),
	pageList: document.querySelector("#pageList"),
	outlineList: document.querySelector("#outlineList"),
	searchTab: document.querySelector("#searchTab"),
	searchPanel: document.querySelector("#searchPanel"),
	searchForm: document.querySelector("#searchForm"),
	searchInput: document.querySelector("#searchInput"),
	searchStatus: document.querySelector("#searchStatus"),
	searchCount: document.querySelector("#searchCount"),
	searchPrev: document.querySelector("#searchPrev"),
	searchNext: document.querySelector("#searchNext"),
	searchResults: document.querySelector("#searchResults"),
	metaPanel: document.querySelector(".meta-panel"),
	appPanel: document.querySelector("#appPanel"),
	offlineStatus: document.querySelector("#offlineStatus"),
	refreshAppButton: document.querySelector("#refreshAppButton"),
	metaFile: document.querySelector("#metaFile"),
	metaTitle: document.querySelector("#metaTitle"),
	metaSubject: document.querySelector("#metaSubject"),
	metaAuthor: document.querySelector("#metaAuthor"),
	metaCreationDate: document.querySelector("#metaCreationDate"),
	metaModDate: document.querySelector("#metaModDate"),
	metaType: document.querySelector("#metaType"),
	metaVersion: document.querySelector("#metaVersion"),
	metaSignatures: document.querySelector("#metaSignatures"),
	metaFonts: document.querySelector("#metaFonts"),
	attachmentPanel: document.querySelector("#attachmentPanel"),
	attachmentList: document.querySelector("#attachmentList"),
	signaturePanel: document.querySelector("#signaturePanel"),
	signatureSummary: document.querySelector("#signatureSummary"),
	signatureList: document.querySelector("#signatureList"),
	annotationPanel: document.querySelector("#annotationPanel"),
	annotationList: document.querySelector("#annotationList"),
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
el.pagesTab.addEventListener("click", () => showNavigation(el.pagesTab));
el.outlinesTab.addEventListener("click", () => showNavigation(el.outlinesTab));
el.searchTab.addEventListener("click", focusSearch);
el.navigationTabs.addEventListener("keydown", (event) => {
	if (event.isComposing || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) {
		return;
	}
	if (["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) {
		event.preventDefault();
		const tabs = [el.pagesTab, el.outlinesTab, el.searchTab].filter((tab) => !tab.hidden);
		const current = tabs.indexOf(event.target);
		const index = event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1
			: (current + (event.key === "ArrowRight" ? 1 : -1) + tabs.length) % tabs.length;
		showNavigation(tabs[index]);
		tabs[index].focus({ preventScroll: true });
	}
});
el.searchForm.addEventListener("submit", (event) => {
	event.preventDefault();
	searchDocument();
});
el.searchInput.addEventListener("input", () => resetSearch(false));
el.searchPrev.addEventListener("click", () => selectSearchMatch(Math.max(0, state.searchIndex) - 1));
el.searchNext.addEventListener("click", () => selectSearchMatch(state.searchIndex + 1));
document.addEventListener("keydown", handleKeyDown);
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
el.rotateButton.addEventListener("click", rotatePages);
el.panButton.addEventListener("click", togglePan);
el.continuousButton.addEventListener("click", toggleContinuous);
el.annotationButton.addEventListener("click", toggleAnnotations);
document.addEventListener("copy", copySelection);
document.addEventListener("selectionchange", syncSelection);
el.exportFormat.addEventListener("change", () => updateDPIControl());
el.exportPageButton.addEventListener("click", () => exportFile(false));
el.exportButton.addEventListener("click", openExportPanel);
el.exportCancel.addEventListener("click", () => el.exportPanel.close());
el.cancelExportButton.addEventListener("click", cancelExport);
el.exportPanel.addEventListener("pointerdown", (event) => {
	state.exportBackdrop = event.target === el.exportPanel;
});
el.exportPanel.addEventListener("click", (event) => {
	if (state.exportBackdrop && event.target === el.exportPanel) {
		el.exportPanel.close();
	}
});
el.exportAll.addEventListener("change", updateExportRange);
el.exportSpecified.addEventListener("change", () => {
	updateExportRange();
	el.exportRange.focus();
});
el.exportRange.addEventListener("input", updateExportRange);
el.exportForm.addEventListener("submit", (event) => {
	event.preventDefault();
	if (!el.exportSubmit.disabled) {
		return exportFile(true, state.exportPages);
	}
});
el.refreshAppButton.addEventListener("click", refreshApplication);
el.pageInput.addEventListener("change", () => {
	const page = Number.parseInt(el.pageInput.value, 10);
	if (Number.isFinite(page)) {
		renderPage(page - 1);
	}
});
window.addEventListener("resize", resizeViewer);
window.addEventListener("blur", () => endPan());
COMPACT_LAYOUT.addEventListener("change", syncLayoutMode);
el.viewerPanel.addEventListener("scroll", () => {
	schedulePageSync();
});
el.viewerPanel.addEventListener("pointerdown", startPan);
el.viewerPanel.addEventListener("pointermove", movePan);
el.viewerPanel.addEventListener("pointerup", endPan);
el.viewerPanel.addEventListener("pointercancel", endPan);
el.viewerPanel.addEventListener("lostpointercapture", endPan);
el.viewerPanel.addEventListener("click", () => {
	if (COMPACT_LAYOUT.matches && (state.showPages || state.showMeta)) {
		state.showPages = false;
		state.showMeta = false;
		updateSidebarState();
	}
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

function handleKeyDown(event) {
	if (event.defaultPrevented || event.isComposing || event.altKey) {
		return;
	}
	const key = event.key;
	const target = event.target;
	if ((event.ctrlKey || event.metaKey) && !event.shiftKey && key.toLowerCase() === "a" && state.doc
		&& el.viewerPanel.contains(target) && !target.closest("input, textarea, select, [contenteditable]")) {
		event.preventDefault();
		if (!document.body.hasAttribute("aria-busy") && !el.exportPanel.open) {
			selectDocumentText();
		}
		return;
	}
	if (document.body.hasAttribute("aria-busy") || el.exportPanel.open) {
		return;
	}
	if (event.ctrlKey || event.metaKey) {
		if (!event.shiftKey && key.toLowerCase() === "o") {
			event.preventDefault();
			openOFDFile();
		} else if (!event.shiftKey && key.toLowerCase() === "f" && state.doc) {
			event.preventDefault();
			focusSearch();
		}
		return;
	}
	if (!state.doc) {
		return;
	}
	if (key === "Escape" && !event.shiftKey && state.documentSelection && el.viewerPanel.contains(target)
		&& !target.closest("input, textarea, select, [contenteditable]")) {
		event.preventDefault();
		document.getSelection().removeAllRanges();
		syncSelection();
		return;
	}
	if (key === "F3") {
		event.preventDefault();
		if (state.searchMatches.length) {
			selectSearchMatch(event.shiftKey ? Math.max(0, state.searchIndex) - 1 : state.searchIndex + 1);
		} else {
			focusSearch();
		}
		return;
	}
	if (key === "Escape" && !event.shiftKey && !el.searchPanel.hidden && (el.pageListPanel.contains(target) || el.viewerPanel.contains(target))) {
		event.preventDefault();
		resetSearch();
		showNavigation(el.pagesTab);
		if (COMPACT_LAYOUT.matches && state.showPages) {
			state.showPages = false;
			updateSidebarState();
		}
		el.viewerPanel.focus({ preventScroll: true });
		return;
	}
	if (key === "Enter" && target === el.pageInput && !event.shiftKey) {
		event.preventDefault();
		el.viewerPanel.focus({ preventScroll: true });
		return;
	}
	if (key === "Enter" && target === el.searchInput && event.shiftKey && state.searchMatches.length) {
		event.preventDefault();
		selectSearchMatch(Math.max(0, state.searchIndex) - 1);
		return;
	}
	if (!el.viewerPanel.contains(target) || target.closest("input, textarea, select, button, a, [contenteditable]")) {
		return;
	}
	let button;
	if (key === "+" || (key === "=" && !event.shiftKey)) {
		button = el.zoomInButton;
	} else if (key === "-" && !event.shiftKey) {
		button = el.zoomOutButton;
	} else if (!event.shiftKey && (key === "ArrowLeft" || key === "ArrowRight")) {
		if (document.getSelection().isCollapsed && el.viewerPanel.scrollWidth <= el.viewerPanel.clientWidth) {
			button = key === "ArrowLeft" ? el.prevButton : el.nextButton;
		}
	}
	if (button) {
		event.preventDefault();
		button.click();
	}
}

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
		if (COMPACT_LAYOUT.matches) {
			state.showMeta = false;
		}
	} else if (side === "meta") {
		state.showMeta = !state.showMeta;
		if (COMPACT_LAYOUT.matches) {
			state.showPages = false;
		}
	}
	updateSidebarState();
	if (!COMPACT_LAYOUT.matches) {
		resizeViewer();
	}
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
		await loadExportFormats();
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
		setStatus("引擎加载失败");
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
		watchApplicationUpdates(result.checksum);
	} catch {
		el.offlineStatus.textContent = "未就绪";
	}
	el.refreshAppButton.disabled = false;
	return true;
}

function watchApplicationUpdates(checksum) {
	let checking = false;
	async function check() {
		if (checking || document.hidden || !navigator.onLine) {
			return;
		}
		checking = true;
		try {
			const response = await fetch("./", { method: "HEAD", cache: "no-store", signal: AbortSignal.timeout(10000) });
			const latest = response.headers.get("X-OFDGo-Checksum");
			if (response.ok && !response.redirected && latest) {
				el.offlineStatus.textContent = latest === checksum ? "可离线" : "可更新";
			}
		} catch {
		} finally {
			checking = false;
		}
	}
	window.addEventListener("online", check);
	document.addEventListener("visibilitychange", check);
	check();
}

function waitForWorker(worker, target) {
	return new Promise((resolve, reject) => {
		function changed() {
			if (worker.state === target || worker.state === "activated") {
				worker.removeEventListener("statechange", changed);
				resolve();
			} else if (worker.state === "redundant") {
				worker.removeEventListener("statechange", changed);
				reject(new Error("离线启动失败"));
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
				reject(new Error("离线服务中断"));
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
	const message = temporaryFonts ? "刷新后需重新打开文件\n未保存字体需重新添加" : "刷新后需重新打开文件";
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

async function loadExportFormats() {
	const formats = await callWASM("ofdgoExportFormats") || [];
	state.exportFormats = formats;
	el.exportFormat.replaceChildren();
	for (const format of formats) {
		const option = document.createElement("option");
		option.value = format.value;
		option.textContent = format.label;
		el.exportFormat.append(option);
	}
	if (formats.length) {
		el.exportFormat.value = formats[0].value;
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
	const wasmSeq = state.wasmSeq + 1;
	state.wasmSeq = wasmSeq;
	state.ready = false;
	state.wasmExited = false;
	await new Promise((resolve, reject) => {
		const worker = new Worker("./ofdgo.wasm.js");
		wasmWorker = worker;
		const fail = (err) => {
			markWASMExited(wasmSeq, err);
			reject(err);
		};
		worker.onerror = (event) => fail(new Error(event.message || "引擎加载失败"));
		worker.onmessageerror = () => fail(new Error("引擎通信失败"));
		worker.onmessage = ({ data }) => {
			if (worker !== wasmWorker) {
				return;
			}
			if (data.type === "progress") {
				setProgress(data.text, data.percent);
			} else if (data.type === "export") {
				if (wasmRequests.get(data.id)?.openSeq === state.openSeq && state.exporting) {
					if (data.stage === "save") {
						el.cancelExportButton.disabled = true;
						setProgress("正在保存", null);
					} else if (!el.cancelExportButton.disabled) {
						if (data.completed === data.total) {
							setProgress("正在收尾", null);
						} else {
							setProgress(`正在导出 ${data.completed} / ${data.total} 页`, data.completed / data.total * 100);
						}
					}
				}
			} else if (data.type === "ready") {
				state.ready = true;
				resolve();
			} else if (data.type === "exit") {
				fail(new Error(data.error));
			} else {
				const request = wasmRequests.get(data.id);
				wasmRequests.delete(data.id);
				if (data.ok) {
					request.resolve(data.data);
				} else {
					const err = new Error(data.error);
					if (data.canceled) {
						err.name = "AbortError";
					}
					request.reject(err);
				}
			}
		};
	});
}

function markWASMExited(wasmSeq = state.wasmSeq, err) {
	if (wasmSeq !== state.wasmSeq) {
		return;
	}
	state.ready = false;
	state.wasmExited = true;
	wasmWorker?.terminate();
	wasmWorker = null;
	for (const request of wasmRequests.values()) {
		request.reject(err || new Error("引擎运行中断"));
	}
	wasmRequests.clear();
	if (err) {
		setStatus("引擎运行中断");
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
	const openSeq = ++state.openSeq;
	setBusy(true, "正在恢复引擎", 18, STATUS.recovering);
	try {
		await openDocument({
			pageIndex: state.pageIndex,
			fitMode: state.fitMode || "width",
			scale: state.scale,
			skipAutoFonts: true,
			openSeq,
		});
	} catch (err) {
		if (openSeq === state.openSeq) {
			showError(err, !state.doc);
		}
	} finally {
		state.wasmRecovering = false;
		if (openSeq === state.openSeq) {
			setBusy(false);
		}
	}
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
	const openSeq = ++state.openSeq;
	setBusy(true, "正在读取文档", 10, STATUS.opening);
	try {
		const bytes = new Uint8Array(await file.arrayBuffer());
		if (openSeq !== state.openSeq) {
			return;
		}
		state.ofdBytes = bytes;
		state.fileName = file.name || "ofdgo.ofd";
		await openDocument({ pageIndex: 0, resetScroll: true, openSeq });
	} catch (err) {
		if (openSeq === state.openSeq) {
			showError(err, true);
			setBusy(false);
		}
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
			setProgress(`正在读取字体 ${i + 1} / ${files.length}`, 10 + Math.round(i / files.length * 60));
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
		setStatus("无法读取系统字体");
		return;
	}
	setBusy(true, state.doc ? "正在匹配字体" : "正在请求授权", 12, state.doc ? STATUS.fonts : "正在请求授权");
	try {
		await nextFrame();
		const available = await queryLocalFonts();
		if (!state.doc) {
			setStatus(available.length ? `字体授权完成 ${available.length} 个` : "暂无系统字体");
			return;
		}
		if (await loadDocumentLocalFonts(available)) {
			await applyFontChange({ skipAutoFonts: true });
		}
	} catch (err) {
		if (err && err.name === "NotAllowedError") {
			setStatus("字体尚未授权");
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
		setStatus(available.length ? `字体授权完成 ${available.length} 个` : "暂无系统字体");
	} catch (err) {
		if (err && err.name === "NotAllowedError") {
			setStatus("字体尚未授权");
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
	if (!state.doc?.fonts?.some((font) => !font.embedded) || !canReadLocalFonts() || state.systemFontPermission === "denied") {
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
			setStatus("字体尚未授权");
			return false;
		}
		setStatus(String(err.message || err));
		return false;
	}
}

async function loadDocumentLocalFonts(available, openSeq = state.openSeq) {
	const docFonts = state.doc?.fonts || [];
	if (!docFonts.length) {
		state.localFonts = [];
		setStatus("暂无文档字体");
		updateFontSummary();
		renderFontList();
		return false;
	}
	if (docFonts.every((font) => font.embedded)) {
		state.localFonts = [];
		setStatus("字体均为内嵌");
		updateFontSummary();
		renderFontList();
		return false;
	}
	const selected = await selectLocalFonts(available);
	if (openSeq !== state.openSeq) {
		return false;
	}
	const emptyStatus = available.length === 0 ? "暂无系统字体" : "暂无匹配字体";
	const fonts = [];
	for (let i = 0; i < selected.length; i += 1) {
		setProgress(`正在读取字体 ${i + 1} / ${selected.length}`, 20 + Math.round(i / selected.length * 60));
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
	setStatus(fonts.length ? `字体加载完成 ${fonts.length} 个` : emptyStatus);
	updateFontSummary();
	renderFontList();
	return fonts.length > 0;
}

async function selectLocalFonts(fonts) {
	const available = new Map();
	for (const font of fonts) {
		const names = [
			localFontName(font),
			font.postscriptName && `${font.postscriptName}.ttf`,
			font.family && `${[font.family, font.style].filter(Boolean).join(" ")}.ttf`,
		];
		for (const name of names) {
			if (name && !available.has(name)) {
				available.set(name, font);
			}
		}
	}
	const matched = await callWASM("ofdgoMatchFontFiles", [...available.keys()]);
	return [...new Set(matched.map((name) => available.get(name)))];
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

function localFontName(font) {
	const name = font.fullName || font.family || font.postscriptName || "local-font";
	return `${name}.ttf`;
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
	if (document.body.hasAttribute("aria-busy")) {
		return;
	}
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
	el.localFontButton.title = supported ? "读取系统字体" : "无法读取系统字体";
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
	const openSeq = options.openSeq || (state.openSeq += 1);
	if (!state.ready || state.wasmExited) {
		await ensureWASM();
	}
	if (openSeq !== state.openSeq) {
		return;
	}
	const resetLocalFonts = !options.skipAutoFonts;
	resetSearch();
	if (resetLocalFonts) {
		state.localFonts = [];
		updateFontSummary();
		renderFontList();
	}
	setBusy(true, "正在打开文档", 20, STATUS.opening);
	try {
		setProgress("正在解析文档", 52);
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
			? await callWASM("ofdgoConfigure", fonts, state.renderAnnotations)
			: await callWASM("ofdgoOpen", state.ofdBytes, fonts, state.renderAnnotations);
		if (openSeq !== state.openSeq) {
			return;
		}
		const pageCount = doc.pageCount || 0;
		const pageIndex = Math.min(Math.max(options.pageIndex || 0, 0), Math.max(pageCount - 1, 0));
		state.doc = doc;
		state.pageIndex = pageIndex;
		state.scale = options.scale || 1;
		if (!options.fitMode) {
			state.rotation = 0;
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
		if (options.resetScroll || el.outlineList.childElementCount === 0) {
			renderOutlines();
		}
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
		const page = await renderPage(pageIndex, { keepBusy: true, scroll: false, openSeq });
		if (page && options.resetScroll) {
			el.viewerPanel.focus({ preventScroll: true });
		}
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
		const info = await callWASM("ofdgoDocumentInfo");
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
		return null;
	}
	const openSeq = options.openSeq || state.openSeq;
	if (openSeq !== state.openSeq) {
		return null;
	}
	const pageCount = state.doc.pageCount || 0;
	if (index < 0 || index >= pageCount) {
		return null;
	}
	const pageSeq = ++state.pageSeq;
	if (!state.exporting) {
		if (options.keepBusy) {
			setProgress("正在渲染页面", 76);
		} else {
			setBusy(true, "正在渲染页面", 35, "正在渲染页面");
		}
	}
	try {
		setCurrentPage(index);
		if (options.fit !== false) {
			applyFit(false);
		}
		if (options.scroll !== false) {
			scrollToPage(index);
		}
		const page = await renderFlowPage(index, { throwError: true, openSeq, priority: 0 });
		if (openSeq !== state.openSeq || index !== state.pageIndex) {
			return null;
		}
		if (options.scroll !== false) {
			scrollToPage(index);
		}
		queueNearbyPages(index, openSeq);
		return page;
	} catch (err) {
		if (openSeq === state.openSeq) {
			showError(err, false);
		}
		return null;
	} finally {
		if (!options.keepBusy && !state.exporting && openSeq === state.openSeq && pageSeq === state.pageSeq) {
			setBusy(false);
		}
	}
}

async function downloadAttachment(attachment) {
	if (!state.doc || document.body.hasAttribute("aria-busy")) {
		return;
	}
	const openSeq = state.openSeq;
	state.exporting = true;
	updateControls();
	setBusy(true, "正在读取附件", null, "正在读取附件");
	try {
		const file = window.showSaveFilePicker ? await window.showSaveFilePicker({ suggestedName: attachment.fileName }) : null;
		if (openSeq !== state.openSeq) {
			return;
		}
		const result = await callWASM("ofdgoExportAttachment", attachment.id, file);
		if (openSeq !== state.openSeq) {
			return;
		}
		if (result.blob) {
			downloadBytes(result.blob, result.mime, attachment.fileName);
		}
		setStatus(`附件下载完成 ${formatBytes(result.size)}`);
	} catch (err) {
		if (openSeq === state.openSeq) {
			if (err.name === "AbortError") {
				setStatus("下载已取消");
			} else {
				showError(err, false);
			}
		}
	} finally {
		state.exporting = false;
		state.exportRequestID = 0;
		el.cancelExportButton.hidden = true;
		if (openSeq === state.openSeq) {
			setBusy(false);
			updateControls();
		}
	}
}

function openExportPanel() {
	if (!state.doc || document.body.hasAttribute("aria-busy")) {
		return;
	}
	el.exportForm.reset();
	updateExportRange();
	el.exportPanel.showModal();
}

async function updateExportRange() {
	const specified = el.exportSpecified.checked;
	const value = el.exportRange.value.trim();
	const openSeq = state.openSeq;
	state.exportPages = null;
	el.exportRangeRow.hidden = !specified;
	el.exportRange.removeAttribute("aria-invalid");
	el.exportSubmit.disabled = specified;
	el.exportRangeStatus.textContent = specified && value ? "正在校验" : "";
	if (!specified || !value) {
		return;
	}
	const current = () => openSeq === state.openSeq && el.exportPanel.open
		&& el.exportSpecified.checked && value === el.exportRange.value.trim();
	try {
		const indices = await callWASM("ofdgoParsePageRange", value);
		if (current()) {
			state.exportPages = indices;
			el.exportSubmit.disabled = false;
			el.exportRangeStatus.textContent = `共 ${indices.length} 页`;
		}
	} catch {
		if (current()) {
			el.exportRange.setAttribute("aria-invalid", "true");
			el.exportRangeStatus.textContent = "页码无效";
		}
	}
}

async function exportFile(whole, indices = null) {
	if (!state.doc || document.body.hasAttribute("aria-busy")) {
		return;
	}
	const format = exportFormatInfo(el.exportFormat.value);
	if (!format) {
		return;
	}
	const archive = whole && format.value !== "pdf" && format.value !== "txt";
	const label = archive ? "ZIP" : format.label;
	const extension = archive ? "zip" : format.extension;
	const mime = archive ? "application/zip" : format.mime;
	const openSeq = state.openSeq;
	const fileName = whole ? `${baseFileName()}.${extension}` : pageFileName(extension);
	const pageIndex = state.pageIndex;
	const dpi = exportFormatUsesDPI(format.value) ? currentImageDPI() : 0;
	state.exporting = true;
	updateControls();
	setBusy(true, `正在生成 ${label}`, null, whole ? STATUS.exporting : STATUS.pageExporting);
	try {
		const file = window.showSaveFilePicker ? await window.showSaveFilePicker({
			suggestedName: fileName,
			types: [{ description: label, accept: { [mime]: [`.${extension}`] } }],
		}) : null;
		if (openSeq !== state.openSeq) {
			return;
		}
		const result = whole
			? await callWASM("ofdgoExportDocument", format.value, dpi, indices, file)
			: await callWASM("ofdgoExportPage", pageIndex, format.value, dpi, file);
		if (openSeq !== state.openSeq) {
			return;
		}
		if (result.blob) {
			downloadBytes(result.blob, result.mime, fileName);
		}
		setStatus(`${result.label} 导出完成 ${formatBytes(result.size)}`);
	} catch (err) {
		if (openSeq === state.openSeq) {
			if (err.name === "AbortError") {
				setStatus("导出已取消");
			} else {
				showError(err, false);
			}
		}
	} finally {
		state.exporting = false;
		state.exportRequestID = 0;
		el.cancelExportButton.hidden = true;
		if (openSeq === state.openSeq) {
			setBusy(false);
			updateControls();
		}
	}
}

function cancelExport() {
	if (!state.exportRequestID || el.cancelExportButton.disabled) {
		return;
	}
	el.cancelExportButton.disabled = true;
	setProgress("正在取消", null);
	wasmWorker.postMessage({ type: "cancel", id: state.exportRequestID });
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
	for (const font of state.svgFonts.values()) {
		document.fonts.delete(font);
	}
	state.svgFonts.clear();
	for (const image of state.svgImages.values()) {
		URL.revokeObjectURL(image.url);
	}
	state.svgImages.clear();
	state.pageCache.clear();
	state.selectedPages.clear();
	state.documentSelection = null;
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
					unmountPage(index);
					continue;
				}
				state.visiblePages.add(index);
				state.selectedPages.delete(index);
				renderFlowPage(index, { openSeq, priority: 4 });
			}
			trimPageCache();
		}, {
			root: el.viewerPanel,
			rootMargin: "600px 0px",
		});
	}
	return state.pageObserver;
}

function unmountPage(index) {
	const shell = pageShell(index);
	const selection = document.getSelection();
	const range = selection.rangeCount ? selection.getRangeAt(0) : null;
	if (range && (!state.documentSelection || !isDocumentSelection(range)) && range.intersectsNode(shell)) {
		state.selectedPages.add(index);
		return;
	}
	shell.querySelector(".page-surface").replaceChildren();
	shell.classList.remove("rendered");
	state.selectedPages.delete(index);
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
		trimPageCache();
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
		const page = state.pageCache.get(index);
		state.pageCache.delete(index);
		state.pageCache.set(index, page);
		return Promise.resolve(page);
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
		await nextFrame();
		while (state.pageRenderQueue.length) {
			state.pageRenderQueue.sort(comparePageRenderTask);
			const task = state.pageRenderQueue.shift();
			try {
				if (task.openSeq !== state.openSeq) {
					task.resolve(null);
					continue;
				}
				if (state.pageCache.has(task.index)) {
					task.resolve(state.pageCache.get(task.index));
					continue;
				}
				const page = await callWASM("ofdgoRenderPage", task.index);
				await loadSVGFonts(page.fonts, task.openSeq);
				if (task.openSeq === state.openSeq) {
					for (const image of page.images) {
						if (!state.svgImages.has(image.name)) {
							const blob = new Blob([image.bytes], { type: image.mime });
							state.svgImages.set(image.name, { url: URL.createObjectURL(blob), size: blob.size });
						}
					}
					page.imageNames = page.images.map((image) => image.name);
					delete page.images;
					page.cacheBytes = (page.svg.length + page.text.length) * 2;
					page.text = JSON.parse(page.text);
					state.pageCache.set(task.index, page);
				}
				task.resolve(page);
			} catch (err) {
				task.reject(err);
			} finally {
				state.pageInFlight.delete(task.key);
			}
		}
	} finally {
		state.pageRenderRunning = false;
	}
}

function trimPageCache() {
	const protectedPages = new Set([state.pageIndex, ...state.visiblePages, ...state.visibleThumbnails, ...state.selectedPages]);
	for (const task of state.pageInFlight.values()) {
		protectedPages.add(task.index);
	}
	const references = new Map();
	let bytes = 0;
	for (const page of state.pageCache.values()) {
		bytes += page.cacheBytes;
		for (const name of page.imageNames) {
			references.set(name, (references.get(name) || 0) + 1);
		}
	}
	for (const image of state.svgImages.values()) {
		bytes += image.size;
	}
	for (const [index, page] of state.pageCache) {
		if (state.pageCache.size <= PAGE_CACHE_LIMIT && bytes <= PAGE_CACHE_BYTES) {
			break;
		}
		if (protectedPages.has(index)) {
			continue;
		}
		state.pageCache.delete(index);
		bytes -= page.cacheBytes;
		for (const name of page.imageNames) {
			const count = references.get(name) - 1;
			references.set(name, count);
			if (count === 0) {
				const image = state.svgImages.get(name);
				bytes -= image.size;
				URL.revokeObjectURL(image.url);
				state.svgImages.delete(name);
			}
		}
	}
}

async function loadSVGFonts(fonts, openSeq) {
	for (const font of fonts) {
		if (openSeq !== state.openSeq) {
			return;
		}
		if (state.svgFonts.has(font.name)) {
			continue;
		}
		const data = await callWASM("ofdgoSVGFontData", font.name);
		if (openSeq !== state.openSeq) {
			return;
		}
		const face = new FontFace(font.name, data.bytes, { weight: String(font.weight), style: font.style });
		await face.load();
		if (openSeq !== state.openSeq) {
			return;
		}
		document.fonts.add(face);
		state.svgFonts.set(font.name, face);
	}
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
	surface.append(createTextLayer(page.text));
	for (const link of page.links) {
		let url;
		try {
			url = new URL(link.uri);
		} catch {
			continue;
		}
		if (url.protocol !== "http:" && url.protocol !== "https:") {
			continue;
		}
		const anchor = document.createElement("a");
		anchor.className = "page-link";
		anchor.href = url.href;
		anchor.target = "_blank";
		anchor.rel = "noopener noreferrer";
		anchor.title = url.href;
		anchor.setAttribute("aria-label", url.href);
		anchor.style.left = `${link.x / page.width * 100}%`;
		anchor.style.top = `${link.y / page.height * 100}%`;
		anchor.style.width = `${link.width / page.width * 100}%`;
		anchor.style.height = `${link.height / page.height * 100}%`;
		surface.append(anchor);
	}
	shell.classList.add("rendered");
	renderSearchHighlights(index);
	if (state.doc?.pages?.[index]) {
		layoutPageShell(shell, state.doc.pages[index]);
	}
}

function createTextLayer(text) {
	const layer = document.createElement("div");
	layer.className = "text-layer";
	const measure = textMeasure ||= document.createElement("canvas").getContext("2d");
	measure.font = "100px sans-serif";
	const widths = new Map();
	for (const run of text.runs || []) {
		const line = document.createElement("span");
		line.className = "text-run";
		const chars = Array.from(run.text);
		for (const item of run.spans || []) {
			const span = document.createElement("span");
			span.textContent = chars.slice(item.start, item.end).join("");
			let width = widths.get(span.textContent);
			if (width === undefined) {
				width = measure.measureText(span.textContent).width || 1;
				widths.set(span.textContent, width);
			}
			const [a, b, c, d, e, f] = item.matrix.map((value) => value * MM_TO_PX);
			span.style.transform = `matrix(${a / width}, ${b / width}, ${c / 100}, ${d / 100}, ${e}, ${f})`;
			line.append(span);
		}
		layer.append(line);
	}
	return layer;
}

function syncSelection() {
	const selection = document.getSelection();
	if (state.documentSelection && (!selection.rangeCount || !isDocumentSelection(selection.getRangeAt(0)))) {
		state.documentSelection = null;
		if (state.doc && !document.body.hasAttribute("aria-busy")) {
			setStatus(pageStatus(state.pageIndex, state.doc.pageCount));
		}
	}
	for (const index of state.selectedPages) {
		unmountPage(index);
	}
	trimPageCache();
}

function isDocumentSelection(range) {
	return range.startContainer === el.svgHost && range.startOffset === 0
		&& range.endContainer === el.svgHost && range.endOffset === el.svgHost.childNodes.length;
}

async function selectDocumentText() {
	const selection = document.getSelection();
	if (state.documentSelection && selection.rangeCount && isDocumentSelection(selection.getRangeAt(0))) {
		return;
	}
	const openSeq = state.openSeq;
	const current = { text: null };
	const active = () => state.documentSelection === current && openSeq === state.openSeq
		&& selection.rangeCount > 0 && isDocumentSelection(selection.getRangeAt(0));
	state.documentSelection = current;
	const range = document.createRange();
	range.selectNodeContents(el.svgHost);
	selection.removeAllRanges();
	selection.addRange(range);
	const pages = [];
	const count = state.doc.pageCount;
	try {
		for (let index = 0; index < count; index += 1) {
			setStatus(`正在全选 ${index + 1} / ${count} 页`);
			const text = await callWASM("ofdgoPageTextString", index);
			if (!active()) {
				return;
			}
			if (text) {
				pages.push(text);
			}
		}
		current.text = pages.join("\n");
		setStatus(current.text ? "文字已全选" : "暂无文字");
	} catch (err) {
		if (active()) {
			state.documentSelection = null;
			selection.removeAllRanges();
			showError(err, false);
		}
	}
}

function copySelection(event) {
	if (event.target.closest?.("input, textarea, [contenteditable]")) {
		return;
	}
	const selection = document.getSelection();
	if (selection.isCollapsed || !selection.rangeCount) {
		return;
	}
	const range = selection.getRangeAt(0);
	if (state.documentSelection && isDocumentSelection(range)) {
		event.preventDefault();
		if (state.documentSelection.text === null) {
			setStatus("正在全选文字");
		} else {
			event.clipboardData.setData("text/plain", state.documentSelection.text);
		}
		return;
	}
	if (!el.svgHost.contains(range.startContainer) || !el.svgHost.contains(range.endContainer)) {
		return;
	}
	const lines = [];
	for (const run of el.svgHost.querySelectorAll(".text-run")) {
		if (!range.intersectsNode(run)) {
			continue;
		}
		const part = range.cloneRange();
		if (!run.contains(part.startContainer)) {
			part.setStart(run, 0);
		}
		if (!run.contains(part.endContainer)) {
			part.setEnd(run, run.childNodes.length);
		}
		const text = part.cloneContents().textContent;
		if (text) {
			lines.push(text);
		}
	}
	if (!lines.length) {
		return;
	}
	event.clipboardData.setData("text/plain", lines.join("\n"));
	event.preventDefault();
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
	return el.svgHost.children.item(index);
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
	if (el.viewerPanel.scrollTop + el.viewerPanel.clientHeight >= el.viewerPanel.scrollHeight - 1) {
		const shell = pageShell(state.pageIndex);
		const bounds = shell?.getBoundingClientRect();
		return bounds && bounds.top >= rect.top - 1 && bounds.bottom <= rect.bottom + 1
			? shell : el.svgHost.lastElementChild;
	}
	if (state.continuous) {
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
	if (state.doc && !state.exporting) {
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
		if (state.showPages && !el.pageList.hidden) {
			const panel = el.pageListPanel.getBoundingClientRect();
			const item = next.getBoundingClientRect();
			const top = panel.top + Number.parseFloat(getComputedStyle(next).scrollMarginTop);
			if (item.top < top) {
				el.pageListPanel.scrollTop += item.top - top;
			} else if (item.bottom > panel.bottom) {
				el.pageListPanel.scrollTop += Math.min(item.top - top, item.bottom - panel.bottom);
			}
		}
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
	const size = pageViewSize(page);
	const viewWidth = Math.max(1, size.width * MM_TO_PX);
	const viewHeight = Math.max(1, size.height * MM_TO_PX);
	const scale = state.fitMode === "width" ? state.scale * pageViewSize(currentPageInfo()).width / size.width : state.scale;
	shell.style.width = `${viewWidth * scale}px`;
	shell.style.height = `${viewHeight * scale}px`;
	const surface = shell.querySelector(".page-surface");
	if (surface) {
		surface.style.width = `${width}px`;
		surface.style.height = `${height}px`;
		const x = state.rotation === 90 || state.rotation === 180 ? viewWidth : 0;
		const y = state.rotation >= 180 ? viewHeight : 0;
		surface.style.transform = `scale(${scale}) translate(${x}px, ${y}px) rotate(${state.rotation}deg)`;
	}
}

function parseSVG(svgText, prefix = "") {
	const template = document.createElement("template");
	template.innerHTML = svgText.replace(/xlink:href="(ofdgo-image-[a-f0-9]{64})"/g, (_, name) => `xlink:href="${state.svgImages.get(name).url}"`);
	const svg = document.adoptNode(template.content.firstElementChild);
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
	for (const attr of attrs) {
		for (const node of svg.querySelectorAll(`[${attr === "xlink:href" ? "*|href" : attr}*="#"]`)) {
			const value = node.getAttribute(attr);
			if (!value) {
				continue;
			}
			const next = replaceRef(value);
			if (next !== value) {
				node.setAttribute(attr, next);
			}
		}
	}
}

function focusSearch() {
	if (!state.showPages) {
		toggleSidebar("pages");
	}
	showNavigation(el.searchTab);
	el.searchInput.focus({ preventScroll: true });
	el.searchInput.select();
}

function showNavigation(selected) {
	const scrollTop = el.pageListPanel.scrollTop;
	for (const [tab, panel] of [[el.pagesTab, el.pageList], [el.outlinesTab, el.outlineList], [el.searchTab, el.searchPanel]]) {
		if (tab.getAttribute("aria-selected") === "true") {
			state.navigationScroll.set(tab, scrollTop);
		}
		const active = tab === selected;
		panel.hidden = !active;
		tab.setAttribute("aria-selected", String(active));
		tab.tabIndex = active ? 0 : -1;
	}
	el.pageListPanel.scrollTop = state.navigationScroll.get(selected) || 0;
}

function renderOutlines() {
	state.navigationScroll.clear();
	el.pageListPanel.scrollTop = 0;
	const outlines = state.doc.outlines || [];
	el.pageListTitle.hidden = true;
	el.navigationTabs.hidden = false;
	el.outlinesTab.hidden = outlines.length === 0;
	el.outlineList.replaceChildren();
	if (outlines.length > 0) {
		el.outlineList.append(createOutlineList(outlines));
	}
	showNavigation(el.pagesTab);
}

function resetSearch(clearInput = true) {
	state.searchSeq += 1;
	state.searchQuery = "";
	state.searchMatches = [];
	state.searchIndex = -1;
	if (clearInput) {
		el.searchInput.value = "";
	}
	el.searchStatus.textContent = "";
	el.searchResults.replaceChildren();
	updateSearchCount();
	clearSearchHighlights();
}

async function searchDocument() {
	if (!state.doc || document.body.hasAttribute("aria-busy")) {
		return;
	}
	const query = el.searchInput.value.trim();
	if (query && query === state.searchQuery) {
		await selectSearchMatch(state.searchIndex + 1);
		return;
	}
	resetSearch(false);
	if (!query) {
		return;
	}
	state.searchQuery = query;
	const seq = state.searchSeq;
	const openSeq = state.openSeq;
	const pageCount = state.doc.pageCount;
	try {
		for (let page = 0; page < pageCount; page += 1) {
			const matches = await callWASM("ofdgoSearchPage", page, query);
			if (seq !== state.searchSeq || openSeq !== state.openSeq) {
				return;
			}
			el.searchStatus.textContent = `正在搜索 ${page + 1} / ${pageCount} 页`;
			if (!matches?.length) {
				continue;
			}
			for (const match of matches) {
				state.searchMatches.push({ ...match, page });
			}
			renderSearchResults();
		}
		el.searchStatus.textContent = state.searchMatches.length ? "搜索完成" : "暂无结果";
		if (state.searchIndex < 0 && state.searchMatches.length && !el.searchPanel.hidden && state.showPages) {
			await selectSearchMatch(0);
		}
	} catch (err) {
		if (seq === state.searchSeq && openSeq === state.openSeq) {
			state.searchQuery = "";
			el.searchStatus.textContent = "搜索失败";
			showError(err, false);
		}
	}
}

function updateSearchCount() {
	const count = state.searchMatches.length;
	el.searchCount.textContent = `${state.searchIndex + 1} / ${count}`;
	el.searchPrev.disabled = count === 0;
	el.searchNext.disabled = count === 0;
}

function renderSearchResults() {
	const start = Math.floor(Math.max(0, state.searchIndex) / 50) * 50;
	if (el.searchResults.firstElementChild && Number(el.searchResults.firstElementChild.dataset.matchIndex) !== start) {
		el.searchResults.replaceChildren();
	}
	for (let i = start + el.searchResults.childElementCount; i < Math.min(start + 50, state.searchMatches.length); i += 1) {
		const match = state.searchMatches[i];
		const button = document.createElement("button");
		button.type = "button";
		button.className = "search-result";
		button.dataset.matchIndex = String(i);
		const page = document.createElement("span");
		page.className = "search-page";
		page.textContent = `第 ${match.page + 1} 页`;
		const text = document.createElement("span");
		const mark = document.createElement("mark");
		mark.textContent = match.text;
		text.append(match.before, mark, match.after);
		button.append(page, text);
		button.addEventListener("click", () => selectSearchMatch(i));
		el.searchResults.append(button);
	}
	for (const button of el.searchResults.children) {
		button.setAttribute("aria-current", String(Number(button.dataset.matchIndex) === state.searchIndex));
	}
	updateSearchCount();
}

async function selectSearchMatch(index) {
	const count = state.searchMatches.length;
	if (!count || document.body.hasAttribute("aria-busy")) {
		return;
	}
	index = (index + count) % count;
	state.searchIndex = index;
	const match = state.searchMatches[index];
	const seq = state.searchSeq;
	const openSeq = state.openSeq;
	clearSearchHighlights();
	renderSearchResults();
	if (!await renderPage(match.page, { fit: false, scroll: false })) {
		return;
	}
	await nextFrame();
	if (seq !== state.searchSeq || openSeq !== state.openSeq || state.searchIndex !== index || state.pageIndex !== match.page) {
		return;
	}
	renderSearchHighlights(match.page);
	const mark = pageShell(match.page).querySelector(".search-highlight");
	if (mark) {
		const box = mark.getBoundingClientRect();
		const viewer = el.viewerPanel.getBoundingClientRect();
		el.viewerPanel.scrollTop += box.top + box.height / 2 - viewer.top - el.viewerPanel.clientHeight / 2;
		if (box.left < viewer.left) {
			el.viewerPanel.scrollLeft += box.left - viewer.left;
		} else if (box.right > viewer.left + el.viewerPanel.clientWidth) {
			el.viewerPanel.scrollLeft += box.right - viewer.left - el.viewerPanel.clientWidth;
		}
	} else {
		scrollToPage(match.page);
	}
	const button = el.searchResults.querySelector("[aria-current=true]");
	const row = button.getBoundingClientRect();
	const panel = el.searchResults.getBoundingClientRect();
	if (row.top < panel.top) {
		el.searchResults.scrollTop += row.top - panel.top;
	} else if (row.bottom > panel.bottom) {
		el.searchResults.scrollTop += row.bottom - panel.bottom;
	}
}

function clearSearchHighlights() {
	for (const mark of el.svgHost.querySelectorAll(".search-highlight")) {
		mark.remove();
	}
}

function renderSearchHighlights(index) {
	const match = state.searchMatches[state.searchIndex];
	if (!match || match.page !== index) {
		return;
	}
	const surface = pageShell(index).querySelector(".page-surface");
	for (const mark of surface.querySelectorAll(".search-highlight")) {
		mark.remove();
	}
	for (const box of match.boxes || []) {
		const mark = document.createElement("span");
		mark.className = "search-highlight";
		mark.style.left = `${box.X * MM_TO_PX}px`;
		mark.style.top = `${box.Y * MM_TO_PX}px`;
		mark.style.width = `${box.W * MM_TO_PX}px`;
		mark.style.height = `${box.H * MM_TO_PX}px`;
		surface.append(mark);
	}
}

function createOutlineList(outlines) {
	const list = document.createElement("ul");
	list.className = "outline-list";
	for (const outline of outlines) {
		const item = document.createElement("li");
		const hasChildren = outline.children?.length > 0;
		const row = document.createElement(hasChildren ? "summary" : "div");
		if (!hasChildren) {
			row.className = "outline-leaf";
		}
		const link = document.createElement(outline.page ? "button" : "span");
		link.className = "outline-link";
		const title = document.createElement("span");
		title.textContent = outline.title;
		link.append(title);
		if (outline.page) {
			link.type = "button";
			const page = document.createElement("span");
			page.className = "outline-page";
			page.textContent = String(outline.page);
			page.setAttribute("aria-label", `第 ${outline.page} 页`);
			link.append(page);
			link.addEventListener("click", (event) => {
				event.preventDefault();
				renderPage(outline.page - 1);
			});
		}
		row.append(link);
		if (hasChildren) {
			const details = document.createElement("details");
			details.open = outline.expanded;
			details.append(row, createOutlineList(outline.children));
			item.append(details);
		} else {
			item.append(row);
		}
		list.append(item);
	}
	return list;
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
		layoutThumbnail(button, page);

		const thumb = document.createElement("span");
		thumb.className = "thumb-paper";
		thumb.setAttribute("aria-hidden", "true");
		setThumbnailContent(thumb, state.pageCache.get(page.index)?.svg, page.index, openSeq);

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
	state.visibleThumbnails.clear();
	state.thumbnailInFlight.clear();
	if (state.thumbnailObserver) {
		state.thumbnailObserver.disconnect();
		state.thumbnailObserver = null;
	}
}

function layoutThumbnail(button, page) {
	if (page.width <= 0 || page.height <= 0) {
		return;
	}
	const size = pageViewSize(page);
	button.style.setProperty("--thumb-ratio", `${size.width} / ${size.height}`);
	button.style.setProperty("--thumb-width", `${page.width / size.width * 100}%`);
	button.style.setProperty("--thumb-height", `${page.height / size.height * 100}%`);
	button.style.setProperty("--thumb-rotation", `${state.rotation}deg`);
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
			trimPageCache();
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
	if (state.pageCache.has(index)) {
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
		trimPageCache();
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
		setThumbnailContent(thumb, state.pageCache.get(index)?.svg, index, openSeq);
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
	for (const [field, value] of [
		[el.metaSubject, (doc.subject || "").trim()],
		[el.metaCreationDate, formatDocumentTime(doc.creationDate)],
		[el.metaModDate, formatDocumentTime(doc.modDate)],
	]) {
		field.textContent = value;
		field.parentElement.hidden = !value;
	}
	el.metaType.textContent = doc.docType || "-";
	el.metaVersion.textContent = doc.version || "-";
	el.metaSignatures.textContent = doc.detailsPending ? "正在检查" : String(doc.signatureCount || 0);
	el.metaFonts.textContent = String(doc.fontCount || 0);
	el.pageTotal.textContent = String(doc.pageCount || 0);
	renderAttachments();
	renderSignatures();
	renderAnnotations();
	renderDocumentFonts();
	renderFontList();
	updateLocalFontButton();
}

function renderAttachments() {
	const attachments = state.doc?.attachments || [];
	el.attachmentPanel.hidden = !attachments.length && !state.doc?.attachmentError;
	el.attachmentList.replaceChildren();
	if (state.doc?.attachmentError) {
		const error = document.createElement("div");
		error.className = "attachment-detail";
		error.textContent = "附件读取失败";
		error.title = state.doc.attachmentError;
		el.attachmentList.append(error);
		return;
	}
	for (const attachment of attachments) {
		const item = document.createElement("button");
		item.type = "button";
		item.className = "attachment-item";
		item.title = `下载 ${attachment.fileName}`;
		item.setAttribute("aria-label", `下载 ${attachment.name}`);
		const name = document.createElement("span");
		name.className = "attachment-name";
		name.textContent = attachment.name;
		item.append(name);
		const detail = [attachment.format, attachment.size == null ? "" : formatBytes(attachment.size * 1024)].filter(Boolean).join(" · ");
		if (detail) {
			const meta = document.createElement("span");
			meta.className = "attachment-detail";
			meta.textContent = ` · ${detail}`;
			item.append(meta);
		}
		item.addEventListener("click", () => downloadAttachment(attachment));
		el.attachmentList.append(item);
	}
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

		head.append(badges, name);
		row.append(head);
		appendInfoLine(row, "编号", signature.id);
		appendInfoLine(row, "版本", signature.version);
		appendInfoLine(row, "章图", signature.sealType);
		appendInfoLine(row, "章号", signature.sealId);
		appendInfoLine(row, "章名", signature.sealName);
		appendInfoLine(row, "厂商", signature.sealVendor);
		appendInfoLine(row, "签者", signature.signer);
		appendInfoLine(row, "时间", formatDocumentTime(signature.signatureDateTime));
		appendInfoLine(row, "机构", signatureAgency(signature));
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
			appendInfoLine(row, "制期", signature.sealCertTimeOK ? "有效" : "失效", signature.sealCertTimeOK ? "ok" : "");
		}
		appendSignaturePolicy(row, "时效", signature.certTimeChecked, signature.certTimeOK);
		appendSignatureCheck(row, "信任", signature.certTrustChecked, signature.certTrustOK);
		appendInfoLine(row, "保护", signatureReferenceText(signature), signatureReferenceStatus(signature));
		appendInfoLine(row, "算法", signature.signatureMethod);
		appendInfoLine(row, "散列", signature.digestMethod);
		appendInfoLine(row, "序号", signature.signSerial);
		appendInfoLine(row, "主体", signature.signSubject && signature.signSubject !== signature.signer ? signature.signSubject : "");
		appendInfoLine(row, "颁发", signature.signIssuer);
		appendInfoLine(row, "章证", signature.sealSubject);
		appendInfoLine(row, "错误", signature.error, "fail");
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
	const name = document.createElement("div");
	name.className = "signature-name";
	if (!stamps.length) {
		name.textContent = "签名";
		return name;
	}
	const button = document.createElement("button");
	button.type = "button";
	button.className = "info-name-button";
	button.textContent = "签名";
	button.addEventListener("click", () => focusPageRegion(stamps[0], "签名"));
	name.append(button, signatureStampGroup(stamps));
	return name;
}

function signatureStampGroup(stamps) {
	const group = document.createElement("span");
	group.className = "signature-stamp-group";
	for (const [index, stamp] of stamps.entries()) {
		const item = document.createElement("span");
		item.className = "signature-stamp-item";
		const button = document.createElement("button");
		button.type = "button";
		button.className = "signature-stamp-link";
		button.textContent = `第${index + 1}处`;
		button.addEventListener("click", () => focusPageRegion(stamp, "签名"));
		item.append(index === 0 ? "（" : "", button, index === stamps.length - 1 ? "）" : "、");
		group.append(item);
	}
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
	return `${status === "ok" ? "通过" : status === "fail" ? "失败" : "未验"} ${passed} / ${count}`;
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

function formatDocumentTime(value) {
	const text = String(value || "").trim();
	return text.replace(/^(\d{4})(\d{2})(\d{2})(\d{2})(\d{2})(\d{2})(\.\d+)?(Z|[+-]\d{2}:?\d{2})?$/, "$1-$2-$3 $4:$5:$6$7$8").replace("T", " ");
}

function appendInfoLine(row, label, value, status = "") {
	if (!value && value !== 0) {
		return;
	}
	const line = document.createElement("div");
	line.className = status ? `info-line ${status}` : "info-line";

	const key = document.createElement("span");
	key.className = "info-label";
	key.textContent = label;

	const text = document.createElement("span");
	text.className = "info-value";
	text.textContent = String(value);

	line.append(key, text);
	row.append(line);
}

function appendSignatureCheck(row, label, checked, ok) {
	appendInfoLine(row, label, checked ? ok ? "通过" : "失败" : "未验", checked ? ok ? "ok" : "fail" : "");
}

function appendSignaturePolicy(row, label, checked, ok) {
	if (!checked) {
		return;
	}
	appendSignatureCheck(row, label, checked, ok);
}

async function focusPageRegion(region, label) {
	const pageIndex = region.page - 1;
	if (!state.doc || pageIndex < 0 || document.body.hasAttribute("aria-busy")) {
		return;
	}
	const openSeq = state.openSeq;
	if (!await renderPage(pageIndex, { fit: false, scroll: false })) {
		return;
	}
	await nextFrame();
	if (openSeq !== state.openSeq || pageIndex !== state.pageIndex) {
		return;
	}
	highlightPageRegion(region);
	setStatus(`${label}定位完成 第 ${region.page} 页`);
}

function highlightPageRegion(region) {
	clearRegionHighlights();
	const pageIndex = region.page - 1;
	const shell = pageShell(pageIndex);
	if (!shell || !(region.width > 0 && region.height > 0)) {
		return;
	}
	const mark = document.createElement("div");
	mark.className = "region-highlight";
	mark.style.left = `${region.x * MM_TO_PX}px`;
	mark.style.top = `${region.y * MM_TO_PX}px`;
	mark.style.width = `${region.width * MM_TO_PX}px`;
	mark.style.height = `${region.height * MM_TO_PX}px`;
	shell.querySelector(".page-surface").append(mark);
	mark.scrollIntoView({ block: "nearest", inline: "nearest" });
	window.setTimeout(() => mark.remove(), 1800);
}

function clearRegionHighlights() {
	for (const mark of el.svgHost.querySelectorAll(".region-highlight")) {
		mark.remove();
	}
}

function renderAnnotations() {
	const annotations = (state.doc?.annotations || []).filter((annotation) => annotation.visible);
	el.annotationPanel.hidden = !annotations.length;
	el.annotationList.replaceChildren();
	const types = { Link: "链接", Path: "路径", Highlight: "高亮", Stamp: "印章", Watermark: "水印" };
	const fragment = document.createDocumentFragment();
	for (const annotation of annotations) {
		const row = document.createElement("div");
		row.className = "annotation-row";
		const head = document.createElement("div");
		head.className = "annotation-head";
		const name = document.createElement("button");
		name.type = "button";
		name.className = "info-name-button";
		name.textContent = `第 ${annotation.page} 页`;
		name.disabled = !state.renderAnnotations || annotation.width <= 0 || annotation.height <= 0;
		name.title = state.renderAnnotations ? "定位注解" : "注解已隐藏";
		name.addEventListener("click", () => focusPageRegion(annotation, "注解"));
		const badge = fontBadge(types[annotation.type] || "注解", "");
		head.append(badge, name);
		row.append(head);
		appendInfoLine(row, "内容", annotation.remark);
		appendInfoLine(row, "作者", annotation.creator);
		appendInfoLine(row, "时间", formatDocumentTime(annotation.lastModDate));
		fragment.append(row);
	}
	el.annotationList.append(fragment);
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

		head.append(badges, name);
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
	clearRegionHighlights();
	if (state.fitMode === "free") {
		restoreScaleAnchor(anchor);
	} else {
		applyFit(false);
		scrollToPage(state.pageIndex);
	}
}

function rotatePages() {
	if (document.body.hasAttribute("aria-busy")) {
		return;
	}
	state.rotation = (state.rotation + 90) % 360;
	clearRegionHighlights();
	el.viewerPanel.classList.remove("single-page-fits-height");
	layoutPages();
	for (const page of state.doc.pages) {
		layoutThumbnail(el.pageList.children.item(page.index), page);
	}
	if (state.fitMode === "free") {
		updateFitSpace();
	} else {
		applyFit(false);
	}
	scrollToPage(state.pageIndex);
}

function pageViewSize(page) {
	return state.rotation % 180 ? { width: page.height, height: page.width } : page;
}

function togglePan() {
	if (document.body.hasAttribute("aria-busy")) {
		return;
	}
	endPan();
	state.panMode = !state.panMode;
	el.viewerPanel.classList.toggle("pan-mode", state.panMode);
	el.panButton.setAttribute("aria-pressed", String(state.panMode));
}

function startPan(event) {
	if (!state.panMode || !state.doc || event.pointerType !== "mouse" || event.button !== 0 || document.body.hasAttribute("aria-busy") || event.target.closest("a")) {
		return;
	}
	const rect = el.viewerPanel.getBoundingClientRect();
	if (event.clientX >= rect.left + el.viewerPanel.clientWidth || event.clientY >= rect.top + el.viewerPanel.clientHeight) {
		return;
	}
	event.preventDefault();
	state.pan = { id: event.pointerId, x: event.clientX + el.viewerPanel.scrollLeft, y: event.clientY + el.viewerPanel.scrollTop };
	el.viewerPanel.setPointerCapture(event.pointerId);
	el.viewerPanel.classList.add("panning");
	el.viewerPanel.focus({ preventScroll: true });
}

function movePan(event) {
	const pan = state.pan;
	if (!pan || event.pointerId !== pan.id) {
		return;
	}
	el.viewerPanel.scrollLeft = pan.x - event.clientX;
	el.viewerPanel.scrollTop = pan.y - event.clientY;
}

function endPan(event) {
	const pan = state.pan;
	if (!pan || (event && event.pointerId !== pan.id)) {
		return;
	}
	state.pan = null;
	el.viewerPanel.classList.remove("panning");
	if (el.viewerPanel.hasPointerCapture(pan.id)) {
		el.viewerPanel.releasePointerCapture(pan.id);
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
	page = pageViewSize(page);
	const space = pageSpace();
	const width = Math.max(1, page.width * MM_TO_PX);
	let available = Math.max(1, el.viewerPanel.clientWidth - space * 2);
	if (!viewerHasVerticalScrollbar()) {
		const height = state.continuous
			? state.doc.pages.reduce((total, item) => {
				const size = pageViewSize(item);
				return total + available * size.height / size.width;
			}, 0)
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
	const size = pageViewSize(page);
	const width = Math.max(1, size.width * MM_TO_PX);
	const height = Math.max(1, size.height * MM_TO_PX);
	if (state.continuous) {
		availableWidth = Math.max(1, el.viewerPanel.offsetWidth);
		availableHeight = Math.max(1, el.viewerPanel.offsetHeight);
		const contentWidth = state.doc.pages.reduce((max, item) => Math.max(max, pageViewSize(item).width), 0) * MM_TO_PX;
		const contentHeight = state.doc.pages.reduce((total, item) => total + pageViewSize(item).height, 0) * MM_TO_PX;
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
		clearRegionHighlights();
		layoutPages();
	}
	updateFitSpace();
	if (layoutChanged) {
		restoreScaleAnchor(anchor);
	}
	el.zoomLabel.textContent = `${Math.round(state.scale * 100)}%`;
	if (updateStatus && state.doc && !state.exporting) {
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
	probe.style.scrollbarWidth = getComputedStyle(el.viewerPanel).scrollbarWidth;
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
	const height = shell ? shell.getBoundingClientRect().height : Math.max(1, pageViewSize(page).height * MM_TO_PX * state.scale);
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
	el.rotateButton.disabled = !hasDoc;
	el.panButton.disabled = !hasDoc;
	el.continuousButton.disabled = !hasDoc;
	el.fitButton.toggleAttribute("aria-pressed", hasDoc && state.fitMode === "width");
	el.fitHeightButton.toggleAttribute("aria-pressed", hasDoc && state.fitMode === "height");
	updateAnnotationButton();
	el.exportFormat.disabled = !hasDoc || state.exporting || !state.exportFormats.length;
	el.exportPageButton.disabled = !hasDoc || state.exporting || !state.exportFormats.length;
	el.exportButton.disabled = !hasDoc || state.exporting || !state.exportFormats.length;
	updateDPIControl();
}

function updateAnnotationButton() {
	el.annotationButton.setAttribute("aria-pressed", String(state.renderAnnotations));
	el.annotationButton.title = state.renderAnnotations ? "关闭注解" : "开启注解";
	el.annotationButton.setAttribute("aria-label", el.annotationButton.title);
}

async function callWASM(name, ...args) {
	if (state.wasmExited) {
		scheduleWASMRecovery();
		throw new Error(state.wasmRecovering ? STATUS.recovering : "引擎运行中断");
	}
	if (!state.ready) {
		throw new Error("引擎尚未就绪");
	}
	return new Promise((resolve, reject) => {
		const id = ++wasmRequestID;
		wasmRequests.set(id, { resolve, reject, openSeq: state.openSeq });
		if (name === "ofdgoExportPage" || name === "ofdgoExportDocument" || name === "ofdgoExportAttachment") {
			state.exportRequestID = id;
			el.cancelExportButton.hidden = false;
			el.cancelExportButton.disabled = false;
		}
		try {
			wasmWorker.postMessage({ id, name, args });
		} catch (err) {
			wasmRequests.delete(id);
			reject(err);
		}
	});
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
	el.exportPanel.close();
	endPan();
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

function pageFileName(extension) {
	const page = String(state.pageIndex + 1).padStart(String(state.doc?.pageCount || 1).length, "0");
	return `${baseFileName()}_${page}.${extension}`;
}

function exportFormatInfo(value) {
	return state.exportFormats.find((format) => format.value === value) || null;
}

function exportFormatUsesDPI(value) {
	return value === "png" || value === "jpg";
}

function updateDPIControl() {
	el.imageDPI.disabled = state.exporting || !state.doc || !exportFormatUsesDPI(el.exportFormat.value);
}

function currentImageDPI() {
	return Number.parseFloat(el.imageDPI.value) || DEFAULT_IMAGE_DPI;
}

function formatBytes(size) {
	if (!Number.isFinite(size) || size <= 0) {
		return "0 KB";
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
