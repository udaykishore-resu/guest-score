// Typed client for the Guest Score API. The shapes here mirror the Go DTOs in
// backend/internal/api; keep them in sync when the API changes.

export type Dimension =
  | 'house_rules'
  | 'property_care'
  | 'communication'
  | 'noise'
  | 'accuracy'

export type IncidentType =
  | 'property_damage'
  | 'noise_complaint'
  | 'unauthorized_guests'
  | 'house_rules_violation'
  | 'late_checkout'
  | 'payment_issue'

export type Severity = 'minor' | 'moderate' | 'severe'

export type Confidence = 'none' | 'low' | 'medium' | 'high'

export type Handling =
  | 'vip_treatment'
  | 'standard'
  | 'watch'
  | 'escalate'
  | 'insufficient_data'

export interface Ratings {
  house_rules: number
  property_care: number
  communication: number
  noise: number
  accuracy: number
}

export interface Incident {
  type: IncidentType
  severity: Severity
  note?: string
}

export type CommendationType =
  | 'exceptional_room_care'
  | 'staff_commendation'
  | 'repeat_stay'
  | 'accommodating'
  | 'referral'

export interface Commendation {
  type: CommendationType
  note?: string
}

export interface DimensionScore {
  dimension: Dimension
  label: string
  average: number
  weight: number
  contributes: number
}

export interface Factor {
  kind: 'strength' | 'concern' | 'penalty' | 'bonus' | 'context'
  description: string
  impact: number
}

export interface Score {
  rated: boolean
  composite: number

  /** Loyalty standing and what it earns the guest. */
  tier: string
  discount_percent: number
  deposit_multiplier: number
  flagged: boolean
  tier_note: string
  points_to_next_tier: number
  next_tier?: string

  confidence: Confidence
  handling: Handling
  headline: string

  stay_count: number
  disputed_count: number
  effective_stay_count: number
  incident_count: number
  commendation_count: number

  raw_average: number
  adjusted_average: number
  base_score: number
  incident_penalty: number
  commendation_bonus: number
  tenure_bonus: number
  tenure_years: number

  dimensions: DimensionScore[]
  factors: Factor[]
}

export interface Guest {
  id: string
  name: string
  email: string
  phone?: string
  city?: string
  verified: boolean
  joined_at: string
  avatar_seed: string
}

export interface GuestSummary extends Guest {
  score: Score
  last_stay_at?: string
}

export interface Review {
  id: string
  guest_id: string
  host_id: string
  host_name: string
  property_id: string
  property_name: string
  stay_id: string
  room_number?: string
  ratings: Ratings
  incidents: Incident[]
  commendations: Commendation[]
  comment: string
  check_in: string
  check_out: string
  submitted_at: string
}

export interface GuestDetail extends GuestSummary {
  reviews: Review[]
}

export interface Stats {
  empty: boolean
  total_guests: number
  total_reviews: number
  rated_guests: number
  unrated_guests: number
  average_score: number
  guests_with_incidents: number
  total_incidents: number
  tier_distribution: Record<string, number>
  guests_with_discount: number
  flagged_guests: number
  average_discount: number
  dimension_averages: { dimension: Dimension; label: string; average: number }[]
  review_timeline: { month: string; count: number }[]
}

export interface ScoringModel {
  dimensions: { dimension: Dimension; label: string; weight: number }[]
  review_half_life_days: number
  incident_half_life_days: number
  prior_mean: number
  prior_strength: number
  commendation_half_life_days: number
  score_min: number
  score_max: number
  new_guest_score: number
  tenure_points_per_year: number
  tenure_max_points: number
  tiers: {
    min: number
    name: string
    discount_percent: number
    deposit_multiplier: number
    flagged: boolean
    description: string
  }[]
  incident_catalog: {
    type: IncidentType
    label: string
    base_penalty: number
    description: string
  }[]
  commendation_catalog: {
    type: CommendationType
    label: string
    base_bonus: number
    description: string
  }[]
  severity_multipliers: Record<Severity, number>
}

export interface CreateReviewResult {
  review: Review
  score_before: Score
  score_after: Score
  composite_delta: number
}

/** ApiError carries the server's field-level validation detail to the form. */
export class ApiError extends Error {
  status: number
  code: string
  fields: Record<string, string>

  constructor(status: number, code: string, message: string, fields: Record<string, string> = {}) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.fields = fields
  }
}

const BASE = import.meta.env.VITE_API_BASE ?? ''

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let res: Response
  try {
    res = await fetch(`${BASE}${path}`, {
      ...init,
      headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) },
    })
  } catch {
    // A network failure is indistinguishable from a stopped backend here, and
    // "failed to fetch" is useless to a user, so say the useful thing instead.
    throw new ApiError(0, 'network', 'Could not reach the Guest Score API. Is the backend running?')
  }

  if (!res.ok) {
    let code = 'unknown'
    let message = `Request failed with status ${res.status}`
    let fields: Record<string, string> = {}
    try {
      const body = await res.json()
      if (body?.error) {
        code = body.error.code ?? code
        message = body.error.message ?? message
        fields = body.error.fields ?? {}
      }
    } catch {
      /* non-JSON error body; keep the status-based message */
    }
    throw new ApiError(res.status, code, message, fields)
  }

  if (res.status === 204) return undefined as T
  return res.json() as Promise<T>
}

export interface GuestQuery {
  q?: string
  tier?: string
  incidents?: boolean
  sort?: string
}

export const api = {
  health: () => request<{ status: string }>('/api/health'),

  listGuests: (query: GuestQuery = {}) => {
    const p = new URLSearchParams()
    if (query.q) p.set('q', query.q)
    if (query.tier) p.set('tier', query.tier)
    if (query.incidents) p.set('incidents', 'true')
    if (query.sort) p.set('sort', query.sort)
    const qs = p.toString()
    return request<{ guests: GuestSummary[]; total: number }>(`/api/guests${qs ? `?${qs}` : ''}`)
  },

  getGuest: (id: string) => request<GuestDetail>(`/api/guests/${encodeURIComponent(id)}`),

  createGuest: (guest: { name: string; email: string; city?: string; phone?: string }) =>
    request<GuestSummary>('/api/guests', { method: 'POST', body: JSON.stringify(guest) }),

  createReview: (review: {
    guest_id: string
    stay_id?: string
    property_name?: string
    room_number?: string
    ratings: Ratings
    incidents: Incident[]
    commendations: Commendation[]
    comment: string
    check_in?: string
    check_out?: string
  }) =>
    request<CreateReviewResult>('/api/reviews', {
      method: 'POST',
      body: JSON.stringify(review),
    }),

  listReviews: (limit = 12) =>
    request<{ reviews: (Review & { guest_name: string })[] }>(`/api/reviews?limit=${limit}`),

  stats: () => request<Stats>('/api/stats'),

  scoringModel: () => request<ScoringModel>('/api/scoring-model'),
}
