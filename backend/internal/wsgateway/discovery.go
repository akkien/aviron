package wsgateway

import "errors"

// errNoBackends is nextBackend's error when the discovery source's
// current pool is empty — possible now that the pool can change at
// runtime, unlike when it was a fixed, LoadConfig-validated slice.
var errNoBackends = errors.New("wsgateway: no backends available")

// BackendDiscovery reports the current live pool of race-service
// addresses (host:port) round-robin'd across for room-less requests
// (dynamic-backend-discovery.md). Room-scoped requests never consult
// this — they resolve via RoomLocator.Owner instead, which already
// reflects a changing race-service pod set with no help from this
// interface.
type BackendDiscovery interface {
	// Backends returns the current backend pool. Called on every
	// room-less request — must be cheap and non-blocking.
	Backends() []string
}

// StaticBackends satisfies BackendDiscovery with a fixed slice, set once
// at construction and never mutated — what local go run/docker-compose
// keeps using via RACE_SERVICE_INSTANCES, unchanged from this project's
// original behavior.
type StaticBackends []string

func (s StaticBackends) Backends() []string { return s }
