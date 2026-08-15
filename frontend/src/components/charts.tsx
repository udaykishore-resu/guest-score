import { useState } from 'react'
import type { DimensionScore, Score } from '../lib/api'
import { gradeColor } from './ui'

/* Charts are hand-rolled SVG: four simple forms did not justify a charting
   dependency. Conventions follow the dataviz reference —
   - one series per chart, so no legend is needed and no CVD pair can collide;
   - 4px rounded data-ends anchored to the baseline;
   - recessive grid, muted axis ink, text in text tokens rather than mark colour;
   - a hover tooltip on every plotted mark;
   - a table view behind a <details> for every chart, which is also the relief
     for the sub-3:1 status colours on the light surface. */

function useTooltip() {
  const [tip, setTip] = useState<{ x: number; y: number; text: string } | null>(null)
  const show = (e: React.MouseEvent, text: string) => setTip({ x: e.clientX, y: e.clientY, text })
  const hide = () => setTip(null)
  const node = tip ? (
    <div className="chart-tooltip" style={{ left: tip.x + 12, top: tip.y - 34 }} role="tooltip">
      {tip.text}
    </div>
  ) : null
  return { show, hide, node }
}

/** ScoreDial — the hero figure. A single number is not a chart, so this is a
 *  gauge around a large value rather than a plot. Status colour is paired with
 *  the grade letter and label so hue never carries the meaning alone. */
export function ScoreDial({ score, size = 168 }: { score: Score; size?: number }) {
  const stroke = 11
  const r = (size - stroke) / 2
  const circumference = 2 * Math.PI * r
  const pct = score.rated ? score.composite / 100 : 0
  // Leave a 25% gap at the bottom so the arc reads as a gauge, not a pie.
  const arcSpan = 0.75
  const dash = circumference * arcSpan * pct
  const gap = circumference - dash
  const color = gradeColor(score.grade)

  return (
    <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 10 }}>
      <div style={{ position: 'relative', width: size, height: size }}>
        <svg width={size} height={size} style={{ transform: 'rotate(135deg)' }} aria-hidden="true">
          <circle
            cx={size / 2}
            cy={size / 2}
            r={r}
            fill="none"
            stroke="var(--grid)"
            strokeWidth={stroke}
            strokeLinecap="round"
            strokeDasharray={`${circumference * arcSpan} ${circumference * (1 - arcSpan)}`}
          />
          {score.rated && (
            <circle
              cx={size / 2}
              cy={size / 2}
              r={r}
              fill="none"
              stroke={color}
              strokeWidth={stroke}
              strokeLinecap="round"
              strokeDasharray={`${dash} ${gap}`}
              style={{ transition: 'stroke-dasharray 0.5s ease-out' }}
            />
          )}
        </svg>
        <div
          style={{
            position: 'absolute',
            inset: 0,
            display: 'grid',
            placeItems: 'center',
            textAlign: 'center',
          }}
        >
          {score.rated ? (
            <div>
              <div style={{ fontSize: size * 0.29, fontWeight: 660, letterSpacing: '-0.04em', lineHeight: 1 }}>
                {score.composite.toFixed(0)}
              </div>
              <div style={{ fontSize: 12, color: 'var(--text-muted)', marginTop: 2 }}>out of 100</div>
            </div>
          ) : (
            <div>
              <div style={{ fontSize: size * 0.2, fontWeight: 620, color: 'var(--text-muted)', lineHeight: 1 }}>
                —
              </div>
              <div style={{ fontSize: 12, color: 'var(--text-muted)', marginTop: 4 }}>unrated</div>
            </div>
          )}
        </div>
      </div>
      {score.rated && (
        <div style={{ textAlign: 'center' }}>
          <span style={{ fontSize: 15, fontWeight: 620 }}>
            Grade {score.grade}
          </span>
          <span style={{ fontSize: 14, color: 'var(--text-secondary)' }}> · {score.grade_label}</span>
        </div>
      )}
    </div>
  )
}

/** DimensionBars — magnitude on a fixed 1–5 scale, one series, direct-labelled.
 *  The weight is shown per row because the composite is a weighted mean and a
 *  reader cannot reconstruct it from the averages alone. */
