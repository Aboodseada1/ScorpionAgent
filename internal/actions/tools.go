// Package actions defines the LLM tool schemas the SDR AI can call and a local
// executor that persists them. External integrations (Calendar/Email/CRM) can
// plug into the Executor interface later.
package actions

import (
	"context"
	"encoding/json"
	"errors"

	"scorpion/agent/internal/llm"
	"scorpion/agent/internal/memory"
)

// Tools returns the JSON-schema tool specs advertised to the LLM.
func Tools() []llm.ToolSpec {
	mk := func(name, desc, schema string) llm.ToolSpec {
		t := llm.ToolSpec{Type: "function"}
		t.Function.Name = name
		t.Function.Description = desc
		t.Function.Parameters = json.RawMessage(schema)
		return t
	}
	return []llm.ToolSpec{
		mk("schedule_followup",
			"Book a follow-up call with the prospect at a specific ISO-8601 date/time.",
			`{"type":"object","properties":{"when":{"type":"string","description":"ISO-8601 datetime"},"topic":{"type":"string"},"duration_min":{"type":"integer"}},"required":["when","topic"]}`),
		mk("reschedule",
			"Move an existing appointment from one date/time to another.",
			`{"type":"object","properties":{"from":{"type":"string"},"to":{"type":"string"},"reason":{"type":"string"}},"required":["to"]}`),
		mk("log_qualification",
			"Record BANT-style qualification for this prospect.",
			`{"type":"object","properties":{"budget":{"type":"string"},"authority":{"type":"string"},"need":{"type":"string"},"timeline":{"type":"string"},"score":{"type":"integer","minimum":0,"maximum":10}}}`),
		mk("draft_email",
			"Draft a follow-up email to the prospect.",
			`{"type":"object","properties":{"subject":{"type":"string"},"body":{"type":"string"}},"required":["subject","body"]}`),
		mk("add_note",
			"Add a free-text note to the prospect record.",
			`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		mk("flag_objection",
			"Capture that the prospect raised an objection.",
			`{"type":"object","properties":{"type":{"type":"string","enum":["price","timing","authority","fit","trust","other"]},"detail":{"type":"string"}},"required":["type","detail"]}`),
	}
}

type Executor interface {
	Execute(ctx context.Context, call llm.ToolCall, clientID, convID string) (*memory.Action, string, error)
}

// LocalExecutor persists actions to SQLite and returns a short confirmation
// message that becomes the `tool` message fed back to the LLM.
type LocalExecutor struct {
	Mem *memory.DB
}

func (l *LocalExecutor) Execute(ctx context.Context, call llm.ToolCall, clientID, convID string) (*memory.Action, string, error) {
	name := call.Function.Name
	if name == "" {
		return nil, "", errors.New("empty tool name")
	}
	var payload json.RawMessage
	if call.Function.Arguments != "" {
		payload = json.RawMessage(call.Function.Arguments)
	} else {
		payload = json.RawMessage("{}")
	}
	a := &memory.Action{
		ClientID: clientID,
		ConvID:   convID,
		Type:     name,
		Payload:  payload,
		Status:   "logged",
	}
	if err := l.Mem.InsertAction(a); err != nil {
		return nil, "", err
	}
	msg := confirmation(name)
	return a, msg, nil
}

func confirmation(name string) string {
	switch name {
	case "schedule_followup":
		return `{"ok":true,"note":"Follow-up logged. Tell the prospect you'll send a calendar invite."}`
	case "reschedule":
		return `{"ok":true,"note":"Reschedule logged. Confirm the new time back to them."}`
	case "log_qualification":
		return `{"ok":true,"note":"Qualification recorded."}`
	case "draft_email":
		return `{"ok":true,"note":"Email drafted."}`
	case "add_note":
		return `{"ok":true,"note":"Note saved."}`
	case "flag_objection":
		return `{"ok":true,"note":"Objection flagged. Address it briefly, then pivot."}`
	default:
		return `{"ok":true}`
	}
}
