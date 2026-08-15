import { useEffect, useState } from 'react'
import { api, type ScoringModel } from '../lib/api'
import { Empty, Skeleton, gradeColor } from '../components/ui'

/** The scoring model page renders entirely from GET /api/scoring-model.
 *  No constant on this page is hardcoded in the frontend — if the backend
 *  retunes a weight, this page follows automatically. That is the point of
 *  FR-007: the explanation cannot drift from the implementation. */
export default function ScoringModelPage() {
  const [model, setModel] = useState<ScoringModel | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    api.scoringModel().then(setModel).catch((e) => setError(e.message))
  }, [])

  if (error) return <Empty title="Could not load the scoring model">{error}</Empty>
  if (!model) return <Skeleton count={3} height={140} />

  const halfLifeYears = (model.review_half_life_days / 365).toFixed(1)

  return (
    <>
      <div className="page-head">
        <h1>How the score is calculated</h1>
        <p>
          These are the live values the engine is using right now, served from the API rather than
          written into this page. A score you disagree with can be traced back to something on this
          page.
        </p>
      </div>

      <div className="grid grid-2">
        <div className="card">
          <h2 className="card-title">Step 1 · Dimension weights</h2>
          <p className="card-sub">
            Each assessment becomes a single 1–5 quality value using these weights. They sum to 100%.
          </p>
          <table className="data-table">
            <thead>
              <tr>
                <th>Dimension</th>
                <th>Weight</th>
              </tr>
            </thead>
            <tbody>
              {model.dimensions.map((d) => (
                <tr key={d.dimension}>
                  <td>{d.label}</td>
                  <td>{(d.weight * 100).toFixed(0)}%</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>

        <div className="card">
          <h2 className="card-title">Step 2 · Recency</h2>
          <p className="card-sub">Older evidence counts for less, on a smooth curve with no cutoff.</p>
          <p style={{ fontSize: 14, color: 'var(--text-secondary)', lineHeight: 1.6 }}>
            A review's weight halves every{' '}
            <strong style={{ color: 'var(--text-primary)' }}>{model.review_half_life_days} days</strong> (
            {halfLifeYears} years). A stay from two years ago counts a quarter as much as one from last
            month. Incident penalties fade faster, halving every{' '}
            <strong style={{ color: 'var(--text-primary)' }}>{model.incident_half_life_days} days</strong>.
          </p>
          <table className="data-table">
            <thead>
              <tr>
                <th>Age of review</th>
                <th>Counts as</th>
              </tr>
            </thead>
            <tbody>
              {[0, 182, 365, 730, 1095].map((days) => (
                <tr key={days}>
                  <td>{days === 0 ? 'Today' : `${(days / 365).toFixed(1)} years`}</td>
                  <td>{(Math.pow(0.5, days / model.review_half_life_days) * 100).toFixed(0)}%</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>

        <div className="card">
          <h2 className="card-title">Step 3 · Limited-history adjustment</h2>
          <p className="card-sub">Why two great reviews do not equal twenty.</p>
          <p style={{ fontSize: 14, color: 'var(--text-secondary)', lineHeight: 1.6 }}>
            The weighted average is blended with a population baseline of{' '}
            <strong style={{ color: 'var(--text-primary)' }}>{model.prior_mean}/5</strong>, weighted as if
            it were <strong style={{ color: 'var(--text-primary)' }}>{model.prior_strength}</strong>{' '}
            additional reviews. A guest with one perfect stay lands well below a guest with twenty,
            because one stay is not yet evidence. As real reviews accumulate, the baseline's influence
            shrinks toward nothing.
          </p>
          <div
            style={{
              background: 'var(--surface-raised)',
              border: '1px solid var(--border)',
              borderRadius: 'var(--radius-sm)',
              padding: '11px 13px',
              fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
              fontSize: 12.5,
              color: 'var(--text-secondary)',
              marginTop: 10,
              overflowX: 'auto',
            }}
          >
            adjusted = (prior×C + Σ wᵢ·qᵢ) / (C + Σ wᵢ)
          </div>
        </div>

        <div className="card">
          <h2 className="card-title">Step 4 · Incident penalties</h2>
          <p className="card-sub">
            Applied on the 0–100 scale after the ratings, scaled by severity and faded by age.
          </p>
          <table className="data-table">
            <thead>
              <tr>
                <th>Incident</th>
                <th>Base penalty</th>
              </tr>
            </thead>
            <tbody>
              {model.incident_catalog.map((c) => (
                <tr key={c.type}>
                  <td>
                    {c.label}
                    <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>{c.description}</div>
                  </td>
                  <td>−{c.base_penalty}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <p style={{ fontSize: 13, color: 'var(--text-secondary)', marginTop: 12 }}>
            Severity multipliers:{' '}
            {Object.entries(model.severity_multipliers)
              .map(([k, v]) => `${k} ×${v}`)
              .join(' · ')}
          </p>
        </div>

        <div className="card" style={{ gridColumn: '1 / -1' }}>
          <h2 className="card-title">Grade bands</h2>
          <p className="card-sub">Where the final 0–100 composite lands.</p>
          <table className="data-table">
            <thead>
              <tr>
                <th style={{ width: 60 }}>Grade</th>
                <th>Meaning</th>
                <th style={{ textAlign: 'left' }}>Range</th>
              </tr>
            </thead>
            <tbody>
              {model.grade_bands.map((b, i) => {
                const upper = i === 0 ? 100 : model.grade_bands[i - 1].min - 0.1
                return (
                  <tr key={b.grade}>
                    <td>
                      <span
                        className="badge"
                        style={{ borderColor: gradeColor(b.grade), color: 'var(--text-primary)' }}
                      >
                        <span className="badge-dot" style={{ background: gradeColor(b.grade) }} />
                        {b.grade}
                      </span>
                    </td>
                    <td>
                      <strong style={{ fontWeight: 570 }}>{b.label}</strong>
                      <div style={{ fontSize: 12.5, color: 'var(--text-muted)' }}>{b.description}</div>
                    </td>
                    <td style={{ textAlign: 'left', fontVariantNumeric: 'tabular-nums' }}>
                      {b.min} – {upper.toFixed(upper % 1 === 0 ? 0 : 1)}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      </div>
    </>
  )
}
