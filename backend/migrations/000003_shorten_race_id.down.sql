-- Reverts races.id back to UUID. Only valid if every existing race.id
-- (and the FK columns referencing it) still holds a real UUID string —
-- a dev-only rollback path, not a data-preserving one, consistent with
-- this project's other migrations.

ALTER TABLE race_participants DROP CONSTRAINT race_participants_race_id_fkey;
ALTER TABLE workout_samples DROP CONSTRAINT workout_samples_race_id_fkey;

ALTER TABLE races ALTER COLUMN id TYPE UUID USING id::uuid;
ALTER TABLE races ALTER COLUMN id SET DEFAULT gen_random_uuid();

ALTER TABLE race_participants ALTER COLUMN race_id TYPE UUID USING race_id::uuid;
ALTER TABLE workout_samples ALTER COLUMN race_id TYPE UUID USING race_id::uuid;

ALTER TABLE race_participants ADD CONSTRAINT race_participants_race_id_fkey FOREIGN KEY (race_id) REFERENCES races(id);
ALTER TABLE workout_samples ADD CONSTRAINT workout_samples_race_id_fkey FOREIGN KEY (race_id) REFERENCES races(id);
