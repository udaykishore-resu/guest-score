import type { Confidence, Recommendation, Score } from '../lib/api'

/** Status colour for a grade band. Never used as the sole signal — every
 *  caller pairs it with the grade letter and its label (dataviz status rule). */
export function gradeColor(grade: string): string {
  switch (grade) {
    case 'A':
      return 'var(--status-good)'
    case 'B':
      return 'var(--status-good)'
    case 'C':
      return 'var(--status-warning)'
    case 'D':
      return 'var(--status-serious)'
    case 'F':
      return 'var(--status-critical)'
    default:
      return 'var(--text-muted)'
  }
}

/** Deterministic avatar tint from the guest id, so a face is recognisable
 *  across pages. Drawn from the categorical ramp, not random hues. */
const AVATAR_TINTS = ['#2a78d6', '#eb6834', '#1baf7a', '#4a3aa7', '#e87ba4', '#256abf']

export function Avatar({ seed, name, size }: { seed: string; name: string; size?: number }) {
  let hash = 0
  for (let i = 0; i < seed.length; i++) hash = (hash * 31 + seed.charCodeAt(i)) >>> 0
  const tint = AVATAR_TINTS[hash % AVATAR_TINTS.length]
  const initials = name
    .split(/\s+/)
    .slice(0, 2)
    .map((w) => w[0] ?? '')
    .join('')
    .toUpperCase()
  return (
    <div
      className="avatar"
      style={{ background: tint, ...(size ? { width: size, height: size, fontSize: size * 0.36 } : {}) }}
      aria-hidden="true"
    >
      {initials}
    </div>
  )
}

export function GradeBadge({ score }: { score: Score }) {
  if (!score.rated) {
    return (
      <span className="badge" style={{ borderColor: 'var(--border-strong)', color: 'var(--text-muted)' }}>
        Unrated
      </span>
    )
  }
  const color = gradeColor(score.grade)
  return (
    <span className="badge" style={{ borderColor: color, color: 'var(--text-primary)' }}>
      <span className="badge-dot" style={{ background: color }} />
      {score.grade} · {score.grade_label}
    </span>
  )
}

const REC_TEXT: Record<Recommendation, { label: string; icon: string; color: string }> = {
  accept: { label: 'Accept', icon: '✓', color: 'var(--status-good)' },
  accept_with_conditions: { label: 'Accept with conditions', icon: '!', color: 'var(--status-warning)' },
  manual_review: { label: 'Manual review', icon: '?', color: 'var(--status-serious)' },
  decline: { label: 'Decline', icon: '×', color: 'var(--status-critical)' },
  insufficient_data: { label: 'Not enough data', icon: '–', color: 'var(--text-muted)' },
}

export function RecommendationBadge({ rec }: { rec: Recommendation }) {
  const r = REC_TEXT[rec]
  return (
    <span className="badge" style={{ borderColor: r.color, color: 'var(--text-primary)' }}>
      <span
        aria-hidden="true"
        style={{
          width: 15,
          height: 15,
          borderRadius: '50%',
          background: r.color,
          color: '#fff',
          display: 'grid',
          placeItems: 'center',
          fontSize: 10,
          fontWeight: 700,
        }}
      >
        {r.icon}
      </span>
      {r.label}
    </span>
  )
}

const CONFIDENCE_TEXT: Record<Confidence, string> = {
  none: 'No evidence',
  low: 'Low confidence',
  medium: 'Medium confidence',
  high: 'High confidence',
}

export function ConfidenceChip({ confidence, effective }: { confidence: Confidence; effective?: number }) {
  const filled = { none: 0, low: 1, medium: 2, high: 3 }[confidence]
  return (
    <span className="chip" title={effective !== undefined ? `${effective} effective reviews` : undefined}>
      <span style={{ display: 'inline-flex', gap: 2 }} aria-hidden="true">
        {[0, 1, 2].map((i) => (
          <span
            key={i}
            style={{
              width: 4,
              height: 10,
              borderRadius: 1,
              background: i < filled ? 'var(--series-1)' : 'var(--axis)',
            }}
          />
        ))}
      </span>
      {CONFIDENCE_TEXT[confidence]}
    </span>
  )
}

export function Banner({
  tone = 'neutral',
  children,
}: {
  tone?: 'neutral' | 'good' | 'bad'
  children: React.ReactNode
}) {
  const color =
    tone === 'good' ? 'var(--status-good)' : tone === 'bad' ? 'var(--status-critical)' : 'var(--series-1)'
  return (
    <div className="banner" style={{ borderLeft: `3px solid ${color}` }} role="status">
      {children}
    </div>
  )
}

export function Empty({ title, children }: { title: string; children?: React.ReactNode }) {
  return (
    <div className="empty">
      <h3>{title}</h3>
      {children && <p>{children}</p>}
    </div>
  )
}

export function Skeleton({ height = 76, count = 4 }: { height?: number; count?: number }) {
  return (
    <div style={{ display: 'grid', gap: 8 }}>
      {Array.from({ length: count }).map((_, i) => (
        <div key={i} className="skeleton" style={{ height }} />
      ))}
    </div>
  )
}

export function formatDate(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}

export function relativeDate(iso: string): string {
  const days = Math.floor((Date.now() - new Date(iso).getTime()) / 86_400_000)
  if (days < 1) return 'today'
  if (days < 30) return `${days}d ago`
  if (days < 365) return `${Math.floor(days / 30)}mo ago`
  const years = (days / 365).toFixed(1).replace('.0', '')
  return `${years}y ago`
}
