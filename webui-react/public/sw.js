// v4 does not ship a service worker — this stub exists solely so any
// browser that still has the v3 SW cached (registered from /sw.js at
// root scope) fetches an updated script, detects the "please go away"
// pattern, and unregisters itself on install. Net effect: one reload
// after the v4 cutover and the SPA runs without SW interception.
self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (event) => {
  event.waitUntil((async () => {
    try {
      const keys = await caches.keys();
      await Promise.all(keys.map((k) => caches.delete(k)));
    } catch {
      // ignore — cleanup is best-effort
    }
    await self.registration.unregister();
    const clients = await self.clients.matchAll();
    clients.forEach((c) => c.navigate(c.url));
  })());
});
