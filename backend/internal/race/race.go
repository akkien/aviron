package race

import "time"

type Race struct {
	ID             string
	Name           string
	DistanceMeters int
	Status         string
	CreatedBy      string
	CreatedAt      time.Time
}
