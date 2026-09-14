importScripts("./wasm_exec.js");

let pending = Promise.resolve();
self.onmessage = ({ data }) => {
	pending = pending.then(() => handleMessage(data));
};
async function handleMessage({ id, name, args }) {
	let output;
	try {
		const chunks = [];
		let size = 0;
		const exporting = name === "ofdgoExportPage" || name === "ofdgoExportDocument";
		if (exporting) {
			const file = args.pop();
			if (file) {
				output = await file.createWritable();
			}
			args.push((bytes, done) => {
				size += bytes.length;
				if (output) {
					output.write(bytes).then(() => done(), (err) => done(err.message));
				} else {
					chunks.push(new Blob([bytes]));
					setTimeout(done, 0);
				}
			});
			if (name === "ofdgoExportDocument") {
				args.push((completed, total) => {
					self.postMessage({ id, type: "export", stage: "pages", completed, total });
				});
			}
		}
		const payload = await globalThis[name](...args);
		const result = typeof payload === "string" ? JSON.parse(payload) : payload;
		if (exporting && result.ok) {
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
		self.postMessage({ id, ...result }, result.data?.bytes ? [result.data.bytes.buffer] : []);
	} catch (err) {
		await output?.abort().catch(() => {});
		self.postMessage({ id, ok: false, error: err.message });
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
