package runner

import (
	"context"
	"strconv"

	"github.com/jwstover/tend/internal/telemetry"
	"github.com/jwstover/tend/internal/usage"
	"github.com/jwstover/tend/internal/workflow"
)

// sink is the receiver's Sink: it stores one export's events against the
// step run named in the URL. An error is logged and returned, so the
// receiver answers 500 and the exporter may retry.
func (r *Runner) sink(runID int64) telemetry.Sink {
	return func(stepRunID int64, evs []telemetry.Event) error {
		out := make([]workflow.StepEvent, 0, len(evs))
		for _, ev := range evs {
			out = append(out, stepEvent(stepRunID, ev))
		}
		if err := r.Store.AddStepRunEvents(context.Background(), stepRunID, out); err != nil {
			r.logf("run %d: telemetry for step run %d dropped: %v", runID, stepRunID, err)
			return err
		}
		return nil
	}
}

// stepEvent maps a decoded OTLP event onto the domain type, promoting the
// attributes tend queries on and keeping every raw one. Numbers are parsed
// leniently: an attribute that is not one reads as 0.
func stepEvent(stepRunID int64, ev telemetry.Event) workflow.StepEvent {
	a := ev.Attributes
	first := func(keys ...string) string {
		for _, k := range keys {
			if v := a[k]; v != "" {
				return v
			}
		}
		return ""
	}
	return workflow.StepEvent{
		StepRunID: stepRunID, Name: ev.Name,
		SessionID: a["session.id"], Model: a["model"], QuerySource: a["query_source"],
		AgentName:     first("agent.name", "agent_name"),
		SkillName:     first("skill.name", "skill_name"),
		MCPServerName: first("mcp_server.name", "mcp_server_name"),
		Tokens: usage.Tokens{
			Input: attrInt(a["input_tokens"]), Output: attrInt(a["output_tokens"]),
			CacheRead: attrInt(a["cache_read_tokens"]), CacheCreation: attrInt(a["cache_creation_tokens"]),
		},
		CostUSD:    attrFloat(a["cost_usd"]),
		DurationMS: attrInt(a["duration_ms"]),
		StatusCode: attrInt(a["status_code"]),
		Attributes: a, OccurredAt: ev.Time,
	}
}

func attrFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func attrInt(s string) int64 {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	return int64(attrFloat(s))
}
