package httpx

import (
	"net/http"
	"strconv"

	"workflow-optimizer/internal/application"
)

// Pagination limits.
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// ParsePage reads ?page= (default 1) and ?page_size= (default 20, at most
// 100).
func ParsePage(r *http.Request) (application.Page, error) {
	return ParsePageDefault(r, DefaultPageSize)
}

// ParsePageDefault is ParsePage with another default page size.
func ParsePageDefault(r *http.Request, defaultSize int) (application.Page, error) {
	p := application.Page{Number: 1, Size: defaultSize}
	q := r.URL.Query()
	if v := q.Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1_000_000 {
			return p, BadRequest("page must be a positive integer")
		}
		p.Number = n
	}
	if v := q.Get("page_size"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > MaxPageSize {
			return p, BadRequest("page_size must be between 1 and 100")
		}
		p.Size = n
	}
	return p, nil
}
