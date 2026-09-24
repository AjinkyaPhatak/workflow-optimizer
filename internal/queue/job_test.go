package queue_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"workflow-optimizer/internal/queue"
)

func TestJobRoundTripIsDeterministicAndSmall(t *testing.T) {
	id := uuid.MustParse("6f1c2c1e-6d7a-4c1b-9a51-2f5f3c1d8e90")
	a, err := queue.EncodeJob(queue.Job{ExecutionID: id})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"v":1,"execution_id":"6f1c2c1e-6d7a-4c1b-9a51-2f5f3c1d8e90"}`
	if string(a) != want {
		t.Fatalf("encoded = %s, want %s", a, want)
	}
	for i := 0; i < 10; i++ {
		b, _ := queue.EncodeJob(queue.Job{ExecutionID: id})
		if string(b) != string(a) {
			t.Fatal("encoding is not deterministic")
		}
	}
	got, err := queue.DecodeJob(a)
	if err != nil || got.ExecutionID != id {
		t.Fatalf("decoded = %+v, %v", got, err)
	}
}

func TestEncodeRejectsInvalidJob(t *testing.T) {
	if _, err := queue.EncodeJob(queue.Job{}); !errors.Is(err, queue.ErrInvalidJob) {
		t.Fatalf("err = %v", err)
	}
}

func TestDecodeRejectsMalformedPayloads(t *testing.T) {
	id := uuid.NewString()
	cases := map[string]string{
		"empty":            ``,
		"not json":         `hello`,
		"array":            `[1,2]`,
		"missing id":       `{"v":1}`,
		"nil uuid":         `{"v":1,"execution_id":"00000000-0000-0000-0000-000000000000"}`,
		"bad uuid":         `{"v":1,"execution_id":"123"}`,
		"non-canonical":    `{"v":1,"execution_id":"{` + id + `}"}`,
		"unknown version":  `{"v":2,"execution_id":"` + id + `"}`,
		"missing version":  `{"execution_id":"` + id + `"}`,
		"unknown field":    `{"v":1,"execution_id":"` + id + `","definition":{"nodes":[]}}`,
		"trailing data":    `{"v":1,"execution_id":"` + id + `"}{"v":1}`,
		"wrong type":       `{"v":"1","execution_id":"` + id + `"}`,
		"oversized":        `{"v":1,"execution_id":"` + id + `","x":"` + strings.Repeat("a", 2000) + `"}`,
		"null":             `null`,
		"truncated object": `{"v":1,"execution_id":"` + id,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := queue.DecodeJob([]byte(payload))
			var mal *queue.MalformedJobError
			if !errors.Is(err, queue.ErrMalformedJob) || !errors.As(err, &mal) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// The payload carries only identity: a decoded job cannot smuggle workflow
// data, and the encoding has exactly two fields.
func TestJobPayloadCarriesOnlyIdentity(t *testing.T) {
	b, _ := queue.EncodeJob(queue.Job{ExecutionID: uuid.New()})
	for _, forbidden := range []string{"definition", "nodes", "edges", "input", "credential", "status"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("payload %s contains %q", b, forbidden)
		}
	}
}
