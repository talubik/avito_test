package order

const (
	PendingRestaurant = "pending_restaurant"
	Accepted          = "accepted"
	Preparing         = "preparing"
	ReadyForDelivery  = "ready_for_delivery"
	Delivering        = "delivering"
	Delivered         = "delivered"
	Rejected          = "rejected"
	Cancelled         = "cancelled"
)

var previous = map[string]string{
	Accepted:         PendingRestaurant,
	Rejected:         PendingRestaurant,
	Preparing:        Accepted,
	ReadyForDelivery: Preparing,
	Delivering:       ReadyForDelivery,
	Delivered:        Delivering,
}

// PreviousStatus returns the only valid predecessor for a restaurant-driven
// transition. Customer cancellation is intentionally handled separately.
func PreviousStatus(target string) (string, bool) {
	status, ok := previous[target]
	return status, ok
}

func IsTerminal(status string) bool {
	return status == Delivered || status == Rejected || status == Cancelled
}
