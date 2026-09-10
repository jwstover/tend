package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// The stream-json log a headless step writes (see RunHeadless) is one
// event per line, most of it noise to a person watching the step: system
// bookkeeping, rate-limit events, the model's thinking, tool results, and
// finally a result event that repeats the last message. RenderStream
// turns it into the lines a run view shows -- the assistant's text and
// the tools it called -- and skips the rest. It is display only: nothing
// here decides a run's state, which lives in SQLite (tend task #183).

// RenderStream renders every event in r, in order, into display lines.
// Lines that are not JSON objects, or are events the renderer does not
// show, produce nothing; a partial final line (a step still writing, or
// killed mid-write) is simply skipped. Only a read error is an error.
func RenderStream(r io.Reader) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		out = append(out, RenderStreamLine(sc.Bytes())...)
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("reading stream-json: %w", err)
	}
	return out, nil
}

// RenderStreamLine renders one stream-json line into zero or more display
// lines:
//
//   - system/init: one line naming the model and permission mode.
//   - assistant text: the text, one line per line of it.
//   - assistant tool_use: "⚙ Tool: <summary>", the summary being the
//     most descriptive string in the tool's input (a command, a path, a
//     pattern), truncated.
//   - result: one closing line with the subtype, turn count and cost, and
//     the error text when claude reports one.
//
// Everything else -- thinking, tool results, rate-limit events, unknown
// types, lines that are not JSON -- renders as nothing.
func RenderStreamLine(line []byte) []string {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return nil
	}
	var ev renderEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return nil
	}
	switch ev.Type {
	case "system":
		if ev.Subtype != "init" {
			return nil
		}
		desc := "session started"
		if ev.Model != "" {
			desc += " · " + ev.Model
		}
		if ev.PermissionMode != "" {
			desc += " · " + ev.PermissionMode
		}
		return []string{desc}
	case "assistant":
		var out []string
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "text":
				text := strings.TrimRight(c.Text, "\n")
				if text == "" {
					continue
				}
				out = append(out, strings.Split(text, "\n")...)
			case "tool_use":
				out = append(out, toolLine(c.Name, c.Input))
			}
		}
		return out
	case "result":
		desc := "finished"
		if ev.Subtype != "" {
			desc += ": " + ev.Subtype
		}
		if ev.NumTurns > 0 {
			desc += fmt.Sprintf(" · %d turns", ev.NumTurns)
		}
		if ev.TotalCostUSD > 0 {
			desc += fmt.Sprintf(" · $%.2f", ev.TotalCostUSD)
		}
		if n := len(ev.PermissionDenials); n > 0 {
			desc += fmt.Sprintf(" · %d tool call(s) denied", n)
		}
		out := []string{desc}
		if ev.IsError && strings.TrimSpace(ev.Result) != "" {
			out = append(out, strings.Split(strings.TrimRight(ev.Result, "\n"), "\n")...)
		}
		return out
	}
	return nil
}

// renderEvent is the union of the stream-json fields the renderer reads.
// Like streamEvent it ignores everything else so the stream can grow.
type renderEvent struct {
	Type           string `json:"type"`
	Subtype        string `json:"subtype"`
	Model          string `json:"model"`
	PermissionMode string `json:"permissionMode"`
	Message        struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
	IsError           bool              `json:"is_error"`
	Result            string            `json:"result"`
	NumTurns          int               `json:"num_turns"`
	TotalCostUSD      float64           `json:"total_cost_usd"`
	PermissionDenials []json.RawMessage `json:"permission_denials"`
}

// ToolGlyph leads every tool_use line, so a viewer can tell a tool call
// from the assistant's prose at a glance.
const ToolGlyph = "⚙"

// toolSummaryWidth bounds the input summary on a tool line.
const toolSummaryWidth = 72

// toolSummaryKeys are the input fields worth showing, most descriptive
// first: what a Bash tool ran, what file an edit touched, what a search
// looked for, what a sub-agent was asked.
var toolSummaryKeys = []string{"command", "file_path", "path", "pattern", "query", "url", "description", "prompt", "skill"}

// toolLine renders a tool_use as "⚙ Name: summary".
func toolLine(name string, input json.RawMessage) string {
	line := ToolGlyph + " " + name
	if s := toolSummary(input); s != "" {
		line += ": " + s
	}
	return line
}

// toolSummary picks one string out of a tool's input to show beside its
// name, collapsed to a single line and truncated.
func toolSummary(input json.RawMessage) string {
	var fields map[string]any
	if err := json.Unmarshal(input, &fields); err != nil {
		return ""
	}
	for _, k := range toolSummaryKeys {
		if s, ok := fields[k].(string); ok && strings.TrimSpace(s) != "" {
			return truncateOneLine(s, toolSummaryWidth)
		}
	}
	return ""
}

// truncateOneLine collapses whitespace runs (including newlines) to one
// space and clips to w runes with an ellipsis.
func truncateOneLine(s string, w int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	return string(r[:w-1]) + "…"
}
