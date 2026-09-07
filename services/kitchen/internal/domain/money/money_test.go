package money

import (
	"errors"
	"math"
	"testing"
)

func TestSubtotal(t *testing.T) {
	t.Parallel()
	actual, err := Subtotal([]Line{{UnitPriceMinor: 59000, Quantity: 2}, {UnitPriceMinor: 19000, Quantity: 1}})
	if err != nil || actual != 137000 {
		t.Fatalf("Subtotal() = %d, %v; want 137000, nil", actual, err)
	}
}

func TestSubtotalRejectsInvalidAndOverflow(t *testing.T) {
	t.Parallel()
	for _, lines := range [][]Line{
		{{UnitPriceMinor: -1, Quantity: 1}},
		{{UnitPriceMinor: 1, Quantity: 0}},
		{{UnitPriceMinor: math.MaxInt64, Quantity: 2}},
		{{UnitPriceMinor: math.MaxInt64, Quantity: 1}, {UnitPriceMinor: 1, Quantity: 1}},
	} {
		if _, err := Subtotal(lines); !errors.Is(err, ErrInvalidAmount) {
			t.Fatalf("Subtotal(%v) error = %v; want ErrInvalidAmount", lines, err)
		}
	}
}
