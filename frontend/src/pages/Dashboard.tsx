import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, type Review, type Stats } from '../lib/api'
import { BandDistribution, DimensionBars, ReviewTimeline } from '../components/charts'
import { Empty, Skeleton, relativeDate } from '../components/ui'

export default function Dashboard() {
  const [stats, setStats] = useState<Stats | null>(null)
  const [recent, setRecent] = useState<(Review & { guest_name: string })[]>([])
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    api.stats().then(setStats).catch((e) => setError(e.message))
    api.listReviews(8).then((r) => setRecent(r.reviews)).catch(() => setRecent([]))
  }, [])

  if (error) return <Empty title="Could not load the dashboard">{error}</Empty>
  if (!stats) return <Skeleton count={4} height={110} />

  // An empty portfolio gets an empty state, not zeros dressed up as measurements.
  if (stats.empty) {
    return (
      <>
        <div className="page-head">
          <h1>Portfolio</h1>
        </div>
        <Empty title="No assessments yet">
          Once you rate your first guest, this page will show your review activity, score distribution,
          and incident history. <Link to="/review" style={{ color: 'var(--series-1)' }}>Rate a guest</Link> to
          get started.
        </Empty>
      </>
    )
  }

  return (
    <>
      <div className="page-head">
        <h1>Portfolio</h1>
        <p>Everything on this page is computed from the assessments in your account.</p>
      </div>

      <div className="grid grid-4" style={{ marginBottom: 16 }}>
        <div className="stat">
          <div className="stat-label">Guests tracked</div>
          <div className="stat-value">{stats.total_guests}</div>
          <div className="stat-note">
            {stats.rated_guests} scored · {stats.unrated_guests} unrated
          </div>
        </div>
        <div className="stat">
          <div className="stat-label">Assessments</div>
          <div className="stat-value">{stats.total_reviews}</div>
          <div className="stat-note">across all properties</div>
        </div>
        <div className="stat">
          <div className="stat-label">Average score</div>
          <div className="stat-value">{stats.average_score.toFixed(1)}</div>
          <div className="stat-note">of scored guests</div>
        </div>
        <div className="stat">
          <div className="stat-label">Incidents</div>
          <div className="stat-value">{stats.total_incidents}</div>
          <div className="stat-note">
            affecting {stats.guests_with_incidents} guest{stats.guests_with_incidents === 1 ? '' : 's'}
          </div>
        </div>
      </div>

      <div className="grid grid-2" style={{ marginBottom: 16 }}>
        <div className="card">
          <h2 className="card-title">Guests by grade</h2>
          <p className="card-sub">How the guests you have hosted distribute across score bands.</p>
          <BandDistribution bands={stats.band_distribution} />
        </div>

        <div className="card">
          <h2 className="card-title">Review activity</h2>
          <p className="card-sub">Assessments submitted per month, last 12 months.</p>
          <ReviewTimeline data={stats.review_timeline} />
        </div>
      </div>

      <div className="grid grid-2">
        <div className="card">
          <h2 className="card-title">Average rating by dimension</h2>
          <p className="card-sub">
            Unweighted mean across every assessment. Useful for spotting which problems recur.
          </p>
          <DimensionBars
            showWeights={false}
            dimensions={stats.dimension_averages.map((d) => ({
              dimension: d.dimension,
              label: d.label,
              average: d.average,
              weight: 0,
              contributes: 0,
            }))}
          />
        </div>

        <div className="card">
          <h2 className="card-title">Recent activity</h2>
          <p className="card-sub">The last few assessments submitted.</p>
          {recent.length === 0 ? (
            <Empty title="Nothing yet" />
          ) : (
            <div style={{ display: 'grid', gap: 2 }}>
              {recent.map((r) => (
                <Link
                  key={r.id}
                  to={`/guests/${r.guest_id}`}
                  style={{
                    display: 'flex',
                    justifyContent: 'space-between',
                    gap: 12,
                    padding: '9px 0',
                    borderBottom: '1px solid var(--grid)',
                    fontSize: 13.5,
                  }}
                >
                  <span style={{ minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    <strong style={{ fontWeight: 570 }}>{r.guest_name}</strong>
                    {r.incidents.length > 0 && (
                      <span style={{ color: 'var(--status-critical)', marginLeft: 7, fontSize: 12.5 }}>
                        {r.incidents.length} incident{r.incidents.length === 1 ? '' : 's'}
                      </span>
                    )}
                  </span>
                  <span style={{ color: 'var(--text-muted)', fontSize: 12.5, whiteSpace: 'nowrap' }}>
                    {relativeDate(r.submitted_at)}
                  </span>
                </Link>
              ))}
            </div>
          )}
        </div>
      </div>
    </>
  )
}
