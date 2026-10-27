package handlers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"workflow-optimizer/internal/api/httpx"
)

// PlanningTimeout bounds the whole synchronous planner operation, including its
// model passes. Keep frontend/next.config.ts's proxyTimeout above this budget
// plus planningResponseAllowance so clients receive the JSON timeout response.
const PlanningTimeout = 6 * time.Minute

const planningResponseAllowance = 15 * time.Second

var errPlanningTimeout = errors.New("assistant planning budget exhausted")

func beginAssistantPlanning(w http.ResponseWriter, r *http.Request) (context.Context, context.CancelFunc) {
	return assistantPlanningContext(w, r, PlanningTimeout, planningResponseAllowance)
}

func assistantPlanningContext(w http.ResponseWriter, r *http.Request, budget, responseAllowance time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeoutCause(r.Context(), budget, errPlanningTimeout)
	deadline, _ := ctx.Deadline()
	// The planner must stop before the socket's write deadline, leaving enough
	// time for cancellation to unwind and the handler to send a useful error.
	_ = http.NewResponseController(w).SetWriteDeadline(deadline.Add(responseAllowance))
	return ctx, cancel
}

func assistantPlanningError(ctx context.Context, err error) error {
	if errors.Is(context.Cause(ctx), errPlanningTimeout) {
		return &httpx.Error{
			Status:  http.StatusGatewayTimeout,
			Code:    "PLANNING_TIMEOUT",
			Message: "Planning took too long. Your saved draft and previous answers are still available. Refresh the draft before trying again.",
		}
	}
	return err
}
