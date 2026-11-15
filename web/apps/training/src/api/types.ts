// Типы ответов cue-vision. Источник правды — структуры в internal/store
// (trainings.go, shots.go, stats.go, players.go); при смене API править здесь.
// Время — строка RFC 3339 в UTC, расстояния и скорости — в нормированных единицах стола.

export interface List<T> {
  items: T[]
}

export type Pocket = 'tl' | 'tm' | 'tr' | 'bl' | 'bm' | 'br'
export type Outcome = 'pot' | 'miss' | 'no_contact' | 'in_off'
export type ShotStatus = 'open' | 'closed' | 'aborted' | 'rejected'
export type TrainingStatus = 'active' | 'finished'
export type TrainingOrigin = 'manual' | 'auto'

export interface Player {
  id: number
  name: string
  created_at: string
}

export interface Training {
  id: number
  stream: string
  status: TrainingStatus
  origin: TrainingOrigin
  player_id?: number
  player_name?: string
  session_id?: number
  title?: string
  exercise?: string
  notes?: string
  started_at: string
  ended_at?: string
  created_at: string
  shots: number
  pots: number
}

export interface NewTraining {
  stream?: string
  player_id?: number
  title?: string
  exercise?: string
  notes?: string
  started_at?: string
}

/** PATCH /trainings/{id}: переданное поле меняется, null очищает. */
export interface TrainingPatch {
  player_id?: number | null
  title?: string | null
  exercise?: string | null
  notes?: string | null
}

export interface PocketStat {
  pocket: Pocket
  pots: number
  near_misses: number
  near_miss_left: number
  near_miss_right: number
}

export interface BallStat {
  ball: string
  pots: number
}

export interface BucketStat {
  label: string
  from: number
  to: number
  shots: number
  pots: number
  pot_rate: number | null
}

export interface Stats {
  shots: number
  pots: number
  misses: number
  no_contact: number
  in_offs: number
  pot_rate: number | null
  balls_potted: number
  best_streak: number
  declared_shots: number

  near_misses: number
  near_miss_left: number
  near_miss_right: number
  avg_near_miss_distance: number | null

  avg_cue_speed: number | null
  max_cue_speed: number | null
  avg_cut_angle: number | null
  avg_cue_to_object: number | null
  avg_object_to_pocket: number | null
  avg_shot_duration: number | null
  avg_cushions: number | null

  first_shot_at: string | null
  last_shot_at: string | null

  by_pocket: PocketStat[]
  by_ball: BallStat[]
  by_cut_angle: BucketStat[]
  by_distance: BucketStat[]

  warnings?: Record<string, number>
}

export interface TrainingStats {
  training: Training
  /** Секунды от начала до конца тренировки (или до «сейчас», если идёт). */
  duration: number
  stats: Stats
}

export interface Shot {
  id: number
  session_id: number
  training_id?: number
  shot_no: number
  status: ShotStatus
  reject_reason?: string
  t_start: number
  t_end?: number
  started_at: string
  duration?: number
  cue_ball?: string
  cue_x?: number
  cue_y?: number
  cue_direction?: number
  cue_speed?: number
  cue_source?: string
  first_contact?: string
  contacts: string[]
  cushions: number
  pocketed: string[]
  object_potted: number
  cue_potted: boolean
  cut_angle?: number
  cue_to_object?: number
  object_to_pocket?: number
  obstructed?: boolean
  near_miss_ball?: string
  near_miss_pocket?: Pocket
  near_miss_distance?: number
  near_miss_side?: 'left' | 'right'
  near_miss_conf?: number
  target_ball?: string
  target_pocket?: Pocket
  outcome?: Outcome
  success?: boolean
  conf?: number
}
