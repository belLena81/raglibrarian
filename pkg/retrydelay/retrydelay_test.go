package retrydelay

import (
	"testing"
	"time"
)

func TestCappedExponential(t *testing.T) {
	tests := []struct {
		name    string
		base    time.Duration
		maximum time.Duration
		attempt int
		want    time.Duration
	}{
		{name: "first retry", base: time.Second, maximum: 5 * time.Minute, attempt: 0, want: time.Second},
		{name: "third retry", base: time.Second, maximum: 5 * time.Minute, attempt: 2, want: 4 * time.Second},
		{name: "caps at max", base: 2 * time.Second, maximum: 5 * time.Minute, attempt: 20, want: 5 * time.Minute},
		{name: "negative attempt clamps to zero", base: 2 * time.Second, maximum: time.Minute, attempt: -1, want: 2 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CappedExponential(test.base, test.maximum, test.attempt); got != test.want {
				t.Fatalf("CappedExponential() = %s, want %s", got, test.want)
			}
		})
	}
}
