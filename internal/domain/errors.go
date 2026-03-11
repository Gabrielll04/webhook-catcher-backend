package domain

import "errors"

var (
	ErrNotFound        = errors.New("not found")
	ErrInboxDisabled   = errors.New("inbox disabled")
	ErrPayloadTooLarge = errors.New("payload too large")
	ErrInvalidInput    = errors.New("invalid input")
	ErrConflict        = errors.New("conflict")
	ErrUnauthorized    = errors.New("unauthorized")
	ErrForbidden       = errors.New("forbidden")
	ErrRateLimited     = errors.New("rate limited")
)
