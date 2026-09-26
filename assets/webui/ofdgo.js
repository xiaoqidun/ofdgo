import { CanvasEditor, canEditObject, editTextValue, missingGlyphMessage, objectEditReason, selectedText, lineShape, selectionBounds, pagePoint } from "./ofdgo_edit.js";
import { FontManager, FontPicker } from "./ofdgo_font.js";

const MM_TO_PX = 96 / 25.4;
const COMPACT_LAYOUT = window.matchMedia("(max-width: 900px)");
const DEFAULT_IMAGE_DPI = 300;
const PAGE_CACHE_LIMIT = 16;
const PAGE_CACHE_BYTES = 32 * 1024 * 1024;
const SCROLL_SEGMENT_SIZE = 1000000;
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
const metaContents = new WeakMap();
const batch = { items: [], formats: [], running: false, canceled: false, worker: null, requests: new Map(), sequence: 0, activeID: 0, progressTime: 0 };
const batchElements = Object.fromEntries(["Button", "Panel", "Form", "Input", "Add", "Clear", "Count", "Format", "Destination", "DPIRow", "DPI", "List", "Empty", "Progress", "Status", "Close", "Cancel", "Start"].map(name => [name, document.querySelector(`#convert${name}`)]));

let wasmPromise = null;
let wasmWorker = null;
let wasmRequestID = 0;
let wasmRecoveryTimer = 0;
let textMeasure = null;
let credentialRequest = null;
let cancelPageTouch = null;

const state = {
	conversionWarnings: [],
	composite: null,
	annotationEdit: null,
	annotationCreate: null,
	ready: false,
	wasmExited: false,
	wasmSeq: 0,
	wasmRecovering: false,
	wasmRecoveries: 0,
	exporting: false,
	signing: false,
	signCanceled: false,
	exportRequestID: 0,
	editing: false,
	selectObjects: true,
	dirty: false,
	editorInfo: null,
	editorViews: new Map(),
	savedRevision: null,
	insertObject: null,
	importPageCount: 0,
	importEncrypted: false,
	importing: false,
	pageSelection: new Set(),
	pageSelectionAnchor: null,
	pageMultiSelect: false,
	pageTouchPointer: null,
	outlineSelection: null,
	outlineAction: "add",
	styleOriginal: null,
	objectClipboard: null,
	styleClipboard: null,
	outlineExpanded: new Map(),
	textFonts: [],
	textFontID: null,
	textDefaults: { type: "TextObject", size: 16 * 25.4 / 72, color: "#000000", wrap: true, align: "left", paragraphHeight: 0, letterSpacing: 0 },
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
	renderBackend: "canvas",
	renderMode: "svg",
	renderDPI: DEFAULT_IMAGE_DPI,
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
	pageWindow: null,
	thumbnailWindow: null,
	thumbnailFrame: 0,
	pageDragIndex: null,
	pageListCurrent: null,
	thumbnailInFlight: new Set(),
	thumbnailObserver: null,
	visibleThumbnails: new Set(),
	exportFormats: [],
	exportPages: null,
	exportBackdrop: false,
	exportEncrypted: false,
	showPages: !COMPACT_LAYOUT.matches,
	showMeta: !COMPACT_LAYOUT.matches,
};

const el = {
	signButton: document.querySelector("#signButton"),
	signPanel: document.querySelector("#signPanel"),
	signForm: document.querySelector("#signForm"),
	signCertificate: document.querySelector("#signCertificate"),
	signKey: document.querySelector("#signKey"),
	signKeyPassword: document.querySelector("#signKeyPassword"),
	signRoots: document.querySelector("#signRoots"),
	signIntermediates: document.querySelector("#signIntermediates"),
	signSeal: document.querySelector("#signSeal"),
	signPlacementFields: document.querySelector("#signPlacementFields"),
	signPlacement: document.querySelector("#signPlacement"),
	signPages: document.querySelector("#signPages"),
	signX: document.querySelector("#signX"),
	signY: document.querySelector("#signY"),
	signWidth: document.querySelector("#signWidth"),
	signHeight: document.querySelector("#signHeight"),
	signMode: document.querySelector("#signMode"),
	signLock: document.querySelector("#signLock"),
	signStatus: document.querySelector("#signStatus"),
	signCancel: document.querySelector("#signCancel"),
	verifyButton: document.querySelector("#verifyButton"),
	verifyPanel: document.querySelector("#verifyPanel"),
	verifyForm: document.querySelector("#verifyForm"),
	verifyRoots: document.querySelector("#verifyRoots"),
	verifyCerts: document.querySelector("#verifyCerts"),
	verifyTimeRoots: document.querySelector("#verifyTimeRoots"),
	verifyTokens: document.querySelector("#verifyTokens"),
	verifyCRLs: document.querySelector("#verifyCRLs"),
	verifyOCSP: document.querySelector("#verifyOCSP"),
	verifyRequireTime: document.querySelector("#verifyRequireTime"),
	verifyRequireRevocation: document.querySelector("#verifyRequireRevocation"),
	verifyStatus: document.querySelector("#verifyStatus"),
	verifyCancel: document.querySelector("#verifyCancel"),
	securityPanel: document.querySelector("#securityPanel"),
	securityEncryption: document.querySelector("#securityEncryption"),
	securityMethod: document.querySelector("#securityMethod"),
	securityMethodRow: document.querySelector("#securityMethodRow"),
	encryptSaveButton: document.querySelector("#encryptSaveButton"),
	credentialsPanel: document.querySelector("#credentialsPanel"),
	credentialsType: document.querySelector("#credentialsType"),
	credentialsPasswordRow: document.querySelector("#credentialsPasswordRow"),
	credentialsCertificateRow: document.querySelector("#credentialsCertificateRow"),
	credentialsKeyRow: document.querySelector("#credentialsKeyRow"),
	credentialsCertificate: document.querySelector("#credentialsCertificate"),
	credentialsKey: document.querySelector("#credentialsKey"),
	credentialsKeyPassword: document.querySelector("#credentialsKeyPassword"),
	credentialsKeyPasswordRow: document.querySelector("#credentialsKeyPasswordRow"),
	credentialsForm: document.querySelector("#credentialsForm"),
	credentialsUser: document.querySelector("#credentialsUser"),
	credentialsPassword: document.querySelector("#credentialsPassword"),
	credentialsStatus: document.querySelector("#credentialsStatus"),
	credentialsCancel: document.querySelector("#credentialsCancel"),
	encryptionFields: document.querySelector("#encryptionFields"),
	encryptionType: document.querySelector("#encryptionType"),
	encryptionUserRow: document.querySelector("#encryptionUserRow"),
	encryptionPasswordRow: document.querySelector("#encryptionPasswordRow"),
	encryptionConfirmRow: document.querySelector("#encryptionConfirmRow"),
	encryptionCertificatesRow: document.querySelector("#encryptionCertificatesRow"),
	encryptionCertificates: document.querySelector("#encryptionCertificates"),
	encryptionUser: document.querySelector("#encryptionUser"),
	encryptionPassword: document.querySelector("#encryptionPassword"),
	encryptionConfirm: document.querySelector("#encryptionConfirm"),
	ofdInput: document.querySelector("#ofdInput"),
	ofdButton: document.querySelector("#ofdButton"),
	newButton: document.querySelector("#newButton"),
	editButton: document.querySelector("#editButton"),
	editNotice: document.querySelector("#editNotice"),
	editorTools: document.querySelector("#editorTools"),
	selectObjectButton: document.querySelector("#selectObjectButton"),
	deleteObjectButton: document.querySelector("#deleteObjectButton"),
	editObjectButton: document.querySelector("#editObjectButton"),
	compositeBackButton: document.querySelector("#compositeBackButton"),
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
	infoCustomRows: document.querySelector("#infoCustomRows"),
	infoCustomAdd: document.querySelector("#infoCustomAdd"),
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
	sourceTextPanel: document.querySelector("#sourceTextPanel"),
	sourceTextForm: document.querySelector("#sourceTextForm"),
	sourceTextValue: document.querySelector("#sourceTextValue"),
	sourceTextPreview: document.querySelector("#sourceTextPreview"),
	sourceTextStatus: document.querySelector("#sourceTextStatus"),
	sourceTextCancel: document.querySelector("#sourceTextCancel"),
	objectStyleForm: document.querySelector("#objectStyleForm"),
	groupObjectsButton: document.querySelector("#groupObjectsButton"),
	ungroupObjectButton: document.querySelector("#ungroupObjectButton"),
	objectOpacity: document.querySelector("#objectOpacity"),
	objectLinkFields: document.querySelector("#objectLinkFields"),
	objectLinkKind: document.querySelector("#objectLinkKind"),
	objectLinkAddress: document.querySelector("#objectLinkAddress"),
	objectLinkPage: document.querySelector("#objectLinkPage"),
	objectLinkAddressRow: document.querySelector("#objectLinkAddressRow"),
	objectLinkPageRow: document.querySelector("#objectLinkPageRow"),
	objectGradientFields: document.querySelector("#objectGradientFields"),
	gradientTarget: document.querySelector("#gradientTarget"),
	objectBorderFields: document.querySelector("#objectBorderFields"),
	borderEnabled: document.querySelector("#borderEnabled"),
	borderWidth: document.querySelector("#borderWidth"),
	borderColor: document.querySelector("#borderColor"),
	borderColorOriginal: document.querySelector("#borderColorOriginal"),
	borderHorizontal: document.querySelector("#borderHorizontal"),
	borderVertical: document.querySelector("#borderVertical"),
	patternTarget: document.querySelector("#patternTarget"),
	gradientKind: document.querySelector("#gradientKind"),
	gradientControls: document.querySelector("#gradientControls"),
	gradientAngle: document.querySelector("#gradientAngle"),
	gradientStops: document.querySelector("#gradientStops"),
	gradientAdd: document.querySelector("#gradientAdd"),
	objectPatternFields: document.querySelector("#objectPatternFields"),
	patternWidth: document.querySelector("#patternWidth"),
	patternHeight: document.querySelector("#patternHeight"),
	patternXStep: document.querySelector("#patternXStep"),
	patternYStep: document.querySelector("#patternYStep"),
	patternCTM: document.querySelector("#patternCTM"),
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
	pageRangeRow: document.querySelector("#pageRangeRow"),
	pageRange: document.querySelector("#pageRange"),
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
	pageControl: document.querySelector(".page-control"),
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
	dpiValue: document.querySelector("#dpiValue"),
	formatValue: document.querySelector("#formatValue"),
	exportFormat: document.querySelector("#exportFormat"),
	exportPageButton: document.querySelector("#exportPageButton"),
	exportButton: document.querySelector("#exportButton"),
	exportPanel: document.querySelector("#exportPanel"),
	exportTitle: document.querySelector("#exportTitle"),
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
	pageSelectionTools: document.querySelector("#pageSelectionTools"),
	pageSelectionCount: document.querySelector("#pageSelectionCount"),
	selectAllPages: document.querySelector("#selectAllPages"),
	finishPageSelection: document.querySelector("#finishPageSelection"),
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
	renderVectorButton: document.querySelector("#renderVectorButton"),
	renderRasterButton: document.querySelector("#renderRasterButton"),
	offlineStatus: document.querySelector("#offlineStatus"),
	refreshAppButton: document.querySelector("#refreshAppButton"),
	metaFile: document.querySelector("#metaFile"),
	metaTitle: document.querySelector("#metaTitle"),
	conversionNotice: document.querySelector("#conversionNotice"),
	conversionWarnings: document.querySelector("#conversionWarnings"),
	metaSubject: document.querySelector("#metaSubject"),
	metaAuthor: document.querySelector("#metaAuthor"),
	metaCreationDate: document.querySelector("#metaCreationDate"),
	metaModDate: document.querySelector("#metaModDate"),
	metaCreator: document.querySelector("#metaCreator"),
	metaType: document.querySelector("#metaType"),
	metaVersion: document.querySelector("#metaVersion"),
	metaSignatures: document.querySelector("#metaSignatures"),
	metaFonts: document.querySelector("#metaFonts"),
	attachmentPanel: document.querySelector("#attachmentPanel"),
	attachmentList: document.querySelector("#attachmentList"),
	signaturePanel: document.querySelector("#signaturePanel"),
	signatureSummary: document.querySelector("#signatureSummary"),
	signatureList: document.querySelector("#signatureList"),
	annotationNote: document.querySelector("#annotationNote"),
	annotationTool: document.querySelector("#annotationTool"),
	penTool: document.querySelector("#penTool"),
	annotationCreate: document.querySelector("#annotationCreate"),
	annotationCreateStatus: document.querySelector("#annotationCreateStatus"),
	objectPicker: document.querySelector("#objectPicker"),
	objectPickerList: document.querySelector("#objectPickerList"),
	annotationForm: document.querySelector("#annotationForm"),
	annotationFields: document.querySelector("#annotationFields"),
	annotationRemark: document.querySelector("#annotationRemark"),
	annotationCreator: document.querySelector("#annotationCreator"),
	annotationStatus: document.querySelector("#annotationStatus"),
	annotationClose: document.querySelector("#annotationClose"),
	annotationSubmit: document.querySelector("#annotationSubmit"),
	annotationDetail: document.querySelector("#annotationDetail"),
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
const annotationFields = Object.fromEntries(["CreateForm", "CreateCancel", "Content", "Old", "Font", "Text", "TextRow", "Image", "ImageRow", "Author", "LinkKind", "Address", "AddressRow", "Target", "TargetRow", "X", "Y", "Width", "Height", "Angle", "Opacity", "Tile", "Placement", "PlacementLabel", "Pick", "Scope", "Pages"].map(key => [key, document.querySelector(`#annotation${key}`)]));
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
	onReshape: (item, box) => changeDocument("ofdgoReshapeObject", item, box.x, box.y, box.width, box.height, Boolean(item.oriented)),
	onResize: (item, box) => changeDocument("ofdgoResizeObjects", { ...item, id: [item.id] }, box.x, box.y, box.width, box.height),
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
		const item = { index: items[0].index, id: items.map(item => item.id), scoped: items.every(item => item.scoped) };
		if (points) return changeDocument("ofdgoEraseObjectsPath", item, points);
		return box ? changeDocument("ofdgoEraseObjects", item, box.x, box.y, box.width, box.height)
			: changeDocument("ofdgoDeleteObjects", item);
	},
	onEdit: editCanvasObject,
	onPick: openObjectPicker,
	onExitScope: () => {
		if (!state.composite) return false;
		exitCompositeScope();
		return true;
	},
	drawStyle: shapeStyle,
	onTool: () => {
		updateDrawingControls();
		const entry = state.annotationCreate;
		if (entry?.picking && canvasEditor.tool !== "annotation:watermark") {
			queueMicrotask(() => {
				if (state.annotationCreate === entry && entry.picking) {
					entry.picking = false;
					if (!state.editing || entry.openSeq !== state.openSeq || canvasEditor.tool) {
						state.annotationCreate = null;
						return;
					}
					el.annotationCreate.showModal();
				}
			});
		}
	},
	onDraw: (index, shape, box, style) => changeDocument("ofdgoInsertShape", null, index, shape,
		box.x, box.y, box.width, box.height, style.fill, style.fillColor, style.stroke, style.strokeColor, style.lineWidth),
	onDrawText: beginCanvasText,
	onDrawAnnotation: placeAnnotation,
	onDrawInk: (index, points, style, pressure) => changeDocument("ofdgoInsertInk", null, index, JSON.stringify(points), style.lineWidth, pressure, style.strokeColor),
	onPreviewText: (item, value, font, size) => callWASM("ofdgoPreviewText", item.index, item.id, value, font || null, size),
	onCommitText: (item, value, fontData, color = null) => item.draft
		? changeDocument("ofdgoInsertText", null, item.index, value, fontData || item.fontData, item.x, item.y, item.size, item.color, item.width, item.wrap, item.align, item.paragraphHeight, item.letterSpacing, ...textIndents(item))
		: changeDocument("ofdgoUpdateText", item, value, fontData || null, item.size, color),
	onTextChange: () => {
		const editing = canvasEditor.input;
		updatePendingChanges();
		if (!editing) {
			updateObjectControls(canvasEditor.selected);
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
editorClick(el.compositeBackButton, exitCompositeScope);
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
	el.infoCustomRows.replaceChildren();
	for (const [index, field] of (state.doc.customData || []).entries()) addCustomDataRow(field, index);
	el.infoStatus.textContent = "";
	el.infoPanel.showModal();
});
el.infoCancel.addEventListener("click", () => el.infoPanel.close());
el.infoCustomAdd.addEventListener("click", () => addCustomDataRow());
el.infoForm.addEventListener("submit", async event => {
	event.preventDefault();
	const fields = [...el.infoCustomRows.children].map(row => ({Name: row.children[0].value, Value: row.children[1].value, Source: row.sourceIndex}));
	if (await changeDocument("ofdgoUpdateInfo", null, el.infoTitle.value, el.infoAuthor.value, el.infoSubject.value, JSON.stringify(fields))) el.infoPanel.close();
});

