package automation

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DiscardReferenceAnswers prevents obsolete wiring questions from becoming
// supplied facts in a later planner request. Genuine user answers remain intact.
func DiscardReferenceAnswers(plan Plan, answers map[string]any) map[string]any {
	technical := map[string]bool{}
	for _, question := range plan.Questions {
		if internalReference.MatchString(question.Example) || internalReference.MatchString(question.Prompt) {
			technical[question.ID] = true
		}
	}
	out := make(map[string]any, len(answers))
	for key, value := range answers {
		if !technical[key] {
			out[key] = value
		}
	}
	return out
}

// normalizeReferences repairs legacy moustache wiring only when the output is
// declared and available in this coroutine's scope. Unknown tokens remain for
// technical diagnostics; they are never converted into requests for user facts.
func normalizeReferences(plan Plan, tools []ToolDescriptor) Plan {
	catalog := map[string]ToolDescriptor{}
	for _, tool := range tools {
		catalog[tool.ID] = tool
	}
	technical := map[string]Question{}
	keep := []Question{}
	for _, q := range plan.Questions {
		if internalReference.MatchString(q.Example) || internalReference.MatchString(q.Prompt) {
			technical[q.ID] = q
		} else {
			keep = append(keep, q)
		}
	}
	plan.Questions = keep
	used := map[string]bool{}
	var collect func([]Step)
	collect = func(steps []Step) {
		for _, s := range steps {
			used[s.ID] = true
			collect(s.Children)
			collect(s.Otherwise)
		}
	}
	collect(plan.Steps)
	newID := func(s Step, name string, index int) string {
		base := questionPrefix("fill_"+s.ID, name)
		for n := index; ; n++ {
			id := fmt.Sprintf("%s_%d", base, n)
			if !used[id] {
				used[id] = true
				return id
			}
		}
	}
	valid := func(ref string, available map[string]ToolDescriptor) bool {
		parts := strings.Split(strings.TrimSpace(ref), ".")
		if len(parts) < 2 {
			return false
		}
		t, ok := available[parts[0]]
		if !ok {
			return false
		}
		for _, f := range t.Outputs {
			if f.Name == parts[1] {
				return PortType(f, parts[2:]) != ""
			}
		}
		return false
	}
	var walk func([]Step, map[string]ToolDescriptor) []Step
	walk = func(steps []Step, available map[string]ToolDescriptor) []Step {
		out := []Step{}
		for _, s := range steps {
			for i, b := range s.Inputs {
				if q, ok := technical[b.Ref]; ok && b.Source == "answer" {
					b.Source = "literal"
					raw, _ := json.Marshal(q.Example)
					b.ValueJSON = string(raw)
					b.Ref = ""
				}
				if b.Source != "literal" && b.Source != "template" {
					continue
				}
				var text string
				if json.Unmarshal([]byte(b.ValueJSON), &text) != nil {
					continue
				}
				matches := internalReference.FindAllStringSubmatch(text, -1)
				if len(matches) == 0 {
					continue
				}
				// A legacy template may already have a question prefix. Rebuild it using
				// facts only after technical slots have been removed.
				b.Source = "literal"
				b.Ref = ""
				s.Inputs[i] = b
				allValid := true
				for _, m := range matches {
					if !valid(m[1], available) {
						allValid = false
						break
					}
				}
				if !allValid {
					continue
				}
				if len(matches) == 1 && strings.TrimSpace(text) == matches[0][0] {
					s.Inputs[i] = Binding{Name: b.Name, Source: "step", Ref: strings.TrimSpace(matches[0][1])}
					continue
				}
				if _, ok := catalog["value.bind"]; !ok {
					continue
				}
				prepared := internalReference.ReplaceAllStringFunc(text, func(token string) string {
					return "$(" + strings.TrimSpace(internalReference.FindStringSubmatch(token)[1]) + ")"
				})
				// Preserve existing fact IDs even when old technical questions
				// occurred before them in the same template.
				facts := []string{}
				matchedQuestions := map[string]bool{}
				prepared = factPlaceholder.ReplaceAllStringFunc(prepared, func(token string) string {
					id := ""
					for _, q := range plan.Questions {
						if q.StepID == s.ID && q.Input == b.Name && q.Example == token && !matchedQuestions[q.ID] {
							id = q.ID
							break
						}
					}
					if id == "" {
						id = fmt.Sprintf("%s_%d", questionPrefix(s.ID, b.Name), len(facts))
						kind := "text"
						if strings.Contains(strings.ToLower(token), "amount") {
							kind = "number"
						}
						plan.Questions = append(plan.Questions, Question{ID: id, Prompt: placeholderPrompt(s, catalog[s.Tool], b.Name, token), Kind: kind, Required: true, StepID: s.ID, Input: b.Name, Example: token, Options: []Option{}})
					}
					matchedQuestions[id] = true
					facts = append(facts, id)
					return "$(human_" + id + ")"
				})
				raw, _ := json.Marshal(prepared)
				current := Binding{Name: "template", Source: "literal", ValueJSON: string(raw)}
				seen := map[string]bool{}
				for index, m := range matches {
					ref := strings.TrimSpace(m[1])
					if seen[ref] {
						continue
					}
					seen[ref] = true
					id := newID(s, b.Name, index)
					name, _ := json.Marshal(ref)
					current.Name = "template"
					fill := Step{ID: id, Name: "Include the workflow result", Kind: "tool", Tool: "value.bind", Inputs: []Binding{current, {Name: "name", Source: "literal", ValueJSON: string(name)}, {Name: "value", Source: "step", Ref: ref}}, Children: []Step{}, Otherwise: []Step{}}
					out = append(out, fill)
					available[id] = catalog["value.bind"]
					current = Binding{Name: b.Name, Source: "step", Ref: id + ".result"}
				}
				for index, questionID := range facts {
					id := newID(s, b.Name, len(matches)+index)
					name, _ := json.Marshal("human_" + questionID)
					current.Name = "template"
					out = append(out, Step{ID: id, Name: "Include the supplied detail", Kind: "tool", Tool: "value.bind", Inputs: []Binding{current, {Name: "name", Source: "literal", ValueJSON: string(name)}, {Name: "value", Source: "answer", Ref: questionID}}, Children: []Step{}, Otherwise: []Step{}})
					available[id] = catalog["value.bind"]
					current = Binding{Name: b.Name, Source: "step", Ref: id + ".result"}
				}
				s.Inputs[i] = current
			}
			if s.Kind == "parallel" {
				for i, child := range s.Children {
					nested := walk([]Step{child}, copyCatalog(available))
					if len(nested) == 1 {
						s.Children[i] = nested[0]
					} else { // Keep prerequisites in their original parallel branch.
						s.Children[i] = Step{ID: newID(s, "branch", i), Name: child.Name, Kind: "branch", Condition: &Condition{Left: Binding{Source: "literal", ValueJSON: "true"}, Op: "equals", Right: Binding{Source: "literal", ValueJSON: "true"}}, Children: nested, Otherwise: []Step{}}
					}
				}
			} else {
				s.Children = walk(s.Children, copyCatalog(available))
			}
			s.Otherwise = walk(s.Otherwise, copyCatalog(available))
			out = append(out, s)
			if s.Kind == "tool" {
				available[s.ID] = catalog[s.Tool]
			} else {
				available[s.ID] = genericOutput()
			}
		}
		return out
	}
	plan.Steps = walk(plan.Steps, map[string]ToolDescriptor{})
	return plan
}

