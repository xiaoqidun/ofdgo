importScripts("./wasm_exec.js");

const engines = new Map();
let resolveRuntime;
let rejectRuntime;
const runtimeReady = new Promise((resolve, reject) => { resolveRuntime = resolve; rejectRuntime = reject; });
runtimeReady.catch(() => {});
let fontSequence = 0;
let engineMemory = null;

function requestFont(engine, id, signal, name, done) {
	const fontRequests = engine.fontRequests;
	if (signal.aborted) { done(null, "", true); return; }
	const request = ++fontSequence;
	const cancel = () => {
		fontRequests.delete(request);
		done(null, "", true);
	};
	signal.addEventListener("abort", cancel, { once: true });
	fontRequests.set(request, result => {
		signal.removeEventListener("abort", cancel);
		done(result.bytes, result.error || "", signal.aborted);
	});
	self.postMessage({ engineID: engine.id, id, type: "font", request, name });
}
self.onmessage = ({ data }) => {
	if (data.type === "module") {
		if (data.error) exitWASM(new Error(data.error));
		else loadWASM(data.module).catch(exitWASM);
		return;
	}
	const engineID = data.engineID;
	if (typeof engineID !== "string" || !engineID) {
		self.postMessage({ engineID, id: data.id, ok: false, error: "missing engine ID" });
		return;
	}
	if (data.type === "open") {
		if (engines.has(engineID)) {
			self.postMessage({ engineID, type: "exit", error: "engine already exists" });
			return;
		}
		const engine = { id: engineID, operations: new Map(), fontRequests: new Map(), active: new Set(), closed: false };
		engines.set(engineID, engine);
		engine.pending = runtimeReady.then(async () => {
			if (engine.closed) return;
			const opened = JSON.parse(globalThis.ofdgoEngineOpen(engineID));
			if (!opened.ok) throw new Error(opened.error);
			const backends = JSON.parse(await globalThis.ofdgoEngineCall(engineID, "ofdgoRenderBackends", null));
			if (!backends.ok) throw new Error(backends.error);
			if (!engine.closed) self.postMessage({ engineID, type: "ready", backends: backends.data, memory: engineMemory.buffer.byteLength });
		}).catch(error => {
			engine.closed = true;
			self.postMessage({ engineID, type: "exit", error: error.message });
		});
		return;
	}
	const engine = engines.get(engineID);
	if (!engine && data.type === "close") {
		self.postMessage({ engineID, type: "closed" });
		return;
	}
	if (!engine || engine.closed && data.type !== "close") {
		self.postMessage({ engineID, id: data.id, ok: false, error: "unknown or closed engine" });
		return;
	}
	if (data.type === "close") {
		engine.closed = true;
		for (const operation of engine.operations.values()) operation.abort();
		engine.pending.then(async () => {
			await Promise.all([...engine.active]);
			await runtimeReady;
			globalThis.ofdgoEngineClose(engineID);
			engines.delete(engineID);
			self.postMessage({ engineID, type: "closed" });
		}).catch(() => engines.delete(engineID));
		return;
	}
	if (data.type === "font") {
		engine.fontRequests.get(data.request)?.(data);
		engine.fontRequests.delete(data.request);
		return;
	}
	if (data.type === "cancel") {
		engine.operations.get(data.id)?.abort();
		return;
	}
	engine.operations.set(data.id, new AbortController());
	engine.pending = engine.pending.then(() => {
		const operation = (data.name === "ofdgoConvertFile" || data.name === "ofdgoPackFiles" ? handleBatchMessage(engine, data) : handleMessage(engine, data)).catch(error => {
			engine.operations.delete(data.id);
			self.postMessage({ engineID, id: data.id, ok: false, error: error.message });
		});
		engine.active.add(operation);
		operation.finally(() => engine.active.delete(operation));
		if (data.name !== "ofdgoSaveSnapshot") return operation;
	});
};

