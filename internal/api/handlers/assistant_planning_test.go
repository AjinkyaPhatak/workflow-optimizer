package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"

	"workflow-optimizer/internal/api/httpx"
)

func TestAssistantPlanningTimeoutDeliversJSON(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := assistantPlanningContext(w, r, 20*time.Millisecond, time.Second)
		defer cancel()
		// Stand in for a planner HTTP request that respects cancellation. The
		// wrapped error also models provider normalization during unwinding.
		<-ctx.Done()
		err := assistantPlanningError(ctx, fmt.Errorf("planner failed: %w", ctx.Err()))
		httpx.WriteError(w, r, nil, err)
	}))
	// A plain server write timeout would close the socket before the planner
	// finishes. The handler must extend it past its cancellation budget.
	server.Config.WriteTimeout = 5 * time.Millisecond
	server.Start()
	defer server.Close()

	client := server.Client()
	client.Timeout = 2 * time.Second
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("timeout response could not be read: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", response.StatusCode)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("invalid timeout JSON: %v", err)
	}
	if body.Error.Code != "PLANNING_TIMEOUT" || body.Error.Message == "" {
		t.Fatalf("unexpected timeout body: %+v", body)
	}
}

func TestAssistantPlanningPreservesCallerCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(parent)
	ctx, cancel := beginAssistantPlanning(httptest.NewRecorder(), r)
	defer cancel()
	cancelParent()
	<-ctx.Done()
	if got := assistantPlanningError(ctx, ctx.Err()); !errors.Is(got, context.Canceled) {
		t.Fatalf("caller cancellation became a planning timeout: %v", got)
	}
}

func TestAssistantPlanningPreservesEarlierParentDeadline(t *testing.T) {
	parent, cancelParent := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelParent()
	r := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(parent)
	ctx, cancel := beginAssistantPlanning(httptest.NewRecorder(), r)
	defer cancel()
	if got := assistantPlanningError(ctx, ctx.Err()); !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("parent deadline became a planning timeout: %v", got)
	}
}

func TestAssistantPlanningPreservesServiceResult(t *testing.T) {
	ctx, cancel := beginAssistantPlanning(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))
	defer cancel()
	serviceError := errors.New("validation failed")
	if got := assistantPlanningError(ctx, serviceError); got != serviceError {
		t.Fatalf("service error changed: %v", got)
	}
	if got := assistantPlanningError(ctx, nil); got != nil {
		t.Fatalf("successful result changed: %v", got)
	}
}

func TestAssistantPlanningProxyOutlastsResponseDeadline(t *testing.T) {
	// Go and Next.js cannot import a shared duration constant; enforce the
	// cross-language ordering here to prevent another socket reset regression.
	config, err := os.ReadFile("../../../frontend/next.config.ts")
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`proxyTimeout:\s*(\d+)\s*\*\s*60\s*\*\s*1000`).FindSubmatch(config)
	if len(match) != 2 {
		t.Fatal("proxyTimeout must use an explicit minute budget; update this contract test if its format changes")
	}
	minutes, err := strconv.Atoi(string(match[1]))
	if err != nil {
		t.Fatal(err)
	}
	if proxy := time.Duration(minutes) * time.Minute; proxy <= PlanningTimeout+planningResponseAllowance {
		t.Fatalf("proxy timeout %s must exceed planning budget %s plus response allowance %s", proxy, PlanningTimeout, planningResponseAllowance)
	}
}
