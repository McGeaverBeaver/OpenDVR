import React from 'react'
import ReactDOM from 'react-dom/client'
import { Navigate, createBrowserRouter, RouterProvider } from 'react-router-dom'
import App from './App'
import Channels from './pages/Channels'
import Watch from './pages/Watch'
import Status from './pages/Status'
import Admin from './pages/Admin'
import Guide from './pages/Guide'
import DVR from './pages/DVR'
import Recording from './pages/Recording'
import ErrorPage from './pages/ErrorPage'
import './styles.css'
import { registerSW } from 'virtual:pwa-register'

// These paths are answered by the server, never by the app. A service
// worker from an older build may still hand them to the app shell; if
// that happens, drop the worker and reload once so the request goes to
// the network. The current worker re-registers on the next app page.
const serverPaths = /^\/(tv$|downloads\/|auth\/|mcp|\.well-known\/)/
if (serverPaths.test(window.location.pathname) && !sessionStorage.getItem('sw-bypass')) {
  sessionStorage.setItem('sw-bypass', '1')
  const regs = navigator.serviceWorker?.getRegistrations?.() ?? Promise.resolve([])
  regs.then((rs) => Promise.all(rs.map((r) => r.unregister()))).finally(() => window.location.reload())
} else {
  sessionStorage.removeItem('sw-bypass')
}

// Service worker: precaches the app shell and updates itself in the
// background. Nothing live (API, streams) goes through it.
registerSW({ immediate: true })

const router = createBrowserRouter([
  {
    path: '/',
    element: <App />,
    errorElement: <ErrorPage />,
    children: [
      { index: true, element: <Channels /> },
      { path: 'watch/:number', element: <Watch /> },
      { path: 'status', element: <Status /> },
      { path: 'admin', element: <Admin /> },
      { path: 'guide', element: <Guide /> },
      { path: 'dvr', element: <DVR /> },
      { path: 'recordings/:id', element: <Recording /> },
      // Signed-in visitors to /login go home; signed-out ones never reach
      // the router because App renders the login page itself.
      { path: 'login', element: <Navigate to="/" replace /> },
      { path: '*', element: <ErrorPage /> },
    ],
  },
])

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <RouterProvider router={router} />
  </React.StrictMode>,
)