async function availableOutput(directory, base, format) {
	for (let suffix = 0; ; suffix++) {
		const stem = suffix ? `${base} (${suffix + 1})` : base;
		const name = format.paged ? stem : `${stem}.${format.extension}`;
		try {
			await directory.getFileHandle(name);
		} catch (err) {
			if (err.name === "NotFoundError") return { stem, name };
			if (err.name !== "TypeMismatchError") throw err;
		}
	}
}

async function handleBatchMessage(engine, { id, name, args }) {
	const operations = engine.operations;
	const postMessage = (data, transfer) => self.postMessage({ ...data, engineID: engine.id }, transfer);
	const signal = operations.get(id).signal;
	const channel = new MessageChannel();
	const finish = (done, err) => {
		channel.port1.onmessage = () => done(err?.message || "", signal.aborted);
		channel.port2.postMessage(null);
	};
	const reader = new FileReaderSync();
	const read = file => (offset, size) => new Uint8Array(reader.readAsArrayBuffer(file.slice(offset, offset + size)));
	let output = null;
	let chunks = [];
	let files = [];
	let directory = null;
	let created = null;
	let root = null;
	let current = null;
	let size = 0;
	let credentials = null;
	const write = (bytes, done) => {
		if (signal.aborted) return finish(done);
		size += bytes.byteLength;
		if (output) output.write(bytes).then(() => finish(done), err => finish(done, err));
		else { chunks.push(new Blob([bytes])); finish(done); }
	};
	try {
		signal.throwIfAborted();
		let payload;
		if (name === "ofdgoConvertFile") {
			const [file, options, fonts, destination] = args;
			credentials = options.credentials;
			const formats = JSON.parse(await globalThis.ofdgoEngineCall(engine.id, "ofdgoOutputFormats", null)).data;
			const format = formats.find(item => item.value === options.format);
			if (!format) throw new Error("不支持此格式");
			root = destination;
			const base = options.base.replace(/[<>:"/\\|?*\x00-\x1f]/g, "_").replace(/[. ]+$/g, "") || "document";
			const target = root ? await navigator.locks.request("ofdgo-convert-output", { signal }, async () => {
				signal.throwIfAborted();
				const target = await availableOutput(root, base, format);
				if (format.paged) directory = await root.getDirectoryHandle(target.name, { create: true });
				else { await root.getFileHandle(target.name, { create: true }); directory = root; }
				created = target.name;
				postMessage({ id, type: "output", name: created });
				return target;
			}) : { stem: base, name: format.paged ? base : `${base}.${format.extension}` };
			const boundary = async (action, pageName) => {
				signal.throwIfAborted();
				if (action === "open") {
					const fileName = format.paged ? pageName : target.name;
					current = { name: format.paged ? `${target.stem}/${pageName}` : fileName, mime: format.mime };
					chunks = [];
					if (directory) {
						const handle = await directory.getFileHandle(fileName, { create: true });
						if (!format.paged) {
							created = fileName;
							postMessage({ id, type: "output", name: created });
						}
						current.handle = handle;
						output = await handle.createWritable();
					}
				} else {
					if (output) { await output.close(); output = null; }
					if (options.archive) current.file = current.handle ? await current.handle.getFile() : new File(chunks, pageName, { type: format.mime });
					delete current.handle;
					files.push(current);
					chunks = [];
				}
			};
			payload = await globalThis.ofdgoEngineCall(engine.id, "ofdgoConvertFile", done => finish(done), read(file), file.size, options, write,
				(phase, completed, total, done) => {
					postMessage({ id, type: "conversion", phase, completed, total, memory: engineMemory.buffer.byteLength });
					finish(done);
				},
				(action, pageName, done) => boundary(action, pageName).then(() => finish(done), err => finish(done, err)),
				done => finish(done), fonts, (name, done) => requestFont(engine, id, signal, name, done));
		} else {
			const [entries, handle, compression] = args;
			if (handle) output = await handle.createWritable();
			payload = await globalThis.ofdgoEngineCall(engine.id, "ofdgoPackFiles", done => finish(done), entries.map(entry => ({ name: entry.name, size: entry.file.size })),
				index => read(entries[index].file), write,
				(completed, total, done) => { postMessage({ id, type: "conversion", phase: "pack", completed, total, memory: engineMemory.buffer.byteLength }); finish(done); },
				done => finish(done), compression);
		}
		signal.throwIfAborted();
		const result = typeof payload === "string" ? JSON.parse(payload) : payload;
		if (!result.ok) throw Object.assign(new Error(result.error), { code: result.code });
		if (name === "ofdgoPackFiles") {
			if (output) { await output.close(); output = null; }
			else result.data.blob = new Blob(chunks, { type: "application/zip" });
		} else {
			if (result.data.unchanged && !result.data.bytes) {
				if (output) { await output.abort(); output = null; }
				if (root && created) { await root.removeEntry(created, { recursive: true }); created = null; }
				files = [];
			}
			result.data.files = files;
		}
		result.data.size = size;
		operations.delete(id);
		postMessage({ id, ...result, memory: engineMemory.buffer.byteLength });
	} catch (err) {
		await output?.abort().catch(() => {});
		let message = signal.aborted ? "转换已取消" : err.message;
		if (root && created) {
			try { await root.removeEntry(created, { recursive: true }); }
			catch (cleanupError) { message += `；清理失败：${cleanupError.message}`; }
		}
		postMessage({ id, ok: false, error: message, code: err.code, canceled: signal.aborted, memory: engineMemory.buffer.byteLength });
	} finally {
		credentials?.password?.fill(0);
		credentials?.key?.fill(0);
		credentials?.keyPassword?.fill(0);
		channel.port1.close();
		channel.port2.close();
		operations.delete(id);
	}
}
async function handleMessage(engine, { id, name, args }) {
	const operations = engine.operations;
	const postMessage = (data, transfer) => self.postMessage({ ...data, engineID: engine.id }, transfer);
	let output;
	let saveFile;
	let saveStamp;
	let channel;
	const signal = operations.get(id)?.signal;
	const cancellable = ["ofdgoPreparePrint", "ofdgoLayoutPrint", "ofdgoPrintSide", "ofdgoPrintPDF", "ofdgoResources", "ofdgoPrepareSave", "ofdgoSaveSnapshot", "ofdgoImportDocuments", "ofdgoImportImages", "ofdgoExportImages", "ofdgoOpenPDF", "ofdgoExportPage", "ofdgoExportDocument", "ofdgoExportAttachment", "ofdgoExportResource", "ofdgoExportMedia", "ofdgoExportVersion", "ofdgoSaveDocument", "ofdgoSaveSigned", "ofdgoImportPages"].includes(name);
	const importing = name === "ofdgoImportPages" || name === "ofdgoImportImages" || name === "ofdgoImportDocuments";
	const converting = name === "ofdgoOpenPDF";
	const saving = name === "ofdgoSaveDocument" || name === "ofdgoSaveSnapshot" || name === "ofdgoSaveSigned";
	const resources = ["ofdgoPreparePrint", "ofdgoLayoutPrint", "ofdgoPrintSide", "ofdgoPrintPDF", "ofdgoResources", "ofdgoPrepareSave"].includes(name);
	if (engine.pdf) {
		const checked = JSON.parse(await globalThis.ofdgoEngineCall(engine.id, "ofdgoCheckDocument", null, name));
		if (!checked.ok) { postMessage({id, ...checked}); operations.delete(id); return; }
	}
	const key = name === "ofdgoOpen" ? args[3]?.key : name === "ofdgoSaveSigned" || name === "ofdgoLoadImport" ? args[1]?.key : null;
	const keyPassword = name === "ofdgoOpen" ? args[3]?.keyPassword : name === "ofdgoSaveSigned" || name === "ofdgoLoadImport" ? args[1]?.keyPassword : null;
	const password = converting ? args[2] : name === "ofdgoSetEncryption" ? args[0]?.password : name === "ofdgoOpen" ? args[3]?.password : name === "ofdgoLoadImport" ? args[1]?.password : null;
	try {
		signal?.throwIfAborted();
		if (["ofdgoOpen", "ofdgoOpenPDF", "ofdgoLoadImport"].includes(name) && args[0] instanceof Blob) {
			const file = args[0], reader = new FileReaderSync();
			args[0] = {size: file.size, read: (offset, size) => new Uint8Array(reader.readAsArrayBuffer(file.slice(offset, offset + size)))};
		}
		if (name === "ofdgoDetectFormat") {
			const file = args[0], reader = new FileReaderSync();
			channel = new MessageChannel();
			args = [(offset, size) => new Uint8Array(reader.readAsArrayBuffer(file.slice(offset, offset + size))), file.size, done => {
				channel.port1.onmessage = () => done("", false);
				channel.port2.postMessage(null);
			}];
		}
		if (name === "ofdgoChangeAttachment" && (args[0] === "add" || args[0] === "replace")) {
			const files = args[3], reader = new FileReaderSync();
			args[3] = index => {
				try { return { bytes: new Uint8Array(reader.readAsArrayBuffer(files[index])) }; }
				catch (err) { return { error: err.message }; }
			};
		}
		if (name === "ofdgoChangeResource" && (args[0] === "add" || args[0] === "replace")) {
			args[4] = new Uint8Array(new FileReaderSync().readAsArrayBuffer(args[4]));
		}
		if (["ofdgoImagePageSize", "ofdgoImagePreview"].includes(name) && args[0] instanceof Blob) args[0] = new Uint8Array(new FileReaderSync().readAsArrayBuffer(args[0]));
		if (name === "ofdgoImportImages") {
			const files = args[0], reader = new FileReaderSync();
			args[0] = files.map(file => file.name);
			args.push(index => {
				try { return { bytes: new Uint8Array(reader.readAsArrayBuffer(files[index])) }; }
				catch (err) { return { error: err.message }; }
			});
		}
		const chunks = [];
		let size = 0;
		if (cancellable) {
			channel = new MessageChannel();
			const finish = (done, err, commit = false) => {
				channel.port1.onmessage = () => {
					if (commit && !signal.aborted && !err) operations.delete(id);
					done(err?.message || "", signal.aborted);
				};
				channel.port2.postMessage(null);
			};
			if (resources) {
				args.push(done => finish(done));
			} else if (importing || converting) {
				args.push((phase, completed, total, done) => {
					postMessage({ id, type: importing ? "import" : "conversion", phase, completed, total });
					finish(done, null, importing && phase === "commit");
				});
				if (converting) args.push((name, done) => requestFont(engine, id, signal, name, done));
			} else {
				const destination = args.pop();
				const file = destination?.fileHandle || destination;
				saveFile = file;
				saveStamp = destination?.saveStamp;
				const compression = saving && name !== "ofdgoSaveSigned" && args.at(-1)?.mode !== undefined ? args.pop() : null;
				const indices = saving ? args.shift() : null;
				if (file) output = await file.createWritable();
				signal.throwIfAborted();
				args.push((bytes, done) => {
					if (signal.aborted) {
						finish(done);
						return;
					}
					size += bytes.length;
					if (output) {
						output.write(bytes).then(() => finish(done), (err) => finish(done, err));
					} else {
						chunks.push(new Blob([bytes]));
						finish(done);
					}
				});
				if (name === "ofdgoExportDocument") {
					args.push((completed, total, done) => {
						postMessage({ id, type: "export", stage: "pages", completed, total });
						finish(done);
					});
				} else if (name === "ofdgoExportVersion") {
					args.push((phase, completed, total, done) => {
						postMessage({ id, type: "export", stage: "prepare", phase, completed, total });
						finish(done);
					}, done => finish(done));
				} else if (saving) {
					args.push((phase, completed, total, done) => {
						postMessage({ id, type: "export", stage: "prepare", phase, completed, total });
						finish(done);
					});
					if (compression || name === "ofdgoSaveSnapshot") args.push(indices, compression || {mode: 0}, done => finish(done));
					else if (indices != null) args.push(indices);
				}
				if (!saving && args.some(value => value?.mode !== undefined)) args.push(done => finish(done));
			}
		}
		if (engine.pdf && name !== "ofdgoOpenPDF") {
			channel ||= new MessageChannel();
			const context = await globalThis.ofdgoEngineCall(engine.id, "ofdgoPDFContext", null, (phase, completed, total, done) => {
				postMessage({id, type: "conversion", phase, completed, total});
				channel.port1.onmessage = () => done("", signal.aborted);
				channel.port2.postMessage(null);
			}, (name, done) => requestFont(engine, id, signal, name, done));
			engine.pdf = JSON.parse(context).data === true;
		}
		const started = name === "ofdgoOpen" || name === "ofdgoRenderPage" ? performance.now() : null;
		const payload = await globalThis.ofdgoEngineCall(engine.id, name, done => {
			channel ||= new MessageChannel();
			channel.port1.onmessage = () => done("", signal.aborted);
			channel.port2.postMessage(null);
		}, ...args);
		signal?.throwIfAborted();
		const result = typeof payload === "string" ? JSON.parse(payload) : payload;
		if (result.ok && name === "ofdgoOpenPDF") engine.pdf = true;
		if (result.ok && (name === "ofdgoOpen" || name === "ofdgoLoadPDF" && result.data.done)) engine.pdf = false;
		if (started !== null && result.ok) result.elapsed = performance.now() - started;
		if (cancellable && !importing && !converting && !resources && result.ok) {
			result.data.size = size;
			if (name !== "ofdgoExportMedia") postMessage({ id, type: "export", stage: "save" });
			if (output) {
				if (saveStamp) {
					const current = await saveFile.getFile();
					if (current.size !== saveStamp.size || current.lastModified !== saveStamp.modified) throw new Error("文件已被修改，请另存");
				}
				signal.throwIfAborted();
				operations.delete(id);
				await output.close();
			} else {
				result.data.blob = new Blob(chunks, { type: result.data.mime });
			}
		} else if (output) {
			await output.abort();
		}
		output = null;
		const transfer = [...new Set([result.data?.bytes, ...(result.data?.images || []).map(image => image.bytes), ...(result.data?.fonts || []).map(font => font.data)].filter(bytes => bytes?.byteLength).map(bytes => bytes.buffer))];
		postMessage({ id, ...result, memory: engineMemory.buffer.byteLength }, transfer);
	} catch (err) {
		await output?.abort().catch(() => {});
		const result = { id, ok: false, error: err.message, errorName: err.name };
		if (signal?.aborted) {
			result.error = resources ? "查询已取消" : importing ? "导入已取消" : converting ? "转换已取消" : cancellable ? "导出已取消" : "操作已取消";
			result.canceled = true;
		}
		postMessage(result);
	} finally {
		password?.fill(0);
		key?.fill(0);
		keyPassword?.fill(0);
		channel?.port1.close();
		channel?.port2.close();
		operations.delete(id);
	}
}

function exitWASM(err) {
	rejectRuntime(err || new Error("引擎运行中断"));
	self.postMessage({ type: "exit", error: err?.message || "引擎运行中断" });
	self.close();
}

async function loadWASM(module) {
	const go = new Go();
	const instance = await WebAssembly.instantiate(module, go.importObject);
	engineMemory = instance.exports.mem;
	self.postMessage({ type: "progress", phase: "start", text: "正在启动引擎", percent: 58 });
	go.run(instance).then(exitWASM, exitWASM);
	if (!go.exited) {
		resolveRuntime();
	}
}

self.postMessage({ type: "module" });
