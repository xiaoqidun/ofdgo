import { nativeHost, engineLimits } from "./ofdgo_host.js";

export const MM_TO_PX = 96 / 25.4;
const PAGE_CACHE_LIMIT = 16;
const PAGE_CACHE_BYTES = 32 * 1024 * 1024;
const svgFontBytes = new WeakMap();
const PAGE_LAYOUT_GAP = 16;
export const SCROLL_SEGMENT_SIZE = 1000000;
let textMeasure = null;

export function createPageView(context) {
	const pageView = {
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
				if (options.fit !== false) {
					pageView.applyFit(false);
				}
				if (options.scroll !== false) {
					pageView.scrollToPage(index);
				} else if (state.pageWindow && (!pageView.pageShell(index) || pageView.pageShell(index).style.visibility === "hidden")) {
					pageView.scrollToPage(index);
				}
				pageView.prunePageRenderQueue();
				const page = await pageView.renderFlowPage(index, { throwError: true, openSeq, priority: 0 });
				if (!page || openSeq !== state.openSeq || index !== state.pageIndex) {
					return null;
				}
				if (options.scroll !== false) {
					pageView.scrollToPage(index);
				}
				pageView.queueNearbyPages(index, openSeq);
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
					if (!options.openAction) context.scheduleOpenActions();
				}
			}
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

		createPageWindow(host, viewport, overscan, create, release, observe) {
			host.classList.add("virtual-pages");
			return { host, viewport, overscan, create, release, observe, nodes: new Map(), offsets: new Float64Array(1), origin: 0, total: 0, gap: 0 };
		},

		windowInset(view) {
			return view.host.getBoundingClientRect().top - view.viewport.getBoundingClientRect().top
				+ view.viewport.scrollTop - view.viewport.clientTop;
		},

		pageWindowTop(view) {
			const inset = pageView.windowInset(view);
			if (view.total <= SCROLL_SEGMENT_SIZE) return view.viewport.scrollTop - inset;
			const delta = view.viewport.scrollTop - view.scrollTop;
			if (!delta) return view.logicalTop;
			if (view.relativeScroll) return view.logicalTop + delta;
			const max = view.viewport.scrollHeight - view.viewport.clientHeight;
			return view.viewport.scrollTop / Math.max(1, max) * (max + view.total - SCROLL_SEGMENT_SIZE) - inset;
		},

		scrollPageWindowBy(view, delta) {
			if (view) pageView.syncPageWindow(view, pageView.pageWindowTop(view) + delta);
		},

		scrollViewerBy(delta) {
			if (context.state.pageWindow) pageView.scrollPageWindowBy(context.state.pageWindow, delta);
			else context.el.viewerPanel.scrollTop += delta;
		},

		syncPageWindow(view, target = null) {
			if (!view || view.offsets.length < 2) return;
			const { host, viewport, offsets, nodes } = view;
			const extent = Math.min(SCROLL_SEGMENT_SIZE, view.total);
			host.style.height = `${extent}px`;
			const inset = pageView.windowInset(view), height = viewport.clientHeight;
			const maxTop = Math.max(0, viewport.scrollHeight - height);
			const logicalMax = maxTop + view.total - extent;
			const logical = Math.max(-inset, Math.min(logicalMax - inset, target ?? pageView.pageWindowTop(view)));
			viewport.scrollTop = logicalMax > 0 ? (logical + inset) * maxTop / logicalMax : 0;
			view.scrollTop = viewport.scrollTop;
			view.logicalTop = logical;
			view.origin = logical - viewport.scrollTop + inset;
			let first = pageView.virtualIndex(offsets, Math.max(view.origin, logical - view.overscan));
			let last = pageView.virtualIndex(offsets, Math.min(view.origin + extent, logical + height + view.overscan));
			if (view.columns) {
				first -= first % view.columns;
				last = Math.min(offsets.length - 2, last + view.columns - 1 - last % view.columns);
			}
			const selection = host === context.el.svgHost && context.state.documentSelection ? document.getSelection() : null;
			const selected = selection?.rangeCount && context.isDocumentSelection(selection.getRangeAt(0));
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
				const node = view.create(context.state.doc.pages[index]);
				nodes.set(index, node);
				const next = [...host.children].find(child => Number(child.dataset.pageIndex) > index);
				host.insertBefore(node, next || null);
				changed = true;
				node.style.top = `${offsets[index] - view.origin}px`;
				view.observe(node, index);
			}
			for (const [index, node] of nodes) {
				const top = offsets[index] - view.origin;
				const itemHeight = view.heights?.[index] ?? offsets[index + 1] - offsets[index] - view.gap;
				if (view.lefts) {
					node.style.left = `${view.lefts[index]}px`;
					node.style.transform = "none";
				}
				node.style.top = `${Math.max(-itemHeight, Math.min(extent, top))}px`;
				node.style.visibility = top + itemHeight < 0 || top >= extent ? "hidden" : "";
			}
			if (selected && changed) selection.getRangeAt(0).selectNodeContents(host);
			view.anchor = pageView.pageWindowAnchor(view);
		},

		pageWindowAnchor(view) {
			const top = pageView.pageWindowTop(view);
			const index = pageView.virtualIndex(view.offsets, top);
			return { index, offset: top - view.offsets[index] };
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
			const surface = document.createElement("div");
			surface.className = "page-surface";
			const placeholder = document.createElement("div");
			placeholder.className = "page-placeholder";
			placeholder.textContent = `第${page.index + 1}页`;
			shell.append(surface, placeholder);
			pageView.layoutPageShell(shell, page);
			return shell;
		},

		renderPageFlow() {
			context.el.svgHost.replaceChildren();
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
			if (!keepCache) {
				context.state.pageCache.clear();
				pageView.releasePageResources();
			}
			context.state.selectedPages.clear();
			context.state.documentSelection = null;
			context.state.visiblePages.clear();
			pageView.resetPageLoading();
		},

		resetPageLoading() {
			context.state.pageLoadSeq++;
			context.releaseMediaPlayers();
			if (context.state.pagePresentFrame) cancelAnimationFrame(context.state.pagePresentFrame);
			context.state.pagePresentFrame = 0;
			for (const task of context.state.pagePresentQueue.values()) task.resolve(null);
			context.state.pagePresentQueue.clear();
			for (const [id, request] of context.wasmRequests) {
				if (request.tab === context.documentTab() && request.name === "ofdgoRenderPage" && (!context.state.sourcePDF || context.state.pdfComplete || context.state.editorInfo)) context.wasmWorker?.postMessage({ type: "cancel", id });
			}
			for (const task of context.state.pageInFlight.values()) {
				task.resolve(null);
			}
			context.state.pageRenderQueue = [];
			context.state.pageRenderRunning = context.state.pageRenderWorkers > 0;
			context.state.pagePrefetchIndex = null;
			context.state.pagePrefetchDirection = 1;
			context.state.pageInFlight.clear();
			pageView.resetThumbnails();
			if (context.state.pageObserver) {
				context.state.pageObserver.disconnect();
				context.state.pageObserver = null;
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
						pageView.renderFlowPage(index, { openSeq, priority: 4 });
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
			if (context.canvasEditor.input?.item.index === index || context.canvasEditor.crop?.item.index === index
				|| range && (!context.state.documentSelection || !context.isDocumentSelection(range)) && range.intersectsNode(shell)) {
				context.state.selectedPages.add(index);
				return;
			}
			context.releaseMediaPlayers(shell.querySelector(".page-surface"));
			shell.querySelector(".page-surface").replaceChildren();
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
			const loadSeq = context.state.pageLoadSeq;
			if (openSeq !== context.state.openSeq) {
				return Promise.resolve(null);
			}
			const key = `${openSeq}:${index}`;
			const priority = options.priority ?? 3;
			const current = context.state.pageInFlight.get(key);
			if (current) {
				current.priority = Math.min(current.priority, priority);
				current.view &&= !!options.view;
				if (current.preview && !options.preview) return current.promise.then(page => page && openSeq === context.state.openSeq && loadSeq === context.state.pageLoadSeq ? pageView.loadPageData(index, options) : null);
				return current.promise;
			}
			if (!options.refresh && context.state.pageCache.has(index) && (options.preview || !context.state.pageCache.get(index).previewOnly)) {
				const page = context.state.pageCache.get(index);
				context.state.pageCache.delete(index);
				context.state.pageCache.set(index, page);
				return Promise.resolve(page);
			}
			let resolve;
			let reject;
			const promise = new Promise((done, fail) => {
				resolve = done;
				reject = fail;
			});
			const task = { key, index, openSeq, priority, view: !!options.view, preview: !!options.preview, refresh: options.refresh, resolve, reject, promise };
			context.state.pageInFlight.set(key, task);
			context.state.pageRenderQueue.push(task);
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
			context.state.pageRenderRunning = true;
			const tab = context.documentTab(), openSeq = tab.state.openSeq, loadSeq = tab.state.pageLoadSeq;
			requestAnimationFrame(() => {
				if (context.activeTab === tab && openSeq === tab.state.openSeq && loadSeq === tab.state.pageLoadSeq) pageView.processPageRenderQueue();
			});
		},

		async processPageRenderQueue() {
			const tab = context.documentTab(), openSeq = tab.state.openSeq, loadSeq = tab.state.pageLoadSeq;
			await context.nextFrame();
			if (context.activeTab !== tab || openSeq !== tab.state.openSeq || loadSeq !== tab.state.pageLoadSeq) return;
			const available = (nativeHost?.parallelism || context.wasmWorker?.parallelism || 1) - tab.state.pageRenderWorkers;
			await Promise.all(Array.from({ length: Math.max(0, available) }, () => pageView.processPageRenderTasks()));
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
						if (task.openSeq !== state.openSeq) {
							task.resolve(null);
							continue;
						}
						if (!task.refresh && state.pageCache.has(task.index) && (task.preview || !state.pageCache.get(task.index).previewOnly)) {
							task.resolve(state.pageCache.get(task.index));
							continue;
						}
						const args = [task.index, state.renderBackend, state.renderDPI, pageView.displayMode() === "raster"];
						args.push([...state.svgFonts.keys()], [...state.svgImages.keys()], task.preview);
						const page = await context.callWASM("ofdgoRenderPage", ...args);
						if (context.activeTab !== tab || state.pageInFlight.get(task.key) !== task || task.openSeq !== state.openSeq) {
							task.resolve(null);
							continue;
						}
						if (state.editing) await context.loadEditorMembers(tab, page, task.index);
						if (context.activeTab !== tab || state.pageInFlight.get(task.key) !== task || task.openSeq !== state.openSeq) {
							task.resolve(null);
							continue;
						}
						await pageView.loadSVGFonts(page.fonts, task.openSeq);
						for (const font of page.fonts) delete font.data;
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
				if (--state.pageRenderWorkers === 0) {
					state.pageRenderRunning = false;
					if (context.activeTab === tab && !state.pageInFlight.size) pageView.releasePageResources();
					if (context.activeTab === tab && state.pageRenderQueue.length) pageView.schedulePageRender();
				}
			}
		},

		trimPageCache() {
			const protectedPages = new Set([context.state.pageIndex, ...context.state.visiblePages, ...context.state.visibleThumbnails, ...context.state.selectedPages]);
			for (const index of context.state.pagePresentQueue.keys()) protectedPages.add(index);
			for (const view of [context.state.pageWindow, context.state.thumbnailWindow]) {
				for (const index of view?.nodes.keys() || []) protectedPages.add(index);
			}
			for (const task of context.state.pageInFlight.values()) {
				protectedPages.add(task.index);
			}
			const references = new Map(), fontReferences = new Map();
			let bytes = 0;
			for (const page of context.state.pageCache.values()) {
				bytes += page.cacheBytes;
				for (const name of page.imageNames) {
					references.set(name, (references.get(name) || 0) + 1);
				}
				for (const font of page.fonts || []) {
					fontReferences.set(font.name, (fontReferences.get(font.name) || 0) + 1);
				}
			}
			for (const image of context.state.svgImages.values()) {
				bytes += image.size;
			}
			for (const face of context.state.svgFonts.values()) bytes += svgFontBytes.get(face) || 0;
			const budget = nativeHost?.cacheBytes || PAGE_CACHE_BYTES;
			for (const [index, page] of context.state.pageCache) {
				if (context.state.pageCache.size <= PAGE_CACHE_LIMIT * Math.max(1, budget / PAGE_CACHE_BYTES) && bytes <= budget) {
					break;
				}
				if (protectedPages.has(index)) {
					continue;
				}
				context.state.pageCache.delete(index);
				bytes -= page.cacheBytes;
				for (const name of page.imageNames) {
					const count = references.get(name) - 1;
					references.set(name, count);
					if (count === 0 && !context.state.pageInFlight.size) {
						const image = context.state.svgImages.get(name);
						if (image) {
							bytes -= image.size;
							URL.revokeObjectURL(image.url);
							context.state.svgImages.delete(name);
						}
					}
				}
				for (const font of page.fonts || []) {
					const count = fontReferences.get(font.name) - 1;
					fontReferences.set(font.name, count);
					if (count === 0 && !context.state.pageInFlight.size) {
						const face = context.state.svgFonts.get(font.name);
						if (face) {
							bytes -= svgFontBytes.get(face) || 0;
							document.fonts.delete(face);
							context.state.svgFonts.delete(font.name);
						}
					}
				}
			}
			if (!context.state.pageInFlight.size) pageView.releasePageResources();
			pageView.trimWorkspaceCaches();
		},

		async loadSVGFonts(fonts, openSeq) {
			const state = context.state, loadSeq = state.pageLoadSeq;
			const current = () => state === context.state && openSeq === state.openSeq && loadSeq === state.pageLoadSeq;
			for (let offset = 0; offset < fonts.length && current(); offset += 4) {
				const results = await Promise.allSettled(fonts.slice(offset, offset + 4).map(async font => {
					if (state.svgFonts.has(font.name)) return;
					const key = `${openSeq}:${loadSeq}:${font.name}`;
					while (current() && !state.svgFonts.has(font.name) && !state.svgFontLoads.has(key) && state.svgFontLoads.size >= 4) {
						await Promise.race([...state.svgFontLoads.values()].map(loading => loading.catch(() => {})));
					}
					if (!current() || state.svgFonts.has(font.name)) return;
					let loading = state.svgFontLoads.get(key);
					if (!loading) {
						loading = pageView.loadSVGFont(font, openSeq, loadSeq);
						state.svgFontLoads.set(key, loading);
					}
					try { await loading; }
					finally { if (state.svgFontLoads.get(key) === loading) state.svgFontLoads.delete(key); }
				}));
				const failed = results.find(result => result.status === "rejected");
				if (failed) throw failed.reason;
			}
		},

		async loadSVGFont(font, openSeq, loadSeq = context.state.pageLoadSeq) {
			const data = font.data ? { bytes: font.data } : await context.callWASM("ofdgoSVGFontData", font.name);
			if (openSeq !== context.state.openSeq || loadSeq !== context.state.pageLoadSeq) {
				return;
			}
			const face = new FontFace(font.name, data.bytes, { weight: String(font.weight), style: font.style });
			await face.load();
			if (openSeq !== context.state.openSeq || loadSeq !== context.state.pageLoadSeq) {
				return;
			}
			document.fonts.add(face);
			svgFontBytes.set(face, data.bytes.byteLength);
			context.state.svgFonts.set(font.name, face);
		},

		comparePageRenderTask(a, b) {
			return pageView.pageRenderPriority(a) - pageView.pageRenderPriority(b) || Math.abs(a.index - context.state.pageIndex) - Math.abs(b.index - context.state.pageIndex);
		},

		pageRenderPriority(task, state = context.state) {
			if (!task.view || task.refresh) return task.priority;
			if (task.index === state.pageIndex) return 0;
			if (Math.floor(task.index / state.pageLayout) === Math.floor(state.pageIndex / state.pageLayout)) return 1;
			if (!task.preview) {
				if (state.visiblePages.has(task.index)) return 2;
				if (state.pageWindow?.nodes.has(task.index)) return 3;
			}
			if (state.visibleThumbnails.has(task.index) || state.thumbnailWindow?.nodes.has(task.index)) return 5;
			return 6;
		},

		queuePagePresentation(index, page, openSeq, flow = false) {
			const state = context.state;
			if (openSeq !== state.openSeq || state.pageCache.get(index) !== page) return Promise.resolve(null);
			let shell = flow ? pageView.pageShell(index) : null;
			let thumb = context.el.pageList.querySelector(`[data-page-index="${index}"] .thumb-paper`);
			if (shell?.classList.contains("rendered") || state.pageObserver && !state.visiblePages.has(index) && index !== state.pageIndex) shell = null;
			if (thumb?.dataset.renderKey === `${openSeq}:${index}` || state.thumbnailObserver && !state.visibleThumbnails.has(index) && index !== state.pageIndex) thumb = null;
			if (!shell && !thumb) return Promise.resolve(null);
			const queue = state.pagePresentQueue, current = queue.get(index);
			if (current?.page === page) {
				if (shell) { current.shell = shell; current.preview = false; }
				current.thumb = thumb;
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
				if (context.activeTab !== tab || openSeq !== state.openSeq || loadSeq !== state.pageLoadSeq) return;
				state.pagePresentFrame = 0;
				const started = performance.now();
				do {
					const task = [...state.pagePresentQueue.values()].sort(pageView.comparePageRenderTask)[0];
					try {
						if (state.pageCache.get(task.index) === task.page) {
							if (task.shell && pageView.pageShell(task.index) === task.shell && !task.shell.classList.contains("rendered")
								&& (!state.pageObserver || state.visiblePages.has(task.index) || task.index === state.pageIndex)) {
								task.text ||= pageView.createTextLayerParts(task.page.text);
								let text;
								do { text = task.text.next(); } while (!text.done && performance.now() - started < 6);
								if (!text.done) break;
								pageView.mountPageSVG(task.index, task.page, task.openSeq, text.value);
							}
							if (task.thumb && context.el.pageList.querySelector(`[data-page-index="${task.index}"] .thumb-paper`) === task.thumb) pageView.updateThumbnail(task.index, task.openSeq);
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
			const svg = pageView.parseSVG(page.svg, `p${openSeq}-${index}`);
			svg.classList.add("ofd-svg");
			surface.replaceChildren(svg);
			surface.append(textLayer || pageView.createTextLayer(page.text));
			if (context.state.editorInfo && pageView.displayMode() === "svg") {
				context.mountEditorObjects(index, page, surface);
			}
			context.mountAnnotationNotes(index, surface, page.annotations);
			context.mountPageLinks(page, surface);
			shell.classList.add("rendered");
			context.renderSearchHighlights(index);
			if (context.state.doc?.pages?.[index]) {
				pageView.layoutPageShell(shell, context.state.doc.pages[index]);
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
			const start = pageView.pageGroupStart(index);
			if (context.state.pagePrefetchIndex !== null && start !== context.state.pagePrefetchIndex) context.state.pagePrefetchDirection = Math.sign(start - context.state.pagePrefetchIndex);
			const direction = context.state.pagePrefetchDirection;
			context.state.pagePrefetchIndex = start;
			let bytes = 0;
			for (const page of context.state.pageCache.values()) bytes += page.cacheBytes || 0;
			for (const image of context.state.svgImages.values()) bytes += image.size;
			for (const face of context.state.svgFonts.values()) bytes += svgFontBytes.get(face) || 0;
			const budget = nativeHost?.cacheBytes || PAGE_CACHE_BYTES;
			const cost = Math.max(1 << 20, bytes / Math.max(1, context.state.pageCache.size));
			const count = Math.max(0, Math.min(nativeHost?.parallelism || context.wasmWorker?.parallelism || 1, context.state.pageLayout * 2, Math.floor((budget - bytes) / cost)));
			for (let i = start; i < Math.min(context.state.doc?.pageCount || 1, start + context.state.pageLayout); i += 1) {
				pageView.renderFlowPage(i, { openSeq, priority: 1 });
			}
			for (let offset = 1; offset <= count; offset += 1) {
				const next = direction > 0 ? start + context.state.pageLayout + offset - 1 : start - offset;
				if (next >= 0 && next < (context.state.doc?.pageCount || 1)) pageView.renderFlowPage(next, { openSeq, priority: 6 });
			}
		},

		pageShell(index) {
			return context.state.pageWindow?.nodes.get(index) || null;
		},

		scrollToPage(index) {
			if (context.state.pageLayout > 1 && context.state.pageWindow) {
				const view = context.state.pageWindow, start = pageView.pageGroupStart(index);
				const size = pageView.pageGroupSize(start), gap = context.state.continuous ? 0 : PAGE_LAYOUT_GAP;
				const height = size.height * MM_TO_PX * context.state.scale;
				const whole = height <= context.el.viewerPanel.clientHeight - pageView.pageBlockSpace() * 2 + 1;
				const space = context.state.fitMode === "height" && !context.state.continuous
					? Math.max(pageView.pageBlockSpace(), (context.el.viewerPanel.clientHeight - height) / 2) : pageView.pageBlockSpace();
				pageView.syncPageWindow(view, view.offsets[whole ? start : index] - space);
				context.el.viewerPanel.scrollLeft = Math.max(0, (context.el.viewerPanel.scrollWidth - context.el.viewerPanel.clientWidth) / 2);
				if (size.width * MM_TO_PX * context.state.scale + gap > context.el.viewerPanel.clientWidth) {
					const shell = pageView.pageShell(index);
					if (shell) pageView.centerPageInline(shell);
				}
				return;
			}
			if (context.state.pageWindow) pageView.syncPageWindow(context.state.pageWindow, context.state.pageWindow.offsets[index]);
			const shell = pageView.pageShell(index);
			if (shell) {
				if (context.state.pageWindow) {
					const shellRect = shell.getBoundingClientRect(), viewerRect = context.el.viewerPanel.getBoundingClientRect();
					const space = context.state.fitMode === "height" && !context.state.continuous
						? (context.el.viewerPanel.clientHeight - shellRect.height) / 2 : pageView.pageBlockSpace();
					pageView.scrollViewerBy(shellRect.top - viewerRect.top - space);
				} else if (context.state.fitMode === "height" && !context.state.continuous) {
					shell.scrollIntoView({ block: "center", inline: "nearest" });
				} else {
					const viewerRect = context.el.viewerPanel.getBoundingClientRect();
					const shellRect = shell.getBoundingClientRect();
					const top = context.el.viewerPanel.scrollTop + shellRect.top - viewerRect.top - pageView.pageBlockSpace();
					const maxTop = Math.max(0, context.el.viewerPanel.scrollHeight - context.el.viewerPanel.clientHeight);
					context.el.viewerPanel.scrollTop = Math.min(maxTop, Math.max(0, top));
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
			if (context.state.scrollFrame || !context.state.doc) {
				return;
			}
			context.state.scrollFrame = requestAnimationFrame(() => {
				context.state.scrollFrame = 0;
				pageView.syncCurrentPageFromScroll();
			});
		},

		syncCurrentPageFromScroll() {
			if (context.state.openActionTask?.navigating) return;
			pageView.syncPageWindow(context.state.pageWindow);
			const navigation = context.state.pageNavigation;
			const top = context.state.pageWindow ? pageView.pageWindowTop(context.state.pageWindow) : context.el.viewerPanel.scrollTop;
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
				pageView.queueNearbyPages(nextIndex);
				context.scheduleOpenActions();
			}
			pageView.prunePageRenderQueue();
		},

		prunePageRenderQueue() {
			context.state.pageRenderQueue = context.state.pageRenderQueue.filter(task => {
				if (!task.view || task.refresh || Math.abs(task.index - context.state.pageIndex) <= 2
					|| context.state.pageWindow?.nodes.has(task.index) || context.state.thumbnailWindow?.nodes.has(task.index)) return true;
				context.state.pageInFlight.delete(task.key);
				task.resolve(null);
				return false;
			});
		},

		pageShellFromView() {
			const rect = context.el.viewerPanel.getBoundingClientRect();
			if (context.state.pageLayout > 1) {
				let best = null, area = 0;
				const current = pageView.pageShell(context.state.pageIndex);
				for (const shell of [current, ...(context.state.pageWindow?.nodes.values() || [])]) {
					if (!shell || shell.style.visibility === "hidden") continue;
					const bounds = shell.getBoundingClientRect();
					const visible = Math.max(0, Math.min(rect.right, bounds.right) - Math.max(rect.left, bounds.left))
						* Math.max(0, Math.min(rect.bottom, bounds.bottom) - Math.max(rect.top, bounds.top));
					const ratio = visible / Math.max(1, bounds.width * bounds.height);
					if (ratio > area + 0.01) { best = shell; area = ratio; }
				}
				return best;
			}
			const x = rect.left + rect.width / 2;
			if (context.el.viewerPanel.scrollTop + context.el.viewerPanel.clientHeight >= context.el.viewerPanel.scrollHeight - 1) {
				const shell = pageView.pageShell(context.state.pageIndex);
				const bounds = shell?.getBoundingClientRect();
				return bounds && bounds.top >= rect.top - 1 && bounds.bottom <= rect.bottom + 1
					? shell : context.el.svgHost.lastElementChild;
			}
			if (context.state.continuous) {
				return pageView.pageShellAtPoint(x, rect.top + 1);
			}
			return pageView.pageShellAtPoint(x, rect.top + rect.height * 0.45)
				|| pageView.pageShellAtPoint(x, rect.top + rect.height * 0.25)
				|| pageView.pageShellAtPoint(x, rect.top + rect.height * 0.65);
		},

		pageShellAtPoint(x, y) {
			return document.elementFromPoint(x, y)?.closest?.(".page-shell") || null;
		},

		setCurrentPage(index) {
			if (index !== context.state.pageIndex && !context.state.openActionTask?.navigating) context.state.openActionTask = null;
			context.state.pageIndex = index;
			pageView.prunePageRenderQueue();
			if (context.state.editing) context.state.openActionPage = context.state.doc?.pages[index]?.id;
			if (context.state.fitMode === "width" && context.state.pageLayout === 1) {
				context.state.scale = pageView.fitWidthScale(pageView.currentPageInfo());
				context.el.zoomLabel.textContent = `${Math.round(context.state.scale * 100)}%`;
			}
			pageView.updatePageListCurrent();
			context.updateControls();
			if (context.state.doc && !context.state.exporting) {
				context.setStatus(context.pageStatus(index, context.state.doc.pageCount));
			}
		},

		updatePageListCurrent(force = false) {
			const current = context.el.pageList.querySelector(".page-list-item[data-current]");
			if (context.state.thumbnailWindow) {
				if (!force && context.state.pageListCurrent === context.state.pageIndex) return;
				context.state.pageListCurrent = context.state.pageIndex;
				if (context.state.showPages && !context.el.pageList.hidden) pageView.revealThumbnail(context.state.pageIndex);
			}
			if (current) {
				if (!force && Number.parseInt(current.dataset.pageIndex, 10) === context.state.pageIndex) {
					return;
				}
				current.removeAttribute("data-current");
			}
			const next = context.el.pageList.querySelector(`.page-list-item[data-page-index="${context.state.pageIndex}"]`);
			if (next) {
				pageView.setPageItemCurrent(next, true);
				if (context.state.showPages && !context.el.pageList.hidden && !context.state.thumbnailWindow) {
					const panel = context.el.navigationContent.getBoundingClientRect();
					const item = next.getBoundingClientRect();
					if (item.top < panel.top) {
						context.el.navigationContent.scrollTop += item.top - panel.top;
					} else if (item.bottom > panel.bottom) {
						context.el.navigationContent.scrollTop += Math.min(item.top - panel.top, item.bottom - panel.bottom);
					}
				}
			}
		},

		setPageItemCurrent(item, current) {
			if (current) {
				item.setAttribute("data-current", "page");
				return;
			}
			item.removeAttribute("data-current");
		},

		pageGroupStart(index = context.state.pageIndex) {
			return Math.floor(index / context.state.pageLayout) * context.state.pageLayout;
		},

		pageGroupSize(index = context.state.pageIndex) {
			const start = pageView.pageGroupStart(index), widths = [0, 0];
			let height = 0;
			for (let offset = 0; offset < context.state.pageLayout; offset++) {
				const page = context.state.doc.pages[start + offset];
				if (!page) continue;
				const size = pageView.pageViewSize(page);
				widths[offset] = size.width;
				height = Math.max(height, size.height);
			}
			if (!widths[1]) widths[1] = widths[0];
			return { widths, width: widths[0] + widths[1], height };
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
			if (context.state.showPages && !context.el.pageList.hidden) pageView.revealThumbnail(context.state.pageIndex);
			pageView.fitHeight();
			pageView.queueNearbyPages(context.state.pageIndex);
		},

		layoutPages() {
			if (!context.state.doc) {
				return;
			}
			context.canvasEditor.cancel();
			const view = context.state.pageWindow;
			if (!view) return;
			const anchor = view.offsets.length > 1 ? pageView.pageWindowAnchor(view) : { index: context.state.pageIndex, offset: 0 };
			const gap = context.state.continuous ? 0 : Math.max(context.state.pageLayout > 1 ? PAGE_LAYOUT_GAP : 0, Number.parseFloat(getComputedStyle(context.el.svgHost).rowGap) || 0);
			if (context.state.pageLayout > 1) {
				const count = context.state.doc.pages.length, spacing = context.state.continuous ? 0 : PAGE_LAYOUT_GAP;
				view.columns = 2;
				view.offsets = new Float64Array(count + 1);
				view.heights = new Float64Array(count);
				view.lefts = new Float64Array(count);
				let top = 0, width = 0;
				const groups = [];
				for (let start = 0; start < count; start += context.state.pageLayout) {
					const size = pageView.pageGroupSize(start), factor = MM_TO_PX * context.state.scale;
					const groupWidth = size.width * factor + spacing;
					groups.push({ start, size, width: groupWidth });
					width = Math.max(width, groupWidth);
					for (let column = 0; column < 2; column++) {
						const index = start + column;
						if (index >= count) break;
						view.offsets[index] = top;
						view.heights[index] = pageView.pageViewSize(context.state.doc.pages[index]).height * factor;
					}
					top += size.height * factor + gap;
				}
				for (const group of groups) {
					for (let index = group.start; index < Math.min(count, group.start + context.state.pageLayout); index++) {
						const column = (index - group.start) % 2, factor = MM_TO_PX * context.state.scale;
						view.lefts[index] = (width - group.width) / 2
							+ (column ? group.size.widths[0] * factor + spacing : 0)
							+ (group.size.widths[column] - pageView.pageViewSize(context.state.doc.pages[index]).width) * factor / 2;
					}
				}
				view.offsets[count] = top;
				view.gap = gap;
				view.total = Math.max(0, top - gap);
				view.host.style.width = `${width}px`;
				for (const [index, shell] of view.nodes) pageView.layoutPageShell(shell, context.state.doc.pages[index]);
				pageView.syncPageWindow(view, view.offsets[Math.min(anchor.index, count - 1)] + anchor.offset);
				return;
			}
			view.columns = 0;
			view.heights = null;
			view.lefts = null;
			for (const shell of view.nodes.values()) {
				shell.style.removeProperty("left");
				shell.style.removeProperty("transform");
			}
			const offsets = new Float64Array(context.state.doc.pages.length + 1);
			const fittedWidth = context.state.scale * pageView.pageViewSize(pageView.currentPageInfo()).width;
			let width = 1;
			for (let index = 0; index < context.state.doc.pages.length; index++) {
				const size = pageView.pageViewSize(context.state.doc.pages[index]);
				const scale = context.state.fitMode === "width" ? fittedWidth / size.width : context.state.scale;
				offsets[index + 1] = offsets[index] + Math.max(1, size.height * MM_TO_PX) * scale + gap;
				width = Math.max(width, Math.max(1, size.width * MM_TO_PX) * scale);
			}
			view.offsets = offsets;
			view.gap = gap;
			view.total = Math.max(0, offsets.at(-1) - gap);
			view.host.style.width = `${width}px`;
			for (const [index, shell] of view.nodes) pageView.layoutPageShell(shell, context.state.doc.pages[index]);
			pageView.syncPageWindow(view, offsets[anchor.index] + anchor.offset);
		},

		layoutPageShell(shell, page) {
			const width = Math.max(1, page.width * MM_TO_PX);
			const height = Math.max(1, page.height * MM_TO_PX);
			const size = pageView.pageViewSize(page);
			const viewWidth = Math.max(1, size.width * MM_TO_PX);
			const viewHeight = Math.max(1, size.height * MM_TO_PX);
			const scale = context.state.fitMode === "width" && context.state.pageLayout === 1 ? context.state.scale * pageView.pageViewSize(pageView.currentPageInfo()).width / size.width : context.state.scale;
			shell.style.width = `${viewWidth * scale}px`;
			shell.style.height = `${viewHeight * scale}px`;
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
			const ids = new Set(context.state.doc.pages.map(page => page.id));
			for (const id of context.state.pageSelection) if (!ids.has(id) || !context.state.editing) context.state.pageSelection.delete(id);
			context.state.thumbnailWindow = pageView.createPageWindow(context.el.pageList, context.el.navigationContent, 180, pageView.createThumbnail, pageView.releaseThumbnail, pageView.observeThumbnail);
			pageView.layoutThumbnails();
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
			pageView.layoutThumbnail(button, page);

			const thumb = document.createElement("span");
			thumb.className = "thumb-paper";
			pageView.setThumbnailContent(thumb, context.state.pageCache.get(page.index)?.svg, page.index, openSeq);

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
			if (!view) return;
			const visible = !context.el.pageList.hidden && context.state.showPages;
			const anchor = visible && view.offsets.length > 1 ? pageView.pageWindowAnchor(view) : view.anchor || { index: context.state.pageIndex, offset: 0 };
			const width = context.el.navigationContent.clientWidth || view.width || 180;
			view.width = width;
			view.rotation = context.state.rotation;
			view.layout = context.state.pageLayout;
			view.host.classList.toggle("grouped-pages", context.state.pageLayout > 1);
			const gap = Number.parseFloat(getComputedStyle(view.host).columnGap) || 0;
			const offsets = new Float64Array(context.state.doc.pages.length + 1);
			view.heights = new Float64Array(context.state.doc.pages.length);
			view.lefts = context.state.pageLayout > 1 ? new Float64Array(context.state.doc.pages.length) : null;
			view.scales = context.state.pageLayout > 1 ? new Float64Array(context.state.doc.pages.length) : null;
			view.columns = context.state.pageLayout > 1 ? context.state.pageLayout : 0;
			view.itemWidth = context.state.pageLayout > 1 ? Math.max(1, (width - gap) / 2) : width;
			let top = 0;
			for (let start = 0; start < context.state.doc.pages.length; start += context.state.pageLayout) {
				if (context.state.pageLayout === 1) {
					const size = pageView.pageViewSize(context.state.doc.pages[start]);
					const ratio = size.width > 0 && size.height > 0 ? size.height / size.width : 1 / 0.707;
					view.heights[start] = Math.max(1, width - 20) * ratio + 66;
					offsets[start] = top;
					top += view.heights[start] + gap;
					continue;
				}
				const group = pageView.pageGroupSize(start), scale = Math.max(1, view.itemWidth - 20) / Math.max(...group.widths);
				const height = group.height * scale + 66;
				for (let column = 0; column < 2; column++) {
					const index = start + column;
					if (index >= context.state.doc.pages.length) break;
					offsets[index] = top;
					view.heights[index] = height;
					view.lefts[index] = column * (view.itemWidth + gap);
					view.scales[index] = scale;
				}
				top += height + gap;
			}
			offsets[context.state.doc.pages.length] = top;
			view.offsets = offsets;
			view.gap = gap;
			view.total = Math.ceil(offsets.at(-1));
			for (const [index, button] of view.nodes) {
				pageView.layoutThumbnail(button, context.state.doc.pages[index]);
			}
			view.pendingTop = offsets[anchor.index] + anchor.offset;
			view.host.style.height = `${Math.min(SCROLL_SEGMENT_SIZE, view.total)}px`;
			if (visible) {
				pageView.syncPageWindow(view, view.pendingTop);
				view.pendingTop = null;
			}
		},

		syncThumbnailWindow() {
			const view = context.state.thumbnailWindow;
			if (!view || context.el.pageList.hidden || !context.state.showPages) return;
			if (context.el.navigationContent.clientWidth && (view.width !== context.el.navigationContent.clientWidth || view.rotation !== context.state.rotation || view.layout !== context.state.pageLayout)) pageView.layoutThumbnails();
			pageView.syncPageWindow(view, view.pendingTop ?? null);
			view.pendingTop = null;
			pageView.prunePageRenderQueue();
		},

		revealThumbnail(index) {
			pageView.syncThumbnailWindow();
			const view = context.state.thumbnailWindow;
			if (!view) return;
			const top = pageView.pageWindowTop(view);
			let start = view.offsets[index], end = start + view.heights[index];
			if (start < top || end > top + context.el.navigationContent.clientHeight) {
				const first = pageView.pageGroupStart(index), last = Math.min(context.state.doc.pageCount - 1, first + context.state.pageLayout - 1);
				const groupEnd = view.offsets[last] + view.heights[last];
				if (groupEnd - view.offsets[first] <= context.el.navigationContent.clientHeight) {
					start = view.offsets[first];
					end = groupEnd;
				}
				const target = start < top ? Math.floor(start) : Math.min(start, Math.ceil(end - context.el.navigationContent.clientHeight));
				pageView.syncPageWindow(view, target);
			}
		},

		resetThumbnails() {
			context.state.visibleThumbnails.clear();
			if (context.state.thumbnailObserver) {
				context.state.thumbnailObserver.disconnect();
				context.state.thumbnailObserver = null;
			}
		},

		layoutThumbnail(button, page) {
			if (page.width <= 0 || page.height <= 0) {
				return;
			}
			const size = pageView.pageViewSize(page);
			const view = context.state.thumbnailWindow;
			button.style.height = `${view.heights[page.index]}px`;
			button.style.width = `${view.itemWidth}px`;
			if (view.scales) {
				button.style.setProperty("--thumb-paper-width", `${size.width * view.scales[page.index]}px`);
				button.style.setProperty("--thumb-paper-height", `${size.height * view.scales[page.index]}px`);
			} else {
				button.style.removeProperty("left");
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
							if (thumb.dataset.renderKey) {
								pageView.setThumbnailContent(thumb, "", index, openSeq);
							}
							continue;
						}
						context.state.visibleThumbnails.add(index);
						pageView.renderThumbnail(index, openSeq);
					}
					pageView.trimPageCache();
				}, {
					root: context.el.navigationContent,
					rootMargin: "180px 0px",
				});
			}
			return context.state.thumbnailObserver;
		},

		async renderThumbnail(index, openSeq = context.state.openSeq) {
			const loadSeq = context.state.pageLoadSeq;
			const current = () => openSeq === context.state.openSeq && loadSeq === context.state.pageLoadSeq;
			if (!current() || !context.state.doc) {
				return;
			}
			try {
				const page = await pageView.loadPageData(index, { openSeq, priority: 5, preview: true, view: true });
				if (!current() || !page) {
					return;
				}
				await pageView.queuePagePresentation(index, page, openSeq);
				if (!current()) return;
				pageView.trimPageCache();
			} catch {
				if (current()) {
					pageView.markThumbnailError(index);
				}
			}
		},

		updateThumbnail(index, openSeq = context.state.openSeq) {
			if (openSeq !== context.state.openSeq || (context.state.thumbnailObserver && !context.state.visibleThumbnails.has(index) && index !== context.state.pageIndex)) {
				return;
			}
			const thumb = context.el.pageList.querySelector(`[data-page-index="${index}"] .thumb-paper`);
			if (thumb) {
				pageView.setThumbnailContent(thumb, context.state.pageCache.get(index)?.svg, index, openSeq);
			}
		},

		markThumbnailError(index) {
			const thumb = context.el.pageList.querySelector(`[data-page-index="${index}"] .thumb-paper`);
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
		},

		async toggleContinuous() {
			if (!context.state.doc || document.body.hasAttribute("data-busy")) return;
			const openSeq = context.state.openSeq;
			if (!await context.canvasEditor.commitNudge() || openSeq !== context.state.openSeq) return;
			if (!await context.canvasEditor.commitText() || openSeq !== context.state.openSeq) return;
			if (!await context.canvasEditor.commitCrop() || openSeq !== context.state.openSeq) return;
			const anchor = context.state.fitMode === "free" ? pageView.scaleAnchor() : null;
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
				pageView.scrollToPage(context.state.pageIndex);
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
			const size = context.state.pageLayout > 1 ? pageView.pageGroupSize(page.index) : pageView.pageViewSize(page);
			const width = Math.max(1, size.width * MM_TO_PX), height = Math.max(1, size.height * MM_TO_PX);
			const space = pageView.pageSpace(), gap = context.state.pageLayout > 1 && !context.state.continuous ? PAGE_LAYOUT_GAP : 0;
			let contentWidth = 0, contentHeight = 0, rows = 0;
			for (let index = 0; index < context.state.doc.pages.length; index += context.state.pageLayout) {
				const item = context.state.pageLayout > 1 ? pageView.pageGroupSize(index) : pageView.pageViewSize(context.state.doc.pages[index]);
				const factor = mode === "width" && context.state.pageLayout === 1 ? size.width / item.width : 1;
				contentWidth = Math.max(contentWidth, item.width * MM_TO_PX * factor);
				contentHeight += item.height * MM_TO_PX * factor;
				rows++;
			}
			const layoutWidth = context.state.pageLayout > 1 ? Math.max(width, contentWidth) : width;
			const scrollbar = pageView.scrollbarWidth();
			const viewport = context.el.viewerPanel.getBoundingClientRect();
			let vertical = false, horizontal = false, scale = 1;
			for (let pass = 0; pass < 3; pass++) {
				const availableWidth = Math.max(1, viewport.width - (vertical ? scrollbar : 0));
				const availableHeight = Math.max(1, viewport.height - (horizontal ? scrollbar : 0));
				scale = Math.min(4, Math.max(1, availableWidth - space * 2 - gap) / layoutWidth);
				if (mode === "height") scale = Math.min(scale, Math.max(1, availableHeight - space * 2) / height);
				const blockSpace = context.state.continuous ? 0 : Math.max(space, (availableHeight - height * scale) / 2);
				const rowGap = context.state.continuous ? 0 : Math.max(gap, blockSpace > space ? blockSpace + 1 : blockSpace);
				const nextVertical = contentHeight * scale + rowGap * (rows - 1) + blockSpace * 2 > availableHeight + 0.5;
				const nextHorizontal = contentWidth * scale + gap + space * 2 > availableWidth + 0.5;
				if ((!nextVertical || vertical) && (!nextHorizontal || horizontal)) break;
				vertical ||= nextVertical;
				horizontal ||= nextHorizontal;
			}
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
			const layoutChanged = scale !== context.state.scale || fitMode !== context.state.fitMode;
			context.state.fitMode = fitMode;
			context.state.scale = scale;
			if (layoutChanged) {
				context.clearRegionHighlights();
				pageView.layoutPages();
			}
			pageView.updateFitSpace();
			const gap = context.state.continuous ? 0 : Math.max(context.state.pageLayout > 1 ? PAGE_LAYOUT_GAP : 0, Number.parseFloat(getComputedStyle(context.el.svgHost).rowGap) || 0);
			if (context.state.pageWindow && context.state.pageWindow.gap !== gap) pageView.layoutPages();
			if (layoutChanged) {
				pageView.restoreScaleAnchor(anchor);
			}
			context.el.zoomLabel.textContent = `${Math.round(context.state.scale * 100)}%`;
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
			const probe = document.createElement("div");
			const content = document.createElement("div");
			probe.style.position = "absolute";
			probe.style.width = "100px";
			probe.style.height = "100px";
			probe.style.overflow = "scroll";
			probe.style.scrollbarWidth = getComputedStyle(context.el.viewerPanel).scrollbarWidth;
			probe.style.left = "-9999px";
			probe.append(content);
			document.body.append(probe);
			const width = 100 - Number.parseFloat(getComputedStyle(content).width);
			probe.remove();
			return width;
		},

		updateFitSpace() {
			const page = pageView.currentPageInfo();
			if (!page || context.state.fitMode === "free") {
				context.el.viewerPanel.classList.remove("single-page-fits-height");
				context.el.pageFrame.style.removeProperty("--fit-space");
				context.el.pageFrame.style.removeProperty("--fit-gap");
				return;
			}
			const shell = pageView.pageShell(context.state.pageIndex);
			const group = context.state.pageLayout > 1 ? pageView.pageGroupSize() : null;
			const height = group ? group.height * MM_TO_PX * context.state.scale
				: shell ? shell.getBoundingClientRect().height : Math.max(1, pageView.pageViewSize(page).height * MM_TO_PX * context.state.scale);
			const base = pageView.pageSpace();
			context.el.viewerPanel.classList.toggle("single-page-fits-height", context.state.doc.pageCount === 1 && height <= context.el.viewerPanel.clientHeight - base * 2);
			const space = context.state.continuous ? 0 : Math.max(base, (context.el.viewerPanel.clientHeight - height) / 2);
			const gap = space > base ? space + 1 : space;
			context.el.pageFrame.style.setProperty("--fit-space", `${space}px`);
			context.el.pageFrame.style.setProperty("--fit-gap", `${gap}px`);
		},

		releasePageResources(state = context.documentTab().state) {
			if (state.pageInFlight.size) return;
			const images = new Set(), fonts = new Set();
			for (const page of state.pageCache.values()) {
				for (const name of page.imageNames) images.add(name);
				for (const font of page.fonts || []) fonts.add(font.name);
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
		},

		trimWorkspaceCaches() {
			const budget = nativeHost?.cacheBytes || Math.max(PAGE_CACHE_BYTES, Math.min(256 << 20, engineLimits().memoryBytes / 4));
			const bytes = tab => [...tab.state.pageCache.values()].reduce((sum, page) => sum + (page.cacheBytes || 0), 0)
				+ [...tab.state.svgImages.values()].reduce((sum, image) => sum + (image.size || 0), 0)
				+ [...tab.state.svgFonts.values()].reduce((sum, face) => sum + (svgFontBytes.get(face) || 0), 0);
			let total = context.tabs.reduce((sum, tab) => sum + bytes(tab), 0);
			for (const tab of context.tabs.filter(tab => tab !== context.activeTab).sort((a, b) => a.touched - b.touched)) {
				if (total <= budget) break;
				const previous = bytes(tab);
				tab.state.pageCache.clear();
				pageView.releasePageResources(tab.state);
				total -= previous - bytes(tab);
			}
		},

		parseSVG(svgText, prefix = "", images = context.state.svgImages) {
			const template = document.createElement("template");
			template.innerHTML = svgText.replace(/xlink:href="(ofdgo-image-[a-f0-9]{64})"/g, (_, name) => `xlink:href="${images.get(name).url}"`);
			const svg = document.adoptNode(template.content.firstElementChild);
			pageView.prefixSVGIds(svg, prefix);
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
			let count = 0;
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
					if (++count % 128 === 0) yield;
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
