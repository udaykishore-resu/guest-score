import { useEffect, useState } from 'react'
import { NavLink, Route, Routes } from 'react-router-dom'
import Directory from './pages/Directory'
import GuestProfile from './pages/GuestProfile'
import Dashboard from './pages/Dashboard'
import SubmitReview from './pages/SubmitReview'
import ScoringModelPage from './pages/ScoringModelPage'

/** Theme toggle. Dark mode is a selected palette, not an inverted one — the
 *  values live in styles.css; this only stamps the attribute the CSS keys off. */
function useTheme() {
  const [theme, setTheme] = useState<'light' | 'dark' | 'system'>(
    () => (localStorage.getItem('gs-theme') as 'light' | 'dark' | 'system') ?? 'system',
  )
  useEffect(() => {
    const root = document.documentElement
    if (theme === 'system') root.removeAttribute('data-theme')
    else root.setAttribute('data-theme', theme)
    localStorage.setItem('gs-theme', theme)
  }, [theme])
  return { theme, setTheme }
}

export default function App() {
  const { theme, setTheme } = useTheme()

  const cycle = () => setTheme(theme === 'system' ? 'light' : theme === 'light' ? 'dark' : 'system')
  const icon = theme === 'light' ? '☀' : theme === 'dark' ? '☾' : '◐'

  return (
    <div className="app">
      <header className="topbar">
        <div className="brand">
          <span className="brand-mark" aria-hidden="true">
            GS
          </span>
          Guest&nbsp;Score
        </div>
        <nav className="nav">
          <NavLink to="/" end>
            Directory
          </NavLink>
          <NavLink to="/dashboard">Portfolio</NavLink>
          <NavLink to="/review">Rate a guest</NavLink>
          <NavLink to="/model">Scoring model</NavLink>
        </nav>
        <button
          className="icon-button"
          onClick={cycle}
          title={`Theme: ${theme}. Click to change.`}
          aria-label={`Theme: ${theme}. Click to change.`}
        >
          {icon}
        </button>
      </header>

      <main>
        <Routes>
          <Route path="/" element={<Directory />} />
          <Route path="/guests/:id" element={<GuestProfile />} />
          <Route path="/dashboard" element={<Dashboard />} />
          <Route path="/review" element={<SubmitReview />} />
          <Route path="/review/:guestId" element={<SubmitReview />} />
          <Route path="/model" element={<ScoringModelPage />} />
          <Route
            path="*"
            element={
              <div className="empty">
                <h3>Page not found</h3>
              </div>
            }
          />
        </Routes>
      </main>
    </div>
  )
}
