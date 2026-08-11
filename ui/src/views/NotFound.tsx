import { Link } from 'react-router-dom'

export function NotFound() {
  return (
    <div>
      <h2 className="panel__title">Page not found</h2>
      <p>
        <Link to="/">Return to the dashboard</Link>
      </p>
    </div>
  )
}
