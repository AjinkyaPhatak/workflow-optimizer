// Package responses defines the JSON bodies the API returns. Every response
// is an explicit DTO mapped from domain values, so internal fields (claim
// tokens, encrypted credential data, legacy values) can never leak through
// serialization.
package responses

// ErrorBody is the single error shape of the API:
//
//	{"error": {"code": "WORKFLOW_NOT_FOUND", "message": "Workflow not found", "details": null}}
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail describes one API error.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details"`
}

// List is the envelope of unpaginated collections.
type List[T any] struct {
	Items []T `json:"items"`
}

// PageOf is the envelope of paginated collections.
type PageOf[T any] struct {
	Items    []T `json:"items"`
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
	Total    int `json:"total"`
}