function addCustomDataRow(field = {Name: "", Value: ""}, sourceIndex) {
	const row = document.createElement("div");
	row.sourceIndex = sourceIndex;
	row.className = "info-custom-row";
	for (const [key, label] of [["Name", "名称"], ["Value", "内容"]]) {
		const input = document.createElement("input");
		input.type = "text";
		input.value = field[key];
		input.setAttribute("aria-label", label);
		input.placeholder = label;
		row.append(input);
	}
	const remove = document.createElement("button");
	remove.type = "button";
	remove.className = "small-button";
	remove.textContent = "×";
	remove.title = "删除字段";
	remove.setAttribute("aria-label", "删除字段");
	remove.addEventListener("click", () => row.remove());
	row.append(remove);
	el.infoCustomRows.append(row);
}
el.annotationClose.addEventListener("click", () => el.annotationNote.close());
el.annotationNote.addEventListener("close", () => { state.annotationEdit = null; });
el.penTool.addEventListener("change", () => {
	const tool = el.penTool.value;
	el.penTool.value = "";
	canvasEditor.select(null);
	toggleDrawingTool(tool);
});
el.annotationTool.addEventListener("change", async () => {
	const kind = el.annotationTool.value;
	el.annotationTool.value = "";
	if (kind === "objects") { openObjectPicker(); return; }
	let selected = canvasEditor.selected;
	if (state.composite?.key.startsWith("annotation:")) {
		const id = state.composite.key.split("/")[0];
		selected = { ...state.pageCache.get(state.composite.index)?.objects.find(item => item.id === id), index: state.composite.index, id, type: "Annotation" };
	}
	if (kind === "details" || kind === "replace") {
		if (selected?.type !== "Annotation") { setStatus("请先选择注解"); return; }
		try {
			const info = await callWASM("ofdgoReadAnnotation", selected.index, selected.id);
			if (kind === "details") editAnnotationDetails({ ...selected, ...info });
			else openAnnotationCreate(kind, selected.index, null, selected, info);
		} catch (error) { showError(error, false); }
		return;
	}
	if (kind === "link" && selected?.annotationType === "Link") {
		try {
			const info = await callWASM("ofdgoReadAnnotation", selected.index, selected.id);
			openAnnotationCreate(kind, selected.index, selected, selected, info);
		} catch (error) { showError(error, false); }
	} else {
		resetCompositeScope();
		updateControls();
		if (kind === "watermark") openAnnotationCreate(kind, state.pageIndex);
		else {
			canvasEditor.select(null);
			toggleDrawingTool(`annotation:${kind}`);
		}
	}
});
annotationFields.CreateCancel.addEventListener("click", () => el.annotationCreate.close());
el.annotationCreate.addEventListener("close", () => {
	if (!state.annotationCreate?.picking) state.annotationCreate = null;
});
annotationFields.Content.addEventListener("change", updateAnnotationFields);
annotationFields.Height.addEventListener("input", () => { state.annotationCreate.height = annotationFields.Height.value; });
annotationFields.LinkKind.addEventListener("change", updateAnnotationFields);
annotationFields.Old.addEventListener("change", () => { annotationFields.Text.value = annotationFields.Old.value; });
annotationFields.Tile.addEventListener("change", () => {
	annotationFields.Placement.value = "page";
	state.annotationCreate.area = null;
	updateAnnotationFields();
});
annotationFields.Placement.addEventListener("change", () => {
	if (annotationFields.Placement.value === "custom") pickWatermarkArea();
	else state.annotationCreate.area = null;
});
annotationFields.Pick.addEventListener("click", pickWatermarkArea);
annotationFields.Scope.addEventListener("change", updateAnnotationFields);
annotationFields.CreateForm.addEventListener("submit", async event => {
	event.preventDefault();
	const entry = state.annotationCreate;
	if (!entry || !annotationFields.CreateForm.reportValidity() || document.body.hasAttribute("aria-busy")) return;
	const openSeq = state.openSeq;
	const value = key => annotationFields[key].value;
	const image = entry.kind === "stamp" || entry.kind === "watermark" && value("Content") === "image";
	let font = null, data = null;
	try {
		if (image) data = new Uint8Array(await annotationFields.Image.files[0].arrayBuffer());
		if (entry.kind === "watermark" && !image || entry.kind === "replace" && value("Font") === "current") {
			const choice = state.textDefaults.fontChoice || state.textFonts[Number(fontPicker.value)];
			if (!choice || choice.disabled) throw new Error("请先选择可用字体");
			font = await readTextFont(choice);
		}
		if (openSeq !== state.openSeq || state.annotationCreate !== entry) return;
		const options = { kind: entry.kind, id: entry.item?.id.replace("annotation:", "") || "", text: value("Text"), old: value("Old"),
			creator: value("Author"), pages: entry.kind === "watermark" && value("Scope") !== "custom"
				? value("Scope") === "all" ? `1-${state.doc.pageCount}` : String(entry.index + 1) : value("Pages"),
			uri: value("Address"), target: value("LinkKind") === "page" ? Number(value("Target")) : 0,
			x: Number(value("X")), y: Number(value("Y")), width: entry.kind === "note" ? 5 : Number(value("Width")), height: entry.kind === "note" ? 5 : Number(value("Height")),
			angle: Number(value("Angle")), alpha: Math.round((100 - Number(value("Opacity"))) * 255 / 100), tile: value("Tile") === "tile",
			area: entry.kind === "watermark" && value("Placement") === "custom" ? entry.area : null,
			size: state.textDefaults.size, color: state.textDefaults.color };
		if (entry.kind === "link" && entry.item) {
			options.base = entry.info?.linkBase || "";
			options.keep = value("LinkKind") === "keep" || value("LinkKind") === entry.info?.linkKind
				&& (value("LinkKind") === "uri" ? value("Address") === entry.info.linkURI : Number(value("Target")) === entry.info.linkPage);
		}
		if (await changeDocument("ofdgoWriteAnnotation", null, entry.index, JSON.stringify(options), font, data)) el.annotationCreate.close();
	} catch (error) { el.annotationCreateStatus.textContent = error.message; }
});
el.annotationForm.addEventListener("submit", async event => {
	if (!state.annotationEdit) return;
	event.preventDefault();
	if (await changeAnnotations(state.annotationEdit, "update", el.annotationRemark.value, el.annotationCreator.value)) {
		el.annotationNote.close();
		canvasEditor.focus();
	}
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
	if (item?.scoped) return changeDocument("ofdgoResetCompositeCrop", item);
	if (canEditObject(item, "resetCrop")) return changeDocument("ofdgoResetImageCrop", item);
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
el.importCancel.addEventListener("click", () => state.importing ? cancelExport() : el.importPanel.close());
el.importPanel.addEventListener("cancel", event => {
	if (state.importing) { event.preventDefault(); cancelExport(); }
});
el.importPanel.addEventListener("close", () => {
	state.importPageCount = 0;
	state.importEncrypted = false;
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
	const options = [];
	if (state.importEncrypted) {
		if (!window.confirm(state.doc.encryption?.encrypted ? "按目标文档的加密策略导入，是否继续？" : "目标未加密，导入内容将按明文保存，是否继续？")) return;
		options.push(true);
	}
	const saved = await changeDocument("ofdgoImportPages", null, el.importRange.value === "custom" ? el.importPages.value : "", position, el.importOutlines.checked, ...options);
	if (saved) el.importPanel.close();
});
editorClick(el.copyPageButton, () => changeSelectedPages("copy"));
editorClick(el.deletePageButton, () => changeSelectedPages("delete"));
editorClick(el.movePagePrevButton, () => changeSelectedPages("move", -1));
editorClick(el.movePageNextButton, () => changeSelectedPages("move", 1));
editorClick(el.pageSettingsButton, openPagePanel);
editorClick(el.batchPagesButton, openBatchPages);
function openBatchPages() {
	el.batchPageAction.value = "copy";
	el.batchPagesSubmit.textContent = "复制";
	el.batchPagePositionRow.hidden = true;
	el.batchPagePosition.value = "after";
	el.batchPageRange.value = selectedPageRange();
	el.batchPagesStatus.textContent = "";
	el.batchPagesPanel.showModal();
	el.batchPageRange.select();
}
el.batchPagesCancel.addEventListener("click", () => el.batchPagesPanel.close());
el.batchPageAction.addEventListener("change", () => {
	el.batchPagesSubmit.textContent = { copy: "复制", move: "移动", delete: "删除", save: "另存" }[el.batchPageAction.value];
	el.batchPagePositionRow.hidden = el.batchPageAction.value !== "move";
});
el.batchPagesForm.addEventListener("submit", async event => {
	event.preventDefault();
	if (document.body.hasAttribute("aria-busy") || !el.batchPagesForm.reportValidity()) return;
	const args = [el.batchPageAction.value, el.batchPageRange.value];
	if (args[0] === "save") {
		const seq = state.openSeq;
		try {
			const indices = await callWASM("ofdgoParsePageRange", args[1]);
			if (seq === state.openSeq && el.batchPagesPanel.open && await exportFile(true, indices, "ofd")) el.batchPagesPanel.close();
		} catch (err) {
			if (seq === state.openSeq) el.batchPagesStatus.textContent = err.message;
		}
		return;
	}
	if (args[0] === "move") args.push({ before: state.pageIndex, after: state.pageIndex + 1, first: 0, last: state.doc.pageCount }[el.batchPagePosition.value]);
	if (await changeDocument("ofdgoBatchPages", null, ...args)) el.batchPagesPanel.close();
});
editorClick(el.objectStyleButton, openObjectStyle);
editorClick(el.objectBoundsButton, openObjectBounds);
for (const [button, ungroup] of [[el.groupObjectsButton, false], [el.ungroupObjectButton, true]]) {
	button.addEventListener("click", async () => {
		const item = canvasEditor.selected;
		if (await changeDocument("ofdgoGroupObjects", { ...item, id: canvasEditor.items().map(member => member.id) }, ungroup)) el.objectStylePanel.close();
	});
}
el.arrowTool.addEventListener("change", async () => {
	const tool = el.arrowTool.value;
	el.arrowTool.value = "";
	if (await canvasEditor.commitText() && await canvasEditor.commitCrop()) {
		const item = canvasEditor.selected;
		if (lineShape(item?.shape)) {
			const box = item.geometry;
			await changeDocument("ofdgoReshapeLine", item, box.x, box.y, box.width, box.height, tool);
			return;
		}
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
		await callWASM("ofdgoCaptureStyle", item.index, item.id, state.composite?.key || "");
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
	if (item.oriented) {
		const [a, b, c, d, e, f] = item.oriented.matrix, determinant = a * d - b * c;
		const x = values[0] - e, y = values[1] - f;
		saved = await changeDocument("ofdgoReshapeObject", item, (d * x - c * y) / determinant, (a * y - b * x) / determinant, values[2] / Math.hypot(a, b), values[3] / Math.hypot(c, d), true);
	} else if (item.shape) {
		if (lineShape(item.shape)) { values[2] *= Math.sign(item.geometry.width); values[3] *= Math.sign(item.geometry.height); }
		saved = await changeDocument("ofdgoReshapeObject", item, ...values);
	} else saved = await changeDocument("ofdgoResizeObjects", { ...item, id: canvasEditor.items().map(member => member.id) }, ...values);
	if (saved) el.objectBoundsPanel.close();
});
el.objectStyleCancel.addEventListener("click", () => el.objectStylePanel.close());
el.sourceTextCancel.addEventListener("click", () => el.sourceTextPanel.close());
el.sourceTextValue.addEventListener("input", () => {
	clearTimeout(state.sourceTextTimer);
	state.sourceTextTimer = setTimeout(previewPositionedText, 150);
});
el.sourceTextPanel.addEventListener("close", () => {
	clearTimeout(state.sourceTextTimer);
	state.sourceText = null;
	el.sourceTextPreview.replaceChildren();
});
el.sourceTextForm.addEventListener("submit", async event => {
	event.preventDefault();
	const item = state.sourceText?.item;
	if (!item) return;
	if (await changeDocument("ofdgoUpdateText", item, el.sourceTextValue.value, null, item.size, null)) el.sourceTextPanel.close();
});
el.objectLinkKind.addEventListener("change", updateObjectLinkFields);
el.gradientKind.addEventListener("change", () => {
	el.gradientControls.hidden = el.gradientKind.value === "keep";
	el.gradientAngle.disabled = el.gradientKind.value !== "linear";
	for (const input of el.gradientStops.querySelectorAll("input")) input.disabled = el.gradientKind.value === "keep";
});
el.gradientTarget.addEventListener("change", () => {
	state.gradientForms[state.gradientTarget].form = gradientForm();
	loadGradientForm(el.gradientTarget.value);
});
el.patternTarget.addEventListener("change", () => {
	state.patternForms[state.patternTarget] = patternForm();
	loadPatternForm(el.patternTarget.value);
});
el.gradientAdd.addEventListener("click", () => addGradientStop(50, "#808080", 0));
el.objectDash.addEventListener("change", () => {
	el.objectDashRow.hidden = el.objectDash.value !== "custom";
	el.objectDashPattern.required = !el.objectDashRow.hidden;
});
el.objectStyleForm.addEventListener("submit", async event => {
	event.preventDefault();
	if (!el.objectGradientFields.hidden && el.gradientKind.value !== "keep" && el.gradientStops.children.length < 2) {
		el.objectStyleStatus.textContent = "至少保留两个色标";
		return;
	}
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
	if (await changeDocument("ofdgoBatchPages", null, "resize", selectedPageRange(), Number(el.pageWidth.value), Number(el.pageHeight.value))) {
		el.pagePanel.close();
	}
});
el.ofdButton.addEventListener("click", openOFDFile);
batchElements.Button.addEventListener("click", () => { updateBatchControls(); batchElements.Panel.showModal(); });
batchElements.Add.addEventListener("click", () => batchElements.Input.click());
batchElements.Input.addEventListener("change", () => { addBatchFiles(batchElements.Input.files); batchElements.Input.value = ""; });
batchElements.Clear.addEventListener("click", () => { batch.items = []; renderBatchList(); batchElements.Status.textContent = ""; batchElements.Progress.value = 0; });
batchElements.Format.addEventListener("change", updateBatchControls);
batchElements.Close.addEventListener("click", () => batchElements.Panel.close());
batchElements.Cancel.addEventListener("click", cancelBatch);
batchElements.Form.addEventListener("submit", event => { event.preventDefault(); runBatch(); });
batchElements.Panel.addEventListener("dragover", event => { event.preventDefault(); event.stopPropagation(); event.dataTransfer.dropEffect = batch.running ? "none" : "copy"; });
batchElements.Panel.addEventListener("drop", event => { event.preventDefault(); event.stopPropagation(); if (!batch.running) addBatchFiles(event.dataTransfer.files); });
batchElements.Destination.querySelector('[value="directory"]').disabled = !window.showDirectoryPicker;
batchElements.Destination.value = window.showDirectoryPicker ? "directory" : "archive";
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
el.encryptSaveButton.addEventListener("click", () => openExportPanel(true));
el.signButton.addEventListener("click", () => {
	if (!state.doc || document.body.hasAttribute("aria-busy")) return;
	el.signForm.reset();
	el.signPages.value = state.editing && state.pageSelection.size ? selectedPageRange() : String(state.pageIndex + 1);
	el.signStatus.textContent = "";
	updateSignPlacement();
	el.signPanel.showModal();
});
el.signSeal.addEventListener("change", updateSignPlacement);
el.signPlacement.addEventListener("change", updateSignPlacement);
el.signCancel.addEventListener("click", () => state.signing ? cancelSigning() : el.signPanel.close());
el.signPanel.addEventListener("cancel", event => {
	if (state.signing) { event.preventDefault(); cancelSigning(); }
});
el.signPanel.addEventListener("close", () => { if (!el.signPanel.open) el.signForm.reset(); });
el.signForm.addEventListener("submit", signDocument);
el.verifyButton.addEventListener("click", () => {
	if (!state.doc || document.body.hasAttribute("aria-busy")) return;
	el.verifyForm.reset();
	el.verifyStatus.textContent = "";
	el.verifyPanel.showModal();
});
el.verifyCancel.addEventListener("click", () => el.verifyPanel.close());
el.verifyPanel.addEventListener("close", () => { if (!el.verifyPanel.open) el.verifyForm.reset(); });
el.verifyForm.addEventListener("submit", verifyDocument);
el.credentialsCancel.addEventListener("click", () => finishCredentials(null));
el.credentialsPanel.addEventListener("cancel", event => { event.preventDefault(); finishCredentials(null); });
el.credentialsPanel.addEventListener("close", () => { if (!el.credentialsPanel.open) finishCredentials(null); });
el.credentialsType.addEventListener("change", updateCredentialsType);
el.credentialsForm.addEventListener("submit", async event => {
	event.preventDefault();
	if (!credentialRequest) return;
	if (el.credentialsType.value === "certificate") {
		const request = credentialRequest;
		let key;
		const keyPassword = new TextEncoder().encode(el.credentialsKeyPassword.value);
		el.credentialsKeyPassword.value = "";
		try {
			key = (await securityFiles(el.credentialsKey))[0];
			const certificate = (await securityFiles(el.credentialsCertificate))[0];
			if (credentialRequest !== request) { key?.fill(0); keyPassword.fill(0); return; }
			if (!key || !certificate) { el.credentialsStatus.textContent = "请选择证书和私钥"; key?.fill(0); keyPassword.fill(0); return; }
			finishCredentials({password:new Uint8Array(), key, keyPassword, certificate, userName:el.credentialsUser.value.trim()});
		} catch {
			key?.fill(0);
			keyPassword.fill(0);
			if (credentialRequest === request) el.credentialsStatus.textContent = "文件读取失败";
		}
		return;
	}
	if (!el.credentialsPassword.value) return;
	finishCredentials({ password: new TextEncoder().encode(el.credentialsPassword.value), userName: el.credentialsUser.value.trim() });
});
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
document.addEventListener("keydown", event => {
	if (event.key === "Tab") el.viewerPanel.classList.add("keyboard-focus");
}, true);
document.addEventListener("pointerdown", () => el.viewerPanel.classList.remove("keyboard-focus"), true);
el.fontAddButton.addEventListener("click", () => openFontFile(el.fontInput));
el.fontDirectoryButton.addEventListener("click", () => openFontFile(el.fontDirectoryInput));
el.localFontButton.addEventListener("click", loadLocalFonts);
el.ofdInput.addEventListener("change", () => openOFD(el.ofdInput.files[0]));
el.fontInput.addEventListener("change", openSelectedFonts);
el.fontDirectoryInput.addEventListener("change", openSelectedFonts);
el.prevButton.addEventListener("click", () => renderPage(state.pageIndex - 1));
el.imageDPI.addEventListener("change", () => changeDisplayMode());
el.renderVectorButton.addEventListener("click", () => changeDisplayMode("svg"));
el.renderRasterButton.addEventListener("click", () => changeDisplayMode("raster"));
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
el.exportButton.addEventListener("click", () => openExportPanel());
el.exportCancel.addEventListener("click", () => el.exportPanel.close());
el.exportPanel.addEventListener("close", () => { if (!el.exportPanel.open) clearEncryptionForm(); });
el.encryptionType.addEventListener("change", updateEncryptionType);
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
el.exportForm.addEventListener("submit", async (event) => {
	event.preventDefault();
	if (!el.exportSubmit.disabled) {
		if (state.exportEncrypted) {
			const certificates = el.encryptionType.value === "certificate";
			if (certificates && !el.encryptionCertificates.files?.length) {
				el.exportRangeStatus.textContent = "请选择接收者证书";
				return;
			}
			if (!certificates && !el.encryptionPassword.value) {
				el.exportRangeStatus.textContent = "请输入口令";
				el.encryptionPassword.focus();
				return;
			}
			if (!certificates && el.encryptionPassword.value !== el.encryptionConfirm.value) {
				el.exportRangeStatus.textContent = "口令不一致";
				el.encryptionConfirm.focus();
				return;
			}
			const encryption = certificates
				? {password:new Uint8Array(), userName:"", recipientFiles:Array.from(el.encryptionCertificates.files)}
				: { password: new TextEncoder().encode(el.encryptionPassword.value), userName: el.encryptionUser.value.trim() };
			clearEncryptionForm();
			try { return await exportFile(true, state.exportPages, "ofd", encryption); }
			finally { encryption.password.fill(0); }
		}
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
el.navigationContent.addEventListener("scroll", () => {
	if (state.thumbnailFrame) return;
	state.thumbnailFrame = requestAnimationFrame(() => {
		state.thumbnailFrame = 0;
		syncThumbnailWindow();
	});
});
for (const [viewport, current] of [[el.viewerPanel, () => state.pageWindow], [el.navigationContent, () => el.pageList.hidden ? null : state.thumbnailWindow]]) {
	viewport.addEventListener("wheel", event => {
		const view = current();
		if (!view || view.total <= SCROLL_SEGMENT_SIZE || event.ctrlKey || event.shiftKey || !event.deltaY) return;
		event.preventDefault();
		view.relativeScroll = false;
		const unit = event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? viewport.clientHeight : 1;
		scrollPageWindowBy(view, event.deltaY * unit);
		viewport.scrollLeft += event.deltaX * unit;
	}, { passive: false });
	viewport.addEventListener("pointerdown", event => {
		const view = current();
		if (view) view.relativeScroll = event.pointerType === "touch" || state.editing && !!event.target.closest(".page-shell");
	});
}
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
	if ((event.ctrlKey || event.metaKey) && !event.shiftKey && key.toLowerCase() === "s" && state.doc
		&& (state.editorInfo || state.ofdBytes)) {
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
	if (state.pageWindow && (!event.shiftKey || key === " ") && el.viewerPanel.contains(target)
		&& !target.closest("input, textarea, select, button, a, [contenteditable]")) {
		const view = state.pageWindow;
		const delta = { ArrowUp: -40, ArrowDown: 40, PageUp: -el.viewerPanel.clientHeight * 0.9, PageDown: el.viewerPanel.clientHeight * 0.9,
			" ": el.viewerPanel.clientHeight * 0.9 * (event.shiftKey ? -1 : 1) }[key];
		if (key === "Home" || key === "End") {
			event.preventDefault();
			view.relativeScroll = false;
			syncPageWindow(view, key === "Home" ? -windowInset(view) : view.total);
			syncCurrentPageFromScroll();
			return;
		}
		if (delta !== undefined && !event.ctrlKey && !event.metaKey && view.total > SCROLL_SEGMENT_SIZE) {
			event.preventDefault();
			scrollPageWindowBy(view, delta);
			return;
		}
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
		canvasEditor.focus();
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
	return batchElements.Panel.open || el.exportPanel.open || el.createPanel.open || el.insertPanel.open || el.pagePanel.open || el.paragraphPanel.open || el.importPanel.open || el.infoPanel.open
		|| el.batchPagesPanel.open || el.objectStylePanel.open || el.sourceTextPanel.open || el.outlinePanel.open || el.objectBoundsPanel.open || el.annotationNote.open || el.annotationCreate.open || el.objectPicker.open;
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
	let box = item.geometry || selectionBounds((item.items || [item]).map(member => member.bounds || member));
	if (item.oriented) {
		const frame = item.oriented, [a, b, c, d, e, f] = frame.matrix;
		box = { x: a * frame.box.x + c * frame.box.y + e, y: b * frame.box.x + d * frame.box.y + f,
			width: frame.box.width * Math.hypot(a, b), height: frame.box.height * Math.hypot(c, d) };
	}
	state.boundsOriginal = { x: box.x, y: box.y, width: Math.abs(box.width), height: Math.abs(box.height) };
	for (const [input, key] of [[el.objectX,"x"], [el.objectY,"y"], [el.objectWidth,"width"], [el.objectHeight,"height"]]) setObjectDimension(input, state.boundsOriginal[key]);
	el.objectAspect.checked = true;
	el.objectAspect.disabled = !item.shape && !item.oriented && !canEditObject(item, "stretch") || lineShape(item.shape) && (!box.width || !box.height);
	el.objectWidth.disabled = lineShape(item.shape) && !box.width;
	el.objectHeight.disabled = lineShape(item.shape) && !box.height;
	el.objectBoundsStatus.textContent = "";
	el.objectBoundsPanel.showModal();
}

function canCopyStyle(item) {
	if (item?.type === "Annotation") return false;
	if (!item?.scoped) return canEditObject(item, "update");
	if (item.type === "TextObject") return canEditObject(item, "replaceFont") && canEditObject(item, "paint");
	if (item.type === "PathObject") return canEditObject(item, "transform") && canEditObject(item, "paint");
	return item.type === "ImageObject" && canEditObject(item, "transform");
}

function openObjectStyle() {
	const items = canvasEditor.items();
	if (!items.length) return;
	const ordered = [...items].sort((a, b) => a.position - b.position);
	el.groupObjectsButton.disabled = items.length < 2 || !ordered.every((item, index) => canEditObject(item, "copy") && canEditObject(item, "order") && item.container === ordered[0].container && item.position === ordered[0].position + index);
	el.ungroupObjectButton.disabled = items.length !== 1 || !canEditObject(items[0], "ungroup");
	el.copyStyleButton.disabled = items.length !== 1 || !canCopyStyle(items[0]);
	el.pasteStyleButton.disabled = !state.styleClipboard || !items.every(item => item.type === state.styleClipboard && canCopyStyle(item));
	const shared = (key, fallback) => items.every(item => (item[key] ?? fallback) === (items[0][key] ?? fallback)) ? items[0][key] ?? fallback : null;
	el.objectLinkFields.hidden = items.length !== 1 || items[0].scoped;
	const link = items[0].link ? JSON.parse(items[0].link) : null;
	const targetPage = link?.Goto?.Dest ? state.doc.pages.findIndex(page => page.id === link.Goto.Dest.PageID) : -1;
	el.objectLinkKind.value = items[0].linkCount > 1 ? "keep" : link?.URI ? "uri" : targetPage >= 0 ? "page" : link ? "keep" : "none";
	el.objectLinkKind.disabled = items[0].linkCount > 1;
	el.objectLinkAddress.value = link?.URI?.URI || "";
	el.objectLinkPage.value = String(targetPage >= 0 ? targetPage + 1 : 1);
	el.objectLinkPage.max = String(state.doc?.pages.length || 1);
	state.objectLinkOriginal = { kind: el.objectLinkKind.value, address: el.objectLinkAddress.value, page: el.objectLinkPage.value, link };
	updateObjectLinkFields();
	el.objectBorderFields.hidden = items.length !== 1 || items[0].scoped || items[0].type !== "ImageObject";
	const border = items[0].borderStyle ? JSON.parse(items[0].borderStyle) : null;
	el.borderEnabled.checked = Boolean(border);
	el.borderWidth.value = String(border?.LineWidth ?? .353);
	el.borderHorizontal.value = String(border?.HorizonalCornerRadius ?? 0);
	el.borderVertical.value = String(border?.VerticalCornerRadius ?? 0);
	el.borderColor.value = items[0].borderColor || "#000000";
	el.borderColor.disabled = Boolean(border && !items[0].borderColor);
	el.borderColor.hidden = el.borderColor.disabled;
	el.borderColorOriginal.hidden = !el.borderColor.disabled;
	state.borderOriginal = {
		Enabled: el.borderEnabled.checked, LineWidth: el.borderWidth.value,
		HorizontalRadius: el.borderHorizontal.value, VerticalRadius: el.borderVertical.value,
		Color: el.borderColor.value
	};
	el.objectGradientFields.hidden = items.length !== 1 || items[0].scoped || items[0].type !== "PathObject" || !items[0].paintSize || !canEditObject(items[0], "paint");
	state.gradientForms = {};
	for (const target of ["fill", "stroke"]) {
		const gradient = items[0][`${target}Gradient`], base = gradient ? JSON.parse(gradient.paint) : null;
		const node = base?.AxialShd || base?.RadialShd;
		const start = node?.StartPoint.split(/\s+/).map(Number), end = node?.EndPoint.split(/\s+/).map(Number);
		const form = {
			kind: base?.AxialShd ? "linear" : base?.RadialShd ? "radial" : "keep",
			angle: String(base?.AxialShd ? Math.atan2(end[1] - start[1], end[0] - start[0]) * 180 / Math.PI : 0),
			stops: gradient ? gradient.stops.map((stop, i) => ({ position: String(stop.position), color: stop.color, transparency: String(stop.transparency), source: node.Segment[i] }))
				: [{ position: "0", color: "#000000", transparency: "0" }, { position: "100", color: "#ffffff", transparency: "0" }]
		};
		state.gradientForms[target] = { base, form, original: JSON.stringify(form) };
	}
	el.gradientTarget.value = "fill";
	loadGradientForm("fill");
	state.patternForms = {};
	for (const target of ["fill", "stroke"]) {
		const pattern = items.length === 1 && !items[0].scoped ? items[0][`${target}Pattern`] : null;
		if (pattern) state.patternForms[target] = Object.fromEntries(Object.entries(pattern).map(([key, value]) => [key, String(value)]));
	}
	state.patternOriginal = JSON.stringify(state.patternForms);
	el.objectPatternFields.hidden = !Object.keys(state.patternForms).length;
	el.patternTarget.disabled = Object.keys(state.patternForms).length < 2;
	el.patternTarget.value = state.patternForms.fill ? "fill" : "stroke";
	loadPatternForm(el.patternTarget.value);
	const alpha = items.some(item => item.alphaMixed) ? null : shared("alpha", 255), dash = shared("dashPattern", "");
	el.objectOpacity.value = alpha === null ? "" : String(Math.round((1 - alpha / 255) * 10000) / 100);
	el.objectOpacity.placeholder = alpha === null ? "混合" : "";
	el.objectStrokeFields.hidden = !items.every(item => item.type === "PathObject" && (!item.scoped || canEditObject(item, "paint")));
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
	if (!el.objectLinkFields.hidden) {
		const original = state.objectLinkOriginal, kind = el.objectLinkKind.value;
		if (kind !== "keep" && (kind !== original.kind || kind === "uri" && el.objectLinkAddress.value !== original.address || kind === "page" && el.objectLinkPage.value !== original.page)) {
			style.link = kind === "none" ? null : JSON.stringify(kind === "uri"
				? { URI: { ...original.link?.URI, URI: el.objectLinkAddress.value.trim() } }
				: { Dest: { ...(original.link?.Goto?.Dest || { Type: "Fit" }), PageID: state.doc.pages[Number(el.objectLinkPage.value) - 1].id } });
		}
	}
	if (!el.objectBorderFields.hidden) {
		const border = {};
		if (el.borderEnabled.checked !== state.borderOriginal.Enabled) border.Enabled = el.borderEnabled.checked;
		if (el.borderEnabled.checked) {
			for (const [input, key] of [[el.borderWidth, "LineWidth"], [el.borderHorizontal, "HorizontalRadius"], [el.borderVertical, "VerticalRadius"]]) {
				if (input.value !== state.borderOriginal[key] || !state.borderOriginal.Enabled) border[key] = Number(input.value);
			}
			if (!el.borderColor.disabled && (el.borderColor.value !== state.borderOriginal.Color || !state.borderOriginal.Enabled)) {
				border.Color = { Value: el.borderColor.value };
			}
		}
		if (Object.keys(border).length) style.borderStyle = JSON.stringify(border);
	}
	if (!el.objectGradientFields.hidden) {
		state.gradientForms[state.gradientTarget].form = gradientForm();
		for (const target of ["fill", "stroke"]) {
			const { base, form, original } = state.gradientForms[target];
			if (form.kind === "keep" || JSON.stringify(form) === original) continue;
			const [width, height] = canvasEditor.selected.paintSize;
			const previous = JSON.parse(original);
			const Segment = form.stops.map(stop => {
				const old = previous.stops.find(entry => entry.source && JSON.stringify(entry.source) === JSON.stringify(stop.source));
				const Color = old && old.color === stop.color ? { ...stop.source.Color } : { Value: stop.color };
				if (!old || old.transparency !== stop.transparency || old.color !== stop.color) Color.Alpha = Math.round(255 * (1 - Number(stop.transparency) / 100));
				return { Position: Number(stop.position) / 100, Color };
			}).sort((a, b) => a.Position - b.Position);
			const angle = Number(form.angle) * Math.PI / 180, x = Math.cos(angle), y = Math.sin(angle);
			const length = Math.abs(width * x) + Math.abs(height * y);
			let paint = form.kind === "linear"
				? { AxialShd: { StartPoint: `${width / 2 - x * length / 2} ${height / 2 - y * length / 2}`, EndPoint: `${width / 2 + x * length / 2} ${height / 2 + y * length / 2}`, Extend: "3", Segment } }
				: { RadialShd: { StartPoint: `${width / 2} ${height / 2}`, EndPoint: `${width / 2} ${height / 2}`, EndRadius: Math.max(width, height) / 2, Extend: "3", Segment } };
			if (base && form.kind === previous.kind) {
				paint = JSON.parse(JSON.stringify(base));
				const node = paint.AxialShd || paint.RadialShd;
				node.Segment = Segment;
				if (paint.AxialShd && form.angle !== previous.angle) {
					const start = node.StartPoint.split(/\s+/).map(Number), end = node.EndPoint.split(/\s+/).map(Number);
					const half = Math.hypot(end[0] - start[0], end[1] - start[1]) / 2, cx = (start[0] + end[0]) / 2, cy = (start[1] + end[1]) / 2;
					node.StartPoint = `${cx - x * half} ${cy - y * half}`;
					node.EndPoint = `${cx + x * half} ${cy + y * half}`;
				}
			}
			style[`${target}Paint`] = JSON.stringify(paint);
			style[target] = true;
		}
	}
	if (!el.objectPatternFields.hidden) {
		state.patternForms[state.patternTarget] = patternForm();
		const original = JSON.parse(state.patternOriginal);
		for (const [target, form] of Object.entries(state.patternForms)) {
			const pattern = {};
			for (const [key, field] of [["width", "Width"], ["height", "Height"], ["xStep", "XStep"], ["yStep", "YStep"], ["ctm", "CTM"]]) {
				if (form[key] !== original[target][key]) pattern[field] = key === "ctm" ? form[key].trim() : Number(form[key]);
			}
			if (Object.keys(pattern).length) style[`${target}PatternStyle`] = JSON.stringify(pattern);
		}
	}
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

function updateObjectLinkFields() {
	const kind = el.objectLinkKind.value, visible = !el.objectLinkFields.hidden;
	el.objectLinkAddressRow.hidden = !visible || kind !== "uri";
	el.objectLinkPageRow.hidden = !visible || kind !== "page";
	el.objectLinkAddress.disabled = el.objectLinkAddressRow.hidden;
	el.objectLinkPage.disabled = el.objectLinkPageRow.hidden;
	el.objectLinkAddress.required = visible && kind === "uri";
	el.objectLinkPage.required = visible && kind === "page";
}

function gradientForm() {
	return {
		kind: el.gradientKind.value, angle: el.gradientAngle.value,
		stops: [...el.gradientStops.children].map(row => ({ position: row.children[1].value, color: row.children[0].value, transparency: row.children[2].value, source: row.paintSource }))
	};
}

function loadGradientForm(target) {
	state.gradientTarget = target;
	const form = state.gradientForms[target].form;
	el.gradientKind.value = form.kind;
	el.gradientAngle.value = form.angle;
	el.gradientControls.hidden = form.kind === "keep";
	el.gradientAngle.disabled = form.kind !== "linear";
	el.gradientStops.replaceChildren();
	for (const stop of form.stops) addGradientStop(stop.position, stop.color, stop.transparency, stop.source);
}

function patternForm() {
	return { width: el.patternWidth.value, height: el.patternHeight.value, xStep: el.patternXStep.value, yStep: el.patternYStep.value, ctm: el.patternCTM.value };
}

function loadPatternForm(target) {
	state.patternTarget = target;
	const pattern = state.patternForms[target];
	for (const [input, key] of [[el.patternWidth, "width"], [el.patternHeight, "height"], [el.patternXStep, "xStep"], [el.patternYStep, "yStep"], [el.patternCTM, "ctm"]]) {
		input.value = pattern?.[key] ?? "";
		input.disabled = !pattern;
	}
}

function addGradientStop(position, color, transparency, source) {
	const row = document.createElement("div");
	row.paintSource = source;
	row.className = "gradient-stop-row";
	for (const [type, value, label] of [["color", color, "色标颜色"], ["number", position, "色标位置"], ["number", transparency, "色标透明度"]]) {
		const input = document.createElement("input");
		input.type = type;
		input.value = value;
		input.disabled = el.gradientKind.value === "keep";
		input.setAttribute("aria-label", label);
		input.title = label;
		if (type === "number") { input.min = "0"; input.max = "100"; input.step = "any"; input.required = true; }
		row.append(input);
	}
	const remove = document.createElement("button");
	remove.type = "button";
	remove.className = "small-button";
	remove.textContent = "×";
	remove.title = "删除色标";
	remove.setAttribute("aria-label", "删除色标");
	remove.addEventListener("click", () => row.remove());
	row.append(remove);
	el.gradientStops.append(row);
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
	state.importEncrypted = false;
	el.importSubmit.disabled = true;
	el.importStatus.textContent = "";
	setBusy(true);
	try {
		const data = file ? new Uint8Array(await file.arrayBuffer()) : null;
		if (openSeq !== state.openSeq) return;
		const info = data ? await openWithCredentials(null, openSeq, data, true) : await callWASM("ofdgoLoadImport", null);
		if (openSeq !== state.openSeq || !el.importPanel.open) {
			await callWASM("ofdgoLoadImport", null);
			return;
		}
		state.importPageCount = info?.pageCount || 0;
		state.importEncrypted = Boolean(info?.encrypted);
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
	const pages = selectedPageIndexes().map(index => state.doc.pages[index]);
	if (!pages.length) return;
	for (const [input, key] of [[el.pageWidth, "width"], [el.pageHeight, "height"]]) {
		const same = pages.every(page => page[key] === pages[0][key]);
		input.value = same ? String(pages[0][key]) : "";
		input.placeholder = same ? "" : "混合";
	}
	el.pageRangeRow.hidden = pages.length === 1;
	el.pageRange.textContent = `共${pages.length}页`;
	el.pageRange.title = selectedPageRange();
	el.pageStatus.textContent = "";
	updatePageDirection();
	el.pagePanel.showModal();
}

function updatePageDirection() {
	const complete = el.pageWidth.value !== "" && el.pageHeight.value !== "";
	el.pagePortrait.disabled = el.pageLandscape.disabled = !complete;
	const landscape = Number(el.pageWidth.value) > Number(el.pageHeight.value);
	el.pageLandscape.checked = complete && landscape;
	el.pagePortrait.checked = complete && !landscape;
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

function editorLocation() {
	const pending = canvasEditor.pendingSelection;
	const index = pending?.index ?? canvasEditor.selected?.index ?? state.pageIndex;
	return { page: state.doc.pages[index].id, scope: state.composite?.key || "", ids: pending?.ids || canvasEditor.items().map(item => item.id) };
}

function rememberEditorView(before, revision, reindex = false) {
	for (const key of state.editorViews.keys()) {
		if (key > revision) state.editorViews.delete(key);
	}
	state.editorViews.set(state.editorInfo.revision, { before, after: editorLocation(), reindex });
	if (state.editorViews.size > 100) state.editorViews.delete(state.editorViews.keys().next().value);
}

function pageCan(capability, index = state.pageIndex) {
	const bit = { insert: 1, copy: 2, delete: 4, move: 8, resize: 16 }[capability];
	return Boolean(state.editorInfo?.pageCapabilities?.[index] & bit);
}

async function toggleEditor() {
	if (!state.doc || !state.ready || document.body.hasAttribute("aria-busy") || formDialogOpen()) return;
	if (state.editorInfo) {
		if (!await canvasEditor.commitText() || !await canvasEditor.commitCrop()) return;
		const anchor = scaleAnchor(0);
		state.editing = !state.editing;
		resetCompositeScope();
		canvasEditor.clear();
		canvasEditor.setTool("");
		if (state.editing) {
			state.selectObjects = true;
			setPan(false);
		}
		updateControls();
		renderOutlines(false);
		if (state.renderMode !== "svg") {
			try {
				await refreshPageDisplay(anchor);
			} catch (err) {
				state.editing = !state.editing;
				updateControls();
				showError(err, false);
			}
			return;
		}
		renderPageList();
		applyFit(false);
		restoreScaleAnchor(anchor);
		updatePageListCurrent(true);
		return;
	}
	const { pageIndex, fitMode, scale } = state;
	const anchor = scaleAnchor(0);
	const openSeq = ++state.openSeq;
	setBusy(true, "正在准备编辑", null, "正在准备编辑");
	try {
		const doc = await callWASM("ofdgoEditDocument", pageIndex);
		if (openSeq !== state.openSeq) return;
		state.savedRevision = doc.revision;
		state.editorViews.clear();
		setEditorInfo(doc);
		state.editing = true;
		state.composite = null;
		state.selectObjects = true;
		state.ofdBytes = null;
		state.conversionWarnings = [];
		state.objectClipboard = null;
		state.styleClipboard = null;
		canvasEditor.clear();
		setPan(false);
		await openDocument({ doc, openSeq, skipAutoFonts: true, pageIndex, fitMode, scale,
			keepPreview: true, preserveThumbnails: true, previewAnchor: anchor });
	} catch (err) {
		if (openSeq === state.openSeq) showError(err, false);
	} finally {
		if (openSeq === state.openSeq) {
			setBusy(false);
			updateControls();
			if (state.editing) updatePageListCurrent(true);
		}
	}
}

function confirmTextReflow(item) {
	return canEditObject(item, "reflow") && (canEditObject(item, "layoutKnown")
		|| window.confirm("将重新排版原文，是否继续？"));
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
	clearSecurityForms();
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
		state.composite = null;
		state.objectClipboard = null;
		state.editorViews.clear();
		state.styleClipboard = null;
		state.fontRenderPending = false;
		state.annotationCreate = null;
		canvasEditor.setTool("");
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

function editAnnotationDetails(item) {
	state.annotationEdit = item;
	el.annotationRemark.value = item.remark || "";
	el.annotationCreator.value = item.creator || "";
	el.annotationFields.hidden = el.annotationSubmit.hidden = false;
	el.annotationDetail.hidden = true;
	el.annotationStatus.textContent = "";
	el.annotationClose.textContent = "取消";
	el.annotationNote.showModal();
	el.annotationRemark.focus();
}

function openObjectPicker(items = canvasEditor.pageItems(state.pageIndex)) {
	el.objectPickerList.replaceChildren();
	const labels = { TextObject: "文字", PathObject: "图形", ImageObject: "图片", CompositeObject: "组合", CompositeGraphicUnit: "组合", Annotation: "注解" };
	const kinds = { Path: "批注", Stamp: "印章", Watermark: "水印", Link: "链接", Highlight: "高亮" };
	for (const item of [...items].reverse()) {
		const button = document.createElement("button");
		button.className = "button";
		button.type = "button";
		button.textContent = `${kinds[item.annotationType] || labels[item.type] || "对象"} · ${item.text || item.remark || item.id.replace("annotation:", "")}`;
		button.title = button.textContent;
		button.addEventListener("click", () => {
			el.objectPicker.close();
			canvasEditor.select(item);
			canvasEditor.focus();
		});
		el.objectPickerList.append(button);
	}
	if (!items.length) { setStatus("页面暂无对象"); return; }
	el.objectPicker.showModal();
}

function openAnnotationCreate(kind, index, box = null, item = null, info = null) {
	const page = state.doc.pages[index];
	const width = Math.min(60, page.width), height = 12;
	state.annotationCreate = { kind, index, item, info, openSeq: state.openSeq, height: box?.height || height };
	annotationFields.CreateForm.reset();
	annotationFields.Text.value = kind === "watermark" ? "水印" : "";
	annotationFields.Pages.value = String(index + 1);
	annotationFields.Target.max = state.doc.pageCount;
	annotationFields.Target.value = String(index + 1);
	annotationFields.LinkKind.querySelector('[value="keep"]').hidden = !item;
	if (kind === "link" && info) {
		annotationFields.LinkKind.value = info.linkKind;
		annotationFields.Address.value = info.linkURI || "";
		if (info.linkPage) annotationFields.Target.value = String(info.linkPage);
	}
	annotationFields.X.value = box?.x ?? (page.width - width) / 2;
	annotationFields.Y.value = box?.y ?? (page.height - height) / 2;
	annotationFields.Width.value = box?.width || width;
	annotationFields.Angle.value = kind === "watermark" ? -30 : 0;
	annotationFields.Opacity.value = kind === "watermark" ? 80 : 0;
	annotationFields.Old.replaceChildren(...(info?.texts || []).map(text => new Option(text, text)));
	if (kind === "replace") annotationFields.Text.value = info?.texts[0] || "";
	el.annotationCreateStatus.textContent = "";
	el.annotationCreate.setAttribute("aria-label", {note:"添加批注",watermark:"添加水印",stamp:"添加印章",link: item ? "修改链接" : "添加链接",replace:"替换文字"}[kind]);
	updateAnnotationFields();
	el.annotationCreate.showModal();
	const focus = [annotationFields.Text, annotationFields.Image, annotationFields.Address].find(input => !input.disabled);
	focus?.focus();
}

function pickWatermarkArea() {
	state.annotationCreate.picking = true;
	annotationFields.Placement.value = state.annotationCreate.area ? "custom" : "page";
	el.annotationCreate.close();
	canvasEditor.select(null);
	toggleDrawingTool("annotation:watermark");
}

function placeAnnotation(index, kind, box) {
	const entry = state.annotationCreate;
	if (kind !== "watermark" || !entry?.picking) {
		openAnnotationCreate(kind, index, box);
		return;
	}
	entry.picking = false;
	const page = state.doc.pages[index], tile = annotationFields.Tile.value === "tile";
	const x = Math.max(0, Math.min(1, box.x / page.width));
	const y = Math.max(0, Math.min(1, box.y / page.height));
	const right = Math.max(x, Math.min(1, (box.x + box.width) / page.width));
	const bottom = Math.max(y, Math.min(1, (box.y + box.height) / page.height));
	if (!tile || right > x && bottom > y) {
		entry.index = index;
		entry.area = tile ? { x, y, w: right - x, h: bottom - y } : { x: (x + right) / 2, y: (y + bottom) / 2, w: 0, h: 0 };
		annotationFields.Placement.value = "custom";
		if (annotationFields.Scope.value !== "custom") annotationFields.Pages.value = String(index + 1);
		el.annotationCreateStatus.textContent = "";
	} else {
		el.annotationCreateStatus.textContent = "范围过小";
	}
	el.annotationCreate.showModal();
}

function updateAnnotationFields() {
	const { kind, item } = state.annotationCreate;
	for (const key of ["X", "Y", "Width", "Height", "Pages", "Author"]) annotationFields[key].closest(".form-row").hidden = false;
	for (const row of el.annotationCreate.querySelectorAll("[data-annotation]")) row.hidden = !row.dataset.annotation.split(" ").includes(kind);
	const image = kind === "stamp" || kind === "watermark" && annotationFields.Content.value === "image";
	annotationFields.ImageRow.hidden = !image;
	annotationFields.Image.required = image;
	annotationFields.TextRow.hidden = !["note", "replace", "watermark"].includes(kind) || image;
	annotationFields.AddressRow.hidden = kind !== "link" || annotationFields.LinkKind.value !== "uri";
	annotationFields.TargetRow.hidden = kind !== "link" || annotationFields.LinkKind.value !== "page";
	annotationFields.Height.value = image ? "" : state.annotationCreate.height;
	annotationFields.Height.placeholder = image ? "等比" : "";
	if (kind === "watermark") {
		for (const key of ["X", "Y"]) annotationFields[key].closest(".form-row").hidden = true;
		annotationFields.Pages.closest(".form-row").hidden = annotationFields.Scope.value !== "custom";
		const tile = annotationFields.Tile.value === "tile";
		annotationFields.PlacementLabel.textContent = tile ? "范围" : "位置";
		annotationFields.Placement.options[0].textContent = tile ? "整页" : "居中";
		annotationFields.Placement.options[1].textContent = tile ? "选区" : "指定";
		annotationFields.Pick.textContent = tile ? "框选" : "定位";
	}
	if (kind === "link" && item) {
		for (const key of ["X", "Y", "Width", "Height", "Pages", "Author"]) annotationFields[key].closest(".form-row").hidden = true;
	}
	for (const input of annotationFields.CreateForm.querySelectorAll("input,select,textarea")) {
		input.disabled = Boolean(input.closest("[hidden]")) || image && input === annotationFields.Height;
	}
}

async function editCanvasObject(item) {
	if (!state.editing || document.body.hasAttribute("aria-busy") || canvasEditor.input || item.items) return;
	if (canEditObject(item, "enter")) return enterCompositeScope(item.index, item.id);
	if (item.type === "Annotation") {
		editAnnotationDetails(item);
		return;
	}
	if (item.type === "TextObject") {
		if (!canEditObject(item, "textContent")) {
			if (!item.scoped && canEditObject(item, "rewriteText")) {
				state.sourceText = { item, openSeq: state.openSeq };
				el.sourceTextValue.value = item.text;
				el.sourceTextStatus.textContent = "";
				el.sourceTextPreview.replaceChildren();
				el.sourceTextPanel.showModal();
				el.sourceTextValue.focus();
				await previewPositionedText();
			}
			return;
		}
	} else if (!canEditObject(item, item.type === "ImageObject" ? "replaceImage" : "update")) return;
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
		const source = canEditObject(item, "layoutKnown") ? null : await callWASM("ofdgoPreviewText", item.index, item.id, item.text);
		if (openSeq === state.openSeq && item.node.isConnected) {
			canvasEditor.editText(item, face, source);
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

async function previewPositionedText() {
	const request = state.sourceText, value = el.sourceTextValue.value;
	if (!request || !el.sourceTextPanel.open) return;
	try {
		const svg = await callWASM("ofdgoPreviewPositionedText", request.item.index, request.item.id, value);
		if (state.sourceText !== request || request.openSeq !== state.openSeq || el.sourceTextValue.value !== value) return;
		el.sourceTextPreview.replaceChildren(parseSVG(svg));
		el.sourceTextStatus.textContent = "";
	} catch (error) {
		if (state.sourceText === request && request.openSeq === state.openSeq && el.sourceTextValue.value === value) {
			el.sourceTextPreview.replaceChildren();
			el.sourceTextStatus.textContent = error.message;
		}
	}
}

function mountEditorObjects(index, page, surface) {
	const scope = state.composite?.index === index ? state.composite : null;
	canvasEditor.mount(index, { ...page, objects: scope ? scope.objects : page.objects.filter(item => state.renderAnnotations || item.type !== "Annotation") }, surface);
}

function changeAnnotations(item, action, ...args) {
	return changeDocument("ofdgoChangeAnnotations", { ...item, id: item.items ? item.id : [item.id] }, action, ...args);
}

function resetCompositeScope() {
	const scope = state.composite;
	if (!scope) return;
	state.composite = null;
	canvasEditor.clear();
	const page = state.pageCache.get(scope.index), surface = pageShell(scope.index)?.querySelector(".page-surface");
	if (page && surface) mountEditorObjects(scope.index, page, surface);
}

async function enterCompositeScope(index, key) {
	const openSeq = state.openSeq;
	setBusy(true);
	try {
		const objects = await callWASM("ofdgoCompositeObjects", index, key);
		if (openSeq !== state.openSeq) return;
		canvasEditor.clear();
		canvasEditor.setTool("");
		state.composite = { index, key, objects };
		const page = state.pageCache.get(index), surface = pageShell(index)?.querySelector(".page-surface");
		if (page && surface) mountEditorObjects(index, page, surface);
		updateControls();
		setStatus(pageStatus(index, state.doc.pageCount));
		canvasEditor.focus();
	} catch (err) {
		if (openSeq === state.openSeq) showError(err, false);
	} finally {
		if (openSeq === state.openSeq) setBusy(false);
	}
}

async function exitCompositeScope() {
	const scope = state.composite;
	if (!scope) return;
	const split = scope.key.lastIndexOf("/");
	if (split >= 0) return enterCompositeScope(scope.index, scope.key.slice(0, split));
	resetCompositeScope();
	updateControls();
	canvasEditor.focus();
}

async function startImageCrop(item) {
	if (item.scoped) {
		canvasEditor.startCrop(item, { svg: item.surface.querySelector(".ofd-svg").cloneNode(true), urls: [] });
		return;
	}
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
	el.insertFitRow.hidden = !item || Boolean(item.scoped);
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
	canvasEditor.focus();
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
		fonts.unshift({ id: `embedded:${item.font}`, name: item.fontName || item.font, embedded: true, disabled: !canEditObject(item, "textContent") });
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
				setStatus(err?.name === "NotAllowedError" ? "系统字体未授权" : String(err.message || err));
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
	const before = editorLocation(), revision = state.editorInfo.revision;
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
		const doc = item?.scoped
			? await callWASM("ofdgoChangeCompositeObjects", item.index, state.composite.key, [item.id], "image", data)
			: item ? await callWASM("ofdgoReplaceImage", item.index, item.id, data, el.insertFit.value)
			: await callWASM("ofdgoInsertImage", index, data, x, y, page.width - x * 2, state.composite?.key || "");
		if (openSeq !== state.openSeq) {
			return;
		}
		el.insertPanel.close();
		if (item && doc.revision === state.editorInfo?.revision) {
			canvasEditor.focus();
			return;
		}
		setEditorInfo(doc);
		canvasEditor.pendingSelection = item ? null : { index, ids: doc.selectedIDs };
		state.selectObjects = true;
		setPan(false);
		openSeq = ++state.openSeq;
		await refreshEditorPage(doc, index, openSeq);
		if (openSeq === state.openSeq) {
			rememberEditorView(before, revision, !item && !!state.composite);
			canvasEditor.focus();
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
	if (state.composite && color) {
		if (color && fill !== null && canEditObject(item, "paint")) {
			await changeDocument("ofdgoCompositeTextColor", item, fill);
		}
		updateObjectControls(canvasEditor.selected, true);
		return;
	}
	if (item.items) {
		if (color ? !canEditObject(item, "paint") : !canEditObject(item, "layoutKnown") || !canEditObject(item, "reflow")) return;
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
		if (color ? !canEditObject(item, "paint") : !confirmTextReflow(item)) {
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

function selectedPath(item) {
	if (!item?.items) return item?.type === "PathObject" ? item : null;
	if (!item.items.every(member => member.type === "PathObject")) return null;
	const path = { ...item, type: "PathObject", shape: item.items.every(member => lineShape(member.shape)) ? "line" : "" };
	for (const key of ["fill", "stroke", "fillColor", "strokeColor", "lineWidth"]) {
		const value = item.items[0][key];
		path[key] = item.items.every(member => member[key] === value) ? value : undefined;
	}
	return path;
}

async function changeShapeStyle({ target }) {
	const item = selectedPath(canvasEditor.selected);
	if (!state.editing || document.body.hasAttribute("aria-busy")) {
		return;
	}
	if (canvasEditor.selected && (!item || !canEditObject(item, "paint"))) return;
	if (target === el.shapeWidth && (!el.shapeWidth.checkValidity() || Number(el.shapeWidth.value) <= 0)) {
		el.shapeWidth.value = item ? item.lineWidth === undefined ? "" : String(displayPoints(item.lineWidth)) : "1";
		return;
	}
	if (item) {
		const values = [el.shapeFill, el.shapeFillColor, el.shapeStroke, el.shapeStrokeColor, el.shapeWidth].map(input => {
			if (input !== target) return null;
			if (input === el.shapeFill || input === el.shapeStroke) return input.checked;
			return input === el.shapeWidth ? inputMillimeters(input, item.lineWidth) : input.value;
		});
		await changeDocument("ofdgoUpdatePathStyle", { ...item, id: (item.items || [item]).map(member => member.id) }, ...values);
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
	canvasEditor.focus();
}

function updateDrawingControls() {
	const tool = canvasEditor.tool;
	const path = selectedPath(canvasEditor.selected);
	el.insertTextButton.setAttribute("aria-pressed", String(tool === "text"));
	const line = lineShape(tool) || tool.startsWith("pen-") || !tool && lineShape(path?.shape);
	if (line) {
		el.shapeFill.checked = false;
		el.shapeStroke.checked = true;
	} else if (!path && !el.shapeFill.checked && !el.shapeStroke.checked) {
		el.shapeStroke.checked = true;
	}
	const disabled = !state.editing || !state.ready || state.exporting;
	el.arrowTool.disabled = disabled || !pageCan("insert");
	el.penTool.disabled = el.arrowTool.disabled;
	el.penTool.classList.toggle("active", tool.startsWith("pen-"));
	el.annotationTool.disabled = disabled;
	for (const [button, name] of [[el.drawLineButton, "line"], [el.drawRectangleButton, "rectangle"], [el.drawEllipseButton, "ellipse"]]) {
		button.disabled = disabled || !pageCan("insert");
		button.setAttribute("aria-pressed", String(tool === name));
	}
	el.eraseButton.disabled = el.eraseMode.disabled = disabled;
	el.eraseButton.setAttribute("aria-pressed", String(tool.startsWith("erase-")));
	el.selectObjectButton.setAttribute("aria-pressed", String(canvasEditor.enabled && !tool));
	const styleDisabled = disabled || (canvasEditor.selected ? !path || !canEditObject(path, "paint") : !pageCan("insert"));
	el.shapeFill.disabled = el.shapeStroke.disabled = styleDisabled || line;
	el.shapeFillColor.disabled = styleDisabled || line || !el.shapeFill.checked && !el.shapeFill.indeterminate;
	el.shapeStrokeColor.disabled = el.shapeWidth.disabled = styleDisabled || !line && !el.shapeStroke.checked && !el.shapeStroke.indeterminate;
}

async function changeDocument(name, item, ...args) {
	if (!state.editing || document.body.hasAttribute("aria-busy")) {
		return;
	}
	let openSeq = state.openSeq;
	const active = document.activeElement;
	const toolbarFocus = el.editorTools.contains(active) ? active : null;
	const previous = currentPageInfo();
	const before = editorLocation(), revision = state.editorInfo.revision;
	const restoring = name === "ofdgoUndo" || name === "ofdgoRedo";
	const { scrollLeft, scrollTop } = el.viewerPanel;
	const importing = name === "ofdgoImportPages";
	if (importing) state.importing = true;
	setBusy(true, importing ? "正在导入" : "", null);
	try {
		args = await Promise.all(args);
		if (openSeq !== state.openSeq) {
			return;
		}
		const scope = state.composite;
		const operation = { ofdgoTransformObject: "transform", ofdgoTransformObjects: "transform", ofdgoRotateObjects: "rotate", ofdgoFlipObjects: "flip", ofdgoResizeObjects: "resize",
			ofdgoAlignObject: "align", ofdgoAlignObjects: "align", ofdgoDistributeObjects: "distribute", ofdgoStyleObjects: "style", ofdgoUpdatePathStyle: "paint", ofdgoCompositeTextColor: "textColor",
			ofdgoUpdateText: "text", ofdgoStyleText: "textStyle", ofdgoCropImage: "crop", ofdgoLayoutText: "layout", ofdgoFitImage: "fit", ofdgoResetCompositeCrop: "resetCrop",
			ofdgoReshapeObject: "reshape", ofdgoReshapeLine: "line", ofdgoEraseObjects: "erase", ofdgoEraseObjectsPath: "erasePath", ofdgoPasteStyle: "pasteStyle",
			ofdgoDeleteObject: "delete", ofdgoDeleteObjects: "delete", ofdgoCopyObjects: "copy", ofdgoOrderObjects: "order", ofdgoGroupObjects: "group" }[name];
		const members = item?.items || (item ? [item] : []);
		const scoped = scope && members.length && members.every(member => member.scoped);
		const ids = item ? Array.isArray(item.id) ? item.id : members.map(member => member.id) : [];
		const annotated = !scoped && ids.some(id => id.startsWith("annotation:"));
		const inserting = ["ofdgoInsertShape", "ofdgoInsertText", "ofdgoInsertImage", "ofdgoInsertInk"].includes(name);
		const editingAnnotation = name === "ofdgoWriteAnnotation" && Boolean(state.annotationCreate?.item);
		if (scope && inserting) args.push(scope.key);
		if (scoped && !operation) throw new Error("内部对象暂不支持此操作");
		const doc = scoped
			? await callWASM("ofdgoChangeCompositeObjects", item.index, scope.key, Array.isArray(item.id) ? item.id : members.map(member => member.id), operation, ...args)
			: annotated && operation ? await callWASM("ofdgoChangeSelection", item.index, ids, operation, ...args)
			: await callWASM(name, ...(item ? [item.index, item.id] : []), ...args);
		if (openSeq !== state.openSeq) {
			return;
		}
		if (doc.revision === state.editorInfo?.revision) {
			return true;
		}
		const view = name === "ofdgoUndo" ? state.editorViews.get(revision) : name === "ofdgoRedo" ? state.editorViews.get(doc.revision) : null;
		const reindex = !!scope && (scoped && ["ofdgoDeleteObject", "ofdgoDeleteObjects", "ofdgoCopyObjects", "ofdgoOrderObjects", "ofdgoEraseObjects", "ofdgoEraseObjectsPath", "ofdgoGroupObjects"].includes(name)
			|| inserting || name === "ofdgoPasteObjects" && !!args[4]);
		const reindexed = restoring ? view?.reindex && view.before : reindex && before;
		const clipboard = state.objectClipboard;
		if (reindexed && clipboard?.scope?.startsWith(reindexed.scope + "/") && clipboard.page === reindexed.page) state.objectClipboard = null;
		const location = name === "ofdgoUndo" ? view?.before : view?.after;
		const restoredIndex = location ? doc.pages.findIndex(page => page.id === location.page) : -1;
		if (restoredIndex >= 0) {
			state.composite = location.scope ? { index: restoredIndex, key: location.scope, objects: [] } : null;
		} else if (!item && !inserting && !editingAnnotation && !(name === "ofdgoPasteObjects" && args[4])) {
			resetCompositeScope();
		}
		if (restoring && JSON.stringify(doc.outlines) !== JSON.stringify(state.doc.outlines)) {
			state.outlineSelection = null;
			state.outlineExpanded.clear();
		}
		setEditorInfo(doc);
		if (name === "ofdgoUpdateInfo") {
			Object.assign(state.doc, { title: doc.title, author: doc.author, subject: doc.subject, customData: doc.customData });
			renderMeta();
			updateControls();
			rememberEditorView(before, revision);
			return true;
		}
		if (name === "ofdgoChangeOutline" || name === "ofdgoMoveOutline") {
			remapOutlineExpansion(name, args, doc.outlinePath);
			state.doc.outlines = doc.outlines;
			state.outlineSelection = doc.outlinePath;
			renderOutlines(false);
			showNavigation(el.outlinesTab);
			updateControls();
			rememberEditorView(before, revision);
			return true;
		}
		const clearSelection = !item && !editingAnnotation || name === "ofdgoChangeAnnotations" && args[0] === "delete"
			|| ["ofdgoDeleteObject", "ofdgoDeleteObjects", "ofdgoEraseObjects", "ofdgoEraseObjectsPath"].includes(name);
		if (name === "ofdgoCopyObjects" || name === "ofdgoPasteObjects" || name === "ofdgoGroupObjects" || inserting || name === "ofdgoWriteAnnotation" && !editingAnnotation) {
			state.selectObjects = true;
			if (name !== "ofdgoInsertInk") canvasEditor.setTool("");
			setPan(false);
			canvasEditor.pendingSelection = { index: item?.index ?? args[0], ids: name === "ofdgoInsertInk" ? [] : doc.selectedIDs };
		}
		if (scoped && name === "ofdgoOrderObjects") canvasEditor.pendingSelection = { index: item.index, ids: doc.selectedIDs };
		openSeq = ++state.openSeq;
		if (item || name === "ofdgoPasteObjects" || inserting || editingAnnotation) {
			await refreshEditorPage(doc, item ? item.index : args[0], openSeq, clearSelection);
		} else {
			const samePage = doc.pages.findIndex((page) => page.id === previous.id);
			const pageIndex = restoredIndex >= 0 ? restoredIndex : doc.pageIndex ?? (samePage < 0 ? Math.min(state.pageIndex, doc.pageCount - 1) : samePage);
			const page = doc.pages[pageIndex];
			const keepScroll = doc.pageIndex === undefined && page.id === previous.id && pageIndex === state.pageIndex
				&& page.width === previous.width && page.height === previous.height;
			await openDocument({ doc, openSeq, skipAutoFonts: true, pageIndex, fitMode: state.fitMode, scale: state.scale,
				keepPreview: true, clearSelection, selection: restoredIndex >= 0 ? { index: restoredIndex, ids: location.ids } : null,
				previewScroll: keepScroll ? { scrollLeft, scrollTop } : null });
		}
		if (openSeq === state.openSeq) {
			if (!restoring) rememberEditorView(before, revision, reindex);
			if (!toolbarFocus) canvasEditor.focus();
			return true;
		}
	} catch (err) {
		if (openSeq === state.openSeq) {
			if (el.annotationCreate.open) {
				el.annotationCreateStatus.textContent = err.message;
			} else if (el.annotationNote.open) {
				el.annotationStatus.textContent = err.message;
			} else if (el.batchPagesPanel.open) {
				el.batchPagesStatus.textContent = err.message;
			} else if (el.objectStylePanel.open) {
				el.objectStyleStatus.textContent = err.message;
			} else if (el.sourceTextPanel.open) {
				el.sourceTextStatus.textContent = err.message;
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
		if (importing) {
			state.importing = false;
			state.exportRequestID = 0;
			el.cancelExportButton.hidden = true;
			el.importCancel.disabled = false;
		}
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
			renderMeta(true);
			updateControls();
			loadDocumentDetails(openSeq);
			for (const shell of el.svgHost.children) observeFlowPage(shell);
			for (const button of el.pageList.children) observeThumbnail(button, Number(button.dataset.pageIndex), openSeq);
		}
	}
}

function releasePageResources() {
	const images = new Set(), fonts = new Set();
	for (const page of state.pageCache.values()) {
		for (const name of page.imageNames) images.add(name);
		for (const font of page.fonts) fonts.add(font.name);
	}
	for (const [name, image] of state.svgImages) {
		if (!images.has(name)) {
			URL.revokeObjectURL(image.url);
			state.svgImages.delete(name);
		}
	}
	for (const [name, font] of state.svgFonts) {
		if (!fonts.has(name)) {
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
		setStatus("一次拖入一个文件");
		return;
	}
	const file = event.dataTransfer.files[0];
	if (file && isDocumentFile(file)) {
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
		syncThumbnailWindow();
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
	batch.formats = await callWASM("ofdgoOutputFormats");
	batchElements.Format.replaceChildren(...batch.formats.map(format => new Option(format.label, format.value)));
	updateBatchControls();
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
			} else if (data.type === "conversion") {
				if (wasmRequests.get(data.id)?.openSeq === state.openSeq && !el.cancelExportButton.disabled) {
					if (data.phase === "open") setProgress("正在读取 PDF", null);
					else if (data.phase === "pages") setProgress(data.completed ? `正在整理 ${data.completed} 页` : "正在整理页面", null);
					else if (data.phase === "convert") setProgress(`正在转换 ${data.completed} / ${data.total} 页`, data.total ? 28 + data.completed / data.total * 22 : null);
					else if (data.phase === "write.fonts") setProgress("正在处理字体", null);
					else setProgress("正在生成 OFD", null);
				}
			} else if (data.type === "export") {
				if (wasmRequests.get(data.id)?.openSeq === state.openSeq && state.exporting) {
					if (data.stage === "save") {
						el.cancelExportButton.disabled = true;
						if (state.signing) el.signCancel.disabled = true;
						setProgress("正在保存", null);
					} else if (!el.cancelExportButton.disabled) {
						if (data.stage === "prepare") {
							const label = { snapshot: "正在准备", ids: "正在检查", commit: "正在整理", fonts: "正在处理字体", pages: "正在处理页面", references: "正在检查引用", resources: "正在整理资源", write: "正在写入", sign: "正在签署", encrypt: "正在加密" }[data.phase];
							setProgress(label, data.total ? data.completed / data.total * 100 : null);
						} else if (data.completed === data.total) {
							setProgress("正在收尾", null);
						} else {
							setProgress(`正在导出 ${data.completed} / ${data.total} 页`, data.completed / data.total * 100);
						}
					}
				}
			} else if (data.type === "import") {
				if (wasmRequests.get(data.id)?.openSeq === state.openSeq && state.importing && !el.importCancel.disabled) {
					const label = { ids: "正在检查", pages: "正在导入", resources: "正在读取", commit: "正在完成" }[data.phase];
					el.importStatus.textContent = data.total ? `${label} ${data.completed} / ${data.total} 页` : label;
					setProgress(label, data.total ? data.completed / data.total * 100 : null);
					if (data.phase === "commit") el.importCancel.disabled = el.cancelExportButton.disabled = true;
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
					const err = new Error(missingGlyphMessage(data.missingGlyphs) || (data.reasonCode === "layoutRequired" ? "跨段或换行修改需先设置段落排版" : data.error));
					err.code = data.code;
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
	clearSecurityForms();
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
	if (!isDocumentFile(file)) {
		el.ofdInput.value = "";
		showError(new Error("选择 OFD 文件"), !state.doc);
		return;
	}
	if (!discardChanges()) {
		return;
	}
	clearSecurityForms();
	state.wasmRecoveries = 0;
	const openSeq = ++state.openSeq;
	setBusy(true, "正在读取文档", 10, STATUS.opening);
	try {
		let bytes = new Uint8Array(await file.arrayBuffer());
		let warnings = [];
		if (openSeq !== state.openSeq) {
			return;
		}
		if (/\.pdf$/i.test(file.name || "")) {
			setProgress("正在转换 PDF", 25);
			await ensureWASM();
			if (openSeq !== state.openSeq) return;
			let converted;
			try {
				converted = await callWASM("ofdgoConvertPDF", bytes);
			} finally {
				if (openSeq === state.openSeq) {
					state.exportRequestID = 0;
					el.cancelExportButton.hidden = true;
				}
			}
			if (openSeq !== state.openSeq) return;
			bytes = converted.bytes;
			warnings = JSON.parse(converted.warnings || "null") || [];
		}
		state.ofdBytes = bytes;
		state.conversionWarnings = warnings;
		state.fileName = (file.name || "ofdgo.ofd").replace(/\.pdf$/i, ".ofd");
		state.editing = false;
		state.composite = null;
		state.annotationCreate = null;
		state.pageSelection.clear();
		state.pageSelectionAnchor = null;
		state.pageMultiSelect = false;
		state.objectClipboard = null;
		state.styleClipboard = null;
		state.editorInfo = null;
		state.editorViews.clear();
		state.fontRenderPending = false;
		state.savedRevision = null;
		canvasEditor.clear();
		setDirty(false);
		el.createPanel.close();
		el.insertPanel.close();
		el.pagePanel.close();
		el.paragraphPanel.close();
		el.infoPanel.close();
		el.annotationNote.close();
		el.annotationCreate.close();
		el.objectPicker.close();
		el.importPanel.close();
		el.batchPagesPanel.close();
		el.objectStylePanel.close();
		el.sourceTextPanel.close();
		el.objectBoundsPanel.close();
		el.outlinePanel.close();
		updateControls();
		await openDocument({ pageIndex: 0, resetScroll: true, openSeq });
	} catch (err) {
		if (openSeq === state.openSeq) {
			if (err.name === "AbortError") setStatus("转换已取消");
			else showError(err, !state.doc);
			setBusy(false);
		}
	}
}

function isDocumentFile(file) {
	return /\.(ofd|pdf)$/i.test(file.name || "");
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
		setStatus(saved ? "字体已保存" : "字体未保存，仅本次可用");
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
		setStatus("浏览器不支持系统字体");
		return;
	}
	setBusy(true, state.doc ? "正在匹配字体" : "正在请求授权", 12, state.doc ? STATUS.fonts : "正在请求授权");
	try {
		await nextFrame();
		const available = await fontManager.queryLocal();
		if (!state.doc) {
			setStatus(available.length ? `已授权${available.length}种字体` : "暂无系统字体");
			return;
		}
		if (await loadDocumentLocalFonts(available)) {
			await applyFontChange();
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
	if (!fontManager.canReadLocal() || fontManager.catalogLoaded || fontManager.permission === "denied") {
		return;
	}
	setBusy(true, "正在请求授权", 12, "正在请求授权");
	try {
		const available = await fontManager.queryLocal();
		setStatus(available.length ? `已授权${available.length}种字体` : "暂无系统字体");
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
			setStatus("系统字体未授权");
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
		setStatus("字体均已内嵌");
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
	setStatus(fonts.length ? `已加载${fonts.length}种字体` : emptyStatus);
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

function displayMode() {
	return state.editing ? "svg" : state.renderMode;
}

function updateDisplayControls() {
	const disabled = !state.ready || !state.doc || state.exporting || document.body.hasAttribute("aria-busy");
	el.renderVectorButton.disabled = el.renderRasterButton.disabled = disabled || state.editing;
	el.renderVectorButton.setAttribute("aria-pressed", String(displayMode() === "svg"));
	el.renderRasterButton.setAttribute("aria-pressed", String(displayMode() === "raster"));
	el.renderVectorButton.title = state.editing ? "编辑使用矢量显示" : "矢量显示";
	el.renderRasterButton.title = state.editing ? "阅读时可用" : "位图显示";
	updateDPIControl();
}

async function refreshPageDisplay(anchor = scaleAnchor(0)) {
	const selection = state.editing ? { index: canvasEditor.selected?.index ?? state.pageIndex, ids: canvasEditor.items().map(item => item.id) } : null;
	await openDocument({
		pageIndex: state.pageIndex, fitMode: state.fitMode, scale: state.scale,
		skipAutoFonts: true, reuseSession: true, keepPreview: true, keepSearch: true,
		previewAnchor: anchor, selection, throwError: true,
	});
}

async function changeDisplayMode(mode = state.renderMode) {
	if (document.body.hasAttribute("aria-busy")) {
		el.imageDPI.value = String(state.renderDPI);
		updateDPIControl();
		return;
	}
	if (state.editing) mode = state.renderMode;
	const dpi = currentImageDPI();
	if (state.renderDPI === dpi && state.renderMode === mode) return;
	const previous = { dpi: state.renderDPI, mode: state.renderMode };
	state.renderDPI = dpi;
	state.renderMode = mode;
	updateDisplayControls();
	if (!state.doc || state.editing || (previous.mode === mode && mode === "svg")) return;
	try {
		await refreshPageDisplay();
	} catch (err) {
		state.renderDPI = previous.dpi;
		state.renderMode = previous.mode;
		el.imageDPI.value = String(previous.dpi);
		resetPageLoading();
		state.pageCache.clear();
		updateDisplayControls();
		showError(err, false);
	}
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
	el.localFontButton.title = supported ? "读取系统字体" : "浏览器不支持系统字体";
	updateFontPermissionHint();
}

function updateFontPermissionHint() {
	el.fontPermissionHint.hidden = !fontManager.canReadLocal() || fontManager.permission === "granted";
}

function clearSecurityForms() {
	finishCredentials(null);
	clearEncryptionForm();
	el.signKey.value = "";
	el.signKeyPassword.value = "";
	if (el.signPanel.open) el.signPanel.close();
	if (el.verifyPanel.open) el.verifyPanel.close();
}

function updateCredentialsType() {
	const certificate = el.credentialsType.value === "certificate";
	el.credentialsStatus.textContent = "";
	el.credentialsPasswordRow.hidden = certificate;
	el.credentialsCertificateRow.hidden = el.credentialsKeyRow.hidden = !certificate;
	el.credentialsKeyPasswordRow.hidden = !certificate;
	el.credentialsPassword.required = !certificate;
	el.credentialsCertificate.required = el.credentialsKey.required = certificate;
	el.credentialsPassword.value = "";
	el.credentialsCertificate.value = el.credentialsKey.value = "";
	el.credentialsKeyPassword.value = "";
}

function finishCredentials(credentials) {
	const request = credentialRequest;
	credentialRequest = null;
	el.credentialsPassword.value = "";
	el.credentialsUser.value = "";
	el.credentialsCertificate.value = el.credentialsKey.value = "";
	el.credentialsKeyPassword.value = "";
	if (el.credentialsPanel.open) el.credentialsPanel.close();
	if (request && request.openSeq === state.openSeq) request.resolve(credentials);
	else {
		credentials?.password.fill(0);
		credentials?.key?.fill(0);
		credentials?.keyPassword?.fill(0);
		request?.resolve(null);
	}
}

function requestCredentials(code, openSeq) {
	finishCredentials(null);
	el.credentialsStatus.textContent = code === "invalidCredentials" ? "解锁失败，请重试" : "";
	el.progressPanel.hidden = true;
	return new Promise(resolve => {
		credentialRequest = { resolve, openSeq };
		el.credentialsPanel.showModal();
		(el.credentialsType.value === "certificate" ? el.credentialsCertificate : el.credentialsPassword).focus();
	});
}

async function openWithCredentials(fonts, openSeq, data = state.ofdBytes, importing = false) {
	let credentials = null;
	try {
		while (openSeq === state.openSeq) {
			try {
				return importing ? await callWASM("ofdgoLoadImport", data.slice(), credentials)
					: await callWASM("ofdgoOpen", data, fonts, state.renderAnnotations, credentials);
			} catch (err) {
				if (openSeq !== state.openSeq || !["credentialsRequired", "invalidCredentials"].includes(err.code)) throw err;
				credentials?.password.fill(0);
				credentials?.key?.fill(0);
				credentials?.keyPassword?.fill(0);
				credentials = await requestCredentials(err.code, openSeq);
				if (!credentials || openSeq !== state.openSeq) {
					const canceled = new Error("打开已取消");
					canceled.name = "AbortError";
					throw canceled;
				}
			}
		}
		const canceled = new Error("打开已取消");
		canceled.name = "AbortError";
		throw canceled;
	} finally {
		credentials?.password.fill(0);
		credentials?.key?.fill(0);
		credentials?.keyPassword?.fill(0);
	}
}

async function openDocument(options = {}) {
	if (!state.ofdBytes && !state.editorInfo) {
		return;
	}
	const openSeq = options.openSeq || (state.openSeq += 1);
	const previewPosition = options.previewScroll && state.pageWindow ? pageWindowAnchor(state.pageWindow) : null;
	const viewport = options.keepPreview ? el.viewerPanel.getBoundingClientRect() : null;
	const previewIDs = viewport ? [...state.visiblePages].filter((index) => {
		const bounds = pageShell(index)?.getBoundingClientRect();
		return bounds && bounds.bottom > viewport.top && bounds.top < viewport.bottom;
	}).map((index) => state.doc.pages[index].id) : [];
	const thumbnailPreviews = options.preserveThumbnails ? new Map([...state.pageCache.values()]
		.filter((page) => page.id !== state.doc.pages[state.pageIndex].id).map((page) => [page.id, page])) : null;
	if (!state.ready || state.wasmExited) {
		await ensureWASM();
	}
	if (openSeq !== state.openSeq) {
		return;
	}
	const resetLocalFonts = !options.skipAutoFonts;
	if (!options.keepSearch) resetSearch();
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
			: await openWithCredentials(fonts, openSeq));
		if (openSeq !== state.openSeq) {
			return;
		}
		const pageCount = doc.pageCount || 0;
		const pageIndex = Math.min(Math.max(options.pageIndex || 0, 0), Math.max(pageCount - 1, 0));
		state.doc = doc;
		if (options.resetScroll || !options.skipAutoFonts) {
			state.pageSelection.clear();
			state.pageSelectionAnchor = null;
			state.pageMultiSelect = false;
		}
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
			if (thumbnailPreviews) {
				for (const page of doc.pages) {
					const preview = thumbnailPreviews.get(page.id);
					if (preview) state.pageCache.set(page.index, { ...preview, previewOnly: true });
				}
			}
			const indices = new Set([pageIndex, ...doc.pages.filter((page) => previewIDs.includes(page.id)).map((page) => page.index)]);
			for (const index of indices) {
				await loadPageData(index, { openSeq, priority: 0, refresh: true });
				if (openSeq !== state.openSeq) {
					return;
				}
			}
		}
		if (options.clearSelection) canvasEditor.clear();
		if (options.selection) canvasEditor.pendingSelection = options.selection;
		resetPageFlow(options.keepPreview);
		renderPageList();
		if (options.keepPreview || options.resetScroll || el.outlineList.childElementCount === 0) {
			renderOutlines(!options.keepPreview);
		}
		renderMeta(options.keepPreview);
		renderPageFlow();
		if (options.keepPreview) {
			for (const [index, page] of state.pageCache) {
				if (!page.previewOnly) mountPageSVG(index, page, openSeq);
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
			if (options.previewAnchor) {
				restoreScaleAnchor(options.previewAnchor);
			} else if (options.previewScroll) {
				Object.assign(el.viewerPanel, options.previewScroll);
				if (previewPosition && state.pageWindow) syncPageWindow(state.pageWindow,
					state.pageWindow.offsets[previewPosition.index] + previewPosition.offset);
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
			canvasEditor.focus();
		}
		queueNearbyPages(pageIndex, openSeq);
		loadDocumentDetails(openSeq);
	} catch (err) {
		if (openSeq !== state.openSeq) {
			return;
		}
		if (options.throwError) throw err;
		if (!options.reuseSession && !options.doc) {
			state.ofdBytes = null;
			state.doc = null;
			renderSecurity();
			updateControls();
		}
		showError(err, true);
	} finally {
		if (openSeq === state.openSeq) {
			setBusy(false);
		}
	}
}

async function loadDocumentDetails(openSeq) {
	if (openSeq !== state.openSeq || !state.doc) return;
	try {
		const details = await callWASM("ofdgoDocumentInfo");
		if (openSeq !== state.openSeq) return;
		Object.assign(state.doc, details, { detailsPending: false, detailsError: "" });
		renderMeta();
		while (openSeq === state.openSeq) {
			await waitForPaint();
			if (openSeq !== state.openSeq) return;
			const info = await callWASM("ofdgoDocumentInfo", true);
			if (openSeq !== state.openSeq) return;
			if (!info.detailsPending) {
				Object.assign(state.doc, info, { detailsPending: false, detailsError: "" });
				renderMeta();
				return;
			}
		}
	} catch (err) {
		if (openSeq === state.openSeq) {
			Object.assign(state.doc, { detailsPending: false, detailsError: err.message });
			renderMeta();
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
		} else if (state.pageWindow && (!pageShell(index) || pageShell(index).style.visibility === "hidden")) {
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
	if (state.doc.encryption?.encrypted && !window.confirm("附件将按明文下载，是否继续？")) return;
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
		setStatus(`附件已下载（${formatBytes(result.size)}）`);
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

function updateEncryptionType() {
	const certificates = el.encryptionType.value === "certificate";
	el.encryptionUserRow.hidden = el.encryptionPasswordRow.hidden = el.encryptionConfirmRow.hidden = certificates;
	el.encryptionCertificatesRow.hidden = !certificates;
	el.encryptionPassword.required = el.encryptionConfirm.required = state.exportEncrypted && !certificates;
	el.encryptionCertificates.required = state.exportEncrypted && certificates;
	el.encryptionPassword.value = el.encryptionConfirm.value = "";
	el.encryptionCertificates.value = "";
}

async function encryptionRecipients(files) {
	const names = new Set();
	return Promise.all(files.map(async file => {
		const base = file.name.replace(/\.[^.]+$/, "") || "用户";
		let name = base, index = 2;
		while (names.has(name)) name = `${base} ${index++}`;
		names.add(name);
		return {userName:name, certificate:new Uint8Array(await file.arrayBuffer())};
	}));
}

function clearEncryptionForm() {
	el.encryptionPassword.value = "";
	el.encryptionConfirm.value = "";
	el.encryptionUser.value = "";
	el.encryptionCertificates.value = "";
	state.exportEncrypted = false;
}

function renderSecurity() {
	const encryption = state.doc?.encryption;
	el.securityPanel.hidden = !state.doc;
	el.securityEncryption.textContent = encryption?.encrypted ? "已加密" : "未加密";
	el.securityMethod.textContent = encryption?.method || "";
	el.securityMethodRow.hidden = !encryption?.method;
	el.encryptSaveButton.disabled = !state.doc || !state.ready || state.exporting || document.body.hasAttribute("aria-busy");
	el.verifyButton.disabled = el.encryptSaveButton.disabled;
	el.signButton.disabled = el.encryptSaveButton.disabled;
}

function cancelSigning() {
	if (el.signCancel.disabled) return;
	state.signCanceled = true;
	el.signStatus.textContent = "正在取消";
	cancelExport();
}

function updateSignPlacement() {
	const stamp = Boolean(el.signSeal.files?.length);
	el.signPlacementFields.hidden = !stamp;
	el.signPages.required = stamp;
	el.signX.disabled = !stamp || el.signPlacement.value === "seam";
}

async function signDocument(event) {
	event.preventDefault();
	if (!state.doc || document.body.hasAttribute("aria-busy")) return;
	if (!el.signCertificate.files?.length || !el.signKey.files?.length || !el.signRoots.files?.length) {
		el.signStatus.textContent = "请选择证书、私钥和信任根";
		return;
	}
	if (el.signMode.value === "replace" && !window.confirm("替换现有签名，是否继续？")) return;
	let openSeq = state.openSeq;
	let key;
	const keyPassword = new TextEncoder().encode(el.signKeyPassword.value);
	el.signKeyPassword.value = "";
	state.exporting = true;
	state.signing = true;
	state.signCanceled = false;
	el.signCancel.disabled = false;
	setBusy(true, "正在签署", null, "正在签署");
	el.signForm.querySelectorAll("input, select, button[type=submit]").forEach(input => { input.disabled = true; });
	try {
		const name = `${baseFileName()}_签章.ofd`;
		const file = window.showSaveFilePicker ? await window.showSaveFilePicker({suggestedName:name, types:[{description:"OFD",accept:{"application/ofd":[".ofd"]}}]}) : null;
		if (openSeq !== state.openSeq || state.signCanceled) return;
		if (canvasEditor.input || canvasEditor.crop || canvasEditor.nudge || canvasEditor.nudgeCommit) {
			setBusy(false);
			if (!await canvasEditor.commitNudge() || !await canvasEditor.commitText() || !await canvasEditor.commitCrop()) return;
			openSeq = state.openSeq;
			setBusy(true, "正在签署", null, "正在签署");
		}
		key = (await securityFiles(el.signKey))[0];
		const options = {
			key, keyPassword, certificate:(await securityFiles(el.signCertificate))[0], roots:await securityFiles(el.signRoots),
			intermediates:await securityFiles(el.signIntermediates), seal:(await securityFiles(el.signSeal))[0] || null,
			pages:el.signPages.value.trim(), placement:el.signPlacement.value,
			x:Number(el.signX.value), y:Number(el.signY.value), width:Number(el.signWidth.value), height:Number(el.signHeight.value),
			mode:el.signMode.value, lock:el.signLock.checked,
		};
		el.signKey.value = "";
		if (openSeq !== state.openSeq || state.signCanceled || !el.signPanel.open) return;
		const result = await callWASM("ofdgoSaveSigned", null, options, file);
		if (openSeq !== state.openSeq) return;
		if (result.blob) downloadBytes(result.blob, result.mime, name);
		el.signPanel.close();
		setStatus("签署已另存");
	} catch (err) {
		if (openSeq === state.openSeq) el.signStatus.textContent = err.name === "AbortError" ? "签署已取消" : err.message;
	} finally {
		key?.fill(0);
		keyPassword.fill(0);
		el.signKey.value = "";
		el.signForm.querySelectorAll("input, select, button[type=submit]").forEach(input => { input.disabled = false; });
		el.signCancel.disabled = false;
		if (state.signCanceled && openSeq === state.openSeq) el.signStatus.textContent = "签署已取消";
		state.signing = false;
		updateSignPlacement();
		state.exporting = false;
		state.exportRequestID = 0;
		el.cancelExportButton.hidden = true;
		if (openSeq === state.openSeq) { setBusy(false); updateControls(); }
	}
}

async function securityFiles(input) {
	return Promise.all(Array.from(input.files || [], async file => new Uint8Array(await file.arrayBuffer())));
}

async function verifyDocument(event) {
	event.preventDefault();
	if (!state.doc || document.body.hasAttribute("aria-busy")) return;
	const openSeq = state.openSeq;
	setBusy(true, "正在验签", null, "正在验签");
	el.verifyForm.inert = true;
	try {
		const options = {
			roots: await securityFiles(el.verifyRoots), certificates: await securityFiles(el.verifyCerts),
			timestampRoots: await securityFiles(el.verifyTimeRoots), tokens: await securityFiles(el.verifyTokens),
			crls: await securityFiles(el.verifyCRLs), ocsp: await securityFiles(el.verifyOCSP),
			requireTimestamp: el.verifyRequireTime.checked, requireRevocation: el.verifyRequireRevocation.checked,
		};
		if (openSeq !== state.openSeq || !el.verifyPanel.open) return;
		const info = await callWASM("ofdgoVerifySignatures", options);
		if (openSeq !== state.openSeq || !el.verifyPanel.open) return;
		Object.assign(state.doc, info);
		renderMeta();
		el.verifyPanel.close();
		setStatus(info.signatureCount ? "验签完成" : "无签名");
	} catch (err) {
		if (openSeq === state.openSeq) el.verifyStatus.textContent = err.message;
	} finally {
		el.verifyForm.inert = false;
		if (openSeq === state.openSeq) setBusy(false);
	}
}

function openExportPanel(encrypted = false) {
	if (!state.doc || document.body.hasAttribute("aria-busy")) {
		return;
	}
	el.exportForm.reset();
	clearEncryptionForm();
	state.exportEncrypted = encrypted;
	el.encryptionFields.hidden = !encrypted;
	el.encryptionPassword.required = el.encryptionConfirm.required = encrypted;
	updateEncryptionType();
	el.exportTitle.textContent = encrypted ? "加密另存" : "导出文档";
	el.exportSubmit.textContent = encrypted ? "保存" : "导出";
	if (state.editing && state.pageSelection.size) {
		el.exportSpecified.checked = true;
		el.exportRange.value = selectedPageRange();
	}
	el.exportPanel.showModal();
	updateExportRange();
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
			el.exportRangeStatus.textContent = `共${indices.length}页`;
		}
	} catch {
		if (current()) {
			el.exportRange.setAttribute("aria-invalid", "true");
			el.exportRangeStatus.textContent = "页码无效";
		}
	}
}

function addBatchFiles(files) {
	let rejected = false;
	for (const file of files) {
		if (!/\.(ofd|pdf)$/i.test(file.name)) { rejected = true; continue; }
		batch.items.push({ file, format: "", pages: "", status: "pending", text: "待转", details: [] });
	}
	if (rejected) batchElements.Status.textContent = "仅支持PDF、OFD";
	renderBatchList();
}

function renderBatchList() {
	const fragment = document.createDocumentFragment();
	for (const item of batch.items) {
		const row = document.createElement("tr");
		const name = row.insertCell();
		const label = document.createElement("span");
		label.className = "convert-name";
		label.textContent = item.file.name;
		label.title = item.file.name;
		name.append(label);
		let detailRow;
		if (item.details.length) {
			detailRow = document.createElement("tr");
			detailRow.className = "convert-details";
			const cell = detailRow.insertCell();
			cell.colSpan = 5;
			const details = document.createElement("details");
			const summary = document.createElement("summary");
			summary.textContent = "提示";
			const messages = document.createElement("ul");
			messages.className = "convert-messages";
			messages.tabIndex = 0;
			messages.setAttribute("aria-label", `${item.file.name}的转换提示`);
			for (const message of item.details) {
				const entry = document.createElement("li");
				entry.textContent = message;
				messages.append(entry);
			}
			details.append(summary, messages);
			cell.append(details);
		}
		const reset = () => {
			item.details = [];
			detailRow?.remove();
			batchStatus(item, "pending", "待转");
			batchElements.Status.textContent = "";
			batchElements.Progress.value = 0;
			updateBatchControls();
		};
		const pagesCell = row.insertCell();
		pagesCell.dataset.label = "页码";
		const pages = document.createElement("input");
		pages.type = "text";
		pages.placeholder = "全部";
		pages.title = "如1-3,5；留空为全部";
		pages.setAttribute("aria-label", `${item.file.name}的输出页码`);
		pages.value = item.pages;
		pages.disabled = batch.running;
		pages.addEventListener("change", () => { item.pages = pages.value; reset(); });
		pagesCell.append(pages);
		const formatCell = row.insertCell();
		formatCell.dataset.label = "格式";
		const formatControl = document.createElement("span");
		formatControl.className = "convert-format";
		const format = document.createElement("select");
		format.setAttribute("aria-label", `${item.file.name}的输出格式`);
		format.append(new Option("默认", ""), ...batch.formats.map(value => new Option(value.label, value.value)));
		format.value = item.format;
		format.disabled = batch.running;
		format.addEventListener("change", () => { item.format = format.value; reset(); });
		formatControl.append(format);
		formatCell.append(formatControl);
		item.cell = row.insertCell();
		item.cell.textContent = item.text;
		item.cell.dataset.status = item.status;
		const action = row.insertCell();
		const remove = document.createElement("button");
		remove.type = "button";
		remove.className = "icon-button";
		remove.textContent = "×";
		remove.title = "移除";
		remove.setAttribute("aria-label", `移除${item.file.name}`);
		remove.disabled = batch.running;
		remove.addEventListener("click", () => { batch.items.splice(batch.items.indexOf(item), 1); renderBatchList(); });
		action.append(remove);
		fragment.append(row);
		if (detailRow) fragment.append(detailRow);
	}
	batchElements.List.replaceChildren(fragment);
	batchElements.Empty.hidden = batch.items.length > 0;
	updateBatchControls();
}

function updateBatchControls() {
	for (const name of ["Add", "Clear", "Format", "Destination", "DPI"]) batchElements[name].disabled = batch.running;
	batchElements.Clear.disabled ||= !batch.items.length;
	batchElements.Start.disabled = batch.running || !batch.items.length || !batch.formats.length;
	batchElements.Start.hidden = batch.running;
	batchElements.Cancel.hidden = !batch.running;
	batchElements.Cancel.disabled = batch.canceled;
	batchElements.Close.textContent = batch.running ? "收起" : "关闭";
	batchElements.Count.textContent = batch.items.length ? `${batch.items.length}个文件` : "";
	const formats = batch.items.length ? batch.items.map(item => item.format || batchElements.Format.value) : [batchElements.Format.value];
	batchElements.DPIRow.hidden = !formats.some(value => batch.formats.find(format => format.value === value)?.raster);
	batchElements.Button.toggleAttribute("data-running", batch.running);
}

function batchStatus(item, status, text) {
	item.status = status;
	item.text = text;
	item.cell.textContent = text;
	item.cell.dataset.status = status;
}

function batchProgress(item, position, count, progress) {
	const now = performance.now();
	if (now - batch.progressTime < 80 && progress.completed !== progress.total) return;
	batch.progressTime = now;
	const stage = progress.phase?.startsWith("write.") ? "写入" : { open: "读取", pages: "解析", convert: "转换", prepare: "准备", fonts: "字体", resources: "资源", references: "检查", write: "写入", export: "导出", pack: "打包", commit: "保存" }[progress.phase] || "处理";
	const detail = progress.total > 0 ? `${progress.completed}/${progress.total}` : "";
	if (item) batchStatus(item, "running", stage);
	batchElements.Status.textContent = progress.phase === "pack" ? `打包 ${detail}` : `文件 ${position + 1}/${count} · ${stage}${detail ? ` ${detail}` : ""}`;
	batchElements.Progress.max = count;
	batchElements.Progress.value = progress.phase === "pack" && progress.total > 0 ? progress.completed / progress.total : position;
}

function batchCall(name, args, onProgress) {
	return new Promise((resolve, reject) => {
		if (batch.workerError) { reject(batch.workerError); return; }
		const id = ++batch.sequence;
		batch.activeID = id;
		batch.requests.set(id, { resolve, reject, onProgress, destination: name === "ofdgoConvertFile" ? args[3] : null });
		try { batch.worker.postMessage({ id, name, args }); }
		catch (err) { batch.requests.delete(id); batch.activeID = 0; reject(err); }
	});
}

function startBatchWorker() {
	batch.workerError = null;
	return new Promise((resolve, reject) => {
		const worker = new Worker("./ofdgo_wasm.js");
		batch.worker = worker;
		const fail = async error => {
			batch.workerError = error;
			worker.terminate();
			reject(error);
			const requests = [...batch.requests.values()];
			batch.requests.clear();
			for (const request of requests) {
				if (request.destination && request.outputName) {
					try { await request.destination.removeEntry(request.outputName, { recursive: true }); }
					catch (err) { error = new Error(`${error.message}；清理失败：${err.message}`); }
				}
				request.reject(error);
			}
		};
		worker.onerror = event => fail(new Error(event.message || "转换引擎异常"));
		worker.onmessageerror = () => fail(new Error("转换数据传递失败"));
		worker.onmessage = ({ data }) => {
			if (data.type === "ready") { resolve(); return; }
			if (data.type === "exit") { fail(new Error(data.error)); return; }
			if (data.type === "progress") { batchElements.Status.textContent = data.text; return; }
			const request = batch.requests.get(data.id);
			if (!request) return;
			if (data.type === "output") { request.outputName = data.name; return; }
			if (data.type === "conversion") { request.onProgress?.(data); return; }
			batch.requests.delete(data.id);
			if (batch.activeID === data.id) batch.activeID = 0;
			if (data.ok) request.resolve(data.data);
			else request.reject(Object.assign(new Error(data.error), { code: data.code, name: data.canceled ? "AbortError" : "Error" }));
		};
	});
}

function cancelBatch() {
	batch.canceled = true;
	batchElements.Cancel.disabled = true;
	batchElements.Status.textContent = "正在取消";
	if (batch.activeID) batch.worker.postMessage({ type: "cancel", id: batch.activeID });
}

function warnBatch(event) {
	if (batch.running) { event.preventDefault(); event.returnValue = ""; }
}

async function runBatch() {
	if (batch.running || !batch.items.length) return;
	const defaultFormat = batch.formats.find(format => format.value === batchElements.Format.value);
	if (!defaultFormat) return;
	const pageRange = item => item.pages.trim() === "全部" ? "" : item.pages.trim();
	const dpi = Number(batchElements.DPI.value);
	const archive = batchElements.Destination.value === "archive";
	const backend = state.renderBackend;
	for (const item of batch.items) {
		const format = batch.formats.find(format => format.value === (item.format || defaultFormat.value));
		const key = JSON.stringify([format.value, pageRange(item), format.raster ? dpi : 0, archive, backend]);
		if (item.settingsKey !== key) {
			item.status = "pending";
			item.text = "待转";
			item.details = [];
			item.settingsKey = key;
		}
	}
	renderBatchList();
	let jobs = batch.items.filter(item => item.status !== "done" && item.status !== "unchanged");
	if (!jobs.length) {
		jobs = [...batch.items];
		for (const item of jobs) { item.status = "pending"; item.text = "待转"; item.details = []; }
	}
	batch.running = true;
	batch.canceled = false;
	renderBatchList();
	window.addEventListener("beforeunload", warnBatch);
	let destination = null;
	let zipFile = null;
	let storage = null;
	let stagingName = "";
	let packed = false;
	const entries = [];
	try {
		if (archive) {
			if (window.showSaveFilePicker) zipFile = await window.showSaveFilePicker({ suggestedName: "转换结果.zip", types: [{ description: "ZIP", accept: { "application/zip": [".zip"] } }] });
			if (navigator.storage?.getDirectory) {
				storage = await navigator.storage.getDirectory();
				stagingName = `ofdgo-convert-${crypto.randomUUID()}`;
				destination = await storage.getDirectoryHandle(stagingName, { create: true });
			}
		} else destination = await window.showDirectoryPicker({ mode: "readwrite" });
		if (batch.canceled) return;
		batchElements.Status.textContent = "正在准备引擎";
		await startBatchWorker();
		const fonts = await fontManager.files(fontManager.records());
		const names = new Set();
		for (const [position, item] of jobs.entries()) {
			if (batch.canceled) break;
			item.details = [];
			const format = batch.formats.find(format => format.value === (item.format || defaultFormat.value));
			const base = item.file.name.replace(/\.[^.]+$/, "").replace(/[<>:"/\\|?*\x00-\x1f]/g, "_").replace(/[. ]+$/g, "") || "document";
			let unique = base;
			const outputName = () => (format.paged ? unique : `${unique}.${format.extension}`).toLowerCase();
			for (let suffix = 2; archive && names.has(outputName()); suffix++) unique = `${base} (${suffix})`;
			names.add(outputName());
			const options = { format: format.value, pages: pageRange(item), dpi, backend, archive, base: unique, credentials: null };
			try {
				for (;;) {
					try {
						const result = await batchCall("ofdgoConvertFile", [item.file, options, fonts, destination], progress => batchProgress(item, position, jobs.length, progress));
						for (const file of result.files) entries.push(file);
						item.details = (result.pdf?.Warnings || []).map(warning => `${warning.Page ? `第${warning.Page}页：` : ""}${warning.Message}`);
						if (result.unchanged) {
							batchStatus(item, "unchanged", "原样");
							item.details = ["无需转换，源文件未改动"];
						} else batchStatus(item, archive ? "ready" : "done", archive ? "待存" : "完成");
						break;
					} catch (err) {
						if (batch.canceled || !["credentialsRequired", "invalidCredentials"].includes(err.code)) throw err;
						options.credentials?.password?.fill(0);
						options.credentials?.key?.fill(0);
						options.credentials?.keyPassword?.fill(0);
						options.credentials = await requestCredentials(err.code, state.openSeq);
						if (!options.credentials) throw new DOMException("已取消解锁", "AbortError");
						if (!window.confirm("转换结果将不保留原加密，是否继续？")) throw new DOMException("已取消转换", "AbortError");
					}
				}
			} catch (err) {
				batchStatus(item, err.name === "AbortError" ? "canceled" : "failed", err.name === "AbortError" ? "取消" : "失败");
				item.details = [err.message];
				if (batch.workerError) throw err;
			} finally {
				options.credentials?.password?.fill(0);
				options.credentials?.key?.fill(0);
				options.credentials?.keyPassword?.fill(0);
			}
			batchElements.Progress.max = jobs.length;
			batchElements.Progress.value = position + 1;
		}
		if (archive && entries.length && !batch.canceled) {
			const result = await batchCall("ofdgoPackFiles", [entries, zipFile], progress => batchProgress(null, 0, 1, progress));
			if (result.blob) downloadBytes(result.blob, "application/zip", "转换结果.zip");
			packed = true;
			for (const item of jobs) if (item.status === "ready") batchStatus(item, "done", "完成");
		}
		const done = jobs.filter(item => item.status === "done").length;
		const failed = jobs.filter(item => item.status === "failed").length;
		const unchanged = batch.items.filter(item => item.status === "unchanged").length;
		batchElements.Status.textContent = `${batch.canceled ? "已取消 · " : ""}完成 ${done}${failed ? ` · 失败 ${failed}` : ""}${unchanged ? ` · 原样 ${unchanged}` : ""}`;
	} catch (err) {
		batchElements.Status.textContent = err.name === "AbortError" ? "转换已取消" : err.message;
	} finally {
		if (archive && !packed) for (const item of jobs) if (item.status === "ready") { item.status = "pending"; item.text = "待转"; }
		batch.worker?.terminate();
		batch.worker = null;
		batch.requests.clear();
		batch.activeID = 0;
		if (storage && stagingName) {
			try { await storage.removeEntry(stagingName, { recursive: true }); }
			catch { batchElements.Status.textContent += " · 临时文件清理失败"; }
		}
		batch.running = false;
		window.removeEventListener("beforeunload", warnBatch);
		renderBatchList();
	}
}

async function exportFile(whole, indices = null, value = el.exportFormat.value, encryption = null) {
	if (!state.doc || document.body.hasAttribute("aria-busy")) {
		return;
	}
	const saving = value === "ofd";
	if (saving && !state.editorInfo && !state.ofdBytes && !encryption) {
		return;
	}
	if (!saving && state.doc.encryption?.encrypted && !window.confirm("此格式将输出明文，是否继续？")) return;
	const format = saving ? { value: "ofd", label: "OFD", extension: "ofd", mime: "application/ofd" } : exportFormatInfo(value);
	if (!format) {
		return;
	}
	const archive = whole && !saving && format.value !== "pdf" && format.value !== "txt";
	const label = archive ? "ZIP" : format.label;
	const extension = archive ? "zip" : format.extension;
	const mime = archive ? "application/zip" : format.mime;
	let openSeq = state.openSeq;
	const fileName = saving && indices !== null ? `${baseFileName()}_选页.ofd` : whole ? `${baseFileName()}.${extension}` : pageFileName(extension);
	const pageIndex = state.pageIndex;
	const dpi = exportFormatUsesDPI(format.value) ? currentImageDPI() : 0;
	const status = saving ? "正在保存文档" : whole ? STATUS.exporting : STATUS.pageExporting;
	state.exporting = true;
	updateControls();
	setBusy(true, `正在生成 ${label}`, null, status);
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
			setBusy(true, `正在生成 ${label}`, null, status);
		}
		if (encryption?.recipientFiles) {
			encryption.recipients = await encryptionRecipients(encryption.recipientFiles);
			delete encryption.recipientFiles;
			if (openSeq !== state.openSeq) return;
		}
		const readingSave = saving && !state.editorInfo && !encryption && indices === null;
		const result = readingSave
			? { label: "OFD", size: state.ofdBytes.byteLength, mime, blob: state.ofdBytes }
			: encryption ? await callWASM("ofdgoSaveEncrypted", indices, encryption, file) : saving ? await callWASM("ofdgoSaveDocument", indices, file) : whole
			? await callWASM("ofdgoExportDocument", format.value, dpi, indices, state.renderBackend, file)
			: await callWASM("ofdgoExportPage", pageIndex, format.value, dpi, state.renderBackend, file);
		if (openSeq !== state.openSeq) {
			return;
		}
		if (readingSave && file) {
			const writer = await file.createWritable();
			await writer.write(result.blob);
			await writer.close();
			delete result.blob;
		}
		if (result.blob) {
			downloadBytes(result.blob, result.mime, fileName);
		}
		if (saving && indices === null && state.editorInfo) {
			state.savedRevision = state.editorInfo.revision;
			setDirty(false);
		}
		setStatus(`${result.label}已${saving ? "保存" : "导出"}（${formatBytes(result.size)}）`);
		return true;
	} catch (err) {
		if (openSeq === state.openSeq) {
			const canceled = saving ? "保存已取消" : "导出已取消";
			if (el.batchPagesPanel.open) el.batchPagesStatus.textContent = err.name === "AbortError" ? canceled : err.message;
			if (err.name === "AbortError") {
				setStatus(canceled);
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
	if (state.importing) {
		el.importCancel.disabled = true;
		el.importStatus.textContent = "正在取消";
	}
	setProgress("正在取消", null);
	wasmWorker.postMessage({ type: "cancel", id: state.exportRequestID });
}

function virtualIndex(offsets, top) {
	let low = 0, high = offsets.length - 1;
	while (low + 1 < high) {
		const mid = (low + high) >>> 1;
		if (offsets[mid] <= top) low = mid;
		else high = mid;
	}
	return low;
}

function createPageWindow(host, viewport, overscan, create, release, observe) {
	host.classList.add("virtual-pages");
	return { host, viewport, overscan, create, release, observe, nodes: new Map(), offsets: new Float64Array(1), origin: 0, total: 0, gap: 0 };
}

function windowInset(view) {
	return view.host.getBoundingClientRect().top - view.viewport.getBoundingClientRect().top
		+ view.viewport.scrollTop - view.viewport.clientTop;
}

function pageWindowTop(view) {
	const inset = windowInset(view);
	if (view.total <= SCROLL_SEGMENT_SIZE) return view.viewport.scrollTop - inset;
	const delta = view.viewport.scrollTop - view.scrollTop;
	if (!delta) return view.logicalTop;
	if (view.relativeScroll) return view.logicalTop + delta;
	const max = view.viewport.scrollHeight - view.viewport.clientHeight;
	return view.viewport.scrollTop / Math.max(1, max) * (max + view.total - SCROLL_SEGMENT_SIZE) - inset;
}

function scrollPageWindowBy(view, delta) {
	if (view) syncPageWindow(view, pageWindowTop(view) + delta);
}

function scrollViewerBy(delta) {
	if (state.pageWindow) scrollPageWindowBy(state.pageWindow, delta);
	else el.viewerPanel.scrollTop += delta;
}

function syncPageWindow(view, target = null) {
	if (!view || view.offsets.length < 2) return;
	const { host, viewport, offsets, nodes } = view;
	const extent = Math.min(SCROLL_SEGMENT_SIZE, view.total);
	host.style.height = `${extent}px`;
	const inset = windowInset(view), height = viewport.clientHeight;
	const maxTop = Math.max(0, viewport.scrollHeight - height);
	const logicalMax = maxTop + view.total - extent;
	const logical = Math.max(-inset, Math.min(logicalMax - inset, target ?? pageWindowTop(view)));
	viewport.scrollTop = logicalMax > 0 ? (logical + inset) * maxTop / logicalMax : 0;
	view.scrollTop = viewport.scrollTop;
	view.logicalTop = logical;
	view.origin = logical - viewport.scrollTop + inset;
	const first = virtualIndex(offsets, Math.max(view.origin, logical - view.overscan));
	const last = virtualIndex(offsets, Math.min(view.origin + extent, logical + height + view.overscan));
	const selection = host === el.svgHost && state.documentSelection ? document.getSelection() : null;
	const selected = selection?.rangeCount && isDocumentSelection(selection.getRangeAt(0));
	let changed = false;
	for (const [index, node] of nodes) {
		if ((index < first || index > last) && view.release(node, index)) {
			node.remove();
			nodes.delete(index);
			changed = true;
		}
	}
	for (let index = first; index <= last; index++) {
		if (nodes.has(index)) continue;
		const node = view.create(state.doc.pages[index]);
		nodes.set(index, node);
		const next = [...host.children].find(child => Number(child.dataset.pageIndex) > index);
		host.insertBefore(node, next || null);
		changed = true;
		node.style.top = `${offsets[index] - view.origin}px`;
		view.observe(node, index);
	}
	for (const [index, node] of nodes) {
		const top = offsets[index] - view.origin;
		const itemHeight = offsets[index + 1] - offsets[index] - view.gap;
		node.style.top = `${Math.max(-itemHeight, Math.min(extent, top))}px`;
		node.style.visibility = top + itemHeight < 0 || top >= extent ? "hidden" : "";
	}
	if (selected && changed) selection.getRangeAt(0).selectNodeContents(host);
	view.anchor = pageWindowAnchor(view);
}

function pageWindowAnchor(view) {
	const top = pageWindowTop(view);
	const index = virtualIndex(view.offsets, top);
	return { index, offset: top - view.offsets[index] };
}

function releaseFlowShell(shell, index) {
	if (shell.contains(document.activeElement) || canvasEditor.drag?.item?.index === index) return false;
	unmountPage(index);
	if (state.selectedPages.has(index)) return false;
	state.pageObserver?.unobserve(shell);
	state.visiblePages.delete(index);
	return true;
}

function createFlowShell(page) {
	const shell = document.createElement("div");
	shell.className = "page-shell";
	shell.dataset.pageIndex = String(page.index);
	const surface = document.createElement("div");
	surface.className = "page-surface";
	const placeholder = document.createElement("div");
	placeholder.className = "page-placeholder";
	placeholder.textContent = `第 ${page.index + 1} 页`;
	shell.append(surface, placeholder);
	layoutPageShell(shell, page);
	return shell;
}

function renderPageFlow() {
	el.svgHost.replaceChildren();
	state.pageWindow = null;
	state.visiblePages.clear();
	el.viewerPanel.classList.remove("single-page-fits-height");
	if (!state.doc) {
		return;
	}
	if (state.pageObserver) {
		state.pageObserver.disconnect();
	}
	el.emptyState.hidden = true;
	el.pageFrame.hidden = false;
	state.pageWindow = createPageWindow(el.svgHost, el.viewerPanel, 600, createFlowShell, releaseFlowShell, observeFlowPage);
	layoutPages();
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

function observeFlowPage(shell) {
	if (!shell) return;
	const observer = flowPageObserver();
	if (observer) {
		observer.observe(shell);
	} else {
		const index = Number(shell.dataset.pageIndex);
		state.visiblePages.add(index);
		renderFlowPage(index, { priority: 4 });
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
				if (state.pageWindow && state.pageWindow.nodes.get(index) !== entry.target) continue;
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
	if (!shell) return;
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
		const page = await loadPageData(index, { openSeq, priority: options.priority ?? 2,
			refresh: state.editing && state.pageCache.get(index)?.previewOnly });
		if (openSeq !== state.openSeq) {
			return null;
		}
		if (page) {
			const shell = pageShell(index);
			if (shell && !shell.classList.contains("rendered") && (!state.pageObserver || state.visiblePages.has(index) || index === state.pageIndex)) {
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
			let next = 0;
			for (let index = 1; index < state.pageRenderQueue.length; index++) {
				if (comparePageRenderTask(state.pageRenderQueue[index], state.pageRenderQueue[next]) < 0) next = index;
			}
			const [task] = state.pageRenderQueue.splice(next, 1);
			try {
				if (task.openSeq !== state.openSeq) {
					task.resolve(null);
					continue;
				}
				if (!task.refresh && state.pageCache.has(task.index)) {
					task.resolve(state.pageCache.get(task.index));
					continue;
				}
				const page = await callWASM("ofdgoRenderPage", task.index, state.renderBackend, state.renderDPI, displayMode() === "raster");
				if (state.pageInFlight.get(task.key) !== task) {
					task.resolve(null);
					continue;
				}
				const scope = state.composite;
				if (scope?.index === task.index) {
					const objects = await callWASM("ofdgoCompositeObjects", task.index, scope.key);
					if (scope === state.composite && task.openSeq === state.openSeq) scope.objects = objects;
				}
				await loadSVGFonts(page.fonts, task.openSeq);
				if (task.openSeq === state.openSeq && state.pageInFlight.get(task.key) === task) {
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
					if (task.openSeq !== state.openSeq || state.pageInFlight.get(task.key) !== task) {
						task.resolve(null);
						continue;
					}
					page.cacheBytes = (page.svg.length + page.text.length + page.annotations.length) * 2;
					page.text = JSON.parse(page.text);
					page.annotations = JSON.parse(page.annotations);
					state.pageCache.set(task.index, page);
				}
				task.resolve(page);
			} catch (err) {
				task.reject(err);
			} finally {
				if (state.pageInFlight.get(task.key) === task) state.pageInFlight.delete(task.key);
			}
		}
	} finally {
		state.pageRenderRunning = false;
	}
}

function trimPageCache() {
	const protectedPages = new Set([state.pageIndex, ...state.visiblePages, ...state.visibleThumbnails, ...state.selectedPages]);
	for (const view of [state.pageWindow, state.thumbnailWindow]) {
		for (const index of view?.nodes.keys() || []) protectedPages.add(index);
	}
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
	if (state.editorInfo && displayMode() === "svg") {
		mountEditorObjects(index, page, surface);
	}
	mountAnnotationNotes(index, surface, page.annotations);
	mountPageLinks(page, surface);
	shell.classList.add("rendered");
	renderSearchHighlights(index);
	if (state.doc?.pages?.[index]) {
		layoutPageShell(shell, state.doc.pages[index]);
	}
}

function pageLinkTarget(link) {
	if (link.dest) {
		const target = state.doc.pages.findIndex(page => page.id === link.dest.pageID);
		return { href: target < 0 ? "#" : `#page-${target + 1}`, title: target < 0 ? "文档链接" : `第${target + 1}页`, activate: () => navigateDestination(link.dest) };
	}
	if (link.attachment) {
		return { href: "#", title: link.fileName ? `下载附件：${link.fileName}` : "下载附件", activate: () => {
			if (!link.fileName) { setStatus("附件不存在"); return; }
			return downloadAttachment({ id: link.attachment, fileName: link.fileName });
		} };
	}
	let url;
	try { url = new URL(link.uri); } catch { return null; }
	if (url.protocol !== "http:" && url.protocol !== "https:") return null;
	return { href: url.href, title: url.href, external: true, activate: () => { window.open(url.href, "_blank", "noopener,noreferrer"); } };
}

function mountPageLinks(page, surface) {
	const groups = new Map();
	const mounted = new Map();
	for (const [index, link] of page.links.entries()) {
		const target = pageLinkTarget(link);
		if (!target) continue;
		target.order = index;
		const key = link.group ? JSON.stringify([link.group, link.x, link.y, link.width, link.height, link.path]) : index;
		if (!groups.has(key)) groups.set(key, { link, targets: [] });
		groups.get(key).targets.push(target);
	}
	for (const { link, targets } of groups.values()) {
		const anchor = document.createElement("a");
		anchor.className = "page-link";
		anchor.href = targets[0].href;
		anchor.title = targets[0].title;
		mounted.set(anchor, { link, targets });
		if (!link.group && targets[0].external) {
			anchor.target = "_blank";
			anchor.rel = "noopener noreferrer";
		} else {
			anchor.addEventListener("click", async event => {
				event.preventDefault();
				if (document.body.hasAttribute("aria-busy")) return;
				const openSeq = state.openSeq;
				let actions = targets;
				if (event.detail && link.group) {
					const matches = new Set();
					for (const node of document.elementsFromPoint(event.clientX, event.clientY)) {
						const entry = mounted.get(node.closest(".page-link"));
						if (entry?.link.group === link.group) entry.targets.forEach(target => matches.add(target));
					}
					actions = [...matches].sort((a, b) => a.order - b.order);
				}
				for (const target of actions) {
					if (openSeq !== state.openSeq) break;
					await target.activate();
				}
			});
		}
		anchor.setAttribute("aria-label", anchor.title);
		anchor.style.left = `${link.x / page.width * 100}%`;
		anchor.style.top = `${link.y / page.height * 100}%`;
		anchor.style.width = `${link.width / page.width * 100}%`;
		anchor.style.height = `${link.height / page.height * 100}%`;
		if (link.path) {
			anchor.classList.add("page-region-link");
			const region = document.createElementNS("http://www.w3.org/2000/svg", "svg");
			region.setAttribute("viewBox", `${link.x} ${link.y} ${link.width} ${link.height}`);
			region.setAttribute("aria-hidden", "true");
			const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
			path.setAttribute("d", link.path);
			region.append(path);
			anchor.append(region);
		}
		surface.append(anchor);
	}
}

function destinationPoint(page, x, y) {
	if (state.rotation === 90) return { x: page.height - y, y: x };
	if (state.rotation === 180) return { x: page.width - x, y: page.height - y };
	if (state.rotation === 270) return { x: y, y: page.width - x };
	return { x, y };
}

function sourcePoint(page, x, y) {
	if (state.rotation === 90) return { x: y, y: page.height - x };
	if (state.rotation === 180) return { x: page.width - x, y: page.height - y };
	if (state.rotation === 270) return { x: page.width - y, y: x };
	return { x, y };
}

async function navigateDestination(dest) {
	if (document.body.hasAttribute("aria-busy")) return;
	const index = state.doc.pages.findIndex(page => page.id === dest.pageID);
	if (index < 0) {
		setStatus("目标页面不存在");
		return;
	}
	if (!["XYZ", "Fit", "FitH", "FitV", "FitR"].includes(dest.type)) {
		setStatus("暂不支持此跳转方式");
		return;
	}
	const seq = state.openSeq, scale = state.scale;
	let retained = null;
	if (dest.omitLeft || dest.omitTop) {
		const current = state.doc.pages[state.pageIndex], shell = pageShell(state.pageIndex)?.getBoundingClientRect();
		if (current && shell) {
			const viewer = el.viewerPanel.getBoundingClientRect();
			retained = sourcePoint(current,
				(viewer.left + pageSpace() - shell.left) / (MM_TO_PX * scale),
				(viewer.top + pageBlockSpace() - shell.top) / (MM_TO_PX * scale));
		}
	}
	await renderPage(index, { fit: false, scroll: false });
	if (seq !== state.openSeq || state.pageIndex !== index) return;
	const page = state.doc.pages[index], size = pageViewSize(page), space = pageSpace();
	const width = el.viewerPanel.clientWidth - space * 2, height = el.viewerPanel.clientHeight - space * 2;
	let left = dest.omitLeft && retained ? retained.x : dest.left;
	let top = dest.omitTop && retained ? retained.y : dest.top;
	if (dest.type === "Fit") {
		fitHeight(false);
		scrollToPage(index);
		return;
	} else if (dest.type === "FitH") {
		setScale(fitWidthScale(page), false);
		left = 0;
	} else if (dest.type === "FitV") {
		setScale(height / (size.height * MM_TO_PX), false);
		top = 0;
	} else if (dest.type === "FitR") {
		const a = destinationPoint(page, dest.left, dest.top), b = destinationPoint(page, dest.right, dest.bottom);
		if (dest.right <= dest.left || dest.bottom <= dest.top) return;
		setScale(Math.min(width / Math.abs(b.x - a.x), height / Math.abs(b.y - a.y)) / MM_TO_PX, false);
	} else {
		setScale(!dest.omitZoom && dest.zoom > 0 ? dest.zoom : scale, false);
	}
	const point = destinationPoint(page, left, top);
	if (dest.type === "FitR") {
		const bottom = destinationPoint(page, dest.right, dest.bottom);
		point.x = Math.min(point.x, bottom.x);
		point.y = Math.min(point.y, bottom.y);
	}
	if (state.pageWindow) syncPageWindow(state.pageWindow, state.pageWindow.offsets[index]);
	const shell = pageShell(index).getBoundingClientRect(), viewer = el.viewerPanel.getBoundingClientRect();
	el.viewerPanel.scrollLeft += shell.left - viewer.left + point.x * MM_TO_PX * state.scale - space;
	scrollViewerBy(shell.top - viewer.top + point.y * MM_TO_PX * state.scale - pageBlockSpace());
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
	if (state.composite && items.some(item => item.container !== items[0].container)) {
		event.preventDefault();
		setStatus("请选择同一组内的对象");
		return true;
	}
	if (!canEditObject(canvasEditor.selected, "copy") || cut && !canEditObject(canvasEditor.selected, "delete")) {
		event.preventDefault();
		setStatus(cut ? "对象不可剪切" : "对象不可复制");
		return true;
	}
	const index = items[0].index, ids = items.map(item => item.id);
	const clipboard = { token: crypto.randomUUID(), page: state.doc.pages[index].id, scope: state.composite?.key || "", bounds: selectionBounds(items), x: 0, y: 0, cut,
		compound: items.some(item => item.type === "CompositeObject" || item.type === "CompositeGraphicUnit") };
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
		return callWASM("ofdgoCaptureObjects", index, ids, clipboard.token, clipboard.scope).then(async () => {
			clipboard.cut = false;
			if (cut && openSeq === state.openSeq && revision === state.editorInfo?.revision && index === state.pageIndex && state.objectClipboard === clipboard && clipboard.scope === (state.composite?.key || "")) {
				clipboard.cut = Boolean(await changeDocument("ofdgoDeleteObjects", { index, id: ids, items }));
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
	if (document.body.hasAttribute("aria-busy") || canvasEditor.input || canvasEditor.crop || canvasEditor.drag) return;
	const index = state.pageIndex, page = state.doc.pages[index];
	if (token) {
		const clipboard = state.objectClipboard;
		if (!clipboard || token !== clipboard.token) {
			setStatus("复制内容已失效，请重新复制");
			return;
		}
		const openSeq = state.openSeq;
		if (!await clipboard.ready || openSeq !== state.openSeq || index !== state.pageIndex || state.objectClipboard !== clipboard) return;
		const scope = state.composite?.key || "";
		if (clipboard.compound && (scope || clipboard.scope) && (scope !== clipboard.scope || page.id !== clipboard.page)) {
			setStatus("复合对象请在原范围粘贴");
			return;
		}
		if (!pageCan("insert")) return;
		const visible = visiblePageBounds(index), bounds = clipboard.bounds;
		const offset = (clipboard.targetPage || clipboard.page) === page.id ? { x: clipboard.x + (clipboard.cut ? 0 : 3), y: clipboard.y + (clipboard.cut ? 0 : 3) } : { x: 0, y: 0 };
		for (const [axis, size] of [["x", "width"], ["y", "height"]]) {
			const position = bounds[axis] + offset[axis];
			if (position < visible[axis] || position + bounds[size] > visible[axis] + visible[size]) {
				const margin = Math.min(20, visible[size] / 10);
				offset[axis] = Math.max(visible[axis] + margin, Math.min(position, visible[axis] + visible[size] - margin - bounds[size])) - bounds[axis];
			}
		}
		if (await changeDocument("ofdgoPasteObjects", null, index, token, offset.x, offset.y, state.composite?.key || "")) {
			clipboard.targetPage = page.id;
			clipboard.x = offset.x;
			clipboard.y = offset.y;
			clipboard.cut = false;
		}
		return;
	}
	if (!pageCan("insert")) return;
	const visible = visiblePageBounds(index), margin = Math.min(20, visible.width / 10);
	const x = visible.x + margin, y = visible.y + Math.min(20, visible.height / 10), width = visible.width - margin * 2;
	if (files.length) {
		if (files.length !== 1) {
			setStatus("一次粘贴一张图片");
			return;
		}
		if (!/^image\/(png|jpeg)$/.test(files[0].type)) {
			setStatus("仅支持PNG、JPG图片");
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
	return state.pageWindow?.nodes.get(index) || null;
}

function scrollToPage(index) {
	if (state.pageWindow) syncPageWindow(state.pageWindow, state.pageWindow.offsets[index]);
	const shell = pageShell(index);
	if (shell) {
		if (state.pageWindow) {
			const shellRect = shell.getBoundingClientRect(), viewerRect = el.viewerPanel.getBoundingClientRect();
			const space = state.fitMode === "height" && !state.continuous
				? (el.viewerPanel.clientHeight - shellRect.height) / 2 : pageBlockSpace();
			scrollViewerBy(shellRect.top - viewerRect.top - space);
		} else if (state.fitMode === "height" && !state.continuous) {
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
	syncPageWindow(state.pageWindow);
	const shell = pageShellFromView();
	if (!shell) {
		return;
	}
	const nextIndex = Number.parseInt(shell.dataset.pageIndex, 10);
	if (Number.isFinite(nextIndex) && nextIndex !== state.pageIndex) {
		setCurrentPage(nextIndex);
		queueNearbyPages(nextIndex);
	}
	prunePageRenderQueue();
}

function prunePageRenderQueue() {
	state.pageRenderQueue = state.pageRenderQueue.filter(task => {
		if (task.priority < 2 || Math.abs(task.index - state.pageIndex) <= 2
			|| state.pageWindow?.nodes.has(task.index) || state.thumbnailWindow?.nodes.has(task.index)) return true;
		state.pageInFlight.delete(task.key);
		task.resolve(null);
		return false;
	});
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
	if (state.composite && state.composite.index !== index) resetCompositeScope();
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
	if (state.thumbnailWindow) {
		if (!force && state.pageListCurrent === state.pageIndex) return;
		state.pageListCurrent = state.pageIndex;
		if (state.showPages && !el.pageList.hidden) revealThumbnail(state.pageIndex);
	}
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
		if (state.showPages && !el.pageList.hidden && !state.thumbnailWindow) {
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
	const view = state.pageWindow;
	if (!view) return;
	const anchor = view.offsets.length > 1 ? pageWindowAnchor(view) : { index: state.pageIndex, offset: 0 };
	const gap = Number.parseFloat(getComputedStyle(el.svgHost).rowGap) || 0;
	const offsets = new Float64Array(state.doc.pages.length + 1);
	const fittedWidth = state.scale * pageViewSize(currentPageInfo()).width;
	let width = 1;
	for (let index = 0; index < state.doc.pages.length; index++) {
		const size = pageViewSize(state.doc.pages[index]);
		const scale = state.fitMode === "width" ? fittedWidth / size.width : state.scale;
		offsets[index + 1] = offsets[index] + Math.max(1, size.height * MM_TO_PX) * scale + gap;
		width = Math.max(width, Math.max(1, size.width * MM_TO_PX) * scale);
	}
	view.offsets = offsets;
	view.gap = gap;
	view.total = Math.max(0, offsets.at(-1) - gap);
	view.host.style.width = `${width}px`;
	for (const [index, shell] of view.nodes) layoutPageShell(shell, state.doc.pages[index]);
	syncPageWindow(view, offsets[anchor.index] + anchor.offset);
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
		surface.style.setProperty("--surface-rotation", `${state.rotation}deg`);
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
	el.pageSelectionTools.hidden = !state.editing || !state.pageMultiSelect || selected !== el.pagesTab;
	el.navigationContent.scrollTop = state.navigationScroll.get(selected) || 0;
	if (selected === el.pagesTab) syncThumbnailWindow();
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
		scrollViewerBy(box.top + box.height / 2 - viewer.top - el.viewerPanel.clientHeight / 2);
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
		if (item?.type === "TextObject") {
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

function selectedPageIndexes() {
	if (!state.pageSelection.size) return state.pageMultiSelect ? [] : [state.pageIndex];
	return state.doc.pages.filter(page => state.pageSelection.has(page.id)).map(page => page.index);
}

function changeSelectedPages(action, direction = 0) {
	const indexes = selectedPageIndexes();
	if (!indexes.length) return;
	if (indexes.length === 1) return changeDocument("ofdgoChangePage", null, action, indexes[0], ...(direction ? [indexes[0] + direction] : []));
	const at = direction < 0 ? indexes[0] - 1 : indexes.at(-1) + 2;
	return changeDocument("ofdgoBatchPages", null, action, selectedPageRange(), ...(direction ? [at] : []));
}

function selectedPageRange(indexes = selectedPageIndexes()) {
	const pages = indexes.map(index => index + 1);
	const ranges = [];
	for (let i = 0; i < pages.length; i++) {
		const first = pages[i];
		while (pages[i + 1] === pages[i] + 1) i++;
		ranges.push(first === pages[i] ? String(first) : `${first}-${pages[i]}`);
	}
	return ranges.join(",");
}

function syncPageSelection() {
	if (!state.editing) state.pageMultiSelect = false;
	el.pageSelectionTools.hidden = !state.editing || !state.pageMultiSelect || el.pageList.hidden;
	el.pageSelectionCount.textContent = `已选${state.pageSelection.size}页`;
	const allSelected = state.pageSelection.size === state.doc?.pageCount;
	el.selectAllPages.textContent = allSelected ? "取消" : "全选";
	el.selectAllPages.title = allSelected ? "取消全选" : "全选页面";
	el.selectAllPages.setAttribute("aria-label", el.selectAllPages.title);
	for (const button of el.pageList.children) {
		if (state.editing) button.setAttribute("aria-pressed", String(state.pageSelection.has(state.doc.pages[Number(button.dataset.pageIndex)].id)));
		else button.removeAttribute("aria-pressed");
	}
}

function selectThumbnailPage(event, index) {
	if (document.body.hasAttribute("aria-busy")) return;
	if (event.pointerId === state.pageTouchPointer) {
		state.pageTouchPointer = null;
		return;
	}
	if (state.editing) {
		const page = state.doc.pages[index], additive = event.ctrlKey || event.metaKey || state.pageMultiSelect;
		if (!additive) state.pageSelection.clear();
		if (event.shiftKey) {
			let anchor = state.doc.pages.findIndex(page => page.id === state.pageSelectionAnchor);
			if (anchor < 0) anchor = state.pageIndex;
			for (let i = Math.min(anchor, index); i <= Math.max(anchor, index); i++) state.pageSelection.add(state.doc.pages[i].id);
		} else {
			if (additive && state.pageSelection.has(page.id)) state.pageSelection.delete(page.id);
			else state.pageSelection.add(page.id);
			state.pageSelectionAnchor = page.id;
		}
		syncPageSelection();
		updateControls();
		if (additive || event.shiftKey) return;
	}
	return renderPage(index);
}

el.selectAllPages.addEventListener("click", () => {
	if (document.body.hasAttribute("aria-busy")) return;
	state.pageSelection = new Set(state.pageSelection.size === state.doc.pageCount ? [] : state.doc.pages.map(page => page.id));
	syncPageSelection();
	updateControls();
});

el.finishPageSelection.addEventListener("click", () => {
	state.pageMultiSelect = false;
	syncPageSelection();
	updateControls();
});

el.pageList.addEventListener("pointerdown", event => {
	cancelPageTouch?.();
	state.pageTouchPointer = null;
	if (!state.editing || event.pointerType !== "touch" || !event.isPrimary || document.body.hasAttribute("aria-busy")) return;
	const button = event.target.closest(".page-list-item");
	if (!button || event.target.closest(".thumb-grip")) return;
	const seq = state.openSeq, pointer = event.pointerId;
	const timer = setTimeout(() => {
		if (seq !== state.openSeq || !state.editing || document.body.hasAttribute("aria-busy")) return;
		if (!state.pageMultiSelect) state.pageSelection.clear();
		state.pageMultiSelect = true;
		state.pageTouchPointer = pointer;
		state.pageSelection.add(state.doc.pages[Number(button.dataset.pageIndex)].id);
		syncPageSelection();
		updateControls();
	}, 450);
	const finish = next => {
		if (next.pointerId !== pointer) return;
		cancelPageTouch?.();
	};
	cancelPageTouch = () => {
		clearTimeout(timer);
		el.pageList.removeEventListener("pointermove", move);
		el.pageList.removeEventListener("pointerup", finish);
		el.pageList.removeEventListener("pointercancel", finish);
		el.pageList.removeEventListener("lostpointercapture", finish);
		cancelPageTouch = null;
	};
	const move = next => {
		if (Math.hypot(next.clientX - event.clientX, next.clientY - event.clientY) > 8) finish(next);
	};
	el.pageList.addEventListener("pointermove", move);
	el.pageList.addEventListener("pointerup", finish);
	el.pageList.addEventListener("pointercancel", finish);
	el.pageList.addEventListener("lostpointercapture", finish);
});

el.pageList.addEventListener("contextmenu", event => {
	if (state.pageMultiSelect) event.preventDefault();
});

el.pageList.addEventListener("keydown", event => {
	const button = event.target?.closest(".page-list-item");
	if (state.thumbnailWindow && button && !event.altKey && !event.ctrlKey && !event.metaKey && !event.isComposing) {
		const index = Number(button.dataset.pageIndex);
		const next = { ArrowUp: index - 1, ArrowDown: index + 1, Home: 0, End: state.doc.pageCount - 1,
			Tab: index + (event.shiftKey ? -1 : 1) }[event.key];
		if (next >= 0 && next < state.doc.pageCount) {
			event.preventDefault();
			revealThumbnail(next);
			state.thumbnailWindow.nodes.get(next)?.focus({ preventScroll: true });
			return;
		}
	}
	if (!state.editing || event.isComposing || document.body.hasAttribute("aria-busy")) return;
	if ((event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === "a") {
		state.pageSelection = new Set(state.doc.pages.map(page => page.id));
	} else if (event.key === "Escape") {
		state.pageSelection.clear();
		state.pageSelectionAnchor = null;
		state.pageMultiSelect = false;
	} else return;
	event.preventDefault();
	event.stopPropagation();
	syncPageSelection();
	updateControls();
});

function renderPageList() {
	el.pageList.replaceChildren();
	state.thumbnailWindow = null;
	state.pageListCurrent = null;
	state.visibleThumbnails.clear();
	if (!state.doc) {
		return;
	}
	if (state.thumbnailObserver) {
		state.thumbnailObserver.disconnect();
	}
	const ids = new Set(state.doc.pages.map(page => page.id));
	for (const id of state.pageSelection) if (!ids.has(id) || !state.editing) state.pageSelection.delete(id);
	state.thumbnailWindow = createPageWindow(el.pageList, el.navigationContent, 180, createThumbnail, releaseThumbnail, observeThumbnail);
	layoutThumbnails();
	syncPageSelection();
}

function createThumbnail(page) {
	const openSeq = state.openSeq;
	const button = document.createElement("button");
	button.type = "button";
	button.className = "page-list-item";
	button.dataset.pageIndex = String(page.index);
	button.setAttribute("aria-posinset", String(page.index + 1));
	button.setAttribute("aria-setsize", String(state.doc.pageCount));
	if (state.editing) button.setAttribute("aria-pressed", String(state.pageSelection.has(page.id)));
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
	button.addEventListener("click", event => selectThumbnailPage(event, page.index));
	button.style.height = `${state.thumbnailWindow.offsets[page.index + 1] - state.thumbnailWindow.offsets[page.index] - 12}px`;
	return button;
}

function releaseThumbnail(button, index) {
	if (state.pageDragIndex === index || button.contains(document.activeElement)) return false;
	state.thumbnailObserver?.unobserve(button);
	state.visibleThumbnails.delete(index);
	return true;
}

function layoutThumbnails() {
	const view = state.thumbnailWindow;
	if (!view) return;
	const visible = !el.pageList.hidden && state.showPages;
	const anchor = visible && view.offsets.length > 1 ? pageWindowAnchor(view) : view.anchor || { index: state.pageIndex, offset: 0 };
	const width = el.navigationContent.clientWidth || view.width || 180;
	view.width = width;
	view.rotation = state.rotation;
	const offsets = new Float64Array(state.doc.pages.length + 1);
	for (let index = 0; index < state.doc.pages.length; index++) {
		const size = pageViewSize(state.doc.pages[index]);
		const ratio = size.width > 0 && size.height > 0 ? size.height / size.width : 1 / 0.707;
		offsets[index + 1] = offsets[index] + Math.max(1, width - 20) * ratio + 70 + 12;
	}
	view.offsets = offsets;
	view.gap = 12;
	view.total = Math.ceil(offsets.at(-1));
	for (const [index, button] of view.nodes) {
		layoutThumbnail(button, state.doc.pages[index]);
		button.style.height = `${offsets[index + 1] - offsets[index] - 12}px`;
	}
	view.pendingTop = offsets[anchor.index] + anchor.offset;
	view.host.style.height = `${Math.min(SCROLL_SEGMENT_SIZE, view.total)}px`;
	if (visible) {
		syncPageWindow(view, view.pendingTop);
		view.pendingTop = null;
	}
}

function syncThumbnailWindow() {
	const view = state.thumbnailWindow;
	if (!view || el.pageList.hidden || !state.showPages) return;
	if (el.navigationContent.clientWidth && (view.width !== el.navigationContent.clientWidth || view.rotation !== state.rotation)) layoutThumbnails();
	syncPageWindow(view, view.pendingTop ?? null);
	view.pendingTop = null;
	prunePageRenderQueue();
}

function revealThumbnail(index) {
	syncThumbnailWindow();
	const view = state.thumbnailWindow;
	if (!view) return;
	const top = pageWindowTop(view);
	const start = view.offsets[index], end = view.offsets[index + 1] - view.gap;
	if (start < top || end > top + el.navigationContent.clientHeight) {
		const target = start < top ? Math.floor(start) : Math.min(start, Math.ceil(end - el.navigationContent.clientHeight));
		syncPageWindow(view, target);
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

function pageDropIndex(indexes, target, after) {
	const position = target + Number(after);
	return position - indexes.filter(index => index < position).length;
}

function selectDragPages(index) {
	if (!state.pageSelection.has(state.doc.pages[index].id)) {
		state.pageSelection = new Set([state.doc.pages[index].id]);
		state.pageSelectionAnchor = state.doc.pages[index].id;
		syncPageSelection();
		updateControls();
	}
	return selectedPageIndexes();
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
		const indexes = selectDragPages(index);
		state.pageDragIndex = index;
		let x = event.clientX, y = event.clientY, position = null, marker = null, frame = 0, dragging = false;
		const clearMarker = () => { marker?.classList.remove("drop-before", "drop-after"); marker = null; };
		const locate = () => {
			clearMarker();
			position = null;
			const node = document.elementFromPoint(x, y)?.closest(".page-list-item");
			if (!node || !el.pageList.contains(node)) return;
			const rect = node.getBoundingClientRect(), after = y > rect.top + rect.height / 2;
			const target = Number(node.dataset.pageIndex), to = pageDropIndex(indexes, target, after);
			if (indexes.some((index, offset) => index !== to + offset)) {
				position = target + Number(after);
				marker = node;
				marker.classList.add(after ? "drop-after" : "drop-before");
			}
		};
		const tick = () => {
			if (seq !== state.openSeq || !state.editing) { finish(false); return; }
			if (dragging) {
				const rect = el.navigationContent.getBoundingClientRect();
				const delta = y < rect.top + 48 ? -10 : y > rect.bottom - 48 ? 10 : 0;
				if (delta) {
					if (state.thumbnailWindow) scrollPageWindowBy(state.thumbnailWindow, delta);
					else el.navigationContent.scrollTop += delta;
				}
				syncThumbnailWindow();
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
			state.pageDragIndex = null;
			cancelAnimationFrame(frame);
			grip.removeEventListener("pointermove", move);
			grip.removeEventListener("pointerup", up);
			grip.removeEventListener("pointercancel", cancel);
			grip.removeEventListener("lostpointercapture", cancel);
			clearMarker();
			button.classList.remove("reordering");
			if (grip.hasPointerCapture(pointer)) grip.releasePointerCapture(pointer);
			if (commit && dragging && position !== null && seq === state.openSeq && await canvasEditor.commitText() && await canvasEditor.commitCrop()) {
				await changeDocument("ofdgoBatchPages", null, "move", selectedPageRange(indexes), position);
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
		const indexes = selectDragPages(index), direction = event.key === "ArrowUp" ? -1 : 1;
		const target = direction < 0 ? indexes[0] - 1 : indexes.at(-1) + 1;
		if (target >= 0 && target < state.doc.pageCount && await canvasEditor.commitText() && await canvasEditor.commitCrop()) {
			const focus = (direction < 0 ? target : target - indexes.length + 1) + indexes.indexOf(index);
			if (await changeSelectedPages("move", direction)) {
				revealThumbnail(focus);
				el.pageList.querySelector(`[data-page-index="${focus}"]`)?.focus({ preventScroll: true });
			}
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
	if (!button) return;
	const observer = thumbnailObserver();
	if (observer) {
		observer.observe(button);
	}
	if (index === state.pageIndex || !observer) {
		if (!observer) state.visibleThumbnails.add(index);
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
				if (state.thumbnailWindow && state.thumbnailWindow.nodes.get(index) !== entry.target) continue;
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

function renderMeta(keepDetails = false) {
	renderSecurity();
	el.conversionNotice.hidden = !state.conversionWarnings.length;
	renderMetaContent(el.conversionWarnings, state.conversionWarnings, () => {
		el.conversionWarnings.replaceChildren(...state.conversionWarnings.map(warning => {
			const item = document.createElement("p");
			item.textContent = `第${warning.Page}页 · 位置${warning.Offset}：${warning.Message}`;
			return item;
		}));
	});
	const doc = state.doc || {};
	el.metaPanel.setAttribute("aria-busy", String(!!doc.detailsPending));
	document.title = `OFDGo WebUI - ${state.fileName}`;
	el.metaFile.textContent = state.fileName;
	el.metaTitle.textContent = doc.title || "-";
	el.metaAuthor.textContent = doc.author || "-";
	for (const [field, value] of [
		[el.metaSubject, (doc.subject || "").trim()],
		[el.metaCreationDate, formatDocumentTime(doc.creationDate)],
		[el.metaModDate, formatDocumentTime(doc.modDate)],
		[el.metaCreator, [doc.creator, doc.creatorVersion].filter(Boolean).join(" ")],
	]) {
		field.textContent = value;
		field.parentElement.hidden = !value;
	}
	el.metaType.textContent = doc.docType || "-";
	el.metaVersion.textContent = doc.version || "-";
	el.metaFonts.textContent = String(doc.fontCount || 0);
	el.pageTotal.textContent = String(doc.pageCount || 0);
	if (keepDetails && doc.detailsPending) return;
	el.metaSignatures.textContent = doc.detailsPending ? "正在检查" : doc.detailsError ? "读取失败" : String(doc.signatureCount || 0);
	renderMetaContent(el.attachmentList, [doc.attachments, doc.attachmentError], renderAttachments);
	renderMetaContent(el.signatureList, [doc.signatures, doc.signatureError], renderSignatures);
	renderMetaContent(el.docFontList, doc.fonts || [], renderDocumentFonts);
	refreshEditorFonts();
	updateLocalFontButton();
}

function renderMetaContent(node, value, render) {
	const key = JSON.stringify(value);
	if (metaContents.get(node) === key) return;
	render();
	metaContents.set(node, key);
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
		badges.append(fontBadge(signature.trustedValid ? "可信" : signature.status === "valid" ? "完整" : signature.status === "invalid" ? "异常" : "未验", signature.status));

		head.append(badges, name);
		row.append(head);
		appendInfoLine(row, "签署人", signature.signer);
		appendInfoLine(row, "时间", formatDocumentTime(signature.signatureDateTime));
		appendInfoLine(row, "章名", signature.sealName);
		appendInfoLine(row, "机构", signatureAgency(signature));
		appendSignatureCheck(row, "信任", signature.certTrustChecked, signature.certTrustOK);
		appendInfoLine(row, "保护文件", signatureReferenceText(signature), signatureReferenceStatus(signature));
		appendInfoLine(row, "错误", signature.error, "fail");
		const checks = document.createElement("details");
		const checksTitle = document.createElement("summary");
		checksTitle.textContent = "校验详情";
		checks.append(checksTitle);
		appendSignatureCheck(checks, "原文", signature.dataHashChecked, signature.dataHashOK);
		appendSignatureCheck(checks, "签名", signature.signedValueChecked, signature.signedValueOK);
		appendSignatureCheck(checks, "证书", signature.certChecked, signature.certOK);
		appendSignaturePolicy(checks, "证书期限", signature.certTimeChecked, signature.certTimeOK);
		appendInfoLine(checks, "信任错误", signature.certTrustError, "fail");
		appendSignaturePolicy(checks, "签署时间", signature.signatureTimeChecked, signature.signatureTimeOK);
		if (signature.type !== "Sign") {
			appendSignatureCheck(checks, "印章", signature.sealChecked, signature.sealOK);
			appendSignaturePolicy(checks, "印章匹配", signature.sealMatchChecked, signature.sealMatchOK);
			appendSignaturePolicy(checks, "印章期限", signature.sealTimeChecked, signature.sealTimeOK);
			if (signature.sealCertTimeChecked) {
				appendInfoLine(checks, "制章证书", signature.sealCertTimeOK ? "有效" : "失效", signature.sealCertTimeOK ? "ok" : "fail");
			}
		}
		appendSignatureCheck(checks, "覆盖", signature.coverageChecked, signature.coverageOK);
		appendInfoLine(checks, "覆盖错误", signature.coverageError, "fail");
		appendInfoLine(checks, "未保护", signature.uncoveredFiles?.join("、"), "fail");
		appendSignatureCheck(checks, "策略", signature.policyChecked, signature.policyOK);
		appendInfoLine(checks, "策略错误", signature.policyError, "fail");
		appendSignatureCheck(checks, "时间戳", signature.timestampChecked, signature.timestampOK);
		appendSignatureCheck(checks, "撤销", signature.revocationChecked, signature.revocationOK);
		row.append(checks);
		for (const timestamp of signature.timestamps || []) {
			const details = document.createElement("details");
			const title = document.createElement("summary");
			title.textContent = timestamp.valid ? "可信时间戳" : "时间戳详情";
			details.append(title);
			appendInfoLine(details, timestamp.valid ? "时间" : "声明时间", formatDocumentTime(timestamp.time));
			appendSignatureCheck(details, "绑定", timestamp.bindingChecked, timestamp.bindingOK);
			appendSignatureCheck(details, "签名", timestamp.signedValueChecked, timestamp.signedValueOK);
			appendSignatureCheck(details, "信任", timestamp.certTrustChecked, timestamp.certTrustOK);
			appendSignatureCheck(details, "时效", timestamp.certTimeChecked, timestamp.certTimeOK);
			appendInfoLine(details, "错误", timestamp.error, "fail");
			row.append(details);
		}
		for (const revocation of signature.revocations || []) {
			const details = document.createElement("details");
			const title = document.createElement("summary");
			title.textContent = "撤销详情";
			details.append(title);
			appendInfoLine(details, "证书", revocation.subject);
			appendInfoLine(details, "来源", revocation.source);
			const valid = revocation.checked && revocation.ok && revocation.status === "good";
			appendInfoLine(details, "状态", !revocation.checked ? "未验" : valid ? "未撤销" : revocation.status === "revoked" ? "已撤销" : "未知", valid ? "ok" : revocation.checked ? "fail" : "");
			appendInfoLine(details, "更新", formatDocumentTime(revocation.thisUpdate));
			appendInfoLine(details, "截止", formatDocumentTime(revocation.nextUpdate));
			appendInfoLine(details, "撤销时间", formatDocumentTime(revocation.revokedAt));
			appendInfoLine(details, "错误", revocation.error, "fail");
			row.append(details);
		}
		const details = document.createElement("details");
		const title = document.createElement("summary");
		title.textContent = "签章信息";
		details.append(title);
		appendInfoLine(details, "编号", signature.id);
		appendInfoLine(details, "文档", signature.docRoot);
		appendInfoLine(details, "算法", signature.signatureMethod);
		appendInfoLine(details, "摘要", signature.digestMethod);
		appendInfoLine(details, "证书主体", signature.signSubject);
		appendInfoLine(details, "颁发者", signature.signIssuer);
		appendInfoLine(details, "序列号", signature.signSerial);
		appendInfoLine(details, "章号", signature.sealId);
		appendInfoLine(details, "章图", signature.sealType);
		appendInfoLine(details, "制章证书", signature.sealSubject);
		appendInfoLine(details, "印章厂商", signature.sealVendor);
		appendInfoLine(details, "组件版本", signature.version);
		row.append(details);
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
	return parts.length > 1 ? parts.join(" · ") : `${signatures.every(signature => signature.trustedValid) ? "可信" : "完整"} ${signatures.length}`;
}

function signatureNameNode(signature) {
	const stamps = signature.docIndex ? [] : signature.stamps || [];
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
	const text = String(value || "").trim().replace(/^(\d{4})(\d{2})(\d{2})(\d{2})(\d{2})(\d{2})(\.\d+)?(Z|[+-]\d{2}:?\d{2})?$/, "$1-$2-$3 $4:$5:$6$7$8").replace("T", " ");
	return /^0001[-/]0?1[-/]0?1(?: 0{1,2}:00:00(?:\.0+)?(?:Z|[+-]00:?00)?)?$/.test(text) ? "" : text;
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
	setStatus(`已定位${label}（第${region.page}页）`);
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
	if (state.pageWindow) {
		const box = mark.getBoundingClientRect(), viewer = el.viewerPanel.getBoundingClientRect();
		if (box.top < viewer.top) scrollViewerBy(box.top - viewer.top);
		else if (box.bottom > viewer.top + el.viewerPanel.clientHeight) scrollViewerBy(Math.min(box.top - viewer.top, box.bottom - viewer.top - el.viewerPanel.clientHeight));
		if (box.left < viewer.left) el.viewerPanel.scrollLeft += box.left - viewer.left;
		else if (box.right > viewer.left + el.viewerPanel.clientWidth) el.viewerPanel.scrollLeft += box.right - viewer.left - el.viewerPanel.clientWidth;
	} else mark.scrollIntoView({ block: "nearest", inline: "nearest" });
	window.setTimeout(() => mark.remove(), 1800);
}

function clearRegionHighlights() {
	for (const mark of el.svgHost.querySelectorAll(".region-highlight")) {
		mark.remove();
	}
}

function mountAnnotationNotes(index, surface, annotations) {
	for (const note of surface.querySelectorAll(".annotation-note")) note.remove();
	if (!state.renderAnnotations) return;
	const page = state.doc.pages[index];
	for (const annotation of annotations || []) {
		if (!annotation.visible || annotation.page !== index + 1 || !annotation.remark?.trim()) continue;
		const note = document.createElement("button");
		note.type = "button";
		note.className = "annotation-note";
		note.classList.toggle("no-zoom", !!annotation.noZoom);
		note.classList.toggle("no-rotate", !!annotation.noRotate);
		note.textContent = "\u24d8";
		note.title = "查看注解";
		note.setAttribute("aria-label", "查看注解");
		note.setAttribute("aria-haspopup", "dialog");
		note.style.left = `${Math.max(22 / MM_TO_PX, Math.min(page.width, annotation.x + annotation.width)) * MM_TO_PX}px`;
		note.style.top = `${Math.max(0, Math.min(page.height - 22 / MM_TO_PX, annotation.y)) * MM_TO_PX}px`;
		note.addEventListener("click", () => {
			if (state.editing || document.body.hasAttribute("aria-busy")) return;
			el.annotationDetail.replaceChildren();
			el.annotationFields.hidden = el.annotationSubmit.hidden = true;
			el.annotationDetail.hidden = false;
			el.annotationStatus.textContent = "";
			el.annotationClose.textContent = "关闭";
			const content = document.createElement("div");
			content.textContent = annotation.remark;
			el.annotationDetail.append(content);
			appendInfoLine(el.annotationDetail, "作者", annotation.creator);
			appendInfoLine(el.annotationDetail, "时间", formatDocumentTime(annotation.lastModDate));
			el.annotationNote.showModal();
		});
		surface.append(note);
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
	if (state.pageWindow) {
		updateFitSpace();
		layoutPages();
	}
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
	layoutThumbnails();
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
	if (!state.panMode || !state.doc || event.pointerType !== "mouse" || event.button !== 0 || document.body.hasAttribute("aria-busy") || event.target.closest("a, button")) {
		return;
	}
	const rect = el.viewerPanel.getBoundingClientRect();
	if (event.clientX >= rect.left + el.viewerPanel.clientWidth || event.clientY >= rect.top + el.viewerPanel.clientHeight) {
		return;
	}
	event.preventDefault();
	state.pan = { id: event.pointerId, x: event.clientX + el.viewerPanel.scrollLeft,
		y: event.clientY + (state.pageWindow ? pageWindowTop(state.pageWindow) : el.viewerPanel.scrollTop) };
	el.viewerPanel.setPointerCapture(event.pointerId);
	el.viewerPanel.classList.add("panning");
	canvasEditor.focus();
}

function movePan(event) {
	const pan = state.pan;
	if (!pan || event.pointerId !== pan.id) {
		return;
	}
	el.viewerPanel.scrollLeft = pan.x - event.clientX;
	if (state.pageWindow) syncPageWindow(state.pageWindow, pan.y - event.clientY);
	else el.viewerPanel.scrollTop = pan.y - event.clientY;
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
		const limit = Math.max(1, el.viewerPanel.clientHeight - space * 2);
		let height = Math.max(1, page.height * MM_TO_PX) * (available / width);
		if (state.continuous) {
			height = 0;
			for (const item of state.doc.pages) {
				const size = pageViewSize(item);
				height += available * size.height / size.width;
				if (height > limit) break;
			}
		}
		if (height > limit) {
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
	if (state.pageWindow && state.pageWindow.gap !== (Number.parseFloat(getComputedStyle(el.svgHost).rowGap) || 0)) layoutPages();
	if (layoutChanged) {
		restoreScaleAnchor(anchor);
	}
	el.zoomLabel.textContent = `${Math.round(state.scale * 100)}%`;
	if (updateStatus && state.doc && !state.exporting) {
		setStatus(`第 ${state.pageIndex + 1} / ${state.doc.pageCount} 页`);
	}
	updateControls();
}

function scaleAnchor(viewY = 0.45) {
	const shell = pageShellFromView() || pageShell(state.pageIndex);
	if (!shell) {
		return null;
	}
	const viewerRect = el.viewerPanel.getBoundingClientRect();
	const shellRect = shell.getBoundingClientRect();
	return {
		index: Number.parseInt(shell.dataset.pageIndex, 10),
		viewY,
		x: (viewerRect.left + viewerRect.width / 2 - shellRect.left) / Math.max(1, shellRect.width),
		y: (viewerRect.top + viewerRect.height * viewY - shellRect.top) / Math.max(1, shellRect.height),
	};
}

function restoreScaleAnchor(anchor) {
	if (!anchor) {
		return;
	}
	if (state.fitMode === "height" || (state.fitMode === "width" && anchor.y <= 0)) {
		scrollToPage(anchor.index);
		return;
	}
	if (state.pageWindow) {
		const view = state.pageWindow;
		const height = view.offsets[anchor.index + 1] - view.offsets[anchor.index] - view.gap;
		syncPageWindow(view, view.offsets[anchor.index] + height * anchor.y - el.viewerPanel.clientHeight * anchor.viewY);
	}
	const shell = pageShell(anchor.index);
	if (!shell) {
		return;
	}
	const viewerRect = el.viewerPanel.getBoundingClientRect();
	const shellRect = shell.getBoundingClientRect();
	el.viewerPanel.scrollLeft += shellRect.left + shellRect.width * anchor.x - viewerRect.left - viewerRect.width / 2;
	scrollViewerBy(shellRect.top + shellRect.height * anchor.y - viewerRect.top - viewerRect.height * anchor.viewY);
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
	renderSecurity();
	const hasDoc = Boolean(state.doc);
	updateDisplayControls();
	updateEditorTools();
	const pageCount = state.doc ? state.doc.pageCount : 0;
	el.prevButton.disabled = !hasDoc || state.pageIndex <= 0;
	el.nextButton.disabled = !hasDoc || state.pageIndex >= pageCount - 1;
	el.pageInput.disabled = !hasDoc;
	el.pageInput.max = String(pageCount || 1);
	el.pageInput.value = String(hasDoc ? state.pageIndex + 1 : 0);
	el.pageControl.style.setProperty("--page-width", `${String(pageCount || 1).length + 0.5}ch`);
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
	el.viewerPanel.classList.toggle("editing", state.editing);
	el.editButton.disabled = !state.doc || !state.ready || state.exporting || document.body.hasAttribute("aria-busy");
	el.editButton.setAttribute("aria-pressed", String(state.editing));
	el.editButton.title = state.editing ? "阅读" : "编辑";
	el.editButton.setAttribute("aria-label", el.editButton.title);
	el.editNotice.textContent = state.editorInfo?.editWarnings?.join("；") || "";
	el.infoButton.hidden = !state.editing;
	el.editNotice.hidden = !state.editing || !el.editNotice.textContent;
	el.editorTools.hidden = !state.editing;
	const pagesDisabled = !state.editing || !state.ready || state.exporting;
	el.insertTextButton.disabled = el.insertImageButton.disabled = pagesDisabled || !pageCan("insert");
	el.saveButton.disabled = !state.doc || !state.ready || state.exporting || (!state.editorInfo && !state.ofdBytes);
	el.addPageButton.disabled = pagesDisabled;
	const selectedPages = state.editing ? selectedPageIndexes() : [state.pageIndex];
	const selectionDisabled = pagesDisabled || !selectedPages.length;
	el.batchPagesButton.disabled = selectionDisabled;
	el.copyPageButton.disabled = selectionDisabled || selectedPages.some(index => !pageCan("copy", index));
	el.pageSettingsButton.disabled = selectionDisabled || selectedPages.some(index => !pageCan("resize", index));
	el.deletePageButton.disabled = selectionDisabled || selectedPages.some(index => !pageCan("delete", index)) || selectedPages.length >= state.doc?.pageCount;
	const moveDisabled = selectionDisabled || selectedPages.some(index => !pageCan("move", index));
	el.movePagePrevButton.disabled = moveDisabled || selectedPages[0] === 0;
	el.movePageNextButton.disabled = moveDisabled || selectedPages.at(-1) === state.doc?.pageCount - 1;
	const enabled = state.editing && state.selectObjects && !state.panMode;
	canvasEditor.setEnabled(enabled);
	updateObjectControls(canvasEditor.selected);
	el.undoButton.disabled = !state.editorInfo?.canUndo || !state.ready || state.exporting;
	el.redoButton.disabled = !state.editorInfo?.canRedo || !state.ready || state.exporting;
}

function updateObjectControls(item, reset = false) {
	el.compositeBackButton.hidden = !state.composite;
	el.compositeBackButton.disabled = !state.ready || state.exporting;
	el.selectObjectButton.hidden = Boolean(state.composite);
	const members = item?.items || (item ? [item] : []);
	const disabled = !item || Boolean(item.draft) || !state.ready || state.exporting;
	el.deleteObjectButton.disabled = disabled || !canEditObject(item, "delete");
	el.copyObjectButton.disabled = disabled || !canEditObject(item, "copy") || Boolean(state.composite && members.some(member => member.container !== members[0].container));
	el.objectStyleButton.disabled = disabled || !canEditObject(item, "transform");
	const cropping = Boolean(canvasEditor.crop);
	el.objectBoundsButton.disabled = disabled || cropping || !canEditObject(item, "transform");
	el.objectAlign.disabled = disabled || cropping || !canEditObject(item, "arrange");
	el.objectRotate.disabled = el.objectFlip.disabled = disabled || cropping || !canEditObject(item, state.composite ? "transform" : "arrange");
	el.cropImageButton.disabled = disabled || item.type !== "ImageObject" || !canEditObject(item, "cropImage");
	el.imageFit.disabled = el.cropImageButton.disabled || cropping || Boolean(item.scoped && !canEditObject(item, "fitImage"));
	el.cropImageButton.textContent = cropping ? "完成" : "裁剪";
	el.cropImageButton.setAttribute("aria-label", cropping ? "完成裁剪" : "裁剪图片");
	el.cropImageButton.setAttribute("aria-pressed", String(cropping));
	el.resetCropButton.textContent = cropping ? "取消" : "还原";
	el.resetCropButton.setAttribute("aria-label", cropping ? "取消裁剪" : "还原图片");
	el.resetCropButton.disabled = canEditObject(item, "resetCrop") ? disabled : el.cropImageButton.disabled || !cropping && (item.scoped ? !item.cropped : !item.imageBounds || ["x", "y", "width", "height"].every(key => Math.abs(item[key] - item.imageBounds[key]) < 1e-9));
	el.objectDistribute.disabled = el.objectAlign.disabled || !item.items || item.items.length < 3;
	el.editObjectButton.disabled = disabled || Boolean(item.items) || !canEditObject(item, "enter") && (item.type === "PathObject"
		|| !(item.type === "TextObject" && !item.scoped && canEditObject(item, "rewriteText")) && !canEditObject(item, item.type === "TextObject" ? "textContent" : item.type === "ImageObject" ? "replaceImage" : "update"));
	el.editObjectButton.textContent = canEditObject(item, "enter") ? "进入" : item?.type === "ImageObject" ? "替换" : "修改";
	el.editObjectButton.title = canEditObject(item, "enter") ? item?.type === "Annotation" ? "进入注解" : "进入组合" : item?.type === "ImageObject" ? "替换图片" : item?.type === "Annotation" ? "修改注解" : "修改对象";
	el.editObjectButton.setAttribute("aria-label", el.editObjectButton.title);
	el.multiSelectButton.disabled = !state.editing || !canvasEditor.enabled || !state.ready || state.exporting;
	const selection = selectedText(item);
	const text = selection ? currentTextStyle() : state.textDefaults;
	const textDisabled = !state.editing || !state.ready || state.exporting || Boolean(item?.items && !selection);
	fontPicker.setDisabled(textDisabled || text !== state.textDefaults && !item.draft && !canEditObject(item, "replaceFont"));
	el.textSize.disabled = textDisabled || text !== state.textDefaults && !item.draft && (!canEditObject(item, "reflow") || Boolean(item?.items) && !canEditObject(item, "layoutKnown"));
	el.textColor.disabled = textDisabled || text !== state.textDefaults && !item.draft && !canEditObject(item, "paint");
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
	el.textSize.title = text.size === undefined ? "字号不同" : `字号${displayPoints(text.size)}pt`;
	el.textColor.classList.toggle("mixed-color", Boolean(selection?.items && !text.color));
	el.textColor.title = selection?.items && !text.color ? "文字颜色不同" : "文字颜色";
	if (reset || el.textColor.disabled || document.activeElement !== el.textColor) {
		el.textColor.value = text.color || "#000000";
	}
	const path = selectedPath(item);
	for (const [input, key] of [[el.shapeFill, "fill"], [el.shapeStroke, "stroke"]]) {
		input.indeterminate = Boolean(path?.items && path[key] === undefined);
		if (path) input.checked = Boolean(path[key]);
	}
	for (const [input, key, label] of [[el.shapeFillColor, "fillColor", "填充颜色"], [el.shapeStrokeColor, "strokeColor", "描边颜色"]]) {
		const mixed = Boolean(path?.items && path[key] === undefined);
		input.classList.toggle("mixed-color", mixed);
		input.title = mixed ? `${label}不同` : label;
		if (path && (reset || document.activeElement !== input)) input.value = path[key] || "#000000";
	}
	el.shapeWidth.placeholder = path?.items && path.lineWidth === undefined ? "混合" : "";
	if (path && canEditObject(path, "paint")) {
		if (reset || document.activeElement !== el.shapeWidth) {
			el.shapeWidth.value = path.lineWidth === undefined ? "" : displayPoints(path.lineWidth);
		}
	} else if (!item && !el.shapeWidth.value) {
		el.shapeWidth.value = "1";
	}
	updateDrawingControls();
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
	el.annotationButton.title = state.renderAnnotations ? "隐藏注解" : "显示注解";
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
		if (name === "ofdgoConvertPDF" || name === "ofdgoExportPage" || name === "ofdgoExportDocument" || name === "ofdgoExportAttachment" || name === "ofdgoSaveDocument" || name === "ofdgoSaveEncrypted" || name === "ofdgoSaveSigned" || name === "ofdgoImportPages") {
			state.exportRequestID = id;
			el.cancelExportButton.hidden = false;
			el.cancelExportButton.disabled = false;
			el.cancelExportButton.title = { ofdgoConvertPDF: "取消转换", ofdgoSaveDocument: "取消保存", ofdgoSaveEncrypted: "取消保存", ofdgoSaveSigned: "取消签署", ofdgoImportPages: "取消导入", ofdgoExportAttachment: "取消下载" }[name] || "取消导出";
			el.cancelExportButton.setAttribute("aria-label", el.cancelExportButton.title);
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
	renderSecurity();
	updateDisplayControls();
	el.editButton.disabled = busy || !state.doc || !state.ready || state.exporting;
	el.createForm.inert = busy;
	el.insertForm.inert = busy;
	el.pageForm.inert = busy;
	el.paragraphForm.inert = busy;
	el.infoForm.inert = busy;
	el.annotationForm.inert = busy;
	el.importForm.inert = busy && !state.importing;
	for (const input of [el.importFile, el.importRange, el.importPosition, el.importOutlines]) input.disabled = busy;
	el.importPages.disabled = busy || el.importRange.value !== "custom";
	el.importSubmit.disabled = busy || !state.importPageCount;
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
	el.imageDPI.disabled = state.exporting || !state.doc || document.body.hasAttribute("aria-busy") || (displayMode() !== "raster" && !exportFormatUsesDPI(el.exportFormat.value));
	el.imageDPI.title = `DPI：${currentImageDPI()}`;
	el.dpiValue.textContent = String(currentImageDPI());
	const format = exportFormatInfo(el.exportFormat.value);
	el.formatValue.textContent = format?.label || "";
	el.exportFormat.title = format ? `导出格式：${format.label}` : "导出格式";
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
