import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import {
  ApiError,
  api,
  type CreateReviewResult,
  type GuestSummary,
  type Incident,
  type IncidentType,
  type Ratings,
  type ScoringModel,
  type Severity,
  type Commendation,
  type CommendationType,
} from '../lib/api'
import { Avatar, Banner, Skeleton, TierBadge } from '../components/ui'

const DIMENSION_FIELDS: { key: keyof Ratings; label: string; hint: string }[] = [
  { key: 'house_rules', label: 'Hotel policy compliance', hint: 'Smoking, pets, occupancy, quiet hours.' },
  { key: 'property_care', label: 'Room condition', hint: 'State the room was left in at checkout.' },
  { key: 'communication', label: 'Staff interaction', hint: 'How the guest dealt with front desk and housekeeping.' },
  { key: 'noise', label: 'Noise & other guests', hint: 'Any disturbance to neighbouring rooms.' },
  { key: 'accuracy', label: 'Booking accuracy', hint: 'Did the party match the reservation?' },
]

const SEVERITIES: Severity[] = ['minor', 'moderate', 'severe']

export default function SubmitReview() {
  const { guestId } = useParams<{ guestId: string }>()
  const navigate = useNavigate()

  const [guests, setGuests] = useState<GuestSummary[] | null>(null)
  const [model, setModel] = useState<ScoringModel | null>(null)

  const [selected, setSelected] = useState(guestId ?? '')
  const [ratings, setRatings] = useState<Ratings>({
    house_rules: 4,
    property_care: 4,
    communication: 4,
    noise: 4,
    accuracy: 4,
  })
  const [incidents, setIncidents] = useState<Incident[]>([])
  const [commendations, setCommendations] = useState<Commendation[]>([])
  const [comment, setComment] = useState('')
  const [property, setProperty] = useState('')

  const [submitting, setSubmitting] = useState(false)
  const [result, setResult] = useState<CreateReviewResult | null>(null)
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({})
  const [formError, setFormError] = useState<string | null>(null)

  useEffect(() => {
    api.listGuests({ sort: 'name' }).then((r) => setGuests(r.guests)).catch(() => setGuests([]))
    api.scoringModel().then(setModel).catch(() => setModel(null))
  }, [])

  const toggleCommendation = (type: CommendationType) => {
    setCommendations((prev) =>
      prev.some((c) => c.type === type)
        ? prev.filter((c) => c.type !== type)
        : [...prev, { type }],
    )
  }

  const toggleIncident = (type: IncidentType) => {
    setIncidents((prev) =>
      prev.some((i) => i.type === type)
        ? prev.filter((i) => i.type !== type)
        : [...prev, { type, severity: 'moderate' as Severity }],
    )
  }

  const setSeverity = (type: IncidentType, severity: Severity) => {
    setIncidents((prev) => prev.map((i) => (i.type === type ? { ...i, severity } : i)))
  }

  const setNote = (type: IncidentType, note: string) => {
    setIncidents((prev) => prev.map((i) => (i.type === type ? { ...i, note } : i)))
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setFieldErrors({})
    setFormError(null)

    if (!selected) {
      setFieldErrors({ guest_id: 'Pick the guest whose stay you are recording.' })
      return
    }

    setSubmitting(true)
    try {
      const res = await api.createReview({
        guest_id: selected,
        property_name: property || undefined,
        ratings,
        incidents,
        commendations,
        comment,
      })
      setResult(res)
      window.scrollTo({ top: 0, behavior: 'smooth' })
    } catch (err) {
      if (err instanceof ApiError) {
        setFieldErrors(err.fields)
        setFormError(err.message)
      } else {
        setFormError('Something went wrong submitting the review.')
      }
    } finally {
      setSubmitting(false)
    }
  }

  if (!guests) return <Skeleton count={4} height={110} />

  // --- Success state: show what the review actually did to the score ---------
  if (result) {
    const delta = result.composite_delta
    const guest = guests.find((g) => g.id === result.review.guest_id)
    return (
      <>
        <div className="page-head">
          <h1>Stay recorded</h1>
          <p>Here is exactly how this stay moved the guest's standing.</p>
        </div>

        <div className="card" style={{ maxWidth: 640 }}>
          <div style={{ display: 'flex', gap: 14, alignItems: 'center', marginBottom: 20 }}>
            {guest && <Avatar seed={guest.avatar_seed} name={guest.name} />}
            <div>
              <div style={{ fontWeight: 620, fontSize: 17 }}>{guest?.name ?? result.review.guest_id}</div>
              <div style={{ fontSize: 13.5, color: 'var(--text-secondary)' }}>
                {result.score_after.stay_count} stay
                {result.score_after.stay_count === 1 ? '' : 's'} on record
              </div>
            </div>
          </div>

          <div style={{ display: 'flex', alignItems: 'center', gap: 20, flexWrap: 'wrap' }}>
            <div>
              <div style={{ fontSize: 12, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: '0.05em', fontWeight: 550 }}>
                Before
              </div>
              <div style={{ fontSize: 30, fontWeight: 650, fontVariantNumeric: 'tabular-nums', color: 'var(--text-secondary)' }}>
                {result.score_before.rated ? result.score_before.composite.toFixed(1) : '—'}
              </div>
              <TierBadge score={result.score_before} />
            </div>

            <div style={{ fontSize: 22, color: 'var(--text-muted)' }} aria-hidden="true">
              →
            </div>

            <div>
              <div style={{ fontSize: 12, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: '0.05em', fontWeight: 550 }}>
                After
              </div>
              <div style={{ fontSize: 30, fontWeight: 650, fontVariantNumeric: 'tabular-nums' }}>
                {result.score_after.composite.toFixed(1)}
              </div>
              <TierBadge score={result.score_after} />
            </div>

            <div style={{ marginLeft: 'auto' }}>
              <div style={{ fontSize: 12, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: '0.05em', fontWeight: 550 }}>
                Change
              </div>
              <div
                style={{
                  fontSize: 26,
                  fontWeight: 650,
                  fontVariantNumeric: 'tabular-nums',
                  color: delta >= 0 ? 'var(--success-text)' : 'var(--status-critical)',
                }}
              >
                {delta >= 0 ? '+' : ''}
                {delta.toFixed(1)}
              </div>
            </div>
          </div>

          <div style={{ display: 'flex', gap: 10, marginTop: 24, flexWrap: 'wrap' }}>
            <button className="btn" onClick={() => navigate(`/guests/${result.review.guest_id}`)}>
              View full profile
            </button>
            <button
              className="btn btn-ghost"
              onClick={() => {
                setResult(null)
                setComment('')
                setIncidents([])
                setProperty('')
              }}
            >
              Record another stay
            </button>
          </div>
        </div>
      </>
    )
  }

  // --- Form ------------------------------------------------------------------
  return (
    <>
      <div className="page-head">
        <h1>Record a stay</h1>
        <p>
          Rate each dimension from 1 to 5, then flag anything that went wrong or especially right.
          Weights are shown so you can see which answers move the guest's standing most.
        </p>
      </div>

      {formError && <Banner tone="bad">{formError}</Banner>}

      <form onSubmit={submit} style={{ maxWidth: 680 }}>
        <div className="card" style={{ marginBottom: 16 }}>
          <div className="field">
            <label className="field-label" htmlFor="guest">
              Guest
            </label>
            <select
              id="guest"
              className="select"
              style={{ width: '100%' }}
              value={selected}
              onChange={(e) => setSelected(e.target.value)}
            >
              <option value="">Select a guest…</option>
              {guests.map((g) => (
                <option key={g.id} value={g.id}>
                  {g.name} — {g.email}
                </option>
              ))}
            </select>
            {fieldErrors.guest_id && <div className="field-error">⚠ {fieldErrors.guest_id}</div>}
          </div>

          <div className="field" style={{ marginBottom: 0 }}>
            <label className="field-label" htmlFor="property">
              Property <span style={{ color: 'var(--text-muted)', fontWeight: 400 }}>(optional)</span>
            </label>
            <input
              id="property"
              className="input"
              style={{ width: '100%' }}
              value={property}
              onChange={(e) => setProperty(e.target.value)}
              placeholder="e.g. Grand Meridian — Downtown"
            />
          </div>
        </div>

        <div className="card" style={{ marginBottom: 16 }}>
          <h2 className="card-title">Ratings</h2>
          <p className="card-sub">1 is poor, 5 is excellent. All five are required.</p>

          {DIMENSION_FIELDS.map((f) => {
            const weight = model?.dimensions.find((d) => d.dimension === f.key)?.weight
            const err = fieldErrors[`ratings.${f.key}`]
            return (
              <div className="rating-row" key={f.key}>
                <div className="rating-row-label">
                  {f.label}
                  {weight !== undefined && (
                    <span className="rating-row-weight"> · {(weight * 100).toFixed(0)}% of score</span>
                  )}
                  <div style={{ fontSize: 12.5, color: 'var(--text-muted)', fontWeight: 400 }}>{f.hint}</div>
                  {err && <div className="field-error">⚠ {err}</div>}
                </div>
                <div className="rating-choices" role="radiogroup" aria-label={f.label}>
                  {[1, 2, 3, 4, 5].map((v) => (
                    <button
                      key={v}
                      type="button"
                      role="radio"
                      aria-checked={ratings[f.key] === v}
                      className={`rating-choice${ratings[f.key] === v ? ' selected' : ''}`}
                      onClick={() => setRatings((r) => ({ ...r, [f.key]: v }))}
                    >
                      {v}
                    </button>
                  ))}
                </div>
              </div>
            )
          })}
        </div>

        <div className="card" style={{ marginBottom: 16 }}>
          <h2 className="card-title">Incidents</h2>
          <p className="card-sub">
            Only flag what actually happened. Penalties apply on top of the ratings and fade with time.
          </p>

          <div style={{ display: 'grid', gap: 8 }}>
            {(model?.incident_catalog ?? []).map((c) => {
              const active = incidents.find((i) => i.type === c.type)
              return (
                <div
                  key={c.type}
                  style={{
                    border: `1px solid ${active ? 'var(--status-critical)' : 'var(--border)'}`,
                    borderRadius: 'var(--radius-sm)',
                    padding: '10px 12px',
                  }}
                >
                  <label style={{ display: 'flex', alignItems: 'center', gap: 10, cursor: 'pointer' }}>
                    <input
                      type="checkbox"
                      checked={!!active}
                      onChange={() => toggleIncident(c.type)}
                      style={{ accentColor: 'var(--status-critical)' }}
                    />
                    <span style={{ fontWeight: 550, fontSize: 14 }}>{c.label}</span>
                    <span style={{ fontSize: 12.5, color: 'var(--text-muted)', marginLeft: 'auto' }}>
                      up to −{c.base_penalty} pts
                    </span>
                  </label>
                  <div style={{ fontSize: 12.5, color: 'var(--text-muted)', marginLeft: 27 }}>
                    {c.description}
                  </div>

                  {active && (
                    <div style={{ marginLeft: 27, marginTop: 10, display: 'grid', gap: 8 }}>
                      <div style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>
                        <span style={{ fontSize: 12.5, color: 'var(--text-secondary)' }}>Severity:</span>
                        {SEVERITIES.map((sev) => (
                          <button
                            key={sev}
                            type="button"
                            className={`rating-choice${active.severity === sev ? ' selected' : ''}`}
                            style={{ width: 'auto', padding: '0 12px', fontSize: 12.5 }}
                            onClick={() => setSeverity(c.type, sev)}
                          >
                            {sev}
                            {model && ` ×${model.severity_multipliers[sev]}`}
                          </button>
                        ))}
                      </div>
                      <input
                        className="input"
                        placeholder="What happened? (optional)"
                        value={active.note ?? ''}
                        onChange={(e) => setNote(c.type, e.target.value)}
                      />
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        </div>

        <div className="card" style={{ marginBottom: 16 }}>
          <h2 className="card-title">Commendations</h2>
          <p className="card-sub">
            Positive events that move the score up. Worth less than the matching penalties — standing
            should be slower to earn than to lose.
          </p>

          <div style={{ display: 'grid', gap: 8 }}>
            {(model?.commendation_catalog ?? []).map((c) => {
              const active = commendations.some((x) => x.type === c.type)
              return (
                <label
                  key={c.type}
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: 10,
                    cursor: 'pointer',
                    border: `1px solid ${active ? 'var(--status-good)' : 'var(--border)'}`,
                    borderRadius: 'var(--radius-sm)',
                    padding: '10px 12px',
                  }}
                >
                  <input
                    type="checkbox"
                    checked={active}
                    onChange={() => toggleCommendation(c.type)}
                    style={{ accentColor: 'var(--status-good)' }}
                  />
                  <span style={{ minWidth: 0 }}>
                    <span style={{ fontWeight: 550, fontSize: 14 }}>{c.label}</span>
                    <span style={{ display: 'block', fontSize: 12.5, color: 'var(--text-muted)' }}>
                      {c.description}
                    </span>
                  </span>
                  <span style={{ fontSize: 12.5, color: 'var(--success-text)', marginLeft: 'auto', whiteSpace: 'nowrap' }}>
                    up to +{c.base_bonus} pts
                  </span>
                </label>
              )
            })}
          </div>
        </div>

        <div className="card" style={{ marginBottom: 16 }}>
          <div className="field" style={{ marginBottom: 0 }}>
            <label className="field-label" htmlFor="comment">
              Written note
            </label>
            <p className="field-hint">
              The part the next front-desk agent will actually read. Be specific and factual.
            </p>
            <textarea
              id="comment"
              className="input"
              value={comment}
              onChange={(e) => setComment(e.target.value)}
              maxLength={2000}
              placeholder="How did the stay go?"
            />
            {fieldErrors.comment && <div className="field-error">⚠ {fieldErrors.comment}</div>}
            <div style={{ fontSize: 12, color: 'var(--text-muted)', textAlign: 'right', marginTop: 4 }}>
              {comment.length} / 2000
            </div>
          </div>
        </div>

        <div style={{ display: 'flex', gap: 10 }}>
          <button className="btn" type="submit" disabled={submitting}>
            {submitting ? 'Submitting…' : 'Save stay record'}
          </button>
          <Link to="/" className="btn btn-ghost" style={{ textDecoration: 'none' }}>
            Cancel
          </Link>
        </div>
      </form>
    </>
  )
}