func fieldLabel(name string, t ToolDescriptor) string {
	for _, f := range t.Inputs {
		if f.Name == name && f.Label != "" {
			return f.Label
		}
	}
	return strings.ReplaceAll(strings.TrimPrefix(strings.TrimPrefix(name, "config."), "port."), "_", " ")
}
func stepName(s Step, t ToolDescriptor) string {
	if s.Name != "" {
		return s.Name
	}
	if t.Name != "" {
		return t.Name
	}
	return "this step"
}
func placeholderPrompt(s Step, t ToolDescriptor, input, token string) string {
	value := strings.Trim(token, "[]<> ")
	return fmt.Sprintf("What %s should I use in the %s for %s?", value, fieldLabel(input, t), stepName(s, t))
}
func mailContent(s Step, input string) bool {
	tool := strings.TrimPrefix(s.Tool, "node.")
	if tool != "gmail.send" && tool != "gmail.reply" && tool != "gmail.create_draft" {
		return false
	}
	name := strings.TrimPrefix(strings.TrimPrefix(input, "config."), "port.")
	return name == "body" || name == "subject" || name == "text" || name == "html"
}
func decorateQuestions(plan *Plan, catalog map[string]ToolDescriptor) {
	steps := map[string]Step{}
	orderedSteps := []Step{}
	var walk func([]Step)
	walk = func(list []Step) {
		for _, s := range list {
			steps[s.ID] = s
			orderedSteps = append(orderedSteps, s)
			walk(s.Children)
			walk(s.Otherwise)
		}
	}
	walk(plan.Steps)
	for i := range plan.Questions {
		q := &plan.Questions[i]
		if q.StepID == "" || q.Input == "" {
			for _, candidate := range orderedSteps {
				for _, b := range candidate.Inputs {
					if b.Source == "answer" && b.Ref == q.ID {
						if q.StepID == "" {
							q.StepID = candidate.ID
						}
						if q.Input == "" {
							q.Input = b.Name
						}
					}
				}
			}
		}
		s := steps[q.StepID]
		t := catalog[s.Tool]
		if s.ID == "" {
			continue
		}
		q.StepName = stepName(s, t)
		q.FieldLabel = fieldLabel(q.Input, t)
		if q.Context == "" {
			q.Context = "For " + q.StepName + ", " + q.FieldLabel + "."
			for _, b := range s.Inputs {
				if b.Name == q.Input && (b.Source == "literal" || b.Source == "template") {
					var text string
					if json.Unmarshal([]byte(b.ValueJSON), &text) == nil && text != "" {
						text = internalReference.ReplaceAllString(text, "the workflow result")
						runes := []rune(text)
						if len(runes) > 240 {
							text = string(runes[:240]) + "…"
						}
						q.Context += " Draft: “" + text + "”"
					}
				}
			}
		}
		// Legacy generated subject/body questions represented missing intent, not
		// a request for exact copy. They must be interpreted before sending.
		if q.Purpose == "" && mailContent(s, q.Input) && q.Example == "" {
			q.Purpose = "intent"
		}
		if q.Purpose == "intent" && (strings.HasPrefix(q.Prompt, "What should I use for ") || strings.TrimSpace(q.Prompt) == "") {
			q.Prompt = "Which results or details should the email include?"
			q.Context = "I’ll use your answer to prepare the email from the workflow’s results."
		}
	}
}

