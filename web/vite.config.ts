import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { VitePWA } from 'vite-plugin-pwa'

// In development the Go server runs on :8089 and Vite proxies /api to it.
export default defineConfig({
  plugins: [
    react(),
    VitePWA({
      registerType: 'autoUpdate',
      includeAssets: ['favicon.svg', 'icons/*.png'],
      manifest: {
        name: 'OpenDVR',
        short_name: 'OpenDVR',
        description: 'Live TV from your HDHomeRun',
        id: '/',
        start_url: '/',
        scope: '/',
        display: 'standalone',
        // Once installed, links to this server open in the app where the
        // browser supports it (Chrome on Android does so on its own).
        launch_handler: { client_mode: ['navigate-existing', 'auto'] },
        handle_links: 'preferred',
        background_color: '#0f1115',
        theme_color: '#0f1115',
        icons: [
          { src: '/icons/icon-192.png', sizes: '192x192', type: 'image/png' },
          { src: '/icons/icon-512.png', sizes: '512x512', type: 'image/png' },
          { src: '/icons/maskable-512.png', sizes: '512x512', type: 'image/png', purpose: 'maskable' },
        ],
      },
      workbox: {
        // The app shell is precached so the PWA opens offline and loads
        // instantly. Nothing under /api, /mcp or /auth is ever cached:
        // playlists and segments are live and auth responses are private.
        // Server-side redirects and files (/tv, /downloads, /.well-known)
        // must reach the server too, or the SPA shell answers for them.
        globPatterns: ['**/*.{js,css,html,svg,png,webmanifest}'],
        navigateFallback: '/index.html',
        // Workbox tests these against pathname plus query string, so /tv
        // must also match /tv?code=…, which is what the QR code opens.
        navigateFallbackDenylist: [/^\/api\//, /^\/mcp/, /^\/auth\//, /^\/tv(\?|$)/, /^\/downloads\//, /^\/\.well-known\//],
        runtimeCaching: [],
        cleanupOutdatedCaches: true,
        maximumFileSizeToCacheInBytes: 3 * 1024 * 1024,
      },
    }),
  ],
  server: {
    port: 5173,
    proxy: { '/api': 'http://localhost:8089', '/auth': 'http://localhost:8089' },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    rollupOptions: { output: { manualChunks: { hls: ['hls.js'] } } },
  },
})
