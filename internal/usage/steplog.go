package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// The stream-json log a headless step writes (RunHeadless) is also the
// only record of what the step cost: every result event carries the
// session's token usage and price, and every tool call and its result
// pass through as assistant and user events. ParseStepLog reads that back
// after the fact -- "how much did this step cost, where did the tokens
// go" -- for the run inspection tools (tend task #327). Like RenderStream
// it decides nothing about a run's state; it only tallies.
//
// This is a second parser over the same file Parse (parse.go) reads, and
// deliberately not the same one: Parse does per-message token accounting
// for the account-wide rollups in usage.go and window.go -- one Entry per
// assistant message, deduplicated by message id so a resumed session's
// replayed history is not double-counted. ParseStepLog answers a
// different question, per step run rather than per account: cost, turns,
// duration, permission denials, sub-agent stats and tool-call tallies,
// none of which live on an assistant message -- they are on the result
// event (session totals, collapsed across a resumed step's repeated
// result records rather than summed) and on the tool_use/tool_result
// pairs a message-level reader has no reason to correlate.

// StreamUsage is the tally of one step run's stream-json log.
//
// A resumed session -- a crash resume, a finish_step nudge, a retry --
// appends a whole new stream to the same log, so the log holds one
// result event per claude process that ran the step. The counters
// (Turns, the durations, the token counts) are per process and are
// summed; CostUSD, Models and the sub-agent stats are session totals
// that every result event repeats, so they are read from the last one
// rather than summed, which would over-count by the number of resumes.
type StreamUsage struct {
	// Results is how many result events the log holds: one for a step
	// that ran straight through, one more per resume. A proxy for how
	// often the runner had to come back to the step.
	Results int
	// Turns, DurationMS and APIDurationMS are summed across results.
	Turns         int
	DurationMS    int64
	APIDurationMS int64
	// CostUSD is the session's total price, from the last result event.
	CostUSD float64
	// Token counts are summed across results.
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
	// PermissionDenials is summed: each process reports its own.
	PermissionDenials int
	// Subtype and IsError are the last result event's, the step's final
	// word.
	Subtype string
	IsError bool
	// SubagentsSpawned and SubagentsByType are session totals, from the
	// last result event.
	SubagentsSpawned int
	SubagentsByType  map[string]int
	// Models is the per-model breakdown the last result event carries.
	Models map[string]ModelUsage
	// ToolCalls tallies the top-level agent's tool calls by tool name,
	// most-called first; SubagentToolCalls does the same for every
	// sub-agent the step spawned, together.
	ToolCalls         []ToolUsage
	SubagentToolCalls []ToolUsage
}

// ModelUsage is one model's share of a session, as claude reports it.
type ModelUsage struct {
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	CostUSD             float64
}

// ToolUsage is how often one tool was called and how much text its
// results put back into the context -- the bytes of the tool_result
// content, string or text blocks, which is what the model then had to
// read. Tool-reference and other non-text blocks count for nothing.
type ToolUsage struct {
	Name        string
	Calls       int
	ResultBytes int64
}

// ParseStepLog tallies a stream-json log -- live from a pipe or after the
// fact from the file RunHeadless wrote -- into a StreamUsage. Lines that
// are not JSON objects, and events the tally does not read, are skipped
// rather than failing the parse, as ParseStream does; only a read error
// is an error. A log with no result event yields zero totals with the
// tool calls that did make it in.
func ParseStepLog(r io.Reader) (StreamUsage, error) {
	t := newUsageTally()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		t.line(sc.Bytes())
	}
	if err := sc.Err(); err != nil {
		return t.finish(), fmt.Errorf("reading stream-json: %w", err)
	}
	return t.finish(), nil
}

// usageEvent is the union of the stream-json fields the tally reads.
// Like streamEvent it ignores everything else so the stream can grow.
type usageEvent struct {
	Type            string  `json:"type"`
	Subtype         string  `json:"subtype"`
	ParentToolUseID *string `json:"parent_tool_use_id"`
	Message         struct {
		// Content is an array of blocks for an assistant event and for a
		// tool-result user event, a plain string for a user prompt.
		Content json.RawMessage `json:"content"`
	} `json:"message"`

	IsError           bool              `json:"is_error"`
	NumTurns          int               `json:"num_turns"`
	DurationMS        int64             `json:"duration_ms"`
	DurationAPIMS     int64             `json:"duration_api_ms"`
	TotalCostUSD      float64           `json:"total_cost_usd"`
	PermissionDenials []json.RawMessage `json:"permission_denials"`
	Usage             struct {
		InputTokens         int64 `json:"input_tokens"`
		OutputTokens        int64 `json:"output_tokens"`
		CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadTokens     int64 `json:"cache_read_input_tokens"`
	} `json:"usage"`
	ModelUsage map[string]struct {
		InputTokens         int64   `json:"inputTokens"`
		OutputTokens        int64   `json:"outputTokens"`
		CacheReadTokens     int64   `json:"cacheReadInputTokens"`
		CacheCreationTokens int64   `json:"cacheCreationInputTokens"`
		CostUSD             float64 `json:"costUSD"`
	} `json:"modelUsage"`
	SubagentStats struct {
		Spawned int            `json:"spawned"`
		ByType  map[string]int `json:"by_type"`
	} `json:"subagent_stats"`
}