// checkIntentReplan keeps a clarification from becoming sendable copy, and
// preserves the user's existing decision rules and known destinations.
func checkIntentReplan(out *Compilation, req PlanRequest, tools []ToolDescriptor) {
	q := req.IntentQuestion
	if q == nil {
		return
	}
	fail := func(id, code, hint string) {
		out.Diagnostics = append(out.Diagnostics, Diagnostic{StepID: id, Code: code, Message: "I need to finish applying your clarification while keeping your existing instructions.", RepairHint: hint})
		out.Ready = false
	}
	var answerText string
	if value, ok := req.Answers[q.ID].(string); ok {
		answerText = strings.TrimSpace(value)
	}
	var walk func([]Step)
	walk = func(steps []Step) {
		for _, s := range steps {
			for _, b := range s.Inputs {
				if b.Source == "answer" && b.Ref == q.ID {
					fail(s.ID, "INTENT", "Use the intent answer to plan the content, never bind it directly as a value. Remove the resolved question.")
				}
				if b.Source == "literal" && mailContent(s, b.Name) && answerText != "" {
					var text string
					body := ""
					if json.Unmarshal([]byte(b.ValueJSON), &text) == nil {
						body = strings.ToLower(strings.Join(strings.Fields(text), " "))
					}
					guidance := strings.ToLower(strings.Join(strings.Fields(answerText), " "))
					if body == guidance || len(strings.Fields(guidance)) >= 3 && strings.Contains(body, guidance) {
						fail(s.ID, "INTENT", "Build outgoing content from the actual requested results; the intent answer is guidance, not the message body or subject.")
					}
				}
			}
			walk(s.Children)
			walk(s.Otherwise)
		}
	}
	walk(out.Plan.Steps)
	if req.Previous == nil {
		return
	}
	if req.Previous.Mode != out.Plan.Mode || req.Previous.Budget != out.Plan.Budget {
		fail("", "INTENT_BUDGET", "Preserve the existing execution mode and budgets when applying a content clarification.")
	}
	external := map[string]bool{}
	for _, tool := range tools {
		external[tool.ID] = tool.SideEffects != "" && tool.SideEffects != "none"
	}
	paths := func(plan Plan) map[string]string {
		result := map[string]string{}
		var visit func([]Step, string)
		visit = func(steps []Step, path string) {
			for _, s := range steps {
				if external[s.Tool] {
					result[s.ID] = path
				}
				visit(s.Children, path+"/"+s.ID+":children")
				visit(s.Otherwise, path+"/"+s.ID+":otherwise")
			}
		}
		visit(plan.Steps, "")
		return result
	}
	oldPaths, newPaths := paths(*req.Previous), paths(out.Plan)
	for id, path := range oldPaths {
		if next, exists := newPaths[id]; !exists || next != path {
			fail(id, "INTENT_CONDITION", "Keep each outgoing action inside its original branches, with the same threshold and data-availability guards.")
		}
	}
	before, after := map[string]Step{}, map[string]Step{}
	flatten := func(steps []Step, dest map[string]Step) {
		var visit func([]Step)
		visit = func(list []Step) {
			for _, s := range list {
				dest[s.ID] = s
				visit(s.Children)
				visit(s.Otherwise)
			}
		}
		visit(steps)
	}
	flatten(ClonePlan(*req.Previous).Steps, before)
	flatten(out.Plan.Steps, after)
	for id, old := range before {
		current, exists := after[id]
		if old.Condition != nil {
			a, _ := json.Marshal(old.Condition)
			b, _ := json.Marshal(current.Condition)
			if !exists || string(a) != string(b) {
				fail(id, "INTENT_CONDITION", "Preserve the existing branch/repeat condition, its threshold and operands exactly when applying the content clarification.")
			}
		}
		for _, binding := range old.Inputs {
			field := strings.TrimPrefix(strings.TrimPrefix(binding.Name, "config."), "port.")
			_, suppliedFact := req.Answers[binding.Ref]
			if field != "to" && field != "cc" && field != "bcc" && field != "recipient" && field != "credential_id" && !(binding.Source == "answer" && suppliedFact) {
				continue
			}
			if binding.Source == "answer" && binding.Ref == q.ID {
				continue
			}
			found := false
			for _, next := range current.Inputs {
				if next.Name == binding.Name {
					a, _ := json.Marshal(binding)
					b, _ := json.Marshal(next)
					found = string(a) == string(b)
					// A resolved fact may be frozen as a literal without changing its value.
					if !found && binding.Source == "answer" && next.Source == "literal" {
						value, ok := req.Answers[binding.Ref]
						if ok {
							a, _ = json.Marshal(value)
							var v any
							if json.Unmarshal([]byte(next.ValueJSON), &v) == nil {
								b, _ = json.Marshal(v)
								found = string(a) == string(b)
							}
						}
					}
				}
			}
			if !found {
				fail(id, "INTENT_DESTINATION", "Preserve supplied facts, recipients and the selected account when applying the content clarification.")
			}
		}
	}
}

