import { nativeHost, engineLimits } from "./ofdgo_host.js";

export const MM_TO_PX = 96 / 25.4;
export const SCROLL_SEGMENT_SIZE = 1000000;
export const DEFAULT_VIEW_POLICY = {
	compactWidth: 640,
	tallPageRatio: 2,
	compact: { continuous: true, fitMode: "width" },
	wide: { continuous: false },
};

const PAGE_CACHE_LIMIT = 16;
const PAGE_CACHE_BYTES = 32 * 1024 * 1024;
const THUMB_CACHE_LIMIT = 96;
const THUMB_CACHE_BYTES = 16 * 1024 * 1024;
const DETACHED_DOM_LIMIT = 8;
const FAST_SCROLL_VELOCITY = 3.5;
const SCROLL_SETTLE_DELAY = 55;
const THUMB_SETTLE_DELAY = 80;
const REVEAL_THUMB_DEBOUNCE = 48;
const PAGE_LAYOUT_GAP = 16;
const svgFontBytes = new WeakMap();
let textMeasure = null;

export function resolveDefaultView({ viewportWidth = window.innerWidth, pageCount = 0, pageLayout = 1, page = null, rotation = 0 } = {}) {
	if (viewportWidth <= DEFAULT_VIEW_POLICY.compactWidth) {
		return { continuous: DEFAULT_VIEW_POLICY.compact.continuous, fitMode: DEFAULT_VIEW_POLICY.compact.fitMode };
	}
	const width = page ? (rotation % 180 ? page.height : page.width) : 0;
	const height = page ? (rotation % 180 ? page.width : page.height) : 0;
	const tall = width > 0 && height > width * DEFAULT_VIEW_POLICY.tallPageRatio;
	const landscapeOrSquare = width > 0 && height > 0 && width >= height;
	const fitMode = tall ? "width" : (landscapeOrSquare || pageCount <= 1 || pageLayout > 1 ? "height" : "width");
	return { continuous: DEFAULT_VIEW_POLICY.wide.continuous, fitMode };
}

