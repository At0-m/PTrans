package domain

import "errors"

var (
	ErrPaymentNotFound      = errors.New("payment not found")
	ErrSubsctiptionNotFound = errors.New("subscription not found")
	ErrInvalidPaymentState  = errors.New("invalid payment state")
	ErrInvalidStatusFilter  = errors.New("invalid status filter")
	ErrInvalidPagination    = errors.New("invalid pagination")
)
