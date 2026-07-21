-- races.id switches from a UUID (Postgres's gen_random_uuid() default) to a
-- 12-character base58 string generated in Go (internal/race.GenerateRaceID)
-- — short enough for a player to read aloud or type by hand to invite
-- others into a race. FK columns referencing it must match.

ALTER TABLE race_participants DROP CONSTRAINT race_participants_race_id_fkey;
ALTER TABLE workout_samples DROP CONSTRAINT workout_samples_race_id_fkey;

ALTER TABLE races ALTER COLUMN id DROP DEFAULT;
ALTER TABLE races ALTER COLUMN id TYPE TEXT USING id::text;

ALTER TABLE race_participants ALTER COLUMN race_id TYPE TEXT USING race_id::text;
ALTER TABLE workout_samples ALTER COLUMN race_id TYPE TEXT USING race_id::text;

ALTER TABLE race_participants ADD CONSTRAINT race_participants_race_id_fkey FOREIGN KEY (race_id) REFERENCES races(id);
ALTER TABLE workout_samples ADD CONSTRAINT workout_samples_race_id_fkey FOREIGN KEY (race_id) REFERENCES races(id);