export function DimensionBars({
  dimensions,
  showWeights = true,
}: {
  dimensions: DimensionScore[]
  /** Off for the portfolio chart, which is an unweighted mean — printing
   *  "0% weight" there would state something false about the model. */
  showWeights?: boolean
}) {
  const { show, hide, node } = useTooltip()
  const max = 5

  return (
    <div>
      <div style={{ display: 'grid', gap: 11 }}>
        {dimensions.map((d) => {
          const pct = (d.average / max) * 100
          return (
            <div key={d.dimension}>
              <div
                style={{
                  display: 'flex',
                  justifyContent: 'space-between',
                  alignItems: 'baseline',
                  marginBottom: 4,
                  gap: 10,
                }}
              >
                <span style={{ fontSize: 13.5, color: 'var(--text-secondary)' }}>{d.label}</span>
                <span
                  style={{
                    fontSize: 13,
                    fontWeight: 600,
                    fontVariantNumeric: 'tabular-nums',
                    color: 'var(--text-primary)',
                  }}
                >
                  {d.average.toFixed(1)}
                  {showWeights && (
                    <span style={{ color: 'var(--text-muted)', fontWeight: 450 }}>
                      {' '}
                      · {(d.weight * 100).toFixed(0)}% weight
                    </span>
                  )}
                </span>
              </div>
              <div
                style={{ height: 8, background: 'var(--grid)', borderRadius: 4, overflow: 'hidden' }}
                onMouseMove={(e) =>
                  show(e, `${d.label}: ${d.average.toFixed(1)}/5 · contributes ${d.contributes.toFixed(1)} pts`)
                }
                onMouseLeave={hide}
              >
                <div
                  style={{
                    width: `${pct}%`,
                    height: '100%',
                    background: 'var(--series-1)',
                    borderRadius: 4,
                    transition: 'width 0.45s ease-out',
                  }}
                />
              </div>
            </div>
          )
        })}
      </div>
      {node}
      <details className="table-view">
        <summary>View as table</summary>
        <table className="data-table">
          <thead>
            <tr>
              <th>Dimension</th>
              {showWeights && <th>Weight</th>}
              <th>Average</th>
            </tr>
          </thead>
          <tbody>
            {dimensions.map((d) => (
              <tr key={d.dimension}>
                <td>{d.label}</td>
                {showWeights && <td>{(d.weight * 100).toFixed(0)}%</td>}
                <td>{d.average.toFixed(1)} / 5</td>
              </tr>
            ))}
          </tbody>
        </table>
      </details>
    </div>
  )
}

/** BandDistribution — counts per grade band.
 *
 *  Deliberately ONE colour rather than a status colour per band: the band
 *  letter is already on the axis, so colouring by rank would be redundant
 *  encoding, and the four status hues fail the adjacent normal-vision ΔE floor
 *  when placed side by side. The letters carry identity; length carries count. */
