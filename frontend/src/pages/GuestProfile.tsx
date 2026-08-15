import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { api, type Factor, type GuestDetail, type Review } from '../lib/api'
import { DimensionBars, ScoreComposition, ScoreDial } from '../components/charts'
import {
  Avatar,
  ConfidenceChip,
  Empty,
  RecommendationBadge,
  Skeleton,
  formatDate,
  relativeDate,
} from '../components/ui'

const FACTOR_STYLE: Record<Factor['kind'], { bg: string; icon: string; label: string }> = {
  strength: { bg: 'var(--status-good)', icon: '↑', label: 'Strength' },
  concern: { bg: 'var(--status-warning)', icon: '↓', label: 'Concern' },
  penalty: { bg: 'var(--status-critical)', icon: '!', label: 'Penalty' },
  context: { bg: 'var(--text-muted)', icon: 'i', label: 'Context' },
}

function FactorRow({ factor }: { factor: Factor }) {
  const s = FACTOR_STYLE[factor.kind]
  return (
    <div className="factor">
      <span className="factor-icon" style={{ background: s.bg, color: '#fff' }} aria-hidden="true">
        {s.icon}
      </span>
      <span style={{ flex: 1 }}>
        <span className="sr-only">{s.label}: </span>
        {factor.description}
      </span>
      {factor.impact !== 0 && (
        <span
          className="factor-impact"
          style={{ color: factor.impact > 0 ? 'var(--success-text)' : 'var(--status-critical)' }}
        >
          {factor.impact > 0 ? '+' : ''}
          {factor.impact.toFixed(1)}
        </span>
      )}
    </div>
  )
}

function ReviewCard({ review }: { review: Review }) {
  const dims = [
    ['Rules', review.ratings.house_rules],
    ['Care', review.ratings.property_care],
    ['Comms', review.ratings.communication],
    ['Noise', review.ratings.noise],
    ['Accuracy', review.ratings.accuracy],
  ] as const

  return (
    <div className="review">
      <div className="review-head">
        <div>
          <span className="review-who">{review.host_name || 'A host'}</span>
          {review.property_name && (
            <span style={{ color: 'var(--text-muted)', fontSize: 13 }}> · {review.property_name}</span>
          )}
        </div>
        <span className="review-when" title={formatDate(review.submitted_at)}>
          {relativeDate(review.submitted_at)}
        </span>
      </div>

      <div className="rating-pills">
        {dims.map(([label, v]) => (
          <span key={label} className="rating-pill">
            {label} {v}/5
          </span>
        ))}
      </div>

      {review.incidents.length > 0 && (
        <div style={{ marginTop: 9, display: 'flex', gap: 6, flexWrap: 'wrap' }}>
          {review.incidents.map((inc, i) => (
            <span
              key={i}
              className="badge"
              style={{ borderColor: 'var(--status-critical)', color: 'var(--text-primary)' }}
              title={inc.note}
            >
              <span className="badge-dot" style={{ background: 'var(--status-critical)' }} />
              {inc.type.replace(/_/g, ' ')} · {inc.severity}
            </span>
          ))}
        </div>
      )}

      {review.comment && <p className="review-comment">“{review.comment}”</p>}

      {review.incidents.some((i) => i.note) && (
        <ul style={{ margin: '8px 0 0', paddingLeft: 18, fontSize: 13, color: 'var(--text-muted)' }}>
          {review.incidents
            .filter((i) => i.note)
            .map((i, k) => (
              <li key={k}>{i.note}</li>
            ))}
        </ul>
      )}
    </div>
  )
}

