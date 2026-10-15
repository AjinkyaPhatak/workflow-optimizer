package execution

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/workflow"
)

// repeatRun runs a node once for each item of a list (Phase I4): the list
// arriving on the input the node repeats over (a port, or a variable input
// "var:<name>"). Each item runs with that input (or {{name}}) set to the
// item, under its own idempotency key and timeout. Items that completed are
// kept across retries of the node, so a retry continues with the item that
// failed and never repeats a finished one (an email is not sent twice). The
// node's output is one list per port, item by item.
type repeatRun struct {
	def     node.NodeDefinition
	port    string // the repeated port ("" for a variable input)
	varName string // the repeated variable input ("" for a port)
	items   []any
	// config resolves the node's settings with {{varName}} set to an item.
	config func(item any) (map[string]any, error)
	done   []node.NodeOutput
}

// RepeatItemError is the failure of one item of a repeating node.
type RepeatItemError struct {
	Index, Count int
	Err          error
}

func (e *RepeatItemError) Error() string {
	return fmt.Sprintf("item %d of %d: %v", e.Index+1, e.Count, e.Err)
}

func (e *RepeatItemError) Unwrap() error { return e.Err }

// repeatItems is the list a node repeats over: a list as is, nothing (an
// inactive or unconnected input) as no items, anything else as one item.
func repeatItems(v any, present bool) ([]any, error) {
	if !present || v == nil {
		return nil, nil
	}
	items, ok := v.([]any)
	if !ok {
		items = []any{v}
	}
	if len(items) > workflow.MaxRepeatItems {
		return nil, node.NewNodeError(node.ErrCodeInvalidInput, fmt.Sprintf("this step repeats for each item, at most %d; the list has %d", workflow.MaxRepeatItems, len(items)), false)
	}
	return items, nil
}

// newRepeatRun prepares a repeating node from its resolved inputs.
func newRepeatRun(over string, def node.NodeDefinition, ports map[string]node.Value, vars map[string]any, config func(map[string]any) (map[string]any, error)) (*repeatRun, error) {
	r := &repeatRun{def: def}
	if name, isVar := workflow.VariableInput(over); isVar {
		r.varName = name
		v, ok := vars[name]
		items, err := repeatItems(v, ok)
		if err != nil {
			return nil, err
		}
		r.items = items
		r.config = func(item any) (map[string]any, error) {
			itemVars := make(map[string]any, len(vars))
			for k, x := range vars {
				itemVars[k] = x
			}
			itemVars[name] = item
			return config(itemVars)
		}
		return r, nil
	}
	r.port = over
	v, ok := ports[over]
	items, err := repeatItems(v.Data, ok)
	if err != nil {
		return nil, err
	}
	r.items = items
	return r, nil
}

// run executes the remaining items; base is the node's input with the
// whole list.
func (r *repeatRun) run(ctx context.Context, impl node.Node, base node.NodeInput, timeout time.Duration) (node.NodeOutput, error) {
	for i := len(r.done); i < len(r.items); i++ {
		if err := ctx.Err(); err != nil {
			return node.NodeOutput{}, err
		}
		in := base
		if r.port != "" {
			ports := make(map[string]node.Value, len(base.Ports))
			for k, v := range base.Ports {
				ports[k] = v
			}
			ports[r.port] = node.NewJSONValue(deepCopy(r.items[i]))
			in.Ports = ports
		} else {
			cfg, err := r.config(r.items[i])
			if err != nil {
				return node.NodeOutput{}, &RepeatItemError{Index: i, Count: len(r.items), Err: err}
			}
			in.Config = cfg
		}
		if base.IdempotencyKey != "" {
			in.IdempotencyKey = base.IdempotencyKey + "#" + strconv.Itoa(i)
		}
		out, err := invokeNodeWithTimeout(ctx, impl, in, timeout)
		if err != nil {
			return node.NodeOutput{}, &RepeatItemError{Index: i, Count: len(r.items), Err: err}
		}
		r.done = append(r.done, out)
	}
	return r.combined(), nil
}

// combined is one list per output port (the declared ones, and any other an
// item emitted), item by item; nil where an item did not emit the port.
func (r *repeatRun) combined() node.NodeOutput {
	names := map[string]bool{}
	for _, p := range r.def.Outputs {
		names[p.Name] = true
	}
	for _, o := range r.done {
		for k := range o.Ports {
			names[k] = true
		}
	}
	sorted := make([]string, 0, len(names))
	for k := range names {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	out := node.NewNodeOutput(nil)
	for _, name := range sorted {
		list := make([]any, len(r.done))
		for i, o := range r.done {
			if v, ok := o.Ports[name]; ok {
				list[i] = v.Data
			}
		}
		out.SetPort(name, node.NewArrayValue(list))
	}
	return out
}
