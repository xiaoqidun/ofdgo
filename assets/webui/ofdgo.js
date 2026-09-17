import { CanvasEditor, canEditObject, editTextValue, missingGlyphMessage, objectEditReason, selectedText, lineShape, selectionBounds, pagePoint } from "./ofdgo_edit.js";
import { FontManager, FontPicker } from "./ofdgo_font.js";

const MM_TO_PX = 96 / 25.4;
const COMPACT_LAYOUT = window.matchMedia("(max-width: 900px)");
const DEFAULT_IMAGE_DPI = 300;
const PAGE_CACHE_LIMIT = 16;
const PAGE_CACHE_BYTES = 32 * 1024 * 1024;
const OBJECT_CLIPBOARD_TYPE = "application/x-ofdgo-objects";
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
let textMeasure = null;

const state = {
	ready: false,
	wasmExited: false,
	wasmSeq: 0,
	wasmRecovering: false,
	wasmRecoveries: 0,
	exporting: false,
	exportRequestID: 0,
	editing: false,
	selectObjects: true,
	dirty: false,
	editorInfo: null,
	savedRevision: null,
	insertObject: null,
	importPageCount: 0,
	outlineSelection: null,
	outlineAction: "add",
	styleOriginal: null,
	objectClipboard: null,
	styleClipboard: null,
	outlineExpanded: new Map(),
	textFonts: [],
	textFontID: null,
	textDefaults: { type: "TextObject", size: 12 * 25.4 / 72, color: "#000000", wrap: true, align: "left", paragraphHeight: 0, letterSpacing: 0 },
	ofdBytes: null,
	fileName: "ofdgo.ofd",
	openSeq: 0,
	pageSeq: 0,
	searchSeq: 0,
	searchQuery: "",
	searchMatches: [],
	searchIndex: -1,
	navigationScroll: new Map(),
	fontSyncPending: false,
	fontCatalogLoading: false,
	fontFacesLoading: false,
	fontRenderPending: false,
	fontSyncing: false,
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
	newButton: document.querySelector("#newButton"),
	editButton: document.querySelector("#editButton"),
	editNotice: document.querySelector("#editNotice"),
	editorTools: document.querySelector("#editorTools"),
	selectObjectButton: document.querySelector("#selectObjectButton"),
	deleteObjectButton: document.querySelector("#deleteObjectButton"),
	editObjectButton: document.querySelector("#editObjectButton"),
	copyObjectButton: document.querySelector("#copyObjectButton"),
	objectOrder: document.querySelector("#objectOrder"),
	objectAlign: document.querySelector("#objectAlign"),
	objectDistribute: document.querySelector("#objectDistribute"),
	objectRotate: document.querySelector("#objectRotate"),
	objectFlip: document.querySelector("#objectFlip"),
	imageFit: document.querySelector("#imageFit"),
	cropImageButton: document.querySelector("#cropImageButton"),
	resetCropButton: document.querySelector("#resetCropButton"),
	multiSelectButton: document.querySelector("#multiSelectButton"),
	textAlign: document.querySelector("#textAlign"),
	textWrap: document.querySelector("#textWrap"),
	textLineHeight: document.querySelector("#textLineHeight"),
	textSpacing: document.querySelector("#textSpacing"),
	textFont: document.querySelector("#textFont"),
	textFontToggle: document.querySelector("#textFontToggle"),
	textFontList: document.querySelector("#textFontList"),
	textFontAdd: document.querySelector("#textFontAdd"),
	paragraphButton: document.querySelector("#paragraphButton"),
	infoButton: document.querySelector("#infoButton"),
	infoPanel: document.querySelector("#infoPanel"),
	infoForm: document.querySelector("#infoForm"),
	infoTitle: document.querySelector("#infoTitle"),
	infoAuthor: document.querySelector("#infoAuthor"),
	infoSubject: document.querySelector("#infoSubject"),
	infoCancel: document.querySelector("#infoCancel"),
	infoStatus: document.querySelector("#infoStatus"),
	paragraphPanel: document.querySelector("#paragraphPanel"),
	paragraphForm: document.querySelector("#paragraphForm"),
	paragraphCancel: document.querySelector("#paragraphCancel"),
	paragraphStatus: document.querySelector("#paragraphStatus"),
	leftIndent: document.querySelector("#leftIndent"),
	rightIndent: document.querySelector("#rightIndent"),
	firstLineIndent: document.querySelector("#firstLineIndent"),
	importPanel: document.querySelector("#importPanel"),
	importForm: document.querySelector("#importForm"),
	importFile: document.querySelector("#importFile"),
	importRange: document.querySelector("#importRange"),
	importPagesRow: document.querySelector("#importPagesRow"),
	importPages: document.querySelector("#importPages"),
	importPosition: document.querySelector("#importPosition"),
	importOutlines: document.querySelector("#importOutlines"),
	importStatus: document.querySelector("#importStatus"),
	importCancel: document.querySelector("#importCancel"),
	importSubmit: document.querySelector("#importSubmit"),
	textSize: document.querySelector("#textSize"),
	textColor: document.querySelector("#textColor"),
	drawLineButton: document.querySelector("#drawLineButton"),
	arrowTool: document.querySelector("#arrowTool"),
	drawRectangleButton: document.querySelector("#drawRectangleButton"),
	drawEllipseButton: document.querySelector("#drawEllipseButton"),
	eraseButton: document.querySelector("#eraseButton"),
	eraseMode: document.querySelector("#eraseMode"),
	shapeFill: document.querySelector("#shapeFill"),
	shapeFillColor: document.querySelector("#shapeFillColor"),
	shapeStroke: document.querySelector("#shapeStroke"),
	shapeStrokeColor: document.querySelector("#shapeStrokeColor"),
	shapeWidth: document.querySelector("#shapeWidth"),
	undoButton: document.querySelector("#undoButton"),
	redoButton: document.querySelector("#redoButton"),
	insertTextButton: document.querySelector("#insertTextButton"),
	insertImageButton: document.querySelector("#insertImageButton"),
	addPageButton: document.querySelector("#addPageButton"),
	copyPageButton: document.querySelector("#copyPageButton"),
	deletePageButton: document.querySelector("#deletePageButton"),
	movePagePrevButton: document.querySelector("#movePagePrevButton"),
	movePageNextButton: document.querySelector("#movePageNextButton"),
	pageSettingsButton: document.querySelector("#pageSettingsButton"),
	batchPagesButton: document.querySelector("#batchPagesButton"),
	batchPagesPanel: document.querySelector("#batchPagesPanel"),
	batchPagesForm: document.querySelector("#batchPagesForm"),
	batchPageAction: document.querySelector("#batchPageAction"),
	batchPageRange: document.querySelector("#batchPageRange"),
	batchPagePositionRow: document.querySelector("#batchPagePositionRow"),
	batchPagePosition: document.querySelector("#batchPagePosition"),
	batchPagesStatus: document.querySelector("#batchPagesStatus"),
	batchPagesCancel: document.querySelector("#batchPagesCancel"),
	batchPagesSubmit: document.querySelector("#batchPagesSubmit"),
	objectStyleButton: document.querySelector("#objectStyleButton"),
	copyStyleButton: document.querySelector("#copyStyleButton"),
	pasteStyleButton: document.querySelector("#pasteStyleButton"),
	objectBoundsButton: document.querySelector("#objectBoundsButton"),
	objectBoundsPanel: document.querySelector("#objectBoundsPanel"),
	objectBoundsForm: document.querySelector("#objectBoundsForm"),
	objectBoundsCancel: document.querySelector("#objectBoundsCancel"),
	objectBoundsStatus: document.querySelector("#objectBoundsStatus"),
	objectX: document.querySelector("#objectX"),
	objectY: document.querySelector("#objectY"),
	objectWidth: document.querySelector("#objectWidth"),
	objectHeight: document.querySelector("#objectHeight"),
	objectAspect: document.querySelector("#objectAspect"),
	objectStylePanel: document.querySelector("#objectStylePanel"),
	objectStyleForm: document.querySelector("#objectStyleForm"),
	objectOpacity: document.querySelector("#objectOpacity"),
	objectStrokeFields: document.querySelector("#objectStrokeFields"),
	objectDash: document.querySelector("#objectDash"),
	objectDashRow: document.querySelector("#objectDashRow"),
	objectDashPattern: document.querySelector("#objectDashPattern"),
	objectDashOffset: document.querySelector("#objectDashOffset"),
	objectCap: document.querySelector("#objectCap"),
	objectJoin: document.querySelector("#objectJoin"),
	objectStyleStatus: document.querySelector("#objectStyleStatus"),
	objectStyleCancel: document.querySelector("#objectStyleCancel"),
	outlinePanel: document.querySelector("#outlinePanel"),
	outlineForm: document.querySelector("#outlineForm"),
	outlineTitle: document.querySelector("#outlineTitle"),
	outlineParent: document.querySelector("#outlineParent"),
	outlinePosition: document.querySelector("#outlinePosition"),
	outlinePositionRow: document.querySelector("#outlinePositionRow"),
	outlinePage: document.querySelector("#outlinePage"),
	outlineStatus: document.querySelector("#outlineStatus"),
	outlineCancel: document.querySelector("#outlineCancel"),
	outlineSubmit: document.querySelector("#outlineSubmit"),
	pagePanel: document.querySelector("#pagePanel"),
	pageForm: document.querySelector("#pageForm"),
	pagePortrait: document.querySelector("#pagePortrait"),
	pageLandscape: document.querySelector("#pageLandscape"),
	pageWidth: document.querySelector("#pageWidth"),
	pageHeight: document.querySelector("#pageHeight"),
	pageStatus: document.querySelector("#pageStatus"),
	pageCancel: document.querySelector("#pageCancel"),
	saveButton: document.querySelector("#saveButton"),
	createPanel: document.querySelector("#createPanel"),
	createForm: document.querySelector("#createForm"),
	createName: document.querySelector("#createName"),
	createWidth: document.querySelector("#createWidth"),
	createHeight: document.querySelector("#createHeight"),
	createStatus: document.querySelector("#createStatus"),
	createCancel: document.querySelector("#createCancel"),
	insertPanel: document.querySelector("#insertPanel"),
	insertForm: document.querySelector("#insertForm"),
	insertImage: document.querySelector("#insertImage"),
	insertFitRow: document.querySelector("#insertFitRow"),
	insertFit: document.querySelector("#insertFit"),
	insertStatus: document.querySelector("#insertStatus"),
	insertCancel: document.querySelector("#insertCancel"),
	insertSubmit: document.querySelector("#insertSubmit"),
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
	navigationContent: document.querySelector("#navigationContent"),
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

const fontManager = new FontManager({
	onChange: scheduleFontSync,
	onPermissionChange: updateFontPermissionHint,
});
const fontPicker = new FontPicker(el.textFont, el.textFontToggle, el.textFontList, changeTextFont, loadEditorFonts);
el.editorTools.addEventListener("scroll", () => fontPicker.position());

const canvasEditor = new CanvasEditor(el.viewerPanel, {
	textControls: [el.textFont, el.textFontToggle, el.textFontAdd, el.textSize, el.textColor],
	busy: () => document.body.hasAttribute("aria-busy"),
	rotation: () => state.rotation,
	canInsert: (index) => pageCan("insert", index),
	onSelect: (item, previous) => {
		updateObjectControls(item);
		const reason = objectEditReason(item), previousReason = objectEditReason(previous);
		if (state.editing && state.doc && !document.body.hasAttribute("aria-busy")
			&& (reason || previousReason && el.statusText.textContent === previousReason)) {
			setStatus(reason || pageStatus(state.pageIndex, state.doc.pageCount));
		}
	},
	onTransform: (item, change) => changeDocument(item.items ? "ofdgoTransformObjects" : "ofdgoTransformObject", item,
		change.x + item.x * (1 - change.scale), change.y + item.y * (1 - change.scale), change.scale),
	onNudgeChange: () => {
		updatePendingChanges();
		el.undoButton.disabled = !state.ready || state.exporting || !state.editorInfo?.canUndo && !canvasEditor.nudgeChanged();
	},
	onReshape: (item, box) => changeDocument("ofdgoReshapeObject", item, box.x, box.y, box.width, box.height),
	onTextWidth: (item, box) => changeDocument("ofdgoLayoutText", item, box.x - item.x, box.width, true, item.align || "", item.paragraphHeight || 0, item.letterSpacing || 0, ...textIndents(item)),
	onCrop: (item, box) => changeDocument("ofdgoCropImage", item, box.x, box.y, box.width, box.height),
	onCropChange: () => {
		updatePendingChanges();
		updateObjectControls(canvasEditor.selected);
		syncSelection();
		if (state.fontRenderPending) window.setTimeout(refreshPendingFonts, 0);
	},
	onDelete: (item) => changeDocument(item.items ? "ofdgoDeleteObjects" : "ofdgoDeleteObject", item),
	onErase: (items, box, points) => {
		const blocked = items.find(item => !canEditObject(item, box ? "arrange" : "delete"));
		if (blocked) { setStatus(objectEditReason(blocked) || "对象暂不可擦除"); return false; }
		const item = { index: items[0].index, id: items.map(item => item.id) };
		if (points) return changeDocument("ofdgoEraseObjectsPath", item, points);
		return box ? changeDocument("ofdgoEraseObjects", item, box.x, box.y, box.width, box.height)
			: changeDocument("ofdgoDeleteObjects", item);
	},
	onEdit: editCanvasObject,
	drawStyle: shapeStyle,
	onTool: updateDrawingControls,
	onDraw: (index, shape, box, style) => changeDocument("ofdgoInsertShape", null, index, shape,
		box.x, box.y, box.width, box.height, style.fill, style.fillColor, style.stroke, style.strokeColor, style.lineWidth),
	onDrawText: beginCanvasText,
	onCommitText: (item, value, fontData, color = null) => item.draft
		? changeDocument("ofdgoInsertText", null, item.index, value, fontData || item.fontData, item.x, item.y, item.size, item.color, item.width, item.wrap, item.align, item.paragraphHeight, item.letterSpacing, ...textIndents(item))
		: changeDocument("ofdgoUpdateText", item, value, fontData || null, item.size, color),
	onTextChange: () => {
		const editing = canvasEditor.input;
		updatePendingChanges();
		if (!editing) {
			updateTextFonts(canvasEditor.selected, true);
			syncSelection();
			if (state.fontRenderPending) window.setTimeout(refreshPendingFonts, 0);
		}
	},
});

function updatePendingChanges() {
	setDirty(Boolean(state.editorInfo) && (state.editorInfo.revision !== state.savedRevision
		|| canvasEditor.textChanged() || canvasEditor.cropChanged() || canvasEditor.nudgeChanged()));
}

el.editorTools.addEventListener("pointerdown", (event) => {
	if ((canvasEditor.input || canvasEditor.nudge) && event.target.closest("button")) event.preventDefault();
});

function editorClick(button, action) {
	button.addEventListener("click", async () => {
		if ((canvasEditor.nudge || canvasEditor.nudgeCommit) && !await canvasEditor.commitNudge()) return;
		if (canvasEditor.input && !await canvasEditor.commitText()) return;
		if (canvasEditor.crop && !await canvasEditor.commitCrop()) return;
		return action();
	});
}

editorClick(el.selectObjectButton, () => {
	if (document.body.hasAttribute("aria-busy")) {
		return;
	}
	state.selectObjects = !state.selectObjects || state.panMode || Boolean(canvasEditor.tool);
	canvasEditor.setTool("");
	if (state.selectObjects && state.panMode) {
		togglePan();
	}
	updateEditorTools();
});
editorClick(el.deleteObjectButton, () => {
	if (canvasEditor.selected) {
		canvasEditor.options.onDelete(canvasEditor.selected);
	}
});
editorClick(el.multiSelectButton, () => {
	canvasEditor.multiple = !canvasEditor.multiple;
	el.multiSelectButton.setAttribute("aria-pressed", String(canvasEditor.multiple));
});
editorClick(el.editObjectButton, () => {
	if (canvasEditor.selected) {
		editCanvasObject(canvasEditor.selected);
	}
});
el.textSize.addEventListener("change", () => changeTextStyle(false));
el.textColor.addEventListener("change", () => changeTextStyle(true));
editorClick(el.textWrap, () => changeParagraph(!currentTextStyle().wrap));
el.textAlign.addEventListener("change", () => changeParagraph());
el.textLineHeight.addEventListener("change", () => changeParagraph());
editorClick(el.paragraphButton, () => {
	const item = currentTextStyle();
	for (const key of ["leftIndent", "rightIndent", "firstLineIndent"]) el[key].value = item[key] || 0;
	el.paragraphStatus.textContent = "";
	el.paragraphPanel.showModal();
});
el.paragraphCancel.addEventListener("click", () => el.paragraphPanel.close());
editorClick(el.infoButton, () => {
	for (const key of ["Title", "Author", "Subject"]) el[`info${key}`].value = state.doc[key.toLowerCase()] || "";
	el.infoStatus.textContent = "";
	el.infoPanel.showModal();
});
el.infoCancel.addEventListener("click", () => el.infoPanel.close());
el.infoForm.addEventListener("submit", async event => {
	event.preventDefault();
	if (await changeDocument("ofdgoUpdateInfo", null, el.infoTitle.value, el.infoAuthor.value, el.infoSubject.value)) el.infoPanel.close();
});
el.paragraphForm.addEventListener("submit", async event => {
	event.preventDefault();
	if (document.body.hasAttribute("aria-busy") || !el.paragraphForm.reportValidity()) return;
	const item = currentTextStyle();
	const indents = Object.fromEntries(["leftIndent", "rightIndent", "firstLineIndent"].map(key => [key, Number(el[key].value)]));
	if (item === state.textDefaults) {
		Object.assign(item, indents);
	} else {
		if (!confirmTextReflow(item)) return;
		const saved = await changeDocument("ofdgoLayoutText", item, null, null, Boolean(item.wrap), item.align || "left",
			item.paragraphHeight || 0, item.letterSpacing || 0, ...textIndents(indents));
		if (!saved) return;
	}
	el.paragraphPanel.close();
});
el.textSpacing.addEventListener("change", () => changeParagraph());
el.shapeWidth.addEventListener("focus", () => { el.shapeWidth.defaultValue = el.shapeWidth.value; });
for (const input of [el.textSize, el.shapeWidth, el.textLineHeight, el.textSpacing]) {
	input.addEventListener("keydown", (event) => {
		if (event.isComposing) return;
		if (event.key === "Enter" || event.key === "Escape") {
			event.preventDefault();
			event.stopPropagation();
			if (event.repeat) return;
			if (event.key === "Escape") {
				updateObjectControls(canvasEditor.selected, true);
				if (input === el.shapeWidth && canvasEditor.selected?.type !== "PathObject") {
					input.value = input.defaultValue;
				}
				input.blur();
			} else {
				input.dispatchEvent(new Event("change"));
			}
		}
	});
}
editorClick(el.copyObjectButton, () => {
	const item = canvasEditor.selected;
	if (!item) {
		return;
	}
	return changeDocument("ofdgoCopyObjects", { ...item, id: canvasEditor.items().map(member => member.id) }, 3, 3);
});
el.objectOrder.addEventListener("change", () => {
	const action = el.objectOrder.value;
	el.objectOrder.value = "";
	const item = canvasEditor.selected;
	if (!item || !action) {
		return;
	}
	return changeDocument("ofdgoOrderObjects", { ...item, id: canvasEditor.items().map(member => member.id) }, action);
});
el.objectAlign.addEventListener("change", () => {
	const alignment = el.objectAlign.value;
	el.objectAlign.value = "";
	if (canvasEditor.selected && alignment) {
		return changeDocument(canvasEditor.selected.items ? "ofdgoAlignObjects" : "ofdgoAlignObject", canvasEditor.selected, alignment);
	}
});
el.objectDistribute.addEventListener("change", () => {
	const axis = el.objectDistribute.value;
	el.objectDistribute.value = "";
	if (axis && canvasEditor.items().length >= 3) {
		return changeDocument("ofdgoDistributeObjects", canvasEditor.selected, axis);
	}
});
for (const [select, method] of [[el.objectRotate, "ofdgoRotateObjects"], [el.objectFlip, "ofdgoFlipObjects"]]) {
	select.addEventListener("change", () => {
		const value = select.value, item = canvasEditor.selected;
		select.value = "";
		if (!item || !value || canvasEditor.crop) return;
		return changeDocument(method, { ...item, id: canvasEditor.items().map(member => member.id) },
			select === el.objectRotate ? Number(value) : value);
	});
}
el.cropImageButton.addEventListener("click", () => {
	if (document.body.hasAttribute("aria-busy")) return;
	if (canvasEditor.crop) return canvasEditor.commitCrop();
	const item = canvasEditor.selected;
	if (item?.type === "ImageObject") return startImageCrop(item);
});
el.imageFit.addEventListener("change", () => {
	const mode = el.imageFit.value, item = canvasEditor.selected;
	el.imageFit.value = "";
	if (mode && item?.type === "ImageObject" && !canvasEditor.crop) return changeDocument("ofdgoFitImage", item, mode);
});
el.resetCropButton.addEventListener("click", () => {
	if (document.body.hasAttribute("aria-busy")) return;
	if (canvasEditor.crop) {
		canvasEditor.closeCrop();
		return;
	}
	const item = canvasEditor.selected;
	if (item?.imageBounds) return canvasEditor.options.onCrop(item, item.imageBounds);
});
editorClick(el.undoButton, () => changeDocument("ofdgoUndo"));
editorClick(el.redoButton, () => changeDocument("ofdgoRedo"));
for (const [button, tool] of [[el.drawLineButton, "line"], [el.drawRectangleButton, "rectangle"], [el.drawEllipseButton, "ellipse"]]) {
	editorClick(button, () => toggleDrawingTool(tool));
}
editorClick(el.eraseButton, () => toggleDrawingTool(el.eraseMode.value));
el.eraseMode.addEventListener("change", () => {
	if (canvasEditor.tool.startsWith("erase-")) canvasEditor.setTool(el.eraseMode.value);
});
for (const input of [el.shapeFill, el.shapeFillColor, el.shapeStroke, el.shapeStrokeColor, el.shapeWidth]) {
	input.addEventListener("change", changeShapeStyle);
}
el.addPageButton.addEventListener("change", async () => {
	const action = el.addPageButton.value;
	el.addPageButton.value = "";
	if (canvasEditor.input && !await canvasEditor.commitText()) return;
	if (canvasEditor.crop && !await canvasEditor.commitCrop()) return;
	if (action === "import") { openImportPanel(); return; }
	const page = currentPageInfo();
	return changeDocument("ofdgoChangePage", null, "add", state.pageIndex, page.width, page.height);
});
el.importCancel.addEventListener("click", () => el.importPanel.close());
el.importPanel.addEventListener("close", () => {
	state.importPageCount = 0;
	el.importForm.reset();
	if (state.ready) callWASM("ofdgoLoadImport", null).catch(err => showError(err, false));
});
el.importFile.addEventListener("change", loadImportFile);
el.importRange.addEventListener("change", () => {
	const custom = el.importRange.value === "custom";
	el.importPagesRow.hidden = !custom;
	el.importPages.disabled = !custom;
	el.importPages.required = custom;
	if (custom) el.importPages.focus();
});
el.importForm.addEventListener("submit", async event => {
	event.preventDefault();
	if (document.body.hasAttribute("aria-busy") || !state.importPageCount || !el.importForm.reportValidity()) return;
	const position = { before:state.pageIndex, after:state.pageIndex+1, first:0, last:state.doc.pageCount }[el.importPosition.value];
	const saved = await changeDocument("ofdgoImportPages", null, el.importRange.value === "custom" ? el.importPages.value : "", position, el.importOutlines.checked);
	if (saved) el.importPanel.close();
});
editorClick(el.copyPageButton, () => changeDocument("ofdgoChangePage", null, "copy", state.pageIndex));
editorClick(el.deletePageButton, () => changeDocument("ofdgoChangePage", null, "delete", state.pageIndex));
editorClick(el.movePagePrevButton, () => changeDocument("ofdgoChangePage", null, "move", state.pageIndex, state.pageIndex - 1));
editorClick(el.movePageNextButton, () => changeDocument("ofdgoChangePage", null, "move", state.pageIndex, state.pageIndex + 1));
editorClick(el.pageSettingsButton, openPagePanel);
editorClick(el.batchPagesButton, () => {
	el.batchPageAction.value = "copy";
	el.batchPagesSubmit.textContent = "复制";
	el.batchPagePositionRow.hidden = true;
	el.batchPagePosition.value = "after";
	el.batchPageRange.value = String(state.pageIndex + 1);
	el.batchPagesStatus.textContent = "";
	el.batchPagesPanel.showModal();
	el.batchPageRange.select();
});
el.batchPagesCancel.addEventListener("click", () => el.batchPagesPanel.close());
el.batchPageAction.addEventListener("change", () => {
	el.batchPagesSubmit.textContent = { copy: "复制", move: "移动", delete: "删除" }[el.batchPageAction.value];
	el.batchPagePositionRow.hidden = el.batchPageAction.value !== "move";
});
el.batchPagesForm.addEventListener("submit", async event => {
	event.preventDefault();
	const args = [el.batchPageAction.value, el.batchPageRange.value];
	if (args[0] === "move") args.push({ before: state.pageIndex, after: state.pageIndex + 1, first: 0, last: state.doc.pageCount }[el.batchPagePosition.value]);
	if (await changeDocument("ofdgoBatchPages", null, ...args)) el.batchPagesPanel.close();
});
editorClick(el.objectStyleButton, openObjectStyle);
editorClick(el.objectBoundsButton, openObjectBounds);
el.arrowTool.addEventListener("change", async () => {
	const tool = el.arrowTool.value;
	el.arrowTool.value = "";
	if (await canvasEditor.commitText() && await canvasEditor.commitCrop()) {
		setPan(false);
		state.selectObjects = true;
		canvasEditor.setTool(tool);
		updateControls();
	}
});
el.copyStyleButton.addEventListener("click", async () => {
	const item = canvasEditor.selected, seq = state.openSeq;
	setBusy(true);
	try {
		await callWASM("ofdgoCaptureStyle", item.index, item.id);
		if (seq === state.openSeq) { state.styleClipboard = item.type; el.objectStylePanel.close(); }
	} catch (err) { if (seq === state.openSeq) el.objectStyleStatus.textContent = err.message; }
	finally { if (seq === state.openSeq) setBusy(false); }
});
el.pasteStyleButton.addEventListener("click", async () => {
	const item = canvasEditor.selected;
	if (await changeDocument("ofdgoPasteStyle", { ...item, id: canvasEditor.items().map(member => member.id) })) el.objectStylePanel.close();
});
el.objectBoundsCancel.addEventListener("click", () => el.objectBoundsPanel.close());
for (const input of [el.objectWidth, el.objectHeight]) input.addEventListener("input", () => {
	if (!el.objectAspect.checked || !input.value || el.objectWidth.disabled || el.objectHeight.disabled) return;
	const box = state.boundsOriginal;
	if (input === el.objectWidth) setObjectDimension(el.objectHeight, Number(input.value) * box.height / box.width);
	else setObjectDimension(el.objectWidth, Number(input.value) * box.width / box.height);
});
el.objectAspect.addEventListener("change", () => {
	if (el.objectAspect.checked) setObjectDimension(el.objectHeight, objectDimension(el.objectWidth) * state.boundsOriginal.height / state.boundsOriginal.width);
});
el.objectBoundsForm.addEventListener("submit", async event => {
	event.preventDefault();
	const item = canvasEditor.selected;
	const values = [el.objectX, el.objectY, el.objectWidth, el.objectHeight].map(objectDimension);
	let saved;
	if (item.shape) {
		if (lineShape(item.shape)) { values[2] *= Math.sign(item.geometry.width); values[3] *= Math.sign(item.geometry.height); }
		saved = await changeDocument("ofdgoReshapeObject", item, ...values);
	} else saved = await changeDocument("ofdgoResizeObjects", { ...item, id: canvasEditor.items().map(member => member.id) }, ...values);
	if (saved) el.objectBoundsPanel.close();
});
el.objectStyleCancel.addEventListener("click", () => el.objectStylePanel.close());
el.objectDash.addEventListener("change", () => {
	el.objectDashRow.hidden = el.objectDash.value !== "custom";
	el.objectDashPattern.required = !el.objectDashRow.hidden;
});
el.objectStyleForm.addEventListener("submit", async event => {
	event.preventDefault();
	const style = readObjectStyle();
	const item = canvasEditor.selected;
	if (await changeDocument("ofdgoStyleObjects", { ...item, id: canvasEditor.items().map(member => member.id) }, style)) el.objectStylePanel.close();
});
el.outlineCancel.addEventListener("click", () => el.outlinePanel.close());
el.outlineForm.addEventListener("submit", async event => {
	event.preventDefault();
	const path = state.outlineAction === "add" ? JSON.parse(el.outlineParent.value) : state.outlineSelection;
	const page = el.outlinePage.value === "" ? -1 : Number(el.outlinePage.value) - 1;
	const saved = state.outlineAction === "move"
		? await changeDocument("ofdgoMoveOutline", null, state.outlineSelection, JSON.parse(el.outlineParent.value), Number(el.outlinePosition.value) - 1)
		: await changeDocument("ofdgoChangeOutline", null, state.outlineAction, path, el.outlineTitle.value, page);
	if (saved) el.outlinePanel.close();
});
el.outlineParent.addEventListener("change", updateOutlinePosition);
el.pageCancel.addEventListener("click", () => el.pagePanel.close());
el.pageWidth.addEventListener("input", updatePageDirection);
el.pageHeight.addEventListener("input", updatePageDirection);
for (const radio of [el.pagePortrait, el.pageLandscape]) {
	radio.addEventListener("change", () => {
		[el.pageWidth.value, el.pageHeight.value] = [el.pageHeight.value, el.pageWidth.value];
		updatePageDirection();
	});
}
el.pageForm.addEventListener("submit", async (event) => {
	event.preventDefault();
	el.pageStatus.textContent = "";
	if (await changeDocument("ofdgoChangePage", null, "resize", state.pageIndex, Number(el.pageWidth.value), Number(el.pageHeight.value))) {
		el.pagePanel.close();
	}
});
el.ofdButton.addEventListener("click", openOFDFile);
el.newButton.addEventListener("click", openCreatePanel);
el.editButton.addEventListener("click", toggleEditor);
el.editButton.addEventListener("pointerdown", (event) => {
	if (canvasEditor.input) event.preventDefault();
});
el.createCancel.addEventListener("click", () => el.createPanel.close());
el.createForm.addEventListener("submit", createDocument);
editorClick(el.insertTextButton, toggleTextTool);
editorClick(el.insertImageButton, () => openInsertPanel());
el.insertCancel.addEventListener("click", () => el.insertPanel.close());
el.insertPanel.addEventListener("close", () => { state.insertObject = null; });
el.insertForm.addEventListener("submit", insertObject);
el.textFontAdd.addEventListener("click", () => openFontFile(el.fontInput));
el.saveButton.addEventListener("click", () => exportFile(true, null, "ofd"));
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
document.addEventListener("keydown", () => el.viewerPanel.classList.remove("pointer-focus"), true);
document.addEventListener("pointerdown", () => el.viewerPanel.classList.add("pointer-focus"), true);
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
document.addEventListener("cut", copySelection);
el.viewerPanel.addEventListener("paste", pasteEditorContent);
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
	if ((event.ctrlKey || event.metaKey) && !event.shiftKey && key.toLowerCase() === "s" && state.editorInfo) {
		event.preventDefault();
		if (!document.body.hasAttribute("aria-busy") && !formDialogOpen()) exportFile(true, null, "ofd");
		return;
	}
	if ((event.ctrlKey || event.metaKey) && !event.shiftKey && key.toLowerCase() === "a" && state.doc
		&& el.viewerPanel.contains(target) && !target.closest("input, textarea, select, [contenteditable]")) {
		event.preventDefault();
		if (!document.body.hasAttribute("aria-busy") && !formDialogOpen()) {
			selectDocumentText();
		}
		return;
	}
	if (document.body.hasAttribute("aria-busy") || formDialogOpen()) {
		return;
	}
	if (event.ctrlKey || event.metaKey) {
		if (state.editing && !target.closest("input, textarea, select, [contenteditable]")
			&& (key.toLowerCase() === "z" || (!event.shiftKey && key.toLowerCase() === "y"))) {
			event.preventDefault();
			(key.toLowerCase() === "y" || event.shiftKey ? el.redoButton : el.undoButton).click();
		} else if (!event.shiftKey && key.toLowerCase() === "o") {
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
		if (!event.repeat) el.pageInput.dispatchEvent(new Event("change"));
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

function formDialogOpen() {
	return el.exportPanel.open || el.createPanel.open || el.insertPanel.open || el.pagePanel.open || el.paragraphPanel.open || el.importPanel.open || el.infoPanel.open
		|| el.batchPagesPanel.open || el.objectStylePanel.open || el.outlinePanel.open || el.objectBoundsPanel.open;
}

function setObjectDimension(input, value) {
	input.value = String(Number(value.toFixed(2)));
	input.defaultValue = input.value;
	input.dataset.precise = String(value);
}

function objectDimension(input) {
	return Number(input.value === input.defaultValue ? input.dataset.precise : input.value);
}

function openObjectBounds() {
	const item = canvasEditor.selected;
	const box = item.geometry || selectionBounds((item.items || [item]).map(member => member.bounds || member));
	state.boundsOriginal = { x: box.x, y: box.y, width: Math.abs(box.width), height: Math.abs(box.height) };
	for (const [input, key] of [[el.objectX,"x"], [el.objectY,"y"], [el.objectWidth,"width"], [el.objectHeight,"height"]]) setObjectDimension(input, state.boundsOriginal[key]);
	el.objectAspect.checked = true;
	el.objectAspect.disabled = !item.shape && (item.items || [item]).some(member => member.type !== "ImageObject") || lineShape(item.shape) && (!box.width || !box.height);
	el.objectWidth.disabled = lineShape(item.shape) && !box.width;
	el.objectHeight.disabled = lineShape(item.shape) && !box.height;
	el.objectBoundsStatus.textContent = "";
	el.objectBoundsPanel.showModal();
}

function openObjectStyle() {
	const items = canvasEditor.items();
	if (!items.length) return;
	el.copyStyleButton.disabled = items.length !== 1 || !canEditObject(items[0], "update");
	el.pasteStyleButton.disabled = !state.styleClipboard || !items.every(item => item.type === state.styleClipboard && canEditObject(item, "update"));
	const shared = (key, fallback) => items.every(item => (item[key] ?? fallback) === (items[0][key] ?? fallback)) ? items[0][key] ?? fallback : null;
	const alpha = shared("alpha", 255), dash = shared("dashPattern", "");
	el.objectOpacity.value = alpha === null ? "" : String(Math.round((1 - alpha / 255) * 10000) / 100);
	el.objectOpacity.placeholder = alpha === null ? "混合" : "";
	el.objectStrokeFields.hidden = !items.every(item => item.type === "PathObject");
	el.objectDash.value = dash === null ? "mixed" : dash === "" ? "solid" : "custom";
	el.objectDashPattern.value = dash || "";
	el.objectDashRow.hidden = el.objectDash.value !== "custom";
	el.objectDashPattern.required = !el.objectStrokeFields.hidden && !el.objectDashRow.hidden;
	el.objectDashOffset.value = shared("dashOffset", 0) ?? "";
	el.objectCap.value = shared("cap", "") || (shared("cap", "") === null ? "mixed" : "Butt");
	el.objectJoin.value = shared("join", "") || (shared("join", "") === null ? "mixed" : "Miter");
	state.styleOriginal = Object.fromEntries(["objectOpacity", "objectDash", "objectDashPattern", "objectDashOffset", "objectCap", "objectJoin"].map(key => [key, el[key].value]));
	el.objectStyleStatus.textContent = "";
	el.objectStylePanel.showModal();
}

function readObjectStyle() {
	const style = {}, changed = key => el[key].value !== state.styleOriginal[key];
	if (changed("objectOpacity") && el.objectOpacity.value !== "") style.alpha = Math.round((1 - Number(el.objectOpacity.value) / 100) * 255);
	if (!el.objectStrokeFields.hidden) {
		if (changed("objectDash") || changed("objectDashPattern")) {
			const patterns = { solid: "", dash: "3 2", dot: "0.3 1.5", dashdot: "3 1.5 0.3 1.5", custom: el.objectDashPattern.value.trim() };
			if (el.objectDash.value in patterns) style.dashPattern = patterns[el.objectDash.value];
		}
		if (changed("objectDashOffset") && el.objectDashOffset.value !== "") style.dashOffset = Number(el.objectDashOffset.value);
		if (changed("objectCap") && el.objectCap.value !== "mixed") style.cap = el.objectCap.value;
		if (changed("objectJoin") && el.objectJoin.value !== "mixed") style.join = el.objectJoin.value;
	}
	return style;
}

function openOutlinePanel(action) {
	state.outlineAction = action;
	let outline = null, items = state.doc.outlines || [];
	for (const index of state.outlineSelection || []) { outline = items[index]; items = outline.children || []; }
	el.outlineTitle.value = action === "add" ? "" : outline.title;
	el.outlinePage.value = action === "add" ? String(state.pageIndex + 1) : outline.page || "";
	el.outlinePage.max = String(state.doc.pageCount);
	el.outlineParent.replaceChildren();
	const addOption = (path, title) => {
		const option = document.createElement("option");
		option.value = JSON.stringify(path);
		option.textContent = title;
		option.title = title;
		el.outlineParent.append(option);
	};
	addOption([], "无");
	const walk = (items, path = []) => items.forEach((item, index) => {
		const next = [...path, index];
		if (action === "move" && state.outlineSelection.every((part, i) => next[i] === part)) return;
		addOption(next, `${"　".repeat(path.length)}${item.title}`);
		walk(item.children || [], next);
	});
	walk(state.doc.outlines || []);
	el.outlineParent.value = JSON.stringify(action === "add" ? state.outlineSelection || [] : state.outlineSelection.slice(0, -1));
	el.outlineParent.disabled = action !== "add" && action !== "move";
	el.outlineTitle.disabled = el.outlinePage.disabled = action === "delete" || action === "move";
	el.outlinePositionRow.hidden = action !== "move";
	el.outlinePosition.disabled = action !== "move";
	updateOutlinePosition();
	el.outlineSubmit.textContent = { add: "新增", update: "确定", delete: "删除", move: "确定" }[action];
	el.outlineStatus.textContent = action === "delete" && outline.children?.length ? "同时删除子目录" : "";
	el.outlinePanel.showModal();
}

function outlineChildren(outlines, path) {
	for (const index of path) outlines = outlines[index].children || [];
	return outlines;
}

function updateOutlinePosition() {
	if (state.outlineAction !== "move") return;
	const parent = JSON.parse(el.outlineParent.value);
	const same = JSON.stringify(parent) === JSON.stringify(state.outlineSelection.slice(0, -1));
	const count = outlineChildren(state.doc.outlines, parent).length + (same ? 0 : 1);
	el.outlinePosition.max = String(count);
	el.outlinePosition.value = String(same ? state.outlineSelection.at(-1) + 1 : count);
}

function remapOutlineExpansion(name, args, next) {
	const roots = state.doc.outlines || [], expanded = new Map();
	const walk = (items, visit, path = []) => items.forEach((item, index) => {
		const current = [...path, index];
		visit(item, JSON.stringify(current));
		walk(item.children || [], visit, current);
	});
	walk(roots, (item, key) => expanded.set(item, state.outlineExpanded.get(key) ?? item.expanded));
	const moving = name === "ofdgoMoveOutline", action = moving ? "move" : args[0], source = moving ? args[0] : args[1];
	let item;
	if (action === "move" || action === "delete") item = outlineChildren(roots, source.slice(0, -1)).splice(source.at(-1), 1)[0];
	if (action === "move" || action === "add") {
		let children = roots;
		for (const index of next.slice(0, -1)) {
			expanded.set(children[index], true);
			children = children[index].children ||= [];
		}
		children.splice(next.at(-1), 0, item || {});
	}
	state.outlineExpanded.clear();
	walk(roots, (item, key) => state.outlineExpanded.set(key, Boolean(expanded.get(item))));
}

function openImportPanel() {
	if (!state.editing || document.body.hasAttribute("aria-busy")) return;
	state.importPageCount = 0;
	el.importForm.reset();
	el.importStatus.textContent = "";
	el.importSubmit.disabled = true;
	el.importPagesRow.hidden = true;
	el.importPages.disabled = true;
	el.importPages.required = false;
	el.importPanel.showModal();
}

async function loadImportFile() {
	const file = el.importFile.files[0];
	const openSeq = state.openSeq;
	state.importPageCount = 0;
	el.importSubmit.disabled = true;
	el.importStatus.textContent = "";
	setBusy(true);
	try {
		const data = file ? new Uint8Array(await file.arrayBuffer()) : null;
		if (openSeq !== state.openSeq) return;
		const info = await callWASM("ofdgoLoadImport", data);
		if (openSeq !== state.openSeq || !el.importPanel.open) {
			await callWASM("ofdgoLoadImport", null);
			return;
		}
		state.importPageCount = info?.pageCount || 0;
		el.importPages.title = state.importPageCount ? `页码 1-${state.importPageCount}` : "页码";
		el.importStatus.textContent = info?.signed ? "保留签章外观，原签名不再有效" : "";
		el.importSubmit.disabled = !state.importPageCount;
	} catch (err) {
		if (openSeq === state.openSeq && el.importPanel.open) el.importStatus.textContent = err.message;
	} finally {
		if (openSeq === state.openSeq) setBusy(false);
	}
}

function openPagePanel() {
	if (!state.editing || document.body.hasAttribute("aria-busy")) {
		return;
	}
	const page = currentPageInfo();
	el.pageWidth.value = String(page.width);
	el.pageHeight.value = String(page.height);
	el.pageStatus.textContent = "";
	updatePageDirection();
	el.pagePanel.showModal();
}

function updatePageDirection() {
	const landscape = Number(el.pageWidth.value) > Number(el.pageHeight.value);
	el.pageLandscape.checked = landscape;
	el.pagePortrait.checked = !landscape;
}

function warnUnsaved(event) {
	event.preventDefault();
	event.returnValue = "";
}

function setDirty(dirty) {
	if (state.dirty === dirty) {
		return;
	}
	state.dirty = dirty;
	if (dirty) {
		window.addEventListener("beforeunload", warnUnsaved);
	} else {
		window.removeEventListener("beforeunload", warnUnsaved);
	}
}

function discardChanges() {
	return !state.dirty || window.confirm("文档尚未保存，放弃更改？");
}

function setEditorInfo(doc) {
	state.editorInfo = { revision: doc.revision, canUndo: doc.canUndo, canRedo: doc.canRedo,
		pageCapabilities: doc.pageCapabilities, editWarnings: doc.editWarnings || [] };
	setDirty(doc.revision !== state.savedRevision);
}

function pageCan(capability, index = state.pageIndex) {
	const bit = { insert: 1, copy: 2, delete: 4, move: 8, resize: 16 }[capability];
	return Boolean(state.editorInfo?.pageCapabilities?.[index] & bit);
}

async function toggleEditor() {
	if (!state.doc || !state.ready || document.body.hasAttribute("aria-busy") || formDialogOpen()) return;
	if (state.editorInfo) {
		if (!await canvasEditor.commitText() || !await canvasEditor.commitCrop()) return;
		state.editing = !state.editing;
		canvasEditor.clear();
		canvasEditor.setTool("");
		if (state.editing) {
			state.selectObjects = true;
			setPan(false);
		}
		updateControls();
		renderOutlines(false);
		renderPageList();
		applyFit(false);
		return;
	}
	const { pageIndex, fitMode, scale } = state;
	const { scrollLeft, scrollTop } = el.viewerPanel;
	const openSeq = ++state.openSeq;
	setBusy(true, "正在准备编辑", null, "正在准备编辑");
	try {
		const doc = await callWASM("ofdgoEditDocument");
		if (openSeq !== state.openSeq) return;
		state.savedRevision = doc.revision;
		setEditorInfo(doc);
		state.editing = true;
		state.selectObjects = true;
		state.ofdBytes = null;
		state.objectClipboard = null;
		state.styleClipboard = null;
		canvasEditor.clear();
		setPan(false);
		await openDocument({ doc, openSeq, skipAutoFonts: true, pageIndex, fitMode, scale,
			keepPreview: true, previewScroll: { scrollLeft, scrollTop } });
	} catch (err) {
		if (openSeq === state.openSeq) showError(err, false);
	} finally {
		if (openSeq === state.openSeq) {
			setBusy(false);
			updateControls();
		}
	}
}

function confirmTextReflow(item) {
	return canEditObject(item, "reflow") && (canEditObject(item, "layoutKnown")
		|| window.confirm("原文排版未知，此操作将重新排版。继续？"));
}

function openCreatePanel() {
	if (document.body.hasAttribute("aria-busy")) {
		return;
	}
	el.createForm.reset();
	el.createStatus.textContent = "";
	el.createPanel.showModal();
}

async function createDocument(event) {
	event.preventDefault();
	if (document.body.hasAttribute("aria-busy") || !discardChanges()) {
		return;
	}
	const title = el.createName.value.trim() || "未命名";
	const openSeq = ++state.openSeq;
	setBusy(true, "正在新建文档", null, "正在新建文档");
	try {
		await ensureWASM();
		if (openSeq !== state.openSeq) {
			return;
		}
		const doc = await callWASM("ofdgoCreateDocument", title, Number(el.createWidth.value), Number(el.createHeight.value), state.renderAnnotations);
		if (openSeq !== state.openSeq) {
			return;
		}
		state.editing = true;
		state.objectClipboard = null;
		state.styleClipboard = null;
		state.fontRenderPending = false;
		canvasEditor.clear();
		delete state.textDefaults.fontChoice;
		updateTextFonts(null, true);
		state.selectObjects = true;
		setPan(false);
		state.ofdBytes = null;
		state.fileName = `${title.replace(/[\\/:*?"<>|]+/g, "_").replace(/\.ofd$/i, "")}.ofd`;
		state.savedRevision = null;
		setEditorInfo(doc);
		el.createPanel.close();
		await openDocument({ doc, openSeq, skipAutoFonts: true, resetScroll: true });
	} catch (err) {
		if (openSeq === state.openSeq) {
			el.createStatus.textContent = err.message;
		}
	} finally {
		if (openSeq === state.openSeq) {
			setBusy(false);
			updateControls();
		}
	}
}

async function editCanvasObject(item) {
	if (!state.editing || document.body.hasAttribute("aria-busy") || canvasEditor.input || item.items) return;
	if (item.type === "TextObject") {
		if (!confirmTextReflow(item)) return;
	} else if (!canEditObject(item, "update")) return;
	if (item.type === "PathObject") {
		el.shapeWidth.focus();
		return;
	}
	if (item.type === "ImageObject") {
		return openInsertPanel(item);
	}
	const openSeq = state.openSeq;
	setBusy(true);
	try {
		const data = await callWASM("ofdgoEditorFont", item.font);
		const face = new FontFace(`ofdgo-edit-${item.font}`, data.bytes);
		await face.load();
		if (openSeq === state.openSeq && item.node.isConnected) {
			canvasEditor.editText(item, face);
		}
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

async function startImageCrop(item) {
	const openSeq = state.openSeq;
	const urls = [];
	let mounted = false;
	setBusy(true);
	try {
		const page = await callWASM("ofdgoPreviewImage", item.index, item.id);
		if (openSeq !== state.openSeq || !item.surface.isConnected) return;
		const images = new Map(state.svgImages);
		for (const image of page.images) {
			if (images.has(image.name)) continue;
			const url = URL.createObjectURL(new Blob([image.bytes], { type: image.mime }));
			urls.push(url);
			images.set(image.name, { url });
		}
		await Promise.all(urls.map(url => {
			const image = new Image();
			image.src = url;
			return image.decode();
		}));
		if (openSeq !== state.openSeq || canvasEditor.selected !== item) return;
		const svg = parseSVG(page.svg, `crop${openSeq}-${item.id}`, images);
		svg.classList.add("ofd-svg");
		canvasEditor.startCrop(item, { svg, urls });
		mounted = true;
	} catch (err) {
		if (openSeq === state.openSeq) showError(err, false);
	} finally {
		if (!mounted) urls.forEach(url => URL.revokeObjectURL(url));
		if (openSeq === state.openSeq) setBusy(false);
	}
}

function openInsertPanel(item = null) {
	if (!state.editing || document.body.hasAttribute("aria-busy")) {
		return;
	}
	el.insertForm.reset();
	state.insertObject = item;
	el.insertFitRow.hidden = !item;
	el.insertPanel.setAttribute("aria-label", item ? "替换图片" : "添加图片");
	el.insertSubmit.textContent = item ? "替换" : "添加";
	el.insertStatus.textContent = "";
	el.insertPanel.showModal();
	el.insertImage.focus();
}

function currentTextStyle() {
	const text = selectedText(canvasEditor.selected) || state.textDefaults;
	return canvasEditor.input?.style ? { ...text, ...canvasEditor.input.style } : text;
}

function textIndents(item) {
	return [item.leftIndent || 0, item.rightIndent || 0, item.firstLineIndent || 0];
}

async function toggleTextTool() {
	if (!state.editing || !pageCan("insert") || document.body.hasAttribute("aria-busy")) return;
	if (canvasEditor.tool === "text") {
		canvasEditor.setTool("");
		return;
	}
	const openSeq = state.openSeq;
	if (!state.textFonts.length) {
		await requestLocalFontsBeforeOpen();
		updateTextFonts(canvasEditor.selected, true);
	}
	if (openSeq !== state.openSeq) return;
	const style = !canvasEditor.selected?.items && canEditObject(canvasEditor.selected, "layoutKnown") ? currentTextStyle() : state.textDefaults;
	const font = state.textFonts[Number(fontPicker.value)];
	state.textDefaults = { type: "TextObject", size: style.size, color: style.color, wrap: style.wrap,
		wrapOverride: style === state.textDefaults ? style.wrapOverride : style.wrap,
		align: style.align || "left", paragraphHeight: style.paragraphHeight || 0, letterSpacing: style.letterSpacing || 0,
		leftIndent: style.leftIndent || 0, rightIndent: style.rightIndent || 0, firstLineIndent: style.firstLineIndent || 0,
		fontChoice: font && !font.disabled ? font : state.textDefaults.fontChoice };
	state.selectObjects = true;
	setPan(false);
	canvasEditor.setTool("text");
	el.viewerPanel.focus({ preventScroll: true });
}

async function beginCanvasText(index, box, surface, page) {
	const font = state.textDefaults.fontChoice || state.textFonts[Number(fontPicker.value)];
	if (!font || font.disabled) {
		setStatus("尚未添加字体");
		el.textFontAdd.focus();
		return;
	}
	const openSeq = state.openSeq;
	setBusy(true);
	try {
		const data = await readTextFont(font);
		const face = new FontFace("ofdgo-draft", data);
		await face.load();
		if (openSeq !== state.openSeq || !surface.isConnected) return;
		const node = document.createElement("div");
		const item = { ...state.textDefaults, ...box, wrap: state.textDefaults.wrapOverride ?? box.wrap ?? state.textDefaults.wrap,
			index, page, surface, node, text: "", draft: true, fontData: data };
		canvasEditor.editText(item, face);
	} catch (err) {
		if (openSeq === state.openSeq) showError(err, false);
	} finally {
		if (openSeq === state.openSeq) setBusy(false);
	}
}

function editorFonts(item) {
	const fonts = fontManager.editorFonts();
	if (item) {
		fonts.unshift({ id: `embedded:${item.font}`, name: item.fontName || item.font, embedded: true, disabled: !canEditObject(item, "update") });
	}
	return fonts;
}

async function loadEditorFonts() {
	const tasks = [];
	if (!state.fontCatalogLoading && fontManager.canReadLocal() && !fontManager.catalogLoaded && fontManager.permission !== "denied") {
		state.fontCatalogLoading = true;
		tasks.push((async () => {
			try {
				await fontManager.queryLocal();
				refreshEditorFonts();
			} catch (err) {
				setStatus(err?.name === "NotAllowedError" ? "字体尚未授权" : String(err.message || err));
			} finally {
				state.fontCatalogLoading = false;
			}
		})());
	}
	if (!state.fontFacesLoading && fontManager.userFonts.some(font => font.enabled && !font.faces)) {
		state.fontFacesLoading = true;
		tasks.push((async () => {
			try {
				await fontManager.loadFaces(data => callWASM("ofdgoFontFaces", data));
				refreshEditorFonts();
			} finally {
				state.fontFacesLoading = false;
			}
		})());
	}
	await Promise.all(tasks);
}

function refreshEditorFonts() {
	const open = fontPicker.open;
	const selected = state.textFonts[Number(fontPicker.value)];
	const query = el.textFont.value === (selected?.fullName || selected?.name || "") ? "" : el.textFont.value;
	updateTextFonts(canvasEditor.selected, true);
	if (open && !el.textFont.disabled) { if (query) el.textFont.value = query; fontPicker.show(query, false); }
}

async function readTextFont(font) {
	if (font.embedded) return (await callWASM("ofdgoEditorFont", font.id.slice(9))).bytes;
	if (font.file) return (await callWASM("ofdgoFontFace", await fontManager.read(font.file), font.index)).bytes;
	return fontManager.read(font);
}

function updateTextFonts(item, refresh = false) {
	item = selectedText(item);
	const id = item?.type === "TextObject" && !item.draft ? item.font : null;
	const key = item?.items ? `selection:${item.items.map(member => member.id).join(",")}:${id || ""}` : id;
	if (refresh || state.textFontID !== key) {
		state.textFontID = key;
		state.textFonts = editorFonts(id ? item : null);
		const selected = canvasEditor.input?.fontChoice || (id ? state.textFonts[0] : state.textDefaults.fontChoice);
		if (selected && (canvasEditor.input || selected.embedded)
			&& !state.textFonts.some(font => (font.id || font.postscriptName) === (selected.id || selected.postscriptName))) {
			state.textFonts.unshift(selected);
		}
		fontPicker.setFonts(state.textFonts, item?.items && !id ? null : selected || state.textFonts[0]);
		el.textFont.placeholder = item?.items && !id ? "混合" : "";
		if (!id && !item?.items) state.textDefaults.fontChoice = state.textFonts[Number(fontPicker.value)];
	}
}

async function changeTextFont() {
	const item = selectedText(canvasEditor.selected);
	const font = state.textFonts[Number(fontPicker.value)];
	if (!state.ready || document.body.hasAttribute("aria-busy") || !font || font.disabled) return;
	const editing = canvasEditor.input;
	if (editing) {
		const openSeq = state.openSeq;
		setBusy(true);
		try {
			const data = await readTextFont(font);
			await callWASM("ofdgoCheckTextFont", data, editTextValue(editing.input));
			const face = new FontFace("ofdgo-edit-font", data);
			await face.load();
			if (openSeq !== state.openSeq || canvasEditor.input !== editing) return;
			editing.fontChoice = font;
			if (item.draft) state.textDefaults.fontChoice = font;
			canvasEditor.setTextFont(face, data);
		} catch (err) {
			if (openSeq === state.openSeq) {
				updateTextFonts(canvasEditor.selected, true);
				showError(err, false);
			}
		} finally {
			if (openSeq === state.openSeq) setBusy(false);
		}
	} else if (item?.type === "TextObject") {
		if (canEditObject(item, "replaceFont") && (!font.embedded || item.font !== font.id.slice(9))) {
			await changeDocument("ofdgoStyleText", { ...item, id: (item.items || [item]).map(member => member.id) }, readTextFont(font), 0, null);
			updateTextFonts(canvasEditor.selected, true);
		}
	} else {
		state.textDefaults.fontChoice = font;
	}
}

async function insertObject(event) {
	event.preventDefault();
	if (!state.editing || document.body.hasAttribute("aria-busy") || el.insertSubmit.disabled) {
		return;
	}
	let openSeq = state.openSeq;
	const item = state.insertObject;
	const index = item?.index ?? state.pageIndex;
	const page = state.doc.pages[index];
	const x = Math.min(20, page.width / 10);
	const y = Math.min(20, page.height / 10);
	el.insertStatus.textContent = "";
	setBusy(true);
	try {
		const data = new Uint8Array(await el.insertImage.files[0].arrayBuffer());
		if (openSeq !== state.openSeq) {
			return;
		}
		const doc = item
			? await callWASM("ofdgoReplaceImage", item.index, item.id, data, el.insertFit.value)
			: await callWASM("ofdgoInsertImage", index, data, x, y, page.width - x * 2);
		if (openSeq !== state.openSeq) {
			return;
		}
		el.insertPanel.close();
		if (item && doc.revision === state.editorInfo?.revision) {
			el.viewerPanel.focus({ preventScroll: true });
			return;
		}
		setEditorInfo(doc);
		canvasEditor.pendingSelection = item ? null : { index };
		state.selectObjects = true;
		setPan(false);
		openSeq = ++state.openSeq;
		await refreshEditorPage(doc, index, openSeq);
		if (openSeq === state.openSeq) {
			el.viewerPanel.focus({ preventScroll: true });
		}
	} catch (err) {
		if (openSeq === state.openSeq) {
			if (el.insertPanel.open) {
				el.insertStatus.textContent = err.message;
			} else {
				showError(err, false);
			}
		}
	} finally {
		if (openSeq === state.openSeq) {
			setBusy(false);
		}
	}
}

function displayPoints(length) {
	return Number((length * 72 / 25.4).toPrecision(6));
}

function inputMillimeters(input, original) {
	const points = Number(input.value);
	return points === displayPoints(original) ? original : points * 25.4 / 72;
}

async function changeParagraph(wrap) {
	const item = currentTextStyle();
	const explicitWrap = wrap !== undefined;
	wrap ??= item.wrap;
	if (document.body.hasAttribute("aria-busy") || item.draft) return;
	if (!el.textLineHeight.checkValidity()) {
		updateObjectControls(item, true);
		return;
	}
	const height = inputMillimeters(el.textLineHeight, item.paragraphHeight || 0);
	if (!el.textSpacing.checkValidity()) {
		updateObjectControls(canvasEditor.selected, true);
		return;
	}
	const spacing = inputMillimeters(el.textSpacing, item.letterSpacing || 0);
	if (item === state.textDefaults) {
		Object.assign(item, { wrap: Boolean(wrap), align: el.textAlign.value, paragraphHeight: height, letterSpacing: spacing });
		if (explicitWrap) item.wrapOverride = Boolean(wrap);
		updateObjectControls(canvasEditor.selected, true);
		return;
	}
	if (!confirmTextReflow(item)) {
		updateObjectControls(item, true);
		return;
	}
	await changeDocument("ofdgoLayoutText", item, null, null, Boolean(wrap), el.textAlign.value, height, spacing, ...textIndents(item));
	updateObjectControls(canvasEditor.selected, true);
}

async function changeTextStyle(color) {
	const item = currentTextStyle();
	if (!state.ready || state.exporting || document.body.hasAttribute("aria-busy")) {
		return;
	}
	if (!color && !el.textSize.checkValidity()) {
		showError(new Error("字号无效"), false);
		updateObjectControls(item, true);
		return;
	}
	const size = color ? item.size : inputMillimeters(el.textSize, item.size);
	const fill = color && el.textColor.value !== item.color ? el.textColor.value : null;
	if (canvasEditor.input) {
		canvasEditor.setTextStyle(color ? { color: el.textColor.value } : { size });
		updateObjectControls(canvasEditor.selected, true);
		return;
	}
	if (item.items) {
		if (color ? !canEditObject(item, "update") : !canEditObject(item, "layoutKnown") || !canEditObject(item, "reflow")) return;
		if (!color && !el.textSize.value) return;
		if (size !== item.size || fill !== null) {
			await changeDocument("ofdgoStyleText", { ...item, id: item.items.map(member => member.id) }, null, color ? 0 : size, fill);
		}
		updateObjectControls(canvasEditor.selected, true);
		return;
	}
	if (item === state.textDefaults) {
		item.size = size;
		if (fill !== null) item.color = fill;
		updateObjectControls(canvasEditor.selected, true);
		return;
	}
	if (size !== item.size || fill !== null) {
		if (color ? !canEditObject(item, "update") : !confirmTextReflow(item)) {
			updateObjectControls(item, true);
			return;
		}
		await changeDocument("ofdgoUpdateText", item, color ? null : item.text, null, size, fill);
	}
	updateObjectControls(canvasEditor.selected, true);
}

function shapeStyle() {
	return { fill: el.shapeFill.checked, fillColor: el.shapeFillColor.value,
		stroke: el.shapeStroke.checked, strokeColor: el.shapeStrokeColor.value,
		lineWidth: Number(el.shapeWidth.value) * 25.4 / 72 };
}

async function changeShapeStyle() {
	const item = canvasEditor.selected;
	if (!state.editing || document.body.hasAttribute("aria-busy")) {
		return;
	}
	if (!el.shapeWidth.checkValidity() || Number(el.shapeWidth.value) <= 0) {
		el.shapeWidth.value = String(item?.type === "PathObject" ? displayPoints(item.lineWidth) : 1);
		return;
	}
	if (!el.shapeFill.checked && !el.shapeStroke.checked) {
		el.shapeStroke.checked = true;
	}
	const style = shapeStyle();
	if (item?.type === "PathObject") {
		await changeDocument("ofdgoUpdatePathStyle", item, style.fill, style.fillColor, style.stroke, style.strokeColor,
			inputMillimeters(el.shapeWidth, item.lineWidth));
		updateObjectControls(canvasEditor.selected, true);
	} else {
		el.shapeWidth.value = String(Number(el.shapeWidth.value));
	}
	updateDrawingControls();
}

function toggleDrawingTool(tool) {
	if (!state.editing || document.body.hasAttribute("aria-busy")) return;
	const next = canvasEditor.tool === tool ? "" : tool;
	state.selectObjects = true;
	setPan(false);
	canvasEditor.setTool(next);
	el.viewerPanel.focus({ preventScroll: true });
}

function updateDrawingControls() {
	const tool = canvasEditor.tool;
	el.insertTextButton.setAttribute("aria-pressed", String(tool === "text"));
	const line = lineShape(tool) || !tool && lineShape(canvasEditor.selected?.shape);
	if (line) {
		el.shapeFill.checked = false;
		el.shapeStroke.checked = true;
	}
	const disabled = !state.editing || !state.ready || state.exporting;
	el.arrowTool.disabled = disabled || !pageCan("insert");
	for (const [button, name] of [[el.drawLineButton, "line"], [el.drawRectangleButton, "rectangle"], [el.drawEllipseButton, "ellipse"]]) {
		button.disabled = disabled || !pageCan("insert");
		button.setAttribute("aria-pressed", String(tool === name));
	}
	el.eraseButton.disabled = el.eraseMode.disabled = disabled;
	el.eraseButton.setAttribute("aria-pressed", String(tool.startsWith("erase-")));
	el.selectObjectButton.setAttribute("aria-pressed", String(canvasEditor.enabled && !tool));
	const styleDisabled = disabled || (canvasEditor.selected?.type === "PathObject"
		? !canEditObject(canvasEditor.selected, "update") : !pageCan("insert"));
	el.shapeFill.disabled = el.shapeStroke.disabled = styleDisabled || line;
	el.shapeFillColor.disabled = styleDisabled || line || !el.shapeFill.checked;
	el.shapeStrokeColor.disabled = el.shapeWidth.disabled = styleDisabled || !line && !el.shapeStroke.checked;
}

async function changeDocument(name, item, ...args) {
	if (!state.editing || document.body.hasAttribute("aria-busy")) {
		return;
	}
	let openSeq = state.openSeq;
	const active = document.activeElement;
	const toolbarFocus = el.editorTools.contains(active) ? active : null;
	const previous = currentPageInfo();
	const { scrollLeft, scrollTop } = el.viewerPanel;
	setBusy(true);
	try {
		args = await Promise.all(args);
		if (openSeq !== state.openSeq) {
			return;
		}
		const doc = await callWASM(name, ...(item ? [item.index, item.id] : []), ...args);
		if (openSeq !== state.openSeq) {
			return;
		}
		if (doc.revision === state.editorInfo?.revision) {
			return true;
		}
		if ((name === "ofdgoUndo" || name === "ofdgoRedo") && JSON.stringify(doc.outlines) !== JSON.stringify(state.doc.outlines)) {
			state.outlineSelection = null;
			state.outlineExpanded.clear();
		}
		setEditorInfo(doc);
		if (name === "ofdgoUpdateInfo") {
			Object.assign(state.doc, { title: doc.title, author: doc.author, subject: doc.subject });
			renderMeta();
			updateControls();
			return true;
		}
		if (name === "ofdgoChangeOutline" || name === "ofdgoMoveOutline") {
			remapOutlineExpansion(name, args, doc.outlinePath);
			state.doc.outlines = doc.outlines;
			state.outlineSelection = doc.outlinePath;
			renderOutlines(false);
			showNavigation(el.outlinesTab);
			updateControls();
			return true;
		}
		const clearSelection = !item || ["ofdgoDeleteObject", "ofdgoDeleteObjects", "ofdgoEraseObjects", "ofdgoEraseObjectsPath"].includes(name);
		if (name === "ofdgoCopyObjects" || name === "ofdgoPasteObjects" || name === "ofdgoInsertShape" || name === "ofdgoInsertText" || name === "ofdgoInsertImage") {
			state.selectObjects = true;
			canvasEditor.setTool("");
			setPan(false);
			canvasEditor.pendingSelection = { index: item?.index ?? args[0], ids: doc.selectedIDs };
		}
		openSeq = ++state.openSeq;
		if (item || name === "ofdgoPasteObjects" || name === "ofdgoInsertShape" || name === "ofdgoInsertText" || name === "ofdgoInsertImage") {
			await refreshEditorPage(doc, item ? item.index : args[0], openSeq, clearSelection);
		} else {
			const samePage = doc.pages.findIndex((page) => page.id === previous.id);
			const pageIndex = doc.pageIndex ?? (samePage < 0 ? Math.min(state.pageIndex, doc.pageCount - 1) : samePage);
			const page = doc.pages[pageIndex];
			const keepScroll = doc.pageIndex === undefined && page.id === previous.id && pageIndex === state.pageIndex
				&& page.width === previous.width && page.height === previous.height;
			await openDocument({ doc, openSeq, skipAutoFonts: true, pageIndex, fitMode: state.fitMode, scale: state.scale,
				keepPreview: true, clearSelection, previewScroll: keepScroll ? { scrollLeft, scrollTop } : null });
		}
		if (openSeq === state.openSeq) {
			if (!toolbarFocus) el.viewerPanel.focus({ preventScroll: true });
			return true;
		}
	} catch (err) {
		if (openSeq === state.openSeq) {
			if (el.batchPagesPanel.open) {
				el.batchPagesStatus.textContent = err.message;
			} else if (el.objectStylePanel.open) {
				el.objectStyleStatus.textContent = err.message;
			} else if (el.objectBoundsPanel.open) {
				el.objectBoundsStatus.textContent = err.message;
			} else if (el.outlinePanel.open) {
				el.outlineStatus.textContent = err.message;
			} else if (el.infoPanel.open) {
				el.infoStatus.textContent = err.message;
			} else if (el.pagePanel.open) {
				el.pageStatus.textContent = err.message;
			} else if (el.paragraphPanel.open) {
				el.paragraphStatus.textContent = err.message;
			} else if (el.importPanel.open) {
				el.importStatus.textContent = err.message;
			} else {
				showError(err, false);
			}
		}
	} finally {
		if (openSeq === state.openSeq) {
			setBusy(false);
			if (toolbarFocus?.isConnected && !toolbarFocus.disabled) toolbarFocus.focus({ preventScroll: true });
		}
	}
}

async function refreshEditorPage(doc, index, openSeq, clearSelection = false) {
	state.doc = doc;
	resetSearch();
	state.documentSelection = null;
	resetPageLoading();
	try {
		const page = await loadPageData(index, { openSeq, priority: 0, refresh: true });
		if (openSeq !== state.openSeq) {
			return;
		}
		if (clearSelection) {
			const pending = canvasEditor.pendingSelection;
			canvasEditor.clear();
			canvasEditor.pendingSelection = pending;
		}
		mountPageSVG(index, page, openSeq);
		updateThumbnail(index, openSeq);
		setStatus(pageStatus(state.pageIndex, doc.pageCount));
	} catch (err) {
		if (openSeq === state.openSeq) {
			state.pageCache.delete(index);
			canvasEditor.clear();
			const shell = pageShell(index);
			shell.querySelector(".page-surface").replaceChildren();
			shell.classList.remove("rendered");
			markFlowPageError(index, err);
			markThumbnailError(index);
		}
		throw err;
	} finally {
		if (openSeq === state.openSeq) {
			releasePageResources();
			renderMeta();
			updateControls();
			loadDocumentDetails(openSeq);
			for (const page of doc.pages) {
				observeFlowPage(pageShell(page.index), page.index);
				const button = el.pageList.querySelector(`[data-page-index="${page.index}"]`);
				observeThumbnail(button, page.index, openSeq);
			}
		}
	}
}

function releasePageResources() {
	const pages = [...state.pageCache.values()];
	for (const [name, image] of state.svgImages) {
		if (!pages.some((page) => page.imageNames.includes(name))) {
			URL.revokeObjectURL(image.url);
			state.svgImages.delete(name);
		}
	}
	for (const [name, font] of state.svgFonts) {
		if (!pages.some((page) => page.fonts.some((item) => item.name === name))) {
			document.fonts.delete(font);
			state.svgFonts.delete(name);
		}
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
	const openingPages = state.showPages && el.pageListPanel.inert;
	document.body.toggleAttribute("data-hide-pages", !state.showPages);
	document.body.toggleAttribute("data-hide-meta", !state.showMeta);
	el.pageListPanel.inert = !state.showPages;
	el.metaPanel.inert = !state.showMeta;
	el.togglePagesButton.setAttribute("aria-pressed", String(state.showPages));
	el.toggleMetaButton.setAttribute("aria-pressed", String(state.showMeta));
	if (openingPages) {
		updatePageListCurrent(true);
	}
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
			await fontManager.restore();
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
		await fontManager.refreshPermission();
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
		registration = await navigator.serviceWorker.register("./ofdgo_work.js", { updateViaCache: "none" });
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
	const temporaryFonts = fontManager.userFonts.some((font) => font.source === "upload");
	const message = [state.dirty ? "文档尚未保存，刷新将丢失更改" : "刷新后需重新打开文件", temporaryFonts ? "未保存字体需重新添加" : ""].filter(Boolean).join("\n");
	if ((state.ofdBytes || state.doc || temporaryFonts) && !window.confirm(message)) {
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
			window.removeEventListener("beforeunload", warnUnsaved);
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
		const worker = new Worker("./ofdgo_wasm.js");
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
						if (data.stage === "prepare") {
							const label = { fonts: "正在处理字体", pages: "正在处理页面", references: "正在检查引用", resources: "正在整理资源", write: "正在写入" }[data.phase];
							setProgress(label, data.total ? data.completed / data.total * 100 : null);
						} else if (data.completed === data.total) {
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
					const err = new Error(missingGlyphMessage(data.missingGlyphs) || data.error);
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
		setStatus(state.editorInfo ? "引擎中断，未保存内容无法恢复" : "引擎运行中断");
	}
	updateControls();
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
	if (!discardChanges()) {
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
		state.editing = false;
		state.objectClipboard = null;
		state.styleClipboard = null;
		state.editorInfo = null;
		state.fontRenderPending = false;
		state.savedRevision = null;
		canvasEditor.clear();
		setDirty(false);
		el.createPanel.close();
		el.insertPanel.close();
		el.pagePanel.close();
		el.paragraphPanel.close();
		el.infoPanel.close();
		el.importPanel.close();
		el.batchPagesPanel.close();
		el.objectStylePanel.close();
		el.objectBoundsPanel.close();
		el.outlinePanel.close();
		updateControls();
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
			fonts.push(fontManager.record(file.name, new Uint8Array(await file.arrayBuffer()), "upload"));
		}
		const { saved, changed } = await fontManager.add(fonts);
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
	if (!fontManager.canReadLocal()) {
		setStatus("无法读取系统字体");
		return;
	}
	setBusy(true, state.doc ? "正在匹配字体" : "正在请求授权", 12, state.doc ? STATUS.fonts : "正在请求授权");
	try {
		await nextFrame();
		const available = await fontManager.queryLocal();
		if (!state.doc) {
			setStatus(available.length ? `字体授权完成 ${available.length} 个` : "暂无系统字体");
			return;
		}
		if (await loadDocumentLocalFonts(available)) {
			await applyFontChange();
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
	if (!fontManager.canReadLocal() || fontManager.catalogLoaded || fontManager.permission === "denied") {
		return;
	}
	setBusy(true, "正在请求授权", 12, "正在请求授权");
	try {
		const available = await fontManager.queryLocal();
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

async function autoLoadDocumentLocalFonts(openSeq) {
	if (!state.doc?.fonts?.some((font) => !font.embedded) || !fontManager.canReadLocal() || fontManager.permission === "denied") {
		return false;
	}
	setProgress("正在匹配字体", 62, STATUS.fonts);
	try {
		const available = fontManager.catalogLoaded ? fontManager.catalog : await fontManager.queryLocal();
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
		fontManager.localFonts = [];
		setStatus("暂无文档字体");
		updateFontSummary();
		renderFontList();
		return false;
	}
	if (docFonts.every((font) => font.embedded)) {
		fontManager.localFonts = [];
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
		const data = await fontManager.read(item);
		if (openSeq !== state.openSeq) {
			return false;
		}
		fonts.push(fontManager.record(fontManager.localName(item), data, "browser"));
	}
	fontManager.localFonts = fonts;
	setStatus(fonts.length ? `字体加载完成 ${fonts.length} 个` : emptyStatus);
	updateFontSummary();
	renderFontList();
	return fonts.length > 0;
}

async function selectLocalFonts(fonts) {
	const available = new Map();
	for (const font of fonts) {
		const names = [
			fontManager.localName(font),
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
		if (await fontManager.restore()) {
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
	const fonts = fontManager.records();
	const total = fonts.length;
	const enabled = fonts.filter((font) => font.enabled).length;
	el.availableFontSummary.textContent = total ? `${enabled}/${total}` : "0";
}

async function applyFontChange() {
	updateFontSummary();
	renderFontList();
	if (!state.doc) {
		return;
	}
	if (canvasEditor.input || canvasEditor.crop) {
		const openSeq = state.openSeq;
		const fonts = await fontManager.files(fontManager.records());
		if (openSeq !== state.openSeq) return;
		await callWASM("ofdgoConfigure", fonts, state.renderAnnotations);
		if (openSeq !== state.openSeq) return;
		state.fontRenderPending = true;
		setStatus(pageStatus(state.pageIndex, state.doc.pageCount));
		return;
	}
	await refreshFontPreview(true);
}

async function refreshFontPreview(fontsChanged) {
	state.fontRenderPending = false;
	await openDocument({
		pageIndex: state.pageIndex,
		fitMode: state.fitMode,
		scale: state.scale,
		skipAutoFonts: true,
		reuseSession: true,
		fontsChanged,
		keepPreview: Boolean(state.editorInfo),
		previewScroll: { scrollLeft: el.viewerPanel.scrollLeft, scrollTop: el.viewerPanel.scrollTop },
	});
}

async function refreshPendingFonts() {
	if (!state.fontRenderPending || !state.ready || document.body.hasAttribute("aria-busy") || canvasEditor.input || canvasEditor.crop) return;
	await refreshFontPreview(false);
}

async function toggleAnnotations() {
	if (document.body.hasAttribute("aria-busy")) {
		return;
	}
	state.renderAnnotations = !state.renderAnnotations;
	updateAnnotationButton();
	if (!state.doc) {
		return;
	}
	await openDocument({
		pageIndex: state.pageIndex,
		fitMode: state.fitMode,
		skipAutoFonts: true,
		reuseSession: true,
	});
}

async function changeFont(font, changes) {
	if (document.body.hasAttribute("aria-busy")) {
		return;
	}
	setBusy(true, "正在更新字体", null, STATUS.fonts);
	try {
		await fontManager.change(font, changes);
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
	updateTextFonts(canvasEditor.selected, true);
	el.fontList.replaceChildren();
	const fonts = fontManager.records();
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
	const supported = fontManager.canReadLocal();
	el.localFontButton.disabled = !supported;
	el.localFontButton.title = supported ? "读取系统字体" : "无法读取系统字体";
	updateFontPermissionHint();
}

function updateFontPermissionHint() {
	el.fontPermissionHint.hidden = !fontManager.canReadLocal() || fontManager.permission === "granted";
}

async function openDocument(options = {}) {
	if (!state.ofdBytes && !state.editorInfo) {
		return;
	}
	const openSeq = options.openSeq || (state.openSeq += 1);
	const previewIDs = options.keepPreview ? [...new Set([...state.visiblePages, ...state.visibleThumbnails])].map((index) => state.doc.pages[index].id) : [];
	if (!state.ready || state.wasmExited) {
		await ensureWASM();
	}
	if (openSeq !== state.openSeq) {
		return;
	}
	const resetLocalFonts = !options.skipAutoFonts;
	resetSearch();
	if (resetLocalFonts) {
		fontManager.localFonts = [];
		updateFontSummary();
		renderFontList();
	}
	options.keepPreview ? setBusy(true) : setBusy(true, "正在打开文档", 20, STATUS.opening);
	try {
		if (!options.keepPreview) {
			setProgress("正在解析文档", 52);
			await nextFrame();
		}
		if (openSeq !== state.openSeq) {
			return;
		}
		const fonts = !options.doc && (!options.reuseSession || options.fontsChanged)
			? await fontManager.files(resetLocalFonts ? fontManager.userFonts : fontManager.records()) : null;
		if (openSeq !== state.openSeq) {
			return;
		}
		const doc = options.doc || (options.reuseSession
			? await callWASM("ofdgoConfigure", fonts, state.renderAnnotations)
			: await callWASM("ofdgoOpen", state.ofdBytes, fonts, state.renderAnnotations));
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
		if (options.keepPreview) {
			resetPageLoading();
			state.pageCache.clear();
			const indices = new Set([pageIndex, ...doc.pages.filter((page) => previewIDs.includes(page.id)).map((page) => page.index)]);
			for (const index of indices) {
				await loadPageData(index, { openSeq, priority: 0, refresh: true });
				if (openSeq !== state.openSeq) {
					return;
				}
			}
		}
		if (options.clearSelection) canvasEditor.clear();
		resetPageFlow(options.keepPreview);
		renderPageList();
		if (options.keepPreview || options.resetScroll || el.outlineList.childElementCount === 0) {
			renderOutlines(!options.keepPreview);
		}
		renderMeta();
		renderPageFlow();
		if (options.keepPreview) {
			for (const [index, page] of state.pageCache) {
				mountPageSVG(index, page, openSeq);
			}
			releasePageResources();
		}
		if (options.resetScroll) {
			el.viewerPanel.scrollLeft = 0;
			el.viewerPanel.scrollTop = 0;
			el.navigationContent.scrollLeft = 0;
			el.navigationContent.scrollTop = 0;
			el.metaPanel.scrollLeft = 0;
			el.metaPanel.scrollTop = 0;
		}
		applyFit(false);
		if (options.keepPreview) {
			if (options.previewScroll) {
				Object.assign(el.viewerPanel, options.previewScroll);
			} else {
				scrollToPage(pageIndex);
			}
			setStatus(pageStatus(pageIndex, pageCount));
		}
		await nextFrame();
		if (openSeq !== state.openSeq) {
			return;
		}
		const page = options.keepPreview ? state.pageCache.get(pageIndex)
			: await renderPage(pageIndex, { keepBusy: true, scroll: false, openSeq });
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
	if (!state.doc || (state.editorInfo && document.body.hasAttribute("aria-busy") && !options.keepBusy)) {
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

async function exportFile(whole, indices = null, value = el.exportFormat.value) {
	if (!state.doc || document.body.hasAttribute("aria-busy")) {
		return;
	}
	const saving = value === "ofd";
	if (saving && !state.editorInfo) {
		return;
	}
	const format = saving ? { value: "ofd", label: "OFD", extension: "ofd", mime: "application/ofd" } : exportFormatInfo(value);
	if (!format) {
		return;
	}
	const archive = whole && !saving && format.value !== "pdf" && format.value !== "txt";
	const label = archive ? "ZIP" : format.label;
	const extension = archive ? "zip" : format.extension;
	const mime = archive ? "application/zip" : format.mime;
	let openSeq = state.openSeq;
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
		if (canvasEditor.input || canvasEditor.crop || canvasEditor.nudge || canvasEditor.nudgeCommit) {
			setBusy(false);
			if (!await canvasEditor.commitNudge()) return;
			if (!await canvasEditor.commitText()) return;
			if (!await canvasEditor.commitCrop()) return;
			openSeq = state.openSeq;
			setBusy(true, `正在生成 ${label}`, null, whole ? STATUS.exporting : STATUS.pageExporting);
		}
		const result = saving ? await callWASM("ofdgoSaveDocument", file) : whole
			? await callWASM("ofdgoExportDocument", format.value, dpi, indices, file)
			: await callWASM("ofdgoExportPage", pageIndex, format.value, dpi, file);
		if (openSeq !== state.openSeq) {
			return;
		}
		if (result.blob) {
			downloadBytes(result.blob, result.mime, fileName);
		}
		if (saving) {
			state.savedRevision = state.editorInfo.revision;
			setDirty(false);
		}
		setStatus(`${result.label} ${saving ? "保存" : "导出"}完成 ${formatBytes(result.size)}`);
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

function resetPageFlow(keepCache = false) {
	if (!keepCache) {
		state.pageCache.clear();
		releasePageResources();
	}
	state.selectedPages.clear();
	state.documentSelection = null;
	state.visiblePages.clear();
	resetPageLoading();
}

function resetPageLoading() {
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
	const range = !selection.isCollapsed && selection.rangeCount ? selection.getRangeAt(0) : null;
	if (canvasEditor.input?.item.index === index || canvasEditor.crop?.item.index === index
		|| range && (!state.documentSelection || !isDocumentSelection(range)) && range.intersectsNode(shell)) {
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
	const key = `${openSeq}:${index}`;
	const priority = options.priority ?? 3;
	const current = state.pageInFlight.get(key);
	if (current) {
		current.priority = Math.min(current.priority, priority);
		return current.promise;
	}
	if (!options.refresh && state.pageCache.has(index)) {
		const page = state.pageCache.get(index);
		state.pageCache.delete(index);
		state.pageCache.set(index, page);
		return Promise.resolve(page);
	}
	let resolve;
	let reject;
	const promise = new Promise((done, fail) => {
		resolve = done;
		reject = fail;
	});
	const task = { key, index, openSeq, priority, refresh: options.refresh, resolve, reject, promise };
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
				if (!task.refresh && state.pageCache.has(task.index)) {
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
					if (task.refresh && state.editing) {
						await Promise.all(page.imageNames.map(name => {
							const image = new Image();
							image.src = state.svgImages.get(name).url;
							return image.decode();
						}));
					}
					if (task.openSeq !== state.openSeq) {
						task.resolve(null);
						continue;
					}
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
	if (state.editorInfo) {
		canvasEditor.mount(index, page, surface);
	}
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
		let end = 0;
		for (const item of run.spans || []) {
			const breaks = chars.slice(end, item.start).filter((char) => char === "\n").join("");
			if (breaks) {
				line.append(document.createTextNode(breaks));
			}
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
			end = item.end;
		}
		const breaks = chars.slice(end).filter((char) => char === "\n").join("");
		if (breaks) {
			line.append(document.createTextNode(breaks));
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

function copyEditorSelection(event) {
	if (!state.editing || !canvasEditor.enabled || !canvasEditor.selected || !el.viewerPanel.contains(event.target)
		|| canvasEditor.input || canvasEditor.crop || canvasEditor.drag || document.body.hasAttribute("aria-busy")) return false;
	const items = canvasEditor.items().slice().sort((a, b) => a.order - b.order);
	const cut = event.type === "cut";
	if (!canEditObject(canvasEditor.selected, "copy") || cut && !canEditObject(canvasEditor.selected, "delete")) {
		event.preventDefault();
		setStatus(cut ? "对象不可剪切" : "对象不可复制");
		return true;
	}
	const index = items[0].index, ids = items.map(item => item.id);
	const clipboard = { token: crypto.randomUUID(), page: state.doc.pages[index].id, bounds: selectionBounds(items), x: 0, y: 0, cut };
	if (canvasEditor.nudge) {
		clipboard.bounds.x += canvasEditor.nudge.x;
		clipboard.bounds.y += canvasEditor.nudge.y;
	}
	event.clipboardData.setData(OBJECT_CLIPBOARD_TYPE, clipboard.token);
	event.clipboardData.setData("text/plain", items.map(item => item.text || "").filter(Boolean).join("\n"));
	event.preventDefault();
	state.objectClipboard = clipboard;
	const capture = () => {
		const openSeq = state.openSeq, revision = state.editorInfo.revision;
		return callWASM("ofdgoCaptureObjects", index, ids, clipboard.token).then(async () => {
			clipboard.cut = false;
			if (cut && openSeq === state.openSeq && revision === state.editorInfo?.revision && index === state.pageIndex && state.objectClipboard === clipboard) {
				clipboard.cut = Boolean(await changeDocument("ofdgoDeleteObjects", { index, id: ids }));
			}
			return true;
		}, err => {
			if (state.objectClipboard === clipboard) showError(err, false);
			return false;
		});
	};
	clipboard.ready = canvasEditor.nudge ? canvasEditor.commitNudge().then(saved => saved ? capture() : false) : capture();
	return true;
}

function visiblePageBounds(index) {
	const page = state.doc.pages[index], surface = pageShell(index)?.querySelector(".page-surface");
	const full = { x: 0, y: 0, width: page.width, height: page.height };
	if (!surface) return full;
	const rect = surface.getBoundingClientRect(), view = el.viewerPanel.getBoundingClientRect();
	const left = Math.max(rect.left, view.left), top = Math.max(rect.top, view.top);
	const right = Math.min(rect.right, view.left + el.viewerPanel.clientWidth), bottom = Math.min(rect.bottom, view.top + el.viewerPanel.clientHeight);
	if (right <= left || bottom <= top) return full;
	const a = pagePoint(left, top, rect, page, state.rotation), b = pagePoint(right, bottom, rect, page, state.rotation);
	return { x: Math.min(a.x, b.x), y: Math.min(a.y, b.y), width: Math.abs(a.x - b.x), height: Math.abs(a.y - b.y) };
}

async function pasteEditorContent(event) {
	if (!state.editing || event.target.closest?.("input, textarea, select, [contenteditable]") || formDialogOpen()) return;
	event.preventDefault();
	const token = event.clipboardData.getData(OBJECT_CLIPBOARD_TYPE);
	const files = Array.from(event.clipboardData.files);
	const value = event.clipboardData.getData("text/plain").replace(/\t/g, "    ");
	if ((canvasEditor.nudge || canvasEditor.nudgeCommit) && !await canvasEditor.commitNudge()) return;
	if (document.body.hasAttribute("aria-busy") || canvasEditor.input || canvasEditor.crop || canvasEditor.drag || !pageCan("insert")) return;
	const index = state.pageIndex, page = state.doc.pages[index];
	if (token) {
		const clipboard = state.objectClipboard;
		if (!clipboard || token !== clipboard.token) {
			setStatus("对象剪贴板失效");
			return;
		}
		const openSeq = state.openSeq;
		if (!await clipboard.ready || openSeq !== state.openSeq || index !== state.pageIndex || state.objectClipboard !== clipboard) return;
		const visible = visiblePageBounds(index), bounds = clipboard.bounds;
		const offset = clipboard.page === page.id ? { x: clipboard.x + (clipboard.cut ? 0 : 3), y: clipboard.y + (clipboard.cut ? 0 : 3) } : { x: 0, y: 0 };
		for (const [axis, size] of [["x", "width"], ["y", "height"]]) {
			const position = bounds[axis] + offset[axis];
			if (position < visible[axis] || position + bounds[size] > visible[axis] + visible[size]) {
				const margin = Math.min(20, visible[size] / 10);
				offset[axis] = Math.max(visible[axis] + margin, Math.min(position, visible[axis] + visible[size] - margin - bounds[size])) - bounds[axis];
			}
		}
		if (await changeDocument("ofdgoPasteObjects", null, index, token, offset.x, offset.y)) {
			clipboard.page = page.id;
			clipboard.x = offset.x;
			clipboard.y = offset.y;
			clipboard.cut = false;
		}
		return;
	}
	const visible = visiblePageBounds(index), margin = Math.min(20, visible.width / 10);
	const x = visible.x + margin, y = visible.y + Math.min(20, visible.height / 10), width = visible.width - margin * 2;
	if (files.length) {
		if (files.length !== 1) {
			setStatus("一次粘贴一张图片");
			return;
		}
		if (!/^image\/(png|jpeg)$/.test(files[0].type)) {
			setStatus("图片仅支持 PNG、JPG");
			return;
		}
		await changeDocument("ofdgoInsertImage", null, index, files[0].arrayBuffer().then(bytes => new Uint8Array(bytes)), x, y, width);
		return;
	}
	if (!value.trim()) return;
	const font = state.textFonts[Number(fontPicker.value)];
	if (!font || font.disabled) {
		setStatus("尚未添加字体");
		el.textFontAdd.focus();
		return;
	}
	const style = canvasEditor.selected?.items ? state.textDefaults : currentTextStyle();
	const data = readTextFont(font);
	await changeDocument("ofdgoInsertText", null, index, value, data, x, y, style.size, style.color,
		width, style.wrap, style.align || "left", style.paragraphHeight || 0, style.letterSpacing || 0, ...textIndents(style));
}

function copySelection(event) {
	if (event.target.closest?.("input, textarea, [contenteditable]")) {
		return;
	}
	const selection = document.getSelection();
	if (selection.isCollapsed && copyEditorSelection(event)) return;
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

function updatePageListCurrent(force = false) {
	const current = el.pageList.querySelector(".page-list-item[aria-current]");
	if (current) {
		if (!force && Number.parseInt(current.dataset.pageIndex, 10) === state.pageIndex) {
			return;
		}
		current.removeAttribute("aria-current");
	}
	const next = el.pageList.querySelector(`.page-list-item[data-page-index="${state.pageIndex}"]`);
	if (next) {
		setPageItemCurrent(next, true);
		if (state.showPages && !el.pageList.hidden) {
			const panel = el.navigationContent.getBoundingClientRect();
			const item = next.getBoundingClientRect();
			if (item.top < panel.top) {
				el.navigationContent.scrollTop += item.top - panel.top;
			} else if (item.bottom > panel.bottom) {
				el.navigationContent.scrollTop += Math.min(item.top - panel.top, item.bottom - panel.bottom);
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
	canvasEditor.cancel();
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
		surface.style.setProperty("--surface-scale", String(scale));
		surface.classList.toggle("sideways", state.rotation % 180 !== 0);
	}
}

function parseSVG(svgText, prefix = "", images = state.svgImages) {
	const template = document.createElement("template");
	template.innerHTML = svgText.replace(/xlink:href="(ofdgo-image-[a-f0-9]{64})"/g, (_, name) => `xlink:href="${images.get(name).url}"`);
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
	const scrollTop = el.navigationContent.scrollTop;
	for (const [tab, panel] of [[el.pagesTab, el.pageList], [el.outlinesTab, el.outlineList], [el.searchTab, el.searchPanel]]) {
		if (tab.getAttribute("aria-selected") === "true") {
			state.navigationScroll.set(tab, scrollTop);
		}
		const active = tab === selected;
		panel.hidden = !active;
		tab.setAttribute("aria-selected", String(active));
		tab.tabIndex = active ? 0 : -1;
	}
	el.navigationContent.scrollTop = state.navigationScroll.get(selected) || 0;
}

function renderOutlines(reset = true) {
	const selected = !reset && [el.pagesTab, el.outlinesTab, el.searchTab].find(tab => tab.getAttribute("aria-selected") === "true");
	const scrollTop = reset ? 0 : el.navigationContent.scrollTop;
	if (reset) {
		state.navigationScroll.clear();
		state.outlineExpanded.clear();
		state.outlineSelection = null;
	}
	const outlines = state.doc.outlines || [];
	el.pageListTitle.hidden = true;
	el.navigationTabs.hidden = false;
	el.outlinesTab.hidden = outlines.length === 0 && !state.editing;
	el.outlineList.replaceChildren();
	if (state.editing) {
		const tools = document.createElement("div");
		tools.className = "outline-tools";
		for (const [action, label] of [["add", "新增"], ["update", "修改"], ["move", "移动"], ["delete", "删除"]]) {
			const button = document.createElement("button");
			button.className = "button";
			button.type = "button";
			button.textContent = label;
			button.dataset.outlineAction = action;
			button.disabled = action !== "add" && !state.outlineSelection?.length;
			editorClick(button, () => openOutlinePanel(action));
			tools.append(button);
		}
		el.outlineList.append(tools);
	}
	if (outlines.length > 0) {
		el.outlineList.append(createOutlineList(outlines));
	}
	el.navigationContent.scrollTop = scrollTop;
	showNavigation(selected && !selected.hidden ? selected : el.pagesTab);
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
	if (state.editing) {
		const node = [...pageShell(match.page).querySelectorAll(".edit-object")].find(node => canvasEditor.nodes.get(node)?.id === match.id);
		const item = node && canvasEditor.nodes.get(node);
		if (item?.type === "TextObject" && canEditObject(item, "update")) {
			state.selectObjects = true;
			setPan(false);
			canvasEditor.setTool("");
			canvasEditor.select(item);
			updateControls();
		} else canvasEditor.select(null);
	}
	const button = el.searchResults.querySelector("[aria-current=true]");
	const row = button.getBoundingClientRect();
	const panel = el.searchResults.getBoundingClientRect();
	if (row.top < panel.top) {
		el.searchResults.scrollTop += row.top - panel.top;
	} else if (row.bottom > panel.bottom) {
		el.searchResults.scrollTop += row.bottom - panel.bottom;
	}
	return true;
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

function createOutlineList(outlines, path = []) {
	const list = document.createElement("ul");
	list.className = "outline-list";
	for (const [index, outline] of outlines.entries()) {
		const current = [...path, index];
		const item = document.createElement("li");
		const hasChildren = outline.children?.length > 0;
		const row = document.createElement(hasChildren ? "summary" : "div");
		if (!hasChildren) {
			row.className = "outline-leaf";
		}
		const link = document.createElement(outline.page || state.editing ? "button" : "span");
		link.className = "outline-link";
		link.classList.toggle("selected", state.editing && JSON.stringify(current) === JSON.stringify(state.outlineSelection));
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
		}
		if (outline.page || state.editing) {
			link.type = "button";
			link.addEventListener("keydown", async event => {
				if (!state.editing || event.defaultPrevented || event.isComposing || !event.altKey || event.ctrlKey || event.metaKey || event.shiftKey
					|| !["ArrowUp", "ArrowDown"].includes(event.key)) return;
				event.preventDefault();
				const target = index + (event.key === "ArrowUp" ? -1 : 1);
				if (event.repeat || document.body.hasAttribute("aria-busy") || target < 0 || target >= outlines.length) return;
				if (await changeDocument("ofdgoMoveOutline", null, current, path, target)) {
					const selected = el.outlineList.querySelector(".outline-link.selected");
					selected?.focus({ preventScroll: true });
					selected?.scrollIntoView({ block: "nearest" });
				}
			});
			link.addEventListener("click", (event) => {
				event.preventDefault();
				if (state.editing) {
					state.outlineSelection = current;
					for (const node of el.outlineList.querySelectorAll(".outline-link")) node.classList.toggle("selected", node === link);
					for (const button of el.outlineList.querySelectorAll("[data-outline-action]")) button.disabled = false;
				}
				if (outline.page) renderPage(outline.page - 1);
			});
		}
		row.append(link);
		if (hasChildren) {
			const details = document.createElement("details");
			const key = JSON.stringify(current);
			details.open = state.outlineExpanded.get(key) ?? outline.expanded;
			details.addEventListener("toggle", () => {
				if (details.isConnected) state.outlineExpanded.set(key, details.open);
			});
			details.append(row, createOutlineList(outline.children, current));
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
		if (state.editing && pageCan("move", page.index)) enablePageDrag(button, page.index);
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

function pageDropIndex(from, target, after) {
	const position = target + Number(after);
	return position > from ? position - 1 : position;
}

function enablePageDrag(button, index) {
	const grip = document.createElement("span");
	grip.className = "thumb-grip";
	grip.title = "拖动排序";
	grip.setAttribute("aria-hidden", "true");
	button.append(grip);
	grip.addEventListener("click", event => event.stopPropagation());
	grip.addEventListener("pointerdown", event => {
		if (event.button !== 0 || document.body.hasAttribute("aria-busy")) return;
		event.preventDefault();
		event.stopPropagation();
		const seq = state.openSeq, pointer = event.pointerId;
		let x = event.clientX, y = event.clientY, target = index, marker = null, frame = 0, dragging = false;
		const clearMarker = () => { marker?.classList.remove("drop-before", "drop-after"); marker = null; };
		const locate = () => {
			clearMarker();
			target = index;
			const node = document.elementFromPoint(x, y)?.closest(".page-list-item");
			if (!node || !el.pageList.contains(node)) return;
			const rect = node.getBoundingClientRect(), after = y > rect.top + rect.height / 2;
			target = pageDropIndex(index, Number(node.dataset.pageIndex), after);
			if (target !== index) { marker = node; marker.classList.add(after ? "drop-after" : "drop-before"); }
		};
		const tick = () => {
			if (seq !== state.openSeq || !state.editing) { finish(false); return; }
			if (dragging) {
				const rect = el.navigationContent.getBoundingClientRect();
				const delta = y < rect.top + 48 ? -10 : y > rect.bottom - 48 ? 10 : 0;
				if (delta) el.navigationContent.scrollTop += delta;
				locate();
			}
			frame = requestAnimationFrame(tick);
		};
		const move = next => {
			if (next.pointerId !== pointer) return;
			x = next.clientX; y = next.clientY;
			dragging ||= Math.hypot(x - event.clientX, y - event.clientY) > 4;
			button.classList.toggle("reordering", dragging);
			if (dragging) locate();
		};
		const finish = async commit => {
			cancelAnimationFrame(frame);
			grip.removeEventListener("pointermove", move);
			grip.removeEventListener("pointerup", up);
			grip.removeEventListener("pointercancel", cancel);
			grip.removeEventListener("lostpointercapture", cancel);
			clearMarker();
			button.classList.remove("reordering");
			if (grip.hasPointerCapture(pointer)) grip.releasePointerCapture(pointer);
			if (commit && dragging && target !== index && seq === state.openSeq && await canvasEditor.commitText() && await canvasEditor.commitCrop()) {
				await changeDocument("ofdgoChangePage", null, "move", index, target);
			}
		};
		const up = next => { if (next.pointerId === pointer) finish(true); };
		const cancel = () => finish(false);
		grip.setPointerCapture(pointer);
		grip.addEventListener("pointermove", move);
		grip.addEventListener("pointerup", up);
		grip.addEventListener("pointercancel", cancel);
		grip.addEventListener("lostpointercapture", cancel);
		frame = requestAnimationFrame(tick);
	});
	button.addEventListener("keydown", async event => {
		if (!event.altKey || !["ArrowUp", "ArrowDown"].includes(event.key) || document.body.hasAttribute("aria-busy")) return;
		event.preventDefault();
		const target = index + (event.key === "ArrowUp" ? -1 : 1);
		if (target >= 0 && target < state.doc.pageCount && await canvasEditor.commitText() && await canvasEditor.commitCrop()) {
			if (await changeDocument("ofdgoChangePage", null, "move", index, target)) el.pageList.querySelector(`[data-page-index="${target}"]`)?.focus();
		}
	});
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
			root: el.navigationContent,
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
		delete thumb.dataset.renderKey;
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
	if (font.matchedFace?.fullName) {
		parts.push(font.matchedFace.fullName);
		if (font.matchedFace.index > 0) parts.push(`索引 ${font.matchedFace.index}`);
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
	setPan(!state.panMode);
}

function setPan(enabled) {
	endPan();
	state.panMode = enabled;
	el.viewerPanel.classList.toggle("pan-mode", state.panMode);
	el.panButton.setAttribute("aria-pressed", String(state.panMode));
	updateEditorTools();
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
	} else {
		updateControls();
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
	updateEditorTools();
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

function updateEditorTools() {
	el.editButton.disabled = !state.doc || !state.ready || state.exporting;
	el.editButton.setAttribute("aria-pressed", String(state.editing));
	el.editButton.title = state.editing ? "阅读" : "编辑";
	el.editButton.setAttribute("aria-label", el.editButton.title);
	el.editNotice.textContent = state.editorInfo?.editWarnings?.join("；") || "";
	el.infoButton.hidden = !state.editing;
	el.editNotice.hidden = !state.editing || !el.editNotice.textContent;
	el.editorTools.hidden = !state.editing;
	const pagesDisabled = !state.editing || !state.ready || state.exporting;
	el.insertTextButton.disabled = el.insertImageButton.disabled = pagesDisabled || !pageCan("insert");
	el.saveButton.disabled = el.addPageButton.disabled = pagesDisabled;
	el.batchPagesButton.disabled = pagesDisabled;
	el.copyPageButton.disabled = pagesDisabled || !pageCan("copy");
	el.pageSettingsButton.disabled = pagesDisabled || !pageCan("resize");
	el.deletePageButton.disabled = pagesDisabled || !pageCan("delete") || state.doc?.pageCount <= 1;
	el.movePagePrevButton.disabled = pagesDisabled || !pageCan("move") || state.pageIndex === 0;
	el.movePageNextButton.disabled = pagesDisabled || !pageCan("move") || state.pageIndex === state.doc?.pageCount - 1;
	const enabled = state.editing && state.selectObjects && !state.panMode;
	canvasEditor.setEnabled(enabled);
	updateObjectControls(canvasEditor.selected);
	el.undoButton.disabled = !state.editorInfo?.canUndo || !state.ready || state.exporting;
	el.redoButton.disabled = !state.editorInfo?.canRedo || !state.ready || state.exporting;
}

function updateObjectControls(item, reset = false) {
	const disabled = !item || Boolean(item.draft) || !state.ready || state.exporting;
	el.deleteObjectButton.disabled = disabled || !canEditObject(item, "delete");
	el.copyObjectButton.disabled = disabled || !canEditObject(item, "copy");
	el.objectStyleButton.disabled = disabled || !canEditObject(item, "transform");
	const cropping = Boolean(canvasEditor.crop);
	el.objectBoundsButton.disabled = disabled || cropping || !canEditObject(item, "transform");
	el.objectAlign.disabled = disabled || cropping || !canEditObject(item, "arrange");
	el.objectRotate.disabled = el.objectFlip.disabled = el.objectAlign.disabled;
	el.cropImageButton.disabled = disabled || item.type !== "ImageObject" || !canEditObject(item, "update");
	el.imageFit.disabled = el.cropImageButton.disabled || cropping;
	el.cropImageButton.textContent = cropping ? "完成" : "裁剪";
	el.cropImageButton.setAttribute("aria-label", cropping ? "完成裁剪" : "裁剪图片");
	el.cropImageButton.setAttribute("aria-pressed", String(cropping));
	el.resetCropButton.textContent = cropping ? "取消" : "还原";
	el.resetCropButton.setAttribute("aria-label", cropping ? "取消裁剪" : "还原图片");
	el.resetCropButton.disabled = el.cropImageButton.disabled || !cropping && (!item.imageBounds || ["x", "y", "width", "height"].every(key => Math.abs(item[key] - item.imageBounds[key]) < 1e-9));
	el.objectDistribute.disabled = el.objectAlign.disabled || !item.items || item.items.length < 3;
	el.editObjectButton.disabled = disabled || Boolean(item.items) || item.type === "PathObject"
		|| !canEditObject(item, item.type === "TextObject" ? "reflow" : "update");
	el.multiSelectButton.disabled = !state.editing || !canvasEditor.enabled || !state.ready || state.exporting;
	const selection = selectedText(item);
	const text = selection ? currentTextStyle() : state.textDefaults;
	const textDisabled = !state.editing || !state.ready || state.exporting || Boolean(item?.items && !selection);
	fontPicker.setDisabled(textDisabled || text !== state.textDefaults && !item.draft && !canEditObject(item, "replaceFont"));
	el.textSize.disabled = textDisabled || text !== state.textDefaults && !item.draft && (!canEditObject(item, "reflow") || Boolean(item?.items) && !canEditObject(item, "layoutKnown"));
	el.textColor.disabled = textDisabled || text !== state.textDefaults && !item.draft && !canEditObject(item, "update");
	el.textFontAdd.disabled = !state.editing || !state.ready || state.exporting;
	el.textAlign.disabled = el.textWrap.disabled = el.textLineHeight.disabled = el.textSpacing.disabled = el.textSize.disabled || Boolean(item?.items || item?.draft) || Boolean(canvasEditor.input);
	el.paragraphButton.disabled = el.textAlign.disabled;
	const layoutKnown = text === state.textDefaults || Boolean(item?.draft) || canEditObject(item, "layoutKnown");
	el.textAlign.value = layoutKnown ? text.align || "left" : "";
	el.textLineHeight.placeholder = layoutKnown ? "自动" : "原文";
	el.textSpacing.placeholder = layoutKnown ? "0" : "原文";
	el.textWrap.setAttribute("aria-pressed", String(Boolean(text.wrap)));
	if (reset || document.activeElement !== el.textLineHeight) {
		el.textLineHeight.value = text.paragraphHeight ? String(displayPoints(text.paragraphHeight)) : "";
	}
	if (reset || document.activeElement !== el.textSpacing) {
		el.textSpacing.value = text.letterSpacing ? String(displayPoints(text.letterSpacing)) : "";
	}
	updateTextFonts(item);
	if (reset || el.textSize.disabled || document.activeElement !== el.textSize) {
		el.textSize.value = text.size === undefined ? "" : displayPoints(text.size);
	}
	el.textSize.placeholder = text.size === undefined ? "混合" : "";
	el.textSize.title = text.size === undefined ? "字号：混合" : `字号 ${displayPoints(text.size)} pt`;
	el.textColor.classList.toggle("mixed-color", Boolean(selection?.items && !text.color));
	el.textColor.title = selection?.items && !text.color ? "文字颜色：混合" : "文字颜色";
	if (reset || el.textColor.disabled || document.activeElement !== el.textColor) {
		el.textColor.value = text.color || "#000000";
	}
	if (item?.type === "PathObject" && canEditObject(item, "update")) {
		el.shapeFill.checked = item.fill;
		el.shapeStroke.checked = item.stroke;
		el.shapeFillColor.value = item.fillColor;
		el.shapeStrokeColor.value = item.strokeColor;
		if (reset || document.activeElement !== el.shapeWidth) {
			el.shapeWidth.value = displayPoints(item.lineWidth);
		}
	}
	updateDrawingControls();
	const members = item?.items || (item ? [item] : []);
	const orders = members.map(member => member.position).sort((a, b) => a - b);
	const count = members[0]?.count || 0;
	const canRaise = orders.some((order, i) => order !== count - orders.length + i);
	const canLower = orders.some((order, i) => order !== i);
	el.objectOrder.disabled = disabled || cropping || count <= orders.length || !canEditObject(item, "order")
		|| members.some(member => member.container !== members[0].container);
	for (const option of el.objectOrder.options) {
		option.disabled = el.objectOrder.disabled || (["up", "top"].includes(option.value) ? !canRaise : !canLower);
	}
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
		if (name === "ofdgoExportPage" || name === "ofdgoExportDocument" || name === "ofdgoExportAttachment" || name === "ofdgoSaveDocument") {
			state.exportRequestID = id;
			el.cancelExportButton.hidden = false;
			el.cancelExportButton.disabled = false;
		}
		try {
			const transfer = [];
			if (name === "ofdgoOpen") {
				args[0] = args[0].slice();
				transfer.push(args[0].buffer);
			}
			if (name === "ofdgoLoadImport" && args[0]) transfer.push(args[0].buffer);
			wasmWorker.postMessage({ id, name, args }, transfer);
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
	el.createForm.inert = busy;
	el.insertForm.inert = busy;
	el.pageForm.inert = busy;
	el.paragraphForm.inert = busy;
	el.infoForm.inert = busy;
	el.importForm.inert = busy;
	el.batchPagesForm.inert = busy;
	el.objectStyleForm.inert = busy;
	el.objectBoundsForm.inert = busy;
	el.outlineForm.inert = busy;
	el.outlineList.inert = busy;
	el.navigationTabs.inert = busy;
	el.pageList.inert = busy;
	el.editorTools.inert = busy;
	el.fontList.inert = busy;
	if (!busy) {
		el.progressPanel.hidden = true;
		if (state.fontSyncPending) {
			window.setTimeout(syncUserFonts, 0);
		}
		if (state.fontRenderPending) window.setTimeout(refreshPendingFonts, 0);
		return;
	}
	el.exportPanel.close();
	endPan();
	el.progressPanel.hidden = !text;
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
