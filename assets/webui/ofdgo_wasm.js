importScripts("./wasm_exec.js");

let pending = Promise.resolve();
const exports = new Map();
self.onmessage = ({ data }) => {
	if (data.type === "cancel") {
		exports.get(data.id)?.abort();
		return;
	}
	if (data.name === "ofdgoExportPage" || data.name === "ofdgoExportDocument" || data.name === "ofdgoExportAttachment" || data.name === "ofdgoSaveDocument") {
		exports.set(data.id, new AbortController());
	}
	pending = pending.then(() => handleMessage(data));
};
async function handleMessage({ id, name, args }) {
	let output;
	let channel;
	const signal = exports.get(id)?.signal;
	try {
		signal?.throwIfAborted();
		const chunks = [];
		let size = 0;
		if (signal) {
			const file = args.pop();
			if (file) {
				output = await file.createWritable();
			}
			signal.throwIfAborted();
			channel = new MessageChannel();
			const finish = (done, err) => {
				channel.port1.onmessage = () => done(err?.message || "", signal.aborted);
				channel.port2.postMessage(null);
			};
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
			}
		}
		const payload = await globalThis[name](...args);
		signal?.throwIfAborted();
		const result = typeof payload === "string" ? JSON.parse(payload) : payload;
		if (signal && result.ok) {
			exports.delete(id);
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
			result.error = "导出已取消";
			result.canceled = true;
		}
		self.postMessage(result);
	} finally {
		channel?.port1.close();
		channel?.port2.close();
		exports.delete(id);
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
		self.postMessage({ type: "ready" });
	}
}

loadWASM().catch(exitWASM);
