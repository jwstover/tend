package usage

import (
	"strings"
	"testing"
)

// usageLog is a step log in the shape claude 2.1.x writes: the top-level
// agent calls Bash and gets a string result back, spawns a sub-agent
// (the Agent tool) whose own events carry parent_tool_use_id and whose
// Read result comes back as text blocks, then finishes. The session is
// then resumed once (a nudge), which appends a second stream with its
// own result event repeating the session totals.
const usageLog = `{"type":"system","subtype":"init","session_id":"s1","model":"claude-opus-5"}
{"type":"assistant","parent_tool_use_id":null,"message":{"role":"assistant","content":[{"type":"text","text":"Looking."},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}
{"type":"user","parent_tool_use_id":null,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"a.go\nb.go\n"}]}}
{"type":"assistant","parent_tool_use_id":null,"message":{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Agent","input":{"prompt":"look around"}}]}}
{"type":"assistant","parent_tool_use_id":"t2","message":{"role":"assistant","content":[{"type":"tool_use","id":"t3","name":"Read","input":{"file_path":"a.go"}}]}}
{"type":"user","parent_tool_use_id":"t2","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t3","content":[{"type":"text","text":"package main"},{"type":"tool_reference","tool_name":"x"},{"type":"text","text":"\n"}]}]}}
{"type":"assistant","parent_tool_use_id":"t2","message":{"role":"assistant","content":[{"type":"tool_use","id":"t4","name":"Bash","input":{"command":"go build"}}]}}
{"type":"user","parent_tool_use_id":"t2","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t4","content":""}]}}
{"type":"user","parent_tool_use_id":null,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":[{"type":"text","text":"sub-agent report"}]}]}}
{"type":"user","parent_tool_use_id":null,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"unknown","content":"orphan"}]}}
{"type":"assistant","parent_tool_use_id":null,"message":{"role":"assistant","content":[{"type":"tool_use","id":"t5","name":"Bash","input":{"command":"go test"}}]}}
{"type":"user","parent_tool_use_id":null,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t5","content":"ok"}]}}
{"type":"rate_limit_event","rate_limit_info":{}}
{"type":"result","subtype":"success","is_error":false,"num_turns":6,"duration_ms":1000,"duration_api_ms":800,"total_cost_usd":1.5,"usage":{"input_tokens":10,"output_tokens":100,"cache_creation_input_tokens":1000,"cache_read_input_tokens":10000},"modelUsage":{"claude-opus-5":{"inputTokens":10,"outputTokens":100,"cacheReadInputTokens":10000,"cacheCreationInputTokens":1000,"costUSD":1.5}},"permission_denials":[{"tool_name":"Edit"}],"subagent_stats":{"spawned":1,"by_type":{"general-purpose":1}},"result":"first pass"}
{"type":"system","subtype":"init","session_id":"s1","model":"claude-opus-5"}
{"type":"user","message":{"role":"user","content":"You have not called finish_step yet."}}
{"type":"assistant","parent_tool_use_id":null,"message":{"role":"assistant","content":[{"type":"tool_use","id":"t6","name":"mcp__tend__finish_step","input":{"outcome":"done"}}]}}
{"type":"user","parent_tool_use_id":null,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t6","content":"{\"finished\":true}"}]}}
{"type":"result","subtype":"success","is_error":false,"num_turns":2,"duration_ms":200,"duration_api_ms":150,"total_cost_usd":1.6,"usage":{"input_tokens":2,"output_tokens":20,"cache_creation_input_tokens":0,"cache_read_input_tokens":5000},"modelUsage":{"claude-opus-5":{"inputTokens":12,"outputTokens":120,"cacheReadInputTokens":15000,"cacheCreationInputTokens":1000,"costUSD":1.6}},"permission_denials":[],"subagent_stats":{"spawned":1,"by_type":{"general-purpose":1}},"result":"done"}
not json
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t7","name":"Bash"`

