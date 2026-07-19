package application

import (
	"errors"
	"testing"
	"time"
)

func TestDeliveryDisposition(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		action DeliveryAction
	}{
		{name: "success", action: DeliveryAcknowledge},
		{name: "durable retry", err: NewDeferredError(time.Now()), action: DeliveryAcknowledge},
		{name: "poison", err: ErrInvalidEvent, action: DeliveryReject},
		{name: "conflict", err: ErrConflictingEvent, action: DeliveryReject},
		{name: "unsupported profile", err: ErrUnsupportedProcessingProfile, action: DeliveryReject},
		{name: "persistence failure", err: errors.New("unavailable"), action: DeliveryRequeue},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := DeliveryDisposition(test.err); got != test.action {
				t.Fatalf("expected %d, got %d", test.action, got)
			}
		})
	}
}
