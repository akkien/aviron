package race

// Request/response DTOs for the race HTTP handlers, kept together so the
// wire format for this domain is easy to scan in one place.

type createRaceRequest struct {
	Name           string `json:"name"`
	DistanceMeters int    `json:"distance_meters"`
}

type createRaceResponse struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	DistanceMeters int    `json:"distance_meters"`
	Status         string `json:"status"`
	CreatedBy      string `json:"created_by"`
	CreatedAt      string `json:"created_at"`
}