export default function GuestProfile() {
  const { id } = useParams<{ id: string }>()
  const [guest, setGuest] = useState<GuestDetail | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!id) return
    let cancelled = false
    setGuest(null)
    setError(null)
    api
      .getGuest(id)
      .then((g) => !cancelled && setGuest(g))
      .catch((e) => !cancelled && setError(e.message))
    return () => {
      cancelled = true
    }
  }, [id])

  if (error) {
    return (
      <Empty title="Guest not found">
        {error} · <Link to="/" style={{ color: 'var(--series-1)' }}>Back to the directory</Link>
      </Empty>
    )
  }
  if (!guest) return <Skeleton count={3} height={150} />

  const s = guest.score

  return (
    <>
      <Link to="/" style={{ fontSize: 13.5, color: 'var(--text-secondary)', display: 'inline-block', marginBottom: 16 }}>
        ← All guests
      </Link>

      <div className="profile-head">
        <Avatar seed={guest.avatar_seed} name={guest.name} size={58} />
        <div style={{ flex: 1, minWidth: 220 }}>
          <h1 style={{ margin: 0, fontSize: 25, fontWeight: 660, letterSpacing: '-0.025em' }}>
            {guest.name}
            {guest.verified && (
              <span
                title="ID verified"
                style={{ color: 'var(--series-1)', fontSize: 18, marginLeft: 8, verticalAlign: 'middle' }}
              >
                ✓
              </span>
            )}
          </h1>
          <div style={{ color: 'var(--text-secondary)', fontSize: 14 }}>
            {guest.email}
            {guest.city && ` · ${guest.city}`}
            {` · joined ${formatDate(guest.joined_at)}`}
          </div>
        </div>
        <Link to={`/review/${guest.id}`} className="btn" style={{ textDecoration: 'none' }}>
          Rate this guest
        </Link>
      </div>

      <div className="profile-layout">
        <div style={{ display: 'grid', gap: 16 }}>
          <div className="card">
            <div style={{ display: 'flex', gap: 26, alignItems: 'center', flexWrap: 'wrap' }}>
              <ScoreDial score={s} />
              <div style={{ flex: 1, minWidth: 240, display: 'grid', gap: 12 }}>
                <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
                  <RecommendationBadge rec={s.recommendation} />
                  <ConfidenceChip confidence={s.confidence} effective={s.effective_review_count} />
                </div>
                <div className="headline-box">{s.headline}</div>
                {s.rated && <ScoreComposition score={s} />}
              </div>
            </div>
          </div>

          {s.rated && (
            <div className="card">
              <h2 className="card-title">Why this score</h2>
              <p className="card-sub">
                Every factor the engine used, with the points it moved. Nothing here is hidden behind a
                support ticket.
              </p>
              {s.factors.map((f, i) => (
                <FactorRow key={i} factor={f} />
              ))}
            </div>
          )}

          <div className="card">
            <h2 className="card-title">Stay history</h2>
            <p className="card-sub">
              {guest.reviews.length === 0
                ? 'No stays on record yet.'
                : `${guest.reviews.length} assessment${guest.reviews.length === 1 ? '' : 's'}, newest first.`}
            </p>
            {guest.reviews.length === 0 ? (
              <Empty title="Nothing on record">
                This guest has never been rated. That is not a negative signal — fall back to standard ID
                and payment verification.
              </Empty>
            ) : (
              guest.reviews.map((r) => <ReviewCard key={r.id} review={r} />)
            )}
          </div>
        </div>

        <div style={{ display: 'grid', gap: 16 }}>
          <div className="card">
            <h2 className="card-title">Dimension breakdown</h2>
            <p className="card-sub">Recency-weighted average per dimension, with its weight in the composite.</p>
            <DimensionBars dimensions={s.dimensions} />
          </div>

          <div className="card">
            <h2 className="card-title">At a glance</h2>
            <table className="data-table" style={{ marginTop: 4 }}>
              <tbody>
                <tr>
                  <td>Total stays</td>
                  <td>{s.review_count}</td>
                </tr>
                <tr>
                  <td>Effective stays</td>
                  <td title="Sum of recency weights: older stays count for less">
                    {s.effective_review_count.toFixed(1)}
                  </td>
                </tr>
                <tr>
                  <td>Incidents</td>
                  <td>{s.incident_count}</td>
                </tr>
                {s.rated && (
                  <>
                    <tr>
                      <td>Raw average</td>
                      <td>{s.raw_average.toFixed(2)} / 5</td>
                    </tr>
                    <tr>
                      <td>Adjusted average</td>
                      <td>{s.adjusted_average.toFixed(2)} / 5</td>
                    </tr>
                    <tr>
                      <td>Score before penalties</td>
                      <td>{s.base_score.toFixed(1)}</td>
                    </tr>
                    <tr>
                      <td>Incident penalty</td>
                      <td style={{ color: s.incident_penalty > 0 ? 'var(--status-critical)' : undefined }}>
                        {s.incident_penalty > 0 ? `−${s.incident_penalty.toFixed(1)}` : '0'}
                      </td>
                    </tr>
                  </>
                )}
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </>
  )
}