export function createPageView(context) {
	let scrollbarMeasurement = null;
	let heightFitWidthLimit = 0;
	let appliedHeightFitWidthLimit = 0;
	let widthFitTarget = 0;
	let appliedWidthFitTarget = 0;
	const pageView = {
		defaultView(page = pageView.currentPageInfo()) {
			return resolveDefaultView({
				viewportWidth: window.innerWidth,
				pageCount: context.state.doc?.pageCount || 0,
				pageLayout: context.state.pageLayout,
				page,
				rotation: context.state.rotation,
			});
		},

		widthFitTargetWidth() {
			const available = Math.max(1, context.el.viewerPanel.clientWidth - pageView.pageSpace() * 2);
			return widthFitTarget > 0 ? Math.min(available, widthFitTarget) : available;
		},

		heightFitMaxWidth() {
			const available = Math.max(1, context.el.viewerPanel.clientWidth - pageView.pageSpace() * 2);
			return heightFitWidthLimit > 0 ? Math.min(available, heightFitWidthLimit) : available;
		},

		pageScale(page, scale = context.state.scale, fitMode = context.state.fitMode, maxWidth = null) {
			if (!page || context.state.pageLayout > 1 || fitMode === "free") {
				return scale;
			}
			const size = pageView.pageViewSize(page);
			const pageWidth = Math.max(1, size.width * MM_TO_PX);
			if (fitMode === "width") {
				const target = maxWidth ?? pageView.widthFitTargetWidth();
				return Math.min(4, target / pageWidth);
			}
			const limit = maxWidth ?? pageView.heightFitMaxWidth();
			return Math.min(scale, limit / pageWidth);
		},

		async renderPage(index, options = {}) {
			const tab = context.documentTab(), state = tab.state;
			if (!state.doc || (state.editorInfo && document.body.hasAttribute("data-busy") && !options.keepBusy)) {
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
			if (options.navigation !== state.pageNavigation) state.pageNavigation = null;
			const previousIndex = state.pageIndex;
			const sameSpreadRow = state.pageLayout > 1 && pageView.pageGroupStart(index) === pageView.pageGroupStart(previousIndex);
			if (!options.keepBusy && index !== state.pageIndex) {
				if (!await context.canvasEditor.commitNudge() || !context.isForegroundDocument(tab) || openSeq !== state.openSeq) return null;
				if (!await context.canvasEditor.commitText() || !context.isForegroundDocument(tab) || openSeq !== state.openSeq) return null;
				if (!await context.canvasEditor.commitCrop() || !context.isForegroundDocument(tab) || openSeq !== state.openSeq) return null;
			}
			const pageSeq = ++state.pageSeq;
			if (!state.exporting) {
				if (options.keepBusy) {
					context.setProgress("正在渲染页面", 76);
				} else {
					context.setBusy(true, "正在渲染页面", 35, "正在渲染页面");
				}
			}
			try {
				pageView.setCurrentPage(index);
				const stableContinuousTwoPage = state.pageLayout > 1 && state.continuous && state.pageWindow?.rows?.length > 0;
				const stableSingleWidth = state.fitMode === "width" && state.pageLayout === 1 && state.pageWindow?.offsets.length > 1;
				if (options.fit !== false && !stableSingleWidth && !stableContinuousTwoPage) {
					pageView.applyFit(false);
				}
				const scrollOptions = { preserveInline: sameSpreadRow && state.fitMode === "free" };
				if (options.scroll !== false) {
					pageView.scrollToPage(index, scrollOptions);
				} else if (state.pageWindow && (!pageView.pageShell(index) || pageView.pageShell(index).style.visibility === "hidden")) {
					pageView.scrollToPage(index, scrollOptions);
				}
				pageView.prunePageRenderQueue();
				const pairStart = pageView.pageGroupStart(index);
				let pairTask = null;
				if (state.pageLayout > 1) {
					for (let i = pairStart; i < Math.min(pageCount, pairStart + state.pageLayout); i++) {
						if (i !== index) pairTask = pageView.renderFlowPage(i, { openSeq, priority: 1 });
					}
				}
				const page = await pageView.renderFlowPage(index, { throwError: true, openSeq, priority: 0 });
				if (pairTask && options.waitPair) await pairTask.catch(() => null);
				if (!page || openSeq !== state.openSeq || index !== state.pageIndex) {
					return null;
				}
				if (options.scroll !== false) {
					pageView.scrollToPage(index, scrollOptions);
				}
				if (!options.deferNearby) pageView.queueNearbyPages(index, openSeq);
				return page;
			} catch (err) {
				if (openSeq === state.openSeq) {
					context.showError(err, false);
				}
				return null;
			} finally {
				if (!options.keepBusy && !state.exporting && !context.isForegroundDocument(tab)) context.setBusy(false, "", 0, "", tab);
				if (!options.keepBusy && !state.exporting && openSeq === state.openSeq && pageSeq === state.pageSeq) {
					context.setBusy(false, "", 0, "", tab);
					if (!options.openAction && pageView.pageHasOpenAction(index)) context.scheduleOpenActions();
				}
			}
		},

		pageHasOpenAction(index = context.state.pageIndex) {
			const doc = context.state.doc;
			if (!doc) return false;
			const page = pageView.pageInfo(index);
			if (page && typeof page.hasOpenAction === "boolean") return page.hasOpenAction;
			if (typeof doc.hasOpenAction === "boolean") return doc.hasOpenAction;
			return true;
		},

		pageInfo(index) {
			const doc = context.state.doc;
			if (!doc || index < 0 || index >= (doc.pageCount || 0)) return null;
			return doc.pages?.[index] || null;
		},

		virtualIndex(offsets, top) {
			let low = 0, high = offsets.length - 1;
			while (low + 1 < high) {
				const mid = (low + high) >>> 1;
				if (offsets[mid] <= top) low = mid;
				else high = mid;
			}
			return low;
		},

		virtualRowIndex(view, top) {
			const rowOffsets = view?.rowOffsets;
			if (!rowOffsets || rowOffsets.length < 2) return 0;
			let low = 0, high = rowOffsets.length - 1;
			while (low + 1 < high) {
				const mid = (low + high) >>> 1;
				if (rowOffsets[mid] <= top) low = mid;
				else high = mid;
			}
			return Math.min(low, view.rows.length - 1);
		},

		pageRowIndex(view, index) {
			if (!view?.rows?.length) return 0;
			const stride = view.columns > 1 ? view.columns : 1;
			return Math.max(0, Math.min(view.rows.length - 1, Math.floor(index / stride)));
		},

		createPageWindow(host, viewport, overscan, create, release, observe) {
			host.classList.add("virtual-pages");
			return {
				host, viewport, overscan, create, release, observe,
				nodes: new Map(), nodeStyles: new Map(),
				offsets: new Float64Array(1), rows: [], rowOffsets: new Float64Array(1),
				origin: 0, total: 0, gap: 0, inset: null, padBottom: null,
				hostWidth: 0, columns: 0, logicalTop: null, scrollTop: 0,
			};
		},

		invalidateWindowMetrics(view) {
			if (!view) return;
			view.inset = null;
			view.padBottom = null;
			if (view.total <= SCROLL_SEGMENT_SIZE || view.logicalTop !== null && view.logicalTop <= 0) {
				view.logicalTop = null;
			}
		},

		windowInset(view) {
			if (!view) return 0;
			if (view.inset !== null) return view.inset;
			if (view.host === context.el.svgHost && (view.rows.length < 1 || view.total <= 0 || !view.host.style.height)) {
				return pageView.pageBlockSpace();
			}
			const inset = view.host.getBoundingClientRect().top - view.viewport.getBoundingClientRect().top
				+ view.viewport.scrollTop - view.viewport.clientTop;
			view.inset = Number.isFinite(inset) ? inset : 0;
			return view.inset;
		},

		windowPadBottom(view) {
			if (!view) return 0;
			if (view.padBottom !== null) return view.padBottom;
			view.padBottom = Number.parseFloat(getComputedStyle(view.viewport).paddingBottom) || 0;
			return view.padBottom;
		},

		virtualScrollMetrics(view, inset) {
			const extent = Math.min(SCROLL_SEGMENT_SIZE, view.total);
			const height = view.viewport.clientHeight;
			const maxTop = Math.max(0, view.viewport.scrollHeight - height);
			const bottomInset = Math.max(0, view.viewport.scrollHeight - extent - inset);
			const contentScrollMax = Math.max(0, maxTop - inset - bottomInset);
			const contentLogicalMax = Math.max(0, view.total - height);
			return { height, maxTop, bottomInset, contentScrollMax, contentLogicalMax };
		},

		pageWindowTop(view) {
			const inset = pageView.windowInset(view);
			if (view.total <= SCROLL_SEGMENT_SIZE) return view.viewport.scrollTop - inset;
			const delta = view.viewport.scrollTop - view.scrollTop;
			if (!delta && view.logicalTop !== null) return view.logicalTop;
			if (view.relativeScroll && view.logicalTop !== null) return view.logicalTop + delta;
			const { maxTop, bottomInset, contentScrollMax, contentLogicalMax } = pageView.virtualScrollMetrics(view, inset);
			const scrollTop = view.viewport.scrollTop;
			if (scrollTop <= inset) return scrollTop - inset;
			if (scrollTop >= maxTop - bottomInset) return contentLogicalMax + scrollTop - (maxTop - bottomInset);
			return contentScrollMax > 0 ? (scrollTop - inset) / contentScrollMax * contentLogicalMax : 0;
		},

		scrollPageWindowBy(view, delta) {
			if (!view) return;
			pageView.syncPageWindow(view, pageView.pageWindowTop(view) + delta);
			if (view === context.state.pageWindow && view.total > SCROLL_SEGMENT_SIZE) pageView.schedulePageSync();
		},

		scrollViewerBy(delta) {
			if (context.state.pageWindow) pageView.scrollPageWindowBy(context.state.pageWindow, delta);
			else context.el.viewerPanel.scrollTop += delta;
		},

		syncPageWindow(view, target = null) {
			if (!view || view.rows.length < 1) return;
			const { host, viewport, offsets, nodes, nodeStyles, rows } = view;
			const extent = Math.min(SCROLL_SEGMENT_SIZE, view.total);
			const heightText = `${extent}px`;
			if (host.style.height !== heightText) {
				host.style.height = heightText;
				pageView.invalidateWindowMetrics(view);
			}
			const inset = pageView.windowInset(view);
			const { height, maxTop, bottomInset, contentScrollMax, contentLogicalMax } = pageView.virtualScrollMetrics(view, inset);
			const logicalMax = view.total > SCROLL_SEGMENT_SIZE ? contentLogicalMax + bottomInset : maxTop - inset;
			const logical = Math.max(-inset, Math.min(logicalMax, target ?? pageView.pageWindowTop(view)));
			let nextTop;
			if (view.total <= SCROLL_SEGMENT_SIZE) {
				nextTop = Math.max(0, Math.min(maxTop, logical + inset));
			} else if (logical <= 0) {
				nextTop = Math.max(0, logical + inset);
			} else if (logical >= contentLogicalMax) {
				nextTop = Math.min(maxTop, maxTop - bottomInset + logical - contentLogicalMax);
			} else {
				nextTop = inset + (contentScrollMax > 0 && contentLogicalMax > 0 ? logical * contentScrollMax / contentLogicalMax : 0);
			}
			if (target !== null || view.total > SCROLL_SEGMENT_SIZE && Math.abs(viewport.scrollTop - nextTop) >= 1) {
				viewport.scrollTop = nextTop;
			}
			const activeLogical = view.total <= SCROLL_SEGMENT_SIZE && height > 0 ? viewport.scrollTop - inset : logical;
			view.scrollTop = viewport.scrollTop;
			view.logicalTop = activeLogical;
			view.origin = view.total > SCROLL_SEGMENT_SIZE ? Math.max(0, Math.min(view.total - extent, activeLogical - viewport.scrollTop + inset)) : 0;
			const firstRow = pageView.virtualRowIndex(view, Math.max(view.origin, activeLogical - view.overscan));
			const lastRow = pageView.virtualRowIndex(view, Math.min(view.origin + extent, activeLogical + height + view.overscan));
			const first = rows[firstRow].start;
			const last = rows[lastRow].end;
			const selection = host === context.el.svgHost && context.state.documentSelection ? document.getSelection() : null;
			const selected = selection?.rangeCount && context.isDocumentSelection(selection.getRangeAt(0));
			let changed = false;
			for (const [index, node] of nodes) {
				if ((index < first || index > last) && view.release(node, index)) {
					node.remove();
					nodes.delete(index);
					nodeStyles.delete(index);
					changed = true;
				}
			}
			const created = [];
			for (let index = first; index <= last; index++) {
				if (nodes.has(index)) continue;
				const node = view.create(pageView.pageInfo(index));
				nodes.set(index, node);
				let nextNode = null, nextIndex = Infinity;
				for (const [existingIndex, existingNode] of nodes) {
					if (existingIndex > index && existingIndex < nextIndex && existingNode.parentNode === host) {
						nextIndex = existingIndex;
						nextNode = existingNode;
					}
				}
				host.insertBefore(node, nextNode);
				changed = true;
				created.push([node, index]);
			}
			const isPageList = host === context.el.pageList;
			for (const [index, node] of nodes) {
				const top = offsets[index] - view.origin;
				const itemHeight = view.heights?.[index] ?? offsets[index + 1] - offsets[index] - view.gap;
				const clampedTop = `${Math.max(-itemHeight, Math.min(extent, top))}px`;
				const visibility = top + itemHeight < 0 || top >= extent ? "hidden" : "";
				let styleCache = nodeStyles.get(index);
				if (!styleCache) {
					styleCache = { top: "", left: "", right: "", transform: "", visibility: null };
					nodeStyles.set(index, styleCache);
				}
				if (view.lefts) {
					const leftVal = view.columns && isPageList
						? index % 2 ? `calc(50% + ${view.gap / 2}px)` : "0px"
						: `${view.lefts[index]}px`;
					if (styleCache.left !== leftVal) { node.style.left = leftVal; styleCache.left = leftVal; }
					if (styleCache.right !== "auto") { node.style.right = "auto"; styleCache.right = "auto"; }
					if (styleCache.transform !== "none") { node.style.transform = "none"; styleCache.transform = "none"; }
				}
				if (styleCache.top !== clampedTop) { node.style.top = clampedTop; styleCache.top = clampedTop; }
				if (styleCache.visibility !== visibility) { node.style.visibility = visibility; styleCache.visibility = visibility; }
			}
			for (const [node, index] of created) view.observe(node, index);
			if (selected && changed) selection.getRangeAt(0).selectNodeContents(host);
			view.anchor = pageView.pageWindowAnchor(view);
		},

		pageWindowAnchor(view) {
			if (!view?.rows?.length) return { index: context.state.pageIndex, row: 0, offset: 0 };
			const top = view.logicalTop ?? pageView.pageWindowTop(view);
			const rowIndex = pageView.virtualRowIndex(view, Math.max(0, top));
			const row = view.rows[rowIndex];
			const current = context.state.pageIndex;
			const index = current >= row.start && current <= row.end ? current : row.start;
			return { index, row: rowIndex, offset: top - row.top };
		},

		releaseFlowShell(shell, index) {
			if (shell.contains(document.activeElement) || context.canvasEditor.drag?.item?.index === index || context.canvasEditor.drag?.page?.index === index) return false;
			pageView.unmountPage(index);
			if (context.state.selectedPages.has(index)) return false;
			context.state.pageObserver?.unobserve(shell);
			context.state.visiblePages.delete(index);
			return true;
		},

		createFlowShell(page) {
			const shell = document.createElement("div");
			shell.className = "page-shell";
			shell.dataset.pageIndex = String(page.index);
			shell.dataset.spreadCol = String(page.index % 2);
			const pageCount = context.state.doc?.pageCount || 0;
			if (context.state.pageLayout > 1 && page.index % 2 === 0 && page.index + 1 < pageCount) shell.dataset.spreadPair = "1";
			else delete shell.dataset.spreadPair;
			const surface = document.createElement("div");
			surface.className = "page-surface";
			const placeholder = document.createElement("div");
			placeholder.className = "page-placeholder";
			placeholder.textContent = `第${page.index + 1}页`;
			shell.append(surface, placeholder);
			pageView.layoutPageShell(shell, page);
			return shell;
		},

		resetViewState() {
			heightFitWidthLimit = 0;
			appliedHeightFitWidthLimit = 0;
			widthFitTarget = 0;
			appliedWidthFitTarget = 0;
			context.el.viewerPanel.classList.remove("single-page-fits-height");
			context.el.pageFrame.style.removeProperty("--fit-space");
			context.el.pageFrame.style.removeProperty("--fit-gap");
			context.el.svgHost.style.width = "";
			context.el.svgHost.style.height = "";
		},

		renderPageFlow() {
			context.el.svgHost.replaceChildren();
			context.el.svgHost.style.width = "";
			context.el.svgHost.style.height = "";
			context.state.pageWindow = null;
			context.state.visiblePages.clear();
			context.el.viewerPanel.classList.remove("single-page-fits-height");
			if (!context.state.doc) {
				return;
			}
			if (context.state.pageObserver) {
				context.state.pageObserver.disconnect();
			}
			context.el.emptyState.hidden = true;
			context.el.pageFrame.hidden = false;
			context.state.pageWindow = pageView.createPageWindow(context.el.svgHost, context.el.viewerPanel, 600, pageView.createFlowShell, pageView.releaseFlowShell, pageView.observeFlowPage);
			pageView.layoutPages();
		},

		resetPageFlow(keepCache = false) {
			context.state.pageNavigation = null;
			context.state.selectedPages.clear();
			context.state.documentSelection = null;
			context.state.visiblePages.clear();
			context.state.thumbnailWindow = null;
			context.state.pageListCurrent = null;
			context.state.pageListPair = -1;
			context.el.pageList.removeAttribute("data-page-selection");
			context.el.pageList.replaceChildren();
			context.el.pageList.style.height = "";
			context.el.pageList.classList.remove("virtual-pages", "grouped-pages");
			pageView.resetViewState();
			pageView.resetPageLoading();
			context.state.detachedPages.clear();
			if (!keepCache) {
				context.state.pageCache.clear();
				context.state.thumbCache.clear();
				pageView.releasePageResources();
			}
		},

		cancelPageTask(task) {
			if (!task) return;
			task.canceled = true;
			task.controller?.abort();
			if (task.requestID) {
				context.wasmWorker?.postMessage({ type: "cancel", id: task.requestID });
				task.requestID = 0;
			}
			const state = context.state;
			if (state.pageInFlight.get(task.key) === task) state.pageInFlight.delete(task.key);
			task.resolve(null);
		},

		resetPageLoading() {
			const state = context.state;
			state.pageLoadSeq++;
			context.releaseMediaPlayers();
			if (state.pagePresentFrame) cancelAnimationFrame(state.pagePresentFrame);
			state.pagePresentFrame = 0;
			if (state.viewerScrollTimer) { clearTimeout(state.viewerScrollTimer); state.viewerScrollTimer = 0; }
			if (state.thumbScrollTimer) { clearTimeout(state.thumbScrollTimer); state.thumbScrollTimer = 0; }
			if (state.revealThumbTimer) { clearTimeout(state.revealThumbTimer); state.revealThumbTimer = 0; }
			state.viewerFastScroll = false;
			state.thumbFastScroll = false;
			for (const task of state.pagePresentQueue.values()) task.resolve(null);
			state.pagePresentQueue.clear();
			for (const [id, request] of context.wasmRequests) {
				if (request.tab === context.documentTab() && request.name === "ofdgoRenderPage") context.wasmWorker?.postMessage({ type: "cancel", id });
			}
			for (const task of state.pageInFlight.values()) {
				task.canceled = true;
				task.controller?.abort();
				task.resolve(null);
			}
			state.pageRenderQueue = [];
			state.pageRenderScheduleSeq = (state.pageRenderScheduleSeq || 0) + 1;
			state.pageRenderRunning = state.pageRenderWorkers > 0;
			state.pagePrefetchIndex = null;
			state.pagePrefetchDirection = 1;
			state.pageInFlight.clear();
			pageView.resetThumbnails();
			if (state.pageObserver) {
				state.pageObserver.disconnect();
				state.pageObserver = null;
			}
		},

		observeFlowPage(shell) {
			if (!shell) return;
			const observer = pageView.flowPageObserver();
			if (observer) {
				observer.observe(shell);
			} else {
				const index = Number(shell.dataset.pageIndex);
				context.state.visiblePages.add(index);
				pageView.renderFlowPage(index, { priority: 4 });
			}
		},

		flowPageObserver() {
			if (!("IntersectionObserver" in window)) {
				return null;
			}
			if (!context.state.pageObserver) {
				const openSeq = context.state.openSeq;
				context.state.pageObserver = new IntersectionObserver((entries) => {
					if (openSeq !== context.state.openSeq) {
						return;
					}
					for (const entry of entries) {
						const index = Number.parseInt(entry.target.dataset.pageIndex, 10);
						if (context.state.pageWindow && context.state.pageWindow.nodes.get(index) !== entry.target) continue;
						if (!entry.isIntersecting) {
							context.state.visiblePages.delete(index);
							pageView.unmountPage(index);
							continue;
						}
						context.state.visiblePages.add(index);
						context.state.selectedPages.delete(index);
						if (!context.state.viewerFastScroll) {
							pageView.renderFlowPage(index, { openSeq, priority: 2 });
						} else if (context.state.detachedPages.has(index) && context.state.pageCache.has(index)) {
							pageView.mountPageSVG(index, context.state.pageCache.get(index), openSeq);
						}
					}
					pageView.trimPageCache();
				}, {
					root: context.el.viewerPanel,
					rootMargin: "600px 0px",
				});
			}
			return context.state.pageObserver;
		},

		unmountPage(index) {
			const shell = pageView.pageShell(index);
			if (!shell) return;
			if (context.state.editing && shell.classList.contains("rendered")) {
				const bounds = shell.getBoundingClientRect(), viewport = context.el.viewerPanel.getBoundingClientRect();
				const margin = context.state.pageWindow?.overscan ?? 600;
				const bottom = Math.max(bounds.bottom, bounds.top + shell.scrollHeight);
				if (bottom >= viewport.top - margin && bounds.top <= viewport.bottom + margin) {
					context.state.selectedPages.add(index);
					return;
				}
			}
			const selection = document.getSelection();
			const range = !selection.isCollapsed && selection.rangeCount ? selection.getRangeAt(0) : null;
			const endpointShell = node => (node?.nodeType === 1 ? node : node?.parentElement)?.closest?.(".page-shell");
			const selectedEndpoint = range && (!context.state.documentSelection || !context.isDocumentSelection(range))
				&& (endpointShell(range.startContainer) === shell || endpointShell(range.endContainer) === shell);
			if (context.canvasEditor.input?.item.index === index || context.canvasEditor.crop?.item.index === index || selectedEndpoint) {
				context.state.selectedPages.add(index);
				return;
			}
			const surface = shell.querySelector(".page-surface");
			const hasMedia = !!surface.querySelector("audio, video");
			context.releaseMediaPlayers(surface);
			const cachedPage = context.state.pageCache.get(index);
			if (!context.state.editing && !hasMedia && shell.classList.contains("rendered") && cachedPage && !cachedPage.previewOnly) {
				const svg = surface.querySelector(":scope > .ofd-svg");
				if (svg) {
					const fragment = document.createDocumentFragment();
					for (const child of [...surface.childNodes]) {
						if (child.classList?.contains("search-highlight") || child.classList?.contains("region-highlight")) child.remove();
						else fragment.append(child);
					}
					const detached = context.state.detachedPages;
					detached.delete(index);
					detached.set(index, { openSeq: context.state.openSeq, page: cachedPage, fragment, textPending: shell.dataset.textPending === "1" });
					while (detached.size > DETACHED_DOM_LIMIT) {
						detached.delete(detached.keys().next().value);
					}
				} else {
					surface.replaceChildren();
				}
			} else {
				surface.replaceChildren();
			}
			delete shell.dataset.textPending;
			shell.classList.remove("rendered");
			context.state.selectedPages.delete(index);
		},

		async renderFlowPage(index, options = {}) {
			const openSeq = options.openSeq || context.state.openSeq;
			const loadSeq = context.state.pageLoadSeq;
			const current = () => openSeq === context.state.openSeq && loadSeq === context.state.pageLoadSeq;
			if (!current()) {
				return null;
			}
			try {
				const page = await pageView.loadPageData(index, { openSeq, priority: options.priority ?? 2, view: true,
					refresh: context.state.editing && context.state.pageCache.get(index)?.previewOnly });
				if (!current()) {
					return null;
				}
				if (page) {
					await pageView.queuePagePresentation(index, page, openSeq, true);
				}
				if (!current()) return null;
				pageView.trimPageCache();
				return page;
			} catch (err) {
				if (err?.name === "AbortError") return null;
				if (current()) {
					pageView.markFlowPageError(index, err);
				}
				if (options.throwError && current()) {
					throw err;
				}
				return null;
			}
		},

		loadPageData(index, options = {}) {
			const openSeq = options.openSeq || context.state.openSeq;
			const state = context.state;
			if (openSeq !== state.openSeq) {
				return Promise.resolve(null);
			}
			const key = `${openSeq}:${index}`;
			const priority = options.priority ?? 3;
			const preview = !!options.preview;
			if (!options.refresh) {
				const cached = state.pageCache.get(index);
				if (cached && (preview || !cached.previewOnly)) {
					state.pageCache.delete(index);
					state.pageCache.set(index, cached);
					return Promise.resolve(cached);
				}
				if (preview && state.thumbCache.has(index)) {
					const thumb = state.thumbCache.get(index);
					state.thumbCache.delete(index);
					state.thumbCache.set(index, thumb);
					return Promise.resolve(thumb);
				}
			}
			const current = state.pageInFlight.get(key);
			if (current) {
				if (current.preview && !preview) {
					if (!current.dispatched) {
						current.preview = false;
						current.priority = Math.min(current.priority, priority);
						current.view = current.view || !!options.view;
						current.refresh = current.refresh || !!options.refresh;
						pageView.schedulePageRender();
						return current.promise;
					}
					const previousResolve = current.resolve;
					const previousReject = current.reject;
					current.resolve = () => {};
					current.reject = () => {};
					current.canceled = true;
					current.controller?.abort();
					if (current.requestID) {
						context.wasmWorker?.postMessage({ type: "cancel", id: current.requestID });
						current.requestID = 0;
					}
					state.pageInFlight.delete(key);
					const upgraded = pageView.loadPageData(index, options);
					upgraded.then(previousResolve, previousReject);
					return upgraded;
				}
				current.priority = Math.min(current.priority, priority);
				current.view = current.view || !!options.view;
				return current.promise;
			}
			let resolve;
			let reject;
			const promise = new Promise((done, fail) => {
				resolve = done;
				reject = fail;
			});
			const task = {
				key, index, openSeq, priority, view: !!options.view, preview, refresh: !!options.refresh,
				dispatched: false, canceled: false, requestID: 0, controller: null, resolve, reject, promise,
			};
			state.pageInFlight.set(key, task);
			state.pageRenderQueue.push(task);
			pageView.schedulePageRender();
			return promise;
		},

		schedulePageRender() {
			if (context.state.pageRenderRunning) {
				if (context.state.pageRenderWorkers) {
					while (context.state.pageRenderQueue.length && context.state.pageRenderWorkers < (nativeHost?.parallelism || context.wasmWorker?.parallelism || 1)) pageView.processPageRenderTasks();
				}
				return;
			}
			const tab = context.documentTab(), openSeq = tab.state.openSeq, loadSeq = tab.state.pageLoadSeq;
			const scheduleSeq = (tab.state.pageRenderScheduleSeq || 0) + 1;
			tab.state.pageRenderScheduleSeq = scheduleSeq;
			tab.state.pageRenderRunning = true;
			queueMicrotask(() => {
				if (scheduleSeq !== tab.state.pageRenderScheduleSeq || openSeq !== tab.state.openSeq || loadSeq !== tab.state.pageLoadSeq) return;
				if (context.activeTab === tab) pageView.processPageRenderQueue(tab, openSeq, loadSeq, scheduleSeq);
				else tab.state.pageRenderRunning = tab.state.pageRenderWorkers > 0;
			});
		},

		async processPageRenderQueue(tab = context.documentTab(), openSeq = tab.state.openSeq, loadSeq = tab.state.pageLoadSeq, scheduleSeq = tab.state.pageRenderScheduleSeq) {
			if (scheduleSeq !== tab.state.pageRenderScheduleSeq || openSeq !== tab.state.openSeq || loadSeq !== tab.state.pageLoadSeq) return;
			if (context.activeTab !== tab) {
				tab.state.pageRenderRunning = tab.state.pageRenderWorkers > 0;
				return;
			}
			const available = (nativeHost?.parallelism || context.wasmWorker?.parallelism || 1) - tab.state.pageRenderWorkers;
			if (available <= 0) {
				tab.state.pageRenderRunning = tab.state.pageRenderWorkers > 0;
				return;
			}
			await Promise.all(Array.from({ length: available }, () => pageView.processPageRenderTasks()));
		},

		async processPageRenderTasks() {
			const tab = context.documentTab(), state = tab.state;
			state.pageRenderWorkers++;
			try {
				while (context.activeTab === tab && state.pageRenderQueue.length) {
					let next = 0;
					for (let index = 1; index < state.pageRenderQueue.length; index++) {
						if (pageView.comparePageRenderTask(state.pageRenderQueue[index], state.pageRenderQueue[next]) < 0) next = index;
					}
					const [task] = state.pageRenderQueue.splice(next, 1);
					try {
						if (task.canceled || task.openSeq !== state.openSeq) {
							task.resolve(null);
							continue;
						}
						if (!task.refresh) {
							const cached = state.pageCache.get(task.index);
							if (cached && (task.preview || !cached.previewOnly)) {
								task.resolve(cached);
								continue;
							}
							if (task.preview && state.thumbCache.has(task.index)) {
								task.resolve(state.thumbCache.get(task.index));
								continue;
							}
						}
						task.dispatched = true;
						task.controller = new AbortController();
						const priority = pageView.pageRenderPriority(task, state);
						const args = [task.index, state.renderBackend, state.renderDPI, pageView.displayMode() === "raster",
							[...state.svgFonts.keys()], [...state.svgImages.keys()], task.preview, priority];
						const page = await context.callRenderPage(args, task);
						const releaseSvgUrl = url => {
							if (!url) return;
							if (url.startsWith("blob:")) URL.revokeObjectURL(url);
							else nativeHost?.call("release", { svgUrl: url }).catch(() => {});
						};
						if (task.canceled || context.activeTab !== tab || state.pageInFlight.get(task.key) !== task || task.openSeq !== state.openSeq) {
							releaseSvgUrl(page?.svgUrl);
							task.resolve(null);
							continue;
						}
						if (page.svgUrl) {
							const svgUrl = page.svgUrl;
							let consumed = false;
							try {
								const response = await fetch(svgUrl, { signal: task.controller.signal });
								if (task.canceled || context.activeTab !== tab || state.pageInFlight.get(task.key) !== task || task.openSeq !== state.openSeq) {
									response.body?.cancel?.().catch(() => {});
									task.resolve(null);
									continue;
								}
								if (!response.ok) throw new Error("读取页面矢量失败");
								page.svg = await response.text();
								consumed = true;
							} finally {
								if (!consumed || svgUrl.startsWith("blob:")) releaseSvgUrl(svgUrl);
								delete page.svgUrl;
							}
						} else if (typeof page.svg !== "string") {
							page.svg = "";
						}
						if (task.canceled || context.activeTab !== tab || state.pageInFlight.get(task.key) !== task || task.openSeq !== state.openSeq) {
							task.resolve(null);
							continue;
						}
						if (state.editing && !task.preview) await context.loadEditorMembers(tab, page, task.index);
						if (task.canceled || context.activeTab !== tab || state.pageInFlight.get(task.key) !== task || task.openSeq !== state.openSeq) {
							task.resolve(null);
							continue;
						}
						await pageView.loadSVGFonts(page.fonts || [], task.openSeq, tab, state);
						for (const font of page.fonts || []) delete font.data;
						if (task.canceled || context.activeTab !== tab || task.openSeq !== state.openSeq || state.pageInFlight.get(task.key) !== task) {
							task.resolve(null);
							continue;
						}
						const images = page.images || [];
						for (const image of images) {
							if (image.url) {
								const existing = state.svgImages.get(image.name);
								if (existing && !existing.native) URL.revokeObjectURL(existing.url);
								state.svgImages.set(image.name, { url: image.url, size: image.size || 0, native: true });
							} else if (image.bytes && !state.svgImages.has(image.name)) {
								const blob = new Blob([image.bytes], { type: image.mime });
								state.svgImages.set(image.name, { url: URL.createObjectURL(blob), size: blob.size, native: false });
							}
						}
						page.imageNames = images.map((image) => image.name);
						delete page.images;
						if (task.refresh && state.editing) {
							await Promise.all(page.imageNames.map(name => {
								const entry = state.svgImages.get(name);
								if (!entry) return Promise.resolve();
								const image = new Image();
								image.src = entry.url;
								return image.decode().catch(() => {});
							}));
						}
						if (task.canceled || context.activeTab !== tab || task.openSeq !== state.openSeq || state.pageInFlight.get(task.key) !== task) {
							task.resolve(null);
							continue;
						}
						const rawTextLen = typeof page.text === "string" ? page.text.length : 0;
						const rawAnnLen = typeof page.annotations === "string" ? page.annotations.length : 0;
						if (typeof page.text === "string") page.text = page.text ? JSON.parse(page.text) : { runs: [] };
						else if (!page.text) page.text = { runs: [] };
						if (typeof page.annotations === "string") page.annotations = page.annotations ? JSON.parse(page.annotations) : [];
						else if (!page.annotations) page.annotations = [];
						page.cacheBytes = (page.svg.length + rawTextLen + rawAnnLen) * 2
							+ (rawTextLen ? 0 : (page.text.runs?.length || 0) * 128)
							+ (rawAnnLen ? 0 : page.annotations.length * 128);
						if (typeof page.hasOpenAction === "boolean") {
							context.setPageOpenAction?.(task.index, page.hasOpenAction, !task.preview);
						}
						if (task.preview) {
							page.previewOnly = true;
							state.thumbCache.delete(task.index);
							state.thumbCache.set(task.index, page);
						} else {
							page.previewOnly = false;
							state.thumbCache.delete(task.index);
							state.detachedPages.delete(task.index);
							state.pageCache.delete(task.index);
							state.pageCache.set(task.index, page);
						}
						task.resolve(page);
					} catch (err) {
						if (task.canceled || err?.name === "AbortError") task.resolve(null);
						else task.reject(err);
					} finally {
						task.requestID = 0;
						if (state.pageInFlight.get(task.key) === task) state.pageInFlight.delete(task.key);
					}
				}
			} finally {
				if (--state.pageRenderWorkers === 0) {
					state.pageRenderRunning = false;
					if (!state.pageInFlight.size) pageView.releasePageResources(state);
					if (context.activeTab === tab && state.pageRenderQueue.length) pageView.schedulePageRender();
				}
			}
		},

		trimPageCache() {
			const state = context.state;
			const protectedPages = new Set([state.pageIndex, ...state.visiblePages, ...state.selectedPages]);
			for (const [index, task] of state.pagePresentQueue) {
				if (task.shell) protectedPages.add(index);
			}
			for (const index of state.pageWindow?.nodes.keys() || []) protectedPages.add(index);
			for (const task of state.pageInFlight.values()) {
				if (!task.preview) protectedPages.add(task.index);
			}
			const protectedThumbs = new Set([state.pageIndex, ...state.visibleThumbnails]);
			for (const [index, task] of state.pagePresentQueue) {
				if (task.thumb) protectedThumbs.add(index);
			}
			for (const index of state.thumbnailWindow?.nodes.keys() || []) protectedThumbs.add(index);
			for (const task of state.pageInFlight.values()) {
				if (task.preview) protectedThumbs.add(task.index);
			}
			const references = new Map(), pageImageRefs = new Map(), fontReferences = new Map();
			let pageBytes = 0, thumbBytes = 0, thumbExclusiveImageBytes = 0;
			const countRefs = (page, isPage) => {
				for (const name of page.imageNames || []) {
					references.set(name, (references.get(name) || 0) + 1);
					if (isPage) pageImageRefs.set(name, (pageImageRefs.get(name) || 0) + 1);
				}
				for (const font of page.fonts || []) fontReferences.set(font.name, (fontReferences.get(font.name) || 0) + 1);
			};
			for (const page of state.pageCache.values()) {
				pageBytes += page.cacheBytes || 0;
				countRefs(page, true);
			}
			for (const page of state.thumbCache.values()) {
				thumbBytes += page.cacheBytes || 0;
				countRefs(page, false);
			}
			let sharedBytes = 0;
			for (const [name, image] of state.svgImages) {
				const size = image.size || 0;
				sharedBytes += size;
				if (!pageImageRefs.has(name) && references.has(name)) thumbExclusiveImageBytes += size;
			}
			for (const face of state.svgFonts.values()) sharedBytes += svgFontBytes.get(face) || 0;
			const releaseUnused = (page, fromThumb = false) => {
				for (const name of page.imageNames || []) {
					const count = (references.get(name) || 0) - 1;
					references.set(name, count);
					if (count <= 0) {
						const image = state.svgImages.get(name);
						if (image) {
							if (fromThumb && !pageImageRefs.has(name)) thumbExclusiveImageBytes = Math.max(0, thumbExclusiveImageBytes - (image.size || 0));
							if (!state.pageInFlight.size) {
								sharedBytes -= image.size || 0;
								if (!image.native) URL.revokeObjectURL(image.url);
								state.svgImages.delete(name);
							}
						}
					}
				}
				for (const font of page.fonts || []) {
					const count = (fontReferences.get(font.name) || 0) - 1;
					fontReferences.set(font.name, count);
					if (count <= 0 && !state.pageInFlight.size) {
						const face = state.svgFonts.get(font.name);
						if (face) {
							sharedBytes -= svgFontBytes.get(face) || 0;
							document.fonts.delete(face);
							state.svgFonts.delete(font.name);
						}
					}
				}
			};
			const budget = nativeHost?.cacheBytes || PAGE_CACHE_BYTES;
			const thumbBudget = Math.max(THUMB_CACHE_BYTES, Math.min(48 << 20, Math.floor(budget / 4)));
			for (const [index, page] of state.thumbCache) {
				if (state.thumbCache.size <= THUMB_CACHE_LIMIT && thumbBytes + thumbExclusiveImageBytes <= thumbBudget) break;
				if (protectedThumbs.has(index)) continue;
				state.thumbCache.delete(index);
				thumbBytes -= page.cacheBytes || 0;
				releaseUnused(page, true);
			}
			const pageLimit = Math.round(PAGE_CACHE_LIMIT * Math.max(1, budget / PAGE_CACHE_BYTES));
			for (const [index, page] of state.pageCache) {
				if (state.pageCache.size <= pageLimit && pageBytes + sharedBytes <= budget) break;
				if (protectedPages.has(index)) continue;
				state.pageCache.delete(index);
				state.detachedPages.delete(index);
				pageBytes -= page.cacheBytes || 0;
				releaseUnused(page);
			}
			if (!state.pageInFlight.size) pageView.releasePageResources();
			pageView.trimWorkspaceCaches();
		},

		async loadSVGFonts(fonts, openSeq, tab = context.activeTab, state = tab?.state || context.state) {
			const loadSeq = state.pageLoadSeq;
			const current = () => tab === context.activeTab && state === context.state && openSeq === state.openSeq && loadSeq === state.pageLoadSeq;
			if (!current()) return;
			for (let offset = 0; offset < fonts.length && current(); offset += 4) {
				const results = await Promise.allSettled(fonts.slice(offset, offset + 4).map(async font => {
					if (!current() || state.svgFonts.has(font.name)) return;
					const key = `${openSeq}:${loadSeq}:${font.name}`;
					while (current() && !state.svgFonts.has(font.name) && !state.svgFontLoads.has(key) && state.svgFontLoads.size >= 4) {
						await Promise.race([...state.svgFontLoads.values()].map(loading => loading.catch(() => {})));
					}
					if (!current() || state.svgFonts.has(font.name)) return;
					let loading = state.svgFontLoads.get(key);
					if (!loading) {
						loading = pageView.loadSVGFont(font, openSeq, loadSeq, tab, state);
						state.svgFontLoads.set(key, loading);
					}
					try { await loading; }
					finally { if (state.svgFontLoads.get(key) === loading) state.svgFontLoads.delete(key); }
				}));
				if (!current()) return;
				const failed = results.find(result => result.status === "rejected");
				if (failed) throw failed.reason;
			}
		},

		async loadSVGFont(font, openSeq, loadSeq = context.state.pageLoadSeq, tab = context.activeTab, state = tab?.state || context.state) {
			const current = () => tab === context.activeTab && state === context.state && openSeq === state.openSeq && loadSeq === state.pageLoadSeq;
			if (!current()) return;
			let face;
			let byteSize = font.size || 0;
			if (font.url) {
				face = new FontFace(font.name, `url("${font.url}")`, { weight: String(font.weight), style: font.style });
			} else {
				const data = font.data ? { bytes: font.data } : await context.callWASM("ofdgoSVGFontData", font.name);
				if (!current()) {
					return;
				}
				if (data?.url) {
					byteSize = data.size || 0;
					face = new FontFace(font.name, `url("${data.url}")`, { weight: String(font.weight), style: font.style });
				} else {
					byteSize = data?.bytes?.byteLength || 0;
					face = new FontFace(font.name, data?.bytes, { weight: String(font.weight), style: font.style });
				}
			}
			await face.load();
			if (!current()) {
				return;
			}
			document.fonts.add(face);
			svgFontBytes.set(face, byteSize);
			state.svgFonts.set(font.name, face);
		},

		comparePageRenderTask(a, b) {
			return pageView.pageRenderPriority(a) - pageView.pageRenderPriority(b) || Math.abs(a.index - context.state.pageIndex) - Math.abs(b.index - context.state.pageIndex);
		},

		pageRenderPriority(task, state = context.state) {
			if (!task.view || task.refresh) return task.priority;
			if (task.index === state.pageIndex) return 0;
			if (Math.floor(task.index / state.pageLayout) === Math.floor(state.pageIndex / state.pageLayout)) return 1;
			if (state.visiblePages.has(task.index)) return 2;
			if (!task.preview) {
				if (state.pageWindow?.nodes.has(task.index)) return 3;
				return 4;
			}
			if (state.visibleThumbnails.has(task.index) || state.thumbnailWindow?.nodes.has(task.index)) return 5;
			return 6;
		},

		getThumbnailSVG(index, state = context.state) {
			return state.pageCache.get(index)?.svg || state.thumbCache.get(index)?.svg || "";
		},

		queuePagePresentation(index, page, openSeq, flow = false) {
			const state = context.state;
			const inPage = state.pageCache.get(index) === page;
			const inThumb = state.thumbCache.get(index) === page;
			if (openSeq !== state.openSeq || (!inPage && !inThumb)) return Promise.resolve(null);
			let shell = flow && inPage ? pageView.pageShell(index) : null;
			let thumb = state.thumbnailWindow?.nodes.get(index)?.querySelector(".thumb-paper") || null;
			if (shell?.classList.contains("rendered") || state.pageObserver && !state.visiblePages.has(index) && index !== state.pageIndex) shell = null;
			if (thumb?.dataset.renderKey === `${openSeq}:${index}` || state.thumbnailObserver && !state.visibleThumbnails.has(index) && index !== state.pageIndex) thumb = null;
			if (!shell && !thumb) return Promise.resolve(null);
			const queue = state.pagePresentQueue, current = queue.get(index);
			if (current?.page === page) {
				if (shell) { current.shell = shell; current.preview = false; }
				if (thumb) current.thumb = thumb;
				return current.promise;
			}
			current?.resolve(null);
			let resolve, reject;
			const promise = new Promise((done, fail) => { resolve = done; reject = fail; });
			queue.set(index, { index, page, openSeq, shell, thumb, view: true, preview: !shell, resolve, reject, promise });
			pageView.schedulePagePresentation();
			return promise;
		},

		schedulePagePresentation() {
			const tab = context.documentTab(), state = tab.state, openSeq = state.openSeq, loadSeq = state.pageLoadSeq;
			if (state.pagePresentFrame || !state.pagePresentQueue.size) return;
			state.pagePresentFrame = requestAnimationFrame(() => {
				state.pagePresentFrame = 0;
				if (context.activeTab !== tab || openSeq !== state.openSeq || loadSeq !== state.pageLoadSeq) return;
				const started = performance.now();
				do {
					const task = [...state.pagePresentQueue.values()].sort(pageView.comparePageRenderTask)[0];
					try {
						const inPage = state.pageCache.get(task.index) === task.page;
						const inThumb = state.thumbCache.get(task.index) === task.page;
						if (inPage || inThumb) {
							if (inPage && task.shell && pageView.pageShell(task.index) === task.shell && !task.shell.classList.contains("rendered")
								&& (!state.pageObserver || state.visiblePages.has(task.index) || task.index === state.pageIndex)) {
								const detached = !state.editing ? state.detachedPages.get(task.index) : null;
								if (detached && detached.openSeq === task.openSeq && detached.page === task.page) {
									pageView.mountPageSVG(task.index, task.page, task.openSeq, null);
								} else if (state.viewerFastScroll && !state.editing) {
									pageView.mountPageSVG(task.index, task.page, task.openSeq, false);
								} else {
									task.text ||= pageView.createTextLayerParts(task.page.text);
									let text;
									do { text = task.text.next(); } while (!text.done && performance.now() - started < 6);
									if (!text.done) break;
									pageView.mountPageSVG(task.index, task.page, task.openSeq, text.value);
								}
							}
							if (task.thumb && state.thumbnailWindow?.nodes.get(task.index)?.querySelector(".thumb-paper") === task.thumb) {
								pageView.updateThumbnail(task.index, task.openSeq);
							}
						}
						task.resolve(null);
					} catch (err) { task.reject(err); }
					state.pagePresentQueue.delete(task.index);
				} while (state.pagePresentQueue.size && performance.now() - started < 6);
				pageView.schedulePagePresentation();
			});
		},

		mountPageSVG(index, page, openSeq = context.state.openSeq, textLayer = null) {
			if (openSeq !== context.state.openSeq) {
				return;
			}
			const shell = pageView.pageShell(index);
			if (!shell) {
				return;
			}
			const surface = shell.querySelector(".page-surface");
			const detached = !context.state.editing ? context.state.detachedPages.get(index) : null;
			if (detached && detached.openSeq === openSeq && detached.page === page) {
				context.state.detachedPages.delete(index);
				surface.replaceChildren(detached.fragment);
				if (detached.textPending) {
					if (context.state.viewerFastScroll) {
						shell.dataset.textPending = "1";
					} else {
						delete shell.dataset.textPending;
						const svg = surface.querySelector(":scope > .ofd-svg");
						const layer = textLayer || pageView.createTextLayer(page.text);
						if (svg?.nextSibling) surface.insertBefore(layer, svg.nextSibling);
						else surface.append(layer);
					}
				} else {
					delete shell.dataset.textPending;
				}
				shell.classList.add("rendered");
				context.renderSearchHighlights(index);
				const info = pageView.pageInfo(index);
				if (info) pageView.layoutPageShell(shell, info);
				return;
			}
			const svg = pageView.parseSVG(page.svg, `p${openSeq}-${index}`);
			svg.classList.add("ofd-svg");
			surface.replaceChildren(svg);
			if (textLayer === false && !context.state.editing) {
				shell.dataset.textPending = "1";
			} else {
				delete shell.dataset.textPending;
				surface.append(textLayer || pageView.createTextLayer(page.text));
			}
			if (context.state.editing && context.state.editorInfo && pageView.displayMode() === "svg") {
				context.mountEditorObjects(index, page, surface);
			}
			context.mountAnnotationNotes(index, surface, page.annotations);
			context.mountPageLinks(page, surface);
			shell.classList.add("rendered");
			context.renderSearchHighlights(index);
			const info = pageView.pageInfo(index);
			if (info) {
				pageView.layoutPageShell(shell, info);
			}
		},

		hydratePageTextLayer(index, shell = pageView.pageShell(index)) {
			const state = context.state;
			if (!state.doc || state.editing || !shell || shell.dataset.textPending !== "1") return false;
			const page = state.pageCache.get(index);
			if (!page) return false;
			const surface = shell.querySelector(".page-surface");
			if (!surface || surface.querySelector(":scope > .text-layer")) {
				delete shell.dataset.textPending;
				return true;
			}
			const svg = surface.querySelector(":scope > .ofd-svg");
			const layer = pageView.createTextLayer(page.text);
			if (svg?.nextSibling) surface.insertBefore(layer, svg.nextSibling);
			else surface.append(layer);
			delete shell.dataset.textPending;
			context.renderSearchHighlights(index);
			return true;
		},

		hydrateTargetTextLayer(event) {
			if (!context.state.doc || context.state.editing) return;
			const shell = event.target?.closest?.(".page-shell");
			if (shell?.dataset.textPending === "1") pageView.hydratePageTextLayer(Number(shell.dataset.pageIndex), shell);
		},

		hydratePendingTextLayers() {
			const state = context.state;
			if (!state.doc || state.viewerFastScroll || state.editing) return;
			const nodes = state.pageWindow?.nodes;
			if (!nodes) return;
			for (const [index, shell] of nodes) {
				if (shell.dataset.textPending === "1") pageView.hydratePageTextLayer(index, shell);
			}
		},

		markFlowPageError(index, err) {
			const shell = pageView.pageShell(index);
			if (!shell) {
				return;
			}
			shell.classList.add("error");
			const placeholder = shell.querySelector(".page-placeholder");
			if (placeholder) {
				placeholder.textContent = String(err.message || err);
			}
		},

		queueNearbyPages(index, openSeq = context.state.openSeq) {
			const state = context.state;
			if (!state.doc || state.viewerFastScroll) return;
			const layout = state.pageLayout;
			const start = pageView.pageGroupStart(index);
			if (state.pagePrefetchIndex !== null && start !== state.pagePrefetchIndex) state.pagePrefetchDirection = Math.sign(start - state.pagePrefetchIndex);
			const direction = state.pagePrefetchDirection || 1;
			state.pagePrefetchIndex = start;
			let bytes = 0;
			for (const page of state.pageCache.values()) bytes += page.cacheBytes || 0;
			for (const image of state.svgImages.values()) bytes += image.size || 0;
			for (const face of state.svgFonts.values()) bytes += svgFontBytes.get(face) || 0;
			const budget = nativeHost?.cacheBytes || PAGE_CACHE_BYTES;
			const cost = Math.max(512 << 10, bytes / Math.max(1, state.pageCache.size));
			const pageCount = state.doc.pageCount || 0;
			const maxAheadRows = layout > 1 ? 3 : 4;
			const maxAheadPages = maxAheadRows * layout;
			const parallelism = nativeHost?.parallelism || context.wasmWorker?.parallelism || 1;
			const budgetPages = Math.max(0, Math.floor((budget - bytes) / cost));
			const aheadCount = Math.min(maxAheadPages, Math.max(layout, parallelism * layout), budgetPages);
			for (let i = start; i < Math.min(pageCount, start + layout); i += 1) {
				pageView.renderFlowPage(i, { openSeq, priority: 1 });
			}
			for (let offset = 1; offset <= aheadCount; offset += 1) {
				const next = direction > 0 ? start + layout + offset - 1 : start - offset;
				if (next >= 0 && next < pageCount) pageView.renderFlowPage(next, { openSeq, priority: offset <= layout ? 3 : 4 });
			}
			if (budgetPages > aheadCount) {
				for (let offset = 1; offset <= layout; offset += 1) {
					const back = direction > 0 ? start - offset : start + layout + offset - 1;
					if (back >= 0 && back < pageCount) pageView.renderFlowPage(back, { openSeq, priority: 6 });
				}
			}
		},

		pageShell(index) {
			return context.state.pageWindow?.nodes.get(index) || null;
		},

		scrollToPage(index, options = {}) {
			const view = context.state.pageWindow;
			const viewer = context.el.viewerPanel;
			if (view && view.rows.length > 0) {
				const rowIndex = pageView.pageRowIndex(view, index);
				const row = view.rows[rowIndex];
				const viewportHeight = viewer.clientHeight;
				const viewportWidth = viewer.clientWidth;
				const blockSpace = pageView.pageBlockSpace();
				const space = context.state.fitMode === "height" && !context.state.continuous
					? Math.max(blockSpace, (viewportHeight - row.height) / 2)
					: blockSpace;
				pageView.syncPageWindow(view, row.top - space);
				const maxLeft = Math.max(0, viewer.scrollWidth - viewportWidth);
				if (context.state.pageLayout > 1) {
					const inlineSpace = pageView.pageSpace();
					if (row.width <= viewportWidth - inlineSpace * 2 + 1) {
						viewer.scrollLeft = maxLeft / 2;
					} else {
						const hostLeft = Math.max(inlineSpace, (viewer.scrollWidth - view.hostWidth) / 2);
						const pageLeft = hostLeft + (view.lefts?.[index] || 0);
						const pageWidth = view.widths?.[index] || row.width;
						const pageRight = pageLeft + pageWidth;
						const visibleLeft = viewer.scrollLeft;
						const visibleRight = visibleLeft + viewportWidth;
						const overlap = Math.max(0, Math.min(visibleRight, pageRight) - Math.max(visibleLeft, pageLeft));
						if (!(options.preserveInline && overlap >= Math.min(pageWidth, viewportWidth) * 0.5)) {
							const targetLeft = pageLeft + pageWidth / 2 - viewportWidth / 2;
							viewer.scrollLeft = Math.max(0, Math.min(maxLeft, targetLeft));
						}
					}
					const nav = context.state.pageNavigation;
					if (!nav) {
						context.state.pageNavigation = { openSeq: context.state.openSeq, index, top: view.logicalTop, left: viewer.scrollLeft, internal: true };
					} else if (nav.internal && nav.openSeq === context.state.openSeq && nav.index === index) {
						nav.top = view.logicalTop;
						nav.left = viewer.scrollLeft;
					}
					return;
				}
				viewer.scrollLeft = maxLeft / 2;
				const nav = context.state.pageNavigation;
				if (!nav) {
					context.state.pageNavigation = { openSeq: context.state.openSeq, index, top: view.logicalTop, left: viewer.scrollLeft, internal: true };
				} else if (nav.internal && nav.openSeq === context.state.openSeq && nav.index === index) {
					nav.top = view.logicalTop;
					nav.left = viewer.scrollLeft;
				}
				return;
			}
			const shell = pageView.pageShell(index);
			if (shell) {
				if (context.state.fitMode === "height" && !context.state.continuous) {
					shell.scrollIntoView({ block: "center", inline: "nearest" });
				} else {
					const viewerRect = viewer.getBoundingClientRect();
					const shellRect = shell.getBoundingClientRect();
					const top = viewer.scrollTop + shellRect.top - viewerRect.top - pageView.pageBlockSpace();
					const maxTop = Math.max(0, viewer.scrollHeight - viewer.clientHeight);
					viewer.scrollTop = Math.min(maxTop, Math.max(0, top));
				}
				pageView.centerPageInline(shell);
			}
		},

		centerPageInline(shell) {
			const viewerRect = context.el.viewerPanel.getBoundingClientRect();
			const shellRect = shell.getBoundingClientRect();
			context.el.viewerPanel.scrollLeft += shellRect.left + shellRect.width / 2 - viewerRect.left - context.el.viewerPanel.clientWidth / 2;
		},

		schedulePageSync() {
			const state = context.state;
			if (!state.doc) return;
			const now = performance.now();
			const scrollTop = context.el.viewerPanel.scrollTop;
			if (state.viewerLastScrollTime > 0) {
				const dt = Math.max(1, now - state.viewerLastScrollTime);
				const velocity = Math.abs(scrollTop - state.viewerLastScrollTop) / dt;
				if (velocity > FAST_SCROLL_VELOCITY) {
					state.viewerFastScroll = true;
				}
			}
			state.viewerLastScrollTop = scrollTop;
			state.viewerLastScrollTime = now;
			if (state.viewerFastScroll) {
				if (state.viewerScrollTimer) clearTimeout(state.viewerScrollTimer);
				const openSeq = state.openSeq, loadSeq = state.pageLoadSeq;
				state.viewerScrollTimer = setTimeout(() => {
					state.viewerScrollTimer = 0;
					if (openSeq !== context.state.openSeq || loadSeq !== context.state.pageLoadSeq) return;
					pageView.onViewerScrollSettled(openSeq);
				}, SCROLL_SETTLE_DELAY);
			}
			if (state.scrollFrame) {
				return;
			}
			state.scrollFrame = requestAnimationFrame(() => {
				state.scrollFrame = 0;
				pageView.syncCurrentPageFromScroll();
			});
		},

		onViewerScrollSettled(openSeq = context.state.openSeq) {
			const state = context.state;
			if (!state.doc || openSeq !== state.openSeq) return;
			state.viewerFastScroll = false;
			state.viewerLastScrollTime = 0;
			for (const index of state.visiblePages) {
				const shell = pageView.pageShell(index);
				if (shell && !shell.classList.contains("rendered")) {
					pageView.renderFlowPage(index, { openSeq, priority: 2 });
				}
			}
			pageView.hydratePendingTextLayers();
			pageView.queueNearbyPages(state.pageIndex, openSeq);
			pageView.prunePageRenderQueue();
		},

		syncCurrentPageFromScroll() {
			if (context.state.openActionTask?.navigating) return;
			pageView.syncPageWindow(context.state.pageWindow);
			const navigation = context.state.pageNavigation;
			const top = context.state.pageWindow ? context.state.pageWindow.logicalTop : context.el.viewerPanel.scrollTop;
			if (navigation?.openSeq === context.state.openSeq && navigation.index === context.state.pageIndex
				&& (navigation.top === undefined || Math.abs(navigation.top - top) < 1 && Math.abs(navigation.left - context.el.viewerPanel.scrollLeft) < 1)) return;
			context.state.pageNavigation = null;
			if (context.canvasEditor.input || context.canvasEditor.drag || context.canvasEditor.crop) return;
			const shell = pageView.pageShellFromView();
			if (!shell) {
				return;
			}
			const nextIndex = Number.parseInt(shell.dataset.pageIndex, 10);
			if (Number.isFinite(nextIndex) && nextIndex !== context.state.pageIndex) {
				pageView.setCurrentPage(nextIndex);
				if (!context.state.viewerFastScroll) pageView.queueNearbyPages(nextIndex);
				if (pageView.pageHasOpenAction(nextIndex)) context.scheduleOpenActions();
			}
			pageView.prunePageRenderQueue();
		},

		isPageProtectedFromPrune(index, preview, state = context.state) {
			if (preview) {
				if (index === state.pageIndex) return true;
				if (state.visibleThumbnails.has(index) || state.thumbnailWindow?.nodes.has(index)) return true;
				return false;
			}
			const layout = state.pageLayout;
			const start = pageView.pageGroupStart(state.pageIndex);
			const direction = state.pagePrefetchDirection || 1;
			const minProtected = direction > 0 ? start - layout * 2 : start - layout * 3;
			const maxProtected = direction > 0 ? start + layout * 4 - 1 : start + layout * 3 - 1;
			if (index >= minProtected && index <= maxProtected) return true;
			if (state.visiblePages.has(index) || state.pageWindow?.nodes.has(index)) return true;
			return false;
		},

		prunePageRenderQueue() {
			const state = context.state;
			state.pageRenderQueue = state.pageRenderQueue.filter(task => {
				if (!task.view || task.refresh || pageView.isPageProtectedFromPrune(task.index, task.preview, state)) return true;
				if (state.pageInFlight.get(task.key) === task) state.pageInFlight.delete(task.key);
				task.canceled = true;
				task.resolve(null);
				return false;
			});
			for (const task of [...state.pageInFlight.values()]) {
				if (!task.dispatched || !task.view || task.refresh) continue;
				if (pageView.isPageProtectedFromPrune(task.index, task.preview, state)) continue;
				pageView.cancelPageTask(task);
			}
		},

		pageShellFromView() {
			const view = context.state.pageWindow, count = context.state.doc?.pageCount || 0;
			if (!view || !count || view.rows.length < 1) {
				return pageView.pageShell(context.state.pageIndex);
			}
			const viewer = context.el.viewerPanel;
			const viewportHeight = viewer.clientHeight;
			const viewportWidth = viewer.clientWidth;
			const scrollLeft = viewer.scrollLeft;
			const inset = pageView.windowInset(view);
			const top = view.logicalTop ?? pageView.pageWindowTop(view);
			const bottom = top + viewportHeight;
			const atBottom = view.total + inset * 2 > viewportHeight + 1 && bottom >= view.total + inset - 1;
			const rows = view.rows;
			let rowIndex = 0;
			if (atBottom) {
				const currentRowIndex = pageView.pageRowIndex(view, context.state.pageIndex);
				const currentRow = rows[currentRowIndex];
				if (currentRow && currentRow.top >= top - 1 && currentRow.top + currentRow.height <= bottom + 1) {
					return pageView.pageShell(context.state.pageIndex);
				}
				rowIndex = rows.length - 1;
			} else if (context.state.continuous) {
				if (top <= 1) {
					rowIndex = 0;
				} else {
					const firstVisibleRow = pageView.virtualRowIndex(view, Math.max(0, top));
					const firstRow = rows[firstVisibleRow];
					const adaptiveOffset = firstRow ? Math.min(viewportHeight * 0.3, Math.max(24, firstRow.height * 0.45)) : viewportHeight * 0.3;
					const focusY = Math.max(0, Math.min(view.total, top + adaptiveOffset));
					rowIndex = pageView.virtualRowIndex(view, focusY);
					const lastVisibleRow = pageView.virtualRowIndex(view, Math.min(view.total, bottom));
					let bestRow = rowIndex;
					let bestVisible = 0;
					const focusRow = rows[rowIndex];
					if (focusRow) {
						bestVisible = Math.max(0, Math.min(bottom, focusRow.top + focusRow.height) - Math.max(top, focusRow.top));
					}
					for (let r = firstVisibleRow; r <= lastVisibleRow; r++) {
						const candidate = rows[r];
						const vis = Math.max(0, Math.min(bottom, candidate.top + candidate.height) - Math.max(top, candidate.top));
						if (vis > viewportHeight * 0.5 && vis > bestVisible + 1) {
							bestRow = r;
							bestVisible = vis;
						}
					}
					rowIndex = bestRow;
				}
			} else {
				let matched = -1;
				for (const ratio of [0.45, 0.25, 0.65]) {
					const y = top + viewportHeight * ratio;
					if (y < 0 || y > view.total) continue;
					const candidateIndex = pageView.virtualRowIndex(view, y);
					const candidate = rows[candidateIndex];
					if (candidate && y <= candidate.top + candidate.height) {
						matched = candidateIndex;
						break;
					}
				}
				if (matched >= 0) {
					rowIndex = matched;
				} else {
					const mid = Math.max(0, Math.min(view.total, top + viewportHeight * 0.45));
					rowIndex = pageView.virtualRowIndex(view, mid);
					const row = rows[rowIndex];
					if (row && mid > row.top + row.height && rowIndex + 1 < rows.length) {
						const nextRow = rows[rowIndex + 1];
						if (Math.abs(nextRow.top - mid) < Math.abs(mid - (row.top + row.height))) rowIndex++;
					}
				}
			}
			const row = rows[rowIndex];
			if (!row) return pageView.pageShell(context.state.pageIndex);
			if (context.state.pageLayout === 1 || row.start === row.end) {
				return pageView.pageShell(row.start);
			}
			const hostLeft = Math.max(pageView.pageSpace(), (viewer.scrollWidth - view.hostWidth) / 2);
			const current = context.state.pageIndex;
			const visibleAreaOf = (index) => {
				const pLeft = hostLeft + (view.lefts?.[index] || 0) - scrollLeft;
				const pRight = pLeft + (view.widths?.[index] || 0);
				const pTop = view.offsets[index] - top;
				const pBottom = pTop + (view.heights?.[index] || row.height);
				return Math.max(0, Math.min(viewportWidth, pRight) - Math.max(0, pLeft))
					* Math.max(0, Math.min(viewportHeight, pBottom) - Math.max(0, pTop));
			};
			if (current >= row.start && current <= row.end && visibleAreaOf(current) > 1) {
				return pageView.pageShell(current);
			}
			let bestIndex = row.start, bestArea = -1;
			for (let index = row.start; index <= row.end; index++) {
				const area = visibleAreaOf(index);
				if (area > bestArea + 1) { bestIndex = index; bestArea = area; }
			}
			return pageView.pageShell(bestIndex);
		},

		setCurrentPage(index) {
			if (index !== context.state.pageIndex && !context.state.openActionTask?.navigating) context.state.openActionTask = null;
			context.state.pageIndex = index;
			pageView.prunePageRenderQueue();
			if (context.state.editing) context.state.openActionPage = pageView.pageInfo(index)?.id;
			if (context.state.fitMode === "width" && context.state.pageLayout === 1) {
				const nextPage = pageView.currentPageInfo();
				if (nextPage) {
					context.state.scale = pageView.pageScale(nextPage);
					context.el.zoomLabel.textContent = `${Math.round(context.state.scale * 100)}%`;
				}
			} else if (context.state.fitMode === "height" && context.state.pageLayout === 1) {
				const nextPage = pageView.currentPageInfo();
				if (nextPage) {
					context.el.zoomLabel.textContent = `${Math.round(pageView.pageScale(nextPage) * 100)}%`;
				}
			}
			pageView.updatePageListCurrent();
			context.updateControls();
			if (context.state.doc && !context.state.exporting) {
				context.setStatus(context.pageStatus(index, context.state.doc.pageCount));
			}
		},

		pairPageIndex(index = context.state.pageIndex) {
			if (context.state.pageLayout <= 1 || !context.state.doc) return -1;
			const start = pageView.pageGroupStart(index);
			const other = index === start ? start + 1 : start;
			return other < context.state.doc.pageCount ? other : -1;
		},

		updatePageListCurrent(force = false) {
			const state = context.state;
			const index = state.pageIndex;
			const pairIndex = pageView.pairPageIndex(index);
			if (state.thumbnailWindow) {
				if (!force && state.pageListCurrent === index && state.pageListPair === pairIndex) return;
				state.pageListCurrent = index;
				state.pageListPair = pairIndex;
				if (state.showPages && !context.el.pageList.hidden) pageView.scheduleRevealThumbnail(index, force);
			}
			for (const item of context.el.pageList.querySelectorAll(".page-list-item[data-current], .page-list-item.active, .page-list-item[data-pair-current], .page-list-item.pair-active")) {
				const itemIndex = Number.parseInt(item.dataset.pageIndex, 10);
				if (itemIndex !== index) pageView.setPageItemCurrent(item, false);
				if (itemIndex !== pairIndex) pageView.setPageItemPairCurrent(item, false);
			}
			const next = state.thumbnailWindow?.nodes.get(index) || context.el.pageList.querySelector(`.page-list-item[data-page-index="${index}"]`);
			if (next) {
				pageView.setPageItemCurrent(next, true);
				if (state.showPages && !context.el.pageList.hidden && !state.thumbnailWindow) {
					const panel = context.el.navigationContent.getBoundingClientRect();
					const item = next.getBoundingClientRect();
					if (item.top < panel.top) {
						context.el.navigationContent.scrollTop += item.top - panel.top;
					} else if (item.bottom > panel.bottom) {
						context.el.navigationContent.scrollTop += Math.min(item.top - panel.top, item.bottom - panel.bottom);
					}
				}
			}
			if (pairIndex >= 0) {
				const pair = state.thumbnailWindow?.nodes.get(pairIndex) || context.el.pageList.querySelector(`.page-list-item[data-page-index="${pairIndex}"]`);
				if (pair) pageView.setPageItemPairCurrent(pair, true);
			}
		},

		setPageItemCurrent(item, current) {
			if (current) {
				item.setAttribute("data-current", "page");
				item.classList.add("active");
				return;
			}
			item.removeAttribute("data-current");
			item.classList.remove("active");
		},

		setPageItemPairCurrent(item, active) {
			if (active) {
				item.setAttribute("data-pair-current", "true");
				item.classList.add("pair-active");
				return;
			}
			item.removeAttribute("data-pair-current");
			item.classList.remove("pair-active");
		},

		pageGroupStart(index = context.state.pageIndex) {
			return Math.floor(index / context.state.pageLayout) * context.state.pageLayout;
		},

		pageGroupSize(index = context.state.pageIndex) {
			const start = pageView.pageGroupStart(index), widths = [0, 0];
			let height = 0, count = 0;
			for (let offset = 0; offset < context.state.pageLayout; offset++) {
				const page = pageView.pageInfo(start + offset);
				if (!page) continue;
				const size = pageView.pageViewSize(page);
				widths[offset] = size.width;
				height = Math.max(height, size.height);
				count++;
			}
			const gap = count > 1 && !context.state.continuous ? PAGE_LAYOUT_GAP : 0;
			return { widths, width: widths[0] + widths[1], height, count, gap };
		},

		async togglePageLayout() {
			if (!(context.state.doc?.pageCount > 1) || document.body.hasAttribute("data-busy")) return;
			const openSeq = context.state.openSeq;
			if (!await context.canvasEditor.commitNudge() || openSeq !== context.state.openSeq) return;
			if (!await context.canvasEditor.commitText() || openSeq !== context.state.openSeq) return;
			if (!await context.canvasEditor.commitCrop() || openSeq !== context.state.openSeq) return;
			context.state.pageLayoutPreference = context.state.pageLayout === 1 ? 2 : 1;
			pageView.layoutPages();
			pageView.layoutThumbnails();
			pageView.updatePageListCurrent(true);
			if (pageView.defaultView().fitMode === "height") pageView.fitHeight();
			else pageView.fitWidth();
			pageView.queueNearbyPages(context.state.pageIndex);
		},

		layoutPages() {
			if (!context.state.doc) {
				return;
			}
			context.canvasEditor.cancel();
			const view = context.state.pageWindow;
			if (!view) return;
			pageView.invalidateWindowMetrics(view);
			const anchor = view.rows.length > 0 ? pageView.pageWindowAnchor(view) : { index: context.state.pageIndex, offset: -pageView.pageBlockSpace() };
			const gap = context.state.continuous ? 0 : Math.max(context.state.pageLayout > 1 ? PAGE_LAYOUT_GAP : 0, Number.parseFloat(getComputedStyle(context.el.svgHost).rowGap) || 0);
			const count = context.state.doc.pageCount || 0;
			view.nodeStyles.clear();
			context.el.svgHost.classList.toggle("two-page", context.state.pageLayout > 1);
			if (context.state.pageLayout > 1) {
				const spacing = context.state.continuous ? 0 : PAGE_LAYOUT_GAP;
				const rowCount = Math.ceil(count / 2);
				view.columns = 2;
				view.offsets = new Float64Array(count + 1);
				view.heights = new Float64Array(count);
				view.widths = new Float64Array(count);
				view.lefts = new Float64Array(count);
				view.rows = new Array(rowCount);
				view.rowOffsets = new Float64Array(rowCount + 1);
				const factor = MM_TO_PX * context.state.scale;
				let top = 0, maxWidth = 1;
				for (let rowIdx = 0; rowIdx < rowCount; rowIdx++) {
					const start = rowIdx * 2;
					const end = Math.min(count - 1, start + 1);
					const size = pageView.pageGroupSize(start);
					const groupWidth = size.width * factor + (size.count > 1 ? spacing : 0);
					const rowHeight = size.height * factor;
					maxWidth = Math.max(maxWidth, groupWidth);
					view.rowOffsets[rowIdx] = top;
					view.rows[rowIdx] = { row: rowIdx, start, end, top, height: rowHeight, width: groupWidth, widths0: size.widths[0] * factor };
					for (let index = start; index <= end; index++) {
						const pSize = pageView.pageViewSize(pageView.pageInfo(index));
						view.offsets[index] = top;
						view.heights[index] = Math.max(1, pSize.height * factor);
						view.widths[index] = Math.max(1, pSize.width * factor);
					}
					top += rowHeight + gap;
				}
				for (let rowIdx = 0; rowIdx < rowCount; rowIdx++) {
					const row = view.rows[rowIdx];
					const rowLeft = (maxWidth - row.width) / 2;
					view.lefts[row.start] = rowLeft;
					if (row.end > row.start) {
						view.lefts[row.end] = rowLeft + row.widths0 + spacing;
					}
				}
				view.offsets[count] = top;
				view.rowOffsets[rowCount] = top;
				view.gap = gap;
				view.total = Math.max(0, top - gap);
				view.hostWidth = maxWidth;
				view.host.style.width = `${maxWidth}px`;
				for (const [index, shell] of view.nodes) pageView.layoutPageShell(shell, pageView.pageInfo(index));
				const targetIndex = Math.max(0, Math.min(anchor.index, count - 1));
				const targetRow = view.rows[pageView.pageRowIndex(view, targetIndex)];
				pageView.syncPageWindow(view, (targetRow ? targetRow.top : 0) + anchor.offset);
				return;
			}
			view.columns = 0;
			view.lefts = null;
			for (const shell of view.nodes.values()) {
				shell.style.removeProperty("left");
				shell.style.removeProperty("right");
				shell.style.removeProperty("transform");
			}
			const offsets = new Float64Array(count + 1);
			const heights = new Float64Array(count);
			const widths = new Float64Array(count);
			const rows = new Array(count);
			const rowOffsets = new Float64Array(count + 1);
			const maxWidth = context.state.fitMode === "width" ? pageView.widthFitTargetWidth()
				: context.state.fitMode === "height" ? pageView.heightFitMaxWidth() : null;
			let width = 1, top = 0;
			for (let index = 0; index < count; index++) {
				const page = pageView.pageInfo(index);
				const size = pageView.pageViewSize(page);
				const scale = pageView.pageScale(page, context.state.scale, context.state.fitMode, maxWidth);
				const h = Math.max(1, size.height * MM_TO_PX) * scale;
				const w = Math.max(1, size.width * MM_TO_PX) * scale;
				offsets[index] = top;
				rowOffsets[index] = top;
				heights[index] = h;
				widths[index] = w;
				rows[index] = { row: index, start: index, end: index, top, height: h, width: w };
				top += h + gap;
				width = Math.max(width, w);
			}
			offsets[count] = top;
			rowOffsets[count] = top;
			view.offsets = offsets;
			view.heights = heights;
			view.widths = widths;
			view.rows = rows;
			view.rowOffsets = rowOffsets;
			view.gap = gap;
			view.total = Math.max(0, top - gap);
			view.hostWidth = width;
			view.host.style.width = `${width}px`;
			for (const [index, shell] of view.nodes) pageView.layoutPageShell(shell, pageView.pageInfo(index));
			const targetIndex = Math.max(0, Math.min(anchor.index, count - 1));
			pageView.syncPageWindow(view, (offsets[targetIndex] || 0) + anchor.offset);
		},

		layoutPageShell(shell, page) {
			if (!page) return;
			const width = Math.max(1, page.width * MM_TO_PX);
			const height = Math.max(1, page.height * MM_TO_PX);
			const size = pageView.pageViewSize(page);
			const viewWidth = Math.max(1, size.width * MM_TO_PX);
			const viewHeight = Math.max(1, size.height * MM_TO_PX);
			const scale = pageView.pageScale(page);
			shell.style.width = `${viewWidth * scale}px`;
			shell.style.height = `${viewHeight * scale}px`;
			shell.dataset.spreadCol = String(page.index % 2);
			const pageCount = context.state.doc?.pageCount || 0;
			if (context.state.pageLayout > 1 && page.index % 2 === 0 && page.index + 1 < pageCount) shell.dataset.spreadPair = "1";
			else delete shell.dataset.spreadPair;
			const surface = shell.querySelector(".page-surface");
			if (surface) {
				surface.style.width = `${width}px`;
				surface.style.height = `${height}px`;
				const x = context.state.rotation === 90 || context.state.rotation === 180 ? viewWidth : 0;
				const y = context.state.rotation >= 180 ? viewHeight : 0;
				surface.style.transform = `scale(${scale}) translate(${x}px, ${y}px) rotate(${context.state.rotation}deg)`;
				surface.style.setProperty("--surface-scale", String(scale));
				surface.style.setProperty("--surface-rotation", `${context.state.rotation}deg`);
				surface.classList.toggle("sideways", context.state.rotation % 180 !== 0);
			}
		},

		renderPageList() {
			context.el.pageList.replaceChildren();
			context.state.thumbnailWindow = null;
			context.state.pageListCurrent = null;
			context.state.pageListPair = -1;
			context.state.visibleThumbnails.clear();
			if (!context.state.doc) {
				context.el.pageList.style.height = "";
				context.el.pageList.classList.remove("virtual-pages", "grouped-pages");
				context.el.navigationContent.scrollTop = 0;
				return;
			}
			if (context.state.thumbnailObserver) {
				context.state.thumbnailObserver.disconnect();
			}
			if (context.state.pageSelection.size) {
				if (!context.state.editing) context.state.pageSelection.clear();
				else {
					for (const id of context.state.pageSelection) if (context.pageIndexByID(context.state.doc.pages, id) < 0) context.state.pageSelection.delete(id);
				}
			}
			context.state.thumbnailWindow = pageView.createPageWindow(context.el.pageList, context.el.navigationContent, 180, pageView.createThumbnail, pageView.releaseThumbnail, pageView.observeThumbnail);
			pageView.layoutThumbnails();
			pageView.updatePageListCurrent(true);
			context.syncPageSelection();
		},

		createThumbnail(page) {
			const openSeq = context.state.openSeq;
			const button = document.createElement("button");
			button.type = "button";
			button.className = "page-list-item";
			button.dataset.pageIndex = String(page.index);
			if (context.state.editing) button.setAttribute("data-pressed", String(context.state.pageSelection.has(page.id)));
			button.title = `${context.formatSize(page.width)} x ${context.formatSize(page.height)} mm`;
			pageView.setPageItemCurrent(button, page.index === context.state.pageIndex);
			pageView.setPageItemPairCurrent(button, page.index === pageView.pairPageIndex(context.state.pageIndex));
			pageView.layoutThumbnail(button, page);
			const thumb = document.createElement("span");
			thumb.className = "thumb-paper";
			pageView.setThumbnailContent(thumb, pageView.getThumbnailSVG(page.index), page.index, openSeq);
			const label = document.createElement("span");
			label.className = "thumb-label";
			label.textContent = `第${page.index + 1}页`;
			const size = document.createElement("span");
			size.className = "thumb-size";
			size.textContent = `${context.formatSize(page.width)} x ${context.formatSize(page.height)} mm`;
			button.append(thumb, label, size);
			if (context.state.editing && context.pageCan("move", page.index)) context.enablePageDrag(button, page.index);
			button.addEventListener("click", event => context.selectThumbnailPage(event, page.index));
			return button;
		},

		releaseThumbnail(button, index) {
			if (context.state.pageDragIndex === index || button.contains(document.activeElement)) return false;
			context.state.thumbnailObserver?.unobserve(button);
			context.state.visibleThumbnails.delete(index);
			return true;
		},

		layoutThumbnails() {
			const view = context.state.thumbnailWindow;
			if (!view || !context.state.doc) return;
			const visible = !context.el.pageList.hidden && context.state.showPages;
			pageView.invalidateWindowMetrics(view);
			const inset = visible ? pageView.windowInset(view) : Number.parseFloat(getComputedStyle(view.viewport).paddingTop) || 0;
			const anchor = visible && view.rows.length > 0 ? pageView.pageWindowAnchor(view) : view.anchor || { index: context.state.pageIndex, offset: -inset };
			view.rotation = context.state.rotation;
			view.layout = context.state.pageLayout;
			view.nodeStyles.clear();
			view.host.classList.toggle("grouped-pages", context.state.pageLayout > 1);
			const gap = Number.parseFloat(getComputedStyle(view.host).columnGap) || 0;
			const count = context.state.doc.pageCount || 0;
			const measure = (width) => {
				view.width = width;
				const layout = context.state.pageLayout;
				const rowCount = Math.ceil(count / layout);
				const offsets = new Float64Array(count + 1);
				const heights = new Float64Array(count);
				const rows = new Array(rowCount);
				const rowOffsets = new Float64Array(rowCount + 1);
				view.lefts = layout > 1 ? new Float64Array(count) : null;
				view.scales = layout > 1 ? new Float64Array(count) : null;
				view.columns = layout > 1 ? layout : 0;
				view.itemWidth = layout > 1 ? Math.max(1, (width - gap) / 2) : width;
				let top = 0;
				for (let rowIdx = 0; rowIdx < rowCount; rowIdx++) {
					const start = rowIdx * layout;
					const end = Math.min(count - 1, start + layout - 1);
					if (layout === 1) {
						const size = pageView.pageViewSize(pageView.pageInfo(start));
						const ratio = size.width > 0 && size.height > 0 ? size.height / size.width : 1 / 0.707;
						const h = Math.max(1, width - 20) * ratio + 66;
						heights[start] = h;
						offsets[start] = top;
						rowOffsets[rowIdx] = top;
						rows[rowIdx] = { row: rowIdx, start, end, top, height: h, width };
						top += h + gap;
						continue;
					}
					const innerWidth = Math.max(1, view.itemWidth - 20);
					let rowPaperHeight = 0;
					for (let index = start; index <= end; index++) {
						const size = pageView.pageViewSize(pageView.pageInfo(index));
						const valid = size && size.width > 0 && size.height > 0 && Number.isFinite(size.width) && Number.isFinite(size.height);
						const pageScale = valid ? innerWidth / size.width : 1;
						view.scales[index] = pageScale;
						rowPaperHeight = Math.max(rowPaperHeight, valid ? Math.max(1, size.height) * pageScale : innerWidth / 0.707);
					}
					const height = rowPaperHeight + 66;
					rowOffsets[rowIdx] = top;
					rows[rowIdx] = { row: rowIdx, start, end, top, height, width };
					for (let index = start; index <= end; index++) {
						const column = index - start;
						offsets[index] = top;
						heights[index] = height;
						view.lefts[index] = column * (view.itemWidth + gap);
					}
					top += height + gap;
				}
				offsets[count] = top;
				rowOffsets[rowCount] = top;
				view.offsets = offsets;
				view.heights = heights;
				view.rows = rows;
				view.rowOffsets = rowOffsets;
				view.gap = gap;
				view.total = Math.ceil(Math.max(0, top - gap));
				view.host.style.height = `${Math.min(SCROLL_SEGMENT_SIZE, view.total)}px`;
			};
			let width = pageView.thumbnailHostWidth() || view.width || 180;
			measure(width);
			const nextWidth = pageView.thumbnailHostWidth();
			if (nextWidth && nextWidth !== width) {
				measure(nextWidth);
				view.width = nextWidth;
			}
			for (const [index, button] of view.nodes) {
				pageView.layoutThumbnail(button, pageView.pageInfo(index));
			}
			const targetIndex = Math.max(0, Math.min(anchor.index, count - 1));
			const targetRow = view.rows[pageView.pageRowIndex(view, targetIndex)];
			view.pendingTop = (targetRow ? targetRow.top : 0) + anchor.offset;
			if (visible) {
				pageView.syncPageWindow(view, view.pendingTop);
				view.pendingTop = null;
			}
		},

		thumbnailHostWidth() {
			if (context.el.pageList.clientWidth) return context.el.pageList.clientWidth;
			const navWidth = context.el.navigationContent.clientWidth;
			if (!navWidth) return 0;
			const navStyle = getComputedStyle(context.el.navigationContent);
			return Math.max(0, navWidth - (Number.parseFloat(navStyle.paddingLeft) || 0) - (Number.parseFloat(navStyle.paddingRight) || 0));
		},

		syncThumbnailWindow(fromScroll = false) {
			const state = context.state;
			const view = state.thumbnailWindow;
			if (!view || context.el.pageList.hidden || !state.showPages) return;
			if (fromScroll) {
				const now = performance.now();
				const scrollTop = context.el.navigationContent.scrollTop;
				if (state.thumbLastScrollTime > 0) {
					const dt = Math.max(1, now - state.thumbLastScrollTime);
					const velocity = Math.abs(scrollTop - state.thumbLastScrollTop) / dt;
					if (velocity > FAST_SCROLL_VELOCITY) state.thumbFastScroll = true;
				}
				state.thumbLastScrollTop = scrollTop;
				state.thumbLastScrollTime = now;
				if (state.thumbFastScroll) {
					if (state.thumbScrollTimer) clearTimeout(state.thumbScrollTimer);
					const openSeq = state.openSeq, loadSeq = state.pageLoadSeq;
					state.thumbScrollTimer = setTimeout(() => {
						state.thumbScrollTimer = 0;
						if (openSeq !== context.state.openSeq || loadSeq !== context.state.pageLoadSeq) return;
						context.state.thumbFastScroll = false;
						context.state.thumbLastScrollTime = 0;
						for (const index of context.state.visibleThumbnails) {
							const thumb = context.state.thumbnailWindow?.nodes.get(index)?.querySelector(".thumb-paper");
							if (thumb && !thumb.dataset.renderKey) pageView.renderThumbnail(index, openSeq);
						}
						pageView.prunePageRenderQueue();
					}, THUMB_SETTLE_DELAY);
				}
			}
			const hostWidth = pageView.thumbnailHostWidth();
			if (hostWidth && (view.width !== hostWidth || view.rotation !== state.rotation || view.layout !== state.pageLayout)) pageView.layoutThumbnails();
			pageView.syncPageWindow(view, view.pendingTop ?? null);
			view.pendingTop = null;
			pageView.prunePageRenderQueue();
		},

		scheduleRevealThumbnail(index, immediate = false) {
			const state = context.state;
			if (state.revealThumbTimer) {
				clearTimeout(state.revealThumbTimer);
				state.revealThumbTimer = 0;
			}
			if (immediate || !state.viewerFastScroll && !state.scrollFrame) {
				pageView.revealThumbnail(index);
				return;
			}
			const openSeq = state.openSeq;
			state.revealThumbTimer = setTimeout(() => {
				state.revealThumbTimer = 0;
				if (openSeq !== context.state.openSeq) return;
				pageView.revealThumbnail(context.state.pageIndex);
			}, REVEAL_THUMB_DEBOUNCE);
		},

		revealThumbnail(index) {
			pageView.syncThumbnailWindow();
			const view = context.state.thumbnailWindow;
			if (!view || !view.rows.length) return;
			const inset = pageView.windowInset(view);
			const padBottom = pageView.windowPadBottom(view);
			const top = view.logicalTop ?? pageView.pageWindowTop(view);
			const viewHeight = context.el.navigationContent.clientHeight;
			const row = view.rows[pageView.pageRowIndex(view, index)];
			if (!row) return;
			const start = row.top, end = row.top + row.height;
			if (start - inset >= top && end + padBottom <= top + viewHeight) return;
			const target = start - inset < top
				? Math.floor(start - inset)
				: Math.min(start - inset, Math.ceil(end + padBottom - viewHeight));
			pageView.syncPageWindow(view, target);
		},

		resetThumbnails() {
			context.state.visibleThumbnails.clear();
			if (context.state.thumbnailObserver) {
				context.state.thumbnailObserver.disconnect();
				context.state.thumbnailObserver = null;
			}
		},

		layoutThumbnail(button, page) {
			if (!page) {
				return;
			}
			const view = context.state.thumbnailWindow;
			const fallbackHeight = view?.heights?.[page.index] || 180;
			button.style.height = `${fallbackHeight}px`;
			const valid = page.width > 0 && page.height > 0 && Number.isFinite(page.width) && Number.isFinite(page.height);
			if (!valid) {
				if (view?.scales) {
					const innerWidth = Math.max(1, (view.itemWidth || 90) - 20);
					button.style.width = `calc(50% - ${view.gap / 2}px)`;
					button.style.setProperty("--thumb-paper-width", `${innerWidth}px`);
					button.style.setProperty("--thumb-paper-height", `${Math.max(1, fallbackHeight - 66)}px`);
				} else {
					button.style.removeProperty("width");
					button.style.removeProperty("left");
					button.style.removeProperty("right");
					button.style.removeProperty("transform");
					button.style.removeProperty("--thumb-paper-width");
					button.style.removeProperty("--thumb-paper-height");
				}
				button.style.setProperty("--thumb-ratio", "0.707 / 1");
				button.style.setProperty("--thumb-width", "100%");
				button.style.setProperty("--thumb-height", "100%");
				button.style.setProperty("--thumb-rotation", `${context.state.rotation}deg`);
				return;
			}
			const size = pageView.pageViewSize(page);
			if (view.scales) {
				button.style.width = `calc(50% - ${view.gap / 2}px)`;
				button.style.setProperty("--thumb-paper-width", `${size.width * view.scales[page.index]}px`);
				button.style.setProperty("--thumb-paper-height", `${size.height * view.scales[page.index]}px`);
			} else {
				button.style.removeProperty("width");
				button.style.removeProperty("left");
				button.style.removeProperty("right");
				button.style.removeProperty("transform");
				button.style.removeProperty("--thumb-paper-width");
				button.style.removeProperty("--thumb-paper-height");
			}
			button.style.setProperty("--thumb-ratio", `${size.width} / ${size.height}`);
			button.style.setProperty("--thumb-width", `${page.width / size.width * 100}%`);
			button.style.setProperty("--thumb-height", `${page.height / size.height * 100}%`);
			button.style.setProperty("--thumb-rotation", `${context.state.rotation}deg`);
		},

		setThumbnailContent(container, svgText, index, openSeq = context.state.openSeq) {
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
				const svg = pageView.parseSVG(svgText, `t${openSeq}-${index}`);
				svg.classList.add("thumb-svg");
				container.classList.remove("pending", "error");
				container.append(svg);
				container.dataset.renderKey = renderKey;
			} catch {
				container.classList.add("error");
				container.textContent = String(index + 1);
			}
		},

		observeThumbnail(button, index, openSeq = context.state.openSeq) {
			if (!button) return;
			const observer = pageView.thumbnailObserver();
			if (observer) {
				observer.observe(button);
			}
			if (index === context.state.pageIndex || !observer) {
				if (!observer) context.state.visibleThumbnails.add(index);
				pageView.renderThumbnail(index, openSeq);
			}
		},

		thumbnailObserver() {
			if (!("IntersectionObserver" in window)) {
				return null;
			}
			if (!context.state.thumbnailObserver) {
				const openSeq = context.state.openSeq;
				context.state.thumbnailObserver = new IntersectionObserver((entries) => {
					if (openSeq !== context.state.openSeq) {
						return;
					}
					for (const entry of entries) {
						const index = Number.parseInt(entry.target.dataset.pageIndex, 10);
						if (context.state.thumbnailWindow && context.state.thumbnailWindow.nodes.get(index) !== entry.target) continue;
						if (!entry.isIntersecting) {
							context.state.visibleThumbnails.delete(index);
							const thumb = entry.target.querySelector(".thumb-paper");
							if (thumb?.dataset.renderKey) {
								pageView.setThumbnailContent(thumb, "", index, openSeq);
							}
							continue;
						}
						context.state.visibleThumbnails.add(index);
						pageView.renderThumbnail(index, openSeq);
					}
					pageView.prunePageRenderQueue();
					pageView.trimPageCache();
				}, {
					root: context.el.navigationContent,
					rootMargin: "180px 0px",
				});
			}
			return context.state.thumbnailObserver;
		},

		async renderThumbnail(index, openSeq = context.state.openSeq) {
			const state = context.state;
			const loadSeq = state.pageLoadSeq;
			const current = () => openSeq === context.state.openSeq && loadSeq === context.state.pageLoadSeq;
			if (!current() || !state.doc) {
				return;
			}
			const cachedSVG = pageView.getThumbnailSVG(index, state);
			if (cachedSVG) {
				const cachedPage = state.pageCache.get(index) || state.thumbCache.get(index);
				if (cachedPage) await pageView.queuePagePresentation(index, cachedPage, openSeq);
				return;
			}
			if (state.thumbFastScroll && index !== state.pageIndex) return;
			try {
				const page = await pageView.loadPageData(index, { openSeq, priority: 5, preview: true, view: true });
				if (!current() || !page) {
					return;
				}
				await pageView.queuePagePresentation(index, page, openSeq);
				if (!current()) return;
				pageView.trimPageCache();
			} catch (err) {
				if (err?.name === "AbortError" || !current()) return;
				pageView.markThumbnailError(index);
			}
		},

		updateThumbnail(index, openSeq = context.state.openSeq) {
			if (openSeq !== context.state.openSeq || (context.state.thumbnailObserver && !context.state.visibleThumbnails.has(index) && index !== context.state.pageIndex)) {
				return;
			}
			const thumb = context.state.thumbnailWindow?.nodes.get(index)?.querySelector(".thumb-paper")
				|| context.el.pageList.querySelector(`[data-page-index="${index}"] .thumb-paper`);
			if (thumb) {
				pageView.setThumbnailContent(thumb, pageView.getThumbnailSVG(index), index, openSeq);
			}
		},

		markThumbnailError(index) {
			const thumb = context.state.thumbnailWindow?.nodes.get(index)?.querySelector(".thumb-paper")
				|| context.el.pageList.querySelector(`[data-page-index="${index}"] .thumb-paper`);
			if (thumb) {
				delete thumb.dataset.renderKey;
				thumb.classList.add("error");
				thumb.textContent = String(index + 1);
			}
		},

		setContinuous(continuous) {
			context.state.continuous = continuous;
			context.el.pageFrame.classList.toggle("continuous", context.state.continuous);
			context.el.continuousButton.setAttribute("data-pressed", String(context.state.continuous));
			pageView.invalidateWindowMetrics(context.state.pageWindow);
		},

		async toggleContinuous() {
			if (!context.state.doc || document.body.hasAttribute("data-busy")) return;
			const openSeq = context.state.openSeq;
			if (!await context.canvasEditor.commitNudge() || openSeq !== context.state.openSeq) return;
			if (!await context.canvasEditor.commitText() || openSeq !== context.state.openSeq) return;
			if (!await context.canvasEditor.commitCrop() || openSeq !== context.state.openSeq) return;
			const anchor = context.state.fitMode !== "height" ? pageView.scaleAnchor() : null;
			pageView.setContinuous(!context.state.continuous);
			if (context.state.pageWindow) {
				pageView.updateFitSpace();
				pageView.layoutPages();
			}
			context.clearRegionHighlights();
			if (context.state.fitMode === "free") {
				pageView.restoreScaleAnchor(anchor);
			} else {
				pageView.applyFit(false);
				if (context.state.fitMode === "width" && anchor) pageView.restoreScaleAnchor(anchor);
				else pageView.scrollToPage(context.state.pageIndex);
			}
		},

		rotatePages() {
			if (document.body.hasAttribute("data-busy")) {
				return;
			}
			context.state.rotation = (context.state.rotation + 90) % 360;
			context.clearRegionHighlights();
			context.el.viewerPanel.classList.remove("single-page-fits-height");
			pageView.layoutPages();
			pageView.layoutThumbnails();
			if (context.state.fitMode === "free") {
				pageView.updateFitSpace();
			} else {
				pageView.applyFit(false);
			}
			pageView.scrollToPage(context.state.pageIndex);
			if (context.state.showPages && !context.el.pageList.hidden) pageView.revealThumbnail(context.state.pageIndex);
		},

		pageViewSize(page) {
			return context.state.rotation % 180 ? { width: page.height, height: page.width } : page;
		},

		togglePan() {
			if (document.body.hasAttribute("data-busy")) {
				return;
			}
			pageView.setPan(!context.state.panMode);
		},

		updatePanControl() {
			context.el.viewerPanel.classList.toggle("pan-mode", context.state.panMode);
			context.el.panButton.setAttribute("data-pressed", String(context.state.panMode));
		},

		setPan(enabled) {
			pageView.endPan();
			context.state.panMode = enabled;
			pageView.updatePanControl();
			context.updateEditorTools();
		},

		startPan(event) {
			if (!context.state.panMode || !context.state.doc || event.pointerType !== "mouse" || event.button !== 0 || document.body.hasAttribute("data-busy") || event.target.closest("a, button")) {
				return;
			}
			const rect = context.el.viewerPanel.getBoundingClientRect();
			if (event.clientX >= rect.left + context.el.viewerPanel.clientWidth || event.clientY >= rect.top + context.el.viewerPanel.clientHeight) {
				return;
			}
			event.preventDefault();
			context.state.pan = { id: event.pointerId, x: event.clientX + context.el.viewerPanel.scrollLeft,
				y: event.clientY + (context.state.pageWindow ? pageView.pageWindowTop(context.state.pageWindow) : context.el.viewerPanel.scrollTop) };
			context.el.viewerPanel.setPointerCapture(event.pointerId);
			context.el.viewerPanel.classList.add("panning");
			context.canvasEditor.focus();
		},

		movePan(event) {
			const pan = context.state.pan;
			if (!pan || event.pointerId !== pan.id) {
				return;
			}
			context.el.viewerPanel.scrollLeft = pan.x - event.clientX;
			if (context.state.pageWindow) pageView.syncPageWindow(context.state.pageWindow, pan.y - event.clientY);
			else context.el.viewerPanel.scrollTop = pan.y - event.clientY;
		},

		endPan(event) {
			const pan = context.state.pan;
			if (!pan || (event && event.pointerId !== pan.id)) {
				return;
			}
			context.state.pan = null;
			context.el.viewerPanel.classList.remove("panning");
			if (context.el.viewerPanel.hasPointerCapture(pan.id)) {
				context.el.viewerPanel.releasePointerCapture(pan.id);
			}
		},

		fitWidth(updateStatus = true) {
			const page = pageView.currentPageInfo();
			if (!page) {
				return;
			}
			pageView.setScale(pageView.fitWidthScale(page), updateStatus, "width");
			if (updateStatus) {
				pageView.scrollToPage(context.state.pageIndex);
			}
		},

		fitWidthScale(page) {
			return pageView.fitPageScale(page, "width");
		},

		fitPageScale(page, mode) {
			const doc = context.state.doc;
			const pageCount = doc?.pageCount || 0;
			const layout = context.state.pageLayout;
			const size = layout > 1 ? pageView.pageGroupSize(page.index) : pageView.pageViewSize(page);
			const width = Math.max(1, size.width * MM_TO_PX), height = Math.max(1, size.height * MM_TO_PX);
			const space = pageView.pageSpace(), gap = layout > 1 && !context.state.continuous ? PAGE_LAYOUT_GAP : 0;
			const singleMode = layout === 1;
			const items = [];
			let maxGroupWidth = 0, maxGroupGap = 0, maxGroupHeight = height, contentHeight = 0, rowCount = 0;
			if (doc?.defaultPageSize && pageCount > 4096) {
				const defSize = pageView.pageViewSize(doc.defaultPageSize);
				const defW = Math.max(1, defSize.width * MM_TO_PX), defH = Math.max(1, defSize.height * MM_TO_PX);
				rowCount = Math.ceil(pageCount / layout);
				if (singleMode) {
					items.push({ width: defW, height: defH, gap: 0, count: pageCount });
				} else {
					const pairW = defW * (pageCount > 1 ? 2 : 1), pairGap = pageCount > 1 ? gap : 0;
					maxGroupWidth = pairW;
					maxGroupGap = pairGap;
					maxGroupHeight = Math.max(maxGroupHeight, defH);
					contentHeight = defH * rowCount;
					items.push({ width: pairW, height: defH, gap: pairGap, count: rowCount });
				}
				const overrideStarts = new Set();
				const rawOverrides = doc.pageOverrides;
				if (Array.isArray(rawOverrides)) {
					for (const item of rawOverrides) {
						if (typeof item?.index === "number" && item.index >= 0 && item.index < pageCount) overrideStarts.add(Math.floor(item.index / layout) * layout);
					}
				} else if (rawOverrides && typeof rawOverrides === "object") {
					for (const key of Object.keys(rawOverrides)) {
						const idx = Number(key);
						if (Number.isInteger(idx) && idx >= 0 && idx < pageCount) overrideStarts.add(Math.floor(idx / layout) * layout);
					}
				}
				for (const start of overrideStarts) {
					const item = layout > 1 ? pageView.pageGroupSize(start) : pageView.pageViewSize(pageView.pageInfo(start));
					const itemWidth = Math.max(1, item.width * MM_TO_PX), itemHeight = Math.max(1, item.height * MM_TO_PX);
					const itemGap = layout > 1 && item.count > 1 ? gap : 0;
					items.push({ width: itemWidth, height: itemHeight, gap: itemGap, count: 1 });
					if (!singleMode) {
						if (itemWidth + itemGap > maxGroupWidth + maxGroupGap) {
							maxGroupWidth = itemWidth;
							maxGroupGap = itemGap;
						}
						maxGroupHeight = Math.max(maxGroupHeight, itemHeight);
						contentHeight += itemHeight - defH;
					}
				}
			} else {
				for (let index = 0; index < pageCount; index += layout) {
					const item = layout > 1 ? pageView.pageGroupSize(index) : pageView.pageViewSize(pageView.pageInfo(index));
					const itemWidth = Math.max(1, item.width * MM_TO_PX), itemHeight = Math.max(1, item.height * MM_TO_PX);
					const itemGap = layout > 1 && item.count > 1 ? gap : 0;
					items.push({ width: itemWidth, height: itemHeight, gap: itemGap, count: 1 });
					rowCount++;
					if (!singleMode) {
						if (itemWidth + itemGap > maxGroupWidth + maxGroupGap) {
							maxGroupWidth = itemWidth;
							maxGroupGap = itemGap;
						}
						maxGroupHeight = Math.max(maxGroupHeight, itemHeight);
						contentHeight += itemHeight;
					}
				}
			}
			const fitTargetHeight = !singleMode && context.state.continuous ? maxGroupHeight : height;
			const scrollbar = pageView.scrollbarWidth();
			const viewport = context.el.viewerPanel.getBoundingClientRect();
			let vertical = false, horizontal = false, scale = 1, maxPageWidth = 0;
			for (let pass = 0; pass < 3; pass++) {
				const availableWidth = Math.max(1, viewport.width - (vertical ? scrollbar : 0));
				const availableHeight = Math.max(1, viewport.height - (horizontal ? scrollbar : 0));
				maxPageWidth = Math.max(1, availableWidth - space * 2);
				if (singleMode) {
					scale = Math.min(4, maxPageWidth / width);
					if (mode === "height") scale = Math.min(scale, Math.max(1, availableHeight - space * 2) / height);
				} else {
					scale = 4;
					for (const item of items) {
						scale = Math.min(scale, Math.max(1, availableWidth - space * 2 - item.gap) / item.width);
					}
					if (mode === "height") scale = Math.min(scale, Math.max(1, availableHeight - space * 2) / fitTargetHeight);
				}
				const blockSpace = context.state.continuous ? 0 : Math.max(space, (availableHeight - fitTargetHeight * scale) / 2);
				const rowGap = context.state.continuous ? 0 : Math.max(gap, blockSpace + 2);
				let totalHeight = contentHeight * scale;
				let nextHorizontal = !singleMode && maxGroupWidth * scale + maxGroupGap + space * 2 > availableWidth + 0.5;
				if (singleMode) {
					totalHeight = 0;
					for (const item of items) {
						const itemScale = mode === "width" ? Math.min(4, maxPageWidth / item.width) : Math.min(scale, maxPageWidth / item.width);
						totalHeight += item.height * itemScale * (item.count || 1);
					}
					nextHorizontal = false;
				}
				const nextVertical = totalHeight + rowGap * Math.max(0, rowCount - 1) + blockSpace * 2 > availableHeight + 0.5;
				if ((!nextVertical || vertical) && (!nextHorizontal || horizontal)) break;
				vertical ||= nextVertical;
				horizontal ||= nextHorizontal;
			}
			if (singleMode && mode === "height") heightFitWidthLimit = maxPageWidth;
			if (singleMode && mode === "width") widthFitTarget = maxPageWidth;
			return scale;
		},

		fitHeight(updateStatus = true) {
			const page = pageView.currentPageInfo();
			if (!page) {
				return;
			}
			pageView.setScale(pageView.fitPageScale(page, "height"), updateStatus, "height");
			if (updateStatus) {
				pageView.scrollToPage(context.state.pageIndex);
			}
		},

		applyFit(updateStatus = true) {
			if (context.state.fitMode === "height") {
				pageView.fitHeight(updateStatus);
				return;
			}
			if (context.state.fitMode === "width") {
				pageView.fitWidth(updateStatus);
			} else {
				context.updateControls();
			}
		},

		setScale(nextScale, updateStatus = true, fitMode = "free") {
			const anchor = updateStatus && fitMode === "free" ? pageView.scaleAnchor() : null;
			const scale = Math.min(4, fitMode === "free" ? Math.max(0.2, nextScale) : nextScale);
			if (fitMode !== "height" || context.state.pageLayout > 1) heightFitWidthLimit = 0;
			if (fitMode !== "width" || context.state.pageLayout > 1) widthFitTarget = 0;
			const layoutChanged = scale !== context.state.scale || fitMode !== context.state.fitMode
				|| heightFitWidthLimit !== appliedHeightFitWidthLimit || widthFitTarget !== appliedWidthFitTarget;
			appliedHeightFitWidthLimit = heightFitWidthLimit;
			appliedWidthFitTarget = widthFitTarget;
			context.state.fitMode = fitMode;
			context.state.scale = scale;
			const prevClientHeight = context.el.viewerPanel.clientHeight;
			pageView.updateFitSpace();
			const readGap = () => context.state.continuous ? 0
				: Math.max(context.state.pageLayout > 1 ? PAGE_LAYOUT_GAP : 0, Number.parseFloat(getComputedStyle(context.el.svgHost).rowGap) || 0);
			if (layoutChanged || context.state.pageWindow && context.state.pageWindow.gap !== readGap()) {
				if (layoutChanged) context.clearRegionHighlights();
				pageView.layoutPages();
				if (context.el.viewerPanel.clientHeight !== prevClientHeight) {
					pageView.updateFitSpace();
					if (context.state.pageWindow && context.state.pageWindow.gap !== readGap()) pageView.layoutPages();
				}
			}
			if (layoutChanged) {
				pageView.restoreScaleAnchor(anchor);
			}
			const displayScale = pageView.pageScale(pageView.currentPageInfo());
			context.el.zoomLabel.textContent = `${Math.round(displayScale * 100)}%`;
			if (updateStatus && context.state.doc && !context.state.exporting) {
				context.setStatus(`第 ${context.state.pageIndex + 1} / ${context.state.doc.pageCount} 页`);
			}
			context.updateControls();
		},

		scaleAnchor(viewY = 0.45) {
			const shell = pageView.pageShellFromView() || pageView.pageShell(context.state.pageIndex);
			if (!shell) {
				return null;
			}
			const viewerRect = context.el.viewerPanel.getBoundingClientRect();
			const shellRect = shell.getBoundingClientRect();
			return {
				index: Number.parseInt(shell.dataset.pageIndex, 10),
				viewY,
				x: (viewerRect.left + viewerRect.width / 2 - shellRect.left) / Math.max(1, shellRect.width),
				y: (viewerRect.top + viewerRect.height * viewY - shellRect.top) / Math.max(1, shellRect.height),
			};
		},

		restoreScaleAnchor(anchor) {
			if (!anchor) {
				return;
			}
			if (context.state.fitMode === "height" || (context.state.fitMode === "width" && anchor.y <= 0)) {
				pageView.scrollToPage(anchor.index);
				return;
			}
			if (context.state.pageWindow) {
				const view = context.state.pageWindow;
				const height = view.heights?.[anchor.index] ?? view.offsets[anchor.index + 1] - view.offsets[anchor.index] - view.gap;
				pageView.syncPageWindow(view, view.offsets[anchor.index] + height * anchor.y - context.el.viewerPanel.clientHeight * anchor.viewY);
			}
			const shell = pageView.pageShell(anchor.index);
			if (!shell) {
				return;
			}
			const viewerRect = context.el.viewerPanel.getBoundingClientRect();
			const shellRect = shell.getBoundingClientRect();
			context.el.viewerPanel.scrollLeft += shellRect.left + shellRect.width * anchor.x - viewerRect.left - viewerRect.width / 2;
			pageView.scrollViewerBy(shellRect.top + shellRect.height * anchor.y - viewerRect.top - viewerRect.height * anchor.viewY);
		},

		currentPageInfo() {
			return context.state.doc?.pages?.[context.state.pageIndex] || null;
		},

		pageSpace() {
			return Number.parseFloat(getComputedStyle(context.el.pageFrame).paddingLeft) || 0;
		},

		pageBlockSpace() {
			return Number.parseFloat(getComputedStyle(context.el.pageFrame).paddingTop) || pageView.pageSpace();
		},

		scrollbarWidth() {
			const style = getComputedStyle(context.el.viewerPanel).scrollbarWidth;
			const key = `${style}:${window.devicePixelRatio || 1}:${window.innerWidth}:${window.innerHeight}:${window.visualViewport?.scale || 1}`;
			if (scrollbarMeasurement?.key === key) return scrollbarMeasurement.width;
			const probe = document.createElement("div");
			const content = document.createElement("div");
			probe.className = "scrollbar-probe";
			probe.style.position = "absolute";
			probe.style.width = "100px";
			probe.style.height = "100px";
			probe.style.overflow = "scroll";
			probe.style.left = "-9999px";
			probe.append(content);
			document.body.append(probe);
			const width = Math.max(0, 100 - Number.parseFloat(getComputedStyle(content).width));
			probe.remove();
			document.documentElement.style.setProperty("--scrollbar-width", `${width}px`);
			scrollbarMeasurement = { key, width };
			return width;
		},

		updateFitSpace() {
			const page = pageView.currentPageInfo();
			if (!page || context.state.fitMode === "free") {
				context.el.viewerPanel.classList.remove("single-page-fits-height");
				context.el.pageFrame.style.removeProperty("--fit-space");
				context.el.pageFrame.style.removeProperty("--fit-gap");
				pageView.invalidateWindowMetrics(context.state.pageWindow);
				return;
			}
			const group = context.state.pageLayout > 1 ? pageView.pageGroupSize() : null;
			const height = group ? group.height * MM_TO_PX * context.state.scale
				: Math.max(1, pageView.pageViewSize(page).height * MM_TO_PX * pageView.pageScale(page));
			const base = pageView.pageSpace();
			context.el.viewerPanel.classList.toggle("single-page-fits-height", context.state.doc.pageCount === 1 && height <= context.el.viewerPanel.clientHeight - base * 2 + 1);
			const space = context.state.continuous ? 0 : Math.max(base, (context.el.viewerPanel.clientHeight - height) / 2);
			const gap = context.state.continuous ? 0 : space + 2;
			context.el.pageFrame.style.setProperty("--fit-space", `${space}px`);
			context.el.pageFrame.style.setProperty("--fit-gap", `${gap}px`);
			pageView.invalidateWindowMetrics(context.state.pageWindow);
		},

		releasePageResources(state = context.documentTab().state) {
			if (state.pageInFlight.size) return;
			const foreground = state === context.activeTab?.state;
			const images = new Set(), fonts = new Set();
			for (const pool of [state.pageCache, state.thumbCache]) {
				if (!pool) continue;
				for (const page of pool.values()) {
					for (const name of page.imageNames || []) images.add(name);
					for (const font of page.fonts || []) fonts.add(font.name);
				}
			}
			for (const [name, image] of state.svgImages) {
				if (!images.has(name)) {
					if (!image.native) URL.revokeObjectURL(image.url);
					state.svgImages.delete(name);
				}
			}
			for (const [name, font] of state.svgFonts) {
				if (!fonts.has(name)) {
					if (foreground) document.fonts.delete(font);
					state.svgFonts.delete(name);
				}
			}
		},

		trimWorkspaceCaches() {
			const budget = nativeHost?.cacheBytes || Math.max(PAGE_CACHE_BYTES, Math.min(256 << 20, engineLimits().memoryBytes / 4));
			const bytes = tab => [...tab.state.pageCache.values()].reduce((sum, page) => sum + (page.cacheBytes || 0), 0)
				+ [...(tab.state.thumbCache?.values() || [])].reduce((sum, page) => sum + (page.cacheBytes || 0), 0)
				+ [...tab.state.svgImages.values()].reduce((sum, image) => sum + (image.size || 0), 0)
				+ [...tab.state.svgFonts.values()].reduce((sum, face) => sum + (svgFontBytes.get(face) || 0), 0);
			let total = context.tabs.reduce((sum, tab) => sum + bytes(tab), 0);
			for (const tab of context.tabs.filter(tab => tab !== context.activeTab).sort((a, b) => a.touched - b.touched)) {
				if (total <= budget) break;
				const previous = bytes(tab);
				tab.state.pageCache.clear();
				tab.state.thumbCache?.clear();
				tab.state.detachedPages?.clear();
				pageView.releasePageResources(tab.state);
				total -= previous - bytes(tab);
			}
		},

		sanitizeSVG(root) {
			if (!root || root.localName?.toLowerCase() !== "svg") {
				return document.createElementNS("http://www.w3.org/2000/svg", "svg");
			}
			const blockedTags = new Set([
				"script", "foreignobject", "iframe", "object", "embed", "applet",
				"frame", "frameset", "meta", "link", "base", "animate", "set",
				"animatemotion", "animatetransform", "handler", "listener",
			]);
			const urlAttrs = new Set(["href", "xlink:href", "src", "action", "formaction", "poster", "data", "codebase"]);
			const safeURL = value => {
				const cleaned = String(value || "").replace(/[\u0000-\u0020\u007f]+/g, "");
				if (!cleaned) return false;
				if (cleaned.startsWith("#") || cleaned.startsWith("/__native/resource/")) return true;
				const lower = cleaned.toLowerCase();
				if (lower.startsWith("blob:")) return true;
				return /^data:image\/(?:png|jpeg|jpg|gif|webp|bmp);/i.test(cleaned);
			};
			const dangerousStyle = /javascript\s*:|vbscript\s*:|expression\s*\(|behavior\s*:|-moz-binding|@import|url\s*\(\s*['"]?\s*(?!#)/i;
			const nodes = [root, ...root.querySelectorAll("*")];
			for (const node of nodes) {
				if (!node.isConnected && node !== root) continue;
				const tag = (node.localName || node.tagName || "").toLowerCase();
				if (node !== root && blockedTags.has(tag)) {
					node.remove();
					continue;
				}
				for (const attr of [...node.attributes]) {
					const name = attr.name.toLowerCase();
					const local = (attr.localName || "").toLowerCase();
					if (name.startsWith("on") || local.startsWith("on")) {
						node.removeAttributeNode(attr);
						continue;
					}
					if (urlAttrs.has(name) || local === "href") {
						if (!safeURL(attr.value)) node.removeAttributeNode(attr);
						continue;
					}
					if (name === "style" && dangerousStyle.test(attr.value)) {
						node.removeAttributeNode(attr);
					}
				}
			}
			return root;
		},

		parseSVG(svgText, prefix = "", images = context.state.svgImages) {
			let markup = svgText.includes("ofdgo-image-")
				? svgText.replace(/(\b(?:xlink:)?href)="(ofdgo-image-[a-f0-9]{64})"/g, (match, attr, name) => {
					const entry = images.get(name);
					return entry ? `${attr}="${entry.url}"` : "";
				})
				: svgText;
			const hasLocalID = !!prefix && (markup.includes('id="') || markup.includes("id='"));
			let needsPrefixWalk = hasLocalID;
			if (hasLocalID) {
				const scopeMatch = /^([pt])\d+-(\d+)$/.exec(prefix);
				if (scopeMatch) {
					const kind = scopeMatch[1], pageIdx = scopeMatch[2];
					const ownPrefix = `${kind}${pageIdx}-`;
					if (!(new RegExp(`\\sid=["'](?!${ownPrefix})`)).test(markup)) {
						needsPrefixWalk = false;
					} else if (kind === "t") {
						const pagePrefix = `p${pageIdx}-`;
						if (!(new RegExp(`\\sid=["'](?!${pagePrefix})`)).test(markup)) {
							const thumbPrefix = `${prefix}-`;
							const attrPattern = new RegExp(`(\\sid=["']|\\s(?:xlink:)?href=["']#|url\\(["']?#)${pagePrefix}`, "g");
							markup = markup.replace(/<(?:[^>"']|"[^"]*"|'[^']*')*>/g, tag => tag.replace(attrPattern, `$1${thumbPrefix}`));
							needsPrefixWalk = false;
						}
					}
				}
			}
			const template = document.createElement("template");
			template.innerHTML = markup;
			const cleaned = pageView.sanitizeSVG(template.content.firstElementChild);
			const svg = document.adoptNode(cleaned);
			if (needsPrefixWalk) pageView.prefixSVGIds(svg, prefix);
			return svg;
		},

		prefixSVGIds(svg, prefix) {
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
				let next = value.replace(/url\((['"]?)#([^)'"]+)\1\)/g, (match, quote, id) => {
					const mapped = idMap.get(id);
					return mapped ? `url(${quote}#${mapped}${quote})` : match;
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
			const selector = attrs.map(attr => `[${attr === "xlink:href" ? "*|href" : attr}*="#"]`).join(",");
			for (const node of svg.querySelectorAll(selector)) {
				for (const attr of attrs) {
					const value = node.getAttribute(attr);
					if (!value?.includes("#")) continue;
					const next = replaceRef(value);
					if (next !== value) {
						node.setAttribute(attr, next);
					}
				}
			}
		},

		createTextLayer(text) {
			const parts = pageView.createTextLayerParts(text);
			let next;
			do { next = parts.next(); } while (!next.done);
			return next.value;
		},

		*createTextLayerParts(text) {
			const layer = document.createElement("div");
			layer.className = "text-layer";
			const measure = textMeasure ||= document.createElement("canvas").getContext("2d");
			measure.font = "100px sans-serif";
			const widths = new Map();
			let count = 0, started = performance.now();
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
					const [a, b, c, d, e, f] = item.matrix;
					span.style.transform = `matrix(${a * MM_TO_PX / width}, ${b * MM_TO_PX / width}, ${c * MM_TO_PX / 100}, ${d * MM_TO_PX / 100}, ${e * MM_TO_PX}, ${f * MM_TO_PX})`;
					line.append(span);
					end = item.end;
					if (++count % 16 === 0 && performance.now() - started >= 4) {
						yield;
						started = performance.now();
					}
				}
				const breaks = chars.slice(end).filter((char) => char === "\n").join("");
				if (breaks) {
					line.append(document.createTextNode(breaks));
				}
				layer.append(line);
			}
			return layer;
		},

		displayMode() {
			return context.state.editing ? "svg" : context.state.renderMode;
		},
	};
	return pageView;
}