// Gmail accepts either a connected body or its fallback setting. Both are
// optional in the integration catalog, but an email still needs meaningful
// content before it can become a runnable draft.
func ensureEmailContentQuestion(plan *Plan, step *Step, tool ToolDescriptor, questions map[string]bool) {
	if !mailContent(*step, "config.body") {
		return
	}
	bodyField := ""
	for _, field := range tool.Inputs {
		if field.Name == "config.body" || bodyField == "" && field.Name == "port.body" {
			bodyField = field.Name
		}
	}
	if bodyField == "" {
		return
	}
	for _, binding := range step.Inputs {
		if binding.Name != "port.body" && binding.Name != "config.body" {
			continue
		}
		if binding.Source != "literal" {
			return
		} // Bound results and genuine value questions already provide content.
		var value any
		if json.Unmarshal([]byte(binding.ValueJSON), &value) == nil {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				return
			}
		}
	}
	// A contextual question already supplied by the planner keeps its stable ID.
	for _, q := range plan.Questions {
		if q.StepID == step.ID && (q.Input == "config.body" || q.Input == "port.body") {
			for i, b := range step.Inputs {
				if b.Name == q.Input {
					step.Inputs[i] = Binding{Name: q.Input, Source: "answer", Ref: q.ID}
					return
				}
			}
			step.Inputs = append(step.Inputs, Binding{Name: q.Input, Source: "answer", Ref: q.ID})
			return
		}
	}
	id := questionPrefix(step.ID, bodyField)
	if questions[id] {
		return
	}
	context := "For " + stepName(*step, tool) + "."
	if strings.TrimSpace(plan.Summary) != "" {
		context += " " + plan.Summary
	}
	plan.Questions = append(plan.Questions, Question{ID: id, Prompt: "What should the recipient learn from this email?", Kind: "text", Purpose: "intent", Required: true, StepID: step.ID, Input: bodyField, Context: context, Options: []Option{}})
	questions[id] = true
	// Replace an empty fallback without introducing duplicate tool inputs.
	for i, b := range step.Inputs {
		if b.Name == bodyField {
			step.Inputs[i] = Binding{Name: bodyField, Source: "answer", Ref: id}
			return
		}
	}
	step.Inputs = append(step.Inputs, Binding{Name: bodyField, Source: "answer", Ref: id})
}