func TestParseStepLogSumsCountersAndTakesTotalsFromTheLastResult(t *testing.T) {
	u, err := ParseStepLog(strings.NewReader(usageLog))
	if err != nil {
		t.Fatalf("ParseStepLog: %v", err)
	}
	if u.Results != 2 {
		t.Errorf("Results = %d, want 2 (one per process that ran the step)", u.Results)
	}
	// Per-process counters are summed across the resume.
	if u.Turns != 8 || u.DurationMS != 1200 || u.APIDurationMS != 950 {
		t.Errorf("turns/duration/api = %d/%d/%d, want 8/1200/950", u.Turns, u.DurationMS, u.APIDurationMS)
	}
	if u.InputTokens != 12 || u.OutputTokens != 120 || u.CacheCreationTokens != 1000 || u.CacheReadTokens != 15000 {
		t.Errorf("tokens in/out/create/read = %d/%d/%d/%d, want 12/120/1000/15000",
			u.InputTokens, u.OutputTokens, u.CacheCreationTokens, u.CacheReadTokens)
	}
	if u.PermissionDenials != 1 {
		t.Errorf("PermissionDenials = %d, want 1", u.PermissionDenials)
	}
	// Session totals come from the last result, not the sum of both.
	if u.CostUSD != 1.6 {
		t.Errorf("CostUSD = %v, want 1.6 (the last result's total, not 3.1)", u.CostUSD)
	}
	if u.Subtype != "success" || u.IsError {
		t.Errorf("subtype/is_error = %q/%v", u.Subtype, u.IsError)
	}
	if u.SubagentsSpawned != 1 || u.SubagentsByType["general-purpose"] != 1 {
		t.Errorf("sub-agents = %d %v, want 1 general-purpose", u.SubagentsSpawned, u.SubagentsByType)
	}
	m, ok := u.Models["claude-opus-5"]
	if !ok || m.CostUSD != 1.6 || m.OutputTokens != 120 || m.CacheReadTokens != 15000 {
		t.Errorf("Models[claude-opus-5] = %+v (present %v), want the last result's figures", m, ok)
	}
}

func TestParseStepLogAttributesToolCallsAndResultBytesByScope(t *testing.T) {
	u, err := ParseStepLog(strings.NewReader(usageLog))
	if err != nil {
		t.Fatalf("ParseStepLog: %v", err)
	}
	// Top level: Bash twice (ls -> 10 bytes, go test -> 2), Agent once
	// (its result is the sub-agent's text report, 16 bytes), finish_step
	// once. Most-called first, then by name.
	want := []ToolUsage{
		{Name: "Bash", Calls: 2, ResultBytes: 12},
		{Name: "Agent", Calls: 1, ResultBytes: 16},
		{Name: "mcp__tend__finish_step", Calls: 1, ResultBytes: 17},
	}
	if len(u.ToolCalls) != len(want) {
		t.Fatalf("ToolCalls = %+v, want %+v", u.ToolCalls, want)
	}
	for i := range want {
		if u.ToolCalls[i] != want[i] {
			t.Errorf("ToolCalls[%d] = %+v, want %+v", i, u.ToolCalls[i], want[i])
		}
	}
	// Sub-agent: Bash once with an empty result, Read once whose text
	// blocks total 13 bytes; the tool_reference block counts for nothing.
	wantSub := []ToolUsage{
		{Name: "Bash", Calls: 1, ResultBytes: 0},
		{Name: "Read", Calls: 1, ResultBytes: 13},
	}
	if len(u.SubagentToolCalls) != len(wantSub) {
		t.Fatalf("SubagentToolCalls = %+v, want %+v", u.SubagentToolCalls, wantSub)
	}
	for i := range wantSub {
		if u.SubagentToolCalls[i] != wantSub[i] {
			t.Errorf("SubagentToolCalls[%d] = %+v, want %+v", i, u.SubagentToolCalls[i], wantSub[i])
		}
	}
}

// A log cut short -- no result event yet, a partial last line -- still
// tallies the tool calls that made it in, with zero totals.
func TestParseStepLogWithoutAResultEvent(t *testing.T) {
	cut := usageLog[:strings.Index(usageLog, `{"type":"result"`)]
	u, err := ParseStepLog(strings.NewReader(cut))
	if err != nil {
		t.Fatalf("ParseStepLog: %v", err)
	}
	if u.Results != 0 || u.CostUSD != 0 || u.Turns != 0 {
		t.Errorf("no result event, yet results/cost/turns = %d/%v/%d", u.Results, u.CostUSD, u.Turns)
	}
	if len(u.ToolCalls) != 2 || u.ToolCalls[0].Name != "Bash" || u.ToolCalls[0].Calls != 2 {
		t.Errorf("ToolCalls = %+v, want Bash x2 and Agent x1", u.ToolCalls)
	}
	if u.Models != nil || u.SubagentsByType != nil {
		t.Errorf("no result event, yet models/sub-agents are set: %v %v", u.Models, u.SubagentsByType)
	}
}

func TestParseStepLogOfAnEmptyLog(t *testing.T) {
	u, err := ParseStepLog(strings.NewReader(""))
	if err != nil {
		t.Fatalf("ParseStepLog: %v", err)
	}
	if u.Results != 0 || len(u.ToolCalls) != 0 || len(u.SubagentToolCalls) != 0 {
		t.Errorf("empty log tallied something: %+v", u)
	}
}
