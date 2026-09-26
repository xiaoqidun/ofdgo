importScripts("./wasm_exec.js");

let pending = Promise.resolve();
const operations = new Map();
self.onmessage = ({ data }) => {
	if (data.type === "cancel") {
		operations.get(data.id)?.abort();
		return;
	}
	if (data.name === "ofdgoConvertFile" || data.name === "ofdgoPackFiles" || data.name === "ofdgoConvertPDF" || data.name === "ofdgoExportPage" || data.name === "ofdgoExportDocument" || data.name === "ofdgoExportAttachment" || data.name === "ofdgoSaveDocument" || data.name === "ofdgoSaveEncrypted" || data.name === "ofdgoSaveSigned" || data.name === "ofdgoImportPages") {
		operations.set(data.id, new AbortController());
	}
	pending = pending.then(() => data.name === "ofdgoConvertFile" || data.name === "ofdgoPackFiles" ? handleBatchMessage(data) : handleMessage(data));
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

async function handleBatchMessage({ id, name, args }) {
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
			const formats = JSON.parse(globalThis.ofdgoOutputFormats()).data;
			const format = formats.find(item => item.value === options.format);
			if (!format) throw new Error("不支持此格式");
			root = destination;
			const base = options.base.replace(/[<>:"/\\|?*\x00-\x1f]/g, "_").replace(/[. ]+$/g, "") || "document";
			const target = root ? await availableOutput(root, base, format) : { stem: base, name: format.paged ? base : `${base}.${format.extension}` };
			if (root && format.paged) {
				directory = await root.getDirectoryHandle(target.name, { create: true });
				created = target.name;
				self.postMessage({ id, type: "output", name: created });
			} else directory = root;
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
							self.postMessage({ id, type: "output", name: created });
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
			payload = await globalThis.ofdgoConvertFile(read(file), file.size, options, write,
				(phase, completed, total, done) => {
					self.postMessage({ id, type: "conversion", phase, completed, total });
					finish(done);
				},
				(action, pageName, done) => boundary(action, pageName).then(() => finish(done), err => finish(done, err)),
				done => finish(done), fonts);
		} else {
			const [entries, handle] = args;
			if (handle) output = await handle.createWritable();
			payload = await globalThis.ofdgoPackFiles(entries.map(entry => ({ name: entry.name, size: entry.file.size })),
				index => read(entries[index].file), write,
				(completed, total, done) => { self.postMessage({ id, type: "conversion", phase: "pack", completed, total }); finish(done); },
				done => finish(done));
		}
		signal.throwIfAborted();
		const result = typeof payload === "string" ? JSON.parse(payload) : payload;
		if (!result.ok) throw Object.assign(new Error(result.error), { code: result.code });
		if (name === "ofdgoPackFiles") {
			if (output) { await output.close(); output = null; }
			else result.data.blob = new Blob(chunks, { type: "application/zip" });
		} else {
			if (result.data.unchanged) {
				if (output) { await output.abort(); output = null; }
				if (root && created) { await root.removeEntry(created, { recursive: true }); created = null; }
				files = [];
			}
			result.data.files = files;
		}
		result.data.size = size;
		operations.delete(id);
		self.postMessage({ id, ...result });
	} catch (err) {
		await output?.abort().catch(() => {});
		let message = signal.aborted ? "转换已取消" : err.message;
		if (root && created) {
			try { await root.removeEntry(created, { recursive: true }); }
			catch (cleanupError) { message += `；清理失败：${cleanupError.message}`; }
		}
		self.postMessage({ id, ok: false, error: message, code: err.code, canceled: signal.aborted });
	} finally {
		credentials?.password?.fill(0);
		credentials?.key?.fill(0);
		credentials?.keyPassword?.fill(0);
		channel.port1.close();
		channel.port2.close();
		operations.delete(id);
	}
}
async function handleMessage({ id, name, args }) {
	let output;
	let channel;
	const signal = operations.get(id)?.signal;
	const importing = name === "ofdgoImportPages";
	const converting = name === "ofdgoConvertPDF";
	const saving = name === "ofdgoSaveDocument" || name === "ofdgoSaveEncrypted" || name === "ofdgoSaveSigned";
	const key = name === "ofdgoOpen" ? args[3]?.key : name === "ofdgoSaveSigned" || name === "ofdgoLoadImport" ? args[1]?.key : null;
	const keyPassword = name === "ofdgoOpen" ? args[3]?.keyPassword : name === "ofdgoSaveSigned" || name === "ofdgoLoadImport" ? args[1]?.keyPassword : null;
	const password = name === "ofdgoOpen" ? args[3]?.password : name === "ofdgoSaveEncrypted" || name === "ofdgoLoadImport" ? args[1]?.password : null;
	try {
		signal?.throwIfAborted();
		const chunks = [];
		let size = 0;
		if (signal) {
			channel = new MessageChannel();
			const finish = (done, err, commit = false) => {
				channel.port1.onmessage = () => {
					if (commit && !signal.aborted && !err) operations.delete(id);
					done(err?.message || "", signal.aborted);
				};
				channel.port2.postMessage(null);
			};
			if (importing || converting) {
				args.push((phase, completed, total, done) => {
					self.postMessage({ id, type: importing ? "import" : "conversion", phase, completed, total });
					finish(done, null, importing && phase === "commit");
				});
			} else {
				const file = args.pop();
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
						self.postMessage({ id, type: "export", stage: "pages", completed, total });
						finish(done);
					});
				} else if (saving) {
					args.push((phase, completed, total, done) => {
						self.postMessage({ id, type: "export", stage: "prepare", phase, completed, total });
						finish(done);
					});
					if (indices != null) args.push(indices);
				}
			}
		}
		const payload = await globalThis[name](...args);
		signal?.throwIfAborted();
		const result = typeof payload === "string" ? JSON.parse(payload) : payload;
		if (signal && !importing && !converting && result.ok) {
			operations.delete(id);
			result.data.size = size;
			self.postMessage({ id, type: "export", stage: "save" });
			if (output) {
				await output.close();
			} else {
				result.data.blob = new Blob(chunks, { type: result.data.mime });
			}
		} else if (output) {
			await output.abort();
		}
		output = null;
		const transfer = result.data?.bytes ? [result.data.bytes.buffer] : (result.data?.images || []).map((image) => image.bytes.buffer);
		self.postMessage({ id, ...result }, transfer);
	} catch (err) {
		await output?.abort().catch(() => {});
		const result = { id, ok: false, error: err.message };
		if (signal?.aborted) {
			result.error = importing ? "导入已取消" : converting ? "转换已取消" : "导出已取消";
			result.canceled = true;
		}
		self.postMessage(result);
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
	self.postMessage({ type: "exit", error: err?.message || "引擎运行中断" });
	self.close();
}

async function loadWASM() {
	const go = new Go();
	self.postMessage({ type: "progress", text: "正在下载引擎", percent: 16 });
	const response = await fetch("./ofdgo.wasm");
	self.postMessage({ type: "progress", text: "正在编译引擎", percent: 35 });
	const { instance } = await WebAssembly.instantiateStreaming(response, go.importObject);
	self.postMessage({ type: "progress", text: "正在启动引擎", percent: 58 });
	go.run(instance).then(exitWASM, exitWASM);
	if (!go.exited) {
		const backends = JSON.parse(globalThis.ofdgoRenderBackends());
		if (!backends.ok) throw new Error(backends.error);
		self.postMessage({ type: "ready", backends: backends.data });
	}
}

loadWASM().catch(exitWASM);
