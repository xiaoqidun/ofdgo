import { SVG_NAMESPACE, CanvasEditor, canEditObject, editTextValue, richTextModel, richTextAlignment, richTextLayout, fontFaceStyle, missingGlyphMessage, objectEditReason, selectedText, lineShape, selectionBounds, pagePoint } from "./ofdgo_edit.js";
import { FontManager, FontPicker } from "./ofdgo_font.js";
import { nativeHost, createEngineWorker, engineLimits, documentBytes, saveFilePicker, directoryPicker } from "./ofdgo_host.js";
import { PrintDialog } from "./ofdgo_print.js";
import { DocumentDialogs, SelectPicker } from "./ofdgo_util.js";
import { MM_TO_PX, SCROLL_SEGMENT_SIZE, createPageView } from "./ofdgo_view.js";

const COMPACT_LAYOUT = window.matchMedia("(max-width: 900px)");
const DEFAULT_IMAGE_DPI = 300;
const OBJECT_CLIPBOARD_TYPE = "application/x-ofdgo-objects";
const RESOURCE_KINDS = [["Font", "字体"], ["Image", "图片"], ["Audio", "音频"], ["Video", "视频"], ["Attachment", "附件"], ["ColorSpace", "色彩"], ["DrawParam", "样式"]];
const SETTINGS_KINDS = [["bookmarks", "书签"], ["actions", "动作"], ["templates", "模板"], ["layers", "图层"], ["extensions", "扩展"]];
const INFO_FIELDS = ["docId", "title", "author", "subject", "abstract", "creationDate", "modDate", "docUsage", "cover", "creator", "creatorVersion"];
const STATUS = {
	ready: "选择 OFD、PDF 文件",
	opening: "正在打开文档",
	engine: "正在准备引擎",
	recovering: "正在恢复引擎",
	fonts: "正在匹配字体",
	exporting: "正在导出文档",
	pageExporting: "正在导出单页",
};
const wasmRequests = new Map();
const metaContents = new WeakMap();
const batch = { items: [], formats: [], running: false, canceled: false, workers: new Set(), requests: new Map(), sequence: 0, progress: null, jobs: [], credentials: Promise.resolve() };
const batchElements = Object.fromEntries(["Button", "Panel", "Form", "Input", "Add", "Clear", "Count", "Format", "Compression", "FontRow", "FontMode", "Destination", "DPIRow", "DPI", "DPIValue", "List", "Empty", "Progress", "Status", "Close", "Cancel", "Start"].map(name => [name, document.querySelector(`#convert${name}`)]));
const fontBinding = { font: null, choices: [], sequence: 0 };
const fontBindingElements = Object.fromEntries(["Panel", "Form", "Title", "Name", "SourceRow", "Source", "File", "Hint", "Status", "Cancel", "Submit"].map(name => [name, document.querySelector(`#fontBinding${name}`)]));
const resourceElements = Object.fromEntries(["ManageButton", "Panel", "Form", "Tabs", "List", "Empty", "Reference", "ReferenceGroup", "Actions", "Add", "Preview", "Export", "Rename", "Replace", "Delete", "Locate", "Close", "Binding", "BindingActions", "Page", "Object", "Pick", "Bind", "Unbind", "Viewer", "Status", "File"].map(name => [name, document.querySelector(`#resource${name}`)]));
let resourceManager = createResourceManager();
const settingsElements = Object.fromEntries(["Button", "Panel", "Form", "Title", "Tabs", "Context", "Content", "Actions", "Status"].map(name => [name, document.querySelector(`#settings${name}`)]));
let settings = createSettings();
let invoicePanel = createInvoicePanel();
const invoiceDocuments = new WeakMap();
const invoiceReferenceStandard = "http://www.ofdspec.org/2016";

let remoteDownload = null;
let wasmWorker = null;
let wasmRequestID = 0;
let wasmRecoveryTimer = 0;
let credentialRequest = null;
let documentDialogs = null;
let confirmationTask = Promise.resolve();
let cancelPageTouch = null;

const workspace = { tabs: [], active: null, sequence: 0, nextID: 0, switching: false, closing: false, objectClipboard: null, textClipboard: null };
const busyControls = new Set();

function createResourceManager() {
	return { kind: "Font", scope: "", views: new Map(), items: [], filteredItems: [], start: -1, end: -1, selected: null, references: [], referenceOffset: 0, targets: [], pick: null, busy: false, sequence: 0, url: null, fileAction: null };
}

function createSettings() {
	return { kind: "bookmarks", locked: false, views: new Map(), sequence: 0, busy: false, page: 0, scope: "document", object: "", path: [], selected: -1, data: null, actions: [], drafts: new Map(), view: "", baseline: {}, suspended: false, invoice: false };
}

function createInvoicePanel() {
	return { tab: "basic", views: new Map(), entries: [], formats: [], drafts: new Map(), key: "", result: null };
}

function createDocumentState() {
	return {
		conversionWarnings: [],
		annotationEdit: null,
		annotationCreate: null,
		ready: false,
		wasmExited: false,
		wasmSeq: 0,
		wasmRecovering: false,
		wasmRecoveries: 0,
		exporting: false,
		saving: false,
		saveRequestID: 0,
		signing: false,
		signCanceled: false,
		exportRequestID: 0,
		editing: false,
		selectObjects: true,
		dirty: false,
		editorInfo: null,
		editorViews: new Map(),
		savedRevision: null,
		saveHandle: null,
		saveStamp: null,
		packageDirty: false,
		encryptionDirty: false,
		insertObject: null,
		importPageCount: 0,
		importDocumentCount: 0,
		importDocumentMode: false,
		importEncrypted: false,
		importing: false,
		imageImporting: false,
		createInsert: false,
		createPackage: false,
		createDocumentAt: null,
		createImageQueue: null,
		documentManagement: null,
		versionManagement: null,
		infoDocumentIndex: null,
		pageSelection: new Set(),
		pageSelectionAnchor: null,
		pageMultiSelect: false,
		pageTouchPointer: null,
		outlineSelection: null,
		outlineSeq: 0,
		openActionEnabled: false,
		openActionDocument: false,
		openActionPage: null,
		openActionTask: null,
		openActionFrame: 0,
		outlineAction: "add",
		styleOriginal: null,
		objectClipboardToken: null,
		styleClipboard: null,
		objectDistribution: null,
		outlineExpanded: new Map(),
		textFonts: [],
		textFontID: null,
		textFontInitialized: false,
		textFontInitializing: null,
		textDefaults: { type: "TextObject", size: 16 * 25.4 / 72, color: "#000000", wrap: true, align: "left", paragraphHeight: 0, letterSpacing: 0 },
		ofdBytes: null,
		sourcePDF: false,
		pdfComplete: false,
		fileName: "ofdgo.ofd",
		openSeq: 0,
		pageSeq: 0,
		searchSeq: 0,
		searchQuery: "",
		searchMatches: [],
		searchIndex: -1,
		searchRunning: false,
		searchNext: 0,
		searchPaused: false,
		searchTruncated: false,
		navigationScroll: new Map(),
		fontSyncPending: false,
		fontCatalogLoading: false,
		fontFacesLoading: false,
		fontRenderPending: false,
		fontSyncing: false,
		doc: null,
		pageIndex: 0,
		pageNavigation: null,
		scale: 1,
		rotation: 0,
		panMode: false,
		pan: null,
		fitMode: "width",
		continuous: false,
		pageLayoutPreference: 1,
		get pageLayout() {
			return this.doc?.pageCount > 1 ? this.pageLayoutPreference : 1;
		},
		renderAnnotations: true,
		renderBackend: "canvas",
		renderMode: "svg",
		renderDPI: DEFAULT_IMAGE_DPI,
		pageCache: new Map(),
		mediaPlayers: new Map(),
		svgFonts: new Map(),
		svgFontLoads: new Map(),
		svgImages: new Map(),
		selectedPages: new Set(),
		documentSelection: null,
		pageInFlight: new Map(),
		pageLoadSeq: 0,
		pageRenderQueue: [],
		pagePrefetchIndex: null,
		pagePrefetchDirection: 1,
		pageRenderRunning: false,
		pageRenderWorkers: 0,
		pagePresentQueue: new Map(),
		pagePresentFrame: 0,
		pageObserver: null,
		visiblePages: new Set(),
		scrollFrame: 0,
		pageWindow: null,
		thumbnailWindow: null,
		thumbnailFrame: 0,
		pageDragIndex: null,
		pageListCurrent: null,
		thumbnailObserver: null,
		visibleThumbnails: new Set(),
		exportFormats: [],
		exportPages: null,
		exportBackdrop: false,
		encryptionChanged: false,
		showPages: !COMPACT_LAYOUT.matches,
		showMeta: !COMPACT_LAYOUT.matches,
	};
}

let state = createDocumentState();
let createImagePicker = null;
const createPaperSizes = { a4: [210, 297], a3: [297, 420], a5: [148, 210] };

const el = {
	appearanceButton: document.querySelector("#appearanceButton"),
	appearancePanel: document.querySelector("#appearancePanel"),
	printButton: document.querySelector("#printButton"),
	printPanel: document.querySelector("#printPanel"),
	documentTitleBar: document.querySelector("#documentTitleBar"),
	documentTabs: document.querySelector("#documentTabs"),
	documentNewTab: document.querySelector("#documentNewTab"),
	aboutButton: document.querySelector("#aboutButton"),
	aboutPanel: document.querySelector("#aboutPanel"),
	aboutVersion: document.querySelector("#aboutVersion"),
	associationSection: document.querySelector("#associationSection"),
	associationCategory: document.querySelector("#associationCategory"),
	associationMessage: document.querySelector("#associationMessage"),
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
	securityScheme: document.querySelector("#securityScheme"),
	securitySchemeRow: document.querySelector("#securitySchemeRow"),
	encryptSaveButton: document.querySelector("#encryptSaveButton"),
	credentialsPanel: document.querySelector("#credentialsPanel"),
	credentialsType: document.querySelector("#credentialsType"),
	credentialsTypeRow: document.querySelector("#credentialsTypeRow"),
	credentialsUserRow: document.querySelector("#credentialsUserRow"),
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
	encryptionPanel: document.querySelector("#encryptionPanel"),
	encryptionForm: document.querySelector("#encryptionForm"),
	encryptionKeep: document.querySelector("#encryptionKeep"),
	encryptionCurrentRow: document.querySelector("#encryptionCurrentRow"),
	encryptionCurrent: document.querySelector("#encryptionCurrent"),
	encryptionChange: document.querySelector("#encryptionChange"),
	encryptionCancel: document.querySelector("#encryptionCancel"),
	encryptionSubmit: document.querySelector("#encryptionSubmit"),
	encryptionStatus: document.querySelector("#encryptionStatus"),
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
	closeButton: document.querySelector("#closeButton"),
	confirmPanel: document.querySelector("#confirmPanel"),
	confirmMessage: document.querySelector("#confirmPanel .confirm-message"),
	closePanel: document.querySelector("#closePanel"),
	closeTitle: document.querySelector("#closePanel .form-title"),
	closeMessage: document.querySelector("#closePanel .confirm-message"),
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
	saveImageButton: document.querySelector("#saveImageButton"),
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
	documentSelect: document.querySelector("#documentSelect"),
	documentActions: document.querySelector("#documentActions"),
	documentManageButton: document.querySelector("#documentManageButton"),
	invoiceButton: document.querySelector("#invoiceButton"),
	documentPanel: document.querySelector("#documentPanel"),
	documentForm: document.querySelector("#documentForm"),
	documentList: document.querySelector("#documentList"),
	documentStatus: document.querySelector("#documentStatus"),
	documentClose: document.querySelector("#documentClose"),
	documentRow: document.querySelector("#documentRow"),
	documentAddButton: document.querySelector("#documentAddButton"),
	documentImportButton: document.querySelector("#documentImportButton"),
	documentDeleteButton: document.querySelector("#documentDeleteButton"),
	documentUpButton: document.querySelector("#documentUpButton"),
	documentDownButton: document.querySelector("#documentDownButton"),
	versionManageButton: document.querySelector("#versionManageButton"),
	versionTitle: document.querySelector("#versionTitle"),
	versionNameRow: document.querySelector("#versionNameRow"),
	versionActions: document.querySelector("#versionActions"),
	versionPanel: document.querySelector("#versionPanel"),
	versionForm: document.querySelector("#versionForm"),
	versionNotice: document.querySelector("#versionNotice"),
	versionList: document.querySelector("#versionList"),
	versionName: document.querySelector("#versionName"),
	versionStatus: document.querySelector("#versionStatus"),
	versionAdd: document.querySelector("#versionAdd"),
	versionRename: document.querySelector("#versionRename"),
	versionDefault: document.querySelector("#versionDefault"),
	versionDelete: document.querySelector("#versionDelete"),
	versionSelect: document.querySelector("#versionSelect"),
	versionExport: document.querySelector("#versionExport"),
	versionClose: document.querySelector("#versionClose"),
	infoPanel: document.querySelector("#infoPanel"),
	infoForm: document.querySelector("#infoForm"),
	infoDocId: document.querySelector("#infoDocId"),
	infoTitle: document.querySelector("#infoTitle"),
	infoAuthor: document.querySelector("#infoAuthor"),
	infoSubject: document.querySelector("#infoSubject"),
	infoAbstract: document.querySelector("#infoAbstract"),
	infoCreationDate: document.querySelector("#infoCreationDate"),
	infoModDate: document.querySelector("#infoModDate"),
	infoDocUsage: document.querySelector("#infoDocUsage"),
	infoCover: document.querySelector("#infoCover"),
	infoKeywordRows: document.querySelector("#infoKeywordRows"),
	infoKeywordAdd: document.querySelector("#infoKeywordAdd"),
	infoCreator: document.querySelector("#infoCreator"),
	infoCreatorVersion: document.querySelector("#infoCreatorVersion"),
	infoCustomRows: document.querySelector("#infoCustomRows"),
	infoCustomAdd: document.querySelector("#infoCustomAdd"),
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
	importTitle: document.querySelector("#importTitle"),
	importForm: document.querySelector("#importForm"),
	importFile: document.querySelector("#importFile"),
	importRange: document.querySelector("#importRange"),
	importPagesRow: document.querySelector("#importPagesRow"),
	importPages: document.querySelector("#importPages"),
	importPagesLabel: document.querySelector("#importPagesLabel"),
	importDocuments: document.querySelector("#importDocuments"),
	importPosition: document.querySelector("#importPosition"),
	importPositionRow: document.querySelector("#importPositionRow"),
	importOutlinesRow: document.querySelector("#importOutlinesRow"),
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
	batchPagesTitle: document.querySelector("#batchPagesTitle"),
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
	outlinePanelTitle: document.querySelector("#outlinePanelTitle"),
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
	saveAsButton: document.querySelector("#saveAsButton"),
	createPanel: document.querySelector("#createPanel"),
	createTitle: document.querySelector("#createTitle"),
	createMode: document.querySelector("#createMode"),
	createImages: document.querySelector("#createImages"),
	createImageQueue: document.querySelector("#createImageQueue"),
	createImageList: document.querySelector("#createImageList"),
	createImageAdd: document.querySelector("#createImageAdd"),
	createImageOrder: document.querySelector("#createImageOrder"),
	createPortrait: document.querySelector("#createPortrait"),
	createLandscape: document.querySelector("#createLandscape"),
	createSize: document.querySelector("#createSize"),
	createMargin: document.querySelector("#createMargin"),
	createPosition: document.querySelector("#createPosition"),
	createSubmit: document.querySelector("#createSubmit"),
	createForm: document.querySelector("#createForm"),
	createName: document.querySelector("#createName"),
	createWidth: document.querySelector("#createWidth"),
	createHeight: document.querySelector("#createHeight"),
	createStatus: document.querySelector("#createStatus"),
	createCancel: document.querySelector("#createCancel"),
	insertPanel: document.querySelector("#insertPanel"),
	insertTitle: document.querySelector("#insertTitle"),
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
	pageLayoutButton: document.querySelector("#pageLayoutButton"),
	annotationButton: document.querySelector("#annotationButton"),
	imageDPI: document.querySelector("#imageDPI"),
	outputCompression: document.querySelector("#outputCompression"),
	compressionValue: document.querySelector("#compressionValue"),
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
	annotationsTab: document.querySelector("#annotationsTab"),
	annotationsPanel: document.querySelector("#annotationsPanel"),
	annotationsFilter: document.querySelector("#annotationsFilter"),
	annotationsKind: document.querySelector("#annotationsKind"),
	annotationsStatus: document.querySelector("#annotationsStatus"),
	annotationsList: document.querySelector("#annotationsList"),
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
	renderVectorButton: document.querySelector("#renderVectorButton"),
	renderRasterButton: document.querySelector("#renderRasterButton"),
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
	attachmentDialog: document.querySelector("#attachmentDialog"),
	attachmentForm: document.querySelector("#attachmentForm"),
	attachmentTitle: document.querySelector("#attachmentTitle"),
	attachmentFile: document.querySelector("#attachmentFile"),
	attachmentFileRow: document.querySelector("#attachmentFileRow"),
	attachmentName: document.querySelector("#attachmentName"),
	attachmentNameRow: document.querySelector("#attachmentNameRow"),
	attachmentStatus: document.querySelector("#attachmentStatus"),
	attachmentCancel: document.querySelector("#attachmentCancel"),
	attachmentSubmit: document.querySelector("#attachmentSubmit"),
	signaturePanel: document.querySelector("#signaturePanel"),
	structurePanel: document.querySelector("#structurePanel"),
	structureList: document.querySelector("#structureList"),
	structureSummary: document.querySelector("#structureSummary"),
	structureManage: document.querySelector("#structureManage"),
	structureDialog: document.querySelector("#structureDialog"),
	structureForm: document.querySelector("#structureForm"),
	structureEntry: document.querySelector("#structureEntry"),
	structureNodeSelect: document.querySelector("#structureNodeSelect"),
	structureAction: document.querySelector("#structureAction"),
	structureFields: document.querySelector("#structureFields"),
	structureType: document.querySelector("#structureType"),
	structureName: document.querySelector("#structureName"),
	structureNamespace: document.querySelector("#structureNamespace"),
	structureText: document.querySelector("#structureText"),
	structureAttributes: document.querySelector("#structureAttributes"),
	structureBinding: document.querySelector("#structureBinding"),
	structureReferences: document.querySelector("#structureReferences"),
	structureStatus: document.querySelector("#structureStatus"),
	structureSubmit: document.querySelector("#structureSubmit"),
	signatureSummary: document.querySelector("#signatureSummary"),
	signatureClear: document.querySelector("#signatureClear"),
	signatureList: document.querySelector("#signatureList"),
	annotationNote: document.querySelector("#annotationNote"),
	annotationNoteTitle: document.querySelector("#annotationNoteTitle"),
	annotationTool: document.querySelector("#annotationTool"),
	penTool: document.querySelector("#penTool"),
	annotationCreate: document.querySelector("#annotationCreate"),
	annotationCreateTitle: document.querySelector("#annotationCreateTitle"),
	annotationCreateStatus: document.querySelector("#annotationCreateStatus"),
	objectPicker: document.querySelector("#objectPicker"),
	objectPickerTitle: document.querySelector("#objectPickerTitle"),
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

function createPrintDialog(tab) {
	const panel = el.printPanel.cloneNode(true);
	el.printPanel.remove();
	documentDialogs.register(panel, {shared:false});
	return new PrintDialog({
		panel,
		nativeHost,
		show: panel => documentDialogs.openFor(tab, panel),
		download: (bytes, mime, name) => nativeHost ? nativeHost.download(bytes, mime, name) : downloadBytes(bytes, mime, name),
		cancel: tab => {
			for (const [id, request] of wasmRequests) if (request.tab === tab && ["ofdgoPreparePrint", "ofdgoLayoutPrint", "ofdgoPrintSide", "ofdgoPrintPDF"].includes(request.name)) cancelOperation(id);
		},
	});
}

async function openPrintPanel() {
	const tab = documentTab(), content = documentContentKey(state);
	if (!tab.state.doc || !tab.state.ready || document.body.hasAttribute("data-busy") || formDialogOpen()) return;
	const current = () => isForegroundDocument(tab) && content === documentContentKey(state);
	if (!await canvasEditor.commitNudge() || !current() || !await canvasEditor.commitText() || !current()
		|| !await canvasEditor.commitCrop() || !current() || document.body.hasAttribute("data-busy") || formDialogOpen()) return;
	tab.print ||= createPrintDialog(tab);
	tab.print.open({tab, document:documentEntryKey(tab.state.doc.documents?.[tab.state.doc.documentIndex || 0]), call:(name, ...args) => callTabWASM(tab, name, ...args), page:tab.state.pageIndex,
		selection:tab.state.pageSelection.size ? selectedPageIndexes() : [], annotations:tab.state.renderAnnotations,
		name:tab.state.fileName || tab.state.doc.title || "OFDGo"});
}

el.printButton.addEventListener("click", openPrintPanel);

const fontManager = new FontManager({
	host: nativeHost,
	onChange: scheduleFontSync,
	onPermissionChange: updateFontPermissionHint,
});
const fontPicker = new FontPicker(el.textFont, el.textFontToggle, el.textFontList, changeTextFont, () => { selectPicker.close(); loadEditorFonts(); }, groupTextFonts);
const selectPicker = new SelectPicker(() => fontPicker.prepareCommit());
const textFormatButtons = [...document.querySelectorAll("[data-text-format]")];
const textBackground = document.querySelector("#textBackground");
const editorInputs = new Map();
const annotationFields = Object.fromEntries(["CreateForm", "CreateCancel", "Content", "Old", "Font", "Text", "TextRow", "Image", "ImageRow", "Author", "LinkKind", "Address", "AddressRow", "Target", "TargetRow", "X", "Y", "Width", "Height", "Angle", "Opacity", "Tile", "Placement", "PlacementLabel", "Pick", "Scope", "Pages"].map(key => [key, document.querySelector(`#annotation${key}`)]));
el.editorTools.addEventListener("scroll", () => fontPicker.position());

const canvasEditor = new CanvasEditor(el.viewerPanel, {
	textControls: [el.appearanceButton, el.appearancePanel, el.textAlign, el.textFont, el.textFontToggle, el.textFontAdd, el.textFontList, el.textSize, el.textColor, el.textLineHeight, el.textSpacing, el.textWrap, el.paragraphButton, el.paragraphPanel, textBackground, ...textFormatButtons, el.undoButton, el.redoButton, el.saveButton, el.saveAsButton, el.fontInput, document.querySelector("#textReflow")],
	keepTextFocus: () => el.appearancePanel.open || fontPicker.open || el.confirmPanel.open || el.closePanel.open || el.paragraphPanel.open || canvasEditor.input?.fontDialog,
	prepareTextCommit: flushEditorInputs,
	onTextFormat: changeRangeFormat,
	onRichInput: ensureRichInput,
	textClipboardScope: () => `${documentTab().id}:${documentContentKey(state)}:${state.editorInfo?.revision ?? ""}`,
	getTextClipboard: () => workspace.textClipboard,
	setTextClipboard: clipboard => { workspace.textClipboard = clipboard; },
	scrollBy: (x, y) => { el.viewerPanel.scrollLeft += x; pageView.scrollViewerBy(y); },
	busy: () => document.body.hasAttribute("data-busy"),
	rotation: () => state.rotation,
	canInsert: (index) => pageCan("insert", index),
	onPage: (index) => {
		if (index !== state.pageIndex) pageView.setCurrentPage(index);
	},
	onSelect: (item, previous) => {
		if (item && item.index !== state.pageIndex) pageView.setCurrentPage(item.index);
		updateObjectControls(item);
		const reason = objectEditReason(item), previousReason = objectEditReason(previous);
		if (state.editing && state.doc && !document.body.hasAttribute("data-busy")
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
	onTextWidth: (item, box) => item.richText
		? saveRichText(item, { ...item.richText, layout: { ...item.richText.layout, wrap: true } }, [], { ...item, frameWidth: box.width, frameOffset: box.x - item.x })
		: changeDocument("ofdgoLayoutText", item, box.x - item.x, box.width, true, item.align || "", item.paragraphHeight || 0, item.letterSpacing || 0, ...textIndents(item)),
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
		const item = { index: items[0].index, id: items.map(item => item.id), items };
		if (points) return changeDocument("ofdgoEraseObjectsPath", item, points);
		return box ? changeDocument("ofdgoEraseObjects", item, box.x, box.y, box.width, box.height)
			: changeDocument("ofdgoDeleteObjects", item);
	},
	onEdit: editCanvasObject,
	onPick: openObjectPicker,
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
	onPreviewText: (item, value, font, size, reflow, color = null) => item.selectionIDs
		? callWASM("ofdgoPreviewTextSelection", item.index, item.selectionIDs, value, font || null, size, color, !!reflow)
		: callWASM("ofdgoPreviewText", item.index, item.id, value, font || null, size, !!reflow),
	onCommitRichText: saveRichText,
	onPreviewRichText: validateRichText,
	onTextError: (message, previous) => {
		if (message) setStatus(message);
		else if (el.statusText.textContent === previous) setStatus(state.doc ? pageStatus(state.pageIndex, state.doc.pageCount) : STATUS.ready);
	},
	onCommitText: (item, value, fontData, color = null, reflow = false) => item.selectionIDs
		? changeDocument("ofdgoUpdateTextSelection", item, value, fontData || null, canvasEditor.input?.style?.size ?? null, color, reflow)
		: item.draft
		? changeDocument("ofdgoInsertText", null, item.index, value, fontData || item.fontData, item.x, item.y, item.size, item.color, item.width, item.wrap, item.align, item.paragraphHeight, item.letterSpacing, ...textIndents(item))
		: changeDocument("ofdgoUpdateText", item, value, fontData || null, item.size, color, reflow),
	onTextChange: () => {
		const editing = canvasEditor.input;
		updatePendingChanges();
		if (editing?.rich || editing?.source) updateObjectControls(canvasEditor.selected);
		if (editing?.source) updateTextFonts(canvasEditor.selected, true);
		if (!editing) {
			updateObjectControls(canvasEditor.selected);
			updateTextFonts(canvasEditor.selected, true);
			syncSelection();
			if (state.fontRenderPending) window.setTimeout(refreshPendingFonts, 0);
		}
	},
});

const pageView = createPageView({
	get state() { return state; },
	get wasmWorker() { return wasmWorker; },
	get activeTab() { return workspace.active; },
	get tabs() { return workspace.tabs; },
	callWASM, canvasEditor, clearRegionHighlights, documentTab, el,
	enablePageDrag, formatSize, isDocumentSelection, isForegroundDocument, mountAnnotationNotes,
	mountEditorObjects, mountPageLinks, nextFrame, pageCan, pageStatus,
	releaseMediaPlayers, renderSearchHighlights, loadEditorMembers, scheduleOpenActions, selectThumbnailPage,
	setBusy, setProgress, setStatus, showError, syncPageSelection,
	updateControls, updateEditorTools, wasmRequests,
});

function updatePendingChanges() {
	setDirty(state.packageDirty || state.encryptionDirty || Boolean(state.editorInfo) && (state.editorInfo.revision !== state.savedRevision
		|| canvasEditor.textChanged() || canvasEditor.cropChanged() || canvasEditor.nudgeChanged()));
}

el.documentSelect.addEventListener("change", () => switchDocument(Number(el.documentSelect.value)));

el.editorTools.addEventListener("pointerdown", (event) => {
	if ((canvasEditor.input || canvasEditor.nudge) && event.target.closest("button")) event.preventDefault();
});

function editorClick(button, action, keepText = false) {
	button.addEventListener("click", async () => {
		if ((canvasEditor.nudge || canvasEditor.nudgeCommit) && !await canvasEditor.commitNudge()) return;
		if (!keepText && canvasEditor.input && !await canvasEditor.commitText()) return;
		if (canvasEditor.crop && !await canvasEditor.commitCrop()) return;
		return action();
	});
}

editorClick(el.selectObjectButton, () => {
	if (document.body.hasAttribute("data-busy")) {
		return;
	}
	state.selectObjects = !state.selectObjects || state.panMode || Boolean(canvasEditor.tool);
	canvasEditor.setTool("");
	if (state.selectObjects && state.panMode) {
		pageView.togglePan();
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
	el.multiSelectButton.setAttribute("data-pressed", String(canvasEditor.multiple));
});
editorClick(el.editObjectButton, () => {
	if (canvasEditor.selected) {
		editCanvasObject(canvasEditor.selected);
	}
});
bindEditorInput(el.textSize, () => changeTextStyle(false));
bindEditorInput(el.textColor, () => changeTextStyle(true));
for (const button of textFormatButtons) button.addEventListener("click", () => changeRangeFormat(button.dataset.textFormat));
bindEditorInput(textBackground, value => changeRangeFormat("background", value));
document.addEventListener("pointerdown", flushEditorInputs, true);
editorClick(el.textWrap, () => changeParagraph(!currentTextStyle().wrap), true);
el.textAlign.addEventListener("change", () => changeTextAlignment());
bindEditorInput(el.textLineHeight, () => changeParagraph(undefined, "lineHeight"));
editorClick(el.paragraphButton, () => {
	const item = currentTextStyle();
	for (const key of ["leftIndent", "rightIndent", "firstLineIndent"]) {
		const mixed = Boolean(canvasEditor.input?.rich || item.richText || item.items) && item[key] === undefined;
		el[key].required = !mixed;
		el[key].placeholder = mixed ? "混合" : "";
		el[key].value = mixed ? "" : item[key] || 0;
	}
	el.paragraphStatus.textContent = "";
	el.paragraphPanel.showModal();
}, true);
el.paragraphCancel.addEventListener("click", () => el.paragraphPanel.close());
el.paragraphPanel.addEventListener("close", () => canvasEditor.input?.input.focus({ preventScroll: true }));
editorClick(el.documentAddButton, () => openCreatePanel(false, true));
editorClick(el.documentImportButton, () => openImportPanel(true));
editorClick(el.documentManageButton, openDocumentManagement);
resourceElements.ManageButton.addEventListener("click", openResourceManagement);
resourceElements.Close.addEventListener("click", () => resourceElements.Panel.close());
resourceElements.Form.addEventListener("submit", event => event.preventDefault());
resourceElements.Panel.addEventListener("close", () => {
	rememberResourceView();
	resourceManager.sequence++;
	if (resourceManager.requestID) cancelOperation(resourceManager.requestID);
	releaseResourcePreview();
});
resourceElements.List.closest(".resource-list-container").addEventListener("scroll", renderResourceWindow, {passive: true});
new ResizeObserver(renderResourceWindow).observe(resourceElements.List.closest(".resource-list-container"));
resourceElements.List.addEventListener("keydown", event => {
	if (resourceManager.busy) return;
	const items = resourceManager.filteredItems, index = items.indexOf(resourceManager.selected);
	const target = { ArrowDown: Math.min(items.length - 1, index + 1), ArrowUp: Math.max(0, index - 1), Home: 0, End: items.length - 1 }[event.key];
	if (target == null || !items[target]) return;
	event.preventDefault();
	const sequence = state.openSeq;
	chooseManagedResource(items[target]).then(() => {
		if (!resourceElements.Panel.open || sequence !== state.openSeq || items !== resourceManager.filteredItems || resourceManager.selected !== items[target]) return;
		const container = resourceElements.List.closest(".resource-list-container");
		const height = parseFloat(getComputedStyle(resourceElements.List).getPropertyValue("--resource-row-height"));
		const viewport = container.clientHeight - container.querySelector(".resource-heading").offsetHeight;
		const top = target * height;
		if (top < container.scrollTop) container.scrollTop = top;
		else if (top + height > container.scrollTop + viewport) container.scrollTop = top + height - viewport;
		renderResourceWindow();
		resourceElements.List.querySelector(`[data-index="${target}"]`)?.focus({preventScroll: true});
	});
});
resourceElements.Reference.addEventListener("change", () => resourceTask(syncResourceReference));
resourceElements.ReferenceGroup.addEventListener("change", () => resourceTask(() => loadManagedReferences(Number(resourceElements.ReferenceGroup.value) * 100)));
resourceElements.Page.addEventListener("change", () => resourceTask(() => loadResourceTargets("")));
resourceElements.Object.addEventListener("change", updateResourceControls);
resourceElements.Pick.addEventListener("click", () => resourceTask(beginResourcePick));
resourceElements.Preview.addEventListener("click", () => resourceTask(previewManagedResource));
resourceElements.Export.addEventListener("click", () => {
	const item = resourceManager.selected;
	if (item) downloadResource({ id: JSON.stringify([item.kind, item.id]), fileName: resourceFileName(item) }, "ofdgoExportResource", "资源");
});
resourceElements.Locate.addEventListener("click", () => resourceTask(locateManagedResource));
resourceElements.Add.addEventListener("click", () => {
	if (!state.editing) return;
	const kind = resourceManager.kind;
	if (kind === "Font") openFontFile(el.fontInput);
	else if (kind === "Image") {
		const tab = documentTab(), scope = documentPanelScope(tab.state);
		openInsertPanel();
		if (documentDialogs) documentDialogs.request(el.insertPanel, tab).then(() => {
			if (isForegroundDocument(tab) && scope === documentPanelScope(tab.state) && resourceElements.Panel.open && tab.resources.kind === "Image") resourceElements.Add.focus({preventScroll: true});
		});
	}
	else if (kind === "Attachment") openAttachmentDialog("add");
	else if (kind === "ColorSpace" || kind === "DrawParam") openResourceSettings(kind, "");
	else chooseResourceFile("add");
});
resourceElements.Replace.addEventListener("click", async () => {
	const item = resourceManager.selected;
	if (!state.editing || !item) return;
	if (item.kind === "ColorSpace" || item.kind === "DrawParam") {
		await openResourceSettings(item.kind, item.id);
	} else if (item.kind === "Font") {
		const font = state.doc.fonts?.find(font => font.id === item.id) || {id: item.id, fontName: item.name, embedded: Boolean(item.file)};
		await openFontBinding(font);
		if (!fontBindingElements.Panel.open) await resourceTask(loadManagedResources);
	} else chooseResourceFile("replace");
});
resourceElements.Rename.addEventListener("click", () => {
	const item = resourceManager.selected;
	if (item?.kind === "Attachment") openAttachmentDialog("rename", item);
});
resourceElements.Delete.addEventListener("click", () => resourceTask(async () => {
	const item = resourceManager.selected;
	if (!item) return;
	const message = item.kind === "Attachment" ? `删除附件“${item.name}”？相关附件链接将一并移除。` : `删除资源“${item.name}”？`;
	if (await requestConfirmation(message)) await changeManagedResource("delete", item);
}));
for (const [button, action] of [[resourceElements.Bind, "bind"], [resourceElements.Unbind, "unbind"]]) {
	button.addEventListener("click", () => resourceTask(() => changeManagedResource(action, resourceManager.selected)));
}
resourceElements.File.addEventListener("change", () => resourceTask(async () => {
	const request = resourceManager.fileAction, file = resourceElements.File.files[0];
	if (!file || !request || request.sequence !== state.openSeq || !state.editing) return;
	await changeManagedResource(request.action, request.item, file);
}));
for (const dialog of [fontBindingElements.Panel, el.attachmentDialog]) {
	dialog.addEventListener("close", () => { if (resourceElements.Panel.open) resourceTask(loadManagedResources); });
}
editorClick(el.versionManageButton, openVersionManagement);
el.versionClose.addEventListener("click", () => el.versionPanel.close());
el.versionPanel.addEventListener("cancel", event => { if (el.versionForm.inert) event.preventDefault(); });
el.versionPanel.addEventListener("close", () => {
	if (!el.versionPanel.open) {
		state.versionManagement = null;
		el.versionList.replaceChildren();
		el.versionName.value = el.versionStatus.textContent = "";
	}
});
el.versionForm.addEventListener("submit", event => event.preventDefault());
el.versionName.addEventListener("input", updateVersionControls);
for (const [button, action] of [[el.versionAdd, "add"], [el.versionRename, "rename"], [el.versionDefault, "default"], [el.versionDelete, "delete"], [el.versionSelect, "select"]]) {
	button.addEventListener("click", () => changeVersion(action));
}
el.versionList.addEventListener("keydown", event => {
	const manager = state.versionManagement;
	if (!manager?.entries || manager.loading || document.body.hasAttribute("data-busy")) return;
	let index = manager.entries.findIndex(entry => entry.id === manager.id);
	if (event.key === "ArrowUp") index--;
	else if (event.key === "ArrowDown") index++;
	else if (event.key === "Home") index = 0;
	else if (event.key === "End") index = manager.entries.length - 1;
	else return;
	event.preventDefault();
	selectManagedVersion(manager.entries[Math.max(0, Math.min(index, manager.entries.length - 1))].id, true);
});
document.querySelector("#confirmCancel").addEventListener("click", () => el.confirmPanel.close("cancel"));
el.documentClose.addEventListener("click", () => el.documentPanel.close());
el.documentPanel.addEventListener("cancel", event => { if (el.documentForm.inert) event.preventDefault(); });
el.documentPanel.addEventListener("close", () => {
	if (!el.documentPanel.open) {
		state.documentManagement = null;
		el.documentList.replaceChildren();
		el.documentStatus.textContent = "";
	}
});
el.documentList.addEventListener("keydown", event => {
	if (!state.documentManagement || !["ArrowUp", "ArrowDown", "Home", "End"].includes(event.key)) return;
	event.preventDefault();
	const count = state.doc.documents.length, index = state.documentManagement.index;
	selectManagedDocument(event.key === "Home" ? 0 : event.key === "End" ? count - 1
		: Math.max(0, Math.min(count - 1, index + (event.key === "ArrowUp" ? -1 : 1))), true);
});
editorClick(el.documentDeleteButton, async () => {
	const index = managedDocumentIndex(), entry = state.doc.documents[index];
	if (await requestConfirmation(`删除文档“${entry.title || "未命名"}”？`)) return changeDocument("ofdgoChangeDocument", null, "delete", index);
});
editorClick(el.documentUpButton, () => {
	const index = managedDocumentIndex();
	return changeDocument("ofdgoChangeDocument", null, "move", index, index - 1);
});
editorClick(el.documentDownButton, () => {
	const index = managedDocumentIndex();
	return changeDocument("ofdgoChangeDocument", null, "move", index, index + 1);
});
editorClick(el.infoButton, () => {
	state.infoDocumentIndex = managedDocumentIndex();
	const info = state.documentManagement?.entries?.[state.infoDocumentIndex] || state.doc;
	for (const key of INFO_FIELDS) setInfoInput(el[`info${key[0].toUpperCase()}${key.slice(1)}`], info[key] || "");
	el.infoKeywordRows.replaceChildren();
	for (const keyword of info.keywords || []) addInfoRow(el.infoKeywordRows, [keyword]);
	el.infoCustomRows.replaceChildren();
	for (const [index, field] of (info.customData || []).entries()) addInfoRow(el.infoCustomRows, [field.Name, field.Value], index);
	el.infoStatus.textContent = "";
	el.infoPanel.showModal();
});
el.infoCancel.addEventListener("click", () => el.infoPanel.close());
el.infoPanel.addEventListener("cancel", event => { if (el.infoForm.inert) event.preventDefault(); });
el.infoPanel.addEventListener("close", () => { if (!el.infoPanel.open) state.infoDocumentIndex = null; });
el.infoKeywordAdd.addEventListener("click", () => addInfoRow(el.infoKeywordRows, [""]));
el.infoCustomAdd.addEventListener("click", () => addInfoRow(el.infoCustomRows, ["", ""]));
el.infoForm.addEventListener("submit", async event => {
	event.preventDefault();
	const info = Object.fromEntries(INFO_FIELDS.map(key => [key, infoInputValue(el[`info${key[0].toUpperCase()}${key.slice(1)}`])]));
	info.keywords = [...el.infoKeywordRows.children].map(row => infoInputValue(row.children[0]));
	info.customData = [...el.infoCustomRows.children].map(row => ({Name: infoInputValue(row.children[0]), Value: infoInputValue(row.children[1]), Source: row.sourceIndex}));
	if (await changeDocument("ofdgoUpdateInfo", null, JSON.stringify(info), state.infoDocumentIndex)) el.infoPanel.close();
});

function setInfoInput(input, value) {
	input.defaultValue = value;
	input.value = value;
	input.dataset.initialValue = input.value;
}

function infoInputValue(input) {
	return input.value === input.dataset.initialValue ? input.defaultValue : input.value;
}

function addInfoRow(container, values, sourceIndex) {
	const row = document.createElement("div");
	row.sourceIndex = sourceIndex;
	row.className = values.length === 1 ? "info-keyword-row" : "info-custom-row";
	for (const [index, value] of values.entries()) {
		const input = document.createElement("textarea");
		input.rows = 1;
		setInfoInput(input, value);
		input.placeholder = values.length === 1 ? "关键词" : index === 0 ? "属性名" : "属性值";
		row.append(input);
	}
	const remove = document.createElement("button");
	remove.type = "button";
	remove.className = "icon-button remove-button";
	remove.title = values.length === 1 ? "删除词条" : "删除属性";
	remove.addEventListener("click", () => row.remove());
	row.append(remove);
	container.append(row);
}
el.annotationClose.addEventListener("click", () => el.annotationNote.close());
el.annotationNote.addEventListener("close", () => { state.annotationEdit = null; });
el.objectPicker.addEventListener("close", () => { if (!el.objectPicker.open) el.objectPickerList.replaceChildren(); });
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
	if (selected?.parent?.startsWith("annotation:")) {
		const id = selected.parent.split("/")[0];
		selected = { ...state.pageCache.get(selected.index)?.objects.find(item => item.id === id), index: selected.index, id, type: "Annotation" };
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
	if (!entry || !annotationFields.CreateForm.reportValidity() || document.body.hasAttribute("data-busy")) return;
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
	const fromSummary = state.annotationEdit.fromSummary;
	if (await changeAnnotations(state.annotationEdit, "update", el.annotationRemark.value, el.annotationCreator.value)) {
		el.annotationNote.close();
		if (fromSummary) showNavigation(el.annotationsTab);
		canvasEditor.focus();
	}
});
el.paragraphForm.addEventListener("submit", async event => {
	event.preventDefault();
	if (document.body.hasAttribute("data-busy") || !el.paragraphForm.reportValidity()) return;
	const item = currentTextStyle(), tab = documentTab();
	const indents = Object.fromEntries(["leftIndent", "rightIndent", "firstLineIndent"].filter(key => el[key].value !== "").map(key => [key, Number(el[key].value)]));
	try {
		if (canvasEditor.input) {
			if (!await changeInputParagraph(indents)) return;
		} else if (item === state.textDefaults) {
			Object.assign(item, indents);
		} else {
			const saved = await saveTextParagraphs(item, indents);
			if (!saved) return;
		}
		if (isForegroundDocument(tab)) el.paragraphPanel.close();
	} catch (error) {
		if (isForegroundDocument(tab) && el.paragraphPanel.open) el.paragraphStatus.textContent = error.message;
	}
});
bindEditorInput(el.textSpacing, () => changeParagraph(undefined, "letterSpacing"));
el.shapeWidth.addEventListener("focus", () => { el.shapeWidth.defaultValue = el.shapeWidth.value; });
for (const input of [el.textSize, el.shapeWidth, el.textLineHeight, el.textSpacing]) {
	input.addEventListener("keydown", (event) => {
		if (event.isComposing) return;
		if (event.key === "Enter" || event.key === "Escape") {
			event.preventDefault();
			event.stopPropagation();
			if (event.repeat) return;
			if (event.key === "Escape") {
				const control = editorInputs.get(input);
				control.pending = null;
				control.handled = true;
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
	if (axis && !el.objectDistribute.disabled && state.objectDistribution?.axes?.includes(axis)) {
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
	if (document.body.hasAttribute("data-busy")) return;
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
	if (document.body.hasAttribute("data-busy")) return;
	if (canvasEditor.crop) {
		canvasEditor.closeCrop();
		return;
	}
	const item = canvasEditor.selected;
	if (item?.scoped) return changeDocument("ofdgoResetCompositeCrop", item);
	if (canEditObject(item, "resetCrop")) return changeDocument("ofdgoResetImageCrop", item);
	if (item?.imageBounds) return canvasEditor.options.onCrop(item, item.imageBounds);
});
for (const [button, direction, command] of [[el.undoButton, -1, "ofdgoUndo"], [el.redoButton, 1, "ofdgoRedo"]]) {
	button.addEventListener("click", async () => {
		if (canvasEditor.input?.rich || canvasEditor.input?.source) { canvasEditor.textUndo(direction); return; }
		if (!await canvasEditor.commitNudge() || !await canvasEditor.commitText() || !await canvasEditor.commitCrop()) return;
		await changeDocument(command);
	});
}
for (const [button, tool] of [[el.drawLineButton, "line"], [el.drawRectangleButton, "rectangle"], [el.drawEllipseButton, "ellipse"]]) {
	editorClick(button, () => toggleDrawingTool(tool));
}
editorClick(el.eraseButton, () => toggleDrawingTool(el.eraseMode.value));
el.eraseMode.addEventListener("change", () => {
	if (canvasEditor.tool.startsWith("erase-")) {
		canvasEditor.setTool(el.eraseMode.value);
		canvasEditor.focus();
	}
});
for (const input of [el.shapeFill, el.shapeStroke]) {
	input.addEventListener("change", changeShapeStyle);
}
for (const input of [el.shapeFillColor, el.shapeStrokeColor, el.shapeWidth]) bindEditorInput(input, () => changeShapeStyle({ target: input }));
el.addPageButton.addEventListener("change", async () => {
	const action = el.addPageButton.value;
	el.addPageButton.value = "";
	if (canvasEditor.input && !await canvasEditor.commitText()) return;
	if (canvasEditor.crop && !await canvasEditor.commitCrop()) return;
	if (action === "import") { openImportPanel(); return; }
	if (action === "images") { openCreatePanel(true); return; }
	const page = pageView.currentPageInfo();
	return changeDocument("ofdgoChangePage", null, "add", state.pageIndex, page.width, page.height);
});
el.importCancel.addEventListener("click", () => state.importing ? cancelExport() : el.importPanel.close());
el.importPanel.addEventListener("cancel", event => {
	if (state.importing) { event.preventDefault(); cancelExport(); }
});
el.importPanel.addEventListener("close", () => {
	state.importPageCount = 0;
	state.importDocumentCount = 0;
	state.importDocumentMode = false;
	state.importEncrypted = false;
	el.importForm.reset();
	if (state.ready) callWASM("ofdgoLoadImport", null).catch(err => showError(err, false));
});
el.importFile.addEventListener("change", loadImportFile);
el.importRange.addEventListener("change", () => {
	updateImportControls();
	if (!el.importPages.disabled) el.importPages.focus();
});
el.importDocuments.addEventListener("change", () => updateImportControls());
el.importForm.addEventListener("submit", async event => {
	event.preventDefault();
	if (document.body.hasAttribute("data-busy") || !(state.importDocumentMode ? state.importDocumentCount : state.importPageCount) || !el.importForm.reportValidity()) return;
	const position = state.importDocumentMode ? managedDocumentIndex() + 1 : { before:state.pageIndex, after:state.pageIndex+1, first:0, last:state.doc.pageCount }[el.importPosition.value];
	const options = [];
	if (state.importEncrypted) {
		if (!await requestConfirmation(state.doc.encryption?.encrypted ? "按目标文档的加密策略导入，是否继续？" : "目标未加密，导入内容将按明文保存，是否继续？")) return;
		options.push(true);
	}
	const range = el.importRange.value === "custom" ? state.importDocumentMode ? selectedImportDocuments() : el.importPages.value : "";
	if (state.importDocumentMode && el.importRange.value === "custom" && !range) return;
	const saved = state.importDocumentMode
		? await changeDocument("ofdgoImportDocuments", null, range, position, state.importEncrypted)
		: await changeDocument("ofdgoImportPages", null, range, position, el.importOutlines.checked, ...options);
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
	el.batchPagesTitle.textContent = "复制页面";
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
	el.batchPagesTitle.textContent = `${el.batchPagesSubmit.textContent}页面`;
	el.batchPagePositionRow.hidden = el.batchPageAction.value !== "move";
});
el.batchPagesForm.addEventListener("submit", async event => {
	event.preventDefault();
	if (document.body.hasAttribute("data-busy") || !el.batchPagesForm.reportValidity()) return;
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
		if (ungroup && canvasEditor.items().some(member => member.richText)) return;
		if (await changeDocument("ofdgoGroupObjects", { ...item, id: canvasEditor.items().map(member => member.id) }, ungroup)) el.objectStylePanel.close();
	});
}
el.arrowTool.addEventListener("change", async () => {
	const tab = documentTab(), content = documentContentKey(state);
	const current = () => isForegroundDocument(tab) && state.editing && content === documentContentKey(state);
	const tool = el.arrowTool.value;
	el.arrowTool.value = "";
	if (await canvasEditor.commitText() && current() && await canvasEditor.commitCrop() && current()) {
		const item = canvasEditor.selected;
		if (lineShape(item?.shape)) {
			const box = item.geometry;
			await changeDocument("ofdgoReshapeLine", item, box.x, box.y, box.width, box.height, tool);
			return;
		}
		pageView.setPan(false);
		state.selectObjects = true;
		canvasEditor.setTool(tool);
		updateControls();
		canvasEditor.focus();
	}
});
el.copyStyleButton.addEventListener("click", async () => {
	const tab = documentTab(), state = tab.state;
	const item = canvasEditor.selected, seq = state.openSeq;
	if (!canCopyStyle(item)) return;
	setBusy(true, "", 0, "", tab);
	try {
		await callTabWASM(tab, "ofdgoCaptureStyle", item.index, item.id, objectParent(item));
		if (!tab.closed) state.styleClipboard = item.type;
		if (isForegroundDocument(tab) && seq === state.openSeq) el.objectStylePanel.close();
	} catch (err) { if (isForegroundDocument(tab) && seq === state.openSeq) el.objectStyleStatus.textContent = err.message; }
	finally { setBusy(false, "", 0, "", tab); }
});
el.pasteStyleButton.addEventListener("click", async () => {
	const item = canvasEditor.selected;
	if (canvasEditor.items().some(member => member.richText)) return;
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
document.querySelector("#textReflow").addEventListener("click", reflowCanvasText);
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
el.structureManage.addEventListener("click", openStructureDialog);
el.signatureClear.addEventListener("click", () => changeDocument("ofdgoChangeSignature", null, "clear", ""));
document.querySelector("#structureCancel").addEventListener("click", () => el.structureDialog.close());
el.structureEntry.addEventListener("change", refreshStructureNodes);
el.structureNodeSelect.addEventListener("change", refreshStructureFields);
el.structureAction.addEventListener("change", refreshStructureFields);
document.querySelector("#structureAttributeAdd").addEventListener("click", () => addStructureAttribute());
document.querySelector("#structureReferenceAdd").addEventListener("click", () => addStructureReference());
document.querySelector("#structureSelection").addEventListener("click", () => {
	const items = canvasEditor.items();
	if (!items.length) { el.structureStatus.textContent = "尚未选择图元"; return; }
	const page = state.doc.pages[items[0].index];
	addStructureReference({ pageId: page.id, objectIds: items.map(item => item.id) });
});
el.structureForm.addEventListener("submit", async event => {
	event.preventDefault();
	if (!state.editing || document.body.hasAttribute("data-busy")) return;
	const index = Number(el.structureEntry.value), path = JSON.parse(el.structureNodeSelect.value || "[]");
	if (state.doc.structures?.[index]?.internal) return;
	const action = index < 0 ? "create" : el.structureAction.value;
	let original = state.doc.structures?.[index]?.root;
	for (const part of path) original = original?.children?.[part];
	const text = action === "update" && original?.text?.replace(/\r\n?/g, "\n") === el.structureText.value
		? original.text : el.structureText.value;
	const node = { name: el.structureName.value, namespace: el.structureNamespace.value, text,
		attributes: [...el.structureAttributes.children].map(row => ({ name: row.querySelector('[name="name"]').value, namespace: row.querySelector('[name="namespace"]').value, value: row.querySelector('[name="value"]').value })) };
	const references = [...el.structureReferences.children].map(row => ({ pageId: row.querySelector('[name="page"]').value,
		objectIds: row.querySelector('[name="ids"]').value.split(/[,\s]+/u).filter(Boolean) })).filter(ref => ref.objectIds.length);
	el.structureStatus.textContent = "";
	const saved = await changeDocument("ofdgoChangeStructure", null, JSON.stringify({ action, index, path, typeId: el.structureType.value, node, references }));
	if (saved) el.structureDialog.close();
});
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
el.aboutButton.addEventListener("click", openAboutPanel);
el.associationSection.addEventListener("click", changeFileAssociation);
let aboutBackdrop = false;
el.aboutPanel.addEventListener("pointerdown", event => { aboutBackdrop = event.target === el.aboutPanel; });
el.aboutPanel.addEventListener("click", event => {
	if (aboutBackdrop && event.target === el.aboutPanel) el.aboutPanel.close();
	aboutBackdrop = false;
});
el.ofdButton.addEventListener("click", openOFDFile);
el.closeButton.addEventListener("pointerdown", event => event.preventDefault());
el.closeButton.addEventListener("click", () => closeDocument().catch(error => showError(error, false)));
batchElements.Button.addEventListener("click", () => { updateBatchControls(); batchElements.Panel.showModal(); });
batchElements.Add.addEventListener("click", () => batchElements.Input.click());
batchElements.Input.addEventListener("change", () => { addBatchFiles(batchElements.Input.files); batchElements.Input.value = ""; });
batchElements.Clear.addEventListener("click", () => { batch.items = []; renderBatchList(); batchElements.Status.textContent = ""; batchElements.Progress.value = 0; });
batchElements.Format.addEventListener("change", updateBatchControls);
batchElements.DPI.addEventListener("change", updateBatchControls);
batchElements.Close.addEventListener("click", () => batchElements.Panel.close());
batchElements.Cancel.addEventListener("click", cancelBatch);
batchElements.Form.addEventListener("submit", event => { event.preventDefault(); runBatch(); });
batchElements.Panel.addEventListener("dragover", event => { event.preventDefault(); event.stopPropagation(); event.dataTransfer.dropEffect = batch.running ? "none" : "copy"; });
batchElements.Panel.addEventListener("drop", event => { event.preventDefault(); event.stopPropagation(); if (!batch.running) addBatchFiles(event.dataTransfer.files); });
batchElements.Destination.querySelector('[value="directory"]').disabled = !nativeHost && !window.showDirectoryPicker;
batchElements.Destination.value = nativeHost || window.showDirectoryPicker ? "directory" : "archive";
fontBindingElements.Cancel.addEventListener("click", () => fontBindingElements.Panel.close());
fontBindingElements.Panel.addEventListener("close", () => { fontBinding.font = null; fontBinding.choices = []; fontBinding.sequence++; fontBindingElements.File.value = ""; });
fontBindingElements.Source.addEventListener("change", () => {
	if (fontBindingElements.Source.value === "file") {
		fontBindingElements.File.ownerTab = documentTab();
		fontBindingElements.Source.value = "";
		fontBindingElements.File.click();
	}
});
fontBindingElements.File.addEventListener("change", async () => {
	const tab = fontBindingElements.File.ownerTab || documentTab(), wasmSeq = tab.state.wasmSeq;
	delete fontBindingElements.File.ownerTab;
	const file = fontBindingElements.File.files[0];
	const binding = isForegroundDocument(tab) ? fontBinding : tab.fontBinding;
	if (!file || !binding || tab.closed) return;
	const sequence = ++binding.sequence;
	const current = () => !tab.closed && wasmSeq === tab.state.wasmSeq && sequence === (isForegroundDocument(tab) ? fontBinding : tab.fontBinding)?.sequence;
	const update = action => {
		if (!current()) return;
		if (isForegroundDocument(tab)) action();
		else documentDialogs?.updateFor(tab, fontBindingElements.Panel, () => { if (current() && isForegroundDocument(tab)) action(); });
	};
	if (isForegroundDocument(tab)) fontBindingElements.File.value = "";
	try {
		const data = new Uint8Array(await file.arrayBuffer());
		if (!current()) return;
		const faces = await callTabWASM(tab, "ofdgoFontFaces", data);
		update(() => {
			for (const face of faces) {
				const value = String(fontBinding.choices.length);
				fontBinding.choices.push({ data, index: face.index });
				fontBindingElements.Source.add(new Option(face.fullName || face.family || file.name, value));
				if (faces.length === 1) fontBindingElements.Source.value = value;
			}
			fontBindingElements.Status.textContent = faces.length > 1 ? "请选择字体" : "";
		});
	} catch (err) { update(() => { fontBindingElements.Status.textContent = err.message; }); }
});
fontBindingElements.Form.addEventListener("submit", async event => {
	event.preventDefault();
	const tab = documentTab(), wasmSeq = tab.state.wasmSeq;
	const font = fontBinding.font, sequence = fontBinding.sequence;
	const current = () => !tab.closed && wasmSeq === tab.state.wasmSeq && sequence === (isForegroundDocument(tab) ? fontBinding : tab.fontBinding)?.sequence;
	const update = action => {
		if (tab.closed) return;
		if (isForegroundDocument(tab)) action();
		else documentDialogs?.updateFor(tab, fontBindingElements.Panel, () => { if (!tab.closed && isForegroundDocument(tab)) action(); });
	};
	if (!font || document.body.hasAttribute("data-busy") || !fontBindingElements.Form.reportValidity()) return;
	const choice = fontBinding.choices[Number(fontBindingElements.Source.value)];
	fontBindingElements.Form.inert = true;
	try {
		let data = null;
		if (!font.embedded) {
			if (!choice) throw new Error("请选择字体");
			data = choice.data ? (await callTabWASM(tab, "ofdgoFontFace", choice.data, choice.index || 0)).bytes : await readTextFont(choice, tab);
		}
		if (!current() || !isForegroundDocument(tab)) return;
		if (await changeDocument("ofdgoChangeFontBinding", null, font.id, font.embedded ? 2 : 1, data)) {
			update(() => { if (current()) fontBindingElements.Panel.close(); });
		}
	} catch (err) { update(() => { if (current()) fontBindingElements.Status.textContent = fontBindingError(err.message); }); }
	finally { update(() => { if (current() || !fontBindingElements.Panel.open) fontBindingElements.Form.inert = false; }); }
});
el.newButton.addEventListener("click", openCreatePanel);
el.editButton.addEventListener("click", toggleEditor);
el.editButton.addEventListener("pointerdown", (event) => {
	if (canvasEditor.input) event.preventDefault();
});
el.createCancel.addEventListener("click", () => state.imageImporting ? cancelExport() : el.createPanel.close());
el.createPanel.addEventListener("cancel", event => {
	if (state.imageImporting) { event.preventDefault(); cancelExport(); }
});
el.createPanel.addEventListener("close", () => { releaseCreateImages(documentTab()); el.createImages.value = ""; el.createImageList.replaceChildren(); });
el.createMode.addEventListener("change", () => {
	el.createSize.value = el.createMode.value === "images" ? "image" : "a4";
	applyCreatePaper();
});
el.createSize.addEventListener("change", applyCreatePaper);
for (const input of [el.createWidth, el.createHeight]) input.addEventListener("input", () => updateCreateDirection(true));
el.createMargin.addEventListener("input", updateCreateImagePreviews);
for (const radio of [el.createPortrait, el.createLandscape]) radio.addEventListener("change", () => {
	[el.createWidth.value, el.createHeight.value] = [el.createHeight.value, el.createWidth.value];
	updateCreateDirection();
});
el.createImageAdd.addEventListener("click", () => {
	createImagePicker = { tab: documentTab(), queue: state.createImageQueue };
	el.createImages.click();
});
el.createImages.addEventListener("change", addCreateImages);
el.createImages.addEventListener("cancel", () => { createImagePicker = null; });
el.createImageOrder.addEventListener("change", event => { state.createImageQueue.order = event.target.value; sortCreateImages(); });
el.createImageList.addEventListener("click", event => {
	const row = event.target.closest("[data-image-id]");
	if (!row || state.imageImporting) return;
	state.createImageQueue.selected = Number(row.dataset.imageId);
	if (event.target.closest(".remove-button")) removeCreateImage();
	else updateCreateImageSelection();
});
el.createImageList.addEventListener("keydown", event => {
	const row = event.target.closest("[data-image-id]"), queue = state.createImageQueue;
	if (!row || event.ctrlKey || event.metaKey || event.shiftKey) return;
	const index = queue.entries.findIndex(entry => entry.id === Number(row.dataset.imageId));
	if (["ArrowUp", "ArrowDown", "Home", "End"].includes(event.key)) {
		event.preventDefault();
		queue.selected = queue.entries[index].id;
		if (event.altKey && event.key.startsWith("Arrow")) moveCreateImage(event.key === "ArrowUp" ? -1 : 1);
		else {
			const next = event.key === "Home" ? 0 : event.key === "End" ? queue.entries.length - 1 : Math.max(0, Math.min(queue.entries.length - 1, index + (event.key === "ArrowUp" ? -1 : 1)));
			queue.selected = queue.entries[next].id;
			updateCreateImageSelection();
		}
		focusCreateImage();
	} else if (event.key === "Delete" && !event.altKey) {
		event.preventDefault();
		queue.selected = queue.entries[index].id;
		removeCreateImage();
		focusCreateImage();
	}
});
el.createImageList.addEventListener("dragstart", event => {
	const row = event.target.closest("[data-image-id]");
	if (!row || state.imageImporting) { event.preventDefault(); return; }
	state.createImageQueue.drag = Number(row.dataset.imageId);
	event.dataTransfer.effectAllowed = "move";
	event.dataTransfer.setData("application/x-ofdgo-image", row.dataset.imageId);
});
el.createImageList.addEventListener("dragover", event => {
	if (state.createImageQueue?.drag == null || !event.dataTransfer.types.includes("application/x-ofdgo-image")) return;
	event.preventDefault();
	event.dataTransfer.dropEffect = "move";
	const row = event.target.closest("[data-image-id]");
	for (const item of el.createImageList.children) delete item.dataset.drop;
	if (row) row.dataset.drop = event.clientY < row.getBoundingClientRect().top + row.offsetHeight / 2 ? "before" : "after";
});
el.createImageList.addEventListener("drop", event => {
	const queue = state.createImageQueue, row = event.target.closest("[data-image-id]");
	if (queue?.drag == null || !event.dataTransfer.types.includes("application/x-ofdgo-image")) return;
	event.preventDefault();
	const from = queue.entries.findIndex(entry => entry.id === queue.drag);
	let to = row ? queue.entries.findIndex(entry => entry.id === Number(row.dataset.imageId)) + (row.dataset.drop === "after" ? 1 : 0) : queue.entries.length;
	if (to > from) to--;
	queue.selected = queue.drag;
	queue.drag = null;
	reorderCreateImage(from, to);
});
el.createImageList.addEventListener("dragend", () => {
	if (state.createImageQueue) state.createImageQueue.drag = null;
	for (const row of el.createImageList.children) delete row.dataset.drop;
});
el.createForm.addEventListener("submit", createDocument);
editorClick(el.insertTextButton, toggleTextTool);
editorClick(el.insertImageButton, () => openInsertPanel());
el.insertCancel.addEventListener("click", () => el.insertPanel.close());
el.insertPanel.addEventListener("close", () => { state.insertObject = null; });
el.insertForm.addEventListener("submit", insertObject);
el.textFontAdd.addEventListener("click", () => openFontFile(el.fontInput));
el.saveButton.addEventListener("click", () => exportFile(true, null, "ofd", false));
el.saveAsButton.addEventListener("click", () => exportFile(true, null, "ofd", true));
editorClick(el.encryptSaveButton, openEncryptionPanel);
el.signButton.addEventListener("click", () => {
	if (!state.doc || document.body.hasAttribute("data-busy")) return;
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
el.signPanel.addEventListener("close", () => { if (!el.signPanel.open && !sealPreview) el.signForm.reset(); });
el.signForm.addEventListener("submit", signDocument);
const sealPosition = Object.fromEntries(["Layer", "Stamp", "Image"].map(name => [name, document.querySelector(`#signPosition${name}`)]));
let sealPreview = null;
document.querySelector("#signPick").addEventListener("click", openSealPosition);
el.viewerPanel.addEventListener("pointerdown", beginSealDrag, true);
el.viewerPanel.addEventListener("pointerdown", pickResourceObject, true);
document.addEventListener("keydown", event => {
	if (!resourceManager.pick || el.objectPicker.open || el.confirmPanel.open) return;
	if (event.key === "Escape") { event.preventDefault(); event.stopImmediatePropagation(); finishResourcePick(); return; }
	if (event.target.closest?.("input, textarea, select")) return;
	if (event.key !== "Tab") { event.preventDefault(); event.stopImmediatePropagation(); }
}, true);
el.viewerPanel.addEventListener("pointermove", moveSealDrag, true);
el.viewerPanel.addEventListener("pointerup", confirmSealPosition, true);
el.viewerPanel.addEventListener("pointercancel", () => { if (sealPreview) finishSealPosition(); }, true);
el.verifyButton.addEventListener("click", () => {
	if (!state.doc || document.body.hasAttribute("data-busy")) return;
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
el.annotationsTab.addEventListener("click", () => showNavigation(el.annotationsTab));
el.annotationsFilter.addEventListener("input", renderAnnotationSummary);
el.annotationsKind.addEventListener("change", renderAnnotationSummary);
el.versionExport.addEventListener("click", exportManagedVersion);
editorClick(settingsElements.Button, () => openDocumentSettings());
editorClick(el.invoiceButton, () => openDocumentSettings(true));
settingsElements.Form.addEventListener("submit", event => event.preventDefault());
settingsElements.Panel.addEventListener("cancel", event => { event.preventDefault(); closeDocumentSettings(); });
settingsElements.Panel.addEventListener("close", () => { if (!settings.suspended) { settings.sequence++; settings.drafts.clear(); settings.views.clear(); settings.view = ""; invoicePanel.drafts.clear(); invoicePanel.views.clear(); invoicePanel.key = ""; } });
document.querySelector("#objectActionsButton").addEventListener("click", async () => {
	const item = canvasEditor.selected;
	if (!item || item.scoped || item.items) return;
	const original = state.objectLinkOriginal;
	if ((el.objectLinkKind.value !== original.kind || el.objectLinkAddress.value !== original.address || el.objectLinkPage.value !== original.page)
		&& !await requestConfirmation("链接尚未应用，放弃链接更改？")) return;
	syncObjectLink(item);
	settings.scope = "object";
	settings.kind = "actions";
	await openDocumentSettings();
	settings.locked = true;
	settingsElements.Tabs.hidden = true;
});
el.outlinesTab.addEventListener("click", () => showNavigation(el.outlinesTab));
el.searchTab.addEventListener("click", focusSearch);
el.navigationTabs.addEventListener("keydown", (event) => {
	if (event.isComposing || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) {
		return;
	}
	if (["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) {
		event.preventDefault();
		const tabs = [el.pagesTab, el.outlinesTab, el.annotationsTab, el.searchTab].filter((tab) => !tab.hidden);
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
el.ofdInput.addEventListener("change", async () => { await Promise.all(Array.from(el.ofdInput.files, file => openOFD(file))); });
el.fontInput.addEventListener("change", openSelectedFonts);
el.fontDirectoryInput.addEventListener("change", openSelectedFonts);
el.prevButton.addEventListener("click", () => pageView.renderPage(pageView.pageGroupStart() - state.pageLayout));
el.imageDPI.addEventListener("change", () => changeDisplayMode());
el.renderVectorButton.addEventListener("click", () => changeDisplayMode("svg"));
el.renderRasterButton.addEventListener("click", () => changeDisplayMode("raster"));
el.nextButton.addEventListener("click", () => pageView.renderPage(pageView.pageGroupStart() + state.pageLayout));
el.zoomOutButton.addEventListener("click", () => pageView.setScale(state.scale - 0.1));
el.zoomInButton.addEventListener("click", () => pageView.setScale(state.scale + 0.1));
el.fitButton.addEventListener("click", pageView.fitWidth);
el.fitHeightButton.addEventListener("click", pageView.fitHeight);
el.rotateButton.addEventListener("click", pageView.rotatePages);
el.panButton.addEventListener("click", pageView.togglePan);
el.continuousButton.addEventListener("click", pageView.toggleContinuous);
el.pageLayoutButton.addEventListener("click", pageView.togglePageLayout);
el.annotationButton.addEventListener("click", toggleAnnotations);
document.addEventListener("copy", copySelection);
document.addEventListener("cut", copySelection);
el.viewerPanel.addEventListener("paste", pasteEditorContent);
el.editorTools.addEventListener("paste", pasteEditorContent);
document.addEventListener("selectionchange", syncSelection);
el.exportFormat.addEventListener("change", () => updateDPIControl());
el.outputCompression.addEventListener("change", () => {
	el.compressionValue.textContent = el.outputCompression.selectedOptions[0].textContent;
	el.compressionValue.dataset.short = ["原", "无", "轻", "均", "强"][Number(el.outputCompression.value)];
});
el.exportPageButton.addEventListener("click", () => exportFile(false));
el.exportButton.addEventListener("click", () => openExportPanel());
el.exportCancel.addEventListener("click", () => el.exportPanel.close());
el.encryptionPanel.addEventListener("close", () => { if (!el.encryptionPanel.open) clearEncryptionForm(); });
el.encryptionPanel.addEventListener("cancel", event => { if (el.encryptionForm.inert) event.preventDefault(); });
el.encryptionCancel.addEventListener("click", () => el.encryptionPanel.close());
el.encryptionType.addEventListener("change", () => {
	state.encryptionChanged = false;
	updateEncryptionType();
});
el.encryptionChange.addEventListener("click", () => {
	state.encryptionChanged = true;
	updateEncryptionType();
	(el.encryptionType.value === "password" ? el.encryptionPassword : el.encryptionCertificates).focus();
});
el.encryptionForm.addEventListener("submit", applyEncryption);
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
		return exportFile(true, state.exportPages);
	}
});
el.refreshAppButton.addEventListener("click", refreshApplication);
el.pageInput.addEventListener("change", () => {
	const page = Number.parseInt(el.pageInput.value, 10);
	if (Number.isFinite(page)) {
		pageView.renderPage(page - 1);
	}
});
window.addEventListener("resize", resizeViewer);
window.addEventListener("resize", () => {
	for (const tabs of document.querySelectorAll(".panel-tabs, .document-tabs")) revealPanelTab(tabs);
	updateTitleBarIcons();
});
el.documentTabs.addEventListener("scroll", updateTitleBarIcons);
window.addEventListener("blur", () => pageView.endPan());
COMPACT_LAYOUT.addEventListener("change", syncLayoutMode);
el.viewerPanel.addEventListener("scroll", () => {
	pageView.schedulePageSync();
});
el.navigationContent.addEventListener("scroll", () => {
	if (state.thumbnailFrame) return;
	state.thumbnailFrame = requestAnimationFrame(() => {
		state.thumbnailFrame = 0;
		pageView.syncThumbnailWindow();
	});
});
for (const [viewport, current] of [[el.viewerPanel, () => state.pageWindow], [el.navigationContent, () => el.pageList.hidden ? null : state.thumbnailWindow]]) {
	viewport.addEventListener("wheel", event => {
		const view = current();
		if (!view || view.total <= SCROLL_SEGMENT_SIZE || event.ctrlKey || event.shiftKey || !event.deltaY) return;
		event.preventDefault();
		view.relativeScroll = false;
		const unit = event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? viewport.clientHeight : 1;
		pageView.scrollPageWindowBy(view, event.deltaY * unit);
		viewport.scrollLeft += event.deltaX * unit;
	}, { passive: false });
	viewport.addEventListener("pointerdown", event => {
		const view = current();
		if (view) view.relativeScroll = event.pointerType === "touch" || state.editing && !!event.target.closest(".page-shell");
	});
}
el.viewerPanel.addEventListener("pointerdown", pageView.startPan);
el.viewerPanel.addEventListener("pointerdown", event => {
	if (event.button !== 0 || state.editing || document.body.hasAttribute("data-busy")) return;
	const shell = event.target.closest(".page-shell");
	if (shell && state.doc) pageView.setCurrentPage(Number(shell.dataset.pageIndex));
});
el.viewerPanel.addEventListener("pointermove", pageView.movePan);
el.viewerPanel.addEventListener("pointerup", pageView.endPan);
el.viewerPanel.addEventListener("pointercancel", pageView.endPan);
el.viewerPanel.addEventListener("lostpointercapture", pageView.endPan);
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
		event.dataTransfer.dropEffect = document.body.hasAttribute("data-busy") ? "none" : "copy";
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
nativeHost?.installFileInputs(error => showError(error, false));
if (nativeHost) document.title = "OFDGo - OFD编辑器";
documentTab();
renderDocumentTabs();
el.documentNewTab.addEventListener("click", openEmptyDocumentTab);
updateSidebarState();
boot();

function editorShortcutTarget(target) {
	const field = target.closest?.("input, textarea, select, [contenteditable]");
	return !field || el.editorTools.contains(field) && field.matches('select, input[type="checkbox"], input[type="radio"], input[type="color"], input[type="range"]');
}

function handleKeyDown(event) {
	if (sealPreview && !el.confirmPanel.open) {
		if (event.key === "Escape") { event.preventDefault(); finishSealPosition(); }
		return;
	}
	if (event.defaultPrevented || event.isComposing || event.altKey) {
		return;
	}
	const key = event.key;
	const target = event.target;
	if ((event.ctrlKey || event.metaKey) && !event.shiftKey && key.toLowerCase() === "p") {
		event.preventDefault();
		openPrintPanel();
		return;
	}
	if (nativeHost && (event.ctrlKey || event.metaKey) && (key === "Tab" || key.toLowerCase() === "w")) {
		event.preventDefault();
		if (document.body.hasAttribute("data-busy") || documentDialogs?.applicationOpen() || formDialogOpen() && !documentTab().print?.context && !batchElements.Panel.open) return;
		if (key.toLowerCase() === "w") closeTab(documentTab());
		else {
			const count = workspace.tabs.length, index = workspace.tabs.indexOf(documentTab());
			activateDocumentTab(workspace.tabs[(index + (event.shiftKey ? -1 : 1) + count) % count]);
		}
		return;
	}
	if ((event.ctrlKey || event.metaKey) && key.toLowerCase() === "s" && state.doc
		&& (state.editorInfo || state.ofdBytes)) {
		event.preventDefault();
		if (!document.body.hasAttribute("data-busy") && !formDialogOpen()) exportFile(true, null, "ofd", event.shiftKey);
		return;
	}
	if ((event.ctrlKey || event.metaKey) && !event.shiftKey && key.toLowerCase() === "a" && state.doc
		&& (el.viewerPanel.contains(target) || state.editing && el.editorTools.contains(target)) && editorShortcutTarget(target)) {
		event.preventDefault();
		if (!document.body.hasAttribute("data-busy") && !formDialogOpen()) {
			if (state.editing) canvasEditor.selectAll(state.pageIndex);
			else selectDocumentText();
		}
		return;
	}
	if (document.body.hasAttribute("data-busy") || formDialogOpen()) {
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
			pageView.syncPageWindow(view, key === "Home" ? -pageView.windowInset(view) : view.total);
			pageView.syncCurrentPageFromScroll();
			return;
		}
		if (delta !== undefined && !event.ctrlKey && !event.metaKey && view.total > SCROLL_SEGMENT_SIZE) {
			event.preventDefault();
			pageView.scrollPageWindowBy(view, delta);
			return;
		}
	}
	if (event.ctrlKey || event.metaKey) {
		if (state.editing && editorShortcutTarget(target)
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
	if (state.editing && !event.shiftKey && el.editorTools.contains(target) && editorShortcutTarget(target)
		&& (key === "Delete" || key === "F2")) {
		canvasEditor.keyDown(event, true);
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
		focusSearch(event.shiftKey ? -1 : 1);
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
	if (documentTab().print?.context) return true;
	return el.confirmPanel.open || el.appearancePanel.open || Boolean(sealPreview || resourceManager.pick) || el.aboutPanel.open || el.closePanel.open || el.attachmentDialog.open || settingsElements.Panel.open || fontBindingElements.Panel.open || el.signPanel.open || el.verifyPanel.open || el.credentialsPanel.open || el.encryptionPanel.open || batchElements.Panel.open || el.exportPanel.open || el.createPanel.open || el.insertPanel.open || el.pagePanel.open || el.paragraphPanel.open || el.importPanel.open || el.infoPanel.open || el.documentPanel.open
		|| resourceElements.Panel.open || el.structureDialog.open || el.versionPanel.open || el.batchPagesPanel.open || el.objectStylePanel.open || el.outlinePanel.open || el.objectBoundsPanel.open || el.annotationNote.open || el.annotationCreate.open || el.objectPicker.open;
}

async function openAboutPanel() {
	el.aboutVersion.textContent = "读取中";
	documentDialogs.showApplication(el.aboutPanel);
	let raw = "";
	try {
		const response = await fetch("./", { cache: "no-store" });
		if (response.ok) raw = response.headers.get("X-OFDGo-Build-Version") || "";
	} catch {
		if (el.aboutPanel.open) el.aboutVersion.textContent = "版本信息不可用";
		return;
	}
	if (!el.aboutPanel.open) return;
	if (!raw) {
		el.aboutVersion.textContent = "develop";
		return;
	}
	const match = /^(\d+)-([0-9a-f]{40}|[0-9a-f]{64})$/i.exec(raw);
	const seconds = Number(match?.[1]);
	const date = new Date(seconds * 1000);
	if (!match || !Number.isSafeInteger(seconds) || Number.isNaN(date.getTime())) {
		el.aboutVersion.textContent = "版本信息异常";
		return;
	}
	const day = date.toLocaleDateString("zh-CN", { timeZone: "Asia/Shanghai", year: "numeric", month: "2-digit", day: "2-digit" }).replaceAll("/", "");
	el.aboutVersion.textContent = `${day}-${match[2].slice(0, 7)}`;
}

function selectAppearanceCategory(value) {
	for (const button of el.appearancePanel.querySelectorAll("[data-appearance-category]")) button.dataset.selected = String(button.dataset.appearanceCategory === value);
	for (const section of el.appearancePanel.querySelectorAll("[data-appearance-page]")) section.hidden = section.dataset.appearancePage !== value;
	el.appearancePanel.querySelector(".appearance-content").scrollTop = 0;
}

async function refreshFileAssociations() {
	if (!nativeHost || el.associationSection.dataset.busy) return;
	el.associationSection.dataset.busy = "true";
	for (const control of el.associationSection.querySelectorAll("button")) control.disabled = true;
	for (const label of el.associationSection.querySelectorAll(".association-state")) {
		label.textContent = "查询中";
		label.title = "";
	}
	try {
		const states = await nativeHost.call("association.query", {});
		el.associationCategory.hidden = !states.user.supported;
		if (el.associationCategory.hidden && el.associationCategory.dataset.selected === "true") {
			selectAppearanceCategory("theme");
			if (el.appearancePanel.open) el.appearancePanel.querySelector('[data-appearance-category="theme"]').focus({preventScroll:true});
		}
		for (const row of el.associationSection.querySelectorAll(".association-row")) {
			const state = states[row.dataset.scope];
			const label = row.querySelector(".association-state");
			label.textContent = state.error || !state.writable ? "不可用" : state.complete ? "已注册" : state.registered ? "待更新" : "未注册";
			label.title = state.error || "";
			row.querySelector('[data-action="register"]').disabled = !state.writable || !!state.error || state.complete;
			row.querySelector('[data-action="remove"]').disabled = !state.writable || !!state.error || !state.registered;
		}
	} catch (error) {
		el.associationCategory.hidden = false;
		for (const label of el.associationSection.querySelectorAll(".association-state")) label.textContent = "不可用";
		el.associationMessage.textContent = `查询失败：${error.message}`;
		el.associationMessage.hidden = false;
	} finally {
		delete el.associationSection.dataset.busy;
	}
}

async function changeFileAssociation(event) {
	const button = event.target.closest("button[data-action]");
	if (!nativeHost || !button || button.disabled || el.associationSection.dataset.busy) return;
	const row = button.closest(".association-row");
	el.associationSection.dataset.busy = "true";
	for (const control of el.associationSection.querySelectorAll("button")) control.disabled = true;
	el.associationMessage.hidden = true;
	row.querySelector(".association-state").textContent = button.dataset.action === "register" ? "注册中" : "撤销中";
	try {
		await nativeHost.call(`association.${button.dataset.action}`, {scope:row.dataset.scope});
	} catch (error) {
		el.associationMessage.textContent = `${row.firstElementChild.textContent}：${error.message}`;
		el.associationMessage.hidden = false;
	} finally {
		delete el.associationSection.dataset.busy;
		await refreshFileAssociations();
	}
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
	if (item?.type === "Annotation" || item?.richText) return false;
	if (!item?.scoped) return canEditObject(item, "update");
	if (item.type === "TextObject") return canEditObject(item, "replaceFont") && canEditObject(item, "paint");
	if (item.type === "PathObject") return canEditObject(item, "transform") && canEditObject(item, "paint");
	return item.type === "ImageObject" && canEditObject(item, "transform");
}

function syncObjectLink(item) {
	const link = item.link ? JSON.parse(item.link) : null;
	const targetPage = link?.Goto?.Dest ? pageIndexByID(state.doc.pages, link.Goto.Dest.PageID) : -1;
	el.objectLinkKind.value = item.linkCount > 1 ? "keep" : link?.URI ? "uri" : targetPage >= 0 ? "page" : link ? "keep" : "none";
	el.objectLinkKind.disabled = item.linkCount > 1;
	el.objectLinkAddress.value = link?.URI?.URI || "";
	el.objectLinkPage.value = String(targetPage >= 0 ? targetPage + 1 : 1);
	el.objectLinkPage.max = String(state.doc?.pages.length || 1);
	state.objectLinkOriginal = {kind: el.objectLinkKind.value, address: el.objectLinkAddress.value, page: el.objectLinkPage.value, link};
	updateObjectLinkFields();
}

function openObjectStyle() {
	const items = canvasEditor.items();
	if (!items.length) return;
	const ordered = [...items].sort((a, b) => a.position - b.position);
	el.groupObjectsButton.disabled = items.length < 2 || !ordered.every((item, index) => canEditObject(item, "copy") && canEditObject(item, "order") && item.container === ordered[0].container && item.position === ordered[0].position + index);
	el.ungroupObjectButton.disabled = items.length !== 1 || Boolean(items[0].richText) || !canEditObject(items[0], "ungroup");
	el.copyStyleButton.disabled = items.length !== 1 || !canCopyStyle(items[0]);
	el.pasteStyleButton.disabled = !state.styleClipboard || !items.every(item => item.type === state.styleClipboard && canCopyStyle(item));
	const shared = (key, fallback) => items.every(item => (item[key] ?? fallback) === (items[0][key] ?? fallback)) ? items[0][key] ?? fallback : null;
	el.objectLinkFields.hidden = items.length !== 1 || items[0].scoped;
	syncObjectLink(items[0]);
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
		const pattern = items.length === 1 && !items[0].scoped && !items[0].richText ? items[0][`${target}Pattern`] : null;
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
		if (type === "color") input.className = "color-input";
		input.value = value;
		input.disabled = el.gradientKind.value === "keep";
		input.title = label;
		if (type === "number") { input.min = "0"; input.max = "100"; input.step = "any"; input.required = true; }
		row.append(input);
	}
	const remove = document.createElement("button");
	remove.type = "button";
	remove.className = "icon-button remove-button";
	remove.title = "删除色标";
	remove.addEventListener("click", () => row.remove());
	row.append(remove);
	el.gradientStops.append(row);
}

function openOutlinePanel(action) {
	state.outlineAction = action;
	el.outlinePanelTitle.textContent = { add: "新增目录", update: "编辑目录", delete: "删除目录", move: "移动目录" }[action];
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

function openImportPanel(documents = false) {
	if (!state.editing || document.body.hasAttribute("data-busy")) return;
	state.importPageCount = 0;
	state.importDocumentCount = 0;
	state.importDocumentMode = documents;
	el.importTitle.textContent = documents ? "导入文档" : "插入页面";
	state.importEncrypted = false;
	el.importForm.reset();
	el.importStatus.textContent = "";
	el.importDocuments.replaceChildren();
	el.importDocuments.hidden = true;
	el.importPagesLabel.textContent = documents ? "文档" : "页码";
	el.importPages.removeAttribute("title");
	el.importPositionRow.hidden = documents;
	el.importOutlinesRow.hidden = documents;
	el.importSubmit.textContent = documents ? "导入" : "插入";
	el.importSubmit.disabled = true;
	el.importPagesRow.hidden = true;
	el.importPages.disabled = true;
	el.importPages.required = false;
	updateImportControls();
	el.importPanel.showModal();
}

function selectedImportDocuments() {
	return Array.from(el.importDocuments.children, row => row.children[0]).filter(input => input.checked).map(input => input.value).join(",");
}

function updateImportControls(busy = document.body.hasAttribute("data-busy")) {
	const custom = el.importRange.value === "custom";
	const documents = state.importDocumentMode;
	const count = documents ? state.importDocumentCount : state.importPageCount;
	el.importRange.disabled = busy || !count;
	el.importDocuments.hidden = !documents || !custom || !count;
	for (const row of el.importDocuments.children) row.children[0].disabled = busy;
	el.importPagesRow.hidden = documents || !custom;
	el.importPages.disabled = busy || documents || !custom;
	el.importPages.required = !documents && custom;
	el.importSubmit.disabled = busy || !count || (documents && custom && !selectedImportDocuments());
}

async function loadImportFile() {
	const tab = documentTab(), state = tab.state;
	const callWASM = (name, ...args) => callTabWASM(tab, name, ...args);
	const file = el.importFile.files[0];
	const openSeq = state.openSeq;
	state.importPageCount = 0;
	state.importDocumentCount = 0;
	el.importDocuments.replaceChildren();
	state.importEncrypted = false;
	el.importSubmit.disabled = true;
	el.importStatus.textContent = "";
	setBusy(true);
	try {
		const data = file ? await documentBytes(file) : null;
		if (openSeq !== state.openSeq) return;
		const info = data ? await openWithCredentials(null, openSeq, data, true, false, tab) : await callWASM("ofdgoLoadImport", null);
		if (openSeq !== state.openSeq || !el.importPanel.open) {
			await callWASM("ofdgoLoadImport", null);
			return;
		}
		state.importPageCount = info?.pageCount || 0;
		state.importDocumentCount = info?.documentCount || 0;
		state.importEncrypted = Boolean(info?.encrypted);
		if (state.importDocumentMode) {
			el.importDocuments.replaceChildren(...(info?.documents || []).map(entry => {
				const row = document.createElement("label");
				const input = document.createElement("input");
				input.type = "checkbox";
				input.value = String(entry.index + 1);
				const name = document.createElement("span");
				name.textContent = `${entry.index + 1} · ${entry.title || "未命名"}`;
				row.append(input, name);
				return row;
			}));
			el.importStatus.textContent = `共${state.importDocumentCount}个文档${info?.documentsSigned || state.doc.signatureCount ? " · 合并后需验签" : ""}`;
		} else {
			el.importPages.title = state.importPageCount ? `页码 1-${state.importPageCount}` : "页码";
			el.importStatus.textContent = info?.signed ? "保留签章外观，原签名不再有效" : "";
		}
		el.importSubmit.disabled = !(state.importDocumentMode ? state.importDocumentCount : state.importPageCount);
		updateImportControls(false);
	} catch (err) {
		if (openSeq === state.openSeq && el.importPanel.open) el.importStatus.textContent = err.message;
	} finally {
		setBusy(false, "", 0, "", tab);
	}
}

function openPagePanel() {
	if (!state.editing || document.body.hasAttribute("data-busy")) {
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

function documentTab() {
	if (!workspace.active) {
		const tab = createDocumentTab(state);
		tab.resources = resourceManager;
		tab.settings = settings;
		tab.invoice = invoicePanel;
		workspace.tabs.push(tab);
		workspace.active = tab;
	}
	return workspace.active;
}

function isForegroundDocument(tab) {
	return !tab.closed && workspace.active === tab;
}

function applyTitleBarInfo(info) {
	if (!info || !info.height) return;
	workspace.titleBarReady = true;
	document.documentElement.toggleAttribute("data-window-material", info.material === 1 || info.material === 2);
	document.body.style.setProperty("--titlebar-height", `${info.height}px`);
	el.documentTitleBar.style.setProperty("--titlebar-left", `${info.left}px`);
	el.documentTitleBar.style.setProperty("--titlebar-right", `${info.right}px`);
	el.documentTitleBar.dataset.active = String(info.active);
	el.documentTitleBar.dataset.maximized = String(info.maximized);
	revealPanelTab(el.documentTabs);
	updateTitleBarRegions();
}

function updateTitleBarIcons() {
	const scale = window.devicePixelRatio;
	el.documentTitleBar.style.setProperty("--titlebar-icon-size", `${Math.floor(10 * scale) / scale}px`);
	el.documentTitleBar.style.setProperty("--titlebar-settings-icon-size", `${Math.floor(12 * scale) / scale}px`);
	const icons = [...el.documentTitleBar.querySelectorAll(".close-icon, .new-icon, .settings-icon, .update-icon")];
	for (const icon of icons) icon.style.inset = "";
	const offsets = icons.map(icon => {
		const bounds = icon.getBoundingClientRect();
		return `${Math.round(bounds.y * scale) / scale - bounds.y}px auto auto ${Math.round(bounds.x * scale) / scale - bounds.x}px`;
	});
	icons.forEach((icon, index) => icon.style.inset = offsets[index]);
}

function updateTitleBarRegions() {
	updateTitleBarIcons();
	if (!nativeHost || !workspace.titleBarReady) return;
	const bounds = el.documentTitleBar.querySelector(".document-title-drag").getBoundingClientRect();
	const regions = bounds.width > 0 && bounds.height > 0 ? [{x: bounds.x, y: bounds.y, width: bounds.width, height: bounds.height}] : [];
	workspace.dragRegions = regions;
	if (workspace.dragUpdating) return;
	workspace.dragUpdating = true;
	Promise.resolve().then(async () => {
		while (workspace.dragRegions) {
			const regions = workspace.dragRegions;
			workspace.dragRegions = null;
			await nativeHost.call("window.drag", {regions});
		}
	}).catch(error => showError(error, false)).finally(() => {
		workspace.dragUpdating = false;
		if (workspace.dragRegions) updateTitleBarRegions();
	});
}

async function closeWorkspace() {
	if (workspace.closing) return;
	workspace.closing = true;
	try {
		for (const tab of workspace.tabs) cancelDocumentPreparation(tab);
		await Promise.all(workspace.tabs.filter(tab => tab.state.saving && tab.saveCompletion).map(tab => tab.saveCompletion));
		if (workspace.tabs.some(tab => tab.state.saving)) { setStatus("文档正在保存，请稍后关闭"); return; }
		if (batch.running && !await requestConfirmation("批量转换尚未完成，是否退出？", null)) return;
		if (workspace.tabs.some(tab => tab.opening || tab.state.exporting) && !await requestConfirmation("文档正在处理，是否取消并退出？", null)) return;
		const activeDirty = state.dirty || canvasEditor.textChanged() || canvasEditor.cropChanged() || canvasEditor.nudgeChanged();
		const dirty = activeDirty || workspace.tabs.some(tab => tab.state.dirty);
		if (dirty && (workspace.switching || workspace.tabs.some(tab => tab.closing) || formDialogOpen() || (activeDirty && document.body.hasAttribute("data-busy")))) {
			setStatus("操作正在进行，请完成或取消后关闭");
			return;
		}
		if (!dirty) {
			await closeApplicationWindow();
			return;
		}
		const count = workspace.tabs.filter(tab => tab.state.dirty || tab === workspace.active && (canvasEditor.textChanged() || canvasEditor.cropChanged() || canvasEditor.nudgeChanged())).length;
		const action = await requestCloseAction("退出程序", `有${count}个文档尚未保存`, "保存");
		if (action === "cancel") return;
		if (action === "save") {
			if (!await commitDocumentView()) return;
			for (const tab of workspace.tabs) {
				if (!tab.state.dirty) continue;
				if (!await activateDocumentTab(tab, true)) return;
				if (!await exportFile(true, null, "ofd", false) || tab.state.dirty) return;
			}
		}
		await closeApplicationWindow();
	} finally {
		workspace.closing = false;
	}
}

async function closeApplicationWindow() {
	await window.ofdgoTheme.flush();
	window.removeEventListener("beforeunload", warnUnsaved);
	window.removeEventListener("beforeunload", warnBatch);
	let closed = false;
	try {
		closed = await nativeHost.call("window.close", {});
	} finally {
		if (!closed) {
			window.addEventListener("beforeunload", warnUnsaved);
			window.addEventListener("beforeunload", warnBatch);
		}
	}
}

function createDocumentTab(value = createDocumentState()) {
	return { id: ++workspace.nextID, state: value, resources: createResourceManager(), settings: createSettings(),
		invoice: createInvoicePanel(), worker: null, promise: null, closed: false, localFonts: [], view: null, status: STATUS.ready,
		busy: false, progress: null, opening: null, touched: Date.now() };
}

function advanceDocumentSequence() {
	state.openSeq = workspace.sequence = Math.max(workspace.sequence, state.openSeq) + 1;
	return state.openSeq;
}

function copyApplicationPreferences(target) {
	for (const key of ["showPages", "showMeta", "exportFormats"]) target[key] = state[key];
	const { fontChoice, ...defaults } = state.textDefaults;
	target.textDefaults = { ...defaults, ...(target.textDefaults.fontChoice ? {fontChoice: target.textDefaults.fontChoice} : {}) };
}

function renderDocumentTabs() {
	documentTab();
	const focused = el.documentTabs.contains(document.activeElement) ? document.activeElement.closest(".document-tab")?.dataset.id : null;
	el.documentTabs.replaceChildren();
	for (const tab of workspace.tabs) {
		const item = document.createElement("div");
		item.className = "document-tab";
		item.dataset.id = String(tab.id);
		item.dataset.selected = String(tab === workspace.active);
		item.tabIndex = tab === workspace.active ? 0 : -1;
		item.draggable = true;
		const name = document.createElement("span");
		name.className = "document-tab-name";
		const title = tab.state.doc || tab.opening || tab.error ? tab.state.fileName : "新标签";
		name.textContent = `${tab.state.dirty ? "● " : ""}${title}`;
		item.title = `${title}${tab.opening ? ` · ${STATUS.opening}` : tab.state.saving ? " · 正在保存" : tab.error ? ` · ${tab.error}` : ""}`;
		item.dataset.loading = String(Boolean(tab.opening));
		item.dataset.error = String(Boolean(tab.error));
		if (tab.error) name.textContent = `! ${name.textContent}`;
		const close = document.createElement("button");
		close.type = "button";
		close.className = "document-tab-close";
		close.title = "关闭";
		const icon = document.createElement("span");
		icon.className = "close-icon";
		close.append(icon);
		close.disabled = !nativeHost && !tab.state.doc && !tab.opening && !tab.error && workspace.tabs.length === 1;
		close.addEventListener("click", async event => {
			event.stopPropagation();
			await closeTab(tab);
		});
		item.append(name, close);
		item.addEventListener("click", () => activateDocumentTab(tab));
		item.addEventListener("auxclick", async event => {
			if (event.button !== 1) return;
			event.preventDefault();
			await closeTab(tab);
		});
		item.addEventListener("dragstart", event => {
			if (event.target.closest("button")) { event.preventDefault(); return; }
			event.dataTransfer.setData("application/x-ofdgo-tab", String(tab.id));
			event.dataTransfer.effectAllowed = "move";
		});
		item.addEventListener("dragover", event => {
			if (Array.from(event.dataTransfer.types).includes("application/x-ofdgo-tab")) { event.preventDefault(); event.dataTransfer.dropEffect = "move"; }
		});
		item.addEventListener("drop", event => {
			const index = workspace.tabs.findIndex(value => String(value.id) === event.dataTransfer.getData("application/x-ofdgo-tab"));
			if (index < 0) return;
			event.preventDefault();
			event.stopPropagation();
			const bounds = item.getBoundingClientRect();
			const after = event.clientX > bounds.left + bounds.width / 2;
			const [moved] = workspace.tabs.splice(index, 1);
			if (moved === tab) workspace.tabs.splice(index, 0, moved);
			else workspace.tabs.splice(workspace.tabs.indexOf(tab) + Number(after), 0, moved);
			renderDocumentTabs();
		});
		item.addEventListener("keydown", async event => {
			if (!["ArrowLeft", "ArrowRight", "Home", "End", "Delete"].includes(event.key) || event.target !== item) return;
			event.preventDefault();
			if (event.key === "Delete") { await closeTab(tab); return; }
			const index = workspace.tabs.indexOf(tab), count = workspace.tabs.length;
			const next = event.key === "Home" ? 0 : event.key === "End" ? count - 1 : (index + (event.key === "ArrowRight" ? 1 : -1) + count) % count;
			if (await activateDocumentTab(workspace.tabs[next])) el.documentTabs.querySelector('[data-selected="true"]')?.focus();
		});
		el.documentTabs.append(item);
	}
	revealPanelTab(el.documentTabs);
	if (focused) el.documentTabs.querySelector(`[data-id="${focused}"]`)?.focus({preventScroll: true});
	updateTitleBarRegions();
}

async function commitDocumentView() {
	return await canvasEditor.commitNudge() && await canvasEditor.commitText() && await canvasEditor.commitCrop();
}

function detachDocumentView() {
	const tab = documentTab();
	if (sealPreview) finishSealPosition(true);
	if (resourceElements.Panel.open) rememberResourceView();
	documentDialogs?.suspend(tab);
	tab.credentials = credentialRequest;
	credentialRequest = null;
	tab.fontBinding = {...fontBinding};
	tab.attachmentEdit = el.attachmentDialog.attachmentEdit;
	for (const face of state.svgFonts.values()) document.fonts.delete(face);
	tab.worker?.setForeground?.(false);
	window.clearTimeout(wasmRecoveryTimer);
	wasmRecoveryTimer = 0;
	const selection = canvasEditor.selected;
	tab.view = { anchor: state.doc ? pageView.scaleAnchor() : null, navigation: el.navigationTabs.querySelector('[data-selected="true"]'),
		navigationTop: el.navigationContent.scrollTop, metaTop: el.metaPanel.scrollTop, tool: canvasEditor.tool,
		selection: selection ? {index: selection.index, ids: (selection.items || [selection]).map(item => item.id)} : canvasEditor.pendingSelection,
		search: el.searchInput.value };
	tab.view.searchStatus = state.searchRunning ? "搜索已暂停" : el.searchStatus.textContent;
	if (state.searchRunning) {
		state.searchPaused = true;
		state.searchSeq++;
		state.searchRunning = false;
	}
	tab.localFonts = fontManager.localFonts;
	if (state.createImageQueue) {
		state.createImageQueue.observer?.disconnect();
		state.createImageQueue.drag = null;
	}
	tab.status = el.statusText.textContent;
	advanceDocumentSequence();
	state.pageSeq++;
	if (state.scrollFrame) cancelAnimationFrame(state.scrollFrame);
	if (state.thumbnailFrame) cancelAnimationFrame(state.thumbnailFrame);
	if (state.openActionFrame) cancelAnimationFrame(state.openActionFrame);
	state.scrollFrame = state.thumbnailFrame = state.openActionFrame = 0;
	state.openActionTask = null;
	pageView.resetPageLoading();
	state.pageWindow = null;
	state.thumbnailWindow = null;
	state.visiblePages.clear();
	state.selectedPages.clear();
	state.documentSelection = null;
	cancelPageTouch?.();
	pageView.endPan();
	if (!documentDialogs) clearSecurityForms();
	if (resourceElements.Panel.open) rememberResourceView();
	workspace.resourceView = null;
	if (documentDialogs) {
		for (const player of resourceElements.Viewer.querySelectorAll("audio, video")) player.pause();
	} else releaseResourcePreview();
	resourceManager.sequence++;
	settings.sequence++;
	fontBinding.sequence++;
	selectPicker.close();
	fontPicker.close();
	fontPicker.changing = null;
	for (const [input, control] of editorInputs) {
		editorInputs.set(input, { ...control, pending: null, task: null, handled: true });
	}
	canvasEditor.cancelNudge();
	canvasEditor.clear();
	canvasEditor.setTool("");
	if (!documentDialogs) for (const panel of [resourceElements.Panel, settingsElements.Panel, el.closePanel, el.attachmentDialog, el.createPanel,
		el.insertPanel, fontBindingElements.Panel, el.pagePanel, el.paragraphPanel, el.infoPanel, el.documentPanel,
		el.versionPanel, el.annotationNote, el.annotationCreate, el.objectPicker, el.importPanel, el.batchPagesPanel,
		el.objectStylePanel, el.objectBoundsPanel, el.outlinePanel, el.structureDialog]) panel.close();
}

function attachDocumentTab(tab) {
	clearBusyControls();
	copyApplicationPreferences(tab.state);
	workspace.active = tab;
	tab.touched = Date.now();
	state = tab.state;
	el.imageDPI.value = String(state.renderDPI);
	for (const face of state.svgFonts.values()) document.fonts.add(face);
	state.textFontID = undefined;
	resourceManager = tab.resources;
	settings = tab.settings;
	invoicePanel = tab.invoice;
	credentialRequest = tab.credentials || null;
	Object.assign(fontBinding, tab.fontBinding || {font: null, choices: [], sequence: fontBinding.sequence + 1});
	wasmWorker = tab.worker;
	wasmWorker?.setForeground?.(true);
	remoteDownload = tab.download || null;
	fontManager.localFonts = tab.localFonts;
	advanceDocumentSequence();
	if (state.annotationCreate) state.annotationCreate.openSeq = state.openSeq;
	el.attachmentDialog.attachmentEdit = tab.attachmentEdit ? {...tab.attachmentEdit, openSeq: state.openSeq} : null;
	el.svgHost.replaceChildren();
	el.pageList.replaceChildren();
	el.outlineList.replaceChildren();
	el.annotationsList.replaceChildren();
	el.annotationsStatus.textContent = "";
	el.searchInput.value = tab.view?.search || "";
	el.searchStatus.textContent = tab.view?.searchStatus || "";
	el.searchResults.replaceChildren();
	renderSearchResults();
	el.ofdInput.value = "";
	canvasEditor.pendingSelection = tab.view?.selection || null;
	documentDialogs?.setOwner(tab);
	if (el.createPanel.open && state.createImageQueue) renderCreateImages();
	workspace.resourceView = resourceElements.Panel.open ? resourceManager : null;
	pageView.setContinuous(state.continuous);
	pageView.updatePanControl();
	pageView.trimWorkspaceCaches();
	renderDocumentTabs();
}

async function openEmptyDocumentTab() {
	if (workspace.closing) return;
	const tab = createDocumentTab();
	workspace.tabs.push(tab);
	if (!await activateDocumentTab(tab) && workspace.active !== tab && workspace.nextTab !== tab) {
		workspace.tabs.splice(workspace.tabs.indexOf(tab), 1);
		await releaseDocumentTab(tab);
		renderDocumentTabs();
	}
}

async function activateDocumentTab(tab, closing = false) {
	if (!tab || tab.closed) return false;
	if (workspace.closing && !closing) return false;
	if (tab === documentTab()) return true;
	if (workspace.switching) { workspace.nextTab = tab; return false; }
	const focused = el.documentTitleBar.contains(document.activeElement);
	workspace.switching = true;
	try {
		try {
			if (!documentTab().busy && !await commitDocumentView()) return false;
			detachDocumentView();
			attachDocumentTab(tab);
			if (focused) el.documentTabs.querySelector('[data-selected="true"]')?.focus({preventScroll: true});
		} finally {
			workspace.switching = false;
		}
		await mountDocumentTab(tab);
		return true;
	} catch (error) {
		if (isForegroundDocument(tab)) showError(error, !state.doc);
		return false;
	} finally {
		const next = workspace.nextTab;
		workspace.nextTab = null;
		if (next && next !== workspace.active && !next.closed) queueMicrotask(() => activateDocumentTab(next));
	}
}

async function refreshDocumentTab(tab) {
	tab.refreshPending = true;
	updateWorkspaceDirty();
	if (isForegroundDocument(tab)) {
		advanceDocumentSequence();
		await mountDocumentTab(tab);
	}
}

async function mountDocumentTab(tab) {
	if (!isForegroundDocument(tab)) return;
	const state = tab.state;
	const openSeq = state.openSeq;
	const callWASM = (name, ...args) => callTabWASM(tab, name, ...args);
	if (tab.opening || tab.error) {
		pageView.renderPageList();
		renderOutlines();
		renderMeta();
		setEmpty(tab.error || tab.status);
		setStatus(tab.status);
		setBusy(tab.busy, tab.progress?.text || "", tab.progress?.percent ?? null, tab.status, tab);
		return;
	}
	if (state.doc) {
		const view = tab.view;
		if (state.wasmExited && state.editorInfo) {
			setEmpty("引擎中断，未保存内容无法恢复");
			updateControls();
			setStatus(tab.status);
			setBusy(false);
			return;
		}
		if (tab.configurePending && !state.wasmExited) {
			setBusy(true, "正在同步文档", null);
			state.doc = await callWASM("ofdgoConfigure", await fontManager.files(fontManager.records()), state.renderAnnotations);
			tab.configurePending = false;
		}
		if (!isForegroundDocument(tab) || openSeq !== state.openSeq) return;
		if (tab.refreshPending) {
			state.objectClipboardToken = state.styleClipboard = null;
			state.pageSelection.clear();
			state.pageSelectionAnchor = null;
			state.documentSelection = null;
			canvasEditor.pendingSelection = null;
		}
		await openDocument({ doc: state.wasmExited ? undefined : state.doc, pageIndex: state.pageIndex, fitMode: state.fitMode || "free", scale: state.scale,
			skipAutoFonts: !tab.initialLoad, keepSearch: !tab.refreshPending, keepCache: !tab.refreshPending, restoreTab: !tab.initialLoad, resetScroll: Boolean(tab.initialLoad), openSeq });
		if (!isForegroundDocument(tab) || openSeq !== state.openSeq) return;
		tab.configurePending = false;
		tab.initialLoad = false;
		tab.refreshPending = false;
		if (view?.anchor) pageView.restoreScaleAnchor(view.anchor);
		showNavigation(view?.navigation || el.pagesTab);
		el.navigationContent.scrollTop = view?.navigationTop || 0;
		el.metaPanel.scrollTop = view?.metaTop || 0;
		canvasEditor.setTool(view?.tool || "");
	} else {
		pageView.renderPageList();
		renderOutlines();
		renderMeta();
		el.zoomLabel.textContent = "100%";
		setEmpty(STATUS.ready);
	}
	renderFontList();
	updateFontSummary();
	updateControls();
	tab.controlsPending = false;
	setStatus(tab.status);
	setBusy(Boolean(tab.opening || tab.editTask || tab.state.exporting), tab.progress?.text || "", tab.progress?.percent ?? null, tab.status, tab);
	if (documentDialogs) await resumeDocumentPanels(tab);
	if (isForegroundDocument(tab) && state.searchPaused && !tab.busy) searchDocument();
}

async function releaseDocumentTab(tab) {
	if (tab.closed) return;
	releaseCreateImages(tab);
	const clipboard = workspace.objectClipboard;
	if (clipboard?.tab === tab) {
		const prepared = await prepareObjectClipboard(clipboard, tab);
		clipboard.tab = null;
		if (!prepared && workspace.objectClipboard === clipboard) workspace.objectClipboard = null;
	}
	tab.closed = true;
	for (const control of editorInputs.values()) {
		if (control.pending?.tab === tab) { control.pending = null; control.handled = true; }
	}
	tab.print?.destroy();
	tab.print = null;
	tab.opening?.controller.abort();
	tab.opening = null;
	documentDialogs?.release(tab);
	tab.credentials?.resolve(null);
	tab.credentials = null;
	await markWASMExited(tab.state.wasmSeq, null, tab);
	for (const image of tab.state.svgImages.values()) URL.revokeObjectURL(image.url);
	for (const font of tab.state.svgFonts.values()) document.fonts.delete(font);
	tab.state.svgImages.clear();
	tab.state.svgFonts.clear();
	tab.state.svgFontLoads.clear();
	tab.state.pageCache.clear();
	tab.state.pageInFlight.clear();
	tab.state.editorViews.clear();
	tab.resources.views.clear();
	if (tab.resources.url) URL.revokeObjectURL(tab.resources.url);
	tab.resources.url = null;
	tab.settings.views.clear();
	tab.settings.drafts.clear();
	tab.invoice.views.clear();
	tab.invoice.drafts.clear();
	if (nativeHost) await nativeHost.release(tab.state.ofdBytes);
	tab.state.ofdBytes = null;
	tab.state.doc = null;
	tab.localFonts = [];
	tab.view = null;
	tab.fontBinding = null;
	tab.attachmentEdit = null;
}

function updateWorkspaceDirty() {
	if (state.dirty || workspace.tabs.some(tab => tab.state.dirty)) window.addEventListener("beforeunload", warnUnsaved);
	else window.removeEventListener("beforeunload", warnUnsaved);
	renderDocumentTabs();
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
	updateWorkspaceDirty();
}

function requestConfirmation(message, tab = documentTab()) {
	const sequence = tab?.state.openSeq, scope = tab && documentPanelScope(tab.state);
	const current = () => !tab || isForegroundDocument(tab) && sequence === tab.state.openSeq && scope === documentPanelScope(tab.state);
	const task = confirmationTask.then(async () => {
		if (!current()) return false;
		el.confirmMessage.textContent = message;
		el.confirmPanel.returnValue = "cancel";
		const choice = new Promise(resolve => el.confirmPanel.addEventListener("close", () => resolve(el.confirmPanel.returnValue), { once: true }));
		documentDialogs.showApplication(el.confirmPanel);
		return await choice === "confirm" && current();
	});
	confirmationTask = task.then(() => {}, () => {});
	return task;
}

async function requestCloseAction(title, message, save, tab = documentTab()) {
	const setup = () => {
		el.closeTitle.textContent = title;
		el.closeMessage.textContent = message;
		const saveButton = el.closePanel.querySelector('[value="save"]');
		saveButton.hidden = !save;
		if (save) saveButton.textContent = save;
		el.closePanel.querySelector('[value="discard"]').textContent = save ? "放弃" : "停止并关闭";
		el.closePanel.returnValue = "cancel";
		el.closePanel.inert = false;
	};
	if (documentDialogs && !workspace.closing) return documentDialogs.request(el.closePanel, tab, setup);
	setup();
	const choice = new Promise(resolve => el.closePanel.addEventListener("close", () => resolve(el.closePanel.returnValue), { once: true }));
	if (documentDialogs) documentDialogs.showApplication(el.closePanel);
	else el.closePanel.showModal();
	try {
		return await choice;
	} finally {
		el.closeTitle.textContent = "关闭文档";
		el.closeMessage.textContent = "文档尚未保存";
		el.closePanel.querySelector('[value="save"]').hidden = false;
		el.closePanel.querySelector('[value="discard"]').textContent = "放弃";
		el.closePanel.inert = document.body.hasAttribute("data-busy");
	}
}

async function confirmDocumentClose(tab = documentTab()) {
	const state = tab.state;
	if (state.dirty || canvasEditor.input || canvasEditor.nudge || canvasEditor.nudgeCommit || canvasEditor.crop) {
		const action = await requestCloseAction("关闭文档", "文档尚未保存", state.saveHandle ? "保存" : "另存");
		if (tab.closed || action === "cancel") return false;
		if (action === "save") {
			if (!isForegroundDocument(tab) && !await activateDocumentTab(tab)) return false;
			if (!await exportFile(true, null, "ofd", false)) return false;
			if (state.dirty) return false;
		}
	}
	return !tab.closed && !state.saving;
}

function closeDocument() {
	return closeFile(documentTab(), true);
}

function closeTab(tab) {
	return closeFile(tab, false);
}

async function closeFile(tab = documentTab(), keepTab = false) {
	if (keepTab && workspace.closeDefault) return;
	if (tab.closed || tab.closing || workspace.closing) return;
	if (!keepTab && nativeHost && workspace.tabs.length === 1) return closeWorkspace();
	if (!tab.state.doc && !tab.opening && !tab.error && (keepTab || workspace.tabs.length === 1)) return;
	tab.closing = true;
	if (keepTab) {
		workspace.closeDefault = tab;
		queueMicrotask(() => { if (workspace.closeDefault === tab) workspace.closeDefault = null; });
	}
	try {
		cancelDocumentPreparation(tab);
		if (tab.state.saving && tab.saveCompletion) await tab.saveCompletion;
		if (tab.closed) return;
		if (tab.opening || tab.state.exporting) {
			const action = await requestCloseAction("关闭文档", "文档正在处理，是否取消并关闭？", null, tab);
			if (action !== "discard" || tab.closed) return;
		}
		if (tab.state.dirty || tab === workspace.active && (canvasEditor.input || canvasEditor.crop || canvasEditor.nudge)) {
			if (tab !== workspace.active && !await activateDocumentTab(tab)) return;
			if (!await confirmDocumentClose(tab) || tab.closed || workspace.closing) return;
		}
		while (workspace.switching) await nextFrame();
		const selected = workspace.active === tab;
		workspace.switching = true;
		const focused = el.documentTitleBar.contains(document.activeElement);
		try {
			const index = workspace.tabs.indexOf(tab);
			if (selected) detachDocumentView();
			if (keepTab) {
				const empty = createDocumentTab();
				empty.id = tab.id;
				workspace.tabs[index] = empty;
			} else workspace.tabs.splice(index, 1);
			if (!workspace.tabs.length) workspace.tabs.push(createDocumentTab());
			if (selected) attachDocumentTab(workspace.tabs[Math.min(index, workspace.tabs.length - 1)]);
			workspace.switching = false;
			await releaseDocumentTab(tab);
			if (selected && workspace.active !== tab) await mountDocumentTab(documentTab());
			updateWorkspaceDirty();
			if (selected && focused) el.documentTabs.querySelector('[data-selected="true"]')?.focus({preventScroll: true});
			return true;
		} finally {
			workspace.switching = false;
		}
	} finally {
		tab.closing = false;
		if (workspace.closeDefault === tab) workspace.closeDefault = null;
	}
}

function setEditorInfo(doc) {
	state.editorInfo = { revision: doc.revision, canUndo: doc.canUndo, canRedo: doc.canRedo,
		pageCapabilities: doc.pageCapabilities, editWarnings: doc.editWarnings || [] };
	setDirty(state.packageDirty || state.encryptionDirty || doc.revision !== state.savedRevision);
}

async function openVersionManagement() {
	if (!state.doc || !state.ready || state.exporting || document.body.hasAttribute("data-busy")) return;
	state.versionManagement = { id: state.doc.documentVersion || "", entries: null, loading: false, failed: false };
	el.versionList.replaceChildren();
	el.versionName.value = "";
	el.versionPanel.showModal();
	await refreshVersionManagement(true);
}

async function refreshVersionManagement(focus = false, tab = documentTab()) {
	const state = tab.state, manager = state.versionManagement;
	if (!manager || manager.loading || tab.closed || isForegroundDocument(tab) && !el.versionPanel.open) return;
	const scope = documentPanelScope(state), openSeq = state.openSeq, view = tab.view;
	const current = () => scope === documentPanelScope(state) && (openSeq === state.openSeq || view !== tab.view);
	manager.loading = true;
	manager.failed = false;
	manager.status = "正在读取版本";
	if (isForegroundDocument(tab)) {
		el.versionStatus.textContent = manager.status;
		updateVersionControls();
	}
	try {
		const entries = await callTabWASM(tab, "ofdgoDocumentVersions");
		if (tab.closed || tab.state !== state || state.versionManagement !== manager || !current()) return;
		manager.entries = entries;
		manager.scope = scope;
		manager.status = "";
		manager.renderPending = true;
		if (!entries.some(entry => entry.id === manager.id)) manager.id = state.doc.documentVersion || "";
	} catch (err) {
		if (tab.closed || tab.state !== state || state.versionManagement !== manager || !current()) return;
		manager.scope = scope;
		manager.failed = true;
		manager.status = err.message;
		manager.renderPending = true;
	} finally {
		manager.loading = false;
		if (!tab.closed && tab.state === state && state.versionManagement === manager && isForegroundDocument(tab) && el.versionPanel.open) {
			if (!current()) await refreshVersionManagement(focus, tab);
			else renderVersionManagement(focus);
		}
	}
}

function renderVersionManagement(focus = false) {
	const manager = state.versionManagement;
	if (!manager) return;
	el.versionStatus.textContent = manager.status || "";
	if (manager.entries) {
		el.versionList.replaceChildren(...manager.entries.map(entry => {
			const button = document.createElement("button");
			button.type = "button";
			button.className = "document-entry";
			button.setAttribute("data-current", String(entry.id === (state.doc.documentVersion || "")));
			button.setAttribute("data-selected", String(entry.id === manager.id));
			button.tabIndex = entry.id === manager.id ? 0 : -1;
			const name = document.createElement("span"), marker = document.createElement("span");
			name.className = "document-name";
			name.textContent = entry.id ? entry.name || `版本 ${entry.index}` : "主文档";
			marker.className = "document-pages";
			marker.textContent = entry.default ? "默认" : "";
			button.title = name.textContent;
			button.append(name, marker);
			button.addEventListener("click", () => selectManagedVersion(entry.id));
			return button;
		}));
		const index = manager.entries.findIndex(entry => entry.id === manager.id);
		el.versionName.value = manager.entries[index]?.name || "";
		if (focus) el.versionList.children[index]?.focus();
	}
	manager.renderPending = false;
	updateVersionControls();
}

function selectManagedVersion(id, focus = false) {
	const manager = state.versionManagement;
	if (!manager || manager.loading || document.body.hasAttribute("data-busy")) return;
	const index = manager.entries?.findIndex(entry => entry.id === id) ?? -1;
	if (index < 0) return;
	manager.id = id;
	el.versionName.value = manager.entries[index].name;
	Array.from(el.versionList.children).forEach((button, position) => {
		button.setAttribute("data-selected", String(position === index));
		button.tabIndex = position === index ? 0 : -1;
	});
	updateVersionControls();
	if (focus) el.versionList.children[index]?.focus();
}

async function changeVersion(action) {
	const tab = documentTab(), state = tab.state;
	if (state.saving) return;
	const manager = state.versionManagement;
	if (!state.ready || state.exporting || !state.editing && action !== "select" || !manager?.entries || manager.loading || manager.failed || document.body.hasAttribute("data-busy")) return;
	const entry = manager.entries.find(entry => entry.id === manager.id);
	if (!entry) return;
	if (!state.editing) {
		if (await switchReadingVersion(entry.id) && !tab.closed && state.versionManagement === manager) {
			manager.id = state.doc.documentVersion || "";
			await refreshVersionManagement(true, tab);
		}
		return;
	}
	if (action === "delete" && (!entry.id || !await requestConfirmation(`删除版本“${entry.name || entry.index}”？`))) return;
	el.versionStatus.textContent = "正在处理版本";
	if (await changeDocument("ofdgoChangeVersion", null, action, entry.id, el.versionName.value.trim())) {
		if (tab.closed || state.versionManagement !== manager) return;
		if (action === "add" || action === "select") manager.id = state.doc.documentVersion || "";
		await refreshVersionManagement(true, tab);
	}
}

async function switchReadingVersion(id) {
	const tab = documentTab(), state = tab.state;
	if (state.saving) return;
	if (!state.doc || state.editing || !state.ready || state.exporting || document.body.hasAttribute("data-busy")) return;
	if (id === (state.doc.documentVersion || "")) return true;
	const dirty = state.dirty;
	const originalDocument = state.doc;
	const view = tab.view;
	const before = state.editorInfo ? editorLocation() : null, revision = state.editorInfo?.revision;
	let openSeq = state.openSeq;
	tab.editTask = true;
	setBusy(true, "正在切换版本", null, "", tab);
	try {
		const doc = await callTabWASM(tab, "ofdgoSelectVersion", id);
		if (tab.closed || tab.state !== state || state.doc !== originalDocument || openSeq !== state.openSeq && view === tab.view) return;
		openSeq = state.openSeq = workspace.sequence = Math.max(workspace.sequence, state.openSeq) + 1;
		state.objectClipboardToken = state.styleClipboard = null;
		delete state.textDefaults.fontChoice;
		if (state.editorInfo) {
			if (!dirty) state.savedRevision = doc.revision;
			state.editorInfo = {revision: doc.revision, canUndo: doc.canUndo, canRedo: doc.canRedo,
				pageCapabilities: doc.pageCapabilities, editWarnings: doc.editWarnings || []};
			state.dirty = state.packageDirty || state.encryptionDirty || doc.revision !== state.savedRevision;
		}
		state.doc = doc;
		if (!isForegroundDocument(tab)) {
			state.pageCache.clear();
			state.pageIndex = 0;
			tab.view = null;
			await refreshDocumentTab(tab);
			return true;
		}
		canvasEditor.clear();
		canvasEditor.setTool("");
		updateTextFonts(null, true);
		if (state.editorInfo) {
			if (!dirty) state.savedRevision = doc.revision;
			setEditorInfo(doc);
		}
		await openDocument({ doc, openSeq, skipAutoFonts: true, resetScroll: true, clearSelection: true,
			fitMode: state.fitMode, scale: state.scale, activateActions: true });
		if (!isForegroundDocument(tab) || openSeq !== state.openSeq) return true;
		if (before) {
			rememberEditorView(before, revision);
			const view = state.editorViews.get(doc.revision);
			if (view) view.reading = true;
		}
		return true;
	} catch (error) {
		if (!tab.closed && tab.state === state) {
			if (state.versionManagement) { state.versionManagement.status = error.message; state.versionManagement.renderPending = true; }
			if (isForegroundDocument(tab)) el.versionStatus.textContent = error.message;
		}
	} finally {
		tab.editTask = false;
		setBusy(false, "", 0, "", tab);
	}
}

function updateVersionControls() {
	const manager = state.versionManagement, busy = document.body.hasAttribute("data-busy");
	const disabled = !state.doc || !state.ready || state.exporting || state.saving || busy;
	const title = state.editing ? "版本管理" : "版本浏览";
	el.versionManageButton.hidden = !state.editing && !(state.doc?.versionCount > 0);
	el.versionTitle.textContent = title;
	el.versionManageButton.disabled = disabled;
	el.versionNotice.textContent = state.editing ? state.editorInfo?.editWarnings?.join(" · ") || "" : "";
	el.versionNotice.hidden = !el.versionNotice.textContent;
	el.versionNameRow.hidden = !state.editing;
	el.versionActions.classList.toggle("version-readonly", !state.editing);
	for (const button of [el.versionAdd, el.versionRename, el.versionDelete, el.versionDefault]) button.hidden = !state.editing;
	const entry = manager?.entries?.find(entry => entry.id === manager.id);
	const unavailable = disabled || !entry || Boolean(manager?.loading || manager?.failed);
	el.versionList.inert = busy || Boolean(manager?.loading);
	el.versionName.disabled = el.versionAdd.disabled = unavailable || !state.editing;
	el.versionRename.disabled = unavailable || !state.editing || !entry?.id || el.versionName.value.trim() === entry.name;
	el.versionDelete.disabled = unavailable || !state.editing || !entry?.id || manager.entries.length < 2;
	el.versionDefault.disabled = unavailable || !state.editing || Boolean(entry?.default);
	el.versionSelect.disabled = unavailable || entry?.id === (state.doc?.documentVersion || "");
	el.versionExport.disabled = unavailable || !entry?.id;
}

async function exportManagedVersion() {
	const tab = documentTab(), state = tab.state;
	const manager = state.versionManagement;
	const entry = manager?.entries?.find(item => item.id === manager.id);
	if (!entry?.id || !state.ready || state.exporting || manager.loading || manager.failed || document.body.hasAttribute("data-busy")) return;
	if (state.doc.signatureCount && !await requestConfirmation("版本副本可能使原签名失效，是否继续？")) return;
	const originalDocument = state.doc, wasmSeq = state.wasmSeq;
	const name = `${baseFileName()}_${entry.name || `版本${entry.index}`}`.replace(/[\\/:*?"<>|\u0000-\u001f]/g, "_") + ".ofd";
	state.exporting = true;
	setBusy(true, "正在另存版本", null, "", tab);
	manager.status = el.versionStatus.textContent = "正在另存版本";
	try {
		const file = nativeHost || window.showSaveFilePicker ? await saveFilePicker({suggestedName: name, types: [{description: "OFD", accept: {"application/ofd": [".ofd"]}}]}) : null;
		if (tab.closed || tab.state !== state || state.doc !== originalDocument || wasmSeq !== state.wasmSeq) return;
		const result = await callTabWASM(tab, "ofdgoExportVersion", entry.id, file);
		if (tab.closed || tab.state !== state || wasmSeq !== state.wasmSeq) return;
		if (result.blob) {
			if (isForegroundDocument(tab)) downloadBytes(result.blob, result.mime, name);
			else tab.versionDownload = {blob: result.blob, mime: result.mime, name};
		}
		manager.status = `已另存（${formatBytes(result.size)}）`;
		manager.renderPending = true;
		if (isForegroundDocument(tab) && state.versionManagement === manager) el.versionStatus.textContent = manager.status;
	} catch (error) {
		if (!tab.closed && tab.state === state && wasmSeq === state.wasmSeq) {
			manager.status = error.name === "AbortError" ? "另存已取消" : error.message;
			manager.renderPending = true;
			if (isForegroundDocument(tab) && state.versionManagement === manager) el.versionStatus.textContent = manager.status;
		}
	} finally {
		state.exporting = false;
		state.exportRequestID = 0;
		setBusy(false, "", 0, "", tab);
		if (isForegroundDocument(tab)) { el.cancelExportButton.hidden = true; updateControls(); }
	}
}

function managedDocumentIndex() {
	return state.documentManagement?.index ?? state.doc?.documentIndex ?? 0;
}

function documentEntryKey(entry) {
	return JSON.stringify([entry?.root, entry?.id]);
}

function documentContentKey(state) {
	const index = state.doc?.documentIndex || 0;
	return JSON.stringify([state.wasmSeq, index, documentEntryKey(state.doc?.documents?.[index]), state.doc?.documentVersion || ""]);
}

async function openDocumentManagement() {
	if (!state.editing || !state.ready || state.exporting || document.body.hasAttribute("data-busy")) return;
	const index = state.doc.documentIndex || 0;
	state.documentManagement = { index, key: documentEntryKey(state.doc.documents[index]), entries: null, loading: false, failed: false };
	renderDocumentManagement();
	el.documentPanel.showModal();
	await refreshDocumentManagement(true);
}

async function refreshDocumentManagement(focus = false, tab = documentTab()) {
	const state = tab.state, manager = state.documentManagement;
	if (!manager || manager.loading || tab.closed || isForegroundDocument(tab) && !el.documentPanel.open) return;
	const scope = documentPanelScope(state), openSeq = state.openSeq, view = tab.view;
	const current = () => scope === documentPanelScope(state) && (openSeq === state.openSeq || view !== tab.view);
	manager.loading = true;
	manager.failed = false;
	manager.status = "正在读取文档";
	if (isForegroundDocument(tab)) {
		el.documentStatus.textContent = manager.status;
		updateDocumentControls();
	}
	try {
		const entries = await callTabWASM(tab, "ofdgoPackageDocuments");
		if (tab.closed || tab.state !== state || state.documentManagement !== manager || !current()) return;
		const index = entries.findIndex(entry => documentEntryKey(entry) === manager.key);
		manager.index = index < 0 ? Math.min(manager.index, entries.length - 1) : index;
		manager.entries = entries;
		manager.key = documentEntryKey(entries[manager.index]);
		manager.scope = scope;
		manager.status = "";
		manager.renderPending = true;
	} catch (error) {
		if (!tab.closed && tab.state === state && state.documentManagement === manager && current()) {
			manager.scope = scope;
			manager.failed = true;
			manager.status = error.message;
			manager.renderPending = true;
		}
	} finally {
		manager.loading = false;
		if (!tab.closed && tab.state === state && state.documentManagement === manager && isForegroundDocument(tab) && el.documentPanel.open) {
			if (!current()) await refreshDocumentManagement(focus, tab);
			else {
				renderDocumentManagement();
				if (focus) el.documentList.children[manager.index]?.focus();
			}
		}
	}
}

function renderDocumentManagement() {
	const manager = state.documentManagement;
	if (!manager) return;
	el.documentStatus.textContent = manager.status || "";
	manager.renderPending = false;
	const entries = manager.entries || state.doc.documents;
	el.documentList.replaceChildren(...entries.map(entry => {
		const button = document.createElement("button");
		button.type = "button";
		button.className = "document-entry";
		button.setAttribute("data-selected", String(entry.index === manager.index));
		button.tabIndex = entry.index === manager.index ? 0 : -1;
		const name = document.createElement("span");
		name.className = "document-name";
		name.textContent = `${entry.index + 1} · ${entry.title || "未命名"}`;
		button.title = name.textContent;
		const pages = document.createElement("span");
		pages.className = "document-pages";
		const count = entry.pageCount ?? (entry.index === state.doc.documentIndex ? state.doc.pageCount : null);
		pages.textContent = count === null ? "-" : `${count}页`;
		button.append(name, pages);
		button.addEventListener("click", () => selectManagedDocument(entry.index));
		return button;
	}));
	updateDocumentControls();
}

function selectManagedDocument(index, focus = false) {
	const manager = state.documentManagement;
	if (!manager || manager.loading || document.body.hasAttribute("data-busy") || index < 0 || index >= state.doc.documents.length) return;
	manager.index = index;
	manager.key = documentEntryKey(state.doc.documents[index]);
	for (const [i, button] of [...el.documentList.children].entries()) {
		button.setAttribute("data-selected", String(i === index));
		button.tabIndex = i === index ? 0 : -1;
	}
	updateDocumentControls();
	if (focus) el.documentList.children[index]?.focus();
}

async function switchDocument(index) {
	const tab = documentTab(), state = tab.state;
	if (state.saving) return;
	if (!state.doc || document.body.hasAttribute("data-busy") || state.exporting || formDialogOpen()) return;
	const previous = state.doc.documentIndex || 0;
	if (index === previous) return;
	if (!await canvasEditor.commitNudge() || !await canvasEditor.commitText() || !await canvasEditor.commitCrop()) {
		if (isForegroundDocument(tab)) el.documentSelect.value = String(previous);
		return;
	}
	if (!isForegroundDocument(tab) || document.body.hasAttribute("data-busy")) return;
	let openSeq = state.openSeq;
	const originalDocument = state.doc;
	tab.editTask = true;
	setBusy(true, "正在切换文档", null, "", tab);
	try {
		const doc = await callTabWASM(tab, "ofdgoSelectDocument", index);
		if (tab.closed || tab.state !== state || state.doc !== originalDocument) return;
		openSeq = state.openSeq = workspace.sequence = Math.max(workspace.sequence, state.openSeq) + 1;
		const source = state.ofdBytes;
		state.packageDirty ||= state.dirty;
		state.savedRevision = doc.revision;
		state.editorViews.clear();
		state.objectClipboardToken = state.styleClipboard = null;
		delete state.textDefaults.fontChoice;
		state.ofdBytes = null;
		state.editorInfo = {revision: doc.revision, canUndo: doc.canUndo, canRedo: doc.canRedo,
			pageCapabilities: doc.pageCapabilities, editWarnings: doc.editWarnings || []};
		state.dirty = state.packageDirty || state.encryptionDirty || doc.revision !== state.savedRevision;
		state.doc = doc;
		if (!isForegroundDocument(tab)) {
			state.pageCache.clear();
			state.pageIndex = 0;
			tab.view = null;
			await refreshDocumentTab(tab);
			if (nativeHost) await nativeHost.release(source);
			return;
		}
		canvasEditor.clear();
		canvasEditor.setTool("");
		updateTextFonts(null, true);
		setEditorInfo(doc);
		await openDocument({ doc, openSeq, skipAutoFonts: true, resetScroll: true, clearSelection: true, fitMode: state.fitMode, scale: state.scale, activateActions: true });
		if (nativeHost) await nativeHost.release(source);
	} catch (error) {
		if (!tab.closed && tab.state === state && state.doc === originalDocument) {
			tab.status = error.message;
			if (state.documentManagement) { state.documentManagement.status = error.message; state.documentManagement.renderPending = true; }
		}
		if (isForegroundDocument(tab) && openSeq === state.openSeq) {
			el.documentSelect.value = String(previous);
			showError(error, false);
		}
	} finally {
		tab.editTask = false;
		setBusy(false, "", 0, "", tab);
	}
}

function editorLocation() {
	const pending = canvasEditor.pendingSelection;
	const index = pending?.index ?? canvasEditor.selected?.index ?? state.pageIndex;
	return { page: state.doc.pages[index].id, ids: pending?.ids || canvasEditor.items().map(item => item.id) };
}

function rememberEditorView(before, revision) {
	for (const key of state.editorViews.keys()) {
		if (key > revision) state.editorViews.delete(key);
	}
	state.editorViews.set(state.editorInfo.revision, { before, after: editorLocation() });
	if (state.editorViews.size > 100) state.editorViews.delete(state.editorViews.keys().next().value);
}

function pageCan(capability, index = state.pageIndex) {
	const bit = { insert: 1, copy: 2, delete: 4, move: 8, resize: 16 }[capability];
	return Boolean(state.editorInfo?.pageCapabilities?.[index] & bit);
}

async function toggleEditor() {
	const tab = documentTab(), state = tab.state;
	const callWASM = (name, ...args) => callTabWASM(tab, name, ...args);
	if (!state.doc || !state.ready || document.body.hasAttribute("data-busy") || formDialogOpen()) return;
	if (state.editorInfo) {
		const wasmSeq = state.wasmSeq;
		const current = () => !tab.closed && tab.state === state && isForegroundDocument(tab) && wasmSeq === state.wasmSeq;
		if (!await canvasEditor.commitText() || !current()) return;
		if (!await canvasEditor.commitCrop() || !current()) return;
		const anchor = pageView.scaleAnchor(0);
		state.editing = !state.editing;
		renderDocumentFonts();
		canvasEditor.clear();
		canvasEditor.setTool("");
		if (state.editing) {
			releaseMediaPlayers();
			state.selectObjects = true;
			pageView.setPan(false);
		}
		updateControls();
		renderOutlines(false);
		if (state.renderMode !== "svg") {
			try {
				await refreshPageDisplay(anchor);
			} catch (err) {
				state.editing = !state.editing;
				if (!isForegroundDocument(tab)) return;
				renderDocumentFonts();
				updateControls();
				showError(err, false);
			}
			return;
		}
		pageView.renderPageList();
		pageView.applyFit(false);
		pageView.restoreScaleAnchor(anchor);
		pageView.updatePageListCurrent(true);
		return;
	}
	const { pageIndex, fitMode, scale } = state;
	const anchor = pageView.scaleAnchor(0);
	let openSeq = state.openSeq;
	const source = state.ofdBytes;
	const originalDocument = state.doc;
	let adopted = false;
	setBusy(true, "正在准备编辑", null, "正在准备编辑", tab);
	try {
		const doc = await callWASM("ofdgoEditDocument", pageIndex);
		if (tab.closed || state.doc !== originalDocument) return;
		adopted = true;
		state.savedRevision = doc.revision;
		state.editorViews.clear();
		if (!isForegroundDocument(tab) || openSeq !== state.openSeq) {
			state.doc = doc;
			state.editorInfo = {revision: doc.revision, canUndo: doc.canUndo, canRedo: doc.canRedo,
				pageCapabilities: doc.pageCapabilities, editWarnings: doc.editWarnings || []};
			state.editing = true;
			state.ofdBytes = null;
			state.selectObjects = true;
			state.conversionWarnings = [];
			state.objectClipboardToken = state.styleClipboard = null;
			state.pageCache.clear();
			await refreshDocumentTab(tab);
			return;
		}
		openSeq = advanceDocumentSequence();
		setEditorInfo(doc);
		state.editing = true;
		releaseMediaPlayers();
		state.selectObjects = true;
		state.ofdBytes = null;
		state.conversionWarnings = [];
		state.objectClipboardToken = null;
		state.styleClipboard = null;
		canvasEditor.clear();
		pageView.setPan(false);
		await openDocument({ doc, openSeq, skipAutoFonts: true, pageIndex, fitMode, scale,
			keepPreview: true, preserveThumbnails: true, previewAnchor: anchor });
	} catch (err) {
		if (isForegroundDocument(tab) && openSeq === state.openSeq) showError(err, false);
	} finally {
		if (nativeHost && adopted) await nativeHost.release(source);
		setBusy(false, "", 0, "", tab);
		if (isForegroundDocument(tab) && openSeq === state.openSeq) {
			updateControls();
			if (state.editing) pageView.updatePageListCurrent(true);
		}
	}
}

async function confirmTextReflow(item) {
	return canEditObject(item, "reflow") && (canEditObject(item, "layoutKnown")
		|| await requestConfirmation("将重新排版原文，是否继续？"));
}

function openCreatePanel(insert = false, packageDocument = false) {
	if (workspace.closing || state.saving && packageDocument) return;
	if (document.body.hasAttribute("data-busy")) {
		if (!insert && !packageDocument) return openEmptyDocumentTab().then(() => { if (!documentTab().busy) openCreatePanel(); });
		return;
	}
	el.createForm.reset();
	releaseCreateImages(documentTab());
	state.createImageQueue = { entries: [], selected: null, next: 0, order: "name-asc", drag: null, observer: null, task: Promise.resolve() };
	state.createInsert = insert === true;
	state.createPackage = packageDocument === true;
	el.createTitle.textContent = state.createInsert ? "插入页面" : state.createPackage ? "新增文档" : "新建文档";
	state.createDocumentAt = state.createPackage ? managedDocumentIndex() + 1 : null;
	el.createMode.value = state.createInsert ? "images" : "blank";
	el.createSize.value = state.createInsert ? "image" : "a4";
	el.createSubmit.textContent = state.createInsert ? "插入" : state.createPackage ? "新增" : "新建";
	el.createCancel.disabled = false;
	updateCreateOptions();
	renderCreateImages();
	el.createStatus.textContent = "";
	el.createPanel.showModal();
}

function updateCreateOptions() {
	const images = el.createMode.value === "images", busy = Boolean(state.imageImporting);
	const toggle = (id, visible) => { document.getElementById(id).hidden = !visible; };
	toggle("createModeRow", !state.createInsert && !state.createPackage);
	el.createName.closest(".form-row").hidden = Boolean(state.createInsert);
	el.createName.disabled = busy || Boolean(state.createInsert);
	el.createMode.disabled = busy;
	for (const [id, input] of [["createImagesRow", el.createImages], ["createMarginRow", el.createMargin]]) {
		toggle(id, images);
		input.disabled = busy || !images;
	}
	el.createSize.disabled = busy;
	el.createSize.querySelector('[value="image"]').hidden = !images;
	el.createSize.querySelector('[value="image"]').disabled = !images;
	el.createImageQueue.hidden = !images || !state.createImageQueue?.entries.length;
	el.createPanel.classList.toggle("create-images", images);
	el.createImageAdd.disabled = busy;
	for (const input of el.createImageOrder.querySelectorAll("input")) input.disabled = busy;
	const dimensions = !images || el.createSize.value !== "image";
	toggle("createDimensions", dimensions);
	toggle("createDirectionRow", dimensions);
	el.createWidth.disabled = el.createHeight.disabled = busy || !dimensions;
	toggle("createPositionRow", Boolean(state.createInsert));
	el.createPosition.disabled = busy || !state.createInsert;
	el.createSubmit.disabled = busy || images && !state.createImageQueue?.entries.length;
	updateCreateDirection();
	updateCreateImageSelection();
}

function applyCreatePaper() {
	const paper = createPaperSizes[el.createSize.value];
	if (paper) {
		const [width, height] = el.createLandscape.checked ? [...paper].reverse() : paper;
		el.createWidth.value = width;
		el.createHeight.value = height;
	}
	updateCreateOptions();
}

function updateCreateDirection(matchPaper = false) {
	const width = Number(el.createWidth.value), height = Number(el.createHeight.value);
	const valid = width > 0 && height > 0;
	el.createPortrait.checked = valid && width < height;
	el.createLandscape.checked = valid && width > height;
	el.createPortrait.disabled = el.createLandscape.disabled = !valid || width === height || el.createWidth.disabled;
	if (matchPaper) el.createSize.value = Object.entries(createPaperSizes).find(([, [w, h]]) => Math.min(width, height) === w && Math.max(width, height) === h)?.[0] || "custom";
	updateCreateImagePreviews();
}

function releaseCreateImages(tab) {
	const queue = tab.state.createImageQueue;
	if (!queue) return;
	queue.observer?.disconnect();
	for (const entry of queue.entries) if (entry.url) URL.revokeObjectURL(entry.url);
	queue.entries.length = 0;
	tab.state.createImageQueue = null;
}

function addCreateImages() {
	const { tab, queue } = createImagePicker || { tab: documentTab(), queue: state.createImageQueue };
	createImagePicker = null;
	const files = [...el.createImages.files];
	el.createImages.value = "";
	if (tab.closed || !queue || tab.state.createImageQueue !== queue) return;
	for (const file of files) queue.entries.push({ id: queue.next++, file, url: null, previewed: false });
	sortCreateImages(queue);
	if (isForegroundDocument(tab)) updateCreateOptions();
}

function sortCreateImages(queue = state.createImageQueue) {
	const order = queue.order;
	if (order !== "custom") queue.entries.sort((a, b) => {
		const value = order.startsWith("name") ? a.file.name.localeCompare(b.file.name, undefined, { numeric: true }) : order.startsWith("time") ? a.file.lastModified - b.file.lastModified : a.id - b.id;
		return (order.endsWith("desc") ? -value : value) || a.id - b.id;
	});
	if (queue === state.createImageQueue) renderCreateImages();
}

function updateCreateImageSelection() {
	const queue = state.createImageQueue;
	for (const row of el.createImageList.children) {
		row.dataset.selected = String(Number(row.dataset.imageId) === queue?.selected);
		row.querySelector(".create-image-select").tabIndex = row.dataset.selected === "true" ? 0 : -1;
		for (const button of row.querySelectorAll("button")) button.disabled = state.imageImporting;
		row.draggable = !state.imageImporting;
	}
}

function focusCreateImage() {
	(el.createImageList.querySelector('[data-selected="true"] .create-image-select') || el.createImageAdd).focus();
}

function reorderCreateImage(from, to) {
	const queue = state.createImageQueue;
	if (from < 0 || to < 0 || to >= queue.entries.length) return;
	if (from !== to) {
		queue.entries.splice(to, 0, queue.entries.splice(from, 1)[0]);
		queue.order = "custom";
	}
	renderCreateImages();
}

function moveCreateImage(direction) {
	const queue = state.createImageQueue, index = queue.entries.findIndex(entry => entry.id === queue.selected);
	reorderCreateImage(index, index + direction);
}

function removeCreateImage() {
	const queue = state.createImageQueue, index = queue.entries.findIndex(entry => entry.id === queue.selected);
	if (index < 0) return;
	const [entry] = queue.entries.splice(index, 1);
	if (entry.url) URL.revokeObjectURL(entry.url);
	queue.selected = queue.entries[Math.min(index, queue.entries.length - 1)]?.id ?? null;
	renderCreateImages();
	updateCreateOptions();
}

function renderCreateImages() {
	const tab = documentTab(), queue = state.createImageQueue;
	queue.observer?.disconnect();
	const top = el.createImageList.scrollTop;
	const focused = el.createImageList.contains(document.activeElement);
	el.createImageList.replaceChildren();
	for (const input of el.createImageOrder.querySelectorAll("input")) input.checked = input.value === queue.order;
	if (!queue.entries.some(entry => entry.id === queue.selected)) queue.selected = queue.entries[0]?.id ?? null;
	queue.observer = new IntersectionObserver(items => {
		for (const item of items) if (item.isIntersecting && isForegroundDocument(tab)) {
			queue.observer.unobserve(item.target);
			const entry = queue.entries.find(entry => entry.id === Number(item.target.dataset.imageId));
			queue.task = queue.task.then(() => loadCreateImagePreview(tab, queue, entry));
		}
	}, { root: el.createImageList, rootMargin: "64px" });
	for (const [index, entry] of queue.entries.entries()) {
		const row = document.createElement("div"), select = document.createElement("button"), remove = document.createElement("button"), number = document.createElement("span"), preview = document.createElement("span"), details = document.createElement("span");
		row.className = "create-image-entry";
		row.dataset.imageId = entry.id;
		select.type = remove.type = "button";
		select.className = "create-image-select";
		select.title = entry.file.name;
		remove.className = "icon-button remove-button";
		remove.title = "移除";
		number.textContent = String(index + 1);
		preview.className = "create-image-preview";
		preview.textContent = entry.file.name.split(".").at(-1).toUpperCase();
		details.className = "create-image-details";
		const name = document.createElement("span");
		name.textContent = entry.file.name;
		details.append(name);
		select.append(number, preview, details);
		row.append(select, remove);
		el.createImageList.append(row);
		if (entry.url) showCreateImagePreview(entry, row);
		else if (!entry.previewed) queue.observer.observe(row);
	}
	el.createImageList.scrollTop = top;
	updateCreateImageSelection();
	if (focused) focusCreateImage();
}

function showCreateImagePreview(entry, row) {
	const paper = document.createElement("span"), image = document.createElement("img");
	paper.className = "create-image-paper";
	image.src = entry.url;
	image.draggable = false;
	paper.append(image);
	row.querySelector(".create-image-preview").replaceChildren(paper);
	layoutCreateImagePreview(entry, row);
}

function updateCreateImagePreviews() {
	const queue = state.createImageQueue;
	if (!queue) return;
	const entries = new Map(queue.entries.map(entry => [entry.id, entry]));
	for (const row of el.createImageList.children) {
		const entry = entries.get(Number(row.dataset.imageId));
		if (entry?.url && row.querySelector(".create-image-paper")) layoutCreateImagePreview(entry, row);
	}
}

function layoutCreateImagePreview(entry, row) {
	const paper = row.querySelector(".create-image-paper"), image = paper.firstElementChild;
	const margin = Number(el.createMargin.value), follow = el.createSize.value === "image";
	const width = follow ? entry.width + 2 * margin : Number(el.createWidth.value);
	const height = follow ? entry.height + 2 * margin : Number(el.createHeight.value);
	const valid = Number.isFinite(width + height + margin) && margin >= 0 && width > 2 * margin && height > 2 * margin;
	paper.hidden = !valid;
	if (!valid) return;
	const scale = 46 / Math.max(width, height);
	const fit = follow ? 1 : Math.min((width - 2 * margin) / entry.width, (height - 2 * margin) / entry.height);
	paper.style.width = `${width * scale}px`;
	paper.style.height = `${height * scale}px`;
	image.style.width = `${entry.width * fit * scale}px`;
	image.style.height = `${entry.height * fit * scale}px`;
}

async function loadCreateImagePreview(tab, queue, entry) {
	if (!entry || entry.previewed || tab.state.createImageQueue !== queue || !queue.entries.includes(entry)) return;
	entry.previewed = true;
	let bitmap;
	try {
		const source = entry.file instanceof Blob ? entry.file : new Blob([await entry.file.arrayBuffer()]);
		if (tab.state.createImageQueue !== queue || !queue.entries.includes(entry)) return;
		bitmap = await createImageBitmap(source, { resizeWidth: 64 });
		if (tab.closed || tab.state.createImageQueue !== queue || !queue.entries.includes(entry)) return;
		const size = await callTabWASM(tab, "ofdgoImagePageSize", new Uint8Array(await source.arrayBuffer()));
		if (tab.state.createImageQueue !== queue || !queue.entries.includes(entry)) return;
		entry.width = size.width;
		entry.height = size.height;
		const canvas = document.createElement("canvas"), scale = Math.min(64 / bitmap.width, 64 / bitmap.height, 1);
		canvas.width = Math.max(1, Math.round(bitmap.width * scale));
		canvas.height = Math.max(1, Math.round(bitmap.height * scale));
		canvas.getContext("2d").drawImage(bitmap, 0, 0, canvas.width, canvas.height);
		const blob = await new Promise(resolve => canvas.toBlob(resolve));
		if (tab.state.createImageQueue !== queue || !queue.entries.includes(entry)) return;
		entry.url = URL.createObjectURL(blob);
		if (isForegroundDocument(tab)) {
			const current = el.createImageList.querySelector(`[data-image-id="${entry.id}"]`);
			if (current) showCreateImagePreview(entry, current);
		}
	} catch {} finally { bitmap?.close(); }
}

function imagePageRequest() {
	const files = state.createImageQueue.entries.map(entry => entry.file);
	const size = el.createSize.value;
	return [files, {
		Width: size === "image" ? 0 : Number(el.createWidth.value),
		Height: size === "image" ? 0 : Number(el.createHeight.value),
		Margin: Number(el.createMargin.value),
	}];
}

async function createDocument(event) {
	event.preventDefault();
	const owner = documentTab();
	if (state.createPackage) {
		if (await changeDocument("ofdgoChangeDocument", null, "add", el.createName.value.trim() || "未命名",
			Number(el.createWidth.value), Number(el.createHeight.value), state.createDocumentAt)) {
			releaseCreateImages(owner);
			documentDialogs.closeFor(owner, el.createPanel);
		}
		return;
	}
	if (state.createInsert) {
		const at = { before: state.pageIndex, after: state.pageIndex + 1, first: 0, last: state.doc.pageCount }[el.createPosition.value];
		if (await changeDocument("ofdgoImportImages", null, ...imagePageRequest(), at, "", state.renderAnnotations)) {
			releaseCreateImages(owner);
			documentDialogs.closeFor(owner, el.createPanel);
		}
		return;
	}
	if (workspace.closing) {
		return;
	}
	const title = el.createName.value.trim() || "未命名";
	const images = el.createMode.value === "images";
	const imageRequest = images ? imagePageRequest() : null;
	const width = Number(el.createWidth.value), height = Number(el.createHeight.value);
	const previous = documentTab();
	const tab = createDocumentTab();
	copyApplicationPreferences(tab.state);
	tab.state.imageImporting = images;
	tab.state.fileName = `${title.replace(/[\\/:*?"<>|]+/g, "_").replace(/\.ofd$/i, "")}.ofd`;
	const request = {controller: new AbortController()};
	tab.opening = request;
	el.createPanel.close();
	const replace = !workspace.switching && !previous.state.doc && !previous.opening && !previous.state.dirty && !previous.error;
	const index = workspace.tabs.indexOf(previous);
	if (replace) workspace.tabs[index] = tab;
	else workspace.tabs.push(tab);
	if (!await activateDocumentTab(tab) && replace && workspace.active !== tab) {
		workspace.tabs[index] = previous;
		await releaseDocumentTab(tab);
		renderDocumentTabs();
		return;
	}
	if (replace) await releaseDocumentTab(previous);
	let accepted = false;
	setBusy(true, "正在新建文档", null, "正在新建文档", tab);
	try {
		await ensureTabEngine(tab);
		if (tab.closed || tab.opening !== request) {
			return;
		}
		const doc = images
			? await callTabWASM(tab, "ofdgoImportImages", ...imageRequest, -1, title, tab.state.renderAnnotations)
			: await callTabWASM(tab, "ofdgoCreateDocument", title, width, height, tab.state.renderAnnotations);
		if (tab.closed || tab.opening !== request) {
			return;
		}
		tab.state.editing = true;
		tab.state.editorInfo = {revision: doc.revision, canUndo: doc.canUndo, canRedo: doc.canRedo,
			pageCapabilities: doc.pageCapabilities, editWarnings: doc.editWarnings || []};
		tab.state.dirty = true;
		tab.state.doc = doc;
		tab.opening = null;
		accepted = true;
		if (isForegroundDocument(tab)) {
			await openDocument({doc, skipAutoFonts: true, openSeq: tab.state.openSeq});
			if (isForegroundDocument(tab)) { updateTextFonts(null, true); updateControls(); }
		}
		updateWorkspaceDirty();
	} catch (err) {
		if (!tab.closed) {
			tab.error = err.message;
			tab.status = err.message;
			if (isForegroundDocument(tab)) setEmpty(err.message);
		}
	} finally {
		if (tab.opening === request) tab.opening = null;
		tab.state.imageImporting = false;
		if (!accepted && tab.worker) await markWASMExited(tab.state.wasmSeq, null, tab);
		if (!tab.closed) setBusy(false, "", 0, "", tab);
		if (isForegroundDocument(tab)) updateControls();
		renderDocumentTabs();
	}
}

function editAnnotationDetails(item) {
	state.annotationEdit = item;
	el.annotationNoteTitle.textContent = "编辑注解";
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
	el.objectPickerTitle.textContent = "选择对象";
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
	el.annotationCreateTitle.textContent = kind === "replace" ? "替换文字"
		: `${item ? "编辑" : "添加"}${{ note: "批注", watermark: "水印", stamp: "印章", link: "链接" }[kind]}`;
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

async function editCanvasObject(item, point = null) {
	const tab = documentTab(), state = tab.state;
	const callWASM = (name, ...args) => callTabWASM(tab, name, ...args);
	if (!state.editing || document.body.hasAttribute("data-busy") || canvasEditor.input) return;
	if (item.items) {
		if (!item.items.every(member => member.type === "TextObject" && !member.richText)) return;
		const openSeq = state.openSeq;
		tab.editTask = true;
		setBusy(true, "", 0, "", tab);
		try {
			const ids = item.items.map(member => member.id);
			const data = await callWASM("ofdgoTextSelection", item.index, ids);
			if (!isForegroundDocument(tab) || openSeq !== state.openSeq) return;
			const faces = new Map();
			for (const font of data.fonts) {
				faces.set(font.id, await new FontFace(`ofdgo-selection-${crypto.randomUUID()}`, font.bytes, fontFaceStyle(font.bytes)).load());
			}
			if (!isForegroundDocument(tab) || openSeq !== state.openSeq || canvasEditor.selected !== item || !item.node.isConnected) return;
			const face = faces.get(data.item.font);
			if (!face) throw new Error("字体不可用");
			Object.assign(item, data.item, { type: "TextObject", selectionIDs: ids, id: ids });
			canvasEditor.editText(item, face, data.source, null, faces);
		} catch (err) {
			if (isForegroundDocument(tab) && openSeq === state.openSeq) showError(err, false);
		} finally {
			tab.editTask = false;
			setBusy(false, "", 0, "", tab);
		}
		return;
	}
	if (!item.richText && canEditObject(item, "children")) return selectCompositeContent(item, point);
	if (item.type === "Annotation") {
		editAnnotationDetails(item);
		return;
	}
	if (item.type === "TextObject") {
		if (!canEditObject(item, "textContent") && !canEditObject(item, "rewriteText")) return;
	} else if (!canEditObject(item, item.type === "ImageObject" ? "replaceImage" : "update")) return;
	if (item.type === "PathObject") {
		el.shapeWidth.focus();
		return;
	}
	if (item.type === "ImageObject") {
		return openInsertPanel(item);
	}
	const openSeq = state.openSeq;
	tab.editTask = true;
	setBusy(true, "", 0, "", tab);
	try {
		const data = await callWASM("ofdgoEditorFont", item.font, !canEditObject(item, "layoutKnown"));
		if (!isForegroundDocument(tab) || openSeq !== state.openSeq) return;
		const face = new FontFace(`ofdgo-edit-${item.font}`, data.bytes, fontFaceStyle(data.bytes));
		await face.load();
		if (!isForegroundDocument(tab) || openSeq !== state.openSeq) return;
		const rich = await loadRichText(item, face, tab, data.bytes);
		if (!isForegroundDocument(tab) || openSeq !== state.openSeq) return;
		const source = rich ? null : await callWASM("ofdgoPreviewText", item.index, item.id, item.text);
		if (isForegroundDocument(tab) && openSeq === state.openSeq && item.node.isConnected) {
			canvasEditor.editText(item, face, source, rich);
			canvasEditor.input.fontResource = data.bytes;
		}
	} catch (err) {
		if (isForegroundDocument(tab) && openSeq === state.openSeq) {
			showError(err, false);
		}
	} finally {
		tab.editTask = false;
		setBusy(false, "", 0, "", tab);
	}
}

function objectParent(item) {
	const ids = item?.items?.map(member => member.id) || (Array.isArray(item?.id) ? item.id : item ? [item.id] : []);
	const parents = ids.map(id => id.includes("/") ? id.slice(0, id.lastIndexOf("/")) : "");
	return parents.every(parent => parent === parents[0]) ? parents[0] || "" : null;
}

function mountEditorObjects(index, page, surface) {
	const objects = [];
	const append = members => {
		for (const item of members) {
			if (!state.renderAnnotations && item.type === "Annotation") continue;
			objects.push(item);
			const children = page.members?.get(item.id);
			if (children) append(children);
		}
	};
	append(page.objects);
	canvasEditor.mount(index, { ...page, objects }, surface);
}

function changeAnnotations(item, action, ...args) {
	return changeDocument("ofdgoChangeAnnotations", { ...item, id: item.items ? item.id : [item.id] }, action, ...args);
}

async function loadEditorMembers(tab, page, index) {
	const state = tab.state, openSeq = state.openSeq;
	const pending = canvasEditor.pendingSelection;
	const ids = pending?.index === index ? pending.ids || [] : canvasEditor.items().filter(item => item.index === index).map(item => item.id);
	page.members ||= new Map();
	const paths = new Set();
	for (const id of ids) {
		const parts = id.split("/");
		for (let i = 1; i < parts.length; i++) paths.add(parts.slice(0, i).join("/"));
	}
	for (const key of [...paths].sort((a, b) => a.split("/").length - b.split("/").length)) {
		if (page.members.has(key)) continue;
		const parent = key.includes("/") ? page.members.get(key.slice(0, key.lastIndexOf("/"))) : page.objects;
		if (!parent?.some(item => item.id === key && canEditObject(item, "children"))) continue;
		const members = await callTabWASM(tab, "ofdgoCompositeObjects", index, key);
		if (!isForegroundDocument(tab) || openSeq !== state.openSeq) return;
		page.members.set(key, members);
	}
}

async function selectCompositeContent(item, point) {
	const tab = documentTab(), state = tab.state, openSeq = state.openSeq;
	const page = state.pageCache.get(item.index), surface = item.surface;
	if (!page) return;
	const original = canvasEditor.selected;
	tab.editTask = true;
	setBusy(true, "", 0, "", tab);
	let target = item;
	try {
		page.members ||= new Map();
		const rect = point && surface.getBoundingClientRect();
		const tolerance = point ? 3 * page.width / (state.rotation % 180 ? rect.height : rect.width) : 0;
		while (canEditObject(target, "children") && !target.richText) {
			const key = target.id;
			let members = page.members.get(key);
			if (!members) {
				members = await callTabWASM(tab, "ofdgoCompositeObjects", item.index, key);
				if (!isForegroundDocument(tab) || openSeq !== state.openSeq || canvasEditor.selected !== original || !surface.isConnected) return;
				page.members.set(key, members);
			}
			const next = point ? [...members].reverse().find(member => member.type === "TextObject" || canEditObject(member, "children")
				? point.x >= member.x && point.x <= member.x + member.width && point.y >= member.y && point.y <= member.y + member.height
				: canvasEditor.contains(member, point, tolerance)) : members[0];
			if (!next) break;
			target = next;
		}
		if (target.id === item.id) return;
		mountEditorObjects(item.index, page, surface);
		target = canvasEditor.pageItems(item.index).find(member => member.id === target.id);
		canvasEditor.select(target);
		canvasEditor.focus();
	} catch (err) {
		if (isForegroundDocument(tab) && openSeq === state.openSeq) showError(err, false);
		return;
	} finally {
		tab.editTask = false;
		setBusy(false, "", 0, "", tab);
	}
	if (target && target.id !== item.id && !canEditObject(target, "children") && isForegroundDocument(tab) && openSeq === state.openSeq) return editCanvasObject(target);
}

async function startImageCrop(item) {
	const tab = documentTab(), state = tab.state;
	if (item.scoped) {
		canvasEditor.startCrop(item, { svg: item.surface.querySelector(".ofd-svg").cloneNode(true), urls: [] });
		return;
	}
	const openSeq = state.openSeq;
	const urls = [];
	let mounted = false;
	tab.editTask = true;
	setBusy(true, "", 0, "", tab);
	try {
		const page = await callTabWASM(tab, "ofdgoPreviewImage", item.index, item.id);
		if (!isForegroundDocument(tab) || openSeq !== state.openSeq || !item.surface.isConnected) return;
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
		if (!isForegroundDocument(tab) || openSeq !== state.openSeq || canvasEditor.selected !== item) return;
		const svg = pageView.parseSVG(page.svg, `crop${openSeq}-${item.id}`, images);
		svg.classList.add("ofd-svg");
		canvasEditor.startCrop(item, { svg, urls });
		mounted = true;
	} catch (err) {
		if (isForegroundDocument(tab) && openSeq === state.openSeq) showError(err, false);
	} finally {
		if (!mounted) urls.forEach(url => URL.revokeObjectURL(url));
		tab.editTask = false;
		setBusy(false, "", 0, "", tab);
	}
}

function openInsertPanel(item = null) {
	if (!state.editing || document.body.hasAttribute("data-busy")) {
		return;
	}
	el.insertForm.reset();
	state.insertObject = item;
	el.insertTitle.textContent = item ? "替换图片" : "添加图片";
	el.insertFitRow.hidden = !item || Boolean(item.scoped);
	el.insertSubmit.textContent = item ? "替换" : "添加";
	el.insertStatus.textContent = "";
	el.insertPanel.showModal();
	el.insertImage.focus();
}

function currentTextStyle() {
	const text = selectedText(canvasEditor.selected) || state.textDefaults;
	if (canvasEditor.input?.rich) {
		const rich = canvasEditor.input.rich, layout = richTextLayout(rich.model, ...rich.selection());
		return { ...text, ...rich.state(), ...textFaceStyle(rich.selectedRuns().map(run => run.format)), ...layout, paragraphHeight: layout.lineHeight };
	}
	if (text.richText) {
		const formats = text.richText.runs.map(run => effectiveTextFormat(run.format));
		const layout = richTextLayout(text.richText);
		return { ...text, ...layout, paragraphHeight: layout.lineHeight, ...Object.fromEntries(["font", "size", "color", "bold", "italic", "underline", "strikeout", "shadow", "background"]
			.map(key => [key, formats.every(format => format[key] === formats[0]?.[key]) ? formats[0]?.[key] : undefined])) };
	}
	if (text.items) return { ...text, ...textFaceStyle(text.items.flatMap(item => textFontRuns(item).map(run => run.format))) };
	return text === state.textDefaults ? text : effectiveTextFormat(canvasEditor.input?.style ? { ...text, ...canvasEditor.input.style } : text);
}

function documentFontChoice(id) {
	const font = state.doc?.fonts?.find(font => font.id === id);
	return { ...(font?.status !== "fallback" && font?.status !== "missing" ? font?.matchedFace : null), id: `embedded:${id}`, name: font?.fontName || id, embedded: true };
}

function textFontChoice(id) {
	const rich = canvasEditor.input?.rich;
	const choice = rich?.choices.get(id);
	return choice ? { ...choice, richKey: id } : id ? documentFontChoice(id) : state.textDefaults.fontChoice;
}

function effectiveTextFormat(format) {
	const font = textFontChoice(format.font), face = canvasEditor.input?.rich?.faces.get(format.font);
	return { ...format, bold: Boolean(format.bold || font?.bold || Number(face?.weight) >= 600), italic: Boolean(format.italic || font?.italic || face?.style === "italic") };
}

function textFaceStyle(formats) {
	formats = formats.map(effectiveTextFormat);
	return Object.fromEntries(["bold", "italic"].map(key => [key, formats.every(format => format[key] === formats[0]?.[key]) ? formats[0]?.[key] : undefined]));
}

function textFontRuns(item) {
	return (item?.items || [item || state.textDefaults]).flatMap(item => item.richText?.runs || [{ text: item.text || "", format: { font: item.font || "", bold: Boolean(item.bold), italic: Boolean(item.italic) } }]);
}

function textFontSource(font) {
	return font.embedded ? "embedded" : font.file || ["stored", "upload"].includes(font.source) ? "upload" : "system";
}

function textFontIdentity(font) {
	return `${textFontSource(font)}:${font.embedded ? font.richKey || font.id?.slice(9) : font.id ?? font.postscriptName}`;
}

async function groupTextFonts(fonts, tab = documentTab()) {
	if (!fonts.some(font => font.familyKey)) return fonts.map((_, index) => [index]);
	const { groups } = await callTabWASM(tab, "ofdgoFontMenu", JSON.stringify({ faces: fonts.map(font => ({ familyKey: font.familyKey, bold: font.bold, italic: font.italic })), sources: fonts.map(textFontSource) }));
	return groups;
}

function sharedTextFont(formats) {
	const choices = formats.map(format => textFontChoice(format.font));
	const first = choices[0];
	return first && choices.every(font => font && (font.familyKey && font.familyKey === first.familyKey && textFontSource(font) === textFontSource(first) || textFontIdentity(font) === textFontIdentity(first))) ? formats[0].font : undefined;
}

async function loadRichText(item, firstFace, tab = documentTab(), firstData = null, reflow = false) {
	const wasmSeq = tab.state.wasmSeq;
	const callWASM = (name, ...args) => {
		if (tab.closed || wasmSeq !== tab.state.wasmSeq) throw new Error("文档已更改");
		return callTabWASM(tab, name, ...args);
	};
	const prepared = item.richText ? { model: item.richText, frame: item.textFrame } : await callWASM("ofdgoGetRichText", item.index, item.id, reflow);
	if (prepared.unsupported) return null;
	const model = prepared.model;
	const remaining = firstData ? { ...model, runs: model.runs.filter(run => run.format.font !== item.font) } : model;
	const fontData = remaining.runs.length ? await callWASM("ofdgoRichTextFonts", JSON.stringify(remaining)) : [];
	const resources = new Map(fontData.map(font => [font.key, font.data]));
	if (firstData) resources.set(item.font, firstData);
	const faces = new Map(), pending = [...new Set(model.runs.map(run => run.format.font))];
	await Promise.all(Array.from({ length: Math.min(4, pending.length) }, async () => {
		while (pending.length) {
			const id = pending.shift(), data = id === item.font && firstFace ? null : resources.get(id);
			const face = data ? await new FontFace(`ofdgo-rich-${crypto.randomUUID()}`, data, fontFaceStyle(data)).load() : firstFace;
			if (tab.closed || wasmSeq !== tab.state.wasmSeq) throw new Error("文档已更改");
			faces.set(id, face);
		}
	}));
	return { model, faces, resources, frame: prepared.frame, reflow };
}

async function ensureRichInput(reflow = false) {
	let editing = canvasEditor.input;
	if (!editing) {
		const item = canvasEditor.selected;
		if (!item || item.items || item.type !== "TextObject") return false;
		await editCanvasObject(item);
		editing = canvasEditor.input;
	}
	if (!editing) return false;
	if (editing.item.selectionIDs) return false;
	if (editing.rich) return true;
	if (editing.composing || editing.saving) return false;
	const tab = documentTab();
	let rich = await loadRichText(editing.item, editing.face, tab, editing.fontResource, reflow);
	if (canvasEditor.input !== editing || !isForegroundDocument(tab)) return false;
	if (!rich && !reflow) {
		if (!await confirmTextReflow(editing.item)) return false;
		rich = await loadRichText(editing.item, editing.face, tab, editing.fontResource, true);
	}
	if (canvasEditor.input !== editing || !isForegroundDocument(tab)) return false;
	if (!rich) throw new Error("文字暂不可重排");
	return Boolean(editing.rich) || canvasEditor.promoteText(rich);
}

async function reflowCanvasText() {
	const editing = canvasEditor.input;
	if (document.body.hasAttribute("data-busy") || !editing?.source || editing.composing || editing.saving || !canEditObject(editing.item, "reflow")) return;
	if (!await requestConfirmation("将重新排版原文，是否继续？")) return;
	editing.reflow = true;
	canvasEditor.recordSourceText();
	await canvasEditor.previewSourceText();
	updateObjectControls(canvasEditor.selected, true);
}

function bindEditorInput(input, change) {
	editorInputs.set(input, { input, change, pending: null, task: null, handled: false });
	const remember = () => {
		const control = editorInputs.get(input);
		control.pending = { value: input.value, tab: documentTab(), content: documentContentKey(state), editing: canvasEditor.input, selected: canvasEditor.selected };
		control.handled = false;
	};
	input.addEventListener("input", remember);
	input.addEventListener("change", () => {
		const control = editorInputs.get(input);
		let task;
		if (!control.handled) {
			if (!control.pending) remember();
			task = flushEditorInput(control);
		}
		control.handled = false;
		return task;
	});
}

function flushEditorInput(control) {
	if (control.task || !control.pending) return control.task;
	control.task = (async () => {
		while (control.pending) {
			const pending = control.pending;
			control.pending = null;
			control.handled = true;
			if (!isForegroundDocument(pending.tab) || pending.content !== documentContentKey(state)
				|| pending.editing !== canvasEditor.input || pending.selected !== canvasEditor.selected) continue;
			control.input.value = pending.value;
			await control.change(pending.value);
		}
	})().finally(() => { control.task = null; });
	return control.task;
}

function flushEditorInputs(event) {
	const tasks = [...editorInputs.values()].filter(control => !event || !control.input.contains(event.target))
		.map(flushEditorInput).concat(fontPicker.prepareCommit(event), canvasEditor.input?.fontTask).filter(Boolean);
	return tasks.length ? Promise.all(tasks) : null;
}

async function changeRangeFormat(key, value) {
	if (!state.ready || document.body.hasAttribute("data-busy") || canvasEditor.input?.composing) return;
	if (key === "bold" || key === "italic") return changeTextFace(null, key, value ?? currentTextStyle()[key] !== true);
	const tab = documentTab(), editing = canvasEditor.input;
	try {
		if (!canvasEditor.selected && !canvasEditor.input) {
			state.textDefaults[key] = value ?? (key === "background" ? state.textDefaults.background ? "" : textBackground.value : !state.textDefaults[key]);
			updateObjectControls(null); return;
		}
		const item = canvasEditor.selected;
		if (!canvasEditor.input && item) {
			const reflow = !canEditObject(item, "layoutKnown");
			if (reflow && !await confirmTextReflow(item)) return;
			const current = currentTextStyle()[key];
			const patch = { [key]: value ?? (key === "background" ? current ? "" : textBackground.value : current !== true) };
			await changeDocument("ofdgoFormatText", { ...item, id: (item.items || [item]).map(member => member.id) }, JSON.stringify(patch), reflow);
			return;
		}
		if (!await ensureRichInput()) return;
		const rich = canvasEditor.input.rich, current = rich.state()[key];
		rich.apply({ [key]: value ?? (key === "background" ? current ? "" : textBackground.value : current !== true) });
	} catch (error) {
		if (isForegroundDocument(tab) && (!editing || canvasEditor.input === editing)) showError(error, false);
	}
}

async function validateRichText(item, model, width, fonts = []) {
	const tab = documentTab(), state = tab.state;
	const callWASM = (name, ...args) => callTabWASM(tab, name, ...args);
	const openSeq = state.openSeq, wasmSeq = state.wasmSeq, editing = canvasEditor.input;
	model = structuredClone(model);
	const prepared = [...fonts], attempted = new Set();
	while (true) {
		try {
			await callWASM("ofdgoPreviewRichText", item.index, item.draft ? "" : item.id, JSON.stringify(model), width, prepared);
			if (tab.closed || wasmSeq !== state.wasmSeq || openSeq !== state.openSeq) throw new Error("文档已更改");
			return prepared;
		} catch (error) {
			const id = error.missingGlyphs?.fontID;
			if (!id || attempted.has(id) || prepared.some(font => font.key === id) || tab.closed || wasmSeq !== state.wasmSeq || openSeq !== state.openSeq) throw error;
			attempted.add(id);
			const original = (await callWASM("ofdgoEditorFont", id)).bytes;
			const [source] = await callWASM("ofdgoFontFaces", original);
			if (!source?.postscriptName || !source.familyKey) throw error;
			await fontManager.loadFaces(data => callWASM("ofdgoFontFaces", data));
			const candidates = fontManager.editorFonts().filter(font => !font.disabled && font.postscriptName === source.postscriptName && font.familyKey === source.familyKey);
			let recovered;
			for (const choice of candidates) {
				try {
					const data = await readTextFont(choice, tab);
					const [face] = await callWASM("ofdgoFontFaces", data);
					if (face?.postscriptName !== source.postscriptName || face.familyKey !== source.familyKey) continue;
					await callWASM("ofdgoCheckTextFont", data, model.runs.filter(run => run.format.font === id).map(run => run.text).join(""));
					recovered = { key: id, data, choice };
					break;
				} catch {}
			}
			if (!recovered || tab.closed || wasmSeq !== state.wasmSeq || openSeq !== state.openSeq) throw error;
			prepared.push({ key: id, data: recovered.data });
			if (isForegroundDocument(tab) && editing?.rich && canvasEditor.input === editing) {
				const rich = editing.rich;
				const face = await new FontFace(`ofdgo-recovered-${crypto.randomUUID()}`, recovered.data, fontFaceStyle(recovered.data)).load();
				if (tab.closed || !isForegroundDocument(tab) || openSeq !== state.openSeq || canvasEditor.input !== editing) throw error;
				rich.imports.set(id, recovered.data);
				rich.choices.set(id, recovered.choice);
				const previous = rich.faces.get(id);
				rich.faces.set(id, face);
				if (previous && ![...rich.faces.values()].includes(previous)) document.fonts.delete(previous);
				document.fonts.add(face);
				if (!editing.composing && !editing.saving) { rich.selection(); rich.render(); updateObjectControls(canvasEditor.selected); }
			}
		}
	}
}

function saveRichText(item, model, fonts = [], box = item) {
	const boundary = { x: box.x, y: box.y, width: box.width, height: box.height };
	if (item.richReflow) boundary.reflow = true;
	if (box.frameWidth !== undefined) Object.assign(boundary, { frameWidth: box.frameWidth, frameOffset: box.frameOffset || 0 });
	return changeDocument("ofdgoSaveRichText", item.draft ? null : item,
		...(item.draft ? [item.index, ""] : []), JSON.stringify(model), boundary,
		() => validateRichText(item, model, box.frameWidth ?? item.textFrame?.width ?? item.width, fonts));
}

function textIndents(item) {
	return [item.leftIndent || 0, item.rightIndent || 0, item.firstLineIndent || 0];
}

async function toggleTextTool() {
	if (!state.editing || !pageCan("insert") || document.body.hasAttribute("data-busy")) return;
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
		bold: Boolean(style.bold), italic: Boolean(style.italic), underline: Boolean(style.underline), strikeout: Boolean(style.strikeout), shadow: Boolean(style.shadow), background: style.background || "",
		align: style.align || "left", paragraphHeight: style.paragraphHeight || 0, letterSpacing: style.letterSpacing || 0,
		leftIndent: style.leftIndent || 0, rightIndent: style.rightIndent || 0, firstLineIndent: style.firstLineIndent || 0,
		fontChoice: font && !font.disabled ? font : state.textDefaults.fontChoice };
	state.selectObjects = true;
	pageView.setPan(false);
	canvasEditor.setTool("text");
	canvasEditor.focus();
}

async function beginCanvasText(index, box, surface, page) {
	const tab = documentTab(), state = tab.state;
	const font = state.textDefaults.fontChoice || state.textFonts[Number(fontPicker.value)];
	if (!font || font.disabled) {
		setStatus("尚未添加字体");
		el.textFontAdd.focus();
		return;
	}
	const openSeq = state.openSeq;
	tab.editTask = true;
	setBusy(true, "", 0, "", tab);
	try {
		const data = await readTextFont(font, tab);
		if (!isForegroundDocument(tab) || openSeq !== state.openSeq) return;
		const face = new FontFace("ofdgo-draft", data, fontFaceStyle(data));
		await face.load();
		if (!isForegroundDocument(tab) || openSeq !== state.openSeq || !surface.isConnected) return;
		const node = document.createElement("div");
		const item = { ...state.textDefaults, ...box, wrap: Boolean(box.wrap),
			index, page, surface, node, text: "", draft: true, fontData: data };
		const model = { ...richTextModel(item), autoWidth: !box.wrap };
		const key = font.embedded ? font.id.slice(9) : `temp-font-${crypto.randomUUID()}`;
		const embedded = font.embedded && state.doc?.fonts?.some(font => font.id === key && font.embedded);
		model.runs[0].format.font = key;
		canvasEditor.editText(item, face, null, { model, faces: new Map([[key, face]]), fonts: embedded ? [] : [{ key, data }], resources: new Map([[key, data]]) });
	} catch (err) {
		if (isForegroundDocument(tab) && openSeq === state.openSeq) showError(err, false);
	} finally {
		tab.editTask = false;
		setBusy(false, "", 0, "", tab);
	}
}

function editorFonts(item) {
	const fonts = fontManager.editorFonts();
	const embedded = new Map();
	for (const member of item?.items || (item ? [item] : [])) {
		for (const id of member.richText ? member.richText.runs.map(run => run.format.font) : [member.font]) {
			if (!id || embedded.has(id)) continue;
			const font = state.doc?.fonts?.find(font => font.id === id);
			embedded.set(id, { ...documentFontChoice(id), name: font?.fontName || (id === member.font ? member.fontName : "") || id,
				embedded: true, disabled: !canEditObject(member, "textContent") });
		}
	}
	return [...embedded.values(), ...fonts];
}

async function loadEditorFonts(requestPermission = true, tab = documentTab(), waitPending = false) {
	const target = tab.state;
	const tasks = waitPending ? [target.fontCatalogLoading, target.fontFacesLoading].filter(Boolean) : [];
	if (!target.fontCatalogLoading && fontManager.canReadLocal() && !fontManager.catalogLoaded && fontManager.permission !== "denied"
		&& (requestPermission || fontManager.permission === "granted")) {
		target.fontCatalogLoading = (async () => {
			try {
				await fontManager.queryLocal();
				refreshEditorFonts();
				await loadEditorFonts(false, tab);
			} catch (err) {
				if (requestPermission && isForegroundDocument(tab)) setStatus(err?.name === "NotAllowedError" ? "系统字体未授权" : String(err.message || err));
			} finally {
				target.fontCatalogLoading = false;
			}
		})();
		tasks.push(target.fontCatalogLoading);
	}
	if (!target.fontFacesLoading && (fontManager.userFonts.some(font => font.enabled && !font.faces) || fontManager.catalog.some(font => font.blob && !font.menuFamily && !font.metadataFailed))) {
		const wasmSeq = target.wasmSeq;
		const current = () => !tab.closed && !target.wasmExited && target.wasmSeq === wasmSeq;
		target.fontFacesLoading = (async () => {
			try {
				await fontManager.loadFaces(data => callTabWASM(tab, "ofdgoFontFaces", data), current);
				refreshEditorFonts();
			} finally {
				target.fontFacesLoading = false;
				if (!current() && state.ready) loadEditorFonts(false);
			}
		})();
		tasks.push(target.fontFacesLoading);
	}
	await Promise.all(tasks);
}

async function initializeTextFont(tab) {
	const target = tab.state;
	if (target.textFontInitialized) return;
	if (target.textFontInitializing) return target.textFontInitializing;
	const wasmSeq = target.wasmSeq;
	const current = () => !tab.closed && !target.wasmExited && target.wasmSeq === wasmSeq;
	target.textFontInitializing = (async () => {
		await loadEditorFonts(false, tab, true);
		if (!current()) return;
		const font = await fontManager.defaultFont((faces, families) => callTabWASM(tab, "ofdgoFontPreferenceOrder", JSON.stringify({
			faces: faces.map(({ family, fullName, postscriptName, style, names }) => ({ family, fullName, postscriptName, style, names })), families
		})), async (font, text) => callTabWASM(tab, "ofdgoCheckTextFont", await readTextFont(font, tab), text), current);
		if (!current()) return;
		target.textDefaults.fontChoice = font || (fontManager.editorFonts().length ? null : undefined);
		target.textFontInitialized = true;
		if (isForegroundDocument(tab)) refreshEditorFonts();
	})();
	try { await target.textFontInitializing; }
	finally { target.textFontInitializing = null; }
}

function refreshEditorFonts() {
	updateTextFonts(canvasEditor.selected, true, true);
}

async function readTextFont(font, tab = documentTab()) {
	const wasmSeq = tab.state.wasmSeq;
	const callWASM = (name, ...args) => {
		if (tab.closed || wasmSeq !== tab.state.wasmSeq) throw new Error("文档已更改");
		return callTabWASM(tab, name, ...args);
	};
	const result = font.embedded ? await callWASM("ofdgoEditorFont", font.id.slice(9))
		: await callWASM("ofdgoFontFace", await fontManager.read(font.file || font), font.index || 0);
	if (tab.closed || wasmSeq !== tab.state.wasmSeq) throw new Error("文档已更改");
	return result.bytes;
}

function updateTextFonts(item, refresh = false, preserve = false) {
	item = selectedText(item);
	const rich = canvasEditor.input?.rich;
	if (rich) {
		const id = sharedTextFont(rich.selectedRuns().map(run => run.format));
		const key = `rich:${id ?? "mixed"}:${rich.faces.size}`;
		if (refresh || state.textFontID !== key) {
			if (!preserve && document.activeElement === el.textFont && fontPicker.open) return;
			state.textFontID = key;
			state.textFonts = editorFonts(null);
			for (const font of rich.faces.keys()) {
				const choice = rich.choices?.get(font);
				if (choice) state.textFonts = state.textFonts.filter(value => textFontIdentity(value) !== textFontIdentity(choice));
				state.textFonts.unshift({ ...(choice || { ...documentFontChoice(font),
					name: state.doc?.fonts?.find(value => value.id === font)?.fontName || (font === rich.editing.item.font ? rich.editing.item.fontName : "") || font,
					embedded: !rich.imports.has(font) }), richKey: font });
			}
			fontPicker.setFonts(state.textFonts, id === undefined ? null : state.textFonts.find(font => font.richKey === id), preserve);
			const placeholder = id === undefined ? "混合" : "";
			if (el.textFont.placeholder !== placeholder) el.textFont.placeholder = placeholder;
		}
		return;
	}
	const id = item?.type === "TextObject" && !item.draft ? sharedTextFont(textFontRuns(item).map(run => run.format)) : null;
	const key = item?.items ? `selection:${item.items.map(member => member.id).join(",")}:${id || ""}`
		: item?.richText ? `rich:${item.id}:${item.richText.runs.map(run => run.format.font).join(",")}` : id;
	if (refresh || state.textFontID !== key) {
		state.textFontID = key;
		state.textFonts = editorFonts(item?.type === "TextObject" && !item.draft ? item : null);
		const selected = canvasEditor.input?.fontChoice || (id ? state.textFonts[0] : state.textDefaults.fontChoice);
		if (selected && (canvasEditor.input || selected.embedded)
			&& !state.textFonts.some(font => (font.id || font.postscriptName) === (selected.id || selected.postscriptName))) {
			state.textFonts.unshift(selected);
		}
		const mixed = (item?.items || item?.richText) && !id;
		fontPicker.setFonts(state.textFonts, mixed ? null : selected || (state.textDefaults.fontChoice === null ? null : state.textFonts[0]), preserve);
		const placeholder = mixed ? "混合" : "";
		if (el.textFont.placeholder !== placeholder) el.textFont.placeholder = placeholder;
		if (!id && !item?.items && !item?.richText) state.textDefaults.fontChoice = state.textFonts[Number(fontPicker.value)] || (state.textDefaults.fontChoice === null ? null : undefined);
	}
}

function textFontStyleKey(format) {
	return JSON.stringify([format.font || "", Boolean(format.bold), Boolean(format.italic)]);
}

async function prepareTextFaces(tab, runs, font, key, value, rich) {
	const { openSeq, wasmSeq, doc } = tab.state;
	const current = () => !tab.closed && openSeq === tab.state.openSeq && wasmSeq === tab.state.wasmSeq && doc === tab.state.doc && (!rich || canvasEditor.input === rich.editing);
	const callWASM = (name, ...args) => {
		if (!current()) throw new Error("文档已更改");
		return callTabWASM(tab, name, ...args);
	};
	const choices = new Map(), data = new Map();
	const originals = new Map(runs.map(run => [run.format.font || "", textFontChoice(run.format.font)]));
	const add = font => { if (font && !font.disabled && !choices.has(textFontIdentity(font))) choices.set(textFontIdentity(font), { ...font }); };
	if (font) add(font);
	if (rich) for (const id of rich.faces.keys()) add({ ...textFontChoice(id), richKey: id });
	for (const original of originals.values()) add(original);
	for (const font of tab.state.doc?.fonts || []) if (font.embedded || font.status === "matched") add(documentFontChoice(font.id));
	for (const font of fontManager.editorFonts()) add(font);
	const read = async choice => {
		const id = textFontIdentity(choice);
		if (!data.has(id)) data.set(id, choice.richKey && (rich?.imports.get(choice.richKey) || rich?.resources.get(choice.richKey)) || await readTextFont(choice, tab));
		if (!current()) throw new Error("文档已更改");
		return data.get(id);
	};
	const candidates = [...choices.values()];
	for (const choice of candidates) {
		if (choice.menuFamily || ![...originals.values()].some(font => textFontIdentity(font || {}) === textFontIdentity(choice)) && (!font || textFontIdentity(choice) !== textFontIdentity(font))) continue;
		try {
			const [metadata] = await callWASM("ofdgoFontFaces", await read(choice));
			if (metadata) Object.assign(choice, metadata, { index: choice.index });
		} catch (error) {
			if (!current() || !font || textFontIdentity(choice) === textFontIdentity(font)) throw error;
		}
	}
	const groups = await groupTextFonts(candidates, tab), changes = new Map(), prepared = new Map();
	for (const run of runs) {
		const from = { font: run.format.font || "", bold: Boolean(run.format.bold), italic: Boolean(run.format.italic) };
		const original = choices.get(textFontIdentity(originals.get(from.font) || {}));
		const target = font ? choices.get(textFontIdentity(font)) : original;
		if (!target) throw new Error("字体不可用");
		const bold = key === "bold" ? value : Boolean(from.bold || original?.bold);
		const italic = key === "italic" ? value : Boolean(from.italic || original?.italic);
		const group = groups.find(group => group.includes(candidates.indexOf(target))).map(index => candidates[index]);
		let { index } = await callWASM("ofdgoFontMenu", JSON.stringify({ faces: group.map(({ bold, italic }) => ({ bold, italic })), bold, italic }));
		if (index < 0 && font) index = group.indexOf(target);
		if (index < 0) throw new Error("此字体没有对应款式");
		const selected = group[index], identity = textFontIdentity(selected);
		if (!prepared.has(identity)) prepared.set(identity, { choice: selected, key: selected.richKey || (selected.embedded ? selected.id.slice(9) : `temp-font-${crypto.randomUUID()}`), data: await read(selected), text: "" });
		const entry = prepared.get(identity);
		entry.text += run.text;
		changes.set(textFontStyleKey(from), { from, to: { font: entry.key,
			bold: !key && selected.bold && bold ? from.bold : Boolean(bold || selected.bold),
			italic: !key && selected.italic && italic ? from.italic : Boolean(italic || selected.italic) } });
	}
	for (const entry of prepared.values()) {
		try { await callWASM("ofdgoCheckTextFont", entry.data, entry.text); }
		catch (error) {
			if (!error.missingGlyphs || !entry.choice.embedded || !entry.choice.familyKey) throw error;
			let recovered = false;
			for (const choice of candidates.filter(choice => !choice.embedded && choice.familyKey === entry.choice.familyKey && choice.postscriptName === entry.choice.postscriptName)) {
				try {
					const bytes = await read(choice);
					await callWASM("ofdgoCheckTextFont", bytes, entry.text);
					entry.data = bytes;
					entry.choice = { ...choice, richKey: entry.key };
					recovered = true;
					break;
				} catch { if (!current()) throw error; }
			}
			if (!recovered) throw error;
		}
		if (!current()) throw new Error("文档已更改");
		if (rich) entry.face = await new FontFace(`ofdgo-edit-font-${crypto.randomUUID()}`, entry.data, fontFaceStyle(entry.data)).load();
	}
	return { changes: [...changes.values()], prepared: [...prepared.values()], fonts: [...prepared.values()].filter(entry => !entry.choice.embedded).map(({ key, data, text }) => ({ key, data, text })) };
}

function changeTextFont() {
	const font = state.textFonts[Number(fontPicker.value)];
	if (font && !font.disabled) return changeTextFace(font);
}

async function changeTextFace(font, key, value) {
	const tab = documentTab(), state = tab.state;
	const openSeq = state.openSeq;
	const item = selectedText(canvasEditor.selected);
	if (!state.ready || document.body.hasAttribute("data-busy")) return;
	const editing = canvasEditor.input;
	const current = () => isForegroundDocument(tab) && openSeq === state.openSeq && canvasEditor.input === editing;
	if (!editing && !item) {
		const source = font || state.textDefaults.fontChoice;
		const bold = key === "bold" ? value : Boolean(state.textDefaults.bold), italic = key === "italic" ? value : Boolean(state.textDefaults.italic);
		let selected = source;
		if (source?.familyKey && (key || bold || italic)) {
			const busy = {};
			tab.editTask = busy;
			setBusy(true, "", 0, "", tab);
			try {
				const available = source.embedded ? (state.doc?.fonts || []).filter(font => font.embedded || font.status === "matched").map(font => documentFontChoice(font.id)) : fontManager.editorFonts();
				const fonts = [source, ...available.filter(font => textFontIdentity(font) !== textFontIdentity(source))].filter(font => font.familyKey === source.familyKey && textFontSource(font) === textFontSource(source));
				const groups = await groupTextFonts(fonts, tab);
				if (!current()) return;
				const index = fonts.findIndex(choice => textFontIdentity(choice) === textFontIdentity(source));
				const faces = (groups.find(group => group.includes(index)) || []).map(index => fonts[index]);
				const result = await callTabWASM(tab, "ofdgoFontMenu", JSON.stringify({ faces: faces.map(({ bold, italic }) => ({ bold, italic })), bold, italic }));
				if (!current()) return;
				selected = faces[result.index] || (font ? source : null);
				if (!selected) throw new Error("此字体没有对应款式");
			} catch (error) {
				if (current()) showError(error, false);
				return;
			} finally {
				if (tab.editTask === busy) { tab.editTask = false; setBusy(false, "", 0, "", tab); }
			}
		}
		state.textDefaults.fontChoice = selected;
		state.textDefaults.bold = Boolean(bold || selected?.bold);
		state.textDefaults.italic = Boolean(italic || selected?.italic);
		updateTextFonts(null, true);
		updateObjectControls(null);
		return;
	}
	if (!editing && item?.type === "TextObject") {
		if (!key && font?.embedded && textFontRuns(item).every(run => run.format.font === font.id.slice(9))) return;
		const reflow = Boolean(key && !canEditObject(item, "layoutKnown"));
		if (!canEditObject(item, "replaceFont") || reflow && !await confirmTextReflow(item)) return;
		await changeDocument("ofdgoStyleTextFonts", { ...item, id: (item.items || [item]).map(member => member.id) }, async () => {
			const result = await prepareTextFaces(tab, textFontRuns(item), font, key, value);
			return { changes: JSON.stringify(result.changes), fonts: result.fonts, reflow };
		});
		if (isForegroundDocument(tab)) updateTextFonts(canvasEditor.selected, true);
		return;
	}
	if (editing.composing) return;
	const task = (async () => {
		try {
			if (!await ensureRichInput() || !current()) return;
			const rich = editing.rich;
			rich.remember();
			const runs = rich.selectedRuns(), before = rich.model.runs, caret = rich.selection().join();
			const busy = {};
			tab.editTask = busy;
			setBusy(true, "", 0, "", tab);
			try {
				const result = await prepareTextFaces(tab, runs, font, key, value, rich);
				if (!current() || rich.model.runs !== before || rich.selection().join() !== caret || editing.composing || editing.saving) return;
				for (const entry of result.prepared) {
					const previous = rich.faces.get(entry.key);
					rich.faces.set(entry.key, entry.face);
					if (previous && ![...rich.faces.values()].includes(previous)) document.fonts.delete(previous);
					if (!entry.choice.embedded) rich.imports.set(entry.key, entry.data);
					else rich.resources.set(entry.key, entry.data);
					rich.choices.set(entry.key, entry.choice);
					document.fonts.add(entry.face);
				}
				const mapping = new Map(result.changes.map(change => [textFontStyleKey(change.from), change.to]));
				rich.apply(format => ({ ...format, ...mapping.get(textFontStyleKey(format)) }));
				if (item?.draft && font && !font.richKey) state.textDefaults.fontChoice = font;
				updateTextFonts(canvasEditor.selected, true);
				updateObjectControls(canvasEditor.selected);
			} finally {
				if (tab.editTask === busy) { tab.editTask = false; setBusy(false, "", 0, "", tab); }
			}
		} catch (error) {
			if (current()) { updateTextFonts(canvasEditor.selected, true); showError(error, false); }
		}
	})();
	if (editing) editing.fontTask = task;
	try { await task; }
	finally { if (editing?.fontTask === task) editing.fontTask = null; }
}

async function insertObject(event) {
	event.preventDefault();
	const tab = documentTab(), state = tab.state;
	const callWASM = (name, ...args) => callTabWASM(tab, name, ...args);
	if (!state.editing || document.body.hasAttribute("data-busy") || el.insertSubmit.disabled) {
		return;
	}
	let openSeq = state.openSeq;
	const item = state.insertObject;
	const before = editorLocation(), revision = state.editorInfo.revision;
	const originalDocument = state.doc;
	const file = el.insertImage.files[0], fit = el.insertFit.value;
	const index = item?.index ?? state.pageIndex;
	const page = state.doc.pages[index];
	const x = Math.min(20, page.width / 10);
	const y = Math.min(20, page.height / 10);
	el.insertStatus.textContent = "";
	tab.editTask = true;
	setBusy(true, "", 0, "", tab);
	try {
		const data = new Uint8Array(await file.arrayBuffer());
		if (!isForegroundDocument(tab) || openSeq !== state.openSeq) {
			return;
		}
		const doc = item?.scoped
			? await callWASM("ofdgoChangeCompositeObjects", item.index, objectParent(item), [item.id], "image", data)
			: item ? await callWASM("ofdgoReplaceImage", item.index, item.id, data, fit)
			: await callWASM("ofdgoInsertImage", index, data, x, y, page.width - x * 2);
		if (tab.closed || state.doc !== originalDocument || state.editorInfo?.revision !== revision) return;
		if (!isForegroundDocument(tab) || openSeq !== state.openSeq) {
			state.doc = doc;
			state.editorInfo = {revision: doc.revision, canUndo: doc.canUndo, canRedo: doc.canRedo,
				pageCapabilities: doc.pageCapabilities, editWarnings: doc.editWarnings || []};
			state.dirty = state.packageDirty || state.encryptionDirty || doc.revision !== state.savedRevision;
			state.pageCache.clear();
			await refreshDocumentTab(tab);
			return;
		}
		el.insertPanel.close();
		if (!item && resourceElements.Panel.open) resourceElements.Panel.close();
		if (item && doc.revision === state.editorInfo?.revision) {
			canvasEditor.focus();
			return;
		}
		setEditorInfo(doc);
		canvasEditor.pendingSelection = item ? null : { index, ids: doc.selectedIDs };
		state.selectObjects = true;
		pageView.setPan(false);
		openSeq = advanceDocumentSequence();
		await refreshEditorPage(doc, index, openSeq);
		if (isForegroundDocument(tab) && openSeq === state.openSeq) {
			rememberEditorView(before, revision);
			canvasEditor.focus();
		}
	} catch (err) {
		if (isForegroundDocument(tab) && openSeq === state.openSeq) {
			if (el.insertPanel.open) {
				el.insertStatus.textContent = err.message;
			} else {
				showError(err, false);
			}
		}
	} finally {
		tab.editTask = false;
		setBusy(false, "", 0, "", tab);
	}
}

function displayPoints(length) {
	return Number((length * 72 / 25.4).toPrecision(6));
}

function inputMillimeters(input, original) {
	const points = Number(input.value);
	return points === displayPoints(original) ? original : points * 25.4 / 72;
}

async function changeTextAlignment() {
	const align = el.textAlign.value, editing = canvasEditor.input, tab = documentTab();
	if (!state.ready || state.exporting || document.body.hasAttribute("data-busy") || editing?.composing || editing?.saving || !align) return;
	const sequence = editing ? editing.alignSequence = (editing.alignSequence || 0) + 1 : 0;
	try {
		if (editing) {
			if (!editing.rich && !editing.item.draft && !await confirmTextReflow(editing.item)) return;
			if (!await ensureRichInput(!canEditObject(editing.item, "layoutKnown")) || !isForegroundDocument(tab) || canvasEditor.input !== editing || editing.alignSequence !== sequence) return;
			editing.rich.align(align);
		} else await saveTextParagraphs(currentTextStyle(), { align });
	} catch (error) {
		if (isForegroundDocument(tab) && canvasEditor.input === editing && (!editing || editing.alignSequence === sequence)) showError(error, false);
	} finally {
		if (isForegroundDocument(tab)) updateObjectControls(canvasEditor.selected, true);
	}
}

async function saveTextParagraphs(item, patch) {
	if (!Object.keys(patch).length) return true;
	if (item === state.textDefaults) {
		Object.assign(item, patch);
		return true;
	}
	if (!await confirmTextReflow(item)) return false;
	return changeDocument("ofdgoLayoutTexts", item, JSON.stringify(patch));
}

async function changeParagraph(wrap, changed) {
	const item = currentTextStyle(), tab = documentTab();
	if (canvasEditor.input) {
		const patch = wrap !== undefined ? { wrap: Boolean(wrap) } : {};
		for (const [input, key, original] of [[el.textLineHeight, "lineHeight", item.paragraphHeight], [el.textSpacing, "letterSpacing", item.letterSpacing]]) {
			if (key !== changed) continue;
			if (!input.checkValidity()) return updateObjectControls(canvasEditor.selected, true);
			if (input.value !== "" || original !== undefined) patch[key] = inputMillimeters(input, original || 0);
		}
		return changeInputParagraph(patch);
	}
	if (document.body.hasAttribute("data-busy") || item.draft) return;
	const patch = wrap !== undefined ? { wrap: Boolean(wrap) } : {};
	for (const [input, key, original] of [[el.textLineHeight, "lineHeight", item.paragraphHeight], [el.textSpacing, "letterSpacing", item.letterSpacing]]) {
		if (key !== changed) continue;
		if (!input.checkValidity()) return updateObjectControls(item, true);
		patch[key] = inputMillimeters(input, original || 0);
	}
	if (item === state.textDefaults) {
		if (patch.lineHeight !== undefined) item.paragraphHeight = patch.lineHeight;
		if (patch.letterSpacing !== undefined) item.letterSpacing = patch.letterSpacing;
		if (patch.wrap !== undefined) item.wrap = patch.wrap;
	} else await saveTextParagraphs(item, patch);
	if (isForegroundDocument(tab)) updateObjectControls(canvasEditor.selected, true);
}

async function changeInputParagraph(patch) {
	const editing = canvasEditor.input, tab = documentTab();
	if (!editing || editing.composing || editing.saving || document.body.hasAttribute("data-busy")) return false;
	try {
		if (!await ensureRichInput() || canvasEditor.input !== editing || !isForegroundDocument(tab)) return false;
		editing.rich.paragraph(patch);
		return true;
	} catch (error) {
		if (canvasEditor.input === editing && isForegroundDocument(tab)) showError(error, false);
		return false;
	}
}

async function changeTextStyle(color) {
	const item = currentTextStyle();
	if (!state.ready || state.exporting || document.body.hasAttribute("data-busy")) {
		return;
	}
	if (!color && !el.textSize.checkValidity()) {
		showError(new Error("字号无效"), false);
		updateObjectControls(item, true);
		return;
	}
	const size = color ? item.size : inputMillimeters(el.textSize, item.size);
	const selectedColor = el.textColor.value;
	const fill = color && selectedColor !== item.color ? selectedColor : null;
	if (canvasEditor.input) {
		const editing = canvasEditor.input, tab = documentTab();
		try {
			if (!await ensureRichInput() || !isForegroundDocument(tab) || editing && canvasEditor.input !== editing) return;
		} catch (error) {
			if (isForegroundDocument(tab) && (!editing || canvasEditor.input === editing)) showError(error, false);
			return;
		}
		canvasEditor.setTextStyle(color ? { color: selectedColor } : { size });
		updateObjectControls(canvasEditor.selected, true);
		return;
	}
	if (item.richText) {
		await changeDocument("ofdgoFormatText", item, JSON.stringify(color ? { color: selectedColor } : { size }));
		return;
	}
	if (item?.scoped && color) {
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
		if (color ? !canEditObject(item, "paint") : !await confirmTextReflow(item)) {
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
	if (!state.editing || document.body.hasAttribute("data-busy")) {
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
	if (!state.editing || document.body.hasAttribute("data-busy")) return;
	const next = canvasEditor.tool === tool ? "" : tool;
	state.selectObjects = true;
	pageView.setPan(false);
	canvasEditor.setTool(next);
	canvasEditor.focus();
}

function updateDrawingControls() {
	const tool = canvasEditor.tool;
	const path = selectedPath(canvasEditor.selected);
	el.insertTextButton.setAttribute("data-pressed", String(tool === "text"));
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
		button.setAttribute("data-pressed", String(tool === name));
	}
	el.eraseButton.disabled = el.eraseMode.disabled = disabled;
	el.eraseButton.setAttribute("data-pressed", String(tool.startsWith("erase-")));
	el.selectObjectButton.setAttribute("data-pressed", String(canvasEditor.enabled && !tool));
	const styleDisabled = disabled || (canvasEditor.selected ? !path || !canEditObject(path, "paint") : !pageCan("insert"));
	el.shapeFill.disabled = el.shapeStroke.disabled = styleDisabled || line;
	el.shapeFillColor.disabled = styleDisabled || line || !el.shapeFill.checked && !el.shapeFill.indeterminate;
	el.shapeStrokeColor.disabled = el.shapeWidth.disabled = styleDisabled || !line && !el.shapeStroke.checked && !el.shapeStroke.indeterminate;
}

async function changeDocument(name, item, ...args) {
	const tab = documentTab(), state = tab.state;
	const callWASM = (name, ...args) => callTabWASM(tab, name, ...args);
	if (!state.editing || document.body.hasAttribute("data-busy")) {
		return;
	}
	let openSeq = state.openSeq;
	const active = document.activeElement;
	const toolbarFocus = el.editorTools.contains(active) ? active : null;
	const previous = pageView.currentPageInfo() || state.doc.pages[Math.min(state.pageIndex, state.doc.pages.length - 1)] || {};
	const originalDocument = state.doc;
	const before = editorLocation(), revision = state.editorInfo.revision, wasmSeq = state.wasmSeq;
	const restoring = name === "ofdgoUndo" || name === "ofdgoRedo";
	const { scrollLeft, scrollTop } = el.viewerPanel;
	const importing = name === "ofdgoImportPages" || name === "ofdgoImportImages" || name === "ofdgoImportDocuments";
	const changingFont = name === "ofdgoChangeFontBinding";
	tab.editTask = true;
	let refreshManagement = false;
	state.imageImporting = name === "ofdgoImportImages";
	if (importing) state.importing = true;
	setBusy(true, importing ? name === "ofdgoImportDocuments" ? "正在导入文档" : "正在导入页面" : "", null);
	const progressTimer = changingFont ? window.setTimeout(() => {
		if (openSeq === state.openSeq && document.body.hasAttribute("data-busy")) {
			el.progressPanel.hidden = false;
			setProgress("正在处理字体", null);
		}
	}, 250) : null;
	try {
		args = await Promise.all(args.map(value => typeof value === "function" ? value() : value));
		if (tab.closed || state.wasmSeq !== wasmSeq || state.doc !== originalDocument || state.editorInfo?.revision !== revision) {
			return;
		}
		const scope = objectParent(item);
		const operation = { ofdgoTransformObject: "transform", ofdgoTransformObjects: "transform", ofdgoRotateObjects: "rotate", ofdgoFlipObjects: "flip", ofdgoResizeObjects: "resize",
			ofdgoAlignObject: "align", ofdgoAlignObjects: "align", ofdgoDistributeObjects: "distribute", ofdgoStyleObjects: "style", ofdgoUpdatePathStyle: "paint", ofdgoCompositeTextColor: "textColor",
			ofdgoUpdateText: "text", ofdgoStyleText: "textStyle", ofdgoFormatText: "formatText", ofdgoCropImage: "crop", ofdgoLayoutText: "layout", ofdgoFitImage: "fit", ofdgoResetCompositeCrop: "resetCrop",
			ofdgoReshapeObject: "reshape", ofdgoReshapeLine: "line", ofdgoEraseObjects: "erase", ofdgoEraseObjectsPath: "erasePath", ofdgoPasteStyle: "pasteStyle",
			ofdgoDeleteObject: "delete", ofdgoDeleteObjects: "delete", ofdgoCopyObjects: "copy", ofdgoOrderObjects: "order", ofdgoGroupObjects: "group" }[name];
		const members = item?.items ? [...item.items].sort((a, b) => a.order - b.order) : item ? [item] : [];
		const scoped = members.some(member => member.scoped);
		const ids = item ? Array.isArray(item.id) && !item.items ? item.id : members.map(member => member.id) : [];
		const annotated = !scoped && ids.some(id => id.startsWith("annotation:"));
		const inserting = ["ofdgoInsertShape", "ofdgoInsertText", "ofdgoInsertImage", "ofdgoInsertInk"].includes(name) || name === "ofdgoSaveRichText" && !item;
		const editingAnnotation = name === "ofdgoWriteAnnotation" && Boolean(state.annotationCreate?.item);
		const scopedRichText = scoped && name === "ofdgoSaveRichText";
		if (scopedRichText) args.push(scope);
		const textSelection = name === "ofdgoUpdateTextSelection";
		if (scoped && !operation && !textSelection && !scopedRichText && !["ofdgoLayoutTexts", "ofdgoStyleTextFonts"].includes(name)) throw new Error("内部对象暂不支持此操作");
		const doc = ["ofdgoStyleText", "ofdgoFormatText", "ofdgoLayoutTexts", "ofdgoStyleTextFonts"].includes(name)
			? await callWASM(name, item.index, ids, ...args)
			: textSelection
			? await callWASM(name, item.index, item.selectionIDs, ...args)
			: scoped && operation && (scope === null || operation === "copy" && members.some(member => member.container !== members[0].container))
			? await callWASM("ofdgoChangeSelection", item.index, ids, operation, ...args)
			: scoped && !scopedRichText
			? await callWASM("ofdgoChangeCompositeObjects", item.index, scope, ids, operation, ...args)
			: annotated && operation ? await callWASM("ofdgoChangeSelection", item.index, ids, operation, ...args)
			: await callWASM(name, ...(item ? [item.index, item.id] : []), ...args);
		if (tab.closed || state.doc !== originalDocument || state.editorInfo?.revision !== revision) return;
		if (!isForegroundDocument(tab) || openSeq !== state.openSeq) {
			if (!tab.closed && doc.pages) {
				tab.state.doc = doc;
				tab.state.editorInfo = {revision: doc.revision, canUndo: doc.canUndo, canRedo: doc.canRedo,
					pageCapabilities: doc.pageCapabilities, editWarnings: doc.editWarnings || []};
				tab.state.dirty = state.packageDirty || state.encryptionDirty || doc.revision !== state.savedRevision;
				tab.state.pageCache.clear();
				await refreshDocumentTab(tab);
			}
			return;
		}
		if (name === "ofdgoChangeInvoice") invoicePanel.result = doc.invoiceResult;
		if (doc.revision === state.editorInfo?.revision && !(name === "ofdgoSaveRichText" && canvasEditor.input?.refreshPending)) {
			return true;
		}
		const richEditing = name === "ofdgoSaveRichText" && canvasEditor.input?.rich && canvasEditor.input;
		if (richEditing?.saving) {
			richEditing.rich.initial = JSON.stringify(richEditing.rich.model);
			richEditing.refreshPending = true;
		}
		if (richEditing?.saving && richEditing.item.draft && doc.selectedIDs?.length) {
			richEditing.item.id = doc.selectedIDs[0];
			richEditing.item.draft = false;
		}
		const restoredView = name === "ofdgoUndo" ? state.editorViews.get(revision) : name === "ofdgoRedo" ? state.editorViews.get(doc.revision) : null;
		if (restoredView?.reading && state.savedRevision === revision) state.savedRevision = doc.revision;
		refreshManagement = el.documentPanel.open && (name === "ofdgoChangeDocument" || name === "ofdgoImportDocuments" || name === "ofdgoUpdateInfo" || restoring);
		const previousDocument = state.doc.documents?.[state.doc.documentIndex || 0];
		const nextDocument = doc.documents?.[doc.documentIndex || 0];
		const sameDocument = previousDocument?.id && previousDocument.id === nextDocument?.id && state.doc.documentIndex === doc.documentIndex;
		const documentChanged = (state.doc.documentVersion || "") !== (doc.documentVersion || "")
			|| previousDocument?.id !== nextDocument?.id || previousDocument?.root !== nextDocument?.root
			&& !(sameDocument && (name === "ofdgoChangeFontBinding" || restoring));
		const packageChanged = documentChanged || name === "ofdgoChangeDocument" || name === "ofdgoImportDocuments" || restoring
			&& JSON.stringify(doc.documents?.map(entry => [entry.root, entry.id])) !== JSON.stringify(state.doc.documents?.map(entry => [entry.root, entry.id]));
		if (packageChanged) {
			if (state.documentManagement && (name === "ofdgoImportDocuments" || name === "ofdgoChangeDocument" && args[0] === "add")) {
				state.documentManagement.key = documentEntryKey(nextDocument);
			}
			state.objectClipboardToken = state.styleClipboard = null;
			state.pageSelection.clear();
			state.pageSelectionAnchor = null;
			state.pageMultiSelect = false;
			state.outlineSelection = null;
			state.outlineExpanded.clear();
			canvasEditor.clear();
			canvasEditor.setTool("");
			if (documentChanged) {
				delete state.textDefaults.fontChoice;
				updateTextFonts(null, true);
			}
			state.ofdBytes = null;
			setEditorInfo(doc);
			openSeq = advanceDocumentSequence();
			await openDocument({ doc, openSeq, skipAutoFonts: true, resetScroll: documentChanged, clearSelection: true,
				pageIndex: documentChanged ? 0 : state.pageIndex, fitMode: state.fitMode, scale: state.scale });
			if (openSeq === state.openSeq) {
				if (!restoring) rememberEditorView(before, revision);
				return true;
			}
			return;
		}
		const view = restoredView;
		const location = name === "ofdgoUndo" ? view?.before : view?.after;
		const restoredIndex = location ? pageIndexByID(doc.pages, location.page) : -1;
		if (restoring && JSON.stringify(doc.outlines) !== JSON.stringify(state.doc.outlines)) {
			state.outlineSelection = null;
			state.outlineExpanded.clear();
		}
		setEditorInfo(doc);
		if (name === "ofdgoUpdateInfo") {
			for (const key of [...INFO_FIELDS, "keywords", "customData", "documents", "documentIndex"]) state.doc[key] = doc[key];
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
		if (name === "ofdgoCopyObjects" || ["ofdgoPasteObjects", "ofdgoImportObjects"].includes(name) || name === "ofdgoGroupObjects" || inserting || name === "ofdgoWriteAnnotation" && !editingAnnotation) {
			state.selectObjects = true;
			if (name !== "ofdgoInsertInk") canvasEditor.setTool("");
			pageView.setPan(false);
			canvasEditor.pendingSelection = { index: item?.index ?? args[0], ids: name === "ofdgoInsertInk" ? [] : doc.selectedIDs };
		}
		if (scoped && name === "ofdgoOrderObjects") canvasEditor.pendingSelection = { index: item.index, ids: doc.selectedIDs };
		if (textSelection) canvasEditor.pendingSelection = { index: item.index, ids: doc.selectedIDs || [] };
		openSeq = advanceDocumentSequence();
		if (changingFont) {
			await refreshFontBinding(doc, openSeq);
		} else if (restoring && !doc.changes?.all && doc.changes?.pages?.length === 1 && doc.changes.pages[0] === state.pageIndex && (restoredIndex < 0 || restoredIndex === state.pageIndex)) {
			if (location) canvasEditor.pendingSelection = {index:state.pageIndex, ids:location.ids};
			await refreshEditorPage(doc, state.pageIndex, openSeq, clearSelection);
		} else if (item || ["ofdgoPasteObjects", "ofdgoImportObjects"].includes(name) || inserting || editingAnnotation) {
			await refreshEditorPage(doc, item ? item.index : args[0], openSeq, clearSelection);
		} else {
			const samePage = pageIndexByID(doc.pages, previous.id);
			const pageIndex = restoredIndex >= 0 ? restoredIndex : doc.pageIndex ?? (samePage < 0 ? Math.min(state.pageIndex, doc.pageCount - 1) : samePage);
			const page = doc.pages[pageIndex];
			const keepScroll = doc.pageIndex === undefined && page.id === previous.id && pageIndex === state.pageIndex
				&& page.width === previous.width && page.height === previous.height;
			await openDocument({ doc, openSeq, skipAutoFonts: true, pageIndex, fitMode: state.fitMode, scale: state.scale,
				keepPreview: true, clearSelection, selection: restoredIndex >= 0 ? { index: restoredIndex, ids: location.ids } : null,
				previewScroll: keepScroll ? { scrollLeft, scrollTop } : null });
		}
		if (openSeq === state.openSeq) {
			if (!restoring) rememberEditorView(before, revision);
			if (el.annotationsTab.getAttribute("data-selected") === "true") await loadAnnotationSummary();
			if (!toolbarFocus && !changingFont) canvasEditor.focus();
			return true;
		}
	} catch (err) {
		refreshManagement = false;
		if (name === "ofdgoChangeFontBinding") err.message = fontBindingError(err.message);
		if (name === "ofdgoChangeInvoice") err.message = invoiceIssues([err.message]);
		if (isForegroundDocument(tab) && openSeq === state.openSeq) {
			if (name === "ofdgoSaveRichText" && canvasEditor.input?.saving) {
				canvasEditor.setTextError(err);
			} else if (settingsElements.Panel.open) {
				settingsElements.Status.textContent = err.message;
			} else if (fontBindingElements.Panel.open) {
				fontBindingElements.Status.textContent = fontBindingError(err.message);
			} else if (el.annotationCreate.open) {
				el.annotationCreateStatus.textContent = err.message;
			} else if (el.annotationNote.open) {
				el.annotationStatus.textContent = err.message;
			} else if (el.batchPagesPanel.open) {
				el.batchPagesStatus.textContent = err.message;
			} else if (el.objectStylePanel.open) {
				el.objectStyleStatus.textContent = err.message;
			} else if (el.objectBoundsPanel.open) {
				el.objectBoundsStatus.textContent = err.message;
			} else if (el.outlinePanel.open) {
				el.outlineStatus.textContent = err.message;
			} else if (el.structureDialog.open) {
				el.structureStatus.textContent = err.message;
			} else if (el.infoPanel.open) {
				el.infoStatus.textContent = err.message;
			} else if (el.attachmentDialog.open) {
				el.attachmentStatus.textContent = err.message;
			} else if (el.pagePanel.open) {
				el.pageStatus.textContent = err.message;
			} else if (el.paragraphPanel.open) {
				el.paragraphStatus.textContent = err.message;
			} else if (el.createPanel.open) {
				el.createStatus.textContent = err.message;
			} else if (el.importPanel.open) {
				el.importStatus.textContent = err.message;
			} else if (el.versionPanel.open) {
				el.versionStatus.textContent = err.message;
			} else if (el.documentPanel.open) {
				el.documentStatus.textContent = err.message;
			} else if (resourceElements.Panel.open) {
				resourceElements.Status.textContent = resourceError(err.message);
			} else {
				showError(err, false);
			}
		}
	} finally {
		if (progressTimer !== null) window.clearTimeout(progressTimer);
		tab.editTask = false;
		if (importing) {
			state.importing = false;
			state.imageImporting = false;
			state.exportRequestID = 0;
			if (isForegroundDocument(tab)) {
				el.createCancel.disabled = false;
				el.cancelExportButton.hidden = true;
				el.importCancel.disabled = false;
			}
		}
		setBusy(false, "", 0, "", tab);
		if (isForegroundDocument(tab) && openSeq === state.openSeq) {
			if (refreshManagement) await refreshDocumentManagement();
			if (toolbarFocus?.isConnected && !toolbarFocus.disabled) toolbarFocus.focus({ preventScroll: true });
		}
	}
}

async function refreshFontBinding(doc, openSeq) {
	const indices = new Set([state.pageIndex, ...state.visiblePages, ...state.visibleThumbnails]);
	for (const shell of el.svgHost.querySelectorAll(".page-shell.rendered")) indices.add(Number(shell.dataset.pageIndex));
	state.doc = doc;
	resetSearch();
	state.documentSelection = null;
	pageView.resetPageLoading();
	state.pageCache.clear();
	canvasEditor.clear();
	try {
		try {
			const details = await callWASM("ofdgoDocumentInfo");
			if (openSeq !== state.openSeq) return;
			Object.assign(doc, details, { detailsPending: false, detailsError: "" });
		} catch (err) {
			if (openSeq !== state.openSeq) return;
			Object.assign(doc, { detailsPending: false, detailsError: err.message });
		}
		for (const index of indices) {
			try {
				const page = await pageView.loadPageData(index, { openSeq, priority: 0, refresh: true });
				if (openSeq !== state.openSeq) return;
				if (page) {
					pageView.mountPageSVG(index, page, openSeq);
					pageView.updateThumbnail(index, openSeq);
				}
			} catch (err) {
				if (openSeq !== state.openSeq) return;
				pageView.unmountPage(index);
				pageView.markFlowPageError(index, err);
				pageView.markThumbnailError(index);
				showError(err, false);
			}
		}
	} finally {
		if (openSeq === state.openSeq) {
			pageView.releasePageResources();
			renderMeta(true);
			updateControls();
			loadDocumentDetails(openSeq);
			for (const shell of el.svgHost.children) pageView.observeFlowPage(shell);
			for (const button of el.pageList.children) pageView.observeThumbnail(button, Number(button.dataset.pageIndex), openSeq);
		}
	}
}

async function refreshEditorPage(doc, index, openSeq, clearSelection = false) {
	state.doc = doc;
	const search = refreshSearchPages([index], openSeq);
	state.documentSelection = null;
	pageView.resetPageLoading();
	let details;
	try {
		const [page, prepared] = await Promise.all([
			pageView.loadPageData(index, { openSeq, priority: 0, refresh: true }),
			callWASM("ofdgoDocumentInfo").catch(err => ({ detailsError: err.message })),
		]);
		if (openSeq !== state.openSeq) {
			return;
		}
		details = prepared.detailsError ? null : prepared;
		Object.assign(doc, { detailsPending: false, detailsError: "" }, prepared);
		if (clearSelection) {
			const pending = canvasEditor.pendingSelection;
			canvasEditor.clear();
			canvasEditor.pendingSelection = pending;
		}
		pageView.mountPageSVG(index, page, openSeq);
		pageView.updateThumbnail(index, openSeq);
		setStatus(pageStatus(state.pageIndex, doc.pageCount));
	} catch (err) {
		if (openSeq === state.openSeq) {
			if (canvasEditor.input?.saving && canvasEditor.input.rich && canvasEditor.input.item.index === index) {
				canvasEditor.select(canvasEditor.input.item);
			} else {
				state.pageCache.delete(index);
				canvasEditor.clear();
				const shell = pageView.pageShell(index);
				shell.querySelector(".page-surface").replaceChildren();
				shell.classList.remove("rendered");
				pageView.markFlowPageError(index, err);
				pageView.markThumbnailError(index);
			}
		}
		throw err;
	} finally {
		if (openSeq === state.openSeq) {
			pageView.releasePageResources();
			renderMeta(true);
			updateControls();
			loadDocumentDetails(openSeq, details);
			await search;
			for (const shell of el.svgHost.children) pageView.observeFlowPage(shell);
			for (const button of el.pageList.children) pageView.observeThumbnail(button, Number(button.dataset.pageIndex), openSeq);
		}
	}
}

async function openOFDFile() {
	if (workspace.closing) return;
	if (!nativeHost && window.showOpenFilePicker) {
		try {
			const handles = await window.showOpenFilePicker({multiple: true, types: [{description: "OFD、PDF", accept: {"application/ofd": [".ofd"], "application/pdf": [".pdf"]}}]});
			await requestLocalFontsBeforeOpen();
			await Promise.all(handles.map(async handle => openOFD(await handle.getFile(), null, handle)));
		} catch (error) { if (error.name !== "AbortError") showError(error, !state.doc); }
		return;
	}
	await requestLocalFontsBeforeOpen();
	el.ofdInput.value = "";
	el.ofdInput.click();
}

async function openOFDFromViewer() {
	if (state.doc || document.body.hasAttribute("data-busy")) {
		return;
	}
	await openOFDFile();
}

async function openOFDFromDrop(event) {
	if (!event.dataTransfer.types.includes("Files")) {
		return;
	}
	event.preventDefault();
	if (workspace.closing) return;
	const files = Array.from(event.dataTransfer.files);
	const handles = Array.from(event.dataTransfer.items).map(item => item.getAsFileSystemHandle?.().catch(() => null));
	if (files.some(isDocumentFile)) {
		await requestLocalFontsBeforeOpen();
	}
	await Promise.all(files.map(async (file, index) => openOFD(file, null, await handles[index])));
}

function openFontFile(input) {
	if (document.body.hasAttribute("data-busy")) {
		return;
	}
	const editing = canvasEditor.input;
	if (editing) {
		editing.rich?.remember();
		editing.fontDialog = true;
		const finish = () => {
			editing.fontDialog = false;
			input.removeEventListener("change", finish);
			input.removeEventListener("cancel", finish);
			if (canvasEditor.input === editing) editing.input.focus({ preventScroll: true });
		};
		input.addEventListener("change", finish);
		input.addEventListener("cancel", finish);
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
	el.togglePagesButton.setAttribute("data-pressed", String(state.showPages));
	el.toggleMetaButton.setAttribute("data-pressed", String(state.showMeta));
	if (openingPages) {
		pageView.updatePageListCurrent(true);
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
		pageView.syncThumbnailWindow();
		pageView.applyFit(false);
		if (state.fitMode !== "free") {
			pageView.scrollToPage(state.pageIndex);
		}
	}
}

async function boot() {
	window.ofdgoTheme.connect(nativeHost, error => showError(error, false));
	el.appearanceButton.addEventListener("click", () => {
		el.associationMessage.hidden = true;
		documentDialogs.showApplication(el.appearancePanel);
		if (nativeHost) void refreshFileAssociations();
	});
	for (const button of el.appearancePanel.querySelectorAll("[data-appearance-category]")) button.addEventListener("click", () => selectAppearanceCategory(button.dataset.appearanceCategory));
	for (const button of el.appearancePanel.querySelectorAll("[data-theme-choice]")) button.addEventListener("click", () => window.ofdgoTheme.set(button.dataset.themeChoice));
	documentDialogs = new DocumentDialogs({document, titlebar: el.documentTitleBar,
		targets: () => [...document.body.children].filter(node => node !== el.documentTitleBar)});
	for (const panel of document.querySelectorAll("dialog.form-dialog")) if (panel !== el.printPanel) documentDialogs.register(panel, {persistent: panel === batchElements.Panel});
	documentDialogs.setOwner(documentTab());
	const openSeq = state.openSeq;
	setBusy(true, "正在准备引擎", 8, STATUS.engine);
	try {
		if (!await prepareOffline()) {
			return;
		}
		if (openSeq === state.openSeq) setProgress("正在准备引擎", 8, STATUS.engine);
		await ensureWASM();
		let fontsRestored = true;
		try {
			await fontManager.restore();
		} catch {
			fontsRestored = false;
		}
		if (openSeq === state.openSeq) setProgress("引擎准备完成", 70);
		await loadExportFormats();
		if (openSeq === state.openSeq) setEmpty("选择 OFD、PDF 文件");
		updateFontSummary();
		renderFontList();
		updateControls();
		updateLocalFontButton();
		await fontManager.refreshPermission();
		loadEditorFonts(false);
		if (openSeq === state.openSeq) {
			setStatus(fontsRestored ? STATUS.ready : "字体读取失败");
			setBusy(false);
		}
		if ("launchQueue" in window) {
			let opening = Promise.resolve();
			window.launchQueue.setConsumer(({ files }) => {
				if (!files.length) {
					return;
				}
				opening = opening.then(async () => {
					for (const file of files) await openOFD(await file.getFile(), null, file);
				}).catch((err) => showError(err, !state.doc));
			});
		}
		if (nativeHost) {
			nativeHost.bridge.on("ofdgo.window", applyTitleBarInfo);
			nativeHost.bridge.on("ofdgo.closing", closeWorkspace);
			applyTitleBarInfo(await nativeHost.call("window.info", {}));
			new ResizeObserver(updateTitleBarRegions).observe(el.documentTitleBar);
			await nativeHost.start(openNativeFiles, error => showError(error, !state.doc));
			watchApplicationUpdates();
		}
		try {
			const remote = remoteDocumentOptions(window.location.hash);
			if (remote && openSeq === state.openSeq) await openOFD({ name: remote.name }, remote);
		} catch (err) {
			if (openSeq === state.openSeq) showError(err, !state.doc);
		}
	} catch (err) {
		if (openSeq === state.openSeq) {
			setStatus("引擎加载失败");
			setEmpty(String(err.message || err));
			setBusy(false);
		}
		return;
	}
}

async function openNativeDocument(file) {
	let retained = false;
	try {
		retained = await openOFD(file);
	} catch (error) {
		showError(error, !state.doc);
	} finally {
		if (!retained) await nativeHost.discardFiles([file]);
	}
}

async function openNativeFiles(files, launch = false) {
	if (launch) {
		for (const file of files) {
			while (workspace.closing || workspace.switching || batchElements.Panel.open || documentDialogs?.applicationOpen()) await new Promise(resolve => setTimeout(resolve, 100));
			void openNativeDocument(file).catch(error => showError(error, !state.doc));
		}
		return;
	}
	let retained = [];
	try {
		if (workspace.closing || batchElements.Panel.open && batch.running) { setStatus("请等待当前操作完成"); return; }
		if (batchElements.Panel.open) {
			retained = files.filter(isDocumentFile);
			addBatchFiles(files);
		} else {
			await Promise.all(files.map(async file => { if (await openOFD(file)) retained.push(file); }));
		}
	} finally {
		await nativeHost.discardFiles(files.filter(file => !retained.includes(file)));
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
	if (nativeHost) {
		return true;
	}
	if (!window.isSecureContext || !("serviceWorker" in navigator) || !("caches" in window) || !("locks" in navigator)) {
		return true;
	}
	setProgress("正在准备离线", null, "正在准备离线");
	let checksum;
	try {
		const registration = await registerOffline();
		const result = await requestOffline(registration.active, "prepare");
		if (result.reload) {
			location.reload();
			return false;
		}
		if (navigator.storage?.persist) {
			navigator.storage.persist().catch(() => false);
		}
		checksum = result.checksum;
	} catch {
	}
	watchApplicationUpdates(checksum);
	el.refreshAppButton.disabled = false;
	return true;
}

async function applicationChecksum(path, options) {
	const response = await fetch(path, { ...options, signal: AbortSignal.timeout(10000) });
	try {
		return response.ok && !response.redirected ? response.headers.get("X-OFDGo-Checksum") : null;
	} finally {
		await response.body?.cancel();
	}
}

function watchApplicationUpdates(checksum) {
	let checking = false;
	let retry = false;
	async function check(force = false) {
		if (checking) {
			if (force === true) retry = true;
			return;
		}
		if (document.hidden || !nativeHost && !navigator.onLine) {
			return;
		}
		checking = true;
		try {
			if (!nativeHost && !checksum) {
				if (!navigator.serviceWorker.controller) return;
				checksum = await applicationChecksum("./ofdgo.js");
				if (!checksum) return;
			}
			const latest = nativeHost ? await nativeHost.call("update.check", {}) : await applicationChecksum("./", { method: "HEAD", cache: "no-store" });
			if (nativeHost) {
				const hidden = !latest;
				if (el.refreshAppButton.hidden !== hidden) {
					el.refreshAppButton.hidden = hidden;
					updateTitleBarRegions();
				}
			} else if (latest) {
				el.refreshAppButton.hidden = latest === checksum;
				updateTitleBarIcons();
			}
		} catch {
		} finally {
			checking = false;
			if (retry) {
				retry = false;
				check(true);
			}
		}
	}
	window.addEventListener("online", () => check(true));
	window.addEventListener("focus", check);
	window.addEventListener("pageshow", check);
	document.addEventListener("visibilitychange", check);
	check();
}

function waitForWorker(worker, target) {
	return new Promise((resolve, reject) => {
		if (!worker) {
			reject(new Error("离线服务未就绪"));
			return;
		}
		const timer = setTimeout(() => {
			finish(new Error("更新等待超时"));
		}, 30000);
		function finish(error) {
			clearTimeout(timer);
			worker.removeEventListener("statechange", changed);
			if (error) reject(error);
			else resolve();
		}
		function changed() {
			if (worker.state === target || worker.state === "activated") {
				finish();
			} else if (worker.state === "redundant") {
				finish(new Error("离线启动失败"));
			}
		}
		worker.addEventListener("statechange", changed);
		changed();
	});
}

function requestOffline(worker, type) {
	return new Promise((resolve, reject) => {
		if (!worker) {
			reject(new Error("离线服务未就绪"));
			return;
		}
		const channel = new MessageChannel();
		const timer = setTimeout(() => {
			close();
			reject(new Error("更新等待超时"));
		}, 180000);
		function close() {
			clearTimeout(timer);
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
		try {
			worker.postMessage({ type }, [channel.port2]);
		} catch (err) {
			close();
			channel.port2.close();
			reject(err);
		}
		changed();
	});
}

async function refreshApplication() {
	if (nativeHost) {
		try { await nativeHost.call("update.open", {}); }
		catch (err) { showError(err, false); }
		return;
	}
	if (state.exporting || document.body.hasAttribute("data-busy")) {
		return;
	}
	if (workspace.tabs.some(tab => tab.state.saving)) {
		setStatus("文档正在保存，请稍后更新");
		return;
	}
	if (!navigator.onLine) {
		setStatus("离线，联网后重试");
		return;
	}
	const temporaryFonts = fontManager.userFonts.some((font) => font.source === "upload");
	const dirty = state.dirty || workspace.tabs.some(tab => tab.state.dirty);
	const message = [dirty ? "文档尚未保存，刷新将丢失更改" : "刷新后需重新打开文件", temporaryFonts ? "未保存字体需重新添加" : ""].filter(Boolean).join("\n");
	if ((workspace.tabs.some(tab => tab.state.doc || tab.state.ofdBytes) || temporaryFonts) && !await requestConfirmation(message, null)) {
		return;
	}
	setBusy(true, "正在更新应用", null, "正在更新应用");
	el.refreshAppButton.disabled = true;
	const panels = document.querySelectorAll(".document-titlebar, .toolbar-frame, .workspace");
	panels.forEach((panel) => { panel.inert = true; });
	try {
		if (navigator.storage?.persist) {
			navigator.storage.persist().catch(() => false);
		}
		const scope = new URL("./", location.href).href;
		await navigator.locks.request(`ofdgo:${scope}:refresh`, { ifAvailable: true }, async (lock) => {
			if (!lock) {
				throw new Error("其他页面正在更新");
			}
			const registration = await registerOffline();
			let timer;
			try {
				await Promise.race([registration.update(), new Promise((resolve, reject) => {
					timer = setTimeout(() => reject(new Error("更新等待超时")), 30000);
				})]);
			} finally {
				clearTimeout(timer);
			}
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
		setStatus(err.message === "其他页面正在更新" ? err.message : "更新失败，请重试");
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
	return ensureTabEngine(documentTab());
}

async function ensureTabEngine(tab) {
	if (tab.closed) throw new Error("文档已关闭");
	if (tab.state.ready && !tab.state.wasmExited) {
		return;
	}
	if (!tab.promise) {
		tab.promise = loadWASM(tab).finally(() => {
			tab.promise = null;
		});
	}
	await tab.promise;
}

async function loadWASM(tab = documentTab()) {
	const state = tab.state;
	const wasmSeq = state.wasmSeq + 1;
	state.wasmSeq = wasmSeq;
	state.ready = false;
	state.wasmExited = false;
	await new Promise((resolve, reject) => {
		tab.rejectLoad = reject;
		const worker = createEngineWorker({pages: true});
		tab.worker = worker;
		if (workspace.active === tab) wasmWorker = worker;
		const fail = (err) => {
			markWASMExited(wasmSeq, err, tab);
			reject(err);
		};
		worker.onerror = (event) => fail(new Error(event.message || "引擎加载失败"));
		worker.onmessageerror = () => fail(new Error("引擎通信失败"));
		worker.onmessage = async ({ data }) => {
			if (worker !== tab.worker || tab.closed) {
				return;
			}
			const visible = isForegroundDocument(tab);
			if (data.type === "progress") tab.progress = {text: data.text, percent: data.percent};
			if (data.type === "font") {
				try {
					const request = wasmRequests.get(data.id);
					const font = request?.localFonts?.get(data.name);
					if (!font || request.tab !== tab) throw new Error("系统字体不可用");
					if (!request.fontData.has(font)) request.fontData.set(font, fontManager.read(font));
					const bytes = await request.fontData.get(font);
					if (worker === tab.worker) worker.postMessage({ type: "font", request: data.request, bytes });
				} catch (err) {
					if (worker === tab.worker) worker.postMessage({ type: "font", request: data.request, error: err.message });
				}
				return;
			}
			if (data.type === "progress") {
				if (visible) setProgress(data.text, data.percent);
			} else if (data.type === "conversion") {
				const request = wasmRequests.get(data.id);
				if (request?.name === "ofdgoLoadPDF") {
					for (const pending of wasmRequests.values()) if (pending.tab === tab && pending.preparing) operationProgress(pending, data);
				} else if (request?.name === "ofdgoOpenPDF") {
					const text = data.phase === "open" ? "正在读取文档" : data.phase === "pages" ? "正在整理页面" : "正在准备阅读";
					tab.progress = {text, percent:null};
					if (visible && !el.cancelExportButton.disabled) setProgress(text, null);
				}
			} else if (data.type === "export") {
				if (data.id === state.saveRequestID && state.saving && !state.exporting) {
					if (visible && data.stage === "save") el.cancelExportButton.disabled = true;
					return;
				}
				if (visible && wasmRequests.get(data.id)?.openSeq === state.openSeq && state.exporting) {
					if (data.stage === "save") {
						el.cancelExportButton.disabled = true;
						if (state.signing) el.signCancel.disabled = true;
						setProgress("正在写入文件", null);
					} else if (!el.cancelExportButton.disabled) {
						if (data.stage === "prepare") {
							const label = { snapshot: "正在准备文档", ids: "正在检查标识", commit: "正在整理文档", fonts: "正在处理字体", pages: "正在处理页面", references: "正在检查引用", resources: "正在整理资源", compress: "正在压缩文件", write: "正在写入文件", sign: "正在签署文档", encrypt: "正在加密文档" }[data.phase];
							setProgress(data.phase === "compress" && data.completed ? `${label} · 已处理 ${data.completed} 项` : label, data.total ? data.completed / data.total * 100 : null);
						} else if (data.completed === data.total) {
							setProgress("正在整理文件", null);
						} else {
							setProgress(`正在导出页面 · ${data.completed} / ${data.total} 页`, data.completed / data.total * 100);
						}
					}
				}
			} else if (data.type === "import") {
				if (visible && wasmRequests.get(data.id)?.openSeq === state.openSeq && state.imageImporting) {
					const text = data.phase === "commit" ? "正在完成导入" : `正在导入图片 · ${data.completed} / ${data.total} 张`;
					el.createStatus.textContent = text;
					setProgress(text, data.total ? data.completed / data.total * 100 : null);
					if (data.phase === "commit") el.createCancel.disabled = el.cancelExportButton.disabled = true;
					return;
				}
				if (visible && wasmRequests.get(data.id)?.openSeq === state.openSeq && state.importing && !el.importCancel.disabled) {
					const label = { ids: "正在检查标识", documents: "正在导入文档", pages: "正在导入页面", resources: "正在读取资源", commit: "正在完成导入" }[data.phase] || "正在导入文档";
					el.importStatus.textContent = data.total ? `${label} · ${data.completed} / ${data.total}${state.importDocumentMode ? "" : " 页"}` : label;
					setProgress(label, data.total ? data.completed / data.total * 100 : null);
					if (data.phase === "commit") el.importCancel.disabled = el.cancelExportButton.disabled = true;
				}
			} else if (data.type === "ready") {
				state.ready = true;
				tab.rejectLoad = null;
				resolve();
			} else if (data.type === "exit") {
				fail(new Error(data.error));
			} else {
				const request = wasmRequests.get(data.id);
				if (!request || request.tab !== tab) return;
				if (data.code === "documentPending" && !request.canceled) { prepareOperation(data.id, request); return; }
				wasmRequests.delete(data.id);
				if (data.ok) {
					request.resolve(data.data);
				} else {
					const err = new Error(missingGlyphMessage(data.missingGlyphs) || (data.reasonCode === "layoutRequired" ? "跨段或换行修改需先设置段落排版" : data.error));
					err.code = data.code;
					if (data.errorName) err.name = data.errorName;
					err.missingGlyphs = data.missingGlyphs;
					if (data.canceled || request.canceled) {
						err.name = "AbortError";
					}
					request.reject(err);
				}
			}
		};
	});
}

function markWASMExited(wasmSeq = state.wasmSeq, err, tab = documentTab()) {
	const state = tab.state;
	if (wasmSeq !== state.wasmSeq) {
		return;
	}
	state.ready = false;
	tab.rejectLoad?.(err || new Error("引擎运行中断"));
	tab.rejectLoad = null;
	if (isForegroundDocument(tab)) clearSecurityForms();
	state.wasmExited = true;
	if (err) tab.status = state.editorInfo ? "引擎中断，未保存内容无法恢复" : "引擎运行中断";
	const stopped = tab.worker?.terminate();
	tab.worker = null;
	if (workspace.active === tab) wasmWorker = null;
	for (const [id, request] of wasmRequests) {
		if (request.tab !== tab) continue;
		wasmRequests.delete(id);
		request.reject(err || new Error("引擎运行中断"));
	}
	if (!isForegroundDocument(tab) || workspace.active !== tab) return stopped;
	if (err) {
		setStatus(tab.status);
	}
	updateControls();
	scheduleWASMRecovery();
	return stopped;
}

function scheduleWASMRecovery() {
	if (!state.ofdBytes || state.editorInfo || state.wasmRecovering || state.wasmRecoveries >= 2) {
		return;
	}
	const tab = documentTab();
	window.clearTimeout(wasmRecoveryTimer);
	wasmRecoveryTimer = window.setTimeout(() => {
		if (workspace.active === tab && !workspace.switching && !tab.opening) recoverWASM();
	}, 0);
}

async function recoverWASM() {
	const tab = documentTab(), state = tab.state;
	if (!state.wasmExited || state.editorInfo || state.wasmRecovering || !state.ofdBytes) {
		return;
	}
	state.wasmRecovering = true;
	state.wasmRecoveries += 1;
	const openSeq = advanceDocumentSequence();
	setBusy(true, "正在恢复引擎", 18, STATUS.recovering, tab);
	try {
		await openDocument({
			pageIndex: state.pageIndex,
			versionID: state.doc?.documentVersion,
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
		setBusy(false, "", 0, "", tab);
	}
}

function remoteDocumentOptions(hash) {
	const params = new URLSearchParams(hash.replace(/^#/, ""));
	for (const key of ["url", "name", "page"]) {
		if (params.getAll(key).length > 1) throw new Error(`重复的链接参数：${key}`);
	}
	if (!params.has("url")) return null;
	let url;
	try { url = new URL(params.get("url")); } catch { throw new Error("文件地址无效"); }
	if (!["http:", "https:"].includes(url.protocol) || url.username || url.password) throw new Error("文件地址必须为不含账号密码的 HTTP 或 HTTPS 地址");
	if (window.location.protocol === "https:" && url.protocol !== "https:") throw new Error("请使用 HTTPS 文件地址");
	const page = params.get("page") || "1";
	return { url: url.href, name: params.get("name") || "", pageIndex: /^[1-9]\d*$/.test(page) ? Math.min(Number(page), Number.MAX_SAFE_INTEGER) - 1 : 0 };
}

function remoteDocumentName(name) {
	return String(name || "").split(/[\\/]/).pop().replace(/[\u0000-\u001f\u007f]/g, "").trim();
}

async function downloadRemoteDocument(options, tab = documentTab(), controller = new AbortController()) {
	tab.download = controller;
	if (isForegroundDocument(tab)) {
		remoteDownload = controller;
		el.cancelExportButton.hidden = false;
		el.cancelExportButton.disabled = false;
	}
	setProgress("正在下载文档", null, "", tab);
	try {
		const response = await fetch(options.url, { mode: "cors", credentials: "omit", referrerPolicy: "no-referrer", cache: "no-store", signal: controller.signal });
		if (!response.ok) throw new Error(`文件下载失败（HTTP ${response.status}）`);
		const total = Number(response.headers.get("Content-Length")) || 0;
		if (!response.body) throw new Error("下载内容为空");
		const chunks = [];
		let received = 0;
		const reader = response.body.getReader();
		try {
			while (true) {
				const { done, value } = await reader.read();
				if (done) break;
				chunks.push(value);
				received += value.byteLength;
				setProgress(`正在下载文档 · ${(received / 1048576).toFixed(1)} MB`, total > 0 && received <= total ? received / total * 20 : null, "", tab);
			}
		} finally { reader.releaseLock(); }
		controller.signal.throwIfAborted();
		const disposition = response.headers.get("Content-Disposition") || "";
		let supplied = /(?:^|;)\s*filename\*=UTF-8'[^']*'([^;]+)/i.exec(disposition)?.[1];
		if (supplied) { try { supplied = decodeURIComponent(supplied.trim()); } catch { supplied = ""; } }
		if (!supplied) supplied = /(?:^|;)\s*filename\s*=\s*(?:"((?:[^"\\]|\\.)*)"|([^;]*))/i.exec(disposition)?.slice(1).find(value => value !== undefined)?.replace(/\\(.)/g, "$1");
		let pathName = new URL(response.url || options.url).pathname.split("/").pop();
		try { pathName = decodeURIComponent(pathName); } catch { pathName = ""; }
		const name = remoteDocumentName(options.name) || remoteDocumentName(supplied) || remoteDocumentName(pathName);
		return { file: new Blob(chunks), name };
	} catch (err) {
		if (err instanceof TypeError) throw new Error("下载失败，请检查网络、链接及跨域配置");
		throw err;
	} finally {
		tab.download = null;
		if (isForegroundDocument(tab) && remoteDownload === controller) {
			remoteDownload = null;
			el.cancelExportButton.hidden = true;
		}
	}
}

async function openOFD(file, remote = null, handle = file?.handle) {
	if (workspace.closing) return;
	if (!file) {
		return;
	}
	if (!remote && !isDocumentFile(file)) {
		el.ofdInput.value = "";
		showError(new Error("选择 OFD、PDF 文件"), !state.doc);
		return;
	}
	const previous = documentTab();
	const tab = createDocumentTab();
	copyApplicationPreferences(tab.state);
	const request = {controller: new AbortController()};
	tab.opening = request;
	tab.state.fileName = file.name || remote?.name || STATUS.opening;
	tab.status = STATUS.opening;
	workspace.tabs.push(tab);
	const current = () => !tab.closed && tab.opening === request;
	let accepted = false;
	try {
		setBusy(true, "正在读取文档", 10, STATUS.opening, tab);
		await activateDocumentTab(tab);
		if (!current()) return;
		const previousIndex = workspace.tabs.indexOf(previous);
		if (previousIndex >= 0 && workspace.active === tab && !previous.state.doc && !previous.opening && !previous.state.dirty && !previous.error) {
			workspace.tabs.splice(previousIndex, 1);
			await releaseDocumentTab(previous);
		}
		renderDocumentTabs();
		if (!current()) return;
		const downloaded = remote ? await downloadRemoteDocument(remote, tab, request.controller) : null;
		if (!current()) return;
		await ensureTabEngine(tab);
		if (!current()) return;
		const source = downloaded ? downloaded.file : file;
		const format = await callTabWASM(tab, "ofdgoDetectFormat", source);
		if (!current()) return;
		const bytes = await documentBytes(source);
		tab.state.ofdBytes = bytes;
		tab.state.sourcePDF = format === "pdf";
		const name = (downloaded ? downloaded.name : file.name) || `ofdgo.${format}`;
		let convertedDoc;
		if (!current()) {
			return;
		}
		if (format === "pdf") {
			setProgress("正在转换文档", 25, "", tab);
			await ensureTabEngine(tab);
			if (!current()) return;
			let converted;
			try {
				converted = await openWithCredentials(await fontManager.files(fontManager.records()), tab.state.openSeq, bytes, false, true, tab);
			} finally {
				if (current()) {
					tab.state.exportRequestID = 0;
					if (isForegroundDocument(tab)) el.cancelExportButton.hidden = true;
				}
			}
			if (!current()) return;
			convertedDoc = converted;
		}
		await ensureTabEngine(tab);
		if (!current()) return;
		const doc = convertedDoc || await openWithCredentials(await fontManager.files(fontManager.userFonts), tab.state.openSeq, bytes, false, false, tab);
		if (!current()) return;
		tab.state.saveHandle = format === "ofd" && !remote ? handle || null : null;
		tab.state.saveStamp = tab.state.saveHandle && !nativeHost ? {size: file.size, modified: file.lastModified} : null;
		tab.state.conversionWarnings = [];
		tab.state.fileName = (name || "ofdgo.ofd").replace(/\.pdf$/i, ".ofd");
		if (remote && !/\.ofd$/i.test(tab.state.fileName)) tab.state.fileName += ".ofd";
		tab.state.doc = doc;
		tab.state.pageIndex = remote?.pageIndex || 0;
		tab.initialLoad = true;
		tab.opening = null;
		tab.busy = false;
		accepted = true;
		if (isForegroundDocument(tab)) {
			const openSeq = tab.state.openSeq;
			await openDocument({doc, pageIndex: tab.state.pageIndex, activateActions: true, resetScroll: true, openSeq});
			if (isForegroundDocument(tab) && openSeq === tab.state.openSeq) tab.initialLoad = false;
		}
		else tab.status = "文档已打开";
		if (tab.state.sourcePDF) completePDF(tab);
	} catch (err) {
		if (current()) {
			tab.error = err.name === "AbortError" ? "打开已取消" : err.message;
			tab.status = tab.error;
			if (isForegroundDocument(tab)) { setStatus(tab.status); setEmpty(tab.error); }
		}
	} finally {
		if (tab.opening === request) tab.opening = null;
		if (!accepted && !tab.closed) setBusy(false, "", 0, "", tab);
		renderDocumentTabs();
		const source = tab.state.ofdBytes;
		if (!accepted && tab.worker) {
			await markWASMExited(tab.state.wasmSeq, null, tab);
			if (nativeHost) await nativeHost.release(source);
			tab.state.ofdBytes = null;
		}
	}
	return accepted;
}

function isDocumentFile(file) {
	return /\.(ofd|pdf)$/i.test(file.name || "");
}

async function openSelectedFonts(event) {
	const tab = documentTab();
	if (tab.fontTask || document.body.hasAttribute("data-busy")) {
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
	tab.fontTask = true;
	setBusy(true, "正在读取字体", 10, STATUS.fonts, tab);
	try {
		const fonts = [];
		for (let i = 0; i < files.length; i += 1) {
			setProgress(`正在读取字体 · ${i + 1} / ${files.length}`, 10 + Math.round(i / files.length * 60), "", tab);
			const file = files[i];
			fonts.push(fontManager.record(file.name, new Uint8Array(await file.arrayBuffer()), "upload"));
		}
		const { saved, changed } = await fontManager.add(fonts);
		if (changed) {
			await applyFontChange(tab);
			if (isForegroundDocument(tab) && resourceElements.Panel.open) await resourceTask(loadManagedResources);
			if (isForegroundDocument(tab) && settingsElements.Panel.open && settings.invoice) {
				await loadEditorFonts();
				if (isForegroundDocument(tab)) renderDocumentSettings();
			}
		}
		tab.status = saved ? "字体已保存" : "字体未保存，仅本次可用";
		if (isForegroundDocument(tab)) setStatus(tab.status);
	} catch (err) {
		tab.status = err.message;
		if (isForegroundDocument(tab)) showError(err, false);
	} finally {
		tab.fontTask = false;
		setBusy(false, "", 0, "", tab);
	}
}

async function loadLocalFonts() {
	const tab = documentTab(), state = tab.state;
	if (tab.fontTask || document.body.hasAttribute("data-busy")) {
		return;
	}
	if (!fontManager.canReadLocal()) {
		setStatus("浏览器不支持系统字体");
		return;
	}
	tab.fontTask = true;
	setBusy(true, state.doc ? "正在匹配字体" : "正在请求授权", 12, state.doc ? STATUS.fonts : "正在请求授权", tab);
	try {
		await nextFrame();
		const available = await fontManager.queryLocal();
		if (tab.closed) return;
		if (!state.doc) {
			tab.status = available.length ? `已授权${available.length}种字体` : "暂无系统字体";
			if (isForegroundDocument(tab)) setStatus(tab.status);
			return;
		}
		if (await loadDocumentLocalFonts(available, undefined, tab)) {
			await applyFontChange(tab);
		}
	} catch (err) {
		tab.status = err?.name === "NotAllowedError" ? "系统字体未授权" : String(err.message || err);
		if (isForegroundDocument(tab)) setStatus(tab.status);
	} finally {
		tab.fontTask = false;
		setBusy(false, "", 0, "", tab);
	}
}

async function requestLocalFontsBeforeOpen() {
	const tab = documentTab();
	if (!fontManager.canReadLocal() || fontManager.catalogLoaded || fontManager.permission === "denied") {
		return;
	}
	if (tab.fontTask) return;
	tab.fontTask = true;
	setBusy(true, "正在请求授权", 12, "正在请求授权", tab);
	try {
		const available = await fontManager.queryLocal();
		tab.status = available.length ? `已授权${available.length}种字体` : "暂无系统字体";
		if (isForegroundDocument(tab)) setStatus(tab.status);
	} catch (err) {
		tab.status = err?.name === "NotAllowedError" ? "系统字体未授权" : String(err.message || err);
		if (isForegroundDocument(tab)) setStatus(tab.status);
	} finally {
		tab.fontTask = false;
		setBusy(false, "", 0, "", tab);
	}
}

async function autoLoadDocumentLocalFonts(openSeq, tab = documentTab()) {
	const state = tab.state;
	if (!state.doc?.fonts?.some((font) => !font.embedded) || !fontManager.canReadLocal() || fontManager.permission === "denied") {
		return false;
	}
	setProgress("正在匹配字体", 62, STATUS.fonts, tab);
	try {
		const available = fontManager.catalogLoaded ? fontManager.catalog : await fontManager.queryLocal();
		if (tab.closed || openSeq !== state.openSeq) {
			return false;
		}
		return await loadDocumentLocalFonts(available, openSeq, tab);
	} catch (err) {
		tab.status = err?.name === "NotAllowedError" ? "系统字体未授权" : String(err.message || err);
		if (isForegroundDocument(tab) && openSeq === state.openSeq) setStatus(tab.status);
		return false;
	}
}

async function loadDocumentLocalFonts(available, openSeq, tab = documentTab()) {
	const state = tab.state, wasmSeq = state.wasmSeq, doc = state.doc;
	const current = () => !tab.closed && wasmSeq === state.wasmSeq && doc === state.doc && (openSeq === undefined || openSeq === state.openSeq);
	if (!current()) return false;
	const docFonts = state.doc?.fonts || [];
	if (!docFonts.length || docFonts.every((font) => font.embedded)) {
		tab.localFonts = [];
		tab.status = docFonts.length ? "字体均已内嵌" : "暂无文档字体";
		if (isForegroundDocument(tab)) {
			fontManager.localFonts = tab.localFonts;
			setStatus(tab.status);
			updateFontSummary();
			renderFontList();
		}
		return false;
	}
	const selected = await selectLocalFonts(available, tab);
	if (!current()) {
		return false;
	}
	const emptyStatus = available.length === 0 ? "暂无系统字体" : "暂无匹配字体";
	const fonts = [];
	for (let i = 0; i < selected.length; i += 1) {
		setProgress(`正在读取字体 · ${i + 1} / ${selected.length}`, 20 + Math.round(i / selected.length * 60), "", tab);
		const item = selected[i];
		const data = await fontManager.read(item);
		if (!current()) {
			return false;
		}
		fonts.push(fontManager.record(fontManager.localName(item), data, "browser"));
	}
	tab.localFonts = fonts;
	tab.status = fonts.length ? `已加载${fonts.length}种字体` : emptyStatus;
	if (isForegroundDocument(tab)) {
		fontManager.localFonts = fonts;
		setStatus(tab.status);
		updateFontSummary();
		renderFontList();
	} else tab.configurePending = true;
	return fonts.length > 0;
}

async function selectLocalFonts(fonts, tab = documentTab()) {
	const manager = Object.create(fontManager);
	manager.localFonts = isForegroundDocument(tab) ? fontManager.localFonts : tab.localFonts;
	const available = manager.localEntries(fonts);
	const matched = await callTabWASM(tab, "ofdgoMatchFontFiles", [...available.keys()]);
	return [...new Set(matched.map((name) => available.get(name)))];
}

function scheduleFontSync() {
	state.fontSyncPending = true;
	syncUserFonts();
}

async function syncUserFonts() {
	const tab = documentTab(), state = tab.state;
	if (!state.ready || !state.fontSyncPending || state.fontSyncing || tab.fontTask || document.hidden || document.body.hasAttribute("data-busy") || document.activeElement?.classList.contains("font-name-input")) {
		return;
	}
	state.fontSyncPending = false;
	state.fontSyncing = true;
	tab.fontTask = true;
	setBusy(true, "正在同步字体", null, "", tab);
	try {
		if (await fontManager.restore()) {
			await applyFontChange(tab);
		}
		if (isForegroundDocument(tab) && !state.doc) {
			setStatus(STATUS.ready);
		}
	} catch {
		tab.status = "字体读取失败";
		if (isForegroundDocument(tab)) setStatus(tab.status);
	} finally {
		state.fontSyncing = false;
		tab.fontTask = false;
		setBusy(false, "", 0, "", tab);
	}
}

function updateFontSummary() {
	const fonts = fontManager.records();
	const total = fonts.length;
	const enabled = fonts.filter((font) => font.enabled).length;
	el.availableFontSummary.textContent = total ? `${enabled}/${total}` : "0";
}

async function applyFontChange(tab = documentTab()) {
	const state = tab.state;
	for (const target of workspace.tabs) if (target !== tab) target.configurePending = true;
	if (!isForegroundDocument(tab)) { if (!tab.closed) tab.configurePending = true; return; }
	updateFontSummary();
	renderFontList();
	if (!state.doc) {
		return;
	}
	if (canvasEditor.input || canvasEditor.crop) {
		const openSeq = state.openSeq;
		const fonts = await fontManager.files(fontManager.records());
		if (!isForegroundDocument(tab) || openSeq !== state.openSeq) { if (!tab.closed) tab.configurePending = true; return; }
		await callTabWASM(tab, "ofdgoConfigure", fonts, state.renderAnnotations);
		if (!isForegroundDocument(tab) || openSeq !== state.openSeq) { if (!tab.closed) tab.configurePending = true; return; }
		state.fontRenderPending = true;
		setStatus(pageStatus(state.pageIndex, state.doc.pageCount));
		return;
	}
	await refreshFontPreview(true, tab);
}

async function refreshFontPreview(fontsChanged, tab = documentTab()) {
	if (!isForegroundDocument(tab)) { if (!tab.closed) tab.configurePending = true; return; }
	const state = tab.state;
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
	const tab = documentTab(), state = tab.state;
	if (!state.fontRenderPending || !state.ready || document.body.hasAttribute("data-busy") || canvasEditor.input || canvasEditor.crop) return;
	await refreshFontPreview(false, tab);
}

async function toggleAnnotations() {
	if (document.body.hasAttribute("data-busy")) {
		return;
	}
	const tab = documentTab(), state = tab.state;
	const previous = state.renderAnnotations;
	state.renderAnnotations = !state.renderAnnotations;
	updateAnnotationButton();
	if (!state.doc) {
		return;
	}
	try {
		await refreshPageDisplay();
	} catch (err) {
		state.renderAnnotations = previous;
		if (isForegroundDocument(tab)) {
			updateAnnotationButton();
			showError(err, false);
		}
	}
}

function updateDisplayControls() {
	const disabled = !state.ready || !state.doc || state.exporting || document.body.hasAttribute("data-busy");
	el.renderVectorButton.disabled = el.renderRasterButton.disabled = disabled || state.editing;
	el.renderVectorButton.setAttribute("data-pressed", String(pageView.displayMode() === "svg"));
	el.renderRasterButton.setAttribute("data-pressed", String(pageView.displayMode() === "raster"));
	updateDPIControl();
}

async function refreshPageDisplay(anchor = pageView.scaleAnchor(0)) {
	const selection = state.editing ? { index: canvasEditor.selected?.index ?? state.pageIndex, ids: canvasEditor.items().map(item => item.id) } : null;
	await openDocument({
		pageIndex: state.pageIndex, fitMode: state.fitMode, scale: state.scale,
		skipAutoFonts: true, reuseSession: true, keepPreview: true, keepSearch: true,
		previewAnchor: anchor, selection, throwError: true,
	});
}

async function changeDisplayMode(mode = state.renderMode) {
	const tab = documentTab(), state = tab.state;
	if (document.body.hasAttribute("data-busy")) {
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
		state.pageCache.clear();
		if (isForegroundDocument(tab)) {
			el.imageDPI.value = String(previous.dpi);
			pageView.resetPageLoading();
			updateDisplayControls();
			showError(err, false);
		}
	}
}

async function changeFont(font, changes) {
	const tab = documentTab(), state = tab.state;
	if (tab.fontTask || document.body.hasAttribute("data-busy")) {
		return;
	}
	tab.fontTask = true;
	setBusy(true, "正在更新字体", null, STATUS.fonts, tab);
	try {
		await fontManager.change(font, changes);
		await applyFontChange(tab);
		if (isForegroundDocument(tab) && !state.doc) {
			setStatus(STATUS.ready);
		}
	} catch {
		tab.status = "字体更新失败";
		if (isForegroundDocument(tab)) { renderFontList(); setStatus(tab.status); }
	} finally {
		tab.fontTask = false;
		setBusy(false, "", 0, "", tab);
	}
}

function renderFontList() {
	refreshEditorFonts();
	if (state.ready) loadEditorFonts(false);
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
		remove.className = "icon-button remove-button font-delete";
		remove.type = "button";
		remove.title = "删除字体";
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
	updateFontPermissionHint();
}

function updateFontPermissionHint() {
	el.fontPermissionHint.hidden = !fontManager.canReadLocal() || fontManager.permission === "granted";
}

function clearSecurityForms() {
	finishResourcePick(false);
	if (sealPreview) finishSealPosition(false);
	finishCredentials(null);
	clearEncryptionForm();
	el.encryptionPanel.close();
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
	if (request?.tab) request.tab.credentialType = el.credentialsType.value;
	credentialRequest = null;
	if (request?.tab) request.tab.credentials = null;
	el.credentialsPassword.value = "";
	el.credentialsUser.value = "";
	el.credentialsCertificate.value = el.credentialsKey.value = "";
	el.credentialsKeyPassword.value = "";
	if (el.credentialsPanel.open) el.credentialsPanel.close();
	if (request && !request.tab?.closed && (request.tab?.opening || request.openSeq === state.openSeq || request.tab === workspace.active)) request.resolve(credentials);
	else {
		credentials?.password.fill(0);
		credentials?.key?.fill(0);
		credentials?.keyPassword?.fill(0);
		request?.resolve(null);
	}
}

function requestCredentials(code, openSeq, pdf = false, tab = documentTab()) {
	return new Promise(resolve => {
		const request = { resolve, openSeq, tab };
		tab.credentials?.resolve(null);
		tab.credentials = request;
		const setup = () => {
			credentialRequest = request;
			el.credentialsTypeRow.hidden = pdf;
			el.credentialsUserRow.hidden = pdf;
			el.credentialsType.value = pdf ? "password" : tab.credentialType || "password";
			el.credentialsUser.value = "";
			updateCredentialsType();
			el.credentialsStatus.textContent = code === "invalidCredentials" ? "解锁失败，请重试" : "";
			el.progressPanel.hidden = true;
		};
		if (documentDialogs) documentDialogs.openFor(tab, el.credentialsPanel, setup);
		else { setup(); el.credentialsPanel.showModal(); }
		if (isForegroundDocument(tab)) (el.credentialsType.value === "certificate" ? el.credentialsCertificate : el.credentialsPassword).focus();
	});
}

async function openWithCredentials(fonts, openSeq, data = state.ofdBytes, importing = false, pdf = false, tab = documentTab()) {
	const state = tab.state;
	const callWASM = (name, ...args) => callTabWASM(tab, name, ...args);
	pdf ||= !importing && state.sourcePDF;
	const current = () => !tab.closed && (Boolean(tab.opening) || openSeq === state.openSeq);
	let credentials = null;
	try {
		while (current()) {
			try {
				if (pdf) {
					state.pdfComplete = false;
					tab.pdfTask = null;
					return await callWASM("ofdgoOpenPDF", data, fonts, credentials?.password || null, state.renderAnnotations);
				}
				return importing ? await callWASM("ofdgoLoadImport", data?.nativeFile ? data : data.slice(), credentials)
					: await callWASM("ofdgoOpen", data, fonts, state.renderAnnotations, credentials);
			} catch (err) {
				if (!current() || !(pdf ? err.code === "pdfPassword" : ["credentialsRequired", "invalidCredentials"].includes(err.code))) throw err;
				const code = pdf ? credentials ? "invalidCredentials" : "credentialsRequired" : err.code;
				credentials?.password.fill(0);
				credentials?.key?.fill(0);
				credentials?.keyPassword?.fill(0);
				credentials = await requestCredentials(code, openSeq, pdf, tab);
				if (!credentials || !current()) {
					const canceled = new Error("打开已取消");
					canceled.name = "AbortError";
					throw canceled;
				}
				setProgress(pdf ? "正在读取文档" : "正在解析文档", null, "", tab);
				if (isForegroundDocument(tab)) el.progressPanel.hidden = false;
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
	const tab = documentTab(), state = tab.state;
	const callWASM = (name, ...args) => callTabWASM(tab, name, ...args);
	if (!state.ofdBytes && !state.editorInfo) {
		return;
	}
	const openSeq = options.openSeq || advanceDocumentSequence();
	const previewPosition = options.previewScroll && state.pageWindow ? pageView.pageWindowAnchor(state.pageWindow) : null;
	const viewport = options.keepPreview ? el.viewerPanel.getBoundingClientRect() : null;
	const previewIDs = viewport ? [...state.visiblePages].filter((index) => {
		const bounds = pageView.pageShell(index)?.getBoundingClientRect();
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
		let doc = options.doc || (options.reuseSession
			? await callWASM("ofdgoConfigure", fonts, state.renderAnnotations)
			: await openWithCredentials(fonts, openSeq));
		if (openSeq !== state.openSeq) {
			return;
		}
		if (options.versionID !== undefined && options.versionID !== (doc.documentVersion || "")) {
			doc = await callWASM("ofdgoSelectVersion", options.versionID);
			if (openSeq !== state.openSeq) return;
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
			pageView.setContinuous(window.matchMedia("(max-width: 640px)").matches);
		}
		state.fitMode = options.fitMode || (state.pageLayout > 1 || !state.continuous && pageCount === 1 ? "height" : "width");
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
				activateActions: options.activateActions,
			});
			return;
		}
		if (openSeq !== state.openSeq) {
			return;
		}
		const pageFonts = state.sourcePDF && !nativeHost && !state.editorInfo ? await fontManager.files(fontManager.records()) : fonts;
		if (state.editing) await initializeTextFont(tab);
		if (openSeq !== state.openSeq) return;
		tab.worker.configurePages?.(!state.editorInfo && !doc.encrypted && state.ofdBytes ? {
			file: state.ofdBytes, fonts: pageFonts, pdf: state.sourcePDF, fontNames: state.sourcePDF ? [...fontManager.localEntries().keys()] : [], annotations: state.renderAnnotations, document: doc.documentIndex, version: doc.documentVersion
		} : null);
		if (options.keepPreview) {
			pageView.resetPageLoading();
			state.pageCache.clear();
			if (thumbnailPreviews) {
				for (const page of doc.pages) {
					const preview = thumbnailPreviews.get(page.id);
					if (preview) state.pageCache.set(page.index, { ...preview, previewOnly: true });
				}
			}
			const indices = new Set([pageIndex, ...previewIDs.map(id => pageIndexByID(doc.pages, id)).filter(index => index >= 0)]);
			for (const index of indices) {
				await pageView.loadPageData(index, { openSeq, priority: 0, refresh: true });
				if (openSeq !== state.openSeq) {
					return;
				}
			}
		}
		if (options.clearSelection) canvasEditor.clear();
		if (options.selection) canvasEditor.pendingSelection = options.selection;
		pageView.resetPageFlow(options.keepPreview || options.keepCache);
		pageView.renderPageList();
		if (options.keepPreview || options.resetScroll || el.outlineList.childElementCount === 0) {
			renderOutlines(!options.keepPreview && !options.restoreTab);
		}
		renderMeta(options.keepPreview);
		pageView.renderPageFlow();
		if (options.keepPreview) {
			for (const [index, page] of state.pageCache) {
				if (!page.previewOnly) pageView.mountPageSVG(index, page, openSeq);
			}
			pageView.releasePageResources();
		}
		if (options.resetScroll) {
			el.viewerPanel.scrollLeft = 0;
			el.viewerPanel.scrollTop = 0;
			el.navigationContent.scrollLeft = 0;
			el.navigationContent.scrollTop = 0;
			el.metaPanel.scrollLeft = 0;
			el.metaPanel.scrollTop = 0;
		}
		pageView.applyFit(false);
		if (options.keepPreview) {
			if (options.previewAnchor) {
				pageView.restoreScaleAnchor(options.previewAnchor);
			} else if (options.previewScroll) {
				Object.assign(el.viewerPanel, options.previewScroll);
				if (previewPosition && state.pageWindow) pageView.syncPageWindow(state.pageWindow,
					state.pageWindow.offsets[previewPosition.index] + previewPosition.offset);
			} else {
				pageView.scrollToPage(pageIndex);
			}
			setStatus(pageStatus(pageIndex, pageCount));
		}
		await nextFrame();
		if (openSeq !== state.openSeq) {
			return;
		}
		const page = options.keepPreview ? state.pageCache.get(pageIndex)
			: await pageView.renderPage(pageIndex, { keepBusy: true, scroll: false, openSeq });
		if (page && options.resetScroll) {
			canvasEditor.focus();
		}
		pageView.queueNearbyPages(pageIndex, openSeq);
		loadDocumentDetails(openSeq);
		if (page && options.activateActions) {
			state.openActionEnabled = true;
			state.openActionDocument = !state.editing;
			state.openActionPage = state.editing ? state.doc.pages[state.pageIndex]?.id : null;
			state.openActionTask = null;
			scheduleOpenActions();
		}
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
		if (openSeq === state.openSeq || !isForegroundDocument(tab)) setBusy(false, "", 0, "", tab);
	}
}

function completePDF(tab) {
	const state = tab.state, source = state.ofdBytes, wasmSeq = state.wasmSeq;
	if (state.pdfComplete || !state.sourcePDF || state.editorInfo) return Promise.resolve();
	if (tab.pdfTask?.wasmSeq === wasmSeq && tab.pdfTask.source === source) return tab.pdfTask.promise;
	const task = {wasmSeq, source};
	tab.pdfTask = task;
	const current = () => !tab.closed && tab.pdfTask === task && state.ofdBytes === source && state.wasmSeq === wasmSeq;
	task.promise = (async () => {
		while (current()) {
			await new Promise(resolve => setTimeout(resolve, 0));
			if (!current()) break;
			const result = await callTabWASM(tab, "ofdgoLoadPDF");
			if (!current()) break;
			if (!result.done) continue;
			state.pdfComplete = true;
			tab.pdfTask = null;
			state.conversionWarnings = JSON.parse(result.warnings || "null") || [];
			if (!state.editorInfo) Object.assign(state.doc, result.doc);
			if (isForegroundDocument(tab)) loadDocumentDetails(state.openSeq);
			return;
		}
		throw new DOMException("文档已关闭", "AbortError");
	})();
	task.promise.catch(err => {
		if (!current()) return;
		tab.pdfTask = null;
		if (err.name !== "AbortError") {
			tab.status = err.message;
			if (isForegroundDocument(tab)) showError(err, false);
		}
	});
	return task.promise;
}

async function loadDocumentDetails(openSeq, details) {
	const tab = documentTab(), state = tab.state;
	const callWASM = (name, ...args) => callTabWASM(tab, name, ...args);
	if (openSeq !== state.openSeq || !state.doc) return;
	if (state.sourcePDF && !state.pdfComplete && !state.editorInfo) { completePDF(tab); return; }
	try {
		details ||= await callWASM("ofdgoDocumentInfo");
		if (openSeq !== state.openSeq) return;
		Object.assign(state.doc, details, { detailsPending: false, detailsError: "" });
		renderMeta();
		while (openSeq === state.openSeq) {
			await waitForPaint();
			if (openSeq !== state.openSeq) return;
			const info = await callWASM("ofdgoDocumentInfo", true);
			if (openSeq !== state.openSeq) return;
			if (!info.detailsPending) {
				const knownFonts = new Set((state.doc.fonts || []).map(font => font.id));
				const missingFonts = info.fonts?.some(font => font.used > 0 && font.status === "missing" && !knownFonts.has(font.id));
				Object.assign(state.doc, info, { detailsPending: false, detailsError: "" });
				renderMeta();
				if (missingFonts && fontManager.permission === "granted" && !state.editorInfo && !document.body.hasAttribute("data-busy")) {
					setBusy(true);
					try {
						if (await autoLoadDocumentLocalFonts(openSeq, tab) && openSeq === state.openSeq) await applyFontChange(tab);
					} finally {
						setBusy(false, "", 0, "", tab);
					}
				}
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

function settingsField(container, label, id, value = "", type = "text", unit = "") {
	const row = document.createElement("label"), title = document.createElement("span");
	row.className = "form-row";
	title.textContent = label;
	const field = document.createElement(type === "textarea" ? "textarea" : "input");
	field.id = `settings${id}`;
	if (type === "textarea") field.rows = 3;
	else field.type = type;
	if (type === "number") field.step = "any";
	if (type === "checkbox") field.checked = Boolean(value);
	else field.value = value ?? "";
	row.append(title, field);
	if (unit) {
		const wrapper = document.createElement("span"), suffix = document.createElement("span");
		wrapper.className = "form-unit";
		suffix.textContent = unit;
		wrapper.append(field, suffix);
		row.append(wrapper);
	}
	container.append(row);
	return field;
}

function revealPanelTab(container) {
	const selected = container.querySelector('[data-selected="true"]');
	if (!selected || !container.clientWidth) return;
	const box = selected.getBoundingClientRect(), viewport = container.getBoundingClientRect();
	if (box.left < viewport.left) container.scrollLeft -= Math.ceil(viewport.left - box.left);
	else if (box.right > viewport.right) container.scrollLeft += Math.ceil(box.right - viewport.right);
}

function renderPanelTabs(container, choices, value, change) {
	const focused = container.contains(document.activeElement) ? document.activeElement.dataset.value : null;
	const left = container.scrollLeft;
	const buttons = choices.map(([key, label, id]) => {
		const button = document.createElement("button");
		button.type = "button";
		button.className = "button";
		button.textContent = label;
		button.dataset.value = key;
		button.setAttribute("data-selected", String(key === value));
		button.tabIndex = key === value ? 0 : -1;
		if (id) button.id = id;
		button.addEventListener("click", async () => {
			const focused = container.contains(document.activeElement);
			await change(key);
			if (focused && container.clientWidth && (document.activeElement === document.body || container.contains(document.activeElement))) container.querySelector(`[data-value="${key}"]`)?.focus({preventScroll: true});
		});
		button.addEventListener("keydown", event => {
			if (event.isComposing || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
			const index = buttons.indexOf(button);
			const next = event.key === "ArrowRight" ? (index + 1) % buttons.length : event.key === "ArrowLeft" ? (index + buttons.length - 1) % buttons.length : event.key === "Home" ? 0 : event.key === "End" ? buttons.length - 1 : -1;
			if (next < 0 || buttons[next].disabled) return;
			event.preventDefault();
			buttons[next].click();
			container.querySelector(`[data-value="${choices[next][0]}"]`)?.focus({preventScroll: true});
		});
		return button;
	});
	container.replaceChildren(...buttons);
	container.scrollLeft = left;
	if (focused != null) container.querySelector(`[data-value="${focused}"]`)?.focus({preventScroll: true});
	revealPanelTab(container);
}

function renderSettingsTabs() {
	const choices = settings.invoice ? [["basic", "基本", "Basic"], ["items", "明细", "Items"], ["fields", "字段", "Fields"], ["appearance", "票面", "Appearance"], ["xml", "原文", "XML"]].map(([key, label, id]) => [key, label, `settingsInvoiceTab${id}`])
		: SETTINGS_KINDS.filter(([key]) => key !== "actions" || state.editing);
	settingsElements.Tabs.hidden = Boolean(settings.resource) || settings.locked || settings.invoice && !invoiceCurrent();
	if (settingsElements.Tabs.hidden) { settingsElements.Tabs.replaceChildren(); return; }
	renderPanelTabs(settingsElements.Tabs, choices, settings.invoice ? invoicePanel.tab : settings.kind, kind => {
		if (settings.busy) return;
		const body = document.querySelector("#settingsBody");
		if (settings.invoice) {
			if (kind === invoicePanel.tab) return;
			invoicePanel.views.set(JSON.stringify([invoicePanel.key, invoicePanel.tab]), {scroll: body.scrollTop,
				tables: [...body.querySelectorAll(".invoice-table-scroll")].map(node => node.scrollLeft),
				expanded: [...body.querySelectorAll("details")].map(node => node.open)});
			invoicePanel.tab = kind;
			renderDocumentSettings();
			return;
		}
		if (kind === settings.kind) return;
		rememberSettingsDraft();
		settings.views.set(settings.kind, {selected: settings.selected, page: settings.page, scope: settings.scope, object: settings.object, path: [...settings.path], scroll: body.scrollTop,
			expanded: [...settingsElements.Content.querySelectorAll("details")].map(node => node.open)});
		settings.kind = kind;
		const saved = settings.views.get(kind);
		settings.selected = saved?.selected ?? -1;
		if (saved) for (const key of ["page", "scope", "object", "path"]) settings[key] = saved[key];
		settings.view = "";
		return loadDocumentSettings();
	});
}

function settingsSelect(container, label, id, choices, value, change) {
	const row = document.createElement("label"), title = document.createElement("span"), field = document.createElement("select");
	row.className = "form-row";
	title.textContent = label;
	field.id = `settings${id}`;
	field.replaceChildren(...choices.map(([value, text]) => new Option(text, String(value))));
	if (value != null && choices.some(([key]) => String(key) === String(value))) field.value = String(value);
	if (change) field.addEventListener("change", () => { rememberSettingsDraft(); change(field.value); });
	row.append(title, field);
	container.append(row);
	return field;
}

function settingsButton(label, action, disabled = false, id = "", container = settingsElements.Actions, reason = "") {
	const button = document.createElement("button");
	button.type = "button";
	button.className = "button";
	button.textContent = label;
	button.disabled = disabled;
	if (disabled && reason) button.title = reason;
	if (id) button.id = `settings${id}`;
	button.addEventListener("click", async () => {
		try { await action(); }
		catch (error) { settingsElements.Status.textContent = settings.invoice ? invoiceIssues([error.message]) : error.message; }
	});
	container.append(button);
	return button;
}

function settingsControlRow(field) {
	const row = document.createElement("div"), label = field.closest(".form-row");
	row.className = "settings-control-row";
	label.replaceWith(row);
	row.append(label);
	return row;
}

function settingsSection(label) {
	const section = document.createElement("section"), title = document.createElement("div"), actions = document.createElement("div");
	section.className = "settings-section";
	title.className = "settings-section-title";
	title.textContent = label;
	actions.className = "settings-section-actions";
	section.append(title);
	settingsElements.Content.append(section);
	return {section, actions};
}

function settingsSnapshot() {
	return Object.fromEntries([...settingsElements.Content.querySelectorAll("input,select,textarea")].filter(field => field.id).map(field => [field.id,
		field.type === "checkbox" ? field.checked : field.type === "file" ? [...field.files] : field.multiple ? [...field.selectedOptions].map(option => option.value) : field.value]));
}

function rememberSettingsDraft() {
	if (settings.renderedKind === "invoices") return;
	if (!state.editing || !settings.view || settings.busy) return;
	const values = settingsSnapshot();
	const changed = Object.keys(values).some(id => {
		if (["settingsTemplateReference", "settingsLayerObjects", "settingsTargetPage", "settingsTargetObject"].includes(id)) return false;
		const value = values[id], before = settings.baseline[id];
		return Array.isArray(value) ? value.length !== (before?.length || 0) || value.some((entry, index) => entry !== before?.[index]) : value !== before;
	});
	if (!changed) { settings.drafts.delete(settings.view); return; }
	settings.drafts.set(settings.view, {values, scroll: document.querySelector("#settingsBody")?.scrollTop || 0,
		expanded: [...settingsElements.Content.querySelectorAll("details")].map(node => node.open)});
}

function restoreSettingsDraft() {
	const kind = settings.kind;
	const entry = kind === "bookmarks" ? settings.data.bookmarks?.[settings.selected]?.Name
		: kind === "templates" ? settings.data.page.templates?.[settings.selected]?.ID
		: kind === "layers" ? settings.data.page.layers?.[settings.selected]?.ID : settings.selected;
	settings.view = JSON.stringify([kind, settings.resource?.id || "", kind === "actions" ? [settings.scope, settings.page, settings.object, settings.path] : ["templates", "layers"].includes(kind) ? settings.page : "", entry ?? -1]);
	settings.baseline = settingsSnapshot();
	const draft = settings.drafts.get(settings.view);
	if (!draft) return;
	if (kind === "actions" && draft.values.settingsActionType) {
		renderActionFields(draft.values.settingsActionType, settings.actions[settings.selected] || {});
	}
	for (const [id, value] of Object.entries(draft.values)) {
		const field = document.getElementById(id);
		if (!field || !settingsElements.Content.contains(field)) continue;
		if (field.type === "checkbox") field.checked = value;
		else if (field.type === "file") {
			const transfer = new DataTransfer();
			for (const file of value) transfer.items.add(file);
			field.files = transfer.files;
		} else if (field.multiple) for (const option of field.options) option.selected = value.includes(option.value);
		else if (field.tagName !== "SELECT" || [...field.options].some(option => option.value === value)) field.value = value;
	}
	if (document.querySelector("#settingsDestType")) updateDestinationFields();
	for (const key of ["FillColor", "StrokeColor"]) {
		const field = document.querySelector(`#settings${key}`);
		if (field) field.closest("label").hidden = settingsValue(key + "Mode") !== "solid";
	}
	const body = document.querySelector("#settingsBody");
	for (const [index, node] of [...settingsElements.Content.querySelectorAll("details")].entries()) node.open = draft.expanded?.[index] || false;
	if (body) body.scrollTop = draft.scroll;
}

async function closeDocumentSettings() {
	if (settings.busy) return;
	rememberSettingsDraft();
	if ((settings.drafts.size || [...invoicePanel.drafts.values()].some(draft => draft.dirty.size)) && !await requestConfirmation(settings.invoice ? "发票尚未应用，放弃更改？" : "配置尚未应用，放弃更改？")) return;
	settingsElements.Panel.close();
}

function renderReadonlySettings() {
	for (const field of settingsElements.Content.querySelectorAll("input,select,textarea")) {
		field.disabled = true;
		if (field.id === "settingsTemplateReference") { field.disabled = false; continue; }
		const row = field.closest(".form-row");
		if (!row || row.hidden || field.type === "file") { if (field.type === "file" && row) row.hidden = true; continue; }
		const value = document.createElement("span");
		value.className = "settings-readonly";
		value.textContent = field.tagName === "SELECT" ? field.selectedOptions[0]?.textContent || "—" : field.type === "checkbox" ? field.checked ? "是" : "否" : field.value || field.placeholder || "—";
		field.hidden = true;
		field.after(value);
	}
}

function settingsValue(id) {
	const field = document.querySelector(`#settings${id}`);
	return field?.type === "checkbox" ? field.checked : field?.value ?? "";
}

async function openDocumentSettings(invoice = false) {
	if (!state.doc || !state.ready || state.exporting || document.body.hasAttribute("data-busy")) return;
	if (resourceManager.pick?.settings) { finishResourcePick(); return; }
	invoicePanel.drafts.clear();
	invoicePanel.views.clear();
	invoicePanel.key = "";
	settings.invoice = invoice;
	settingsElements.Title.textContent = invoice ? "电子发票" : "文档配置";
	settingsElements.Panel.classList.toggle("invoice-dialog", invoice);
	settingsElements.Context.replaceChildren();
	settingsElements.Content.replaceChildren();
	settingsElements.Actions.replaceChildren();
	settings.page = state.pageIndex;
	settings.object = canvasEditor.selected?.index === settings.page && !canvasEditor.selected.scoped ? canvasEditor.selected.id : "";
	settings.path = state.outlineSelection || [];
	settings.selected = -1;
	settings.drafts.clear();
	settings.views.clear();
	settings.view = "";
	settings.resource = null;
	settings.locked = false;
	if (!SETTINGS_KINDS.some(([key]) => key === settings.kind) || settings.kind === "actions" && !state.editing) settings.kind = "bookmarks";
	settingsElements.Panel.showModal();
	await loadDocumentSettings();
}

async function openResourceSettings(kind, id) {
	if (!state.doc || !state.ready || document.body.hasAttribute("data-busy")) return;
	settings.invoice = false;
	settings.resource = {kind, id};
	settings.selected = -1;
	settings.drafts.clear();
	settings.views.clear();
	settings.view = "";
	const page = Number(resourceElements.Page.value);
	settings.page = resourceElements.Page.value !== "" && Number.isInteger(page) && page >= 0 && page < state.doc.pages.length ? page : state.pageIndex;
	settings.object = resourceElements.Object.value || "";
	settings.kind = kind;
	settings.locked = true;
	settingsElements.Panel.showModal();
	await loadDocumentSettings();
}

async function loadDocumentSettings() {
	const tab = documentTab(), state = tab.state, settings = tab.settings;
	const callWASM = (name, ...args) => callTabWASM(tab, name, ...args);
	const sequence = ++settings.sequence, openSeq = state.openSeq;
	const task = settings.loadTask = {};
	const kind = settings.invoice ? "invoices" : settings.kind;
	settings.busy = true;
	renderSettingsTabs();
	settingsElements.Form.inert = true;
	settingsElements.Status.textContent = "正在读取信息";
	settingsElements.Status.title = "";
	try {
		if (kind === "invoices") { await loadInvoiceSettings(sequence, openSeq); return; }
		const data = ["bookmarks", "actions", "extensions"].includes(kind) ? await callWASM("ofdgoDocumentNavigation", kind) : {};
		if (!isForegroundDocument(tab) || sequence !== settings.sequence || openSeq !== state.openSeq || !settingsElements.Panel.open) return;
		data.resources = ["bookmarks", "extensions"].includes(kind) ? [] : await callWASM("ofdgoResources") || [];
		if (!isForegroundDocument(tab) || sequence !== settings.sequence || openSeq !== state.openSeq || !settingsElements.Panel.open) return;
		if (kind === "actions" && state.editing) {
			if (settings.scope === "object") {
				const page = await pageView.loadPageData(settings.page);
				if (!isForegroundDocument(tab) || sequence !== settings.sequence || openSeq !== state.openSeq || !settingsElements.Panel.open) return;
				data.objects = (page.objects || []).filter(item => item.type !== "Annotation" && !item.scoped);
				if (!data.objects.some(item => item.id === settings.object)) settings.object = data.objects[0]?.id || "";
			}
			if (settings.scope === "outline" && !settings.path.length) settings.path = outlineSettingsChoices()[0]?.[0] ? JSON.parse(outlineSettingsChoices()[0][0]) : [];
			data.actions = (settings.scope === "object" && !settings.object || settings.scope === "outline" && !settings.path.length) ? [] : await callWASM("ofdgoActionList", settings.scope, settings.page, settings.object, settings.path) || [];
		}
		if (kind === "templates" || kind === "layers") {
			data.page = await callWASM("ofdgoPageSettings", settings.page, kind);
			if (!isForegroundDocument(tab) || sequence !== settings.sequence || openSeq !== state.openSeq || !settingsElements.Panel.open) return;
			if (kind === "layers" && state.editing) {
				const page = await pageView.loadPageData(settings.page);
				data.objects = (page.objects || []).filter(item => item.type !== "Annotation" && !item.scoped);
			}
		}
		if (kind === "ColorSpace" || kind === "DrawParam") {
			data.resource = settings.resource.id ? await callWASM("ofdgoStyleResource", kind, settings.resource.id) : {};
			if (!isForegroundDocument(tab) || sequence !== settings.sequence || openSeq !== state.openSeq || !settingsElements.Panel.open) return;
			if (state.editing) {
				const page = await pageView.loadPageData(settings.page);
				data.objects = (page.objects || []).filter(item => ["TextObject", "PathObject", "CompositeObject", "CompositeGraphicUnit"].includes(item.type) && !item.scoped);
			}
		}
		if (!isForegroundDocument(tab) || sequence !== settings.sequence || openSeq !== state.openSeq || !settingsElements.Panel.open) return;
		settings.data = data;
		settings.actions = data.actions || [];
		settingsElements.Status.textContent = "";
		renderDocumentSettings();
	} catch (error) {
		if (isForegroundDocument(tab) && sequence === settings.sequence && openSeq === state.openSeq) {
			settingsElements.Content.replaceChildren();
			settingsElements.Actions.replaceChildren();
			settingsButton("关闭", closeDocumentSettings);
			settingsElements.Status.textContent = error.message;
		}
	} finally {
		if (settings.loadTask === task) {
			delete settings.loadTask;
			settings.busy = false;
			if (isForegroundDocument(tab)) settingsElements.Form.inert = false;
		}
	}
}

function renderDocumentSettings() {
	settingsElements.Context.replaceChildren();
	settingsElements.Content.replaceChildren();
	settingsElements.Actions.replaceChildren();
	const kind = settings.invoice ? "invoices" : settings.kind;
	settings.renderedKind = kind;
	const location = document.querySelector("#settingsLocation"), doc = state.doc;
	if (location) {
		const current = doc?.documents?.find(entry => entry.index === doc.documentIndex) || doc?.documents?.[doc?.documentIndex || 0];
		location.textContent = `${current?.title || current?.name || `文档${(doc?.documentIndex || 0) + 1}`}${doc?.documentVersion ? ` · 版本${doc.documentVersion}` : ""}`;
		location.hidden = !(doc?.documents?.length > 1 || doc?.documentVersion);
	}
	settingsElements.Title.textContent = {ColorSpace: "色彩设置", DrawParam: "样式设置", invoices: "电子发票"}[kind] || "文档配置";
	settingsElements.Panel.classList.toggle("invoice-dialog", kind === "invoices");
	renderSettingsTabs();
	if (kind === "invoices") {
		renderInvoiceSettings();
		settingsButton("关闭", closeDocumentSettings, false, "Close").classList.add("settings-close");
		const view = invoicePanel.views.get(JSON.stringify([invoicePanel.key, invoicePanel.tab]));
		const body = document.querySelector("#settingsBody");
		if (view) for (const [index, node] of [...body.querySelectorAll("details")].entries()) node.open = view.expanded[index] || false;
		body.scrollTop = view?.scroll || 0;
		for (const [index, node] of [...body.querySelectorAll(".invoice-table-scroll")].entries()) node.scrollLeft = view?.tables[index] || 0;
		return;
	}
	if (kind === "actions") renderActionSettings();
	else if (kind === "bookmarks") renderBookmarkSettings();
	else if (kind === "extensions") renderExtensionSettings();
	else if (kind === "templates") renderTemplateSettings();
	else if (kind === "layers") renderLayerSettings();
	else if (kind === "ColorSpace" || kind === "DrawParam") renderStyleResourceSettings(kind);
	if (!state.editing) renderReadonlySettings();
	restoreSettingsDraft();
	const move = document.querySelector("#settingsMoveObjects");
	if (move) move.disabled = !document.querySelector("#settingsLayerObjects").selectedOptions.length;
	const target = document.querySelector("#settingsTargetObject");
	if (target) target.dispatchEvent(new Event("change"));
	const lastActions = settingsElements.Content.querySelector(".settings-section:last-child > .settings-section-actions");
	if (lastActions) settingsElements.Actions.append(lastActions);
	settingsButton("关闭", closeDocumentSettings, false, "Close").classList.add("settings-close");
	const draft = settings.drafts.get(settings.view);
	const view = settings.views.get(kind);
	if (!draft && view) for (const [index, node] of [...settingsElements.Content.querySelectorAll("details")].entries()) node.open = view.expanded[index] || false;
	document.querySelector("#settingsBody").scrollTop = draft?.scroll ?? settings.views.get(kind)?.scroll ?? 0;
}

function invoiceSourceKey(source = {}) {
	return JSON.stringify([source.kind || "", source.id || "", source.fileLoc || "", source.nodePath || ""]);
}

function invoiceScope() {
	return {
		documentIndex: state.doc.documentIndex || 0,
		version: state.doc.documentVersion || "",
		...(state.editing ? { revision: state.editorInfo.revision } : {})
	};
}

function invoiceCandidates(invoices) {
	const counts = new Map(), result = [];
	for (const invoice of invoices || []) {
		for (const source of new Map((invoice.sources || []).map(value => [invoiceSourceKey(value), value])).values()) {
			const sourceKey = invoiceSourceKey(source);
			const index = counts.get(sourceKey) || 0;
			counts.set(sourceKey, index + 1);
			result.push({ invoice, source, index, key: JSON.stringify([sourceKey, index]) });
		}
	}
	return result;
}

async function loadInvoiceSettings(sequence, openSeq) {
	const tab = documentTab(), state = tab.state, settings = tab.settings, invoicePanel = tab.invoice;
	const callWASM = (name, ...args) => callTabWASM(tab, name, ...args);
	const cached = !state.editing && invoiceDocuments.get(state.doc);
	const data = cached?.openSeq === openSeq && cached.data ? cached.data : await callWASM("ofdgoInvoices");
	if (!isForegroundDocument(tab) || sequence !== settings.sequence || openSeq !== state.openSeq || !settingsElements.Panel.open) return;
	invoicePanel.entries = invoiceCandidates(data.report?.invoices);
	invoicePanel.formats = data.formats || [];
	invoicePanel.issues = (data.report?.issues || []).filter(issue => issue.code !== "missing-data");
	if (!invoicePanel.drafts.has(invoicePanel.key) && !invoicePanel.entries.some(entry => entry.key === invoicePanel.key)) invoicePanel.key = invoicePanel.entries[0]?.key || "";
	const entry = invoicePanel.entries.find(entry => entry.key === invoicePanel.key);
	if (entry && !invoicePanel.drafts.has(entry.key)) {
		const scope = invoiceScope();
		const detail = await callWASM("ofdgoInvoice", JSON.stringify({ source: entry.source, index: entry.index, ...scope }));
		if (!isForegroundDocument(tab) || sequence !== settings.sequence || openSeq !== state.openSeq || !settingsElements.Panel.open) return;
		invoicePanel.drafts.set(entry.key, invoiceDraft(detail, entry, scope));
	}
	settingsElements.Status.textContent = "";
	renderDocumentSettings();
}

function invoiceDraft(detail, entry, scope) {
	return {
		detail, invoice: structuredClone(detail.invoice), entry, scope,
		baselineFields: structuredClone(detail.fields || []),
		dirty: new Set(), changes: new Map(),
		addedItems: new Set(),
		bindings: structuredClone(detail.bindings || []), xml: detail.xml || "", page: state.pageIndex, object: "", field: "",
		layout: { font: "", fontSize: 4, pageWidth: 210, pageHeight: 297, margin: 15 }, synchronize: !!detail.bindings?.length && !!detail.capabilities?.sync,
		options: { name: "", encoding: { schemaRef: "", context: {}, units: [] } },
		fragments: {}, item: 0
	};
}

function invoiceCurrent() {
	return invoicePanel.drafts.get(invoicePanel.key);
}

function invoiceDirty(draft, section) {
	draft.dirty.add(section);
	const button = document.querySelector("#settingsInvoiceApply");
	if (button) button.disabled = !state.editing;
	const reset = document.querySelector("#settingsInvoiceReset");
	if (reset) reset.disabled = !state.editing;
}

function invoiceInput(container, label, id, value, change, disabled = false, type = "text", unit = "") {
	const field = settingsField(container, label, `Invoice${id}`, value, type, unit);
	field.disabled = disabled;
	field.addEventListener("input", () => change(type === "checkbox" ? field.checked : field.value));
	if (!state.editing && type !== "textarea") {
		const text = document.createElement("span");
		text.className = "settings-readonly";
		text.id = field.id;
		text.textContent = type === "checkbox" ? value ? "是" : "否" : value || "-";
		field.replaceWith(text);
		return text;
	}
	if (!state.editing && type === "textarea") {
		field.disabled = false;
		field.readOnly = true;
	}
	return field;
}

function invoiceValue(invoice, field, item = -1) {
	const keys = field.split(".");
	let value = keys[0] === "items" ? invoice.items?.[item] : invoice;
	if (keys[0] === "items") keys.shift();
	for (const key of keys) value = value?.[key];
	return value ?? "";
}

function invoiceSetValue(invoice, field, item, value) {
	const keys = field.split(".");
	let target = keys[0] === "items" ? invoice.items[item] : invoice;
	if (keys[0] === "items") keys.shift();
	for (const key of keys.slice(0, -1)) target = target[key] ||= {};
	target[keys.at(-1)] = value;
}

function invoiceIssues(issues, details = false) {
	const codes = {
		"appearance-not-synchronized": "票面未同步",
		"signature-review-required": "需重新验签"
	};
	const messages = {
		"unsupported invoice XML root or version": "暂不支持此发票格式或版本",
		"unsupported eInvoice namespace or version": "暂不支持此发票格式或版本",
		"unsupported EInvoice version": "暂不支持此发票版本",
		"EInvoice requires a unique versioned Header": "发票头信息不完整",
		"invoice text exceeds original reference slot capacity": "原框空间不足，请缩短文字",
		"invoice slot requires Font or FontData with the missing characters:": "字体缺字，请更换字体",
		"invoice references cross columns or reverse reading order": "图元跨列，无法重排",
		"invoice references do not form adjacent aligned text rows": "图元未对齐，无法重排"
	};
	return [...new Set((issues || []).map(issue => {
		const message = typeof issue === "string" ? issue : issue.message || issue.code;
		const translated = messages[message] || Object.entries(messages).find(([prefix]) => prefix.endsWith(":") && message?.startsWith(prefix))?.[1] || message;
		if (typeof issue === "string") return details ? message : translated;
		if (details) return [issue.source?.fileLoc || issue.source?.id || issue.source?.kind, issue.source?.nodePath, issue.field, message].filter(Boolean).join("：");
		return [issue.field, codes[issue.code] || translated].filter(Boolean).join("：");
	}).filter(Boolean))].join("\n");
}

function renderInvoiceSettings() {
	const tab = documentTab(), state = tab.state, invoicePanel = tab.invoice;
	const context = settingsElements.Context, content = settingsElements.Content;
	settingsElements.Status.title = "";
	const draft = invoiceCurrent();
	const sources = [...new Map(invoicePanel.entries.map(entry => [invoiceSourceKey(entry.source), entry.source])).entries()];
	if (sources.length) settingsSelect(context, "来源", "InvoiceSource", sources.map(([key, source]) => [key, source.fileLoc || source.id || source.kind]), draft?.entry ? invoiceSourceKey(draft.entry.source) : null, value => {
		invoicePanel.key = invoicePanel.entries.find(entry => invoiceSourceKey(entry.source) === value).key;
		loadDocumentSettings();
	});
	const candidates = invoicePanel.entries.filter(entry => !draft?.entry || invoiceSourceKey(entry.source) === invoiceSourceKey(draft.entry.source));
	const choices = candidates.map(entry => [entry.key, entry.invoice.number || entry.invoice.code || `${entry.invoice.kind} · ${entry.index + 1}`]);
	if (invoicePanel.drafts.has("new")) choices.push(["new", "新建"]);
	if (choices.length) settingsSelect(context, "发票", "InvoiceEntry", choices, invoicePanel.key, value => { invoicePanel.key = value; loadDocumentSettings(); });
	if (state.editing) {
		const actions = document.createElement("div");
		actions.className = "invoice-actions";
		context.append(actions);
		settingsButton("新建", newInvoiceDraft, false, "InvoiceNew", actions);
		settingsButton("导入", () => readInvoiceFile(async text => {
			const value = await newInvoiceDraft();
			if (!value || !isForegroundDocument(tab)) return;
			value.xml = text;
			value.importing = true;
			value.dirty.add("xml");
			invoicePanel.tab = "xml";
			renderDocumentSettings();
		}), false, "InvoiceImport", actions);
	}
	if (!draft) {
		settingsElements.Status.textContent = invoiceIssues(invoicePanel.issues) || "暂无发票";
		settingsElements.Status.title = invoiceIssues(invoicePanel.issues, true);
		return;
	}
	const body = document.createElement("div");
	body.id = "invoiceTabContent";
	content.append(body);
	if (invoicePanel.tab === "basic") renderInvoiceBasic(body, draft);
	else if (invoicePanel.tab === "items") renderInvoiceItems(body, draft);
	else if (invoicePanel.tab === "fields") renderInvoiceFields(body, draft);
	else if (invoicePanel.tab === "appearance") renderInvoiceAppearance(body, draft);
	else renderInvoiceXML(body, draft);
	settingsButton("校验", () => validateInvoiceDraft(draft), false, "InvoiceValidate");
	settingsButton("导出", () => downloadBytes(new TextEncoder().encode(JSON.stringify(draft.invoice, null, 2)), "application/json", "invoice.json"), false, "InvoiceExportJSON");
	if (state.editing) {
		settingsButton("应用", () => applyInvoiceDraft(draft), !draft.dirty.size, "InvoiceApply");
		if (draft.entry) settingsButton("同步", async () => {
			if (!draft.detail.capabilities?.sync || draft.dirty.has("bindings")) {
				invoicePanel.tab = "appearance";
				draft.status = draft.dirty.has("bindings") ? "请先应用票面绑定" : draft.bindings.length ? draft.detail.capabilities?.reasons?.sync || "票面关联不可用" : "票面未绑定，请选择字段和图元后绑定";
				renderDocumentSettings();
				return;
			}
			if (draft.dirty.size) {
				await applyInvoiceDraft(draft, true);
				return;
			}
			await commitInvoice(draft, { operation: "sync" });
		}, false, "InvoiceSync");
		settingsButton("还原", () => resetInvoiceDraft(draft), !draft.dirty.size, "InvoiceReset");
		if (draft.entry) settingsButton("删除", async () => {
			if (draft.dirty.size) throw new Error("请先应用或还原草稿");
			if (await requestConfirmation("删除此来源中的发票数据？页面将保留" + invoiceOtherDraftWarning(draft))) await commitInvoice(draft, { operation: "delete" });
		}, !draft.detail.capabilities?.delete, "InvoiceDelete");
	}
	settingsElements.Status.textContent = draft.status || invoiceIssues(draft.detail.issues) || "";
	if (!draft.status) settingsElements.Status.title = invoiceIssues(draft.detail.issues, true);
}

async function newInvoiceDraft() {
	const tab = documentTab(), state = tab.state, settings = tab.settings, invoicePanel = tab.invoice;
	if (!state.editing) return;
	if (invoicePanel.drafts.get("new")?.dirty.size && !await requestConfirmation("放弃尚未应用的新建发票？")) return;
	const format = invoicePanel.formats.find(value => value.create && value.standard !== invoiceReferenceStandard) || invoicePanel.formats.find(value => value.create);
	if (!format) throw new Error("暂无可新建票种");
	const invoice = { kind: format.kind, standard: format.standard, version: format.version, buyer: {}, seller: {}, items: [], facts: [] };
	const scope = invoiceScope();
	const sequence = settings.sequence;
	const detail = await callTabWASM(tab, "ofdgoInvoice", JSON.stringify({ invoice, ...scope }));
	if (!isForegroundDocument(tab) || sequence !== settings.sequence || !settingsElements.Panel.open) return;
	invoicePanel.drafts.set("new", invoiceDraft(detail, null, scope));
	invoicePanel.key = "new";
	invoicePanel.tab = "basic";
	renderDocumentSettings();
	return invoiceCurrent();
}

function renderInvoiceBasic(container, draft) {
	const fields = (draft.detail.fields || []).filter(field => field.item < 0 && !field.field.startsWith("facts.") && !field.field.startsWith("items."));
	const labels = {
		kind: "票种", standard: "规范", version: "版本", number: "号码", code: "代码", date: "日期",
		currency: "币种", status: "状态", amount: "金额", taxAmount: "税额", totalAmount: "合计", remarks: "备注",
		name: "名称", taxId: "税号", address: "地址", phone: "电话", bank: "银行", account: "账号"
	};
	for (const [group, label] of [["basic", "票据"], ["buyer", "购方"], ["seller", "销方"]]) {
		const title = document.createElement("h3"), grid = document.createElement("div");
		title.className = "invoice-group-title";
		title.textContent = label;
		grid.className = "settings-grid";
		container.append(title, grid);
		for (const field of fields.filter(field => field.group === group)) {
			const value = invoiceValue(draft.invoice, field.field);
			const display = field.field === "standard" ? draft.detail.standardLabel || value : value;
			invoiceInput(grid, labels[field.field.split(".").at(-1)] || field.label, `Field_${field.field.replaceAll(".", "_")}`, display, value => {
				invoiceSetValue(draft.invoice, field.field, -1, value);
				invoiceDirty(draft, "model");
			}, !state.editing || !(field.editable || !draft.entry && field.field === "currency" && invoiceIsXBRL(draft.invoice)) || !!draft.entry && !draft.detail.capabilities?.edit).title = display !== value ? value : field.label;
		}
	}
	if (!draft.entry) renderInvoiceAdvanced(container, draft);
}

function invoiceIsXBRL(invoice) {
	return invoice.standard?.startsWith("http://xbrl.mof.gov.cn/taxonomy/") || false;
}

function renderInvoiceAdvanced(container, draft) {
	const tab = documentTab();
	const details = document.createElement("details"), summary = document.createElement("summary");
	summary.textContent = "高级";
	details.className = "invoice-advanced";
	details.open = draft.advanced || false;
	details.addEventListener("toggle", () => { draft.advanced = details.open; });
	details.append(summary);
	container.append(details);
	const formats = invoicePanel.formats.filter(value => value.create);
	const selected = formats.findIndex(value => value.kind === draft.invoice.kind && value.standard === draft.invoice.standard && value.version === draft.invoice.version);
	settingsSelect(details, "票种", "InvoiceFormat", formats.map((value, index) => [index, `${{ inv: "增值税", einv: "电子票", atr: "航空票", rai: "铁路票" }[value.kind] || value.kind} · ${value.version} · ${value.standard === invoiceReferenceStandard ? "标引" : invoiceIsXBRL(value) ? "XBRL" : "XML"}`]), selected, async value => {
		try {
			const format = formats[Number(value)];
			if (draft.invoice.standard !== format.standard) draft.options.encoding.schemaRef = "";
			Object.assign(draft.invoice, { kind: format.kind, standard: format.standard, version: format.version });
			invoiceDirty(draft, "model");
			await invoiceRefreshFields(draft);
			if (isForegroundDocument(tab) && invoiceCurrent() === draft) renderDocumentSettings();
		} catch (error) {
			if (isForegroundDocument(tab)) settingsElements.Status.textContent = error.message;
		}
	});
	invoiceInput(details, "名称", "Name", draft.options.name, value => { draft.options.name = value; invoiceDirty(draft, "model"); });
	const encoding = draft.options.encoding;
	if (invoiceIsXBRL(draft.invoice)) {
		invoiceInput(details, "模式", "SchemaRef", encoding.schemaRef, value => { encoding.schemaRef = value; invoiceDirty(draft, "model"); }).title = "schemaRef";
		for (const [key, label] of [["id", "标识"], ["identifier", "主体"], ["scheme", "方案"], ["instant", "时点"], ["startDate", "起日"], ["endDate", "止日"]]) {
			invoiceInput(details, label, `Context_${key}`, encoding.context[key], value => { encoding.context[key] = value; invoiceDirty(draft, "model"); }).title = `context.${key}`;
		}
		for (const [index, unit] of encoding.units.entries()) {
			const row = document.createElement("div");
			row.className = "invoice-unit";
			details.append(row);
			for (const [key, label] of [["id", "标识"], ["namespace", "空间"], ["name", "单位"]]) {
				invoiceInput(row, label, `Unit_${index}_${key}`, unit[key], value => { unit[key] = value; invoiceDirty(draft, "model"); }).title = key === "namespace" ? "命名空间" : label;
			}
			settingsButton("删除", () => {
				encoding.units.splice(index, 1);
				invoiceDirty(draft, "model");
				renderDocumentSettings();
			}, false, `InvoiceUnitDelete${index}`, row);
		}
		settingsButton("添加", () => {
			encoding.units.push({ id: "", namespace: "", name: "" });
			invoiceDirty(draft, "model");
			renderDocumentSettings();
		}, false, "InvoiceUnitAdd", details);
	}
	for (const [key, label] of draft.invoice.standard === "EInvoice" ? [["headerXML", "票头"], ["sellerAuthenticationXML", "认证"], ["taxSupervisionXML", "监制"], ["taxBureauSignatureXML", "签名"]] : []) {
		invoiceInput(details, label, `Fragment_${key}`, draft.fragments[key], value => {
			draft.fragments[key] = value;
			invoiceDirty(draft, "model");
		}, false, "textarea");
		settingsButton("导入", () => readInvoiceFile(text => {
			draft.fragments[key] = text;
			invoiceDirty(draft, "model");
			renderDocumentSettings();
		}), false, `InvoiceFragmentImport_${key}`, details);
	}
}

function renderInvoiceItems(container, draft) {
	const tab = documentTab();
	const items = draft.invoice.items ||= [];
	const editable = state.editing && (!draft.entry ? invoicePanel.formats.find(value => value.kind === draft.invoice.kind && value.standard === draft.invoice.standard && value.version === draft.invoice.version)?.items : draft.detail.capabilities?.edit && draft.detail.capabilities?.items);
	draft.item = Math.max(0, Math.min(draft.item, items.length - 1));
	const columns = [["name", "名称"], ["specification", "规格"], ["unit", "单位"], ["quantity", "数量"], ["unitPrice", "单价"], ["amount", "金额"], ["taxRate", "税率"], ["taxAmount", "税额"]];
	const scroll = document.createElement("div"), table = document.createElement("table");
	const head = table.createTHead().insertRow();
	scroll.className = "invoice-table-scroll";
	table.className = "invoice-table";
	table.id = "settingsInvoiceItems";
	for (const label of ["选择", ...columns.map(entry => entry[1])]) {
		const cell = document.createElement("th");
		cell.scope = "col";
		cell.textContent = label;
		head.append(cell);
	}
	const body = table.createTBody();
	for (const [index, item] of items.entries()) {
		const row = body.insertRow(), radio = document.createElement("input");
		radio.type = "radio";
		radio.name = "invoiceItem";
		radio.checked = index === draft.item;
		radio.addEventListener("change", () => {
			draft.item = index;
			renderDocumentSettings();
		});
		row.insertCell().append(radio);
		for (const [key, label] of columns) {
			const input = document.createElement("input");
			input.type = "text";
			input.value = item[key] || "";
			input.id = `settingsInvoiceItem_${index}_${key}`;
			const descriptor = draft.detail.fields?.find(field => field.field === `items.${key}` && field.item === index);
			input.disabled = !editable || !descriptor?.editable;
			input.addEventListener("input", () => {
				item[key] = input.value;
				invoiceDirty(draft, "model");
			});
			const cell = row.insertCell();
			if (!state.editing) {
				const text = document.createElement("span");
				text.className = "settings-readonly";
				text.textContent = item[key] || "-";
				cell.append(text);
			} else cell.append(input);
		}
	}
	scroll.append(table);
	container.append(scroll);
	if (state.editing) {
		const actions = document.createElement("div");
		actions.className = "invoice-actions";
		container.append(actions);
		const reorder = async (direction) => {
			if (draft.changes.size) throw new Error("请先应用原字段修改，再调整明细顺序");
			const next = draft.item + direction;
			[items[next], items[draft.item]] = [items[draft.item], items[next]];
			draft.item = next;
			invoiceDirty(draft, "model");
			await invoiceRefreshFields(draft);
			if (isForegroundDocument(tab) && invoiceCurrent() === draft) renderDocumentSettings();
		};
		settingsButton("添加", async () => {
			const item = {};
			items.push(item);
			draft.addedItems.add(item);
			draft.item = items.length - 1;
			invoiceDirty(draft, "model");
			await invoiceRefreshFields(draft);
			if (isForegroundDocument(tab) && invoiceCurrent() === draft) renderDocumentSettings();
		}, !editable, "InvoiceItemAdd", actions);
		settingsButton("删除", async () => {
			if (draft.changes.size) throw new Error("请先应用原字段修改，再删除明细");
			items.splice(draft.item, 1);
			invoiceDirty(draft, "model");
			await invoiceRefreshFields(draft);
			if (isForegroundDocument(tab) && invoiceCurrent() === draft) renderDocumentSettings();
		}, !editable || !items.length, "InvoiceItemDelete", actions);
		settingsButton("上移", () => reorder(-1), !editable || draft.item === 0 || !items.length, "InvoiceItemUp", actions);
		settingsButton("下移", () => reorder(1), !editable || draft.item >= items.length - 1, "InvoiceItemDown", actions);
	}
}

async function invoiceRefreshFields(draft) {
	const tab = documentTab(), invoicePanel = tab.invoice;
	const invoice = structuredClone(draft.invoice);
	const detail = await callTabWASM(tab, "ofdgoInvoice", JSON.stringify({ invoice, ...draft.scope }));
	if (!tab.closed && [...invoicePanel.drafts.values()].includes(draft) && JSON.stringify(draft.invoice) === JSON.stringify(invoice)) draft.detail.fields = detail.fields;
}

function renderInvoiceFields(container, draft) {
	const facts = (draft.invoice.facts || []).map((fact, index) => ({ fact, field: `facts.${index}`, item: -1 }));
	for (const [item, value] of (draft.invoice.items || []).entries()) {
		for (const [index, fact] of (value.fields || []).entries()) {
			facts.push({ fact, field: `items.fields.${index}`, item });
		}
	}
	const options = facts.map(({ fact, item }, index) => [index, `${item >= 0 ? `明细${item + 1} · ` : ""}${fact.name || "未命名"}${fact.context ? ` · ${fact.context}` : ""} · ${index + 1}`]);
	const owners = [...(!draft.entry && draft.invoice.standard !== invoiceReferenceStandard ? [[-1, "基本"]] : []), ...(draft.invoice.items || []).flatMap((item, index) => !draft.entry || draft.addedItems.has(item) ? [[index, `明细${index + 1}`]] : [])];
	if (state.editing && owners.length) {
		if (!owners.some(([index]) => index === draft.factOwner)) draft.factOwner = owners[0][0];
		settingsSelect(container, "归属", "InvoiceFactOwner", owners, draft.factOwner, value => { draft.factOwner = Number(value); });
		settingsButton("添加", () => {
			const owner = draft.factOwner;
			const target = owner < 0 ? draft.invoice.facts ||= [] : draft.invoice.items[owner].fields ||= [];
			target.push({ name: "", namespace: "", value: "", context: "", unit: "" });
			draft.fact = owner < 0 ? target.length - 1 : (draft.invoice.facts || []).length + draft.invoice.items.slice(0, owner).reduce((sum, item) => sum + (item.fields || []).length, 0) + target.length - 1;
			invoiceDirty(draft, "model");
			renderDocumentSettings();
		}, false, "InvoiceFactAdd", container);
	}
	if (!options.length) return;
	draft.fact = Math.min(draft.fact || 0, facts.length - 1);
	settingsSelect(container, "字段", "InvoiceFact", options, draft.fact, value => {
		draft.fact = Number(value);
		renderDocumentSettings();
	});
	const { fact, field, item } = facts[draft.fact];
	const patchKey = invoiceSourceKey(fact.source);
	const change = draft.changes.get(patchKey);
	const descriptor = draft.detail.fields?.find(value => value.field === field && value.item === item);
	if (descriptor?.bindable) {
		draft.field = JSON.stringify([field, item]);
		const binding = draft.bindings.find(value => value.field === field && value.item === item);
		draft.binding = 0;
		draft.page = binding?.page ?? state.pageIndex;
		draft.object = binding?.objectId || "";
	}
	const fresh = !draft.entry || item >= 0 && draft.addedItems.has(draft.invoice.items[item]);
	const writable = state.editing && (fresh || descriptor?.editable && !!fact.source?.nodePath);
	for (const [key, label] of [["name", "名称"], ["namespace", "空间"], ["context", "语境"], ["unit", "单位"], ["decimals", "小数"], ["precision", "精度"]]) {
		invoiceInput(container, label, `FactMeta_${key}`, fact[key], value => {
			fact[key] = value;
			invoiceDirty(draft, "model");
		}, !fresh || !state.editing).title = key === "namespace" ? "命名空间" : key === "context" ? "上下文标识" : label;
	}
	if (fact.source?.nodePath) invoiceInput(container, "路径", "FactPath", fact.source.nodePath, () => {}, true);
	const update = (key, value) => {
		if (fresh) {
			fact[key] = value;
			if (key === "nil" && value) {
				fact.value = "";
				document.querySelector("#settingsInvoiceFactValue").value = "";
			}
			if (key === "value") {
				fact.nil = false;
				document.querySelector("#settingsInvoiceFactNil").checked = false;
			}
			invoiceDirty(draft, "model");
			return;
		}
		const patch = draft.changes.get(patchKey) || {
			source: structuredClone(fact.source), value: fact.value || "", nil: !!fact.nil,
			expectedValue: fact.value || "", expectedNil: !!fact.nil
		};
		patch[key] = value;
		if ((key === "nil" || key === "delete") && value) {
			patch.value = "";
			document.querySelector("#settingsInvoiceFactValue").value = "";
		}
		if (key === "nil" && value) {
			patch.delete = false;
			document.querySelector("#settingsInvoiceFactDelete").checked = false;
		}
		if (key === "delete" && value) {
			patch.nil = false;
			document.querySelector("#settingsInvoiceFactNil").checked = false;
		}
		if (key === "value") {
			patch.nil = false;
			patch.delete = false;
			document.querySelector("#settingsInvoiceFactNil").checked = false;
			document.querySelector("#settingsInvoiceFactDelete").checked = false;
		}
		draft.changes.set(patchKey, patch);
		invoiceDirty(draft, "fields");
	};
	invoiceInput(container, "数值", "FactValue", change?.value ?? fact.value, value => update("value", value), !writable, "textarea");
	if (writable && !fresh && draft.invoice.standard === invoiceReferenceStandard) {
		const fonts = state.doc.fonts || [];
		const libraryFonts = fontManager.editorFonts().filter(font => !font.disabled);
		const font = settingsSelect(container, "字体", "InvoiceFactFont", [["", "原字体"], ...fonts.map(font => [font.id, font.fontName || font.familyName || font.id]), ...libraryFonts.map(font => [`file:${font.id}`, font.fullName || font.name])], change?.font || "", value => update("font", value));
		settingsButton("添加", () => openFontFile(el.fontInput), false, "InvoiceFactAddFont", settingsControlRow(font));
	}
	invoiceInput(container, "空值", "FactNil", change?.nil ?? fact.nil, value => update("nil", value), !writable || draft.invoice.standard === invoiceReferenceStandard, "checkbox");
	if (!fresh) invoiceInput(container, "删除", "FactDelete", change?.delete || false, value => update("delete", value), !writable || draft.invoice.standard === invoiceReferenceStandard, "checkbox");
	else if (state.editing) settingsButton("删除", () => {
		const index = Number(field.split(".").at(-1));
		(item < 0 ? draft.invoice.facts : draft.invoice.items[item].fields).splice(index, 1);
		invoiceDirty(draft, "model");
		renderDocumentSettings();
	}, false, "InvoiceFactDelete", container);
}

function renderInvoiceAppearance(container, draft) {
	const tab = documentTab(), state = tab.state, settings = tab.settings, invoicePanel = tab.invoice;
	const capabilities = draft.detail.capabilities || {};
	const creatingReference = !draft.entry && draft.invoice.standard === invoiceReferenceStandard;
	const writable = state.editing && !!draft.entry;
	const fields = (draft.detail.fields || []).filter(field => field.bindable);
	const choices = fields.map(field => [JSON.stringify([field.field, field.item]), `${field.item >= 0 ? `明细${field.item + 1} · ` : ""}${field.label}`]);
	if (!choices.some(([key]) => key === draft.field)) {
		draft.field = choices[0]?.[0] || "";
		const binding = draft.bindings.find(binding => JSON.stringify([binding.field, binding.item]) === draft.field);
		draft.binding = 0;
		draft.page = binding?.page ?? state.pageIndex;
		draft.object = binding?.objectId || "";
	}
	settingsSelect(container, "字段", "InvoiceBindingField", choices, draft.field, value => {
		draft.field = value;
		draft.binding = 0;
		const binding = draft.bindings.find(binding => JSON.stringify([binding.field, binding.item]) === value);
		draft.page = binding?.page ?? state.pageIndex;
		draft.object = binding?.objectId || "";
		renderDocumentSettings();
	});
	const bound = draft.bindings.filter(binding => JSON.stringify([binding.field, binding.item]) === draft.field);
	if (bound.length) {
		draft.binding = Math.min(draft.binding || 0, bound.length - 1);
		settingsSelect(container, "引用", "InvoiceBinding", bound.map((binding, index) => [index, `第${binding.page + 1}页 · ${binding.objectId}`]), draft.binding, value => {
			draft.binding = Number(value);
			draft.page = bound[draft.binding].page;
			draft.object = bound[draft.binding].objectId;
			renderDocumentSettings();
		});
	}
	settingsPages(container, "InvoicePage", draft.page, value => {
		draft.page = Number(value);
		draft.object = "";
		renderDocumentSettings();
	});
	const object = settingsSelect(container, "图元", "InvoiceObject", [[draft.object, draft.object || "选择"]], draft.object, value => {
		draft.object = value;
		renderDocumentSettings();
	});
	object.disabled = !writable || !capabilities.bind;
	const actions = document.createElement("div");
	actions.className = "invoice-actions";
	container.append(actions);
	settingsButton("定位", async () => {
		if (!draft.object) return;
		settings.page = draft.page;
		await locateSettingsObjects([draft.object]);
	}, !draft.object, "InvoiceLocate", actions);
	if (state.editing) {
		settingsButton("选取", async () => {
			settings.page = draft.page;
			await beginSettingsPick("invoice");
		}, !writable || !capabilities.bind, "InvoicePick", actions, capabilities.reasons?.bind);
		settingsButton("绑定", () => {
			if (!draft.object || !draft.field) throw new Error("请选择字段和图元");
			const [field, item] = JSON.parse(draft.field);
			if (draft.bindings.some(binding => binding.page === draft.page && binding.objectId === draft.object && (binding.field !== field || binding.item !== item))) throw new Error("此图元已绑定其他字段");
			draft.bindings = draft.bindings.filter(binding => binding.field !== field || binding.item !== item);
			draft.bindings.push({ field, item, page: draft.page, objectId: draft.object });
			invoiceDirty(draft, "bindings");
			renderDocumentSettings();
		}, !writable || !capabilities.bind || !draft.field, "InvoiceBind", actions, capabilities.reasons?.bind);
		settingsButton("解绑", () => {
			draft.bindings = draft.bindings.filter(binding => !(JSON.stringify([binding.field, binding.item]) === draft.field && binding.page === draft.page && binding.objectId === draft.object));
			invoiceDirty(draft, "bindings");
			renderDocumentSettings();
		}, !writable || !capabilities.bind || !bound.length, "InvoiceUnbind", actions);
		settingsButton("清空", () => {
			draft.bindings = [];
			invoiceDirty(draft, "bindings");
			renderDocumentSettings();
		}, !writable || !capabilities.bind || !draft.bindings.length, "InvoiceUnbindAll", actions);
	}
	if (writable || state.editing && creatingReference) {
		const details = document.createElement("details"), summary = document.createElement("summary");
		summary.textContent = "生成";
		details.className = "invoice-advanced";
		details.append(summary);
		container.append(details);
		details.open = draft.layoutOpen || false;
		details.addEventListener("toggle", () => { draft.layoutOpen = details.open; });
		const fonts = state.doc.fonts || [];
		const libraryFonts = fontManager.editorFonts().filter(font => !font.disabled);
		const font = settingsSelect(details, "字体", "InvoiceFont", [["", "选择"], ...fonts.map(font => [font.id, font.fontName || font.familyName || font.id]), ...libraryFonts.map(font => [`file:${font.id}`, font.fullName || font.name])], draft.fontChoice || draft.layout.font, value => {
			draft.fontChoice = value;
			draft.layout.font = value.startsWith("file:") ? "" : value;
			if (creatingReference) invoiceDirty(draft, "model");
			renderDocumentSettings();
		});
		const fontRow = settingsControlRow(font);
		settingsButton("添加", () => openFontFile(el.fontInput), false, "InvoiceAddFont", fontRow);
		settingsButton("嵌入", async () => {
			const font = fonts.find(value => value.id === draft.layout.font);
			if (!font) throw new Error("请选择字体");
			const before = invoiceScope();
			const refresh = () => {
				if (tab.closed) return;
				const next = {documentIndex: state.doc.documentIndex || 0, version: state.doc.documentVersion || "", revision: state.editorInfo.revision};
				if (next.documentIndex !== before.documentIndex || next.version !== before.version) return;
				for (const value of invoicePanel.drafts.values()) {
					if (value.scope.documentIndex === before.documentIndex && value.scope.version === before.version && value.scope.revision === before.revision) {
						value.scope.revision = next.revision;
					}
				}
				if (isForegroundDocument(tab) && settingsElements.Panel.open && settings.invoice) renderDocumentSettings();
			};
			await openFontBinding(font);
			if (!isForegroundDocument(tab)) return;
			if (fontBindingElements.Panel.open) fontBindingElements.Panel.addEventListener("close", refresh, { once: true });
			else refresh();
		}, !draft.layout.font || !!fonts.find(font => font.id === draft.layout.font)?.embedded, "InvoiceEmbedFont", fontRow);
		for (const [key, label] of [["fontSize", "字号"], ["pageWidth", "页宽"], ["pageHeight", "页高"], ["margin", "边距"]]) {
			const input = invoiceInput(details, label, `Layout_${key}`, draft.layout[key], value => { draft.layout[key] = Number(value); if (creatingReference) invoiceDirty(draft, "model"); }, false, "number", "mm");
			input.min = key === "margin" ? "0" : "0.01";
		}
		settingsButton("生成", async () => {
			if (draft.dirty.size) throw new Error("请先应用草稿");
			if (!draft.layout.font && !draft.fontChoice?.startsWith("file:")) throw new Error("请选择字体");
			const openSeq = state.openSeq, sequence = settings.sequence;
			const fontChoice = draft.fontChoice, layout = structuredClone(draft.layout);
			let fontData;
			if (fontChoice?.startsWith("file:")) {
				const font = libraryFonts.find(value => `file:${value.id}` === fontChoice);
				if (!font) throw new Error("所选字体不可用");
				fontData = invoiceBase64(await readTextFont(font));
			}
			if (!isForegroundDocument(tab) || openSeq !== state.openSeq || sequence !== settings.sequence || invoiceCurrent() !== draft || !settingsElements.Panel.open || !settings.invoice) return;
			if (draft.dirty.size || draft.fontChoice !== fontChoice || JSON.stringify(draft.layout) !== JSON.stringify(layout)) throw new Error("内容已变更，请重新生成");
			await commitInvoice(draft, { operation: "generate", layout, fontData });
		}, !capabilities.generate, "InvoiceGenerate", details, capabilities.reasons?.generate || (draft.bindings.length ? "请先解除绑定" : ""));
	}
	loadInvoiceObjects(draft).catch(error => { if (isForegroundDocument(tab) && invoiceCurrent() === draft) settingsElements.Status.textContent = error.message; });
}

async function loadInvoiceObjects(draft) {
	const tab = documentTab(), state = tab.state, settings = tab.settings;
	const page = draft.page, sequence = settings.sequence;
	const field = document.querySelector("#settingsInvoiceObject");
	if (!field || !state.doc.pages[page]) return;
	const data = await pageView.loadPageData(page);
	if (!isForegroundDocument(tab) || invoiceCurrent() !== draft || draft.page !== page || sequence !== settings.sequence || !field.isConnected) return;
	const objects = (data.objects || []).filter(item => !item.scoped && item.type === "TextObject");
	field.replaceChildren(new Option("选择", ""), ...objects.map(item => new Option(`${item.id} · ${(item.text || "文字").slice(0, 60)}`, item.id)));
	field.value = draft.object;
}

function invoiceOtherDraftWarning(draft) {
	return [...invoicePanel.drafts.values()].some(value => value !== draft && value.entry && value.dirty.size && invoiceSourceKey(value.entry.source) === invoiceSourceKey(draft.entry.source)) ? "\n该来源其他发票的未应用修改也将放弃" : "";
}

async function resetInvoiceDraft(draft) {
	const section = { xml: "xml", fields: draft.entry ? "fields" : "model", appearance: "bindings" }[invoicePanel.tab] || "model";
	if (!draft.dirty.has(section) || !await requestConfirmation("放弃当前编辑项的未应用修改？")) return;
	if (section === "xml") draft.xml = draft.detail.xml || "";
	else if (section === "fields") draft.changes.clear();
	else if (section === "bindings") draft.bindings = structuredClone(draft.detail.bindings || []);
	else {
		draft.invoice = structuredClone(draft.detail.invoice);
		draft.detail.fields = structuredClone(draft.baselineFields);
		draft.addedItems.clear();
		draft.item = 0;
		draft.fact = 0;
		draft.factOwner = -1;
		draft.options = { name: "", encoding: { schemaRef: "", context: {}, units: [] } };
		draft.fragments = {};
	}
	draft.dirty.delete(section);
	draft.status = "";
	renderDocumentSettings();
}

function renderInvoiceXML(container, draft) {
	const tab = documentTab();
	const writable = state.editing && (!draft.entry || draft.detail.capabilities?.edit);
	invoiceInput(container, "原文", "XML", draft.xml, value => {
		draft.xml = value;
		invoiceDirty(draft, "xml");
	}, !writable, "textarea").rows = 14;
	const actions = document.createElement("div");
	actions.className = "invoice-actions";
	container.append(actions);
	if (state.editing) settingsButton("导入", () => readInvoiceFile(text => {
		draft.xml = text;
		if (!draft.entry) draft.importing = true;
		invoiceDirty(draft, "xml");
		renderDocumentSettings();
	}), !writable, "InvoiceLoadXML", actions);
	settingsButton("导出", async () => {
		const result = await callTabWASM(tab, "ofdgoExportInvoice", JSON.stringify({ ...draft.scope, source: draft.entry.source, index: draft.entry.index }));
		if (!tab.closed) downloadBytes(result.bytes, "application/xml", (draft.entry.source.fileLoc?.split("/").at(-1) || "invoice.xml"));
	}, !draft.entry, "InvoiceExportXML", actions);
}

function readInvoiceFile(accept) {
	const tab = documentTab(), state = tab.state, settings = tab.settings;
	const sequence = settings.sequence, openSeq = state.openSeq;
	const input = document.createElement("input");
	input.type = "file";
	input.accept = ".xml,.xbrl,application/xml,text/xml";
	input.addEventListener("change", async () => {
		const file = input.files[0];
		if (!file) return;
		try {
			const text = new TextDecoder("utf-8", { fatal: true }).decode(await file.arrayBuffer());
			if (!isForegroundDocument(tab) || sequence !== settings.sequence || openSeq !== state.openSeq || !settingsElements.Panel.open) return;
			await accept(text);
		} catch (error) {
			if (isForegroundDocument(tab)) settingsElements.Status.textContent = error.message;
		}
	});
	input.click();
}

async function validateInvoiceDraft(draft) {
	const tab = documentTab();
	const request = draft.dirty.size ? buildInvoiceRequest(draft) : { invoice: draft.invoice };
	if (!await prepareInvoiceRequest(draft, request)) return;
	if (!isForegroundDocument(tab)) return;
	const result = await callTabWASM(tab, "ofdgoValidateInvoice", JSON.stringify({ ...draft.scope, source: draft.entry?.source, index: draft.entry?.index, digest: draft.detail.digest, ...request }));
	if (!isForegroundDocument(tab) || invoiceCurrent() !== draft) return;
	const appearance = result.appearance || [];
	const unbound = appearance.some(issue => issue.code === "appearance-unbound");
	const issues = (result.issues || []).filter(issue => issue.code !== "appearance-not-synchronized");
	draft.status = invoiceIssues([...issues, ...appearance.filter(issue => issue.code !== "appearance-unbound")])
		|| (unbound ? "数据校验通过，票面未校验" : draft.entry ? "数据与票面校验通过" : "数据校验通过");
	settingsElements.Status.textContent = draft.status;
}

async function applyInvoiceDraft(draft, synchronize = draft.synchronize) {
	const tab = documentTab(), state = tab.state;
	if (!state.editing || !draft.dirty.size) return;
	const request = buildInvoiceRequest(draft);
	request.synchronize = draft.invoice.standard === invoiceReferenceStandard || synchronize;
	if (!await prepareInvoiceRequest(draft, request)) return;
	if (!isForegroundDocument(tab)) return;
	if (request.operation === "replace" && !await requestConfirmation("替换此来源中的全部发票原文？" + invoiceOtherDraftWarning(draft))) return;
	await commitInvoice(draft, request);
}

async function prepareInvoiceRequest(draft, request) {
	const tab = documentTab(), state = tab.state, settings = tab.settings;
	if (draft.invoice.standard !== invoiceReferenceStandard) return true;
	const creating = request.operation === "create";
	const patches = (request.operation === "batch" ? request.operations : [request]).filter(operation => operation.operation === "patch");
	if (!creating && !patches.length) return true;
	if (creating && !draft.layout.font && !draft.fontChoice?.startsWith("file:")) {
		invoicePanel.tab = "appearance";
		draft.layoutOpen = true;
		renderDocumentSettings();
		throw new Error("请选择标引票面字体");
	}
	const sequence = settings.sequence, openSeq = state.openSeq;
	const doc = state.doc, scope = JSON.stringify(invoiceScope());
	const snapshot = JSON.stringify(request), fontChoice = draft.fontChoice;
	for (const patch of patches) patch.changes = structuredClone(patch.changes);
	const busy = settings.busy;
	settings.busy = true;
	settingsElements.Form.inert = true;
	try {
		if (creating && fontChoice?.startsWith("file:")) {
			const font = fontManager.editorFonts().find(value => `file:${value.id}` === fontChoice && !value.disabled);
			if (!font) throw new Error("所选字体不可用");
			request.fontData = invoiceBase64(await readTextFont(font));
		}
		const fonts = new Map();
		for (const patch of patches) {
			for (const change of patch.changes) {
				if (!change.font?.startsWith("file:")) continue;
				if (!fonts.has(change.font)) {
					const font = fontManager.editorFonts().find(value => `file:${value.id}` === change.font && !value.disabled);
					if (!font) throw new Error("所选字体不可用");
					fonts.set(change.font, invoiceBase64(await readTextFont(font)));
				}
				change.fontData = fonts.get(change.font);
				delete change.font;
			}
		}
		if (!isForegroundDocument(tab) || sequence !== settings.sequence || openSeq !== state.openSeq || doc !== state.doc || scope !== JSON.stringify(invoiceScope()) || invoiceCurrent() !== draft || !settingsElements.Panel.open || !settings.invoice) return false;
		if (draft.fontChoice !== fontChoice || JSON.stringify(buildInvoiceRequest(draft)) !== snapshot) throw new Error("内容已变更，请重新应用");
		return true;
	} finally {
		settings.busy = busy;
		if (isForegroundDocument(tab) && sequence === settings.sequence) settingsElements.Form.inert = busy;
	}
}

function buildInvoiceRequest(draft) {
	if (draft.dirty.has("xml") && draft.dirty.size > 1) throw new Error("原文替换与其他修改冲突，草稿已保留，请先还原其中一项");
	if (draft.dirty.has("model") && draft.dirty.has("fields")) throw new Error("基本、明细与原字段不能同时应用，请先还原一项");
	const section = draft.dirty.has("xml") ? "xml" : "model";
	const request = { synchronize: draft.invoice.standard === invoiceReferenceStandard || draft.synchronize };
	if (section === "xml") {
		Object.assign(request, { operation: draft.entry ? "replace" : "import", xml: draft.xml, options: { name: draft.options.name } });
	} else {
		const options = structuredClone(draft.options);
		if (!invoiceIsXBRL(draft.invoice)) options.encoding = {};
		if (!draft.entry && draft.invoice.standard === invoiceReferenceStandard) {
			delete options.encoding;
			options.layout = structuredClone(draft.layout);
		}
		for (const [key, text] of draft.invoice.standard === "EInvoice" ? Object.entries(draft.fragments) : []) {
			options.encoding[key] = invoiceBase64(new TextEncoder().encode(text));
		}
		if (!draft.entry) Object.assign(request, { operation: "create", invoice: draft.invoice, options });
		else {
			const operations = [];
			if (draft.dirty.has("model")) operations.push({ operation: "set", index: draft.entry.index, invoice: draft.invoice });
			if (draft.dirty.has("fields")) operations.push({ operation: "patch", index: draft.entry.index, changes: structuredClone([...draft.changes.values()]) });
			if (draft.dirty.has("bindings")) operations.push({ operation: "bindings", index: draft.entry.index, bindings: draft.bindings });
			Object.assign(request, operations.length === 1 ? operations[0] : { operation: "batch", operations });
		}
	}
	return request;
}

function invoiceBase64(bytes) {
	let value = "";
	for (let offset = 0; offset < bytes.length; offset += 8192) value += String.fromCharCode(...bytes.subarray(offset, offset + 8192));
	return btoa(value);
}

async function commitInvoice(draft, request) {
	const tab = documentTab(), state = tab.state, settings = tab.settings, invoicePanel = tab.invoice;
	if (!state.editing || settings.busy || document.body.hasAttribute("data-busy")) return;
	if (!settingsElements.Panel.open || !settings.invoice || invoiceCurrent() !== draft) return;
	const scope = invoiceScope();
	if (draft.scope.documentIndex !== scope.documentIndex || draft.scope.version !== scope.version || draft.scope.revision !== scope.revision) throw new Error("文档已变更，请重新打开发票");
	const key = invoicePanel.key;
	const revision = state.editorInfo.revision;
	settings.busy = true;
	settingsElements.Form.inert = true;
	settingsElements.Status.textContent = "正在应用修改";
	try {
		invoicePanel.result = null;
		const changed = await changeDocument("ofdgoChangeInvoice", null, JSON.stringify({ ...draft.scope, source: draft.entry?.source, index: draft.entry?.index || 0, digest: draft.detail.digest, ...request }));
		if (!changed && (tab.closed || state.editorInfo?.revision === revision)) {
			if (isForegroundDocument(tab) && settingsElements.Status.textContent === "正在应用修改") settingsElements.Status.textContent = el.statusText.textContent;
			return;
		}
		const result = invoicePanel.result || state.doc.invoiceResult;
		if (invoicePanel.drafts.get(key) === draft) invoicePanel.drafts.delete(key);
		const currentScope = {documentIndex: state.doc.documentIndex || 0, version: state.doc.documentVersion || "", revision: state.editorInfo.revision};
		for (const [key, value] of invoicePanel.drafts) {
			if (value.scope.documentIndex !== currentScope.documentIndex || value.scope.version !== currentScope.version) continue;
			value.scope.revision = currentScope.revision;
			if (value.entry && draft.entry && invoiceSourceKey(value.entry.source) === invoiceSourceKey(draft.entry.source)) {
				if (!value.dirty.size) invoicePanel.drafts.delete(key);
				else value.status = "同源数据已修改，应用时将检查来源冲突";
			}
		}
		if (request.operation === "delete" || request.operation === "replace") {
			for (const [key, value] of invoicePanel.drafts) {
				if (value.entry && invoiceSourceKey(value.entry.source) === invoiceSourceKey(draft.entry.source)) {
					invoicePanel.drafts.delete(key);
				}
			}
		}
		if (!isForegroundDocument(tab) || !settingsElements.Panel.open || !settings.invoice || invoicePanel.key !== key) return;
		if (result?.source?.kind) invoicePanel.key = JSON.stringify([invoiceSourceKey(result.source), draft.entry?.index || 0]);
		await loadDocumentSettings();
		if (!isForegroundDocument(tab)) return;
		const warning = invoiceIssues(result?.issues).replaceAll("\n", " · ");
		if (warning) {
			const status = "已应用 · " + warning;
			if (invoiceCurrent()) invoiceCurrent().status = status;
			settingsElements.Status.textContent = status;
		}
	} finally {
		settings.busy = false;
		if (isForegroundDocument(tab)) settingsElements.Form.inert = false;
	}
}

function settingsPages(container, id, value, change) {
	return settingsSelect(container, "页码", id, state.doc.pages.map((page, index) => [index, `第${index + 1}页`]), value, change);
}

function outlineSettingsChoices() {
	const result = [];
	const visit = (nodes, path = [], prefix = "") => {
		for (const [index, node] of (nodes || []).entries()) {
			const next = [...path, index], title = prefix + node.title;
			result.push([JSON.stringify(next), title]);
			visit(node.children, next, title + " / ");
		}
	};
	visit(state.doc?.outlines);
	return result;
}

function actionSettingsLabel(action) {
	if (action.Goto) return action.Goto.Bookmark ? `书签 · ${action.Goto.Bookmark.Name}` : `页面 · ${pageIndexByID(state.doc.pages, action.Goto.Dest?.PageID) + 1}`;
	if (action.URI) return `网址 · ${action.URI.URI}`;
	const [key, label, id] = action.Sound ? ["Audio", "音频", action.Sound.ResourceID] : action.Movie ? ["Video", "视频", action.Movie.ResourceID] : ["Attachment", "附件", action.GotoA?.AttachID];
	return `${label} · ${settings.data.resources.find(item => item.kind === key && item.id === referenceIDKey(id))?.name || id || ""}`;
}

function renderActionSettings() {
	const context = settingsElements.Context, content = settingsElements.Content;
	settingsSelect(context, "归属", "Scope", [["document", "文档"], ["page", "页面"], ["object", "图元"], ["outline", "目录"]], settings.scope, value => { settings.scope = value; settings.selected = -1; loadDocumentSettings(); });
	if (settings.scope === "page" || settings.scope === "object") settingsPages(context, "Page", settings.page, value => { settings.page = Number(value); settings.selected = -1; loadDocumentSettings(); });
	if (settings.scope === "object") {
		const objects = settingsSelect(context, "图元", "Object", (settings.data.objects || []).map(item => [item.id, `${item.id} · ${item.text || ({TextObject: "文字", PathObject: "图形", ImageObject: "图片"}[item.type] || "组合")}`]), settings.object, value => { settings.object = value; settings.selected = -1; loadDocumentSettings(); });
		if (!el.objectStylePanel.open) settingsPickControl(objects, "action");
	}
	if (settings.scope === "outline") settingsSelect(context, "目录", "Outline", outlineSettingsChoices(), JSON.stringify(settings.path), value => { settings.path = JSON.parse(value); settings.selected = -1; loadDocumentSettings(); });
	if (el.objectStylePanel.open) for (const field of context.querySelectorAll("select")) field.disabled = true;
	if (settings.selected >= settings.actions.length) settings.selected = -1;
	settingsSelect(context, "动作", "Entry", [[-1, "新增"], ...settings.actions.map((item, index) => [index, `${index + 1} · ${actionSettingsLabel(item)}`])], settings.selected, value => { settings.selected = Number(value); renderDocumentSettings(); });
	const action = settings.actions[settings.selected] || {};
	const type = action.Goto ? action.Goto.Bookmark ? "Bookmark" : "Goto" : ["URI", "GotoA", "Sound", "Movie"].find(key => action[key]) || "Goto";
	settingsSelect(content, "类型", "ActionType", [["Goto", "页面"], ["Bookmark", "书签"], ["URI", "网址"], ["GotoA", "附件"], ["Sound", "音频"], ["Movie", "视频"]], type, value => renderActionFields(value, action));
	const fields = document.createElement("div");
	fields.id = "settingsActionFields";
	content.append(fields);
	renderActionFields(type, action);
	const missing = settings.scope === "object" && !settings.object || settings.scope === "outline" && !settings.path.length;
	settingsButton(settings.selected < 0 ? "添加" : "修改", () => writeActionSettings("save"), missing, "Save");
	settingsButton("删除", () => writeActionSettings("delete"), settings.selected < 0, "Delete");
	settingsButton("上移", () => writeActionSettings("up"), settings.selected <= 0, "Up");
	settingsButton("下移", () => writeActionSettings("down"), settings.selected < 0 || settings.selected >= settings.actions.length - 1, "Down");
}

function renderActionFields(type, action) {
	const fields = document.querySelector("#settingsActionFields");
	fields.replaceChildren();
	if (type === "Goto") renderDestinationFields(fields, action.Goto?.Dest);
	else if (type === "Bookmark") settingsSelect(fields, "书签", "Bookmark", (settings.data.bookmarks || []).map(item => [item.Name, item.Name]), action.Goto?.Bookmark?.Name);
	else if (type === "URI") {
		settingsField(fields, "网址", "URI", action.URI?.URI);
		settingsField(fields, "基址", "Base", action.URI?.Base);
		settingsField(fields, "目标", "Target", action.URI?.Target);
	} else {
		const kind = {GotoA: "Attachment", Sound: "Audio", Movie: "Video"}[type];
		settingsSelect(fields, "资源", "Resource", settings.data.resources.filter(item => item.kind === kind).map(item => [item.id, item.name]), referenceIDKey(action[type]?.ResourceID || action[type]?.AttachID));
		if (type === "GotoA") settingsSelect(fields, "窗口", "NewWindow", [["", "默认"], ["true", "新建"], ["false", "当前"]], action.GotoA?.NewWindow == null ? "" : String(action.GotoA.NewWindow));
		if (type === "Sound") {
			const volume = settingsField(fields, "音量", "Volume", action.Sound?.Volume ?? "", "number");
			volume.min = "0"; volume.max = "100"; volume.step = "1"; volume.placeholder = "默认";
			settingsField(fields, "循环", "Repeat", action.Sound?.Repeat, "checkbox");
			settingsField(fields, "同步", "Synchronous", action.Sound?.Synchronous, "checkbox");
		}
		if (type === "Movie") settingsSelect(fields, "操作", "Operator", [["", "默认"], ["Play", "播放"], ["Stop", "停止"], ["Pause", "暂停"], ["Resume", "继续"]], action.Movie?.Operator || "");
	}
}

function renderDestinationFields(container, dest = {}) {
	const type = dest?.Type || "XYZ";
	settingsSelect(container, "定位", "DestType", [["XYZ", "坐标"], ["Fit", "适页"], ["FitH", "适宽"], ["FitV", "适高"], ["FitR", "区域"]], type, () => updateDestinationFields());
	const index = pageIndexByID(state.doc.pages, dest?.PageID);
	const pages = state.doc.pages.map((page, index) => [page.id, `第${index + 1}页`]);
	if (dest?.PageID && index < 0) pages.push([dest.PageID, "页面缺失"]);
	settingsSelect(container, "页码", "DestPage", pages, index >= 0 ? state.doc.pages[index].id : dest?.PageID || state.doc.pages[settings.page]?.id);
	for (const [id, label] of [["Left", "左侧"], ["Top", "顶部"], ["Right", "右侧"], ["Bottom", "底部"], ["Zoom", "缩放"]]) {
		const field = settingsField(container, label, id, dest?.[`Omit${id}`] ? "" : dest?.[id] ?? "", "number");
		field.placeholder = id === "Zoom" ? "保持" : "默认";
	}
	updateDestinationFields();
}

function updateDestinationFields() {
	const type = settingsValue("DestType"), required = {XYZ: ["Left", "Top", "Zoom"], Fit: [], FitH: ["Top"], FitV: ["Left"], FitR: ["Left", "Top", "Right", "Bottom"]}[type] || [];
	for (const id of ["Left", "Top", "Right", "Bottom", "Zoom"]) {
		const field = document.querySelector(`#settings${id}`);
		field.closest("label").hidden = !required.includes(id);
		field.disabled = !state.editing || !required.includes(id);
	}
}

function destinationFromSettings() {
	const dest = {Type: settingsValue("DestType"), PageID: settingsValue("DestPage")};
	for (const id of ["Left", "Top", "Right", "Bottom", "Zoom"]) dest[id] = Number(settingsValue(id));
	for (const id of ["Left", "Top", "Zoom"]) dest[`Omit${id}`] = settingsValue(id) === "";
	return dest;
}

async function commitSettings(name, ...args) {
	const tab = documentTab(), state = tab.state, settings = tab.settings;
	if (!state.editing || settings.busy || document.body.hasAttribute("data-busy")) return false;
	rememberSettingsDraft();
	const pending = settings.drafts.get(settings.view), baseline = settings.baseline;
	const revision = state.editorInfo?.revision;
	settings.busy = true;
	settingsElements.Form.inert = true;
	settingsElements.Status.textContent = "正在应用修改";
	const view = settings.view, focus = document.activeElement?.id, body = document.querySelector("#settingsBody"), scroll = body?.scrollTop || 0;
	try {
		const changed = await changeDocument(name, null, ...args);
		if (!changed && (tab.closed || state.editorInfo?.revision === revision)) {
			if (isForegroundDocument(tab) && settingsElements.Status.textContent === "正在应用修改") settingsElements.Status.textContent = el.statusText.textContent;
			return false;
		}
		settings.drafts.delete(view);
		if (pending) {
			const fields = settingsCommittedFields(name, args, Object.keys(pending.values));
			const values = Object.fromEntries(Object.entries(pending.values).filter(([id, value]) => !fields.includes(id) && JSON.stringify(value) !== JSON.stringify(baseline[id])));
			if (Object.keys(values).length) settings.drafts.set(view, {...pending, values});
		}
		if (!isForegroundDocument(tab)) return true;
		await loadDocumentSettings();
		if (!isForegroundDocument(tab)) return true;
		if (name === "ofdgoChangeActions" && el.objectStylePanel.open && settings.scope === "object") {
			const item = canvasEditor.pageItems(settings.page).find(item => item.id === settings.object && !item.scoped);
			if (item) { canvasEditor.select(item); syncObjectLink(item); }
		}
		if (body) body.scrollTop = scroll;
		document.getElementById(focus)?.focus({preventScroll: true});
		return true;
	} finally {
		settings.busy = false;
		if (isForegroundDocument(tab)) settingsElements.Form.inert = false;
	}
}

function settingsCommittedFields(name, args, fields) {
	const binding = ["settingsTargetPage", "settingsTargetObject", "settingsPaintTarget", "settingsPaintMode", "settingsPaintValue"];
	if (name === "ofdgoChangeTemplate") {
		if (args[0] === "update") return ["settingsName", "settingsTemplateOrder"];
		if (args[0] === "replace") return ["settingsSourcePage"];
		if (["bind", "order", "unbind"].includes(args[0])) return ["settingsBindingOrder"];
		if (args[0] === "move") return [];
	}
	if (name === "ofdgoChangeLayer") {
		if (args[0] === "update") return ["settingsLayerType", "settingsDrawParam"];
		if (args[0] === "objects") return ["settingsLayerObjects"];
		if (args[0] === "move") return [];
	}
	if (name === "ofdgoChangeStyleResource") return fields.filter(id => !binding.includes(id));
	if (name === "ofdgoApplyStyleResource") return binding;
	return fields;
}

async function writeActionSettings(operation) {
	const tab = documentTab(), settings = tab.settings;
	if (operation === "save" && !settingsElements.Form.reportValidity()) return;
	rememberSettingsDraft();
	const drafts = new Map(settings.drafts), view = settings.view;
	const actions = structuredClone(settings.actions), index = settings.selected;
	if (operation === "save") {
		const original = actions[index] || {};
		const action = {Event: {document: "DO", page: "PO", object: "CLICK", outline: original.Event || "CLICK"}[settings.scope], Region: original.Region || null};
		const type = settingsValue("ActionType");
		if (type === "Goto") action.Goto = {Dest: destinationFromSettings()};
		else if (type === "Bookmark") action.Goto = {Bookmark: {Name: settingsValue("Bookmark")}};
		else if (type === "URI") action.URI = {URI: settingsValue("URI"), Base: settingsValue("Base"), Target: settingsValue("Target")};
		else if (type === "GotoA") action.GotoA = {AttachID: settingsValue("Resource"), NewWindow: settingsValue("NewWindow") === "" ? null : settingsValue("NewWindow") === "true"};
		else if (type === "Sound") action.Sound = {ResourceID: settingsValue("Resource"), Volume: settingsValue("Volume") === "" ? null : Number(settingsValue("Volume")), Repeat: settingsValue("Repeat"), Synchronous: settingsValue("Synchronous")};
		else action.Movie = {ResourceID: settingsValue("Resource"), Operator: settingsValue("Operator")};
		if (index < 0) { settings.selected = actions.length; actions.push(action); }
		else actions[index] = action;
	} else if (operation === "delete") { actions.splice(index, 1); settings.selected = Math.min(index, actions.length - 1); }
	else {
		const next = index + (operation === "up" ? -1 : 1);
		[actions[index], actions[next]] = [actions[next], actions[index]];
		settings.selected = next;
	}
	if (!await commitSettings("ofdgoChangeActions", settings.scope, settings.page, settings.object, settings.path, JSON.stringify(actions))) { settings.selected = index; return; }
	if (operation !== "save") {
		const context = JSON.parse(view).slice(0, -1), prefix = JSON.stringify(context);
		for (const key of settings.drafts.keys()) if (JSON.stringify(JSON.parse(key).slice(0, -1)) === prefix) settings.drafts.delete(key);
		for (const [key, draft] of drafts) {
			const parts = JSON.parse(key);
			if (JSON.stringify(parts.slice(0, -1)) !== prefix) continue;
			let position = parts.at(-1);
			if (operation === "delete") {
				if (position === index) continue;
				if (position > index) position--;
			} else if (position === index) position = settings.selected;
			else if (position === settings.selected) position = index;
			settings.drafts.set(JSON.stringify([...context, position]), draft);
		}
		if (isForegroundDocument(tab)) renderDocumentSettings();
	}
}

function renderStyleResourceSettings(kind) {
	const properties = settingsSection("属性"), content = properties.section, value = settings.data.resource || {};
	const existing = Boolean(settings.resource.id);
	if (kind === "ColorSpace") {
		settingsSelect(content, "模型", "ColorType", [["RGB", "RGB"], ["GRAY", "GRAY"], ["CMYK", "CMYK"]], value.Type || "RGB");
		settingsSelect(content, "位深", "Bits", [1, 2, 4, 8, 16].map(bits => [bits, String(bits)]), value.BitsPerComponent || 8);
		settingsField(content, "调色板", "Palette", (value.Palette || []).join("\n"), "textarea");
		if (value.Profile) appendInfoLine(content, "配置", value.Profile);
		const file = settingsField(content, "配置文件", "Profile", "", "file");
		file.accept = ".icc,.icm";
		if (value.Profile) settingsField(content, "移除配置", "ClearProfile", false, "checkbox");
	} else {
		settingsSelect(content, "继承", "Relative", [["", "无"], ...settings.data.resources.filter(item => item.kind === "DrawParam" && item.id !== settings.resource.id).map(item => [item.id, item.name])], referenceIDKey(value.Relative));
		settingsSelect(content, "字体", "Font", [["", "继承"], ...settings.data.resources.filter(item => item.kind === "Font").map(item => [item.id, item.name])], referenceIDKey(value.Font));
		const grid = document.createElement("div");
		grid.className = "settings-grid";
		content.append(grid);
		for (const [id, label, unit] of [["Size", "字号", "mm"], ["Weight", "字重", ""], ["LineWidth", "线宽", "mm"], ["MiterLimit", "截断值", ""]]) {
			const field = settingsField(grid, label, id, value[id] || (value[`${id}Set`] ? 0 : ""), "number", unit);
			field.min = "0";
			field.placeholder = "继承";
			if (id === "Weight") { field.step = "100"; field.max = "900"; }
		}
		settingsField(content, "斜体", "Italic", value.Italic, "checkbox");
		settingsSelect(content, "端点", "Cap", [["", "继承"], ["Butt", "平头"], ["Round", "圆头"], ["Square", "方头"]], value.Cap || "");
		settingsSelect(content, "转角", "Join", [["", "继承"], ["Miter", "尖角"], ["Round", "圆角"], ["Bevel", "斜角"]], value.Join || "");
		settingsField(content, "间隔", "DashPattern", value.DashPattern || "").title = "虚线间隔";
		settingsField(content, "偏移", "DashOffset", value.DashOffset ?? "", "number", "mm");
		for (const [key, label] of [["FillColor", "填充"], ["StrokeColor", "描边"]]) {
			settingsSelect(content, label, key + "Mode", [["keep", "原有"], ["inherit", "继承"], ["solid", "颜色"]], "keep");
			const field = settingsField(content, label + "颜色", key, "#000000", "color");
			field.closest("label").hidden = true;
			document.querySelector(`#settings${key}Mode`).addEventListener("change", event => { field.closest("label").hidden = event.target.value !== "solid"; });
		}
	}
	content.querySelectorAll("input,select,textarea").forEach(field => { field.disabled = !state.editing; });
	if (!state.editing) return;
	settingsButton(existing ? "修改" : "添加", () => writeStyleResource(kind), false, "Save", properties.actions);
	content.append(properties.actions);
	if (!existing) return;
	const group = settingsSection("绑定"), binding = group.section;
	settingsPages(binding, "TargetPage", settings.page, value => { settings.page = Number(value); loadSettingsObjects(); });
	const objects = settingsSelect(binding, "图元", "TargetObject", [["", "选择"], ...(settings.data.objects || []).map(item => [item.id, `${item.id} · ${item.text || "图元"}`])], settings.object);
	settingsPickControl(objects, "style");
	if (kind === "ColorSpace") {
		settingsSelect(binding, "应用", "PaintTarget", [["fill", "填充"], ["stroke", "描边"]], "fill");
		settingsSelect(binding, "方式", "PaintMode", [["value", "分量"], ["index", "索引"]], "value");
		settingsField(binding, "色值", "PaintValue", value.Type === "CMYK" ? `0 0 0 ${2 ** (value.BitsPerComponent || 8) - 1}` : value.Type === "GRAY" ? "0" : "0 0 0");
	}
	const apply = settingsButton("应用", () => applyStyleResource(kind), !objects.value, "Apply", group.actions);
	const unbind = kind === "DrawParam" ? settingsButton("解绑", () => applyStyleResource(kind, true), !objects.value, "Unbind", group.actions) : null;
	objects.addEventListener("change", () => { apply.disabled = !objects.value; if (unbind) unbind.disabled = !objects.value; });
	binding.append(group.actions);
}

async function loadSettingsObjects() {
	const tab = documentTab(), state = tab.state, settings = tab.settings;
	const sequence = settings.sequence, page = settings.page, openSeq = state.openSeq;
	const field = document.querySelector("#settingsTargetObject");
	field.disabled = true;
	try {
		const data = await pageView.loadPageData(page);
		if (!isForegroundDocument(tab) || sequence !== settings.sequence || page !== settings.page || openSeq !== state.openSeq) return;
		field.replaceChildren(new Option("选择", ""), ...(data.objects || []).filter(item => ["TextObject", "PathObject", "CompositeObject", "CompositeGraphicUnit"].includes(item.type) && !item.scoped).map(item => new Option(`${item.id} · ${item.text || "图元"}`, item.id)));
	} catch (error) { if (isForegroundDocument(tab) && sequence === settings.sequence) settingsElements.Status.textContent = error.message; }
	finally { if (isForegroundDocument(tab) && sequence === settings.sequence) { field.disabled = false; field.dispatchEvent(new Event("change")); } }
}

async function writeStyleResource(kind) {
	const tab = documentTab(), state = tab.state, settings = tab.settings, resourceManager = tab.resources;
	if (!settingsElements.Form.reportValidity()) return;
	const sequence = settings.sequence, openSeq = state.openSeq, resource = settings.resource, open = settingsElements.Panel.open;
	const value = structuredClone(settings.data.resource || {});
	let profile = null;
	const colors = {};
	if (kind === "ColorSpace") {
		value.Type = settingsValue("ColorType");
		value.BitsPerComponent = Number(settingsValue("Bits"));
		value.Palette = settingsValue("Palette").split(/\r?\n/).map(line => line.trim()).filter(Boolean);
		const file = document.querySelector("#settingsProfile").files[0];
		if (file) {
			try { profile = new Uint8Array(await file.arrayBuffer()); }
			catch (error) { if (isForegroundDocument(tab) && sequence === settings.sequence && openSeq === state.openSeq) settingsElements.Status.textContent = error.message; return; }
			if (!isForegroundDocument(tab) || sequence !== settings.sequence || openSeq !== state.openSeq || resource !== settings.resource || open !== settingsElements.Panel.open) return;
		}
		else if (settingsValue("ClearProfile")) profile = new Uint8Array();
	} else {
		for (const id of ["Relative", "Font", "Cap", "Join", "DashPattern"]) value[id] = settingsValue(id);
		for (const id of ["Size", "Weight", "LineWidth", "MiterLimit"]) value[id] = Number(settingsValue(id));
		value.LineWidthSet = settingsValue("LineWidth") !== "";
		value.MiterLimitSet = settingsValue("MiterLimit") !== "";
		value.DashOffset = settingsValue("DashOffset") === "" ? null : Number(settingsValue("DashOffset"));
		value.Italic = settingsValue("Italic");
		for (const key of ["FillColor", "StrokeColor"]) {
			const mode = settingsValue(key + "Mode");
			if (mode === "inherit") value[key] = null;
			if (mode === "solid") colors[key] = settingsValue(key);
		}
	}
	const before = new Set(settings.data.resources.filter(item => item.kind === kind).map(item => item.id));
	if (await commitSettings("ofdgoChangeStyleResource", kind, settings.resource.id, JSON.stringify(value), profile, JSON.stringify(colors))) {
		if (tab.closed) return;
		if (isForegroundDocument(tab)) await resourceTask(loadManagedResources);
		else await loadManagedResources(tab);
		if (!settings.resource.id) {
			settings.resource.id = resourceManager.items.find(item => item.kind === kind && !before.has(item.id))?.id || "";
			if (isForegroundDocument(tab)) await loadDocumentSettings();
		}
	}
}

async function applyStyleResource(kind, clear = false) {
	const tab = documentTab(), settings = tab.settings;
	const object = settingsValue("TargetObject");
	if (!object) { settingsElements.Status.textContent = "请选择图元"; return; }
	settings.object = object;
	const paint = kind === "ColorSpace" ? {target: settingsValue("PaintTarget"), mode: settingsValue("PaintMode"), value: settingsValue("PaintValue")} : {};
	if (await commitSettings("ofdgoApplyStyleResource", kind, clear ? "" : settings.resource.id, settings.page, object, JSON.stringify(paint)) && !tab.closed) {
		if (isForegroundDocument(tab)) await resourceTask(loadManagedResources);
		else await loadManagedResources(tab);
	}
}

function renderLayerSettings() {
	const tab = documentTab(), settings = tab.settings;
	const entries = settings.data.page.layers || [];
	settingsPages(settingsElements.Context, "Page", settings.page, value => { settings.page = Number(value); settings.selected = -1; loadDocumentSettings(); });
	if (!entries.length && !state.editing) { settingsElements.Status.textContent = "暂无图层"; return; }
	if (settings.selected >= entries.length) settings.selected = -1;
	if (!state.editing && settings.selected < 0 && entries.length) settings.selected = 0;
	const types = {Background: "背景", Body: "正文", Foreground: "前景"};
	const options = entries.map((entry, index) => [index, `${entry.ID} · ${types[entry.Type] || entry.Type} · ${entry.ObjectCount}个图元`]);
	if (state.editing) options.unshift([-1, "新增"]);
	settingsSelect(settingsElements.Context, "图层", "Entry", options, settings.selected, value => { settings.selected = Number(value); renderDocumentSettings(); });
	const entry = entries[settings.selected];
	const properties = settingsSection("属性"), content = properties.section;
	settingsSelect(content, "类型", "LayerType", Object.entries(types), entry?.Type || "Body");
	settingsSelect(content, "样式", "DrawParam", [["", "无"], ...settings.data.resources.filter(item => item.kind === "DrawParam").map(item => [item.id, item.name])], referenceIDKey(entry?.DrawParam));
	content.querySelectorAll("select").forEach(field => { field.disabled = !state.editing; });
	if (state.editing) settingsButton("定位", () => locateSettingsObjects(entry?.ObjectIDs || []), !entry?.ObjectCount, "LocateLayer", properties.actions, "图层没有可定位的图元");
	content.append(properties.actions);
	if (!state.editing) return;
	const write = async (operation, order = 0, objects = []) => {
		const selected = settings.selected;
		if (!entry && operation === "add") settings.selected = entries.length;
		if (!await commitSettings("ofdgoChangeLayer", operation, settings.page, entry?.ID || "", settingsValue("LayerType"), settingsValue("DrawParam"), order, objects)) settings.selected = selected;
	};
	settingsButton(entry ? "修改" : "添加", () => write(entry ? "update" : "add"), false, "Save", properties.actions);
	settingsButton("删除", () => write("delete"), !entry || entry.ObjectCount > 0, "Delete", properties.actions, entry?.ObjectCount ? "图层仍有图元" : "未选择图层");
	settingsButton("上移", () => write("move", entry.Order - 1), !entry || entry.Order === 0, "Up", properties.actions);
	settingsButton("下移", () => write("move", entry.Order + 1), !entry || entry.Order >= entries.filter(item => item.Type === entry.Type).length - 1, "Down", properties.actions);
	if (entry) {
		const binding = settingsSection("图元");
		const objects = settingsSelect(binding.section, "图元", "LayerObjects", (settings.data.objects || []).filter(item => !entry.ObjectIDs?.includes(item.id)).map(item => [item.id, `${item.id} · ${item.text || "图元"}`]), null);
		objects.multiple = true;
		objects.size = Math.min(4, Math.max(2, objects.options.length));
		for (const option of objects.options) option.selected = canvasEditor.items().some(item => item.index === settings.page && item.id === option.value && !item.scoped);
		settingsButton("选取", () => beginSettingsPick("layer"), false, "PickLayer", binding.actions);
		const move = settingsButton("移入", () => write("objects", 0, [...objects.selectedOptions].map(option => option.value)), !objects.selectedOptions.length, "MoveObjects", binding.actions);
		objects.addEventListener("change", () => { move.disabled = !objects.selectedOptions.length; });
		binding.section.append(binding.actions);
	}
}

function renderTemplateSettings() {
	const tab = documentTab(), settings = tab.settings;
	const entries = settings.data.page.templates || [], bindings = settings.data.page.bindings || [];
	settingsPages(settingsElements.Context, "Page", settings.page, value => { settings.page = Number(value); loadDocumentSettings(); });
	if (!entries.length && !state.editing) { settingsElements.Status.textContent = "暂无模板"; return; }
	if (settings.selected >= entries.length) settings.selected = -1;
	if (!state.editing && settings.selected < 0 && entries.length) settings.selected = 0;
	const options = entries.map((entry, index) => [index, `${entry.Name || entry.ID}${bindings.some(binding => referenceIDKey(binding.TemplateID) === referenceIDKey(entry.ID)) ? " · 已绑定" : ""}`]);
	if (state.editing) options.unshift([-1, "新增"]);
	settingsSelect(settingsElements.Context, "模板", "Entry", options, settings.selected, value => { settings.selected = Number(value); renderDocumentSettings(); });
	const entry = entries[settings.selected];
	const properties = settingsSection("属性"), content = properties.section;
	settingsField(content, "名称", "Name", entry?.Name);
	settingsSelect(content, "层次", "TemplateOrder", [["Background", "背景"], ["Foreground", "前景"]], entry?.ZOrder || "Background");
	if (entry) appendInfoLine(content, "文件", entry.BaseLoc);
	content.querySelectorAll("input,select").forEach(field => { field.disabled = !state.editing; });
	const bound = entry ? bindings.findIndex(binding => referenceIDKey(binding.TemplateID) === referenceIDKey(entry.ID)) : -1;
	const references = (settings.data.page.templateReferences || []).filter(reference => referenceIDKey(reference.TemplateID) === referenceIDKey(entry?.ID));
	if (references.length) {
		const reference = settingsSelect(content, "引用", "TemplateReference", references.map((ref, index) => [index, `第${ref.PageIndex + 1}页`]), 0);
		settingsButton("定位", () => locateSettingsPage(references[Number(reference.value)].PageIndex), false, "LocateTemplate", properties.actions);
	}
	content.append(properties.actions);
	if (!state.editing) {
		if (bound >= 0) appendInfoLine(content, "绑定层次", {Background: "背景", Foreground: "前景"}[bindings[bound].ZOrder] || "默认");
		return;
	}
	const source = entry ? document.createElement("details") : content;
	if (entry) {
		source.className = "settings-section";
		const title = document.createElement("summary");
		title.textContent = "内容";
		source.append(title);
		settingsElements.Content.append(source);
	}
	settingsPages(source, "SourcePage", settings.page);
	document.querySelector("#settingsSourcePage").closest("label").firstElementChild.textContent = "来源";
	const write = async (operation, order = 0) => {
		if (operation === "replace" && !await requestConfirmation("替换模板内容？当前文档中引用此模板的页面将同步更新。")) return;
		const selected = settings.selected;
		if (operation === "add") settings.selected = entries.length;
		if (!await commitSettings("ofdgoChangeTemplate", operation, ["add", "replace"].includes(operation) ? Number(settingsValue("SourcePage")) : settings.page, entry?.ID || "", settingsValue("Name"), ["bind", "order"].includes(operation) ? settingsValue("BindingOrder") : settingsValue("TemplateOrder"), order)) settings.selected = selected;
	};
	settingsButton(entry ? "修改" : "添加", () => write(entry ? "update" : "add"), false, "Save", properties.actions);
	if (!entry) content.append(properties.actions);
	if (entry) settingsButton("替换", () => write("replace"), false, "Replace", source);
	settingsButton("删除", () => write("delete"), !entry || references.length > 0, "Delete", properties.actions, references.length ? "模板仍被页面引用" : "未选择模板");
	if (entry) {
		const binding = settingsSection("绑定");
		settingsSelect(binding.section, "层次", "BindingOrder", [["", "默认"], ["Background", "背景"], ["Foreground", "前景"]], bound < 0 ? "" : bindings[bound].ZOrder || "");
		settingsButton(bound < 0 ? "绑定" : "应用", () => write(bound < 0 ? "bind" : "order"), false, "Bind", binding.actions);
		settingsButton("解绑", () => write("unbind"), bound < 0, "Unbind", binding.actions, "当前页未绑定此模板");
		settingsButton("上移", () => write("move", bound - 1), bound <= 0, "Up", binding.actions);
		settingsButton("下移", () => write("move", bound + 1), bound < 0 || bound >= bindings.length - 1, "Down", binding.actions);
		binding.section.append(binding.actions);
	}
}

function renderBookmarkSettings() {
	const tab = documentTab(), settings = tab.settings;
	const entries = settings.data.bookmarks || [];
	if (!entries.length && !state.editing) { settingsElements.Status.textContent = "暂无书签"; return; }
	if (settings.selected >= entries.length) settings.selected = -1;
	if (!state.editing && settings.selected < 0 && entries.length) settings.selected = 0;
	const options = entries.map((entry, index) => [index, entry.Name]);
	if (state.editing) options.unshift([-1, "新增"]);
	settingsSelect(settingsElements.Context, "书签", "Entry", options, settings.selected, value => { settings.selected = Number(value); renderDocumentSettings(); });
	const entry = entries[settings.selected];
	settingsField(settingsElements.Content, "名称", "Name", entry?.Name).required = true;
	renderDestinationFields(settingsElements.Content, entry?.Dest);
	settingsElements.Content.querySelectorAll("input,select").forEach(field => { field.disabled = !state.editing; });
	updateDestinationFields();
	settingsButton("定位", () => {
		settingsElements.Panel.close();
		const d = entry.Dest;
		navigateDestination({type: d.Type, pageID: d.PageID, left: d.Left, top: d.Top, right: d.Right, bottom: d.Bottom, zoom: d.Zoom, omitLeft: d.OmitLeft, omitTop: d.OmitTop, omitZoom: d.OmitZoom}, false);
	}, !entry, "Locate");
	if (!state.editing) return;
	settingsButton(entry ? "修改" : "添加", async () => {
		if (!settingsElements.Form.reportValidity()) return;
		const bookmark = {Name: settingsValue("Name"), Dest: destinationFromSettings()};
		const selected = settings.selected;
		if (!entry) settings.selected = entries.length;
		if (!await commitSettings("ofdgoChangeBookmark", entry ? "update" : "add", entry?.Name || "", JSON.stringify(bookmark))) settings.selected = selected;
	}, false, "Save");
	settingsButton("删除", () => commitSettings("ofdgoChangeBookmark", "delete", entry.Name, "{}"), !entry, "Delete");
}

function renderExtensionSettings() {
	const entries = settings.data.extensions || [];
	if (!entries.length) settingsElements.Status.textContent = "暂无扩展";
	for (const entry of entries) {
		const section = document.createElement("div");
		section.className = "settings-detail";
		for (const [key, label] of [["AppName", "应用"], ["Company", "单位"], ["AppVersion", "版本"], ["Date", "日期"], ["RefID", "引用"]]) appendInfoLine(section, label, entry[key]);
		for (const property of entry.Property || []) appendInfoLine(section, property.Name, property.Value);
		for (const value of entry.ExtendData || []) appendInfoLine(section, "文件", value);
		for (const value of entry.Data || []) {
			for (const attr of value.Attr || []) appendInfoLine(section, attr.Name.Local, attr.Value);
			const text = document.createElement("pre");
			text.textContent = value.Content;
			section.append(text);
		}
		settingsElements.Content.append(section);
	}
}

function settingsPickControl(field, kind) {
	const wrapper = document.createElement("span");
	wrapper.className = "form-unit settings-pick";
	field.replaceWith(wrapper);
	wrapper.append(field);
	settingsButton("选取", () => beginSettingsPick(kind), false, "PickObject", wrapper);
}

async function beginSettingsPick(kind) {
	const tab = documentTab(), state = tab.state, settings = tab.settings;
	if (!state.editing || settings.busy) return;
	rememberSettingsDraft();
	const page = kind === "style" ? Number(settingsValue("TargetPage")) : settings.page, sequence = state.openSeq;
	if (!await pageView.renderPage(page, {fit: false}) || !isForegroundDocument(tab) || sequence !== state.openSeq || !settingsElements.Panel.open) return;
	suspendSettings(kind);
}

function suspendSettings(kind) {
	settings.suspended = true;
	const parents = [resourceElements.Panel, el.objectStylePanel].filter(panel => panel.open);
	resourceManager.pick = {sequence: state.openSeq, scope: documentPanelScope(state), status: el.statusText.textContent, settings: kind, parents};
	settingsElements.Panel.close();
	for (const panel of parents) panel.close();
	canvasEditor.enabled = false;
	el.viewerPanel.classList.add("resource-picking");
	setBusy(false);
	setStatus(kind === "locate" ? "定位" : "选取图元");
	el.viewerPanel.focus({preventScroll: true});
}

async function pickSettingsObject(event, picking) {
	const tab = documentTab(), state = tab.state, settings = tab.settings, resourceManager = tab.resources;
	if (picking.settings === "locate") { finishResourcePick(); return; }
	const available = canvasEditor.itemsAt(event).filter(item => !item.scoped && !item.draft && item.type !== "Annotation"
		&& (picking.settings !== "invoice" || item.type === "TextObject")
		&& (picking.settings !== "style" || ["TextObject", "PathObject", "CompositeObject", "CompositeGraphicUnit"].includes(item.type))
		&& (picking.settings !== "layer" || item.index === settings.page && !settings.data.page.layers[settings.selected]?.ObjectIDs?.includes(item.id)));
	if (!available.length) return;
	const choose = async item => {
		if (!isForegroundDocument(tab) || resourceManager.pick !== picking || picking.sequence !== state.openSeq) return;
		finishResourcePick();
		if (picking.settings === "invoice") {
			const draft = invoiceCurrent();
			if (draft) { draft.page = item.index; draft.object = item.id; renderDocumentSettings(); }
		} else if (picking.settings === "action") {
			settings.page = item.index;
			settings.object = item.id;
			settings.selected = -1;
			await loadDocumentSettings();
		} else if (picking.settings === "style") {
			settings.page = item.index;
			settings.object = item.id;
			document.querySelector("#settingsTargetPage").value = String(item.index);
			await loadSettingsObjects();
			if (!isForegroundDocument(tab) || resourceManager.pick && resourceManager.pick !== picking) return;
			document.querySelector("#settingsTargetObject").value = item.id;
			document.querySelector("#settingsTargetObject").dispatchEvent(new Event("change"));
		} else {
			const field = document.querySelector("#settingsLayerObjects");
			for (const option of field.options) if (option.value === item.id) option.selected = true;
			field.dispatchEvent(new Event("change"));
		}
	};
	if (available.length === 1) await choose(available[0]);
	else resourceChoices(available, item => `${item.id} · ${(item.text || "图元").slice(0, 50)}`, choose);
}

async function locateSettingsObjects(ids) {
	const tab = documentTab(), settings = tab.settings;
	if (settings.busy) return;
	const sequence = settings.sequence, data = await pageView.loadPageData(settings.page);
	if (!isForegroundDocument(tab) || sequence !== settings.sequence || !settingsElements.Panel.open) return;
	const objects = (data.objects || []).filter(item => ids.includes(item.id) && !item.scoped);
	if (!objects.length) { settingsElements.Status.textContent = "暂无可见位置"; return; }
	const box = selectionBounds(objects);
	rememberSettingsDraft();
	suspendSettings("locate");
	await focusPageRegion({page: settings.page + 1, ...box}, "图层", "center");
}

async function locateSettingsPage(page) {
	rememberSettingsDraft();
	suspendSettings("locate");
	const area = state.doc.pages[page];
	await focusPageRegion({page: page + 1, x: 0, y: 0, width: area.width, height: area.height}, "模板", "center");
}

async function openResourceManagement() {
	if (resourceManager.busy || !state.doc || !state.ready || document.body.hasAttribute("data-busy")) return;
	if (resourceManager.pick) { finishResourcePick(); return; }
	resourceElements.Panel.showModal();
	renderResourceTabs();
	resourceElements.Page.replaceChildren(...state.doc.pages.map((page, index) => new Option(`第${index + 1}页`, String(index))));
	resourceElements.Page.value = String(state.pageIndex);
	await resourceTask(loadManagedResources);
}

async function resumeDocumentPanels(tab) {
	if (!isForegroundDocument(tab) || !tab.state.doc) return;
	if (tab.versionDownload) {
		const {blob, mime, name} = tab.versionDownload;
		tab.versionDownload = null;
		downloadBytes(blob, mime, name);
	}
	for (const [panel, key, refresh, render] of [
		[el.versionPanel, "versionManagement", refreshVersionManagement, renderVersionManagement],
		[el.documentPanel, "documentManagement", refreshDocumentManagement, renderDocumentManagement]
	]) {
		const manager = tab.state[key];
		if (!panel.open || !manager) continue;
		if (!manager.loading && (manager.scope !== documentPanelScope(tab.state) || !manager.entries && !manager.failed)) await refresh(false, tab);
		else if (manager.renderPending) render();
		if (!isForegroundDocument(tab)) return;
	}
	const manager = tab.resources, settings = tab.settings;
	const scope = documentPanelScope(tab.state);
	for (const name of ["fileAction", "pick"]) {
		const intent = manager[name];
		if (!intent || intent.sequence === tab.state.openSeq) continue;
		if (intent.scope === scope) intent.sequence = tab.state.openSeq;
		else {
			if (name === "pick" && intent.settings) settings.suspended = false;
			manager[name] = null;
		}
	}
	el.viewerPanel.classList.toggle("resource-picking", Boolean(manager.pick));
	if (manager.pick) canvasEditor.enabled = false;
	if (resourceElements.Panel.open && !manager.busy && (resourceElements.Panel.hasAttribute("data-busy") || resourceElements.List.inert)) {
		resourceElements.List.inert = false;
		resourceElements.Panel.removeAttribute("data-busy");
		await resourceTask(() => loadManagedResources(tab));
	}
	if (!isForegroundDocument(tab)) return;
	if (settingsElements.Panel.open && !settings.busy && settingsElements.Form.inert) {
		settingsElements.Form.inert = false;
		await loadDocumentSettings();
	}
}

function documentPanelScope(state) {
	return JSON.stringify([documentContentKey(state), state.editorInfo?.revision ?? null]);
}

function renderResourceTabs() {
	renderPanelTabs(resourceElements.Tabs, RESOURCE_KINDS, resourceManager.kind, kind => {
		if (resourceManager.busy || kind === resourceManager.kind) return;
		return resourceTask(async () => {
			rememberResourceView();
			resourceManager.kind = kind;
			renderResourceTabs();
			updateResourceControls();
			await renderResourceList();
		});
	});
}

function rememberResourceView() {
	if (!resourceManager.scope || workspace.resourceView !== resourceManager) return;
	resourceManager.views.set(resourceManager.kind, {selected: resourceManager.selected?.id,
		scroll: resourceElements.List.closest(".resource-list-container").scrollTop,
		reference: Number(resourceElements.Reference.value), page: resourceElements.Page.value, object: resourceElements.Object.value});
}

async function resourceTask(action, delay = 0) {
	const tab = documentTab(), resourceManager = tab.resources;
	if (resourceManager.busy) return;
	const manager = resourceManager;
	const current = () => isForegroundDocument(tab) && manager === tab.resources;
	manager.busy = true;
	resourceElements.List.inert = !delay;
	updateResourceControls();
	const showBusy = () => {
		if (current()) resourceElements.Status.textContent = "处理中";
	};
	const timer = delay ? setTimeout(showBusy, delay) : null;
	if (!delay) showBusy();
	resourceElements.Panel.setAttribute("data-busy", "true");
	try {
		await action();
		if (current() && resourceElements.Status.textContent === "处理中") resourceElements.Status.textContent = "";
	} catch (err) {
		if (current()) resourceElements.Status.textContent = resourceError(err.message);
	} finally {
		if (timer !== null) clearTimeout(timer);
		manager.busy = false;
		if (current()) {
			resourceElements.List.inert = false;
			resourceElements.Panel.removeAttribute("data-busy");
			updateResourceControls();
		}
	}
}

function resourceError(message) {
	if (/still referenced/.test(message)) return "资源仍被引用";
	if (/unsupported content/.test(message)) return "引用无法确认";
	if (/empty media|nonempty Audio/.test(message)) return "文件内容为空";
	if (/image mask .*dimensions differ from source/.test(message)) return "蒙版尺寸不符";
	if (/image mask contains non-binary pixel/.test(message)) return "蒙版像素不符";
	return message;
}

async function loadManagedResources(tab = documentTab()) {
	const state = tab.state, resourceManager = tab.resources;
	const sequence = state.openSeq;
	const manager = resourceManager;
	const scope = JSON.stringify([tab.id, documentContentKey(state), state.editing]);
	if (resourceManager.scope === scope) {
		if (isForegroundDocument(tab)) rememberResourceView();
	}
	else {
		resourceManager.views.clear();
		resourceManager.selected = null;
		resourceManager.scope = scope;
	}
	let items;
	try { items = await callTabWASM(tab, "ofdgoResources"); }
	finally { manager.requestID = 0; }
	if (tab.closed || sequence !== state.openSeq) return;
	resourceManager.items = items || [];
	if (!isForegroundDocument(tab) || !resourceElements.Panel.open) return;
	workspace.resourceView = manager;
	await renderResourceList();
}

async function renderResourceList() {
	const tab = documentTab(), resourceManager = tab.resources;
	const items = resourceManager.items.filter(item => item.kind === resourceManager.kind);
	const view = resourceManager.views.get(resourceManager.kind);
	const selected = items.find(item => item.id === view?.selected) || items[0];
	const saved = selected && selected.id === view?.selected ? view : null;
	resourceManager.filteredItems = items;
	resourceManager.start = resourceManager.end = -1;
	resourceElements.Empty.hidden = items.length !== 0;
	const height = parseFloat(getComputedStyle(resourceElements.List).getPropertyValue("--resource-row-height"));
	resourceElements.List.style.height = `${items.length * height}px`;
	resourceElements.List.closest(".resource-list-container").scrollTop = view?.scroll || 0;
	await selectManagedResource(selected, saved?.reference);
	if (!isForegroundDocument(tab) || !resourceElements.Panel.open || resourceManager.selected !== selected) return;
	if (saved && (resourceElements.Page.value !== saved.page || resourceElements.Object.value !== saved.object) && [...resourceElements.Page.options].some(option => option.value === saved.page)) {
		resourceElements.Page.value = saved.page;
		await loadResourceTargets(saved.object);
		if (isForegroundDocument(tab)) updateResourceControls();
	}
}

function renderResourceWindow() {
	if (!resourceElements.Panel.open) return;
	const items = resourceManager.filteredItems, list = resourceElements.List;
	const container = list.closest(".resource-list-container");
	const height = parseFloat(getComputedStyle(list).getPropertyValue("--resource-row-height"));
	const viewport = container.clientHeight - container.querySelector(".resource-heading").offsetHeight;
	const start = Math.max(0, Math.min(items.length, Math.floor(container.scrollTop / height)) - 4);
	const end = Math.min(items.length, Math.ceil((container.scrollTop + viewport) / height) + 4);
	if (start === resourceManager.start && end === resourceManager.end) return;
	resourceManager.start = start;
	resourceManager.end = end;
	const focused = list.contains(document.activeElement) ? document.activeElement.dataset.index : null;
	const fragment = document.createDocumentFragment();
	for (let index = start; index < end; index++) {
		const item = items[index], button = document.createElement("button");
		button.type = "button";
		button.className = "resource-option";
		button.dataset.index = String(index);
		button.style.top = `${index * height}px`;
		button.setAttribute("data-selected", String(item === resourceManager.selected));
		button.tabIndex = item === resourceManager.selected ? 0 : -1;
		button.title = item.file || item.name;
		for (const value of [item.name, item.missing ? "缺失" : item.file ? formatBytes(item.size) : item.kind === "Font" ? "外部" : "-", item.complete ? String(item.referenceCount || 0) : "未知"]) {
			const span = document.createElement("span");
			span.textContent = value;
			button.append(span);
		}
		button.addEventListener("click", () => chooseManagedResource(item));
		fragment.append(button);
	}
	list.replaceChildren(fragment);
	list.tabIndex = items.length && !list.querySelector('[data-selected="true"]') ? 0 : -1;
	if (focused != null) (list.querySelector(`[data-index="${focused}"]`) || list).focus({preventScroll: true});
}

async function chooseManagedResource(item) {
	if (item === resourceManager.selected) return;
	await resourceTask(() => {
		resourceElements.Status.textContent = "";
		return selectManagedResource(item);
	}, 150);
}

async function selectManagedResource(item, reference) {
	const same = item?.id === resourceManager.selected?.id && item?.kind === resourceManager.selected?.kind;
	reference ??= same ? Number(resourceElements.Reference.value) : 0;
	releaseResourcePreview();
	resourceManager.selected = item || null;
	renderResourceWindow();
	const previous = resourceElements.List.querySelector('[data-selected="true"]');
	if (previous) { previous.setAttribute("data-selected", "false"); previous.tabIndex = -1; }
	const current = resourceElements.List.querySelector(`[data-index="${resourceManager.filteredItems.indexOf(item)}"]`);
	if (current) { current.setAttribute("data-selected", "true"); current.tabIndex = 0; }
	resourceElements.List.tabIndex = resourceManager.filteredItems.length && !current ? 0 : -1;
	await loadManagedReferences(reference);
}

async function loadManagedReferences(index = 0) {
	const tab = documentTab(), state = tab.state, resourceManager = tab.resources;
	const item = resourceManager.selected, sequence = state.openSeq, panelSequence = resourceManager.sequence;
	const count = item?.referenceCount || 0;
	index = Math.max(0, Math.min(index, count - 1));
	const offset = Math.floor(index / 100) * 100;
	const references = count ? await callTabWASM(tab, "ofdgoResourceReferences", item.kind, item.id, offset, 100) || [] : [];
	if (!isForegroundDocument(tab) || sequence !== state.openSeq || panelSequence !== resourceManager.sequence || !resourceElements.Panel.open || item !== resourceManager.selected) return;
	resourceManager.references = references;
	resourceManager.referenceOffset = offset;
	const roles = {Font: "字体", ResourceID: "资源", Substitution: "替换", ImageMask: "蒙版", Thumbnail: "缩略", AttachID: "附件"};
	resourceElements.Reference.replaceChildren(...references.map((ref, index) => new Option(`${ref.page >= 0 ? `第${ref.page + 1}页` : ref.file}${ref.object ? ` · ${ref.object}` : ""} · ${roles[ref.role] || ref.role}`, String(offset + index))));
	if (!references.length) resourceElements.Reference.add(new Option(!item || item.complete ? "暂无引用" : "引用未知", ""));
	else resourceElements.Reference.value = String(index);
	resourceElements.Reference.title = count ? `${offset + 1}-${offset + references.length} / ${count}` : "";
	const groups = Math.ceil(count / 100);
	if (resourceElements.ReferenceGroup.options.length !== groups) {
		const fragment = document.createDocumentFragment();
		for (let group = 0; group < groups; group++) fragment.append(new Option(`${group + 1} / ${groups}`, String(group)));
		resourceElements.ReferenceGroup.replaceChildren(fragment);
	}
	resourceElements.ReferenceGroup.value = String(offset / 100);
	updateResourceControls();
	await syncResourceReference();
}

function updateResourceControls() {
	const item = resourceManager.selected, editing = state.editing;
	const disabled = resourceManager.busy || !state.doc || document.body.hasAttribute("data-busy");
	for (const button of resourceElements.Tabs.querySelectorAll("button")) button.disabled = disabled;
	resourceElements.Page.disabled = resourceElements.Object.disabled = disabled;
	const referenceCount = item?.referenceCount || 0;
	resourceElements.ReferenceGroup.hidden = referenceCount <= 100;
	resourceElements.ReferenceGroup.disabled = disabled || referenceCount <= 100;
	const actionable = item && ["Audio", "Video", "Attachment"].includes(item.kind);
	resourceElements.Add.hidden = resourceElements.Replace.hidden = resourceElements.Delete.hidden = !editing;
	resourceElements.Rename.hidden = !editing || item?.kind !== "Attachment";
	resourceElements.Rename.disabled = disabled || item?.kind !== "Attachment";
	resourceElements.Add.disabled = disabled;
	resourceElements.Reference.disabled = disabled || !referenceCount;
	const style = item && ["ColorSpace", "DrawParam"].includes(item.kind);
	resourceElements.Preview.disabled = disabled || (!style && (!item?.file || item.missing || item.kind === "Attachment"));
	resourceElements.Preview.textContent = style ? "查看" : "预览";
	resourceElements.Export.disabled = disabled || !item?.file || item.missing;
	resourceElements.Replace.disabled = disabled || !item || item.kind !== "Attachment" && !item.complete;
	resourceElements.Replace.textContent = style ? "修改" : item?.kind === "Font" ? item.file ? "外置" : "嵌入" : "替换";
	resourceElements.Delete.disabled = disabled || !item || item.kind !== "Attachment" && (!item.complete || Boolean(referenceCount));
	resourceElements.Delete.title = item?.kind !== "Attachment" && referenceCount ? "资源仍被引用" : "";
	resourceElements.Locate.disabled = disabled || !referenceCount;
	resourceElements.Binding.hidden = !editing || !actionable;
	const actions = resourceElements.Binding.hidden ? resourceElements.Actions : resourceElements.BindingActions;
	if (resourceElements.Close.parentElement !== actions) actions.append(resourceElements.Close);
	const target = resourceManager.targets.find(target => target.id === resourceElements.Object.value);
	resourceElements.Pick.disabled = disabled || !editing || !actionable;
	resourceElements.Bind.disabled = disabled || !actionable || !target || target.bound;
	resourceElements.Unbind.disabled = disabled || !actionable || !target?.bound;
}

async function syncResourceReference() {
	const tab = documentTab(), resourceManager = tab.resources;
	const reference = resourceManager.references[Number(resourceElements.Reference.value) - resourceManager.referenceOffset];
	if (reference?.page >= 0) resourceElements.Page.value = String(reference.page);
	await loadResourceTargets(reference?.page >= 0 ? reference.object : "");
	if (isForegroundDocument(tab)) updateResourceControls();
}

async function loadResourceTargets(preferred) {
	const tab = documentTab(), state = tab.state, resourceManager = tab.resources;
	const resource = resourceManager.selected;
	if (preferred === undefined) {
		const reference = resourceManager.references[Number(resourceElements.Reference.value) - resourceManager.referenceOffset];
		preferred = resourceElements.Object.value || (reference?.page === Number(resourceElements.Page.value) ? reference.object : "");
	}
	resourceManager.targets = [];
	resourceElements.Object.replaceChildren(new Option("选择", ""));
	if (!state.editing || !resource || !["Audio", "Video", "Attachment"].includes(resource.kind)) return;
	const page = Number(resourceElements.Page.value), sequence = state.openSeq;
	const items = await callTabWASM(tab, "ofdgoResourceTargets", page, resource.kind, resource.id);
	if (!isForegroundDocument(tab) || !resourceElements.Panel.open || sequence !== state.openSeq || resource !== resourceManager.selected || page !== Number(resourceElements.Page.value)) return;
	resourceManager.targets = items || [];
	const types = {TextObject: "文字", PathObject: "图形", ImageObject: "图片", CompositeObject: "组合", CompositeGraphicUnit: "组合"};
	for (const item of resourceManager.targets) resourceElements.Object.add(new Option(`${item.id} · ${(item.text || types[item.type] || "图元").slice(0, 50)}${item.bound ? " · 已绑定" : ""}`, item.id));
	const selected = canvasEditor.selected;
	if (!preferred && selected?.index === page && !selected.scoped) preferred = selected.id;
	resourceElements.Object.value = resourceManager.targets.some(item => item.id === preferred) ? preferred : "";
}

async function locateManagedResource() {
	const tab = documentTab(), state = tab.state, resourceManager = tab.resources;
	const item = resourceManager.selected, sequence = state.openSeq;
	if (!item?.referenceCount) return;
	const locations = await callTabWASM(tab, "ofdgoResourceLocations", item.kind, item.id, Number(resourceElements.Reference.value));
	if (!isForegroundDocument(tab) || sequence !== state.openSeq || !resourceElements.Panel.open || item !== resourceManager.selected) return;
	if (!locations?.length) { resourceElements.Status.textContent = "暂无可见位置"; return; }
	const focus = async location => {
		if (!isForegroundDocument(tab) || sequence !== state.openSeq) return;
		resourceElements.Panel.close();
		const box = location.bounds;
		await focusPageRegion({page: location.page, x: box.X, y: box.Y, width: box.W, height: box.H}, "资源", "center");
		if (!isForegroundDocument(tab) || sequence !== state.openSeq || !state.editing) return;
		const target = canvasEditor.pageItems(location.page - 1).find(item => item.id === location.object && !item.scoped);
		if (target) canvasEditor.select(target);
	};
	if (locations.length === 1) await focus(locations[0]);
	else resourceChoices(locations, location => `第${location.page}页 · ${location.object}`, location => resourceTask(() => focus(location)), "选择位置");
}

function resourceChoices(items, label, choose, title = "选择对象") {
	el.objectPickerTitle.textContent = title;
	el.objectPickerList.replaceChildren();
	for (const item of items) {
		const button = document.createElement("button");
		button.type = "button";
		button.className = "button";
		button.textContent = label(item);
		button.addEventListener("click", () => { el.objectPicker.close(); choose(item); });
		el.objectPickerList.append(button);
	}
	el.objectPicker.showModal();
}

async function beginResourcePick() {
	const tab = documentTab(), state = tab.state, resourceManager = tab.resources;
	if (!state.editing || !["Audio", "Video", "Attachment"].includes(resourceManager.selected?.kind)) return;
	const sequence = state.openSeq;
	if (!await pageView.renderPage(Number(resourceElements.Page.value), {fit: false})) return;
	if (!isForegroundDocument(tab) || sequence !== state.openSeq || !resourceElements.Panel.open) return;
	resourceManager.pick = {sequence, scope: documentPanelScope(state), status: el.statusText.textContent};
	resourceElements.Panel.close();
	canvasEditor.enabled = false;
	el.viewerPanel.classList.add("resource-picking");
	setBusy(false);
	setStatus("选取图元");
	el.viewerPanel.focus({preventScroll: true});
}

function finishResourcePick(reopen = true) {
	const picking = resourceManager.pick;
	if (!picking) return;
	resourceManager.pick = null;
	el.viewerPanel.classList.remove("resource-picking");
	if (picking.settings && (!reopen || picking.sequence !== state.openSeq)) {
		settings.suspended = false;
		settings.drafts.clear();
		settings.view = "";
		settings.sequence++;
	}
	if (picking.sequence === state.openSeq) {
		updateEditorTools();
		setBusy(document.body.hasAttribute("data-busy"));
		setStatus(picking.status);
		if (picking.settings) {
			if (reopen) {
				for (const panel of picking.parents) if (!panel.open) panel.showModal();
				settingsElements.Panel.showModal();
				(document.querySelector("#settingsPickObject") || document.querySelector("#settingsPickLayer"))?.focus();
			}
			settings.suspended = false;
		} else if (reopen && !resourceElements.Panel.open) {
			resourceElements.Panel.showModal();
			resourceElements.Pick.focus();
		}
	}
}

function pickResourceObject(event) {
	const tab = documentTab(), state = tab.state, resourceManager = tab.resources;
	const picking = resourceManager.pick;
	if (!picking || event.button !== 0) return;
	event.preventDefault();
	event.stopImmediatePropagation();
	if (resourceManager.busy || document.body.hasAttribute("data-busy")) return;
	if (picking.settings) { pickSettingsObject(event, picking).catch(error => { if (isForegroundDocument(tab)) { finishResourcePick(); settingsElements.Status.textContent = error.message; } }); return; }
	const items = canvasEditor.itemsAt(event).filter(item => !item.scoped && !item.draft);
	if (!items.length) { setStatus("未选中图元"); return; }
	resourceTask(async () => {
		const resource = resourceManager.selected, page = items[0].index;
		const targets = await callTabWASM(tab, "ofdgoResourceTargets", page, resource.kind, resource.id);
		if (!isForegroundDocument(tab) || resourceManager.pick !== picking || picking.sequence !== state.openSeq) return;
		const available = items.filter(item => targets?.some(target => target.id === item.id));
		if (!available.length) { setStatus("图元不可绑定"); return; }
		const choose = async item => {
			if (!isForegroundDocument(tab) || resourceManager.pick !== picking || picking.sequence !== state.openSeq) return;
			finishResourcePick();
			resourceElements.Page.value = String(item.index);
			await loadResourceTargets(item.id);
			if (isForegroundDocument(tab)) resourceElements.Status.textContent = "";
		};
		if (available.length === 1) await choose(available[0]);
		else resourceChoices(available, item => `${item.id} · ${(item.text || "图元").slice(0, 50)}`, item => resourceTask(() => choose(item)));
	});
}

function chooseResourceFile(action) {
	const item = action === "add" ? {kind: resourceManager.kind, id: ""} : resourceManager.selected;
	if (!item) return;
	resourceManager.fileAction = {action, item, sequence: state.openSeq, scope: documentPanelScope(state)};
	resourceElements.File.value = "";
	resourceElements.File.accept = {Audio: "audio/*", Video: "video/*", Image: ".png,.jpg,.jpeg,.bmp,.tif,.tiff,.jb2,.jbig2"}[item.kind] || "";
	resourceElements.File.click();
}

async function changeManagedResource(action, item, file) {
	const tab = documentTab(), state = tab.state;
	if (!item || !state.editing) return;
	releaseResourcePreview();
	const page = Number(resourceElements.Page.value), object = resourceElements.Object.value;
	const format = file?.name.includes(".") ? file.name.split(".").at(-1).toUpperCase() : "";
	const revision = state.editorInfo?.revision;
	const changed = item.kind === "Attachment" && action !== "bind" && action !== "unbind"
		? await changeDocument("ofdgoChangeAttachment", null, action, item.id, file ? [file.name] : [], file ? [file] : [])
		: await changeDocument("ofdgoChangeResource", null, action, item.kind, item.id, format, file || null, page, object);
	if (!tab.closed && (changed || state.editorInfo?.revision !== revision)) {
		await loadManagedResources(tab);
		if (!isForegroundDocument(tab)) return;
		if (action === "bind" || action === "unbind") {
			resourceElements.Page.value = String(page);
			await loadResourceTargets(object);
			if (isForegroundDocument(tab)) resourceElements.Status.textContent = action === "bind" ? "已绑定" : "已解绑";
		}
	}
}

function releaseResourcePreview() {
	resourceManager.sequence++;
	for (const player of resourceElements.Viewer.querySelectorAll("audio, video")) { player.pause(); player.removeAttribute("src"); player.load(); }
	resourceElements.Viewer.replaceChildren();
	resourceElements.Viewer.hidden = true;
	if (resourceManager.url) URL.revokeObjectURL(resourceManager.url);
	resourceManager.url = null;
}

async function previewManagedResource() {
	const tab = documentTab(), state = tab.state, resourceManager = tab.resources;
	const callWASM = (name, ...args) => callTabWASM(tab, name, ...args);
	if (["ColorSpace", "DrawParam"].includes(resourceManager.selected?.kind)) {
		await openResourceSettings(resourceManager.selected.kind, resourceManager.selected.id);
		return;
	}
	const item = resourceManager.selected;
	if (!item?.file) return;
	if (state.doc.encryption?.encrypted && !await requestConfirmation("资源将按明文读取，是否继续？")) return;
	releaseResourcePreview();
	const sequence = resourceManager.sequence, openSeq = state.openSeq;
	let result;
	try {
		result = item.kind === "Font"
			? await callWASM("ofdgoFontPreview", item.id)
			: await callWASM("ofdgoExportResource", JSON.stringify([item.kind, item.id]), null);
	} catch (error) {
		if (sequence !== resourceManager.sequence || openSeq !== state.openSeq || !resourceElements.Panel.open) return;
		if (item.kind === "Font") throw new Error("字体无法预览", {cause: error});
		throw error;
	} finally {
		state.exportRequestID = 0;
		if (isForegroundDocument(tab)) el.cancelExportButton.hidden = true;
	}
	if (sequence !== resourceManager.sequence || openSeq !== state.openSeq || !resourceElements.Panel.open) return;
	const format = item.kind === "Font" ? "svg" : item.format.toLowerCase();
	const mime = {svg: "image/svg+xml", mp3: "audio/mpeg", wav: "audio/wav", ogg: "audio/ogg", m4a: "audio/mp4", mp4: "video/mp4", webm: "video/webm", png: "image/png", jpeg: "image/jpeg", jpg: "image/jpeg", bmp: "image/bmp"}[format] || "application/octet-stream";
	resourceManager.url = URL.createObjectURL(new Blob([result.bytes || result.blob], {type: mime}));
	const image = item.kind === "Image" || item.kind === "Font";
	const element = document.createElement(image ? "img" : item.kind === "Audio" ? "audio" : "video");
	element.src = resourceManager.url;
	if (image) element.alt = item.name;
	else { element.controls = true; element.preload = "metadata"; }
	element.addEventListener("error", () => {
		if (sequence === resourceManager.sequence && openSeq === state.openSeq) resourceElements.Status.textContent = "浏览器无法预览此格式";
	});
	resourceElements.Viewer.append(element);
	resourceElements.Viewer.hidden = false;
}

function resourceFileName(item) {
	let name = item.kind === "Font" ? item.file.split("/").at(-1) : item.name;
	if (!name.includes(".") && item.format) name += "." + item.format.toLowerCase();
	return name;
}

function downloadAttachment(attachment) {
	return downloadResource(attachment, "ofdgoExportAttachment", "附件");
}

el.attachmentCancel.addEventListener("click", () => el.attachmentDialog.close());
el.attachmentDialog.addEventListener("cancel", event => { if (el.attachmentForm.inert) event.preventDefault(); });
el.attachmentDialog.addEventListener("close", () => {
	el.attachmentForm.reset();
	delete el.attachmentDialog.attachmentEdit;
});
el.attachmentForm.addEventListener("submit", async event => {
	event.preventDefault();
	const edit = el.attachmentDialog.attachmentEdit;
	if (!edit || edit.openSeq !== state.openSeq || !el.attachmentForm.reportValidity()) return;
	const files = Array.from(el.attachmentFile.files);
	const names = edit.action === "rename" ? [el.attachmentName.value] : files.map(file => file.name);
	if (await changeDocument("ofdgoChangeAttachment", null, edit.action, edit.id, names, files)) {
		el.attachmentDialog.close();
		setStatus(edit.action === "rename" ? "附件已改名" : "附件已添加");
	}
});

function openAttachmentDialog(action, attachment) {
	if (!state.editing || !state.ready || state.exporting || document.body.hasAttribute("data-busy")) return;
	const rename = action === "rename";
	el.attachmentForm.reset();
	el.attachmentDialog.attachmentEdit = {action, id: attachment?.id || "", openSeq: state.openSeq};
	el.attachmentTitle.textContent = rename ? "附件改名" : "添加附件";
	el.attachmentSubmit.textContent = rename ? "确定" : "添加";
	el.attachmentFileRow.hidden = rename;
	el.attachmentFile.required = !rename;
	el.attachmentNameRow.hidden = !rename;
	el.attachmentName.required = rename;
	el.attachmentName.value = attachment?.name || "";
	el.attachmentStatus.textContent = "";
	el.attachmentDialog.showModal();
	(rename ? el.attachmentName : el.attachmentFile).focus();
}

async function saveDocumentImage(resource) {
	if (!resource || !state.doc || document.body.hasAttribute("data-busy")) return;
	const seq = state.openSeq;
	try {
		const image = await callWASM("ofdgoImages", resource);
		if (seq !== state.openSeq) return;
		await downloadResource({id: image.id, fileName: image.name}, "ofdgoExportImages", "图片");
	} catch (err) {
		if (seq === state.openSeq) showError(err, false);
	}
}

el.saveImageButton.addEventListener("click", () => saveDocumentImage(canvasEditor.selected?.imageResource));

async function downloadResource(attachment, api, label) {
	const tab = documentTab(), state = tab.state;
	if (!state.doc || document.body.hasAttribute("data-busy")) {
		return;
	}
	if (state.doc.encryption?.encrypted && !await requestConfirmation(`${label}将按明文下载，是否继续？`)) return;
	const openSeq = state.openSeq;
	const originalDocument = state.doc;
	state.exporting = true;
	updateControls();
	setBusy(true, `正在读取${label}`, null, `正在读取${label}`, tab);
	try {
		const file = nativeHost || window.showSaveFilePicker ? await saveFilePicker({ suggestedName: attachment.fileName }) : null;
		if (tab.closed || state.doc !== originalDocument) {
			return;
		}
		const result = await callTabWASM(tab, api, attachment.id, file);
		if (tab.closed || state.doc !== originalDocument) {
			return;
		}
		if (result.blob) {
			downloadBytes(result.blob, result.mime, attachment.fileName);
		}
		tab.status = `${label}已下载（${formatBytes(result.size)}）`;
		if (isForegroundDocument(tab)) setStatus(tab.status);
	} catch (err) {
		if (isForegroundDocument(tab) && openSeq === state.openSeq) {
			if (err.name === "AbortError") {
				setStatus("下载已取消");
			} else {
				showError(err, false);
			}
		}
	} finally {
		state.exporting = false;
		state.exportRequestID = 0;
		setBusy(false, "", 0, "", tab);
		if (isForegroundDocument(tab)) {
			el.cancelExportButton.hidden = true;
			updateControls();
		}
	}
}

function encryptionMode() {
	const info = state.doc?.encryption;
	if (!info?.encrypted) return "none";
	if (info.layers !== 1 || info.policyRequired) return "keep";
	return info.method === "1.1.1" ? "password" : info.method === "1.1.2" ? "certificate" : "keep";
}

function openEncryptionPanel() {
	if (!state.doc || document.body.hasAttribute("data-busy")) return;
	clearEncryptionForm();
	el.encryptionType.value = encryptionMode();
	el.encryptionKeep.hidden = el.encryptionType.value !== "keep";
	updateEncryptionType();
	el.encryptionPanel.showModal();
}

function updateEncryptionType() {
	const mode = el.encryptionType.value;
	const existing = mode !== "none" && mode === encryptionMode() && !state.encryptionChanged;
	const password = mode === "password" && !existing;
	const certificates = mode === "certificate" && !existing;
	el.encryptionCurrentRow.hidden = !existing;
	el.encryptionCurrent.textContent = mode === "password" ? state.encryptionDirty ? "待保存" : "已加密" : (state.doc?.encryption?.users || []).join("、") || "已加密";
	el.encryptionChange.hidden = mode === "keep";
	el.encryptionUserRow.hidden = el.encryptionPasswordRow.hidden = el.encryptionConfirmRow.hidden = !password;
	el.encryptionCertificatesRow.hidden = !certificates;
	el.encryptionPassword.required = el.encryptionConfirm.required = password;
	el.encryptionCertificates.required = certificates;
	el.encryptionUser.value = mode === "password" && encryptionMode() === mode ? state.doc.encryption.users?.[0] || "" : "";
	el.encryptionPassword.value = el.encryptionConfirm.value = "";
	el.encryptionCertificates.value = "";
	el.encryptionStatus.textContent = mode === "keep" && state.doc?.encryption?.policyRequired ? "请重新选择加密方式" : "";
	el.encryptionSubmit.disabled = mode === "keep" && Boolean(state.doc?.encryption?.policyRequired);
	el.encryptionSubmit.textContent = mode === encryptionMode() && !state.encryptionChanged ? "确定"
		: mode === "none" ? "解密" : mode !== encryptionMode() ? "加密" : mode === "password" ? "改密" : "修改";
}

async function applyEncryption(event) {
	const tab = documentTab(), state = tab.state, wasmSeq = state.wasmSeq;
	const valid = () => !tab.closed && wasmSeq === state.wasmSeq;
	const active = () => valid() && isForegroundDocument(tab);
	const update = action => {
		if (!valid()) return;
		if (active()) action();
		else documentDialogs?.updateFor(tab, el.encryptionPanel, () => { if (active()) action(); });
	};
	if (state.saving) { event.preventDefault(); return; }
	event.preventDefault();
	if (!state.doc || document.body.hasAttribute("data-busy") || el.encryptionSubmit.disabled) return;
	const mode = el.encryptionType.value;
	if (mode === encryptionMode() && !state.encryptionChanged) {
		el.encryptionPanel.close();
		return;
	}
	if (mode === "password" && !el.encryptionPassword.value) {
		el.encryptionStatus.textContent = "请输入口令";
		el.encryptionPassword.focus();
		return;
	}
	if (mode === "password" && el.encryptionPassword.value !== el.encryptionConfirm.value) {
		el.encryptionStatus.textContent = "口令不一致";
		el.encryptionConfirm.focus();
		return;
	}
	if (mode === "certificate" && !el.encryptionCertificates.files?.length) {
		el.encryptionStatus.textContent = "请选择接收者证书";
		return;
	}
	const status = mode === "none" ? "保存后将解密" : mode !== encryptionMode() ? "保存后将加密"
		: mode === "password" ? "保存后将改密" : "保存后将生效";
	const options = mode === "none" ? null : { password: new TextEncoder().encode(el.encryptionPassword.value), userName: el.encryptionUser.value.trim() };
	const certificates = Array.from(el.encryptionCertificates.files || []);
	el.encryptionForm.inert = true;
	setBusy(true, "正在处理文档", null, "", tab);
	try {
		if (mode === "certificate") options.recipients = await encryptionRecipients(certificates);
		if (!valid()) return;
		const doc = await callTabWASM(tab, "ofdgoSetEncryption", options);
		if (!valid()) return;
		if (!state.editorInfo) state.savedRevision = doc.revision;
		state.encryptionDirty = true;
		state.editorInfo = { revision: doc.revision, canUndo: doc.canUndo, canRedo: doc.canRedo,
			pageCapabilities: doc.pageCapabilities, editWarnings: doc.editWarnings || [] };
		state.dirty = state.packageDirty || state.encryptionDirty || doc.revision !== state.savedRevision;
		state.doc.encryption = doc.encryption;
		state.ofdBytes = null;
		tab.status = status;
		updateWorkspaceDirty();
		update(() => { el.encryptionPanel.close(); renderSecurity(); setStatus(status); });
	} catch (err) {
		if (valid()) tab.status = err.message;
		update(() => { el.encryptionStatus.textContent = err.message; });
	} finally {
		options?.password.fill(0);
		update(() => {
			el.encryptionPassword.value = el.encryptionConfirm.value = "";
			el.encryptionForm.inert = false;
		});
		if (valid()) setBusy(false, "", 0, "", tab);
	}
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
	el.encryptionForm.inert = false;
	el.encryptionPassword.value = "";
	el.encryptionConfirm.value = "";
	el.encryptionUser.value = "";
	el.encryptionCertificates.value = "";
	state.encryptionChanged = false;
}

function renderSecurity() {
	const encryption = state.doc?.encryption;
	el.securityPanel.hidden = !state.doc;
	el.securityEncryption.textContent = state.encryptionDirty ? "待保存" : encryption?.encrypted ? "已加密" : "未加密";
	const scheme = { "1.1.1": "口令", "1.1.2": "证书" }[encryption?.method];
	el.securityScheme.textContent = encryption?.layers > 1 ? "多重" : scheme || encryption?.method || "";
	el.securitySchemeRow.hidden = !encryption?.encrypted;
	el.securityMethod.textContent = scheme ? "SM4-CBC" : "";
	el.securityMethodRow.hidden = !encryption?.encrypted || !scheme;
	el.encryptSaveButton.disabled = !state.doc || !state.ready || state.exporting || state.saving || document.body.hasAttribute("data-busy");
	el.verifyButton.disabled = el.encryptSaveButton.disabled;
	el.signButton.disabled = el.encryptSaveButton.disabled;
	el.signatureClear.hidden = !state.editing;
	el.signatureClear.disabled = el.encryptSaveButton.disabled || !state.editing || !!state.doc?.detailsPending || !state.doc?.signatures?.length;
	const scope = JSON.stringify([documentTab().id, documentContentKey(state)]);
	if (!state.doc?.detailsPending || metaContents.get(el.signaturePanel) !== scope) {
		renderMetaContent(el.signatureList, [scope, state.doc?.signatures, state.doc?.signatureError, state.editing], renderSignatures);
		metaContents.set(el.signaturePanel, scope);
	}
	for (const button of el.signatureList.querySelectorAll(".signature-remove")) {
		button.hidden = !state.editing;
		button.disabled = el.signatureClear.disabled;
	}
}

function cancelSigning() {
	if (el.signCancel.disabled) return;
	state.signCanceled = true;
	el.signStatus.textContent = "正在取消操作";
	cancelExport();
}

function finishSealPosition(returnToForm = true) {
	const preview = sealPreview;
	if (!preview) return;
	sealPreview = null;
	if (preview.pointer != null && el.viewerPanel.hasPointerCapture(preview.pointer)) el.viewerPanel.releasePointerCapture(preview.pointer);
	URL.revokeObjectURL(preview.url);
	sealPosition.Layer.remove();
	sealPosition.Image.removeAttribute("src");
	el.viewerPanel.classList.remove("seal-picking");
	canvasEditor.enabled = preview.enabled;
	if (preview.sidebar && COMPACT_LAYOUT.matches) {
		Object.assign(state, preview.sidebar);
		updateSidebarState();
	}
	setBusy(document.body.hasAttribute("data-busy"));
	if (returnToForm && preview.sequence === state.openSeq && !el.signPanel.open) el.signPanel.showModal();
	if (preview.sequence === state.openSeq) setStatus(preview.status);
}

async function openSealPosition() {
	const tab = documentTab(), state = tab.state, wasmSeq = state.wasmSeq;
	const active = () => !tab.closed && wasmSeq === state.wasmSeq && isForegroundDocument(tab);
	const file = el.signSeal.files?.[0];
	if (!file || !state.doc || document.body.hasAttribute("data-busy")) return;
	const sequence = state.openSeq;
	const range = el.signPages.value.trim(), mode = el.signPlacement.value;
	const keepRatio = document.querySelector("#signKeepRatio").checked, count = state.doc.pageCount;
	let preview;
	setBusy(true, "", 0, "", tab);
	try {
		const selected = await callTabWASM(tab, "ofdgoParsePageRange", range);
		if (!active()) return;
		if (mode === "seam" && selected.length < 2) throw new Error("骑缝签章至少选择两页");
		const data = new Uint8Array(await file.arrayBuffer());
		if (!active()) return;
		const result = await callTabWASM(tab, "ofdgoPreviewSignatureSeal", data, `1-${count}`);
		if (!active() || sequence !== state.openSeq || !el.signPanel.open || file !== el.signSeal.files?.[0]) return;
		sealPreview = {pages:result.pages, selected, range, mode, ratio:keepRatio ? result.width / result.height : 0, sequence, enabled:canvasEditor.enabled, status:el.statusText.textContent, url:URL.createObjectURL(new Blob([result.svg], {type:"image/svg+xml"}))};
		preview = sealPreview;
		sealPosition.Image.src = sealPreview.url;
		await sealPosition.Image.decode();
		if (sealPreview !== preview) return;
		if (!active() || sequence !== state.openSeq || !el.signPanel.open) {
			if (active()) finishSealPosition(false);
			return;
		}
		canvasEditor.enabled = false;
		if (COMPACT_LAYOUT.matches) {
			sealPreview.sidebar = {showPages:state.showPages, showMeta:state.showMeta};
			state.showPages = state.showMeta = false;
			updateSidebarState();
		}
		el.viewerPanel.classList.add("seal-picking");
		el.signPanel.close();
		el.signStatus.textContent = "";
		setStatus("拖动选取签章区域，Esc 取消");
		el.viewerPanel.focus({preventScroll:true});
	} catch (error) {
		if (active() && sequence === state.openSeq && (!preview || sealPreview === preview)) {
			finishSealPosition();
			el.signStatus.textContent = error.message;
		}
	}
	finally {
		if (preview && sealPreview !== preview) URL.revokeObjectURL(preview.url);
		if (!tab.closed && wasmSeq === state.wasmSeq) setBusy(false, "", 0, "", tab);
	}
}

function sealPointer(event) {
	const preview = sealPreview;
	const point = pagePoint(event.clientX, event.clientY, preview.surface.getBoundingClientRect(), preview.page, state.rotation);
	return {x:preview.page.x + point.x, y:preview.page.y + point.y};
}

function beginSealDrag(event) {
	const preview = sealPreview;
	if (!preview || preview.confirming || event.button !== 0) return;
	const surface = event.target.closest(".page-surface");
	if (!surface) return;
	event.preventDefault();
	event.stopImmediatePropagation();
	const index = Number(surface.closest(".page-shell").dataset.pageIndex);
	if ((preview.mode === "seam" || preview.selected.length > 1) && !preview.selected.includes(index)) {
		setStatus("请在所选页码范围内定位");
		return;
	}
	preview.page = preview.pages[index];
	preview.surface = surface;
	preview.start = sealPointer(event);
	preview.pointer = event.pointerId;
	preview.box = null;
	sealPosition.Stamp.hidden = true;
	surface.append(sealPosition.Layer);
	el.viewerPanel.setPointerCapture(event.pointerId);
}

function moveSealDrag(event) {
	const preview = sealPreview;
	if (!preview?.start || preview.pointer !== event.pointerId) return;
	event.preventDefault();
	event.stopImmediatePropagation();
	const point = sealPointer(event), start = preview.start, page = preview.page;
	const dx = point.x - start.x, dy = point.y - start.y;
	let width = Math.abs(dx), height = Math.abs(dy);
	const count = preview.mode === "seam" ? preview.selected.length : 1;
	if (preview.ratio) {
		width = Math.min(width, height * preview.ratio / count);
		height = width * count / preview.ratio;
	}
	const x = dx < 0 ? start.x - width : start.x, y = dy < 0 ? start.y - height : start.y;
	preview.box = {x:preview.mode === "seam" ? page.x + page.width - x - width : x, y, width:width * count, height};
	sealPosition.Stamp.hidden = false;
	Object.assign(sealPosition.Stamp.style, {left:`${(x-page.x)/page.width*100}%`, top:`${(y-page.y)/page.height*100}%`, width:`${width/page.width*100}%`, height:`${height/page.height*100}%`});
	Object.assign(sealPosition.Image.style, {width:`${count*100}%`, left:`${preview.mode === "seam" ? -preview.selected.indexOf(page.index)*100 : 0}%`});
}

async function confirmSealPosition(event) {
	const preview = sealPreview;
	if (!preview?.start || preview.pointer !== event.pointerId || preview.confirming) return;
	moveSealDrag(event);
	preview.start = null;
	el.viewerPanel.releasePointerCapture(event.pointerId);
	const box = preview.box;
	if (!box || box.width < 0.01 || box.height < 0.01) { sealPosition.Layer.remove(); return; }
	preview.confirming = true;
	const range = preview.mode === "normal" && preview.selected.length === 1 ? String(preview.page.index + 1) : preview.range;
	try {
		await callWASM("ofdgoSignaturePlacement", {...box, pages:range, placement:preview.mode});
		if (sealPreview !== preview || preview.sequence !== state.openSeq) return;
		el.signPages.value = range;
		for (const [name, key] of [["X", "x"], ["Y", "y"], ["Width", "width"], ["Height", "height"]]) el[`sign${name}`].value = String(box[key]);
		finishSealPosition();
	} catch (error) {
		if (sealPreview === preview) { finishSealPosition(); el.signStatus.textContent = error.message; }
	}
}

function updateSignPlacement() {
	const stamp = Boolean(el.signSeal.files?.length);
	el.signPlacementFields.hidden = !stamp;
	el.signPlacement.disabled = !stamp;
	for (const input of [el.signPages, el.signX, el.signY, el.signWidth, el.signHeight]) {
		input.disabled = !stamp;
		input.required = stamp;
	}
	document.querySelector('label[for="signX"]').textContent = el.signPlacement.value === "seam" ? "右侧内缩" : "横向";
}

async function signDocument(event) {
	const tab = documentTab(), state = tab.state, wasmSeq = state.wasmSeq;
	const valid = () => !tab.closed && wasmSeq === state.wasmSeq;
	const active = () => valid() && isForegroundDocument(tab);
	const update = action => {
		if (!valid()) return;
		if (active()) action();
		else documentDialogs?.updateFor(tab, el.signPanel, () => { if (active()) action(); });
	};
	if (state.saving) { event.preventDefault(); return; }
	event.preventDefault();
	if (!state.doc || document.body.hasAttribute("data-busy")) return;
	if (!el.signCertificate.files?.length || !el.signKey.files?.length || !el.signRoots.files?.length) {
		el.signStatus.textContent = "请选择证书、私钥和信任根";
		return;
	}
	if (el.signMode.value === "replace" && !await requestConfirmation("替换现有签名，是否继续？")) return;
	let key;
	const keyPassword = new TextEncoder().encode(el.signKeyPassword.value);
	const files = Object.fromEntries(["Key", "Certificate", "Roots", "Intermediates", "Seal"].map(name => [name, Array.from(el[`sign${name}`].files || [])]));
	const options = { keyPassword, pages:el.signPages.value.trim(), placement:el.signPlacement.value,
		x:Number(el.signX.value), y:Number(el.signY.value), width:Number(el.signWidth.value), height:Number(el.signHeight.value),
		mode:el.signMode.value, lock:el.signLock.checked, compression:compressionOptions(el.outputCompression.value) };
	const name = `${baseFileName()}_签章.ofd`;
	el.signKeyPassword.value = "";
	el.signKey.value = "";
	state.exporting = true;
	state.signing = true;
	state.signCanceled = false;
	el.signCancel.disabled = false;
	setBusy(true, "正在签署文档", null, "正在签署文档", tab);
	el.signForm.querySelectorAll("input, select, button[type=submit]").forEach(input => { input.disabled = true; });
	try {
		const file = nativeHost || window.showSaveFilePicker ? await saveFilePicker({suggestedName:name, types:[{description:"OFD",accept:{"application/ofd":[".ofd"]}}]}) : null;
		if (!valid() || state.signCanceled) return;
		if (active() && (canvasEditor.input || canvasEditor.crop || canvasEditor.nudge || canvasEditor.nudgeCommit)) {
			setBusy(false, "", 0, "", tab);
			if (!await canvasEditor.commitNudge() || !active()) return;
			if (!await canvasEditor.commitText() || !active()) return;
			if (!await canvasEditor.commitCrop() || !active()) return;
			setBusy(true, "正在签署文档", null, "正在签署文档", tab);
		}
		key = (await securityFiles({files:files.Key}))[0];
		if (!valid() || state.signCanceled) return;
		const [certificate, roots, intermediates, seal] = await Promise.all(["Certificate", "Roots", "Intermediates", "Seal"].map(name => securityFiles({files:files[name]})));
		Object.assign(options, {key, certificate:certificate[0], roots, intermediates, seal:seal[0] || null});
		if (!valid() || state.signCanceled) return;
		const result = await callTabWASM(tab, "ofdgoSaveSigned", null, options, file);
		if (!valid()) return;
		if (result.blob) downloadBytes(result.blob, result.mime, name);
		tab.status = "签署已另存";
		update(() => { el.signPanel.close(); setStatus("签署已另存"); });
	} catch (err) {
		if (valid()) tab.status = err.name === "AbortError" ? "签署已取消" : err.message;
		update(() => { el.signStatus.textContent = err.name === "AbortError" ? "签署已取消" : err.message; });
	} finally {
		key?.fill(0);
		keyPassword.fill(0);
		state.signing = false;
		state.exporting = false;
		state.exportRequestID = 0;
		update(() => {
			el.signKey.value = "";
			el.signForm.querySelectorAll("input, select, button[type=submit]").forEach(input => { input.disabled = false; });
			el.signCancel.disabled = false;
			if (state.signCanceled) el.signStatus.textContent = "签署已取消";
			updateSignPlacement();
			el.cancelExportButton.hidden = true;
		});
		if (valid()) {
			setBusy(false, "", 0, "", tab);
			if (active()) updateControls();
		}
	}
}

async function securityFiles(input) {
	return Promise.all(Array.from(input.files || [], async file => new Uint8Array(await file.arrayBuffer())));
}

async function verifyDocument(event) {
	event.preventDefault();
	const tab = documentTab(), state = tab.state, wasmSeq = state.wasmSeq;
	const valid = () => !tab.closed && wasmSeq === state.wasmSeq;
	const active = () => valid() && isForegroundDocument(tab);
	const update = action => {
		if (!valid()) return;
		if (active()) action();
		else documentDialogs?.updateFor(tab, el.verifyPanel, () => { if (active()) action(); });
	};
	if (!state.doc || document.body.hasAttribute("data-busy")) return;
	const files = Object.fromEntries(["Roots", "Certs", "TimeRoots", "Tokens", "CRLs", "OCSP"].map(name => [name, Array.from(el[`verify${name}`].files || [])]));
	const options = { requireTimestamp:el.verifyRequireTime.checked, requireRevocation:el.verifyRequireRevocation.checked };
	setBusy(true, "正在验证签名", null, "正在验证签名", tab);
	el.verifyForm.inert = true;
	try {
		const [roots, certificates, timestampRoots, tokens, crls, ocsp] = await Promise.all(Object.values(files).map(values => securityFiles({files:values})));
		Object.assign(options, {roots, certificates, timestampRoots, tokens, crls, ocsp});
		if (!valid()) return;
		const info = await callTabWASM(tab, "ofdgoVerifySignatures", options);
		if (!valid()) return;
		Object.assign(state.doc, info);
		tab.status = info.signatureCount ? "验签完成" : "无签名";
		update(() => { renderMeta(); el.verifyPanel.close(); setStatus(tab.status); });
	} catch (err) {
		if (valid()) tab.status = err.message;
		update(() => { el.verifyStatus.textContent = err.message; });
	} finally {
		update(() => { el.verifyForm.inert = false; });
		if (valid()) setBusy(false, "", 0, "", tab);
	}
}

function openExportPanel() {
	if (!state.doc || document.body.hasAttribute("data-busy")) {
		return;
	}
	el.exportForm.reset();
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
	el.exportRange.removeAttribute("data-invalid");
	el.exportSubmit.disabled = specified;
	el.exportRangeStatus.textContent = specified && value ? "正在校验页码" : "";
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
			el.exportRange.setAttribute("data-invalid", "true");
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
		pages.value = item.pages;
		pages.disabled = batch.running;
		pages.addEventListener("change", () => { item.pages = pages.value; reset(); });
		pagesCell.append(pages);
		const formatCell = row.insertCell();
		formatCell.dataset.label = "格式";
		const formatControl = document.createElement("span");
		formatControl.className = "convert-format";
		const format = document.createElement("select");
		format.append(new Option("默认", ""), ...batch.formats.map(value => new Option(value.label, value.value)));
		format.value = item.format;
		format.disabled = batch.running;
		format.addEventListener("change", () => { item.format = format.value; reset(); });
		formatControl.append(format);
		formatCell.append(formatControl);
		item.cell = row.insertCell();
		item.cell.textContent = item.text;
		item.cell.dataset.status = item.status;
		item.cell.dataset.label = "状态";
		const action = row.insertCell();
		const remove = document.createElement("button");
		remove.type = "button";
		remove.className = "icon-button remove-button";
		remove.title = "移除";
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
	for (const name of ["Add", "Clear", "Format", "Compression", "FontMode", "Destination", "DPI"]) batchElements[name].disabled = batch.running;
	batchElements.FontRow.hidden = !(batch.items.length ? batch.items : [null]).some(item => (item?.format || batchElements.Format.value) === "ofd");
	batchElements.Compression.disabled ||= (batch.items.length ? batch.items : [null]).every(item => (item?.format || batchElements.Format.value) === "txt");
	batchElements.Clear.disabled ||= !batch.items.length;
	batchElements.Start.disabled = batch.running || !batch.items.length || !batch.formats.length;
	batchElements.Start.hidden = batch.running;
	batchElements.Cancel.hidden = !batch.running;
	batchElements.Cancel.disabled = batch.canceled;
	batchElements.Close.textContent = batch.running ? "收起" : "关闭";
	batchElements.Count.textContent = batch.items.length ? `${batch.items.length}个文件` : "";
	batchElements.DPIRow.hidden = !(batch.items.length ? batch.items : [null]).some(batchUsesDPI);
	batchElements.DPIValue.textContent = batchElements.DPI.value;
	batchElements.Button.toggleAttribute("data-running", batch.running);
}

function batchUsesDPI(item) {
	const format = item?.format || batchElements.Format.value;
	if (batch.formats.find(value => value.value === format)?.raster) return true;
	return Boolean(item && /\.pdf$/i.test(item.file.name) && format !== "txt"
		&& (format !== "pdf" || item.pages.trim()));
}

function batchStatus(item, status, text) {
	item.status = status;
	item.text = text;
	item.cell.textContent = text;
	item.cell.dataset.status = status;
}

function batchProgress(item, position, count, progress) {
	if (batch.canceled) return;
	const now = performance.now();
	const previous = item ? item.progress : batch.progress;
	const sameStage = previous?.item === item && previous?.progress.phase === progress.phase;
	if (sameStage && now - previous.updated < 80 && progress.completed !== progress.total) return;
	const current = { item, position, count, progress, started: sameStage ? previous.started : now, updated: now };
	if (item) item.progress = current;
	else batch.progress = current;
	updateBatchProgress();
}

function updateBatchProgress() {
	if (batch.canceled) return;
	for (const item of batch.jobs) {
		if (item.status !== "running" || !item.progress) continue;
		const progress = item.progress.progress;
		const stage = progress.phase?.includes("compress") ? "压缩" : progress.phase?.startsWith("write") ? "写入" : { open: "读取", pages: "解析", convert: "转换", prepare: "准备", fonts: "字体", export: "导出" }[progress.phase] || "处理";
		batchStatus(item, "running", `${stage}${progress.total > 0 ? `\n${progress.completed}/${progress.total}` : ""}`);
	}
	if (!batch.progress) {
		const completed = batch.jobs.filter(item => ["done", "ready", "unchanged", "failed", "canceled"].includes(item.status)).length;
		const running = batch.jobs.filter(item => item.status === "running").length;
		batchElements.Status.textContent = `完成 ${completed}/${batch.jobs.length} · 运行 ${running}`;
		batchElements.Progress.max = batch.jobs.length || 1;
		batchElements.Progress.value = completed;
		return;
	}
	const { item, position, count, progress, started } = batch.progress;
	const compressing = progress.phase === "compress" || progress.phase === "write.compress";
	const fontMode = (item?.format || batchElements.Format.value) === "ofd" ? batchElements.FontMode.value : "0";
	const stage = compressing ? "压缩" : progress.phase?.startsWith("write.") ? "写入" : { open: "读取", pages: "解析", convert: "转换", prepare: "准备", fonts: { 1: "嵌入", 2: "外置" }[fontMode] || "整理", "fonts.prepare": "准备", "fonts.scan": "检查", "fonts.commit": "整理", resources: "资源", references: "检查", write: "写入", export: "导出", pack: "打包", commit: "保存" }[progress.phase] || "处理";
	const detail = progress.total > 0 ? `${progress.completed}/${progress.total}` : (compressing || progress.phase === "fonts") ? `已处理 ${progress.completed || 0} 项` : "";
	const seconds = Math.floor((performance.now() - started) / 1000);
	if (item) batchStatus(item, "running", stage);
	batchElements.Status.textContent = `${progress.phase === "pack" ? "" : `文件 ${position + 1}/${count} · `}正在${stage}${detail ? ` · ${detail}` : ""}${seconds ? ` · ${seconds}秒` : ""}`;
	batchElements.Progress.max = progress.total || 1;
	if (progress.total > 0) batchElements.Progress.value = Math.min(progress.completed, progress.total);
	else batchElements.Progress.removeAttribute("value");
}

function batchCall(slot, name, args, onProgress) {
	return new Promise((resolve, reject) => {
		if (slot.error) { reject(slot.error); return; }
		if (batch.canceled) { reject(new DOMException("已取消转换", "AbortError")); return; }
		const id = ++batch.sequence;
		batch.requests.set(id, { slot, resolve, reject, onProgress, destination: name === "ofdgoConvertFile" ? args[3] : null });
		try { slot.worker.postMessage({ id, name, args }); }
		catch (err) { batch.requests.delete(id); reject(err); }
	});
}

function startBatchWorker() {
	return new Promise((resolve, reject) => {
		const worker = createEngineWorker({batch: true});
		const slot = { worker, error: null };
		batch.workers.add(slot);
		const fail = async error => {
			slot.cancel = null;
			slot.error = error;
			worker.terminate();
			reject(error);
			for (const [id, request] of batch.requests) {
				if (request.slot !== slot) continue;
				batch.requests.delete(id);
				if (request.destination && request.outputName) {
					try { await request.destination.removeEntry(request.outputName, { recursive: true }); }
					catch (err) { error = new Error(`${error.message}；清理失败：${err.message}`); }
				}
				request.reject(error);
			}
		};
		slot.cancel = () => fail(new DOMException("已取消转换", "AbortError"));
		worker.onerror = event => fail(new Error(event.message || "转换引擎异常"));
		worker.onmessageerror = () => fail(new Error("转换数据传递失败"));
		worker.onmessage = async ({ data }) => {
			if (data.type === "ready") { slot.cancel = null; resolve(slot); return; }
			if (data.type === "exit") { fail(new Error(data.error)); return; }
			if (data.type === "progress") {
				if (!batch.canceled) batchElements.Status.textContent = { download: "正在下载", compile: "正在编译", start: "正在启动" }[data.phase] || "正在准备";
				return;
			}
			const request = batch.requests.get(data.id);
			if (!request) return;
			if (data.type === "font") {
				try {
					const font = batch.localFonts.get(data.name);
					if (!font) throw new Error("系统字体不可用");
					if (!batch.fontData.has(font)) batch.fontData.set(font, fontManager.read(font));
					const bytes = await batch.fontData.get(font);
					worker.postMessage({ type: "font", request: data.request, bytes });
				} catch (err) {
					worker.postMessage({ type: "font", request: data.request, error: err.message });
				}
				return;
			}
			if (data.type === "output") { request.outputName = data.name; return; }
			if (data.type === "conversion") { request.onProgress?.(data); return; }
			if (data.memory) slot.memory = data.memory;
			batch.requests.delete(data.id);
			if (data.ok) request.resolve(data.data);
			else request.reject(Object.assign(new Error(data.error), { code: data.code, name: data.canceled ? "AbortError" : "Error" }));
		};
	});
}

function cancelBatch() {
	batch.canceled = true;
	batchElements.Cancel.disabled = true;
	batch.progress = null;
	batchElements.Status.textContent = "正在取消";
	const request = batch.unlocking;
	if (request && request.tab.credentials === request) {
		if (request === credentialRequest) finishCredentials(null);
		else {
			request.tab.credentials = null;
			documentDialogs.closeFor(request.tab, el.credentialsPanel);
			request.resolve(null);
		}
	}
	for (const slot of batch.workers) slot.cancel?.();
	for (const [id, request] of batch.requests) request.slot.worker.postMessage({ type: "cancel", id });
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
	const compression = compressionOptions(batchElements.Compression.value);
	const archive = batchElements.Destination.value === "archive";
	const backend = state.renderBackend;
	const fontKey = [fontManager.permission, fontManager.records().map(font => [font.id, font.name, font.enabled, font.checksum])];
	for (const item of batch.items) {
		const format = batch.formats.find(format => format.value === (item.format || defaultFormat.value));
		const key = JSON.stringify([format.value, pageRange(item), batchUsesDPI(item) ? dpi : 0, archive, backend, fontKey, compression, format.value === "ofd" ? Number(batchElements.FontMode.value) : 0]);
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
	batch.progress = null;
	batch.jobs = jobs;
	batch.credentials = Promise.resolve();
	const progressTimer = window.setInterval(updateBatchProgress, 1000);
	renderBatchList();
	window.addEventListener("beforeunload", warnBatch);
	let destination = null;
	let zipFile = null;
	let storage = null;
	let stagingName = "";
	let packed = false;
	const results = new Array(jobs.length);
	try {
		if (archive) {
			zipFile = await saveFilePicker({ suggestedName: "转换结果.zip", types: [{ description: "ZIP", accept: { "application/zip": [".zip"] } }] });
			if (!nativeHost && navigator.storage?.getDirectory) {
				storage = await navigator.storage.getDirectory();
				stagingName = `ofdgo-convert-${crypto.randomUUID()}`;
				destination = await storage.getDirectoryHandle(stagingName, { create: true });
			}
		} else destination = await directoryPicker();
		if (batch.canceled) return;
		if (jobs.some(item => /\.pdf$/i.test(item.file.name) || !["ofd", "txt"].includes(item.format || defaultFormat.value)
			|| (item.format || defaultFormat.value) === "ofd" && batchElements.FontMode.value === "1")) {
			await requestLocalFontsBeforeOpen();
		}
		if (batch.canceled) return;
		batch.localFonts = fontManager.localEntries();
		batch.fontData = new Map();
		batchElements.Status.textContent = "正在准备";
		const first = await startBatchWorker();
		const fonts = await fontManager.files(fontManager.records());
		const names = new Set();
		const plans = jobs.map(item => {
			item.details = [];
			const format = batch.formats.find(format => format.value === (item.format || defaultFormat.value));
			const base = item.file.name.replace(/\.[^.]+$/, "").replace(/[<>:"/\\|?*\x00-\x1f]/g, "_").replace(/[. ]+$/g, "") || "document";
			let unique = base;
			const outputName = () => (format.paged ? unique : `${unique}.${format.extension}`).toLowerCase();
			for (let suffix = 2; names.has(outputName()); suffix++) unique = `${base} (${suffix})`;
			names.add(outputName());
			const options = { format: format.value, pages: pageRange(item), dpi, backend, archive, compression, base: unique, credentials: null,
				fontNames: [...batch.localFonts.keys()], fontMode: format.value === "ofd" ? Number(batchElements.FontMode.value) : 0 };
			return { item, format, options };
		});
		const limits = nativeHost ? await nativeHost.call("batch", {}) : engineLimits();
		const capacity = limits.parallelism;
		const automatic = nativeHost ? capacity : Math.min(capacity, Math.max(1, Math.floor(limits.memoryBytes / (512 << 20))));
		const parallel = Math.min(jobs.length, automatic);
		const workerMemory = (limits.memoryBytes || 0) / parallel;
		let next = 0;
		const run = async initial => {
			let slot = initial;
			while (!batch.canceled && next < plans.length) {
				const position = next++;
				const { item, format, options } = plans[position];
				batchStatus(item, "running", "准备");
				updateBatchProgress();
				try {
					if (!slot || slot.error) slot = await startBatchWorker();
					for (;;) {
						try {
							const result = await batchCall(slot, "ofdgoConvertFile", [item.file, options, fonts, destination], progress => batchProgress(item, position, jobs.length, progress));
							results[position] = result.files;
							item.details = (result.pdf?.Warnings || []).map(conversionWarningText);
							for (const font of result.fonts || []) item.details.push(`${font.name}：${font.changed ? options.fontMode === 1 ? "已嵌入" : "已外置" : `未处理 · ${fontBindingError(font.reason)}`}`);
							if (result.unchanged && !result.bytes) {
								batchStatus(item, "unchanged", "原样");
								item.details = ["无需转换，源文件未改动"];
							} else batchStatus(item, archive ? "ready" : "done", archive ? "待存" : "完成");
							break;
						} catch (err) {
							if (batch.canceled || !["credentialsRequired", "invalidCredentials", "pdfPassword"].includes(err.code)) throw err;
							const code = err.code === "pdfPassword" ? options.credentials ? "invalidCredentials" : "credentialsRequired" : err.code;
							options.credentials?.password?.fill(0);
							options.credentials?.key?.fill(0);
							options.credentials?.keyPassword?.fill(0);
							const unlock = batch.credentials.then(async () => {
								if (batch.canceled) throw new DOMException("已取消转换", "AbortError");
								const credentials = requestCredentials(code, state.openSeq, err.code === "pdfPassword");
								batch.unlocking = documentTab().credentials;
								try { return await credentials; }
								finally { batch.unlocking = null; }
							});
							batch.credentials = unlock.then(() => {}, () => {});
							options.credentials = await unlock;
							if (!options.credentials) throw new DOMException("已取消解锁", "AbortError");
							const preservesEncryption = err.code === "pdfPassword" && format.value === "pdf" && compression.mode !== 0 && !options.pages;
							if (!preservesEncryption && (!await requestConfirmation("转换结果将不保留原加密，是否继续？", null) || batch.canceled)) throw new DOMException("已取消转换", "AbortError");
						}
					}
				} catch (err) {
					batchStatus(item, err.name === "AbortError" ? "canceled" : "failed", err.name === "AbortError" ? "取消" : "失败");
					item.details = [err.message];
				} finally {
					item.progress = null;
					options.credentials?.password?.fill(0);
					options.credentials?.key?.fill(0);
					options.credentials?.keyPassword?.fill(0);
					if (!nativeHost && slot && (next >= plans.length || slot.memory >= workerMemory)) {
						slot.worker.terminate();
						batch.workers.delete(slot);
						slot = null;
					}
				}
				updateBatchProgress();
			}
		};
		const running = Array.from({ length: parallel }, (_, index) => (async () => {
			if (index) await new Promise(resolve => window.setTimeout(resolve, index * 200));
			await run(index === 0 ? first : null);
		})());
		const settled = await Promise.allSettled(running);
		const failure = settled.find(result => result.status === "rejected");
		if (failure) throw failure.reason;
		const entries = results.flat().filter(Boolean);
		if (archive && entries.length && !batch.canceled) {
			const slot = [...batch.workers].find(slot => !slot.error) || await startBatchWorker();
			const result = await batchCall(slot, "ofdgoPackFiles", [entries, zipFile, compression], progress => batchProgress(null, 0, 1, progress));
			if (result.blob) downloadBytes(result.blob, "application/zip", "转换结果.zip");
			packed = true;
			for (const item of jobs) if (item.status === "ready") batchStatus(item, "done", "完成");
		}
		const done = jobs.filter(item => item.status === "done").length;
		const failed = jobs.filter(item => item.status === "failed").length;
		const unchanged = batch.items.filter(item => item.status === "unchanged").length;
		batchElements.Status.textContent = `${batch.canceled ? "取消 · " : ""}完成 ${done}${failed ? ` · 失败 ${failed}` : ""}${unchanged ? ` · 原样 ${unchanged}` : ""}`;
	} catch (err) {
		batchElements.Status.textContent = err.name === "AbortError" ? "转换取消" : err.message;
	} finally {
		window.clearInterval(progressTimer);
		batch.progress = null;
		if (!batchElements.Progress.hasAttribute("value")) batchElements.Progress.value = 0;
		if (archive && !packed) for (const item of jobs) if (item.status === "ready") { item.status = "pending"; item.text = "待转"; }
		for (const slot of batch.workers) slot.worker.terminate();
		batch.workers.clear();
		batch.requests.clear();
		if (nativeHost) await nativeHost.release(results);
		batch.fontData?.clear();
		batch.localFonts?.clear();
		if (storage && stagingName) {
			try { await storage.removeEntry(stagingName, { recursive: true }); }
			catch { batchElements.Status.textContent += " · 临时文件清理失败"; }
		}
		batch.running = false;
		window.removeEventListener("beforeunload", warnBatch);
		renderBatchList();
	}
}

function compressionOptions(value) {
	const preset = Number(value) || 0;
	return preset > 2 ? { mode: 2, level: preset - 2 } : { mode: preset };
}

async function exportFile(whole, indices = null, value = el.exportFormat.value, saveAs = true) {
	const tab = documentTab();
	const state = tab.state;
	const callWASM = (name, ...args) => callTabWASM(tab, name, ...args);
	const active = () => isForegroundDocument(tab);
	if (!active()) return;
	if (!state.doc || state.saving || state.exporting || document.body.hasAttribute("data-busy")) {
		return;
	}
	const saving = value === "ofd";
	const compression = compressionOptions(el.outputCompression.value);
	const outputOptions = compression.mode ? [compression] : [];
	if (saving && !state.editorInfo && !state.ofdBytes) {
		return;
	}
	if (!saving && state.doc.encryption?.encrypted && !await requestConfirmation("此格式将输出明文，是否继续？")) return;
	const format = saving ? { value: "ofd", label: "OFD", extension: "ofd", mime: "application/ofd" } : exportFormatInfo(value);
	if (!format) {
		return;
	}
	const archive = whole && !saving && format.value !== "pdf" && format.value !== "txt";
	const label = archive ? "ZIP" : format.label;
	const extension = archive ? "zip" : format.extension;
	const mime = archive ? "application/zip" : format.mime;
	const fileName = saving && indices !== null ? `${baseFileName()}_选页.ofd` : whole ? `${baseFileName()}.${extension}` : pageFileName(extension);
	const wasmSeq = state.wasmSeq;
	const pageIndex = state.pageIndex;
	const dpi = exportFormatUsesDPI(format.value) ? currentImageDPI() : 0;
	let action = saving ? saveAs || !state.saveHandle || indices !== null ? "另存" : "保存" : "导出";
	const status = saving ? `正在${action}文档` : whole ? STATUS.exporting : STATUS.pageExporting;
	let retrySaveAs = false;
	let background = false;
	let savedRevision = null;
	let packageDirty = false;
	let encryptionDirty = false;
	let snapshotPrepared = false;
	let finishSave;
	const saveCompletion = saving && indices === null ? new Promise(resolve => { finishSave = resolve; }) : null;
	if (saveCompletion) tab.saveCompletion = saveCompletion;
	state.exporting = true;
	state.saving = saving && indices === null;
	updateControls();
	setBusy(true, "正在生成文件", null, status, tab);
	try {
		let file = saving && indices === null && !saveAs ? state.saveHandle : null;
		if (file && !nativeHost) {
			try {
				const permission = await file.queryPermission({mode: "readwrite"});
				if (permission !== "granted" && await file.requestPermission({mode: "readwrite"}) !== "granted") file = null;
			} catch (error) {
				if (!["NotAllowedError", "SecurityError"].includes(error.name)) throw error;
				file = null;
			}
			if (file && state.saveStamp) {
				const current = await file.getFile();
				if (current.size !== state.saveStamp.size || current.lastModified !== state.saveStamp.modified) throw new Error("文件已被修改，请另存");
			}
		}
		if (saving && !file) action = "另存";
		file ||= nativeHost || window.showSaveFilePicker ? await saveFilePicker({
			suggestedName: fileName,
			types: [{ description: label, accept: { [mime]: [`.${extension}`] } }],
		}) : null;
		if (tab.closed || wasmSeq !== state.wasmSeq) {
			return;
		}
		if (active() && (canvasEditor.input || canvasEditor.crop || canvasEditor.nudge || canvasEditor.nudgeCommit)) {
			setBusy(false, "", 0, "", tab);
			if (!await canvasEditor.commitNudge()) return;
			if (active() && !await canvasEditor.commitText()) return;
			if (active() && !await canvasEditor.commitCrop()) return;
			if (tab.closed || wasmSeq !== state.wasmSeq) return;
			setBusy(true, "正在生成文件", null, status, tab);
		}
		const readingSave = saving && !state.sourcePDF && !state.editorInfo && indices === null && compression.mode === 0;
		const destination = !nativeHost && file && file === state.saveHandle && state.saveStamp ? {fileHandle: file, saveStamp: state.saveStamp} : file;
		if (saving && indices === null && !readingSave) {
			snapshotPrepared = true;
			savedRevision = state.editorInfo?.revision;
			packageDirty = state.packageDirty;
			encryptionDirty = state.encryptionDirty;
			await callWASM("ofdgoPrepareSave");
			if (tab.closed || wasmSeq !== state.wasmSeq) return;
		}
		if (saving && indices === null) {
			background = true;
			state.exporting = false;
			setBusy(false, "", 0, "", tab);
			if (active()) updateControls();
		}
		const result = readingSave
			? { label: "OFD", size: state.ofdBytes.size ?? state.ofdBytes.byteLength, mime, blob: state.ofdBytes }
			: saving ? await callWASM(snapshotPrepared ? "ofdgoSaveSnapshot" : "ofdgoSaveDocument", indices, ...outputOptions, destination) : whole
			? await callWASM("ofdgoExportDocument", format.value, dpi, indices, state.renderBackend, ...outputOptions, file)
			: await callWASM("ofdgoExportPage", pageIndex, format.value, dpi, state.renderBackend, ...outputOptions, file);
		if (tab.closed || wasmSeq !== state.wasmSeq) {
			return;
		}
		if (readingSave && file) {
			const writer = await file.createWritable();
			try {
				await writer.write(result.blob);
				if (destination?.saveStamp) {
					const current = await file.getFile();
					if (current.size !== destination.saveStamp.size || current.lastModified !== destination.saveStamp.modified) throw new Error("文件已被修改，请另存");
				}
				await writer.close();
			} catch (error) {
				await writer.abort().catch(() => {});
				throw error;
			}
			delete result.blob;
		}
		if (result.blob) {
			downloadBytes(result.blob, result.mime, fileName);
		}
		if (saving && indices === null && state.editorInfo && file && savedRevision != null) {
			state.savedRevision = snapshotPrepared ? savedRevision : state.editorInfo.revision;
			if (!snapshotPrepared || state.editorInfo.revision === savedRevision) {
				if (!snapshotPrepared || state.packageDirty === packageDirty) state.packageDirty = false;
				if (!snapshotPrepared || state.encryptionDirty === encryptionDirty) state.encryptionDirty = false;
			}
			state.dirty = state.packageDirty || state.encryptionDirty || state.editorInfo.revision !== state.savedRevision;
			if (active()) updatePendingChanges();
			updateWorkspaceDirty();
		}
		if (saving && indices === null && file) {
			state.saveHandle = file;
			state.fileName = file.name;
			state.saveStamp = null;
			if (!nativeHost) {
				try {
					const current = await file.getFile();
					state.saveStamp = {size: current.size, modified: current.lastModified};
				} catch { state.saveHandle = null; }
			}
			if (active()) {
				document.title = `${nativeHost ? "OFDGo" : "OFDGo WebUI"} - ${state.fileName}`;
				el.metaFile.textContent = state.fileName;
			}
		}
		tab.status = `${result.label}已${action}（${formatBytes(result.size)}）`;
		if (active()) setStatus(tab.status);
		return true;
	} catch (err) {
		const permissionDenied = saving && !saveAs && !nativeHost && !!state.saveHandle && ["NotAllowedError", "SecurityError"].includes(err.name);
		retrySaveAs = active() && permissionDenied;
		if (!tab.closed && wasmSeq === state.wasmSeq) {
			const canceled = `${action}已取消`;
			tab.status = permissionDenied ? "保存权限不足，请切回文档后另存" : err.name === "AbortError" ? canceled : err.message;
			if (active() && !retrySaveAs) {
				if (el.batchPagesPanel.open) el.batchPagesStatus.textContent = tab.status;
				if (err.name === "AbortError") setStatus(tab.status);
				else showError(err, false);
			}
		}
	} finally {
		try {
			if (snapshotPrepared) await callWASM("ofdgoDiscardSave").catch(() => {});
			state.saving = false;
			state.saveRequestID = 0;
			if (!background) {
				state.exporting = false;
				state.exportRequestID = 0;
				if (wasmSeq === state.wasmSeq) setBusy(false, "", 0, "", tab);
			}
			if (active()) {
				el.cancelExportButton.hidden = !state.exportRequestID;
				if (!document.body.hasAttribute("data-busy")) el.progressPanel.hidden = true;
				if (wasmSeq === state.wasmSeq) updateControls();
			} else if (workspace.active === tab) tab.controlsPending = true;
			updateWorkspaceDirty();
			renderDocumentTabs();
		} finally {
			finishSave?.();
			if (tab.saveCompletion === saveCompletion) tab.saveCompletion = null;
		}
	}
	if (retrySaveAs && active() && wasmSeq === state.wasmSeq) return exportFile(whole, indices, value, true);
}

function cancelExport() {
	if (remoteDownload) {
		remoteDownload.abort();
		return;
	}
	const tab = documentTab();
	const target = tab.state;
	const requestID = operationRequest(tab)?.id || target.exportRequestID || target.saveRequestID;
	if (!requestID || el.cancelExportButton.disabled) {
		return;
	}
	el.cancelExportButton.disabled = true;
	if (state.imageImporting) {
		el.createCancel.disabled = true;
		el.createStatus.textContent = "正在取消操作";
	} else if (state.importing) {
		el.importCancel.disabled = true;
		el.importStatus.textContent = "正在取消操作";
	}
	setProgress("正在取消操作", null);
	cancelOperation(requestID);
}

function releaseMediaPlayers(surface) {
	for (const [id, entry] of state.mediaPlayers || []) {
		if (surface && (entry.surface !== surface || entry.floating)) continue;
		releaseMediaPlayer(id, entry);
	}
}

function releaseMediaPlayer(id, entry) {
	if (state.mediaPlayers?.get(id) !== entry) return;
	state.mediaPlayers.delete(id);
	for (const [requestID, request] of wasmRequests) {
		if (request.tab === documentTab() && request.mediaID === id) cancelOperation(requestID);
	}
	entry.finish?.();
	closeMediaWindow(entry);
	if (entry.element) {
		entry.element.pause();
		entry.element.removeAttribute("src");
		entry.element.load();
		entry.element.remove();
	}
	if (entry.url) URL.revokeObjectURL(entry.url);
}

function closeMediaWindow(entry) {
	const dialog = entry.dialog;
	if (!dialog) return;
	const restoreFocus = dialog.contains(document.activeElement);
	entry.dialog = null;
	dialog.close();
	dialog.remove();
	if (restoreFocus && entry.opener?.isConnected) entry.opener.focus({ preventScroll: true });
	entry.opener = null;
}

function placeMediaPlayer(id, entry) {
	const box = entry.placement;
	if (!box) return;
	const element = entry.element;
	entry.finish?.();
	if (box.floating) {
		element.removeAttribute("style");
		if (!entry.dialog) {
			const dialog = document.createElement("dialog");
			dialog.className = "form-dialog media-dialog";
			const label = entry.kind === "Video" ? "视频" : "音频";
			const header = document.createElement("div");
			header.className = "form-title";
			const title = document.createElement("span");
			title.textContent = `${label}播放`;
			const close = document.createElement("button");
			close.type = "button";
			close.className = "small-button";
			close.textContent = "关闭";
			close.title = `关闭${label}`;
			close.addEventListener("click", () => releaseMediaPlayer(id, entry));
			dialog.addEventListener("keydown", event => {
				if (event.key !== "Escape") return;
				event.preventDefault();
				event.stopPropagation();
				releaseMediaPlayer(id, entry);
			});
			dialog.addEventListener("close", () => {
				if (entry.dialog === dialog) releaseMediaPlayer(id, entry);
			});
			header.append(title, close);
			dialog.append(header, element);
			entry.opener = document.activeElement;
			entry.dialog = dialog;
			document.body.append(dialog);
			dialog.show();
		}
	} else {
		entry.surface.append(element);
		closeMediaWindow(entry);
		element.style.left = `${box.x / box.pageWidth * 100}%`;
		element.style.width = `${box.width / box.pageWidth * 100}%`;
		if (entry.kind === "Video") {
			element.style.width = `calc(${box.width / box.pageWidth * 100}% * var(--surface-scale, 1))`;
			element.style.top = `${box.y / box.pageHeight * 100}%`;
			element.style.height = `calc(${box.height / box.pageHeight * 100}% * var(--surface-scale, 1))`;
		} else {
			element.style.bottom = `${Math.max(0, 100 - (box.y + box.height) / box.pageHeight * 100)}%`;
		}
	}
	entry.placement = null;
	element.currentTime = 0;
}

async function playMediaAction(link, page, surface) {
	const action = link.sound || link.movie;
	const id = action?.resourceID;
	if (!id || !surface || !page) return;
	const operator = link.sound ? "Play" : action.operator || "Play";
	if (!["Play", "Stop", "Pause", "Resume"].includes(operator)) return;
	const players = state.mediaPlayers ||= new Map();
	let entry = players.get(id);
	if (operator === "Stop" || operator === "Pause") {
		if (entry) entry.operation = (entry.operation || 0) + 1;
		if (operator === "Stop") {
			if (entry) releaseMediaPlayer(id, entry);
		} else if (entry) {
			entry.paused = true;
			entry.element?.pause();
		}
		return;
	}
	if (operator === "Resume" && (!entry || entry.element?.ended || !entry.paused && !entry.element?.paused)) return;
	const seq = state.openSeq;
	try {
		if (!entry) {
			entry = { surface };
			players.set(id, entry);
			entry.pending = callWASM("ofdgoExportMedia", id, null).then(data => {
				if (seq !== state.openSeq || players.get(id) !== entry) return;
				if (!entry.floating && !entry.surface.isConnected) {
					releaseMediaPlayer(id, entry);
					return;
				}
				const element = document.createElement(data.kind === "Video" ? "video" : "audio");
				entry.kind = data.kind;
				entry.element = element;
				entry.url = URL.createObjectURL(data.blob);
				element.src = entry.url;
				element.controls = true;
				element.className = "page-media";
				element.addEventListener("error", () => {
					entry.finish?.();
					if (seq === state.openSeq && players.get(id) === entry) setStatus("媒体无法播放");
				});
				placeMediaPlayer(id, entry);
			});
		}
		if (operator === "Play") {
			entry.surface = surface;
			const box = link.movie && link.playbackBox ? link.playbackBox : link;
			entry.floating = !!link.movie && !link.playbackBox || !!link.sound && !(link.width > 0 && link.height > 0);
			entry.placement = { x: box.x, y: box.y, width: box.width, height: box.height, pageWidth: page.width, pageHeight: page.height, floating: entry.floating };
		}
		entry.paused = false;
		const operation = entry.operation = (entry.operation || 0) + 1;
		await entry.pending;
		if (seq !== state.openSeq || players.get(id) !== entry || operation !== entry.operation || !entry.element?.isConnected) return false;
		const element = entry.element;
		if (link.sound) {
			element.volume = Math.max(0, Math.min(100, action.volume ?? 100)) / 100;
			element.loop = !!action.repeat;
		}
		placeMediaPlayer(id, entry);
		let ended;
		if (link.sound && action.synchronous && !action.repeat) {
			ended = new Promise(resolve => {
				entry.finish = event => {
					element.removeEventListener("ended", entry.finish);
					entry.finish = null;
					resolve(event?.type === "ended");
				};
				element.addEventListener("ended", entry.finish);
			});
		}
		try { await element.play(); } catch (err) { entry.finish?.(); throw err; }
		if (ended) return await ended;
	} catch (err) {
		if (players.get(id) === entry && seq === state.openSeq) {
			if (!entry?.element) players.delete(id);
			setStatus(err.message || "媒体无法播放");
		}
		return false;
	}
}

function pageLinkTarget(link, page, surface, openAction) {
	if (link.sound || link.movie) {
		return { href: "#", title: link.sound ? "音频播放" : "视频播放", activate: () => playMediaAction(link, page, surface) };
	}
	if (link.dest) {
		const target = pageIndexByID(state.doc.pages, link.dest.pageID);
		return { href: target < 0 ? "#" : `#page-${target + 1}`, title: target < 0 ? "文档链接" : `第${target + 1}页`, activate: () => navigateDestination(link.dest, openAction) };
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
		const target = pageLinkTarget(link, page, surface);
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
				if (document.body.hasAttribute("data-busy")) return;
				state.openActionTask = null;
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
					if (await target.activate() === false) break;
				}
			});
		}
		anchor.style.left = `${link.x / page.width * 100}%`;
		anchor.style.top = `${link.y / page.height * 100}%`;
		anchor.style.width = `${link.width / page.width * 100}%`;
		anchor.style.height = `${link.height / page.height * 100}%`;
		if (link.path) {
			anchor.classList.add("page-region-link");
			const region = document.createElementNS(SVG_NAMESPACE, "svg");
			region.setAttribute("viewBox", `${link.x} ${link.y} ${link.width} ${link.height}`);
			const path = document.createElementNS(SVG_NAMESPACE, "path");
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

async function navigateDestination(dest, openAction) {
	if (document.body.hasAttribute("data-busy")) return;
	if (openAction && state.openActionTask !== openAction) return false;
	const index = pageIndexByID(state.doc.pages, dest.pageID);
	if (index < 0) {
		setStatus("目标页面不存在");
		return;
	}
	if (!["XYZ", "Fit", "FitH", "FitV", "FitR"].includes(dest.type)) {
		setStatus("暂不支持此跳转方式");
		return;
	}
	const seq = state.openSeq, scale = state.scale;
	const navigation = state.pageNavigation = { openSeq: seq, index };
	try {
		await pageView.renderPage(index, { fit: false, scroll: false, openAction, navigation });
		if (openAction && state.openActionTask !== openAction) return false;
		if (seq !== state.openSeq || state.pageIndex !== index || state.pageNavigation !== navigation) return;
		const page = state.doc.pages[index], size = pageView.pageViewSize(page), space = pageView.pageSpace();
		const width = el.viewerPanel.clientWidth - space * 2, height = el.viewerPanel.clientHeight - space * 2;
		let left = dest.omitLeft ? 0 : (dest.left ?? 0);
		let top = dest.omitTop ? 0 : (dest.top ?? 0);
		if (dest.type === "Fit") {
			pageView.fitHeight(false);
			pageView.scrollToPage(index);
			return;
		} else if (dest.type === "FitH") {
			pageView.setScale(pageView.fitWidthScale(page), false);
			left = 0;
		} else if (dest.type === "FitV") {
			pageView.setScale(height / (size.height * MM_TO_PX), false);
			top = 0;
		} else if (dest.type === "FitR") {
			const a = destinationPoint(page, left, top), b = destinationPoint(page, dest.right, dest.bottom);
			if (dest.right <= left || dest.bottom <= top) return;
			pageView.setScale(Math.min(width / Math.abs(b.x - a.x), height / Math.abs(b.y - a.y)) / MM_TO_PX, false);
		} else {
			pageView.setScale(!dest.omitZoom && dest.zoom > 0 ? dest.zoom : scale, false);
		}
		const point = destinationPoint(page, left, top);
		if (dest.type === "FitR") {
			const bottom = destinationPoint(page, dest.right, dest.bottom);
			point.x = Math.min(point.x, bottom.x);
			point.y = Math.min(point.y, bottom.y);
		}
		if (state.pageWindow) pageView.syncPageWindow(state.pageWindow, state.pageWindow.offsets[index]);
		const node = pageView.pageShell(index), shell = node.getBoundingClientRect(), viewer = el.viewerPanel.getBoundingClientRect();
		el.viewerPanel.scrollLeft += shell.left + (node.clientLeft || 0) - viewer.left - (el.viewerPanel.clientLeft || 0) + point.x * MM_TO_PX * state.scale;
		pageView.scrollViewerBy(shell.top + (node.clientTop || 0) - viewer.top - (el.viewerPanel.clientTop || 0) + point.y * MM_TO_PX * state.scale);
	} finally {
		if (state.pageNavigation === navigation) {
			if (seq !== state.openSeq || index !== state.pageIndex) state.pageNavigation = null;
			else Object.assign(navigation, { top: state.pageWindow ? pageView.pageWindowTop(state.pageWindow) : el.viewerPanel.scrollTop, left: el.viewerPanel.scrollLeft });
		}
	}
}

function syncSelection() {
	const selection = document.getSelection();
	if (state.documentSelection && (!selection.rangeCount || !isDocumentSelection(selection.getRangeAt(0)))) {
		state.documentSelection = null;
		if (state.doc && !document.body.hasAttribute("data-busy")) {
			setStatus(pageStatus(state.pageIndex, state.doc.pageCount));
		}
	}
	for (const index of state.selectedPages) {
		pageView.unmountPage(index);
	}
	pageView.trimPageCache();
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
			setStatus(`正在全选文字 · ${index + 1} / ${count} 页`);
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
	const tab = documentTab(), state = tab.state;
	if (!state.editing || !canvasEditor.enabled || !canvasEditor.selected || !editorShortcutTarget(event.target)
		|| !el.viewerPanel.contains(event.target) && !el.editorTools.contains(event.target)
		|| canvasEditor.input || canvasEditor.crop || canvasEditor.drag || document.body.hasAttribute("data-busy") || formDialogOpen()) return false;
	const items = canvasEditor.items().slice().sort((a, b) => a.order - b.order);
	const cut = event.type === "cut";
	if (!canEditObject(canvasEditor.selected, "copy") || cut && !canEditObject(canvasEditor.selected, "delete")) {
		event.preventDefault();
		setStatus(cut ? "对象不可剪切" : "对象不可复制");
		return true;
	}
	const index = items[0].index, ids = items.map(item => item.id);
	const clipboard = { tab, token: crypto.randomUUID(), source: `${tab.id}:${documentContentKey(state)}`, page: state.doc.pages[index].id, bounds: selectionBounds(items), x: 0, y: 0, cut };
	if (canvasEditor.nudge) {
		clipboard.bounds.x += canvasEditor.nudge.x;
		clipboard.bounds.y += canvasEditor.nudge.y;
	}
	event.clipboardData.setData(OBJECT_CLIPBOARD_TYPE, clipboard.token);
	event.clipboardData.setData("text/plain", items.map(item => item.text || "").filter(Boolean).join("\n"));
	event.preventDefault();
	state.objectClipboardToken = clipboard.token;
	const previous = workspace.objectClipboard;
	workspace.objectClipboard = clipboard;
	if (previous?.tab) previous.ready.then(() => {
		if (previous.tab && !previous.tab.closed) return callTabWASM(previous.tab, "ofdgoDiscardObjects", previous.token);
	}).catch(() => {});
	const capture = () => {
		if (tab.closed || !state.editing || workspace.objectClipboard !== clipboard || state.objectClipboardToken !== clipboard.token
			|| clipboard.source !== `${tab.id}:${documentContentKey(state)}`
			|| state.doc.pages[index]?.id !== clipboard.page) return false;
		const openSeq = state.openSeq, revision = state.editorInfo.revision;
		return callTabWASM(tab, "ofdgoCaptureObjects", index, ids, clipboard.token).then(async () => {
			clipboard.cut = false;
			if (cut && isForegroundDocument(tab) && openSeq === state.openSeq && revision === state.editorInfo?.revision && index === state.pageIndex
				&& state.objectClipboardToken === clipboard.token && workspace.objectClipboard === clipboard
				&& state.editing && !formDialogOpen() && !canvasEditor.input && !canvasEditor.crop && !canvasEditor.drag && !canvasEditor.nudge && !canvasEditor.nudgeCommit) {
				clipboard.cut = Boolean(await changeDocument("ofdgoDeleteObjects", { index, id: ids, items }));
			}
			return true;
		});
	};
	clipboard.ready = Promise.resolve(canvasEditor.nudge ? canvasEditor.commitNudge().then(saved => saved ? capture() : false) : capture()).catch(err => {
		if (isForegroundDocument(tab) && state.objectClipboardToken === clipboard.token) showError(err, false);
		return false;
	});
	return true;
}

async function prepareObjectClipboard(clipboard, owner = documentTab()) {
	if (!await clipboard.ready) return false;
	if (clipboard.data) return true;
	if (!clipboard.portable) {
		const tab = clipboard.tab;
		clipboard.portable = callTabWASM(tab, "ofdgoExportObjects", clipboard.token).then(result => {
			clipboard.data = result.bytes;
			return true;
		}, err => {
			clipboard.portable = null;
			if (workspace.objectClipboard === clipboard && isForegroundDocument(owner)) showError(err, false);
			return false;
		});
	}
	return clipboard.portable;
}

function visiblePageBounds(index) {
	const page = state.doc.pages[index], surface = pageView.pageShell(index)?.querySelector(".page-surface");
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
	const tab = documentTab(), state = tab.state;
	if (!state.editing || !editorShortcutTarget(event.target) || formDialogOpen()) return;
	event.preventDefault();
	const token = event.clipboardData.getData(OBJECT_CLIPBOARD_TYPE);
	const files = Array.from(event.clipboardData.files);
	const value = event.clipboardData.getData("text/plain").replace(/\t/g, "    ");
	if ((canvasEditor.nudge || canvasEditor.nudgeCommit) && !await canvasEditor.commitNudge()) return;
	if (!isForegroundDocument(tab) || document.body.hasAttribute("data-busy") || canvasEditor.input || canvasEditor.crop || canvasEditor.drag) return;
	const index = state.pageIndex, page = state.doc.pages[index];
	if (token) {
		const clipboard = workspace.objectClipboard;
		if (!clipboard || token !== clipboard.token) {
			setStatus("复制内容已失效，请重新复制");
			return;
		}
		const openSeq = state.openSeq, identity = documentContentKey(state);
		const local = state.objectClipboardToken === clipboard.token;
		if (!await (local ? clipboard.ready : prepareObjectClipboard(clipboard, tab)) || !isForegroundDocument(tab) || openSeq !== state.openSeq || index !== state.pageIndex || workspace.objectClipboard !== clipboard) return;
		if (identity !== documentContentKey(state) || local && state.objectClipboardToken !== clipboard.token) return;
		if (!state.editing || formDialogOpen() || document.body.hasAttribute("data-busy") || canvasEditor.input || canvasEditor.crop || canvasEditor.drag || canvasEditor.nudge || canvasEditor.nudgeCommit) return;
		if (!pageCan("insert")) return;
		const visible = visiblePageBounds(index), bounds = clipboard.bounds;
		const target = `${tab.id}:${documentContentKey(state)}`;
		const offset = (clipboard.target || clipboard.source) === target && (clipboard.targetPage || clipboard.page) === page.id ? { x: clipboard.x + (clipboard.cut ? 0 : 3), y: clipboard.y + (clipboard.cut ? 0 : 3) } : { x: 0, y: 0 };
		for (const [axis, size] of [["x", "width"], ["y", "height"]]) {
			const position = bounds[axis] + offset[axis];
			if (position < visible[axis] || position + bounds[size] > visible[axis] + visible[size]) {
				const margin = Math.min(20, visible[size] / 10);
				offset[axis] = Math.max(visible[axis] + margin, Math.min(position, visible[axis] + visible[size] - margin - bounds[size])) - bounds[axis];
			}
		}
		if (await changeDocument(local ? "ofdgoPasteObjects" : "ofdgoImportObjects", null, index, local ? token : clipboard.data, offset.x, offset.y)) {
			clipboard.target = target;
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
	if (sealPreview) return;
	if (!editorShortcutTarget(event.target)) {
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

async function focusSearch(direction = 0) {
	if (canvasEditor.input || canvasEditor.crop || canvasEditor.nudge || canvasEditor.nudgeCommit) {
		const tab = documentTab(), content = documentContentKey(state);
		const current = () => isForegroundDocument(tab) && content === documentContentKey(state);
		if (!await canvasEditor.commitNudge() || !current() || !await canvasEditor.commitText() || !current()
			|| !await canvasEditor.commitCrop() || !current() || document.body.hasAttribute("data-busy") || formDialogOpen()) return;
	}
	if (direction && state.searchMatches.length) return selectSearchMatch((direction < 0 ? Math.max(0, state.searchIndex) : state.searchIndex) + direction);
	if (!state.showPages) {
		toggleSidebar("pages");
	}
	showNavigation(el.searchTab);
	el.searchInput.focus({ preventScroll: true });
	el.searchInput.select();
}

function showNavigation(selected) {
	if (selected !== el.annotationsTab) cancelAnnotationSummary(documentTab());
	const scrollTop = el.navigationContent.scrollTop;
	for (const [tab, panel] of [[el.pagesTab, el.pageList], [el.outlinesTab, el.outlineList], [el.annotationsTab, el.annotationsPanel], [el.searchTab, el.searchPanel]]) {
		if (tab.getAttribute("data-selected") === "true") {
			state.navigationScroll.set(tab, scrollTop);
		}
		const active = tab === selected;
		panel.hidden = !active;
		tab.setAttribute("data-selected", String(active));
		tab.tabIndex = active ? 0 : -1;
	}
	el.pageSelectionTools.hidden = !state.editing || !state.pageMultiSelect || selected !== el.pagesTab;
	el.navigationContent.scrollTop = state.navigationScroll.get(selected) || 0;
	if (selected === el.pagesTab) pageView.syncThumbnailWindow();
	if (selected === el.annotationsTab) loadAnnotationSummary();
}

function cancelAnnotationSummary(tab) {
	for (const [id, request] of wasmRequests) {
		if (request.tab === tab && request.name === "ofdgoAnnotationInfos" && !request.canceled) {
			tab.state.annotationSummary = null;
			cancelOperation(id);
		}
	}
}

async function loadAnnotationSummary() {
	const tab = documentTab(), state = tab.state;
	if (!state.doc || !state.ready) return;
	const seq = state.openSeq;
	if (state.annotationSummary?.seq === seq) { renderAnnotationSummary(true); return; }
	cancelAnnotationSummary(tab);
	const summary = {seq, items: [], loading: true};
	state.annotationSummary = summary;
	el.annotationsList.replaceChildren();
	el.annotationsStatus.textContent = "正在读取注解";
	try {
		const items = await callTabWASM(tab, "ofdgoAnnotationInfos");
		if (tab.closed || seq !== state.openSeq || state.annotationSummary !== summary) return;
		summary.items = items || [];
		summary.loading = false;
		if (isForegroundDocument(tab)) renderAnnotationSummary(true);
	} catch (error) {
		if (seq === state.openSeq && state.annotationSummary === summary) {
			state.annotationSummary = null;
			if (isForegroundDocument(tab)) el.annotationsStatus.textContent = error.name === "AbortError" ? "" : error.message;
		}
	}
}

function annotationKind(annotation) {
	return annotation.type === "Path" && annotation.subtype === "Link" ? "Link" : annotation.type;
}

function renderAnnotationSummary(reset = false) {
	const summary = state.annotationSummary;
	if (!summary || summary.loading || summary.seq !== state.openSeq) return;
	const labels = {Path: "批注", Highlight: "高亮", Stamp: "印章", Watermark: "水印", Link: "链接"};
	if (reset) {
		const previousKind = el.annotationsKind.value;
		el.annotationsKind.replaceChildren(new Option("全部", ""), ...[...new Set(summary.items.map(annotationKind))].map(type => new Option(labels[type] || type, type)));
		el.annotationsKind.value = [...el.annotationsKind.options].some(option => option.value === previousKind) ? previousKind : "";
	}
	const query = el.annotationsFilter.value.trim().toLocaleLowerCase();
	const kind = el.annotationsKind.value;
	const items = summary.items.filter(item => (!kind || annotationKind(item) === kind) && `${item.remark || ""}\n${item.creator || ""}\n${item.id}`.toLocaleLowerCase().includes(query));
	el.annotationsStatus.textContent = summary.items.length ? `${items.length} / ${summary.items.length}` : "";
	el.annotationsList.replaceChildren(...items.map(item => {
		const row = document.createElement("div");
		row.className = "annotation-summary";
		const target = document.createElement("button");
		target.type = "button";
		target.className = "annotation-summary-target";
		const heading = document.createElement("span"), content = document.createElement("span");
		heading.textContent = `第${item.page}页 · ${labels[annotationKind(item)] || item.type}${item.visible ? "" : " · 隐藏"}`;
		content.textContent = item.remark || item.id;
		target.append(heading, content);
		target.addEventListener("click", () => focusPageRegion(item, "注解", "center"));
		row.append(target);
		appendInfoLine(row, "作者", item.creator);
		appendInfoLine(row, "时间", formatDocumentTime(item.lastModDate));
		if (state.editing) {
			const edit = document.createElement("button");
			edit.type = "button";
			edit.className = "small-button";
			edit.textContent = "修改";
			edit.addEventListener("click", () => {
				if (!state.editing || document.body.hasAttribute("data-busy") || summary.seq !== state.openSeq) return;
				editAnnotationDetails({...item, id: `annotation:${item.id}`, index: item.page - 1, type: "Annotation", fromSummary: true});
			});
			row.append(edit);
		}
		return row;
	}));
}

function renderOutlines(reset = true) {
	const selected = !reset && [el.pagesTab, el.outlinesTab, el.annotationsTab, el.searchTab].find(tab => tab.getAttribute("data-selected") === "true");
	const scrollTop = reset ? 0 : el.navigationContent.scrollTop;
	if (reset) {
		state.navigationScroll.clear();
		state.outlineExpanded.clear();
		state.outlineSelection = null;
	}
	const outlines = state.doc?.outlines || [];
	el.pageListTitle.hidden = Boolean(state.doc);
	el.navigationTabs.hidden = !state.doc;
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

const SEARCH_MATCH_LIMIT = 10000;

function resetSearch(clearInput = true) {
	for (const [id, request] of wasmRequests) if (request.tab === documentTab() && request.name === "ofdgoSearchPage") cancelOperation(id);
	state.searchTruncated = false;
	state.searchRunning = false;
	state.searchNext = 0;
	state.searchPaused = false;
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

async function refreshSearchPages(pages, openSeq) {
	const tab = documentTab(), target = tab.state, query = target.searchQuery;
	if (!query || target.searchRunning || target.searchTruncated) { resetSearch(false); return; }
	pages = pages.filter(page => page < target.searchNext);
	if (!pages.length) return;
	const seq = ++target.searchSeq, next = target.searchNext;
	target.searchRunning = true;
	const previous = target.searchMatches, selected = previous[target.searchIndex];
	target.searchNext = Math.min(...pages);
	target.searchMatches = previous.filter(match => match.page < target.searchNext);
	target.searchIndex = target.searchMatches.indexOf(selected);
	el.searchResults.replaceChildren();
	clearSearchHighlights();
	renderSearchResults();
	try {
		const matches = previous.filter(match => !pages.includes(match.page));
		for (const page of pages) {
			const result = await callTabWASM(tab, "ofdgoSearchPage", page, query, SEARCH_MATCH_LIMIT - matches.length + 1);
			if (tab.closed || target.openSeq !== openSeq || target.searchSeq !== seq) return;
			for (const match of result || []) {
				if (matches.length === SEARCH_MATCH_LIMIT) { target.searchTruncated = true; break; }
				matches.push({...match, page});
			}
			if (target.searchTruncated) break;
		}
		matches.sort((a,b) => a.page - b.page);
		target.searchMatches = matches;
		target.searchNext = next;
		target.searchPaused = false;
		target.searchIndex = selected ? matches.indexOf(selected) : -1;
		if (isForegroundDocument(tab)) {
			el.searchResults.replaceChildren();
			clearSearchHighlights();
			el.searchStatus.textContent = target.searchTruncated ? "已达结果上限" : matches.length ? "搜索完成" : "暂无结果";
			renderSearchResults();
			if (target.searchIndex >= 0) renderSearchHighlights(matches[target.searchIndex].page);
		}
	} catch (err) {
		if (target.openSeq === openSeq && target.searchSeq === seq) {
			if (isForegroundDocument(tab)) { el.searchStatus.textContent = "搜索失败"; showError(err, false); }
		}
	} finally {
		if (target.searchSeq === seq) target.searchRunning = false;
	}
}

async function searchDocument() {
	if (!state.doc || document.body.hasAttribute("data-busy")) {
		return;
	}
	const query = el.searchInput.value.trim();
	if (query && query === state.searchQuery && (state.searchRunning || state.searchTruncated || state.searchNext >= state.doc.pageCount)) {
		await selectSearchMatch(state.searchIndex + 1);
		return;
	}
	if (query !== state.searchQuery) resetSearch(false);
	if (!query) {
		return;
	}
	state.searchQuery = query;
	const seq = state.searchSeq;
	const openSeq = state.openSeq;
	const pageCount = state.doc.pageCount;
	const target = state;
	target.searchRunning = true;
	target.searchPaused = false;
	try {
		for (let page = target.searchNext; page < pageCount; page += 1) {
			const matches = await callWASM("ofdgoSearchPage", page, query, SEARCH_MATCH_LIMIT - target.searchMatches.length + 1);
			if (target !== state || seq !== target.searchSeq || openSeq !== target.openSeq) {
				return;
			}
			el.searchStatus.textContent = `正在搜索文字 · ${page + 1} / ${pageCount} 页`;
			target.searchNext = page + 1;
			if (!matches?.length) {
				continue;
			}
			for (const match of matches) {
				if (target.searchMatches.length === SEARCH_MATCH_LIMIT) { target.searchTruncated = true; break; }
				state.searchMatches.push({ ...match, page });
			}
			renderSearchResults();
			if (target.searchTruncated) break;
		}
		el.searchStatus.textContent = target.searchTruncated ? "已达结果上限" : state.searchMatches.length ? "搜索完成" : "暂无结果";
		if (state.searchIndex < 0 && state.searchMatches.length && !el.searchPanel.hidden && state.showPages) {
			await selectSearchMatch(0);
		}
	} catch (err) {
		if (target === state && seq === target.searchSeq && openSeq === target.openSeq) {
			el.searchStatus.textContent = "搜索失败";
			showError(err, false);
		}
	} finally {
		if (target.searchSeq === seq) target.searchRunning = false;
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
		page.textContent = `第${match.page + 1}页`;
		const text = document.createElement("span");
		const mark = document.createElement("mark");
		mark.textContent = match.text;
		text.append(match.before, mark, match.after);
		button.append(page, text);
		button.addEventListener("click", () => selectSearchMatch(i));
		el.searchResults.append(button);
	}
	for (const button of el.searchResults.children) {
		button.setAttribute("data-current", String(Number(button.dataset.matchIndex) === state.searchIndex));
	}
	updateSearchCount();
}

async function selectSearchMatch(index) {
	const count = state.searchMatches.length;
	if (!count || document.body.hasAttribute("data-busy")) {
		return;
	}
	index = (index + count) % count;
	state.searchIndex = index;
	const match = state.searchMatches[index];
	const target = state;
	const seq = state.searchSeq;
	const openSeq = state.openSeq;
	clearSearchHighlights();
	renderSearchResults();
	if (!await pageView.renderPage(match.page, { fit: false, scroll: false })) {
		return;
	}
	await nextFrame();
	if (target !== state || seq !== state.searchSeq || openSeq !== state.openSeq || state.searchIndex !== index || state.pageIndex !== match.page) {
		return;
	}
	renderSearchHighlights(match.page);
	const mark = pageView.pageShell(match.page).querySelector(".search-highlight");
	if (mark) {
		const box = mark.getBoundingClientRect();
		const viewer = el.viewerPanel.getBoundingClientRect();
		pageView.scrollViewerBy(box.top + box.height / 2 - viewer.top - el.viewerPanel.clientHeight / 2);
		if (box.left < viewer.left) {
			el.viewerPanel.scrollLeft += box.left - viewer.left;
		} else if (box.right > viewer.left + el.viewerPanel.clientWidth) {
			el.viewerPanel.scrollLeft += box.right - viewer.left - el.viewerPanel.clientWidth;
		}
	} else {
		pageView.scrollToPage(match.page);
	}
	if (state.editing) {
		const node = [...pageView.pageShell(match.page).querySelectorAll(".edit-object")].find(node => canvasEditor.nodes.get(node)?.id === match.id);
		const item = node && canvasEditor.nodes.get(node);
		if (item?.type === "TextObject" && !match.parts?.length) {
			state.selectObjects = true;
			pageView.setPan(false);
			canvasEditor.setTool("");
			canvasEditor.select(item);
			updateControls();
		} else canvasEditor.select(null);
	}
	const button = el.searchResults.querySelector("[data-current=true]");
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
	const surface = pageView.pageShell(index).querySelector(".page-surface");
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

function scheduleOpenActions() {
	if (!state.openActionEnabled || state.openActionFrame) return;
	state.openActionFrame = requestAnimationFrame(() => {
		state.openActionFrame = 0;
		activateOpenActions();
	});
}

async function activateOpenActions() {
	if (!state.openActionEnabled || !state.doc || state.editing || state.exporting || document.body.hasAttribute("data-busy")) return;
	const pageID = state.doc.pages[state.pageIndex]?.id;
	if (!pageID || !state.openActionDocument && state.openActionPage === pageID) return;
	const task = { sequence: state.openSeq, seen: new Set(), navigating: false };
	state.openActionTask = task;
	const current = () => task === state.openActionTask && task.sequence === state.openSeq && !state.editing;
	let documentOpened = state.openActionDocument;
	state.openActionDocument = false;
	state.openActionPage = pageID;
	try {
		while (current()) {
			const index = state.pageIndex;
			const id = state.doc.pages[index]?.id;
			if (!id || !documentOpened && task.seen.has(id)) break;
			if (!documentOpened) {
				task.seen.add(id);
				state.openActionPage = id;
			}
			const page = await pageView.renderFlowPage(index, { openSeq: task.sequence, priority: 0, throwError: true });
			if (!page || !current()) break;
			const links = documentOpened ? await callWASM("ofdgoOpenLinks") : await callWASM("ofdgoOpenLinks", index);
			if (!current()) break;
			for (const link of links) {
				if (!current()) break;
				const info = state.doc.pages[state.pageIndex];
				const surface = pageView.pageShell(state.pageIndex)?.querySelector(".page-surface");
				const target = pageLinkTarget(link, info, surface, task);
				if (!target) continue;
				if ((target.external || link.attachment) && !await requestConfirmation(link.attachment
					? `文档请求下载附件，是否继续？\n${link.fileName || ""}` : `文档请求打开链接，是否继续？\n${target.href}`)) return;
				task.navigating = !!link.dest;
				if (await target.activate() === false) return;
				if (task.navigating) await nextFrame();
				task.navigating = false;
			}
			if (documentOpened) documentOpened = false;
			else if (state.doc.pages[state.pageIndex]?.id === id) break;
		}
	} catch (err) {
		if (current()) setStatus(err.message || "动作无法执行");
	} finally {
		if (task === state.openActionTask) {
			state.openActionPage = state.doc?.pages[state.pageIndex]?.id;
			state.openActionTask = null;
		}
	}
}

async function activateOutline(path) {
	if (document.body.hasAttribute("data-busy") || state.editing) return;
	state.openActionTask = null;
	const sequence = state.openSeq;
	const request = ++state.outlineSeq;
	try {
		const links = await callWASM("ofdgoOutlineLinks", path);
		for (const link of links) {
			if (sequence !== state.openSeq || request !== state.outlineSeq || state.editing) break;
			const page = state.doc?.pages[state.pageIndex];
			const surface = pageView.pageShell(state.pageIndex)?.querySelector(".page-surface");
			const target = pageLinkTarget(link, page, surface);
			if (target && await target.activate() === false) break;
		}
	} catch (err) {
		if (sequence === state.openSeq && request === state.outlineSeq && !state.editing) setStatus(err.message || "目录无法打开");
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
		const link = document.createElement(outline.actionCount || outline.page || state.editing ? "button" : "span");
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
			link.append(page);
		}
		if (outline.actionCount || outline.page || state.editing) {
			link.type = "button";
			link.addEventListener("keydown", async event => {
				if (!state.editing || event.defaultPrevented || event.isComposing || !event.altKey || event.ctrlKey || event.metaKey || event.shiftKey
					|| !["ArrowUp", "ArrowDown"].includes(event.key)) return;
				event.preventDefault();
				const target = index + (event.key === "ArrowUp" ? -1 : 1);
				if (event.repeat || document.body.hasAttribute("data-busy") || target < 0 || target >= outlines.length) return;
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
					if (outline.page) pageView.renderPage(outline.page - 1);
					return;
				}
				activateOutline(current);
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

const pageIndexes = new WeakMap();

function pageIndexByID(pages, id) {
	let indexes = pageIndexes.get(pages);
	if (!indexes) {
		indexes = new Map(pages.map((page, position) => [referenceIDKey(page.id), {page, position}]));
		pageIndexes.set(pages, indexes);
	}
	const entry = indexes.get(referenceIDKey(id));
	return entry ? entry.page.index ?? entry.position : -1;
}

function referenceIDKey(id) {
	const value = String(id ?? ""), trimmed = value.replace(/^[ \t\r\n]+|[ \t\r\n]+$/g, "");
	return /^\d+$/.test(trimmed) && Number(trimmed) > 0 && Number(trimmed) <= 4294967295 ? trimmed.replace(/^0+(?=\d)/, "") : value;
}

function selectedPageIndexes() {
	if (!state.pageSelection.size) return state.pageMultiSelect ? [] : [state.pageIndex];
	return [...state.pageSelection].map(id => pageIndexByID(state.doc.pages, id)).filter(index => index >= 0).sort((a, b) => a - b);
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
	for (const button of el.pageList.children) {
		if (state.editing) button.setAttribute("data-pressed", String(state.pageSelection.has(state.doc.pages[Number(button.dataset.pageIndex)].id)));
		else button.removeAttribute("data-pressed");
	}
}

function selectThumbnailPage(event, index) {
	if (document.body.hasAttribute("data-busy")) return;
	if (event.pointerId === state.pageTouchPointer) {
		state.pageTouchPointer = null;
		return;
	}
	if (state.editing) {
		const page = state.doc.pages[index], additive = event.ctrlKey || event.metaKey || state.pageMultiSelect;
		if (!additive) state.pageSelection.clear();
		if (event.shiftKey) {
			let anchor = pageIndexByID(state.doc.pages, state.pageSelectionAnchor);
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
	return pageView.renderPage(index);
}

el.selectAllPages.addEventListener("click", () => {
	if (document.body.hasAttribute("data-busy")) return;
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
	if (!state.editing || event.pointerType !== "touch" || !event.isPrimary || document.body.hasAttribute("data-busy")) return;
	const button = event.target.closest(".page-list-item");
	if (!button || event.target.closest(".thumb-grip")) return;
	const seq = state.openSeq, pointer = event.pointerId;
	const timer = setTimeout(() => {
		if (seq !== state.openSeq || !state.editing || document.body.hasAttribute("data-busy")) return;
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
		const columns = state.pageLayout > 1 ? 2 : 1;
		const next = { ArrowUp: index - columns, ArrowDown: index + columns, ArrowLeft: index - 1, ArrowRight: index + 1, Home: 0, End: state.doc.pageCount - 1,
			Tab: index + (event.shiftKey ? -1 : 1) }[event.key];
		if (next >= 0 && next < state.doc.pageCount) {
			event.preventDefault();
			pageView.revealThumbnail(next);
			state.thumbnailWindow.nodes.get(next)?.focus({ preventScroll: true });
			return;
		}
	}
	if (!state.editing || event.isComposing || document.body.hasAttribute("data-busy")) return;
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
	button.append(grip);
	grip.addEventListener("click", event => event.stopPropagation());
	grip.addEventListener("pointerdown", event => {
		if (event.button !== 0 || document.body.hasAttribute("data-busy")) return;
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
			const rect = node.getBoundingClientRect(), after = state.pageLayout > 1 ? x > rect.left + rect.width / 2 : y > rect.top + rect.height / 2;
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
					if (state.thumbnailWindow) pageView.scrollPageWindowBy(state.thumbnailWindow, delta);
					else el.navigationContent.scrollTop += delta;
				}
				pageView.syncThumbnailWindow();
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
		if (!event.altKey || !["ArrowUp", "ArrowDown"].includes(event.key) || document.body.hasAttribute("data-busy")) return;
		event.preventDefault();
		const indexes = selectDragPages(index), direction = event.key === "ArrowUp" ? -1 : 1;
		const target = direction < 0 ? indexes[0] - 1 : indexes.at(-1) + 1;
		if (target >= 0 && target < state.doc.pageCount && await canvasEditor.commitText() && await canvasEditor.commitCrop()) {
			const focus = (direction < 0 ? target : target - indexes.length + 1) + indexes.indexOf(index);
			if (await changeSelectedPages("move", direction)) {
				pageView.revealThumbnail(focus);
				el.pageList.querySelector(`[data-page-index="${focus}"]`)?.focus({ preventScroll: true });
			}
		}
	});
}

function conversionWarningText(warning) {
	const location = [];
	if (warning.Page > 0) location.push(`第${warning.Page}页`);
	if (warning.Offset > 0) location.push(`位置${warning.Offset}`);
	const messages = [
		[/^PDF output intents preserved as metadata; not applied to rendering$/, "输出配置仅保留元数据"],
		[/^PDF annotation has no usable static appearance$/, "注解缺少可用外观"],
		[/^invalid PDF widget appearance replaced from field properties$/, "控件外观已按属性重建"],
		[/^PDF 3D rendering unavailable; normal appearance retained:/, "三维内容使用静态外观"],
		[/^PDF 3D markup default view visibility applied; view switching not transferred$/, "三维视图仅保留默认状态"]
	];
	const message = messages.find(([pattern]) => pattern.test(warning.Message))?.[1] || warning.Message;
	return `${location.length ? location.join(" · ") + "：" : ""}${message}`;
}

function renderMeta(keepDetails = false) {
	renderSecurity();
	el.conversionNotice.hidden = !state.conversionWarnings.length;
	renderMetaContent(el.conversionWarnings, state.conversionWarnings, () => {
		el.conversionWarnings.replaceChildren(...state.conversionWarnings.map(warning => {
			const item = document.createElement("p");
			item.textContent = conversionWarningText(warning);
			return item;
		}));
	});
	const doc = state.doc || {};
	updateDocumentControls();
	renderMetaContent(el.documentSelect, doc.documents || [], () => {
		el.documentSelect.replaceChildren(...(doc.documents || []).map(entry => {
			const option = document.createElement("option");
			option.value = String(entry.index);
			option.textContent = `${entry.index + 1} · ${entry.title || "未命名"}`;
			return option;
		}));
	});
	el.documentSelect.value = String(doc.documentIndex || 0);
	el.metaPanel.setAttribute("data-busy", String(!!doc.detailsPending));
	document.title = `${nativeHost ? "OFDGo" : "OFDGo WebUI"} - ${state.doc ? state.fileName : "OFD编辑器"}`;
	el.metaFile.textContent = state.doc ? state.fileName : "-";
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
	el.metaSignatures.textContent = doc.detailsPending ? "正在检查签名" : doc.detailsError ? "读取失败" : String(doc.signatureCount || 0);
	updateStructureControls();
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

function openStructureDialog() {
	if (!state.editing || !state.doc || document.body.hasAttribute("data-busy")) return;
	el.structureEntry.replaceChildren();
	for (const [index, structure] of (state.doc.structures || []).entries()) {
		if (structure.internal) continue;
		el.structureEntry.add(new Option(`${index + 1} · ${structure.root?.name || structure.typeId}`, index));
	}
	el.structureEntry.add(new Option("新建标引", -1));
	el.structureEntry.value = String((state.doc.structures || []).findIndex(structure => !structure.internal));
	el.structureAction.value = "update";
	refreshStructureNodes();
	el.structureDialog.showModal();
}

function refreshStructureNodes() {
	const structure = state.doc.structures?.[Number(el.structureEntry.value)];
	el.structureNodeSelect.replaceChildren();
	const append = (node, path, depth) => {
		if (node.references?.length) return;
		el.structureNodeSelect.add(new Option(`${"　".repeat(depth)}${node.name}`, JSON.stringify(path)));
		for (const [index, child] of (node.children || []).entries()) append(child, [...path, index], depth + 1);
	};
	if (structure?.root) append(structure.root, [], 0);
	refreshStructureFields();
}

function refreshStructureFields() {
	const index = Number(el.structureEntry.value), creating = index < 0;
	const structure = state.doc.structures?.[index];
	let node = structure?.root, canBind = Boolean(structure?.canBind);
	for (const part of JSON.parse(el.structureNodeSelect.value || "[]")) {
		node = node?.children?.[part];
		canBind &&= node?.namespace === structure.root.namespace;
	}
	canBind &&= node?.name !== "ObjectRef";
	el.structureAction.querySelector('[value="bind"]').disabled = !canBind;
	if (!canBind && el.structureAction.value === "bind") el.structureAction.value = "update";
	if (!creating && !node) el.structureAction.value = "delete";
	for (const option of el.structureAction.options) option.disabled = option.value === "bind" ? !canBind : !creating && !node && option.value !== "delete";
	const action = creating ? "create" : el.structureAction.value;
	document.querySelector("#structureNodeRow").hidden = creating || !node;
	document.querySelector("#structureActionRow").hidden = creating;
	document.querySelector("#structureTypeRow").hidden = !creating;
	el.structureType.value = creating ? "custom" : structure?.typeId || "";
	el.structureFields.hidden = action === "bind" || action === "delete";
	el.structureBinding.hidden = action !== "bind";
	el.structureName.value = action === "update" ? node?.name || "" : "";
	el.structureNamespace.value = creating ? "" : node?.namespace || "";
	el.structureText.value = action === "update" ? node?.text || "" : "";
	el.structureAttributes.replaceChildren();
	el.structureReferences.replaceChildren();
	if (action === "update") for (const attr of node?.attributes || []) addStructureAttribute(attr);
	if (action === "bind") {
		const references = [...(node?.references || []), ...(node?.children || []).flatMap(child => child.references || [])];
		for (const ref of references) addStructureReference(ref);
		if (!references.length) addStructureReference();
	}
	for (const control of el.structureFields.querySelectorAll("input,textarea,button")) control.disabled = el.structureFields.hidden;
	el.structureType.disabled = !creating;
	el.structureStatus.textContent = structure?.error || (action === "delete" ? "删除此节点及子节点" : "");
	el.structureSubmit.textContent = ({ create: "新增", add: "新增", update: "修改", bind: "绑定", delete: "删除" })[action];
}

function addStructureAttribute(attribute = {}) {
	const row = document.createElement("div");
	row.className = "structure-attribute";
	for (const [name, label] of [["name", "属性名称"], ["namespace", "命名空间"], ["value", "属性内容"]]) {
		const input = document.createElement("input");
		input.type = "text";
		input.name = name;
		input.value = attribute[name] || "";
		input.placeholder = label;
		input.required = name === "name";
		if (name === "value") input.className = "structure-attribute-value";
		row.append(input);
	}
	const remove = document.createElement("button");
	remove.type = "button";
	remove.className = "icon-button remove-button";
	remove.title = "删除属性";
	remove.addEventListener("click", () => row.remove());
	row.append(remove);
	el.structureAttributes.append(row);
}

function addStructureReference(reference = {}) {
	const row = document.createElement("div");
	row.className = "structure-reference";
	const page = document.createElement("select"), object = document.createElement("select"), ids = document.createElement("input");
	ids.type = "text";
	page.name = "page";
	ids.name = "ids";
	ids.value = (reference.objectIds || []).join(",");
	for (const [index, item] of state.doc.pages.entries()) page.add(new Option(`第${index + 1}页`, item.id));
	if (reference.pageId) page.value = reference.pageId;
	const remove = document.createElement("button"), add = document.createElement("button");
	for (const [button, label, text] of [[remove, "删除引用", ""], [add, "添加图元", "+"]]) {
		button.type = "button";
		button.className = button === remove ? "icon-button remove-button" : "icon-button";
		button.textContent = text;
		button.title = label;
	}
	remove.addEventListener("click", () => row.remove());
	add.addEventListener("click", () => {
		if (object.value) ids.value = [...ids.value.split(/[,\s]+/u).filter(Boolean), object.value].join(",");
	});
	for (const [label, control, button] of [["页码", page, remove], ["图元", object, add], ["顺序", ids, null]]) {
		const line = document.createElement("div"), title = document.createElement("label"), unit = document.createElement("div");
		line.className = "form-row";
		title.textContent = label;
		unit.className = "form-unit";
		unit.append(control);
		if (button) unit.append(button);
		line.append(title, unit);
		row.append(line);
	}
	el.structureReferences.append(row);
	const load = async () => {
		const value = page.value, sequence = state.openSeq;
		object.replaceChildren(new Option("读取中", ""));
		add.disabled = true;
		try {
			const index = state.doc.pages.findIndex(item => item.id === value);
			const targets = await callWASM("ofdgoStructureObjects", index);
			if (!row.isConnected || sequence !== state.openSeq || value !== page.value) return;
			object.replaceChildren(new Option("选择", ""));
			for (const target of targets || []) object.add(new Option(`${target.id} · ${(target.text || "图元").slice(0, 60)}`, target.id));
			add.disabled = false;
		} catch (err) {
			if (!row.isConnected || sequence !== state.openSeq || value !== page.value) return;
			object.replaceChildren(new Option("读取失败", ""));
			el.structureStatus.textContent = err.message;
		}
	};
	page.addEventListener("change", () => { ids.value = ""; load(); });
	load();
}

function renderStructures() {
	const structures = state.doc?.structures || [];
	const count = structures.reduce((total, structure) => total + !structure.internal, 0);
	el.structurePanel.hidden = !count && !state.doc?.structureError && !(state.doc && state.editing);
	el.structureManage.hidden = !state.editing;
	el.structureSummary.textContent = count;
	el.structureList.replaceChildren();
	if (state.doc?.structureError) appendInfoLine(el.structureList, "错误", state.doc.structureError, "fail");
	for (const [index, structure] of structures.entries()) {
		if (structure.internal) continue;
		if (structure.root) el.structureList.append(structureNode(structure.root, index, []));
		if (structure.error) appendInfoLine(el.structureList, "错误", structure.error, "fail");
	}
}

function updateStructureControls() {
	renderMetaContent(el.structureList, [state.doc?.structures, state.doc?.structureError, state.doc?.documentIndex, state.doc?.documentVersion, state.openSeq, state.editorInfo?.revision, state.editing], renderStructures);
	el.structureManage.disabled = !state.ready || state.exporting || document.body.hasAttribute("data-busy") || Boolean(state.doc?.detailsPending);
}

function structureNode(node, index, path) {
	const details = document.createElement("details");
	details.className = "structure-node";
	const summary = document.createElement("summary");
	summary.textContent = node.name;
	details.append(summary);
	let loaded = false;
	details.addEventListener("toggle", async () => {
		if (!details.open || loaded) return;
		loaded = true;
		const content = document.createElement("div");
		content.className = "structure-content";
		details.append(content);
		for (const attr of node.attributes || []) appendInfoLine(content, attr.name, attr.value);
		if (node.text?.trim() && !node.references?.length) appendInfoLine(content, "原文", node.text);
		const children = node.children || [];
		const references = node.references?.length || children.some(child => child.references?.length);
		for (const [i, child] of children.entries()) {
			if (!child.references?.length) content.append(structureNode(child, index, [...path, i]));
		}
		if (!references) return;
		const pending = document.createElement("div");
		pending.className = "panel-detail";
		pending.textContent = "读取中";
		content.append(pending);
		const sequence = state.openSeq;
		try {
			const targets = await callWASM("ofdgoStructureTargets", index, path);
			if (sequence !== state.openSeq || !details.isConnected) return;
			pending.remove();
			appendInfoLine(content, "原文", targets.map(target => target.text || "").join(""));
			const pages = new Map();
			for (const target of targets) {
				if (target.error) { appendInfoLine(content, "引用错误", `${target.pageId} · ${target.id}：${target.error}`, "fail"); continue; }
				const b = target.bounds;
				if (!(b.W > 0 && b.H > 0)) continue;
				const region = pages.get(target.page);
				if (region) {
					const right = Math.max(region.x + region.width, b.X + b.W), bottom = Math.max(region.y + region.height, b.Y + b.H);
					region.x = Math.min(region.x, b.X); region.y = Math.min(region.y, b.Y);
					region.width = right - region.x; region.height = bottom - region.y;
				} else pages.set(target.page, { page: target.page, x: b.X, y: b.Y, width: b.W, height: b.H });
			}
			if (pages.size) {
				const line = document.createElement("div");
				line.className = "info-line";
				const label = document.createElement("span");
				label.className = "info-label"; label.textContent = "位置";
				const links = document.createElement("span"); links.className = "info-value structure-links";
				for (const region of pages.values()) {
					const button = document.createElement("button");
					button.type = "button"; button.className = "signature-stamp-link";
					button.textContent = `第${region.page}页`;
					button.addEventListener("click", () => focusPageRegion(region, node.name));
					links.append(button);
				}
				line.append(label, links); content.append(line);
			}
		} catch (err) {
			pending.textContent = "读取失败";
			appendInfoLine(content, "错误", err.message, "fail");
			loaded = false;
			details.addEventListener("toggle", () => { if (!details.open) content.remove(); }, { once: true });
		}
	});
	return details;
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

		if (state.editing) {
			const tab = documentTab(), key = documentContentKey(state);
			const remove = document.createElement("button");
			remove.className = "small-button signature-remove";
			remove.type = "button";
			remove.textContent = "移除";
			remove.disabled = el.signatureClear.disabled;
			remove.addEventListener("click", () => {
				if (isForegroundDocument(tab) && documentContentKey(state) === key) changeDocument("ofdgoChangeSignature", null, "remove", signature.id);
			});
			badges.append(remove);
		}
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
	const stamps = (signature.docIndex || 0) === (state.doc?.documentIndex || 0) ? signature.stamps || [] : [];
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

async function focusPageRegion(region, label, alignment = "nearest") {
	const pageIndex = region.page - 1;
	if (!state.doc || pageIndex < 0 || document.body.hasAttribute("data-busy")) {
		return;
	}
	const openSeq = state.openSeq;
	if (!await pageView.renderPage(pageIndex, { fit: false, scroll: false })) {
		return;
	}
	await nextFrame();
	if (openSeq !== state.openSeq || pageIndex !== state.pageIndex) {
		return;
	}
	highlightPageRegion(region, alignment);
	if (label === "注解") {
		for (const note of pageView.pageShell(pageIndex)?.querySelectorAll(".annotation-note") || []) {
			if (note.dataset.annotationId !== String(region.id)) continue;
			window.clearTimeout(note.locateTimer);
			note.classList.add("located");
			note.locateTimer = window.setTimeout(() => note.classList.remove("located"), 1800);
		}
	}
	setStatus(`已定位${label}（第${region.page}页）`);
}

function highlightPageRegion(region, alignment = "nearest") {
	clearRegionHighlights();
	const pageIndex = region.page - 1;
	const shell = pageView.pageShell(pageIndex);
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
		if (alignment === "center") pageView.scrollViewerBy(box.top - viewer.top + (box.height - el.viewerPanel.clientHeight) / 2);
		else if (box.top < viewer.top) pageView.scrollViewerBy(box.top - viewer.top);
		else if (box.bottom > viewer.top + el.viewerPanel.clientHeight) pageView.scrollViewerBy(Math.min(box.top - viewer.top, box.bottom - viewer.top - el.viewerPanel.clientHeight));
		if (box.left < viewer.left) el.viewerPanel.scrollLeft += box.left - viewer.left;
		else if (box.right > viewer.left + el.viewerPanel.clientWidth) el.viewerPanel.scrollLeft += box.right - viewer.left - el.viewerPanel.clientWidth;
	} else mark.scrollIntoView({ block: alignment, inline: "nearest" });
	window.setTimeout(() => mark.remove(), 1800);
}

function clearRegionHighlights() {
	for (const note of el.svgHost.querySelectorAll(".annotation-note.located")) {
		window.clearTimeout(note.locateTimer);
		note.classList.remove("located");
	}
	for (const mark of el.svgHost.querySelectorAll(".region-highlight")) {
		mark.remove();
	}
}

function mountAnnotationNotes(index, surface, annotations) {
	for (const note of surface.querySelectorAll(".annotation-note")) {
		window.clearTimeout(note.locateTimer);
		note.remove();
	}
	surface.onpointermove = surface.onpointerleave = null;
	if (!state.renderAnnotations) return;
	const page = state.doc.pages[index];
	const notes = [];
	for (const annotation of annotations || []) {
		if (!annotation.visible || annotationKind(annotation) === "Link" || annotation.page !== index + 1 || !annotation.remark?.trim()) continue;
		const bounds = annotation.bounds;
		const region = !bounds ? annotation : bounds.W > 0 && bounds.H > 0
			? {x: bounds.X, y: bounds.Y, width: bounds.W, height: bounds.H, contours: annotation.contours || []}
			: {x: annotation.x, y: annotation.y, width: 22 / MM_TO_PX, height: 22 / MM_TO_PX, contours: []};
		const note = document.createElement("button");
		note.type = "button";
		note.className = "annotation-note";
		note.dataset.annotationId = annotation.id;
		note.classList.toggle("no-zoom", !!annotation.noZoom);
		note.classList.toggle("no-rotate", !!annotation.noRotate);
		note.textContent = "\u24d8";
		note.title = "查看注解";
		note.style.left = `${Math.max(22 / MM_TO_PX, Math.min(page.width, region.x + region.width)) * MM_TO_PX}px`;
		note.style.top = `${Math.max(0, Math.min(page.height - 22 / MM_TO_PX, region.y)) * MM_TO_PX}px`;
		note.addEventListener("click", () => {
			if (state.editing || document.body.hasAttribute("data-busy")) return;
			el.annotationDetail.replaceChildren();
			el.annotationFields.hidden = el.annotationSubmit.hidden = true;
			el.annotationDetail.hidden = false;
			el.annotationStatus.textContent = "";
			el.annotationClose.textContent = "关闭";
			el.annotationNoteTitle.textContent = "注解详情";
			const content = document.createElement("div");
			content.textContent = annotation.remark;
			el.annotationDetail.append(content);
			appendInfoLine(el.annotationDetail, "作者", annotation.creator);
			appendInfoLine(el.annotationDetail, "时间", formatDocumentTime(annotation.lastModDate));
			el.annotationNote.showModal();
		});
		surface.append(note);
		notes.push({note, region});
	}
	if (!notes.length) return;
	surface.onpointermove = event => {
		if (event.pointerType === "touch" || state.editing) return;
		const point = pagePoint(event.clientX, event.clientY, surface.getBoundingClientRect(), page, state.rotation);
		const overAction = event.target?.closest?.(".page-link, .page-media");
		const hovered = notes.map(({note, region}) => {
			const rect = note.getBoundingClientRect();
			const overNote = event.clientX >= rect.left && event.clientX <= rect.right && event.clientY >= rect.top && event.clientY <= rect.bottom;
			return !overAction && (overNote || canvasEditor.contains(region, point));
		});
		notes.forEach(({note}, i) => note.classList.toggle("active", hovered[i]));
	};
	surface.onpointerleave = () => {
		for (const {note} of notes) note.classList.remove("active");
	};
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
		if (state.editing) {
			const action = document.createElement("button");
			action.type = "button";
			action.className = "small-button font-binding-action";
			action.textContent = font.embedded ? "外置" : "嵌入";
			action.addEventListener("click", () => openFontBinding(font));
			head.append(action);
		}
		row.append(head);
		appendFontDetail(row, metaText);
		appendFontDetail(row, resultText);
		fragment.append(row);
	}
	el.docFontList.append(fragment);
}

async function openFontBinding(font) {
	if (!state.editing || document.body.hasAttribute("data-busy")) return;
	if (!font.embedded && font.status === "matched") {
		await changeDocument("ofdgoChangeFontBinding", null, font.id, 1);
		return;
	}
	fontBinding.font = font;
	fontBinding.sequence++;
	fontBindingElements.Form.inert = false;
	fontBinding.choices = fontManager.editorFonts().filter(choice => !choice.disabled);
	fontBindingElements.Title.textContent = font.embedded ? "外置字体" : "嵌入字体";
	fontBindingElements.Name.textContent = font.fontName || font.familyName || font.id;
	fontBindingElements.SourceRow.hidden = font.embedded;
	fontBindingElements.Source.disabled = font.embedded;
	fontBindingElements.Source.replaceChildren(new Option("选择", ""), ...fontBinding.choices.map((choice, i) => new Option(choice.fullName || choice.name, String(i))), new Option("添加", "file"));
	fontBindingElements.Hint.hidden = !font.embedded;
	fontBindingElements.Status.textContent = "";
	fontBindingElements.Submit.textContent = font.embedded ? "外置" : "嵌入";
	fontBindingElements.Panel.showModal();
}

function fontBindingError(message) {
	if (/requires the original font/.test(message)) return "字形编号需匹配原字体";
	if (/non-character glyph|private character|index-encoded/.test(message)) return "字形映射无法外置";
	if (/unsupported content/.test(message)) return "扩展引用无法确认";
	if (/does not permit/.test(message)) return "字体不允许嵌入";
	if (/no glyph|glyph is unavailable/.test(message)) return "字体缺少所需字形";
	if (/unavailable|not found|no name/.test(message)) return "匹配字体不可用";
	return message;
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
	const parts = [String(fonts.length)];
	if (missing) {
		parts.push(`缺失 ${missing}`);
	}
	if (fallback) {
		parts.push(`回退 ${fallback}`);
	}
	return parts.join(" · ");
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

function updateInvoiceControl(disabled) {
	const doc = state.doc, openSeq = state.openSeq;
	el.invoiceButton.disabled = disabled;
	el.invoiceButton.hidden = !doc || !state.editing;
	if (!doc || state.editing) return;
	let cached = invoiceDocuments.get(doc);
	if (cached?.openSeq !== openSeq) cached = null;
	if (!cached && !disabled) {
		cached = {openSeq, available: false, data: null};
		invoiceDocuments.set(doc, cached);
		callWASM("ofdgoInvoices").then(data => {
			cached.data = data;
			cached.available = !!(data.report?.invoices?.length || data.report?.issues?.some(issue => issue.code !== "missing-data"));
		}).catch(() => { cached.available = true; }).finally(() => {
			if (state.doc === doc && state.openSeq === openSeq) updateDocumentControls();
		});
	}
	el.invoiceButton.hidden = !cached?.available;
}

function updateDocumentControls() {
	const count = state.doc?.documents?.length || 0, index = managedDocumentIndex(), manager = state.documentManagement;
	const busy = document.body.hasAttribute("data-busy");
	const disabled = !state.doc || !state.ready || state.exporting || busy;
	el.printButton.disabled = disabled;
	el.documentManageButton.hidden = !state.editing;
	el.documentManageButton.disabled = disabled || state.saving;
	resourceElements.ManageButton.disabled = disabled;
	settingsElements.Button.disabled = disabled;
	updateInvoiceControl(disabled);
	updateResourceControls();
	el.documentRow.hidden = !count || !state.editing && count < 2;
	el.documentSelect.disabled = disabled || state.saving;
	const actionsDisabled = disabled || state.saving || !state.editing || Boolean(manager?.loading || manager?.failed);
	el.documentList.inert = busy || Boolean(manager?.loading);
	el.documentAddButton.disabled = actionsDisabled;
	el.documentImportButton.disabled = actionsDisabled;
	el.infoButton.disabled = disabled || !state.editing || Boolean(manager && !manager.entries);
	el.documentDeleteButton.disabled = actionsDisabled || count <= 1;
	el.documentUpButton.disabled = actionsDisabled || index === 0;
	el.documentDownButton.disabled = actionsDisabled || index >= count - 1;
	updateVersionControls();
}

function updateControls() {
	renderSecurity();
	updateStructureControls();
	const hasDoc = Boolean(state.doc);
	el.newButton.disabled = workspace.closing;
	el.closeButton.disabled = !(hasDoc || documentTab().opening || documentTab().error) || workspace.closing;
	updateDocumentControls();
	updateDisplayControls();
	updateEditorTools();
	const pageCount = state.doc ? state.doc.pageCount : 0;
	el.prevButton.disabled = !hasDoc || pageView.pageGroupStart() === 0;
	el.nextButton.disabled = !hasDoc || pageView.pageGroupStart() + state.pageLayout >= pageCount;
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
	el.pageLayoutButton.disabled = pageCount < 2 || document.body.hasAttribute("data-busy");
	el.pageLayoutButton.textContent = state.pageLayoutPreference === 1 ? "单页" : "双页";
	el.pageLayoutButton.dataset.short = state.pageLayoutPreference === 1 ? "单" : "双";
	el.fitButton.toggleAttribute("data-pressed", hasDoc && state.fitMode === "width");
	el.fitHeightButton.toggleAttribute("data-pressed", hasDoc && state.fitMode === "height");
	updateAnnotationButton();
	el.exportFormat.disabled = !hasDoc || state.exporting || !state.exportFormats.length;
	el.outputCompression.disabled = !hasDoc || state.exporting;
	el.exportPageButton.disabled = !hasDoc || state.exporting || !state.exportFormats.length;
	el.exportButton.disabled = !hasDoc || state.exporting || !state.exportFormats.length;
	el.printButton.disabled = !hasDoc || !state.ready || state.exporting || document.body.hasAttribute("data-busy");
	updateDPIControl();
}

function updateEditorTools() {
	el.viewerPanel.classList.toggle("editing", state.editing);
	el.editButton.disabled = !state.doc || !state.ready || state.exporting || document.body.hasAttribute("data-busy");
	el.editButton.setAttribute("data-pressed", String(state.editing));
	el.editButton.title = state.editing ? "阅读" : "编辑";
	el.editNotice.textContent = state.editorInfo?.editWarnings?.join(" · ") || "";
	el.editNotice.hidden = !state.editing || !el.editNotice.textContent;
	el.editorTools.hidden = !state.editing;
	const pagesDisabled = !state.editing || !state.ready || state.exporting;
	el.insertTextButton.disabled = el.insertImageButton.disabled = pagesDisabled || !pageCan("insert");
	el.saveButton.disabled = !state.doc || !state.ready || state.exporting || state.saving || (!state.editorInfo && !state.ofdBytes);
	el.saveAsButton.disabled = el.saveButton.disabled;
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
	const enabled = state.editing && state.selectObjects && !state.panMode && !sealPreview && !resourceManager.pick;
	canvasEditor.setEnabled(enabled);
	el.undoButton.disabled = !state.editorInfo?.canUndo || !state.ready || state.exporting;
	el.redoButton.disabled = !state.editorInfo?.canRedo || !state.ready || state.exporting;
	updateObjectControls(canvasEditor.selected);
}

function updateObjectDistribution(item, disabled) {
	const tab = documentTab(), state = tab.state;
	const ids = item?.items?.slice().sort((a, b) => a.order - b.order).map(member => member.id) || [];
	if (disabled || ids.length < 3) state.objectDistribution = null;
	else {
		const { openSeq, wasmSeq } = state, revision = state.editorInfo?.revision;
		const key = JSON.stringify([openSeq, wasmSeq, revision, item.index, ids]);
		if (state.objectDistribution?.key !== key) {
			const request = state.objectDistribution = { key, axes: [] };
			const current = () => !tab.closed && state.objectDistribution === request && state.openSeq === openSeq && state.wasmSeq === wasmSeq && state.editorInfo?.revision === revision;
			callTabWASM(tab, "ofdgoDistributionAxes", item.index, ids).then(axes => {
				if (!current()) return;
				request.axes = axes;
				if (isForegroundDocument(tab)) updateObjectControls(canvasEditor.selected);
			}).catch(error => {
				if (current() && isForegroundDocument(tab)) showError(error, false);
			});
		}
	}
	const axes = state.objectDistribution?.axes || [];
	el.objectDistribute.disabled = disabled || !axes.length;
	for (const option of el.objectDistribute.options) if (option.value) option.disabled = !axes.includes(option.value);
}

function updateObjectControls(item, reset = false) {
	const members = item?.items || (item ? [item] : []);
	const disabled = !item || Boolean(item.draft) || !state.ready || state.exporting;
	el.deleteObjectButton.disabled = disabled || !canEditObject(item, "delete");
	el.copyObjectButton.disabled = disabled || !canEditObject(item, "copy");
	el.objectStyleButton.disabled = disabled || !canEditObject(item, "transform");
	const cropping = Boolean(canvasEditor.crop);
	el.objectBoundsButton.disabled = disabled || cropping || !canEditObject(item, "transform");
	el.objectAlign.disabled = disabled || cropping || !canEditObject(item, "arrange");
	el.objectRotate.disabled = el.objectFlip.disabled = disabled || cropping || !canEditObject(item, members.some(member => member.scoped) ? "transform" : "arrange");
	el.cropImageButton.disabled = disabled || item.type !== "ImageObject" || !canEditObject(item, "cropImage");
	el.saveImageButton.disabled = disabled || !item.imageResource;
	el.imageFit.disabled = el.cropImageButton.disabled || cropping || Boolean(item.scoped && !canEditObject(item, "fitImage"));
	el.cropImageButton.textContent = cropping ? "完成" : "裁剪";
	el.cropImageButton.setAttribute("data-pressed", String(cropping));
	el.resetCropButton.textContent = cropping ? "取消" : "还原";
	el.resetCropButton.disabled = canEditObject(item, "resetCrop") ? disabled : el.cropImageButton.disabled || !cropping && (item.scoped ? !item.cropped : !item.imageBounds || ["x", "y", "width", "height"].every(key => Math.abs(item[key] - item.imageBounds[key]) < 1e-9));
	updateObjectDistribution(item, el.objectAlign.disabled);
	el.editObjectButton.disabled = disabled || (item.items ? !item.items.every(member => member.type === "TextObject" && !member.richText)
		: !canEditObject(item, "children") && (item.type === "PathObject"
		|| !(item.type === "TextObject" && canEditObject(item, "rewriteText")) && !canEditObject(item, item.type === "TextObject" ? "textContent" : item.type === "ImageObject" ? "replaceImage" : "update")));
	el.editObjectButton.textContent = item?.type === "ImageObject" ? "替换" : "修改";
	el.multiSelectButton.disabled = !state.editing || !canvasEditor.enabled || !state.ready || state.exporting;
	const selection = selectedText(item);
	const text = selection ? currentTextStyle() : state.textDefaults;
	const textDisabled = !state.editing || !state.ready || state.exporting || Boolean(item?.items && !selection) || Boolean(canvasEditor.input?.composing || canvasEditor.input?.saving);
	fontPicker.setDisabled(textDisabled || text !== state.textDefaults && !item.draft && !canEditObject(item, "replaceFont"));
	el.textSize.disabled = textDisabled || text !== state.textDefaults && !item.draft && (!canEditObject(item, "reflow") || Boolean(item?.items && !item.selectionIDs) && !canEditObject(item, "layoutKnown"));
	el.textColor.disabled = textDisabled || text !== state.textDefaults && !item.draft && !item.richText && !canEditObject(item, "paint");
	el.textFontAdd.disabled = !state.editing || !state.ready || state.exporting;
	const formatsDisabled = textDisabled || Boolean(item && !item.draft && (!selection || !canEditObject(item, "textContent")));
	for (const button of textFormatButtons) {
		button.disabled = formatsDisabled;
		const key = button.dataset.textFormat;
		button.setAttribute("data-pressed", text[key] === undefined && (canvasEditor.input?.rich || item?.richText || item?.items) ? "mixed" : String(Boolean(text[key])));
	}
	textBackground.disabled = formatsDisabled;
	textBackground.classList.toggle("mixed-color", Boolean((canvasEditor.input?.rich || item?.richText || item?.items) && text.background === undefined));
	if (text.background) textBackground.value = text.background;
	document.querySelector("#textHighlight").style.setProperty("--highlight-color", textBackground.value);
	const richHistory = canvasEditor.input?.rich || canvasEditor.input?.history;
	if (richHistory) {
		el.undoButton.disabled = textDisabled || richHistory.index === 0;
		el.redoButton.disabled = textDisabled || richHistory.index === richHistory.entries.length - 1;
	} else {
		el.undoButton.disabled = !state.editorInfo?.canUndo || !state.ready || state.exporting;
		el.redoButton.disabled = !state.editorInfo?.canRedo || !state.ready || state.exporting;
	}
	el.textLineHeight.disabled = el.textSpacing.disabled = el.textSize.disabled;
	el.textWrap.disabled = el.textLineHeight.disabled || text === state.textDefaults;
	const reflow = document.querySelector("#textReflow"), positioned = canvasEditor.input?.source && (item?.selectionIDs || !canEditObject(item, "layoutKnown"));
	reflow.hidden = !positioned;
	el.textWrap.hidden = !!positioned;
	reflow.disabled = textDisabled || !canEditObject(item, "reflow") || !!canvasEditor.input?.reflow;
	el.paragraphButton.disabled = el.textLineHeight.disabled;
	el.textAlign.disabled = el.textSize.disabled || Boolean(item?.selectionIDs);
	const layoutKnown = text === state.textDefaults || Boolean(item?.draft) || canEditObject(item, "layoutKnown");
	const rich = canvasEditor.input?.rich;
	const alignment = item?.items ? text.align ?? "" : rich ? richTextAlignment(rich.model, ...rich.selection()) : item?.richText ? richTextAlignment(item.richText) : layoutKnown ? text.align || "left" : "";
	el.textAlign.options[0].textContent = rich || item?.richText || item?.items ? "混合" : "原文";
	el.textAlign.value = alignment;
	el.textLineHeight.placeholder = (rich || item?.richText || item?.items) && text.paragraphHeight === undefined ? "混合" : layoutKnown ? "自动" : "原文";
	el.textSpacing.placeholder = (rich || item?.richText || item?.items) && text.letterSpacing === undefined ? "混合" : layoutKnown ? "0" : "原文";
	el.textWrap.setAttribute("data-pressed", item?.items && text.wrap === undefined ? "mixed" : String(Boolean(text.wrap)));
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
	el.textColor.classList.toggle("mixed-color", Boolean(selection && !text.color));
	el.textColor.title = selection && !text.color ? "文字颜色不同" : "文字颜色";
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
	el.annotationButton.setAttribute("data-pressed", String(state.renderAnnotations));
}

async function callWASM(name, ...args) {
	return callTabWASM(documentTab(), name, ...args);
}

function operationRequest(tab) {
	return [...wasmRequests.values()].findLast(request => request.tab === tab && request.cancellable && !request.name.includes("Print"));
}

function operationProgress(request, progress) {
	const text = request.name.includes("Print") ? "正在准备打印" : request.name === "ofdgoEditDocument" ? "正在准备编辑" : request.name.includes("Save") ? "正在准备保存" : request.name.includes("Export") ? "正在准备导出" : "正在准备文档";
	const completed = progress?.phase === "convert" && progress.total > 0;
	const label = completed ? `${text} · ${progress.completed} / ${progress.total} 页` : text;
	request.progress = {text:label, percent:completed ? progress.completed / progress.total * 100 : null};
	if (request.name.includes("Print")) {
		if (request.tab.print?.context) request.tab.print.status(label, true);
	} else if (operationRequest(request.tab) === request) {
		request.tab.progress = request.progress;
		if (isForegroundDocument(request.tab)) setProgress(label, request.tab.progress.percent);
	}
}

function finishOperation(request) {
	if (request.tab.preparationBusy && !operationRequest(request.tab)) {
		request.tab.preparationBusy = false;
		if (!request.tab.closed) setBusy(false, "", 0, "", request.tab);
	}
	if (request.cancellable && isForegroundDocument(request.tab) && !operationRequest(request.tab)) {
		el.cancelExportButton.hidden = !request.tab.state.saving && !request.tab.state.exporting;
	}
}

function cancelDocumentPreparation(tab) {
	if (!tab.state.sourcePDF || tab.state.pdfComplete) return;
	for (const [id, request] of wasmRequests) if (request.tab === tab && (request.preparing || request.name === "ofdgoPrepareSave")) cancelOperation(id);
}

function cancelOperation(id) {
	const request = wasmRequests.get(id);
	if (!request) return;
	request.canceled = true;
	if (request.preparing) {
		wasmRequests.delete(id);
		request.reject(new DOMException("操作已取消", "AbortError"));
	} else request.tab.worker?.postMessage({type:"cancel", id});
}

async function prepareOperation(id, request) {
	request.preparing = true;
	request.cancellable = true;
	const tab = request.tab;
	if (!request.name.includes("Print")) {
		if (!tab.busy) tab.preparationBusy = true;
		setBusy(true, tab.progress?.text || "正在准备文档", null, "", tab);
		if (isForegroundDocument(tab)) {
			el.cancelExportButton.hidden = false;
			el.cancelExportButton.disabled = false;
		}
	}
	operationProgress(request);
	try {
		await completePDF(tab);
		if (wasmRequests.get(id) !== request || request.canceled || tab.closed) return;
		request.preparing = false;
		request.send();
	} catch (err) {
		if (wasmRequests.get(id) !== request) return;
		wasmRequests.delete(id);
		request.reject(err);
	}
}

async function callTabWASM(tab, name, ...args) {
	const state = tab.state;
	const visible = isForegroundDocument(tab);
	if (tab.closed) throw new Error("文档已关闭");
	if (state.wasmExited) {
		if (workspace.active === tab) scheduleWASMRecovery();
		throw new Error(state.wasmRecovering ? STATUS.recovering : "引擎运行中断");
	}
	if (!state.ready) {
		throw new Error("引擎尚未就绪");
	}
	return new Promise((resolve, reject) => {
		const id = ++wasmRequestID;
		if (name === "ofdgoPrepareSave" || name === "ofdgoSaveSnapshot") {
			state.saveRequestID = id;
			if (visible) {
				el.cancelExportButton.hidden = false;
				el.cancelExportButton.disabled = false;
			}
		}
		if (name === "ofdgoResources" && workspace.active === tab && resourceElements.Panel.open) resourceManager.requestID = id;
		const request = {name, openSeq:state.openSeq, tab, id, cancellable:name === "ofdgoEditDocument",
			resolve:value => { finishOperation(request); resolve(value); }, reject:err => { finishOperation(request); reject(err); }};
		wasmRequests.set(id, request);
		if (request.cancellable && visible) { el.cancelExportButton.hidden = false; el.cancelExportButton.disabled = false; }
		if (name === "ofdgoExportMedia") wasmRequests.get(id).mediaID = args[0];
		if (name === "ofdgoOpenPDF" || state.sourcePDF && !state.editorInfo) {
			const request = wasmRequests.get(id);
			request.localFonts = fontManager.localEntries();
			request.fontData = new Map();
			if (name === "ofdgoOpenPDF") args.push([...request.localFonts.keys()]);
		}
		if (name === "ofdgoImportDocuments" || name === "ofdgoImportImages" || name === "ofdgoExportImages" || name === "ofdgoOpenPDF" || name === "ofdgoExportPage" || name === "ofdgoExportDocument" || name === "ofdgoExportAttachment" || name === "ofdgoExportResource" || name === "ofdgoExportVersion" || name === "ofdgoSaveDocument" || name === "ofdgoSaveSigned" || name === "ofdgoImportPages") {
			state.exportRequestID = id;
			if (visible) {
				el.cancelExportButton.hidden = false;
				el.cancelExportButton.disabled = false;
			}
		}
		try {
			const transfer = [];
			if (name === "ofdgoOpen" && !nativeHost && ArrayBuffer.isView(args[0])) {
				args[0] = args[0].slice();
				transfer.push(args[0].buffer);
			}
			if (name === "ofdgoLoadImport" && ArrayBuffer.isView(args[0]) && !nativeHost) transfer.push(args[0].buffer);
			const message = {id, name, args};
			if (name === "ofdgoLoadPDF") message.background = true;
			if (name === "ofdgoRenderPage" && [...state.pageInFlight.values()].some(task => task.index === args[0] && pageView.pageRenderPriority(task, state) >= 6)) message.preload = true;
			request.send = () => tab.worker.postMessage(message, transfer);
			request.send();
		} catch (err) {
			wasmRequests.delete(id);
			request.reject(err);
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

function clearBusyControls() {
	for (const control of busyControls) control.removeAttribute("data-busy-disabled");
	busyControls.clear();
}

function setBusy(busy, text = "", percent = 0, status = "", tab = documentTab()) {
	const operation = operationRequest(tab);
	if (!busy && operation) tab.preparationBusy = true;
	busy ||= Boolean(tab.editTask || operation);
	if (operation?.preparing && operation.progress) ({text, percent} = operation.progress);
	const resumeSearch = tab.busy && !busy && tab.state.searchPaused;
	tab.busy = busy;
	if (text) tab.progress = {text, percent};
	if (status) tab.status = status;
	if (!isForegroundDocument(tab)) return;
	const editing = busy && tab.editTask && !operation && !text;
	if (!editing) clearBusyControls();
	const controls = editing ? document.querySelectorAll("button:enabled, input:enabled, textarea:enabled, select:enabled") : [];
	document.body.toggleAttribute("data-busy", busy);
	el.newButton.disabled = false;
	el.documentNewTab.disabled = workspace.closing;
	el.pageLayoutButton.disabled = busy || !(state.doc?.pageCount > 1);
	el.closeButton.disabled = !(state.doc || tab.opening || tab.error) || workspace.closing;
	el.refreshAppButton.disabled = busy;
	updateStructureControls();
	updateDocumentControls();
	renderSecurity();
	updateDisplayControls();
	el.editButton.disabled = busy || Boolean(sealPreview || resourceManager.pick) || !state.doc || !state.ready || state.exporting;
	el.closePanel.inert = busy && !el.closePanel.open;
	el.createForm.inert = busy && !state.imageImporting;
	updateCreateOptions();
	el.insertForm.inert = busy;
	el.pageForm.inert = busy;
	el.paragraphForm.inert = busy;
	el.infoForm.inert = busy;
	el.documentForm.inert = busy;
	el.versionForm.inert = busy;
	el.attachmentForm.inert = busy;
	el.annotationForm.inert = busy;
	el.importForm.inert = busy && !state.importing;
	for (const input of [el.importFile, el.importRange, el.importPosition, el.importOutlines]) input.disabled = busy;
	updateImportControls(busy);
	el.batchPagesForm.inert = busy;
	el.objectStyleForm.inert = busy;
	el.objectBoundsForm.inert = busy;
	el.outlineForm.inert = busy;
	el.structureForm.inert = busy;
	el.outlineList.inert = busy;
	el.navigationTabs.inert = busy && !(tab.preparationBusy && operation?.preparing && operation.name === "ofdgoAnnotationInfos");
	el.pageList.inert = busy;
	el.editorTools.inert = busy || Boolean(sealPreview || resourceManager.pick);
	el.fontList.inert = busy;
	el.docFontList.inert = busy;
	for (const control of controls) {
		control.setAttribute("data-busy-disabled", "");
		busyControls.add(control);
	}
	if (!busy) {
		if (resumeSearch) queueMicrotask(() => {
			if (isForegroundDocument(tab) && !tab.busy && !tab.refreshPending && tab.state.searchPaused) searchDocument();
		});
		el.progressPanel.hidden = !state.saving;
		if (state.saving) {
			el.cancelExportButton.hidden = false;
			setProgress("正在保存文档", null);
		}
		if (state.fontSyncPending) {
			window.setTimeout(syncUserFonts, 0);
		}
		if (state.fontRenderPending) window.setTimeout(refreshPendingFonts, 0);
		return;
	}
	el.exportPanel.close();
	pageView.endPan();
	el.progressPanel.hidden = !text;
	setProgress(text, percent, status);
	if (operationRequest(tab)) { el.cancelExportButton.hidden = false; el.cancelExportButton.disabled = false; }
}

function setProgress(text = "", percent = 0, status = "", tab = documentTab()) {
	tab.progress = {text, percent};
	if (status) tab.status = status;
	if (!isForegroundDocument(tab)) return;
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
}

function setStatus(text) {
	documentTab().status = text;
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
	return value.toFixed(1).replace(/\.0$/, "");
}

function downloadBytes(bytes, mime, name) {
	if (nativeHost) return nativeHost.download(bytes, mime, name).catch(error => { if (error.name !== "AbortError") showError(error, false); });
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
	el.imageDPI.disabled = state.exporting || !state.doc || document.body.hasAttribute("data-busy") || (pageView.displayMode() !== "raster" && !exportFormatUsesDPI(el.exportFormat.value));
	el.imageDPI.title = `输出精度：${currentImageDPI()} DPI`;
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
