const CACHE_PREFIX = `ofdgo:${self.registration.scope}:app:`;
const META_CACHE = `${CACHE_PREFIX}meta`;
const ASSETS = ["./", "ofdgo.css", "ofdgo.js", "wasm_exec.js", "ofdgo.wasm", "ofdgo.sw.js"]
	.map((path) => new URL(path, self.registration.scope).href);
const CLIENT_PREFIX = new URL("__client/", self.registration.scope).href;

self.addEventListener("install", (event) => {
	event.waitUntil(caches.open(META_CACHE));
});

self.addEventListener("fetch", (event) => {
	const url = resourceURL(event.request.url);
	const navigation = event.request.mode === "navigate";
	if (event.request.method !== "GET" || !url.startsWith(self.registration.scope) || (navigation && url !== self.registration.scope)) {
		return;
	}
	event.respondWith(navigator.locks.request(CACHE_PREFIX, { mode: navigation ? "exclusive" : "shared" }, () => readResource(event, navigation)));
});

self.addEventListener("message", (event) => {
	const type = event.data?.type;
	const port = event.ports[0];
	if (!port || !event.source || (type !== "prepare" && type !== "refresh")) {
		return;
	}
	event.waitUntil((async () => {
		try {
			const reload = await navigator.locks.request(CACHE_PREFIX, async () => {
				const meta = await caches.open(META_CACHE);
				await cleanBundles(meta);
				if (type === "refresh") {
					await updateBundle(meta);
					return true;
				}
				const bundle = await readBundle(meta, CLIENT_PREFIX + event.source.id);
				if (await isBundleComplete(bundle)) {
					return false;
				}
				await currentBundle(meta);
				return true;
			});
			if (type === "refresh") {
				await self.skipWaiting();
			}
			port.postMessage({ ok: true, reload });
		} catch (err) {
			port.postMessage({ error: err.message });
		} finally {
			port.close();
		}
	})());
});

function resourceURL(value) {
	const url = new URL(value);
	url.search = "";
	url.hash = "";
	if (url.href === new URL("index.html", self.registration.scope).href) {
		return self.registration.scope;
	}
	return url.href;
}

async function readBundle(meta, key) {
	const response = await meta.match(key);
	return response ? response.json() : null;
}

async function isBundleComplete(bundle) {
	if (!bundle || !await caches.has(bundle.name)) {
		return false;
	}
	const cache = await caches.open(bundle.name);
	const keys = await cache.keys();
	return bundle.assets.every((url) => keys.some((key) => key.url === url));
}

async function currentBundle(meta) {
	const bundle = await readBundle(meta, self.registration.scope);
	return await isBundleComplete(bundle) ? bundle : updateBundle(meta);
}

async function readResource(event, navigation) {
	const meta = await caches.open(META_CACHE);
	let bundle;
	if (navigation) {
		bundle = await currentBundle(meta);
		await meta.put(CLIENT_PREFIX + event.resultingClientId, Response.json(bundle));
	} else {
		bundle = await readBundle(meta, CLIENT_PREFIX + event.clientId);
	}
	if (!bundle || !await caches.has(bundle.name)) {
		return Response.error();
	}
	const url = resourceURL(event.request.url);
	if (!bundle.assets.includes(url)) {
		return fetch(event.request);
	}
	const cache = await caches.open(bundle.name);
	return await cache.match(url) || Response.error();
}

async function updateBundle(meta) {
	const resources = await Promise.all(ASSETS.map(async (url) => {
		const response = await fetch(new Request(url, { cache: "no-store" }));
		if (!response.ok || response.redirected) {
			throw new Error("应用资源下载失败");
		}
		return { url, response };
	}));
	if (resources.some(({ response }) => response.headers.get("X-OFDGo-Checksum") !== BUNDLE_CHECKSUM)) {
		throw new Error("应用资源版本不一致");
	}
	const bundle = { name: CACHE_PREFIX + crypto.randomUUID(), assets: ASSETS };
	try {
		const cache = await caches.open(bundle.name);
		for (const { url, response } of resources) {
			await cache.put(url, response);
		}
		await meta.put(self.registration.scope, Response.json(bundle));
	} catch (err) {
		await caches.delete(bundle.name);
		throw err;
	}
	return bundle;
}

async function cleanBundles(meta) {
	const current = await readBundle(meta, self.registration.scope);
	const keep = new Set([META_CACHE, current?.name]);
	for (const key of await meta.keys()) {
		if (!key.url.startsWith(CLIENT_PREFIX)) {
			continue;
		}
		const client = await self.clients.get(key.url.slice(CLIENT_PREFIX.length));
		if (client) {
			keep.add((await readBundle(meta, key)).name);
		} else {
			await meta.delete(key);
		}
	}
	for (const name of await caches.keys()) {
		if (name.startsWith(CACHE_PREFIX) && !keep.has(name)) {
			await caches.delete(name);
		}
	}
}
