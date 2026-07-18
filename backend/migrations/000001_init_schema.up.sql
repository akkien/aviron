CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE users (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  email TEXT UNIQUE NOT NULL,
  display_name TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE races (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  distance_meters INT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  created_by UUID NOT NULL REFERENCES users(id),
  started_at TIMESTAMPTZ,
  ended_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE race_participants (
  race_id UUID NOT NULL REFERENCES races(id),
  user_id UUID NOT NULL REFERENCES users(id),
  finish_rank INT,
  finish_time_ms BIGINT,
  avg_pace_watt NUMERIC,
  disconnected_count INT NOT NULL DEFAULT 0,
  joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (race_id, user_id)
);

CREATE TABLE workout_samples (
  id BIGSERIAL PRIMARY KEY,
  race_id UUID NOT NULL REFERENCES races(id),
  user_id UUID NOT NULL REFERENCES users(id),
  ts TIMESTAMPTZ NOT NULL,
  distance_m NUMERIC NOT NULL,
  pace_watt NUMERIC,
  stroke_rate INT
);
CREATE INDEX idx_workout_samples_race_user_ts ON workout_samples (race_id, user_id, ts);

CREATE TABLE leaderboard_alltime (
  user_id UUID PRIMARY KEY REFERENCES users(id),
  best_2000m_ms BIGINT,
  total_races INT NOT NULL DEFAULT 0,
  total_distance_m NUMERIC NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
