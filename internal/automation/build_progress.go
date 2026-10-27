package automation

import "time"

// BuildProgress is a transient preview, never an executable or saved revision.
type BuildProgress struct {
	Status    string    `json:"status"`
	Phase     string    `json:"phase"`
	Pass      int       `json:"pass"`
	Revision  int       `json:"revision"`
	RequestID string    `json:"request_id"`
	Plan      *Plan     `json:"plan,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}
