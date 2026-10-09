import { Link, isRouteErrorResponse, useRouteError } from 'react-router-dom'

/**
 * Route error boundary: a styled page for unknown routes (404) and for
 * anything a page throws while rendering, instead of React Router's
 * developer placeholder.
 */
export default function ErrorPage() {
  const error = useRouteError()
  let title = 'Something went wrong'
  let detail = 'The page hit an error while loading.'
  if (isRouteErrorResponse(error)) {
    if (error.status === 404) {
      title = 'Page not found'
      detail = 'There is nothing at this address. The channel may have been removed or the link mistyped.'
    } else {
      title = `${error.status} ${error.statusText}`
      detail = typeof error.data === 'string' ? error.data : detail
    }
  } else if (error instanceof Error) {
    detail = error.message
  }
  return (
    <div className="app">
      <main>
        <section className="login">
          <div className="login-card">
            <div className="brand">
              <img src="/favicon.svg" alt="" className="brand-mark" /> OpenDVR
            </div>
            <h1>{title}</h1>
            <p className="muted">{detail}</p>
            <div className="modal-actions" style={{ justifyContent: 'center' }}>
              <Link to="/" className="btn">
                Channels
              </Link>
              <Link to="/guide" className="btn">
                Guide
              </Link>
              <button onClick={() => window.location.reload()}>Reload</button>
            </div>
          </div>
        </section>
      </main>
    </div>
  )
}
