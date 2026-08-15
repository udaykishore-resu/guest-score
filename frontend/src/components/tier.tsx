import type { Score } from '../lib/api'
import { tierColor } from './ui'

/** DiscountCard is the guest-facing answer to "what does my score get me?".
 *  The score itself is a means; the discount is the thing a guest acts on, so
 *  it gets the hero treatment rather than being buried in a detail table. */
export function DiscountCard({ score }: { score: Score }) {
  const has = score.discount_percent > 0
  return (
    <div className="card">
      <h2 className="card-title">Booking terms</h2>
      <p className="card-sub">What this standing sets for the next stay.</p>

      <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 12 }}>
        <span style={{ fontSize: 14, color: 'var(--text-secondary)' }}>Available discount</span>
        <span
          style={{
            fontSize: 38,
            fontWeight: 660,
            letterSpacing: '-0.035em',
            lineHeight: 1,
            color: has ? 'var(--text-primary)' : 'var(--text-muted)',
          }}
        >
          {score.discount_percent}%
        </span>
      </div>

      <p style={{ fontSize: 13.5, color: 'var(--text-secondary)', margin: '10px 0 0' }}>
        {has
          ? `${score.tier} standing applies a ${score.discount_percent}% discount at booking.`
          : 'No discount at this standing. Reaching Good unlocks 10%.'}
      </p>

      <div
        style={{
          display: 'flex',
          alignItems: 'baseline',
          justifyContent: 'space-between',
          gap: 12,
          marginTop: 16,
          paddingTop: 14,
          borderTop: '1px solid var(--grid)',
        }}
      >
        <span style={{ fontSize: 14, color: 'var(--text-secondary)' }}>Security deposit</span>
        <span
          style={{
            fontSize: 26,
            fontWeight: 650,
            letterSpacing: '-0.03em',
            color: score.deposit_multiplier > 1 ? 'var(--status-critical)' : 'var(--text-primary)',
          }}
        >
          {(score.deposit_multiplier * 100).toFixed(0)}%
        </span>
      </div>
      <p style={{ fontSize: 13, color: 'var(--text-muted)', margin: '4px 0 0' }}>
        {score.deposit_multiplier < 1
          ? `${((1 - score.deposit_multiplier) * 100).toFixed(0)}% below the property's standard deposit.`
          : score.deposit_multiplier > 1
            ? `${((score.deposit_multiplier - 1) * 100).toFixed(0)}% above the standard deposit.`
            : "The property's standard deposit."}
      </p>

      {score.flagged && (
        <div
          style={{
            marginTop: 14,
            padding: '10px 12px',
            borderRadius: 'var(--radius-sm)',
            border: '1px solid var(--status-critical)',
            background: 'var(--surface-raised)',
            fontSize: 13,
          }}
          role="status"
        >
          <strong style={{ color: 'var(--status-critical)' }}>Flagged</strong> — this standing permits
          declining the booking. The system does not decline automatically; a manager decides, and the
          guest may appeal.
        </div>
      )}

      {score.next_tier && score.points_to_next_tier > 0 && (
        <TierProgress score={score} />
      )}
    </div>
  )
}

/** TierProgress makes the gap to the next tier concrete. A loyalty score that
 *  never tells you how close you are is just a number. */
function TierProgress({ score }: { score: Score }) {
  // Progress within the current band, floored so a guest at the bottom of a
  // band still sees a sliver rather than an empty bar that reads as broken.
  const target = score.composite + score.points_to_next_tier
  // Band widths on the 0–1000 scale are ~100–200 points; 200 keeps the bar
  // readable without pretending to know the exact floor below.
  const bandStart = target - 200
  const pct = Math.max(4, Math.min(100, ((score.composite - bandStart) / (target - bandStart)) * 100))

  return (
    <div style={{ marginTop: 16 }}>
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          fontSize: 12.5,
          color: 'var(--text-secondary)',
          marginBottom: 6,
        }}
      >
        <span>Progress to {score.next_tier}</span>
        <span style={{ fontVariantNumeric: 'tabular-nums' }}>
          {score.points_to_next_tier.toFixed(1)} points to go
        </span>
      </div>
      <div style={{ height: 8, background: 'var(--grid)', borderRadius: 4, overflow: 'hidden' }}>
        <div
          style={{
            width: `${pct}%`,
            height: '100%',
            background: tierColor(score.next_tier ?? ''),
            borderRadius: 4,
            transition: 'width 0.45s ease-out',
          }}
        />
      </div>
    </div>
  )
}

/** TierCard shows the standing itself, with the colour always paired with the
 *  tier name so hue never carries the meaning alone. */
export function TierCard({ score }: { score: Score }) {
  const color = tierColor(score.tier)
  return (
    <div className="card">
      <h2 className="card-title">Guest category</h2>
      <p className="card-sub">Standing across every member hotel in the bureau.</p>

      <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
        <span
          aria-hidden="true"
          style={{ width: 10, height: 10, borderRadius: '50%', background: color, flexShrink: 0 }}
        />
        <span style={{ fontSize: 22, fontWeight: 640, letterSpacing: '-0.02em' }}>{score.tier}</span>
      </div>

      <p style={{ fontSize: 13.5, color: 'var(--text-secondary)', margin: '10px 0 0' }}>
        {score.tier_note}
      </p>
    </div>
  )
}