// contentBlock is one element of a message's content array: a tool_use
// (Name, ID) on an assistant event, a tool_result (ToolUseID, Content)
// on a user event, or text.
type contentBlock struct {
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	Text      string          `json:"text"`
}

// usageTally accumulates ParseStepLog's result line by line. Tool results
// are attributed through the tool_use id they answer, so a result is
// counted against the tool that was called and in the scope (top-level
// or sub-agent) the call was made in.
type usageTally struct {
	u     StreamUsage
	tools map[string]*ToolUsage // top-level, by tool name
	sub   map[string]*ToolUsage // sub-agents', by tool name
	// calls maps a tool_use id to the tally entry its result lands on.
	calls map[string]*ToolUsage
}

func newUsageTally() *usageTally {
	return &usageTally{
		tools: map[string]*ToolUsage{},
		sub:   map[string]*ToolUsage{},
		calls: map[string]*ToolUsage{},
	}
}

func (t *usageTally) line(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return
	}
	var ev usageEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return
	}
	switch ev.Type {
	case "assistant":
		t.assistant(ev)
	case "user":
		t.user(ev)
	case "result":
		t.result(ev)
	}
}

// assistant records every tool_use block against its scope.
func (t *usageTally) assistant(ev usageEvent) {
	scope := t.tools
	if ev.ParentToolUseID != nil {
		scope = t.sub
	}
	for _, b := range blocks(ev.Message.Content) {
		if b.Type != "tool_use" || b.Name == "" {
			continue
		}
		entry, ok := scope[b.Name]
		if !ok {
			entry = &ToolUsage{Name: b.Name}
			scope[b.Name] = entry
		}
		entry.Calls++
		if b.ID != "" {
			t.calls[b.ID] = entry
		}
	}
}

// user adds each tool_result's text volume to the call it answers. A
// result for a call the log never showed (a log cut short) is dropped.
func (t *usageTally) user(ev usageEvent) {
	for _, b := range blocks(ev.Message.Content) {
		if b.Type != "tool_result" {
			continue
		}
		if entry, ok := t.calls[b.ToolUseID]; ok {
			entry.ResultBytes += resultBytes(b.Content)
		}
	}
}

// result folds one result event in: counters summed, totals replaced.
func (t *usageTally) result(ev usageEvent) {
	u := &t.u
	u.Results++
	u.Turns += ev.NumTurns
	u.DurationMS += ev.DurationMS
	u.APIDurationMS += ev.DurationAPIMS
	u.InputTokens += ev.Usage.InputTokens
	u.OutputTokens += ev.Usage.OutputTokens
	u.CacheCreationTokens += ev.Usage.CacheCreationTokens
	u.CacheReadTokens += ev.Usage.CacheReadTokens
	u.PermissionDenials += len(ev.PermissionDenials)

	u.CostUSD = ev.TotalCostUSD
	u.Subtype = ev.Subtype
	u.IsError = ev.IsError
	u.SubagentsSpawned = ev.SubagentStats.Spawned
	u.SubagentsByType = ev.SubagentStats.ByType
	u.Models = make(map[string]ModelUsage, len(ev.ModelUsage))
	for name, m := range ev.ModelUsage {
		u.Models[name] = ModelUsage{
			InputTokens: m.InputTokens, OutputTokens: m.OutputTokens,
			CacheReadTokens: m.CacheReadTokens, CacheCreationTokens: m.CacheCreationTokens,
			CostUSD: m.CostUSD,
		}
	}
}

func (t *usageTally) finish() StreamUsage {
	t.u.ToolCalls = sortedTools(t.tools)
	t.u.SubagentToolCalls = sortedTools(t.sub)
	return t.u
}

// sortedTools flattens a scope's tally, most-called first, ties by name
// so the order is stable.
func sortedTools(m map[string]*ToolUsage) []ToolUsage {
	out := make([]ToolUsage, 0, len(m))
	for _, e := range m {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// blocks decodes a message's content when it is an array of blocks; a
// string content (a plain prompt) or anything else yields none.
func blocks(raw json.RawMessage) []contentBlock {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var out []contentBlock
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// resultBytes measures the text a tool_result put into the context: the
// whole string when the content is one, else the text blocks of the
// array. Bytes rather than tokens, since the log has no token count per
// result; it is a relative measure of where the context went.
func resultBytes(raw json.RawMessage) int64 {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return 0
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0
		}
		return int64(len(s))
	}
	var n int64
	for _, b := range blocks(raw) {
		if b.Type == "text" {
			n += int64(len(b.Text))
		}
	}
	return n
}
