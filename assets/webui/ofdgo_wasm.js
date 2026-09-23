importScripts("./wasm_exec.js");

let pending = Promise.resolve();
const operations = new Map();
self.onmessage = ({ data }) => {
	if (data.type === "cancel") {
		operations.get(data.id)?.abort();
		return;
	}
	if (data.name === "ofdgoExportPage" || data.name === "ofdgoExportDocument" || data.name === "ofdgoExportAttachment" || data.name === "ofdgoSaveDocument" || data.name === "ofdgoImportPages") {
		operations.set(data.id, new AbortController());
	}
	pending = pending.then(() => handleMessage(data));
};
async function handleMessage({ id, name, args }) {
	let output;
	let channel;
	const signal = operations.get(id)?.signal;
	const importing = name === "ofdgoImportPages";
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
			if (importing) {
				args.push((phase, completed, total, done) => {
					self.postMessage({ id, type: "import", phase, completed, total });
					finish(done, null, phase === "commit");
				});
			} else {
				const file = args.pop();
				const indices = name === "ofdgoSaveDocument" ? args.shift() : null;
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
				} else if (name === "ofdgoSaveDocument") {
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
		if (signal && !importing && result.ok) {
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
			result.error = importing ? "导入已取消" : "导出已取消";
			result.canceled = true;
		}
		self.postMessage(result);
	} finally {
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
