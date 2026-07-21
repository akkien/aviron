package race

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// base58Alphabet is the Bitcoin base58 alphabet — excludes 0, O, I, and l,
// which are easy to mix up when a race id is read aloud or typed by hand to
// invite another player.
const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// raceIDLength is fixed at 12 characters: short enough to share verbally,
// long enough that log2(58)*12 ≈ 70 bits of entropy makes a collision
// vanishingly unlikely — CreateRace still retries on the rare unique
// -constraint violation rather than relying on entropy alone.
const raceIDLength = 12

// GenerateRaceID returns a random 12-character base58 race id, replacing
// Postgres's previous gen_random_uuid() default with something short enough
// for a player to read aloud or type by hand.
func GenerateRaceID() (string, error) {
	id := make([]byte, raceIDLength)
	alphabetLen := big.NewInt(int64(len(base58Alphabet)))
	for i := range id {
		n, err := rand.Int(rand.Reader, alphabetLen)
		if err != nil {
			return "", fmt.Errorf("race: generate race id: %w", err)
		}
		id[i] = base58Alphabet[n.Int64()]
	}
	return string(id), nil
}
