const CACHE_NAME = 'agy-ide-v5';
const ASSETS = [
  '/',
  '/login-pwd',
  '/manifest.json',
  '/icon-192.png',
  '/icon-512.png'
];

self.addEventListener('install', (e) => {
  e.waitUntil(
    caches.open(CACHE_NAME).then((cache) => {
      return cache.addAll(ASSETS);
    }).then(() => self.skipWaiting())
  );
});

self.addEventListener('activate', (e) => {
  e.waitUntil(
    caches.keys().then((keys) => {
      return Promise.all(
        keys.map((key) => {
          if (key !== CACHE_NAME) {
            return caches.delete(key);
          }
        })
      );
    }).then(() => self.clients.claim())
  );
});

self.addEventListener('fetch', (e) => {
  if (e.request.method !== 'GET') return;

  const url = new URL(e.request.url);
  if (url.protocol !== 'http:' && url.protocol !== 'https:') {
    return;
  }

  if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/ws')) {
    return;
  }

  e.respondWith(
    fetch(e.request).then((res) => {
      if (!res || res.status !== 200 || res.type !== 'basic') {
        return res;
      }
      const responseToCache = res.clone();
      caches.open(CACHE_NAME).then((cache) => {
        cache.put(e.request, responseToCache);
      });
      return res;
    }).catch(() => {
      return caches.match(e.request);
    })
  );
});

self.addEventListener('notificationclick', (e) => {
  e.notification.close();
  e.waitUntil(
    self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((clientList) => {
      for (const client of clientList) {
        if ('focus' in client) {
          return client.focus();
        }
      }
      if (self.clients.openWindow) {
        return self.clients.openWindow('/');
      }
    })
  );
});
