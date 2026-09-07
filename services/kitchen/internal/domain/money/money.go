package money

import (
	"errors"
	"math"
)

var ErrInvalidAmount = errors.New("invalid money amount")

type Line struct {
	UnitPriceMinor int64
	Quantity       int
}

func Subtotal(lines []Line) (int64, error) {
	var total int64
	for _, line := range lines {
		if line.UnitPriceMinor < 0 || line.Quantity < 1 || line.UnitPriceMinor > math.MaxInt64/int64(line.Quantity) {
			return 0, ErrInvalidAmount
		}
		lineTotal := line.UnitPriceMinor * int64(line.Quantity)
		if total > math.MaxInt64-lineTotal {
			return 0, ErrInvalidAmount
		}
		total += lineTotal
	}
	return total, nil
}
