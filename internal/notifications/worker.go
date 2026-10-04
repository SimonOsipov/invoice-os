package notifications

// TEST-FIRST STUB (AUTH-17-02 Mode A): declarations only; the executor adds InsertOpts.

const QueueContacts = "contacts"

type DeliverArgs struct {
	Email       string `json:"email"`
	Destination string `json:"destination"`
	Version     int64  `json:"version"`
}

func (DeliverArgs) Kind() string { return "contact_deliver" }
