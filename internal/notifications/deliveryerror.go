package notifications

import "fmt"

// DeliveryError: Status 0 means no response (timeout, refused). Its text never carries an
// email or a vendor body, and it wraps nothing: net/http's *url.Error prints the request URL.
type DeliveryError struct {
	Status int
}

func (e *DeliveryError) Error() string {
	return fmt.Sprintf("notifications: delivery failed: status %d", e.Status)
}

// Permanent is true for a 4xx other than 408 and 429.
func (e *DeliveryError) Permanent() bool {
	return e.Status >= 400 && e.Status < 500 && e.Status != 408 && e.Status != 429
}
