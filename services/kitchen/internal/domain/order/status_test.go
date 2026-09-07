package order

import "testing"

func TestPreviousStatus(t *testing.T) {
	t.Parallel()
	tests := []struct{ target, expected string }{
		{Accepted, PendingRestaurant},
		{Rejected, PendingRestaurant},
		{Preparing, Accepted},
		{ReadyForDelivery, Preparing},
		{Delivering, ReadyForDelivery},
		{Delivered, Delivering},
	}
	for _, test := range tests {
		actual, ok := PreviousStatus(test.target)
		if !ok || actual != test.expected {
			t.Fatalf("PreviousStatus(%q) = %q, %v; want %q, true", test.target, actual, ok, test.expected)
		}
	}
}

func TestInvalidAndTerminalStatuses(t *testing.T) {
	t.Parallel()
	if _, ok := PreviousStatus(Cancelled); ok {
		t.Fatal("partner must not be able to cancel an order")
	}
	for _, status := range []string{Delivered, Rejected, Cancelled} {
		if !IsTerminal(status) {
			t.Fatalf("%q must be terminal", status)
		}
	}
	if IsTerminal(Preparing) {
		t.Fatal("preparing must not be terminal")
	}
}
