package internal

import "errors"

// Error definitions for the track publishing system
var (
	ErrUnsupportedMediaType = errors.New("unsupported media type")
	ErrMediaTypeMismatch   = errors.New("media type mismatch")
	ErrAlreadyStarted      = errors.New("already started")
	ErrNotStarted          = errors.New("not started")
	ErrTrackNotFound       = errors.New("track not found")
	ErrSubscriptionNotFound = errors.New("subscription not found")
	ErrInvalidGroup        = errors.New("invalid group number")
)