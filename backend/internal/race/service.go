package race

import (
	"context"
	"strings"
)

type RaceService struct {
	repo RaceRepository
}

func NewRaceService(repo RaceRepository) *RaceService {
	return &RaceService{repo: repo}
}

// CreateRace validates and creates a new race. A non-empty fieldErrs return
// means validation failed and err is always nil in that case; err is only
// set for downstream repository failures.
func (s *RaceService) CreateRace(ctx context.Context, name string, distanceMeters int, createdBy string) (r Race, fieldErrs map[string]string, err error) {
	if errs := validateCreateRace(name, distanceMeters); len(errs) > 0 {
		return Race{}, errs, nil
	}

	r, err = s.repo.CreateRace(ctx, strings.TrimSpace(name), distanceMeters, createdBy)
	if err != nil {
		return Race{}, nil, err
	}
	return r, nil, nil
}

func validateCreateRace(name string, distanceMeters int) map[string]string {
	errs := map[string]string{}

	trimmed := strings.TrimSpace(name)
	if len(trimmed) == 0 || len(name) > 100 {
		errs["name"] = "must be 1-100 characters"
	}
	if distanceMeters <= 0 {
		errs["distance_meters"] = "must be a positive integer"
	}

	return errs
}
