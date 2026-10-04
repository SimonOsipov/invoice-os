package notifications

// STUB (AUTH-17-03 red): compile-only; the executor replaces it.

import "fmt"

// DeliveryError: Status 0 means no response (timeout, refused). Its text never carries an email.
type DeliveryError struct {
	Status int
}

func (e *DeliveryError) Error() string {
	return fmt.Sprintf("notifications: delivery failed: status %d", e.Status)
}

// Permanent is true for a 4xx other than 408 and 429.
func (e *DeliveryError) Permanent() bool { return false }
