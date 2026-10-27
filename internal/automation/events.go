package automation

import (
	"context"
	"fmt"
	"github.com/google/uuid"
)

type EventEnvelope struct {
	Data  any         `json:"data"`
	Trace []uuid.UUID `json:"trace"`
}
type SourceEvent struct {
	ID          string
	Kind        string
	WorkspaceID uuid.UUID
	Source      string
	Data        any
	Trace       []uuid.UUID
}
type EventBus struct {
	Store   Store
	Backend Backend
}
type EventStore interface {
	EnqueueEvent(context.Context, SourceEvent) error
	PendingEvents(context.Context, int) ([]SourceEvent, error)
	CompleteEvent(context.Context, string) error
}

// Explicit source filters prevent another contact or document collection from
// gaining authority to start an automation. A lineage cannot trigger itself.
func (b *EventBus) Emit(ctx context.Context, event SourceEvent) error {
	if store, ok := b.Store.(EventStore); ok {
		list, err := b.Store.ListAutomations(ctx, event.WorkspaceID)
		if err != nil {
			return err
		}
		subscribed := false
		for _, a := range list {
			if a.Enabled && a.Trigger == event.Kind && a.Source == event.Source {
				subscribed = true
				break
			}
		}
		if !subscribed {
			return nil
		}
		if err := store.EnqueueEvent(ctx, event); err != nil {
			return err
		}
		// A durable outbox owns retry; an event outage cannot change a completed run.
		if err := b.dispatch(ctx, event); err == nil {
			return store.CompleteEvent(ctx, event.ID)
		}
		return nil
	}
	return b.dispatch(ctx, event)
}
func (b *EventBus) dispatch(ctx context.Context, event SourceEvent) error {
	if len(event.Trace) >= 16 {
		return nil
	}
	list, err := b.Store.ListAutomations(ctx, event.WorkspaceID)
	if err != nil {
		return err
	}
	for _, a := range list {
		if !a.Enabled || a.Trigger != event.Kind || a.Source != event.Source {
			continue
		}
		repeated := false
		for _, id := range event.Trace {
			if id == a.ID {
				repeated = true
				break
			}
		}
		if repeated {
			continue
		}
		if _, err := b.Store.AcceptTrigger(ctx, a.ID, event.ID); err != nil {
			return err
		}
		if err := b.Backend.Trigger(ctx, a, event.ID, EventEnvelope{Data: event.Data, Trace: event.Trace}); err != nil {
			return fmt.Errorf("event dispatch is temporarily unavailable")
		}
	}
	return nil
}
func (b *EventBus) Drain(ctx context.Context) error {
	store, ok := b.Store.(EventStore)
	if !ok {
		return nil
	}
	events, err := store.PendingEvents(ctx, 100)
	if err != nil {
		return err
	}
	for _, event := range events {
		if err := b.dispatch(ctx, event); err != nil {
			return err
		}
		if err := store.CompleteEvent(ctx, event.ID); err != nil {
			return err
		}
	}
	return nil
}
