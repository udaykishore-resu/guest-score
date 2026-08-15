import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, type GuestSummary } from '../lib/api'
import { Avatar, ConfidenceChip, Empty, GradeBadge, RecommendationBadge, Skeleton, relativeDate } from '../components/ui'

/** useDebounced keeps typing in the search box from firing a request per
 *  keystroke, which matters as soon as the directory is bigger than a demo. */
function useDebounced<T>(value: T, ms = 220): T {
  const [v, setV] = useState(value)
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms)
    return () => clearTimeout(t)
  }, [value, ms])
  return v
}

export default function Directory() {
  const [search, setSearch] = useState('')
  const [band, setBand] = useState('')
  const [incidents, setIncidents] = useState(false)
  const [sort, setSort] = useState('score')

  const [guests, setGuests] = useState<GuestSummary[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  const debounced = useDebounced(search)

  useEffect(() => {
    let cancelled = false
    setError(null)
    api
      .listGuests({ q: debounced, band, incidents, sort })
      .then((r) => {
        if (!cancelled) setGuests(r.guests)
      })
      .catch((e) => {
        if (!cancelled) setError(e.message)
      })
    return () => {
      cancelled = true
    }
  }, [debounced, band, incidents, sort])

  const summary = useMemo(() => {
    if (!guests) return null
    const rated = guests.filter((g) => g.score.rated)
    return { total: guests.length, rated: rated.length }
  }, [guests])

  return (
    <>
      <div className="page-head">
        <h1>Guest directory</h1>
        <p>
          Look up a guest before you accept their booking. Every score is computed from host
          assessments, weighted by recency, and shown with the reasoning behind it.
        </p>
      </div>

      <div className="filters">
        <input
          className="input search"
          type="search"
          placeholder="Search by name or email…"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          aria-label="Search guests"
        />
        <select className="select" value={band} onChange={(e) => setBand(e.target.value)} aria-label="Filter by grade">
          <option value="">All grades</option>
          <option value="A">Grade A</option>
          <option value="B">Grade B</option>
          <option value="C">Grade C</option>
          <option value="D">Grade D</option>
          <option value="F">Grade F</option>
        </select>
        <select className="select" value={sort} onChange={(e) => setSort(e.target.value)} aria-label="Sort guests">
          <option value="score">Highest score</option>
          <option value="reviews">Most reviews</option>
          <option value="recent">Most recent stay</option>
          <option value="name">Name (A–Z)</option>
        </select>
        <label className="toggle">
          <input type="checkbox" checked={incidents} onChange={(e) => setIncidents(e.target.checked)} />
          Has incidents
        </label>
      </div>

      {error && (
        <div className="banner" style={{ borderLeft: '3px solid var(--status-critical)' }}>
          {error}
        </div>
      )}

      {!guests && !error && <Skeleton count={6} />}

      {guests && guests.length === 0 && (
        <Empty title="No guests match those filters">
          Try clearing the search box or widening the grade filter.
        </Empty>
      )}

      {guests && guests.length > 0 && (
        <>
          <div className="row-head">
            <span>Guest</span>
            <span>Score</span>
            <span>Grade</span>
            <span>Stays</span>
            <span>Recommendation</span>
          </div>
          {guests.map((g) => (
            <Link to={`/guests/${g.id}`} key={g.id} className="guest-row">
              <div className="guest-ident">
                <Avatar seed={g.avatar_seed} name={g.name} />
                <div style={{ minWidth: 0 }}>
                  <div className="guest-name">
                    {g.name}
                    {g.verified && (
                      <span title="ID verified" style={{ color: 'var(--series-1)', marginLeft: 6 }}>
                        ✓
                      </span>
                    )}
                  </div>
                  <div className="guest-meta">
                    {g.city ?? g.email}
                    {g.last_stay_at && ` · last stay ${relativeDate(g.last_stay_at)}`}
                  </div>
                </div>
              </div>

              <div
                style={{
                  fontSize: 21,
                  fontWeight: 640,
                  letterSpacing: '-0.03em',
                  fontVariantNumeric: 'tabular-nums',
                  color: g.score.rated ? 'var(--text-primary)' : 'var(--text-muted)',
                }}
              >
                {g.score.rated ? g.score.composite.toFixed(0) : '—'}
              </div>

              <div>
                <GradeBadge score={g.score} />
              </div>

              <div style={{ fontSize: 13.5, color: 'var(--text-secondary)' }}>
                {g.score.review_count === 0 ? (
                  <span style={{ color: 'var(--text-muted)' }}>none</span>
                ) : (
                  <>
                    {g.score.review_count} stay{g.score.review_count === 1 ? '' : 's'}
                    {g.score.incident_count > 0 && (
                      <div style={{ fontSize: 12, color: 'var(--status-serious)', fontWeight: 550 }}>
                        {g.score.incident_count} incident{g.score.incident_count === 1 ? '' : 's'}
                      </div>
                    )}
                  </>
                )}
              </div>

              <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                <RecommendationBadge rec={g.score.recommendation} />
                {g.score.rated && (
                  <ConfidenceChip confidence={g.score.confidence} effective={g.score.effective_review_count} />
                )}
              </div>
            </Link>
          ))}
          {summary && (
            <p style={{ fontSize: 13, color: 'var(--text-muted)', marginTop: 14 }}>
              {summary.total} guest{summary.total === 1 ? '' : 's'} · {summary.rated} with a score
            </p>
          )}
        </>
      )}
    </>
  )
}