export function BandDistribution({ bands }: { bands: Record<string, number> }) {
  const { show, hide, node } = useTooltip()
  const order = ['A', 'B', 'C', 'D', 'F']
  const rows = order.map((g) => ({ grade: g, count: bands[g] ?? 0 }))
  const max = Math.max(1, ...rows.map((r) => r.count))
  const total = rows.reduce((s, r) => s + r.count, 0)
  // labelPad reserves room above the tallest bar for its value label; without
  // it the count on the tallest bar is clipped by the viewBox.
  const labelPad = 18
  const plotH = 118
  const height = plotH + labelPad
  const barW = 40
  const gapW = 20
  const width = rows.length * barW + (rows.length - 1) * gapW

  return (
    <div>
      <svg width="100%" height={height + 28} viewBox={`0 0 ${width} ${height + 28}`} role="img"
           aria-label="Guest count by grade band">
        <line x1={0} y1={height} x2={width} y2={height} stroke="var(--axis)" strokeWidth={1} />
        {rows.map((r, i) => {
          const h = r.count === 0 ? 0 : Math.max(3, (r.count / max) * plotH)
          const x = i * (barW + gapW)
          return (
            <g key={r.grade}>
              {r.count > 0 && (
                <rect
                  x={x}
                  y={height - h}
                  width={barW}
                  height={h}
                  rx={4}
                  fill="var(--series-1)"
                  onMouseMove={(e) =>
                    show(e, `Grade ${r.grade}: ${r.count} guest${r.count === 1 ? '' : 's'}`)
                  }
                  onMouseLeave={hide}
                />
              )}
              <text
                x={x + barW / 2}
                y={height - h - 6}
                textAnchor="middle"
                fontSize={12}
                fontWeight={600}
                fill="var(--text-primary)"
                style={{ fontVariantNumeric: 'tabular-nums' }}
              >
                {r.count || ''}
              </text>
              <text
                x={x + barW / 2}
                y={height + 18}
                textAnchor="middle"
                fontSize={13}
                fontWeight={600}
                fill="var(--text-secondary)"
              >
                {r.grade}
              </text>
            </g>
          )
        })}
      </svg>
      {node}
      <details className="table-view">
        <summary>View as table</summary>
        <table className="data-table">
          <thead>
            <tr>
              <th>Grade</th>
              <th>Guests</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.grade}>
                <td>{r.grade}</td>
                <td>
                  {r.count}
                  {total > 0 && (
                    <span style={{ color: 'var(--text-muted)' }}>
                      {' '}
                      ({((r.count / total) * 100).toFixed(0)}%)
                    </span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </details>
    </div>
  )
}

/** ReviewTimeline — change over time, single series, 12 monthly buckets. */
export function ReviewTimeline({ data }: { data: { month: string; count: number }[] }) {
  const { show, hide, node } = useTooltip()
  const width = 520
  const height = 128
  const padY = 14
  const max = Math.max(1, ...data.map((d) => d.count))
  const barW = width / data.length - 6

  const monthLabel = (m: string) => {
    const [y, mo] = m.split('-')
    const name = new Date(Number(y), Number(mo) - 1, 1).toLocaleDateString(undefined, { month: 'short' })
    return { name, year: y }
  }

  return (
    <div>
      <svg
        width="100%"
        height={height + 26}
        viewBox={`0 0 ${width} ${height + 26}`}
        preserveAspectRatio="none"
        role="img"
        aria-label="Reviews submitted per month over the last 12 months"
      >
        {[0.5, 1].map((f) => (
          <line
            key={f}
            x1={0}
            y1={padY + (height - padY) * (1 - f)}
            x2={width}
            y2={padY + (height - padY) * (1 - f)}
            stroke="var(--grid)"
            strokeWidth={1}
          />
        ))}
        <line x1={0} y1={height} x2={width} y2={height} stroke="var(--axis)" strokeWidth={1} />
        {data.map((d, i) => {
          const h = d.count === 0 ? 0 : Math.max(2, (d.count / max) * (height - padY))
          const x = i * (width / data.length) + 3
          const { name } = monthLabel(d.month)
          return (
            <g key={d.month}>
              {h > 0 && (
                <rect
                  x={x}
                  y={height - h}
                  width={barW}
                  height={h}
                  rx={4}
                  fill="var(--series-1)"
                  onMouseMove={(e) => show(e, `${d.month}: ${d.count} review${d.count === 1 ? '' : 's'}`)}
                  onMouseLeave={hide}
                />
              )}
              {/* Label every other month so ticks never collide at this width. */}
              {i % 2 === 0 && (
                <text
                  x={x + barW / 2}
                  y={height + 17}
                  textAnchor="middle"
                  fontSize={10.5}
                  fill="var(--text-muted)"
                >
                  {name}
                </text>
              )}
            </g>
          )
        })}
      </svg>
      {node}
      <details className="table-view">
        <summary>View as table</summary>
        <table className="data-table">
          <thead>
            <tr>
              <th>Month</th>
              <th>Reviews</th>
            </tr>
          </thead>
          <tbody>
            {data.map((d) => (
              <tr key={d.month}>
                <td>{d.month}</td>
                <td>{d.count}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </details>
    </div>
  )
}

/** Sparkline of the score composition: base score, then the penalty taken off.
 *  A stacked single bar makes "where did the points go" legible at a glance. */
export function ScoreComposition({ score }: { score: Score }) {
  const { show, hide, node } = useTooltip()
  if (!score.rated) return null
  const basePct = score.base_score
  const penaltyPct = Math.min(score.incident_penalty, score.base_score)

  return (
    <div>
      <div
        style={{
          display: 'flex',
          height: 12,
          borderRadius: 6,
          overflow: 'hidden',
          background: 'var(--grid)',
          gap: 2,
        }}
      >
        <div
          style={{ width: `${score.composite}%`, background: 'var(--series-1)', borderRadius: 6 }}
          onMouseMove={(e) => show(e, `Final score: ${score.composite.toFixed(1)}`)}
          onMouseLeave={hide}
        />
        {penaltyPct > 0 && (
          <div
            style={{ width: `${penaltyPct}%`, background: 'var(--status-critical)', borderRadius: 6 }}
            onMouseMove={(e) => show(e, `Incident penalty: −${score.incident_penalty.toFixed(1)} points`)}
            onMouseLeave={hide}
          />
        )}
      </div>
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          fontSize: 12.5,
          color: 'var(--text-secondary)',
          marginTop: 7,
          gap: 12,
          flexWrap: 'wrap',
        }}
      >
        <span>
          Ratings gave <strong style={{ color: 'var(--text-primary)' }}>{basePct.toFixed(1)}</strong>
        </span>
        {score.incident_penalty > 0 && (
          <span>
            Incidents took{' '}
            <strong style={{ color: 'var(--status-critical)' }}>
              −{score.incident_penalty.toFixed(1)}
            </strong>
          </span>
        )}
        <span>
          Final <strong style={{ color: 'var(--text-primary)' }}>{score.composite.toFixed(1)}</strong>
        </span>
      </div>
      {node}
    </div>
  )
}
