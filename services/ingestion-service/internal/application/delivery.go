package application

import "errors"

type DeliveryAction uint8

const (
	DeliveryAcknowledge DeliveryAction = iota
	DeliveryReject
	DeliveryRequeue
)

// DeliveryDisposition is the single outcome policy shared by queue and Lambda
// adapters. Retryable processing outcomes have already committed durable retry
// intent before returning ErrProcessingDeferred.
func DeliveryDisposition(err error) DeliveryAction {
	if err == nil || errors.Is(err, ErrProcessingDeferred) {
		return DeliveryAcknowledge
	}
	if errors.Is(err, ErrInvalidEvent) || errors.Is(err, ErrConflictingEvent) || errors.Is(err, ErrUnsupportedProcessingProfile) {
		return DeliveryReject
	}
	return DeliveryRequeue
}
