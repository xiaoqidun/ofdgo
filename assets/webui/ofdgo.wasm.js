importScripts("./wasm_exec.js");

self.onmessage = ({ data: { id, name, args } }) => {
	try {
		const payload = globalThis[name](...args);
		const result = typeof payload === "string" ? JSON.parse(payload) : payload;
		self.postMessage({ id, ...result }, result.data?.bytes ? [result.data.bytes.buffer] : []);
	} catch (err) {
		self.postMessage({ id, ok: false, error: err.message });
	}
};

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
