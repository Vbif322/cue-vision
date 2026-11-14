-- Исходная схема cue-vision.
--
-- sessions  — прогон детектора: от session_start до session_end на одном потоке;
-- trainings — тренировка: окно времени на потоке, в которое попадают удары;
-- shots     — удар, собранный из shot_start / cue_strike / shot_end;
-- pots      — падение шара в лузу (ball_pocketed);
-- events    — сырой поток как пришёл, для разбора и пересчёта.

CREATE TABLE players (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       text        NOT NULL CHECK (btrim(name) <> ''),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX players_name_key ON players (lower(name));

CREATE TABLE sessions (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    stream         text        NOT NULL,
    status         text        NOT NULL DEFAULT 'active'
                   CHECK (status IN ('active', 'ended', 'interrupted')),
    -- Начало шкалы времени сессии: абсолютное время события = origin_at + t.
    origin_at      timestamptz NOT NULL,
    -- started_at из session_start, как его сообщил детектор.
    started_at     timestamptz,
    ended_at       timestamptz,
    -- Сессия заведена без session_start (подключились к потоку на ходу).
    implicit       boolean     NOT NULL DEFAULT false,
    source         text,
    source_kind    text,
    hall           text,
    table_name     text,
    game           text,
    fps            double precision,
    schema_version text,
    tool           text,
    ball_count     integer,
    calibration    jsonb,
    summary        jsonb,
    last_t         double precision NOT NULL DEFAULT 0,
    received_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (stream, origin_at)
);

CREATE UNIQUE INDEX sessions_one_active_per_stream ON sessions (stream) WHERE status = 'active';

CREATE TABLE trainings (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    stream     text        NOT NULL,
    player_id  bigint      REFERENCES players (id) ON DELETE SET NULL,
    -- Заполнено только у автоматических тренировок: они живут внутри одной сессии.
    session_id bigint      REFERENCES sessions (id) ON DELETE CASCADE,
    origin     text        NOT NULL CHECK (origin IN ('manual', 'auto')),
    title      text,
    exercise   text,
    notes      text,
    started_at timestamptz NOT NULL,
    ended_at   timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (ended_at IS NULL OR ended_at >= started_at),
    CHECK (origin = 'manual' OR session_id IS NOT NULL)
);

CREATE UNIQUE INDEX trainings_one_open_manual_per_stream
    ON trainings (stream) WHERE ended_at IS NULL AND origin = 'manual';
CREATE INDEX trainings_stream_started ON trainings (stream, started_at DESC);
CREATE INDEX trainings_player_started ON trainings (player_id, started_at DESC);
CREATE INDEX trainings_session ON trainings (session_id) WHERE session_id IS NOT NULL;

CREATE TABLE shots (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    session_id         bigint      NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    training_id        bigint      REFERENCES trainings (id) ON DELETE SET NULL,
    shot_no            integer     NOT NULL,
    status             text        NOT NULL DEFAULT 'open'
                       CHECK (status IN ('open', 'closed', 'aborted')),
    t_start            double precision NOT NULL,
    t_end              double precision,
    started_at         timestamptz NOT NULL,
    duration           double precision,
    cue_ball           text,
    cue_x              double precision,
    cue_y              double precision,
    cue_direction      double precision,
    cue_speed          double precision,
    cue_source         text,
    first_contact      text,
    contacts           text[]      NOT NULL DEFAULT '{}',
    cushions           integer     NOT NULL DEFAULT 0,
    pocketed           text[]      NOT NULL DEFAULT '{}',
    object_potted      integer     NOT NULL DEFAULT 0,
    cue_potted         boolean     NOT NULL DEFAULT false,
    cut_angle          double precision,
    cue_to_object      double precision,
    object_to_pocket   double precision,
    obstructed         boolean,
    near_miss_ball     text,
    near_miss_pocket   text,
    near_miss_distance double precision,
    near_miss_side     text,
    near_miss_conf     double precision,
    target_ball        text,
    target_pocket      text,
    outcome            text CHECK (outcome IN ('pot', 'miss', 'no_contact', 'in_off')),
    success            boolean,
    conf               double precision
);

CREATE INDEX shots_session ON shots (session_id, t_start);
CREATE INDEX shots_training ON shots (training_id, started_at) WHERE training_id IS NOT NULL;

CREATE TABLE pots (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    session_id  bigint      NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    shot_id     bigint      REFERENCES shots (id) ON DELETE CASCADE,
    ball        text        NOT NULL,
    pocket      text,
    entry_speed double precision,
    is_cue      boolean     NOT NULL DEFAULT false,
    t           double precision NOT NULL,
    occurred_at timestamptz NOT NULL,
    conf        double precision
);

CREATE INDEX pots_shot ON pots (shot_id);
CREATE INDEX pots_session ON pots (session_id, t);

CREATE TABLE events (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    session_id  bigint      NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    shot_id     bigint      REFERENCES shots (id) ON DELETE SET NULL,
    t           double precision NOT NULL,
    frame       bigint      NOT NULL,
    type        text        NOT NULL,
    immediate   boolean     NOT NULL DEFAULT false,
    conf        double precision,
    occurred_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    -- sha256 канонического JSON события: повторная доставка не даёт дубля.
    hash        bytea       NOT NULL,
    payload     jsonb       NOT NULL,
    UNIQUE (session_id, hash)
);

CREATE INDEX events_session_type ON events (session_id, type, id);
CREATE INDEX events_shot ON events (shot_id, t) WHERE shot_id IS NOT NULL;
