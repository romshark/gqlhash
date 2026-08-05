// The offline cache. dist/sw.js is this file with the build's file list
// written in. See the serviceWorker plugin in ../vite.config.ts.

const VERSION = "__VERSION__";
const SHELL = ["__SHELL__"];
const BINARY = "__BINARY__";

// Two caches. The shell belongs to one build and is replaced whole. The
// binary is named by its content and survives builds that leave it alone.
// It is three quarters of the download.
const SHELL_CACHE = `gqlhash-shell-${VERSION}`;
const BINARY_CACHE = "gqlhash-binary";

// no-cache, not the default. Pages serves with max-age=600, and a stale
// shell can name assets the new deploy has replaced. The ETag makes it a 304.
self.addEventListener("install", (event) => {
  event.waitUntil(
    (async () => {
      const cache = await caches.open(SHELL_CACHE);
      await Promise.all(
        SHELL.map(async (file) => {
          const response = await fetch(file, { cache: "no-cache" });
          if (response.status !== 200) {
            throw new Error(`precaching ${file}: ${response.status}`);
          }
          await cache.put(file, response);
        }),
      );
    })(),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    (async () => {
      // Every other build's shell.
      for (const name of await caches.keys()) {
        if (name !== SHELL_CACHE && name !== BINARY_CACHE) {
          await caches.delete(name);
        }
      }

      // Every binary but this build's.
      const cache = await caches.open(BINARY_CACHE);
      const current = new URL(BINARY, self.location).href;
      for (const request of await cache.keys()) {
        if (request.url !== current) {
          await cache.delete(request);
        }
      }

      await self.clients.claim();
    })(),
  );
});

self.addEventListener("fetch", (event) => {
  const { request } = event;
  if (request.method !== "GET") {
    return;
  }
  if (new URL(request.url).origin !== self.location.origin) {
    return;
  }
  event.respondWith(
    request.mode === "navigate" ? document_(request) : asset(request),
  );
});

/**
 * document_ answers a navigation from the network, falling back to the cache.
 * The document names the hashed assets. A stale one names assets this build
 * does not have.
 */
async function document_(request) {
  try {
    const response = await fetch(request.url, { cache: "no-cache" });
    if (response.status === 200) {
      const cache = await caches.open(SHELL_CACHE);
      await cache.put(request, response.clone());
    }
    return response;
  } catch (error) {
    const cached = await caches.match(request, { ignoreSearch: true });
    if (cached) {
      return cached;
    }
    throw error;
  }
}

/**
 * asset answers everything else from either cache. The names are
 * content-hashed. A hit is never stale. A miss is stored. That is how the
 * binary gets in: the page fetches it on every load.
 */
async function asset(request) {
  const cached = await caches.match(request, { ignoreSearch: true });
  if (cached) {
    return cached;
  }

  const response = await fetch(request);
  if (response.status === 200) {
    const cache = await caches.open(BINARY_CACHE);
    await cache.put(request, response.clone());
  }
  return response;
}
