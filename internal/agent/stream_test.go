package agent

import (
	"strings"
	"testing"
)

// The fixture from headless_test.go plus a tool call and its result:
// the renderer shows the init line, the text and the tool, and skips the
// thinking, the tool result, the rate-limit event and the status event.
const renderFixture = streamFixture +
	`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./...\n","description":"Run the tests"}}]},"session_id":"s"}` + "\n" +
	`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]},"session_id":"s"}` + "\n" +
	`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_2","name":"Read","input":{"file_path":"/tmp/work/main.go"}}]},"session_id":"s"}` + "\n" +
	`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"All green.\nDone."}]},"session_id":"s"}` + "\n"

func TestRenderStream(t *testing.T) {
	got, err := RenderStream(strings.NewReader(renderFixture))
	if err != nil {
		t.Fatalf("RenderStream: %v", err)
	}
	want := []string{
		"session started · claude-haiku-4-5-20251001 · default",
		"pong",
		"finished: success · 1 turns · $0.02",
		ToolGlyph + " Bash: go test ./...",
		ToolGlyph + " Read: /tmp/work/main.go",
		"All green.",
		"Done.",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("RenderStream =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Garbage, blank lines, unknown event types and a truncated final line
// render as nothing rather than failing or leaking JSON into the view.
func TestRenderStreamSkipsNoise(t *testing.T) {
	stream := "\nnot json\n" +
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}` + "\n" +
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hmm"}]}}` + "\n" +
		`{"type":"something_new","payload":{"a":1}}` + "\n" +
		`{"type":"assistant","message":{"content":[{"type":"text","text":"kept"}]}}` + "\n" +
		`{"type":"assistant","message":{"content":[{"type":"te`
	got, err := RenderStream(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("RenderStream: %v", err)
	}
	if len(got) != 1 || got[0] != "kept" {
		t.Errorf("RenderStream = %q, want just the kept text", got)
	}
}

// An error result shows its message; denials are counted on the line.
func TestRenderStreamLineResultVariants(t *testing.T) {
	errLine := `{"type":"result","subtype":"error_max_turns","is_error":true,"result":"ran out of turns","num_turns":10}`
	got := RenderStreamLine([]byte(errLine))
	if len(got) != 2 || got[0] != "finished: error_max_turns · 10 turns" || got[1] != "ran out of turns" {
		t.Errorf("error result = %q", got)
	}
	denied := `{"type":"result","subtype":"success","is_error":false,"result":"x","permission_denials":[{"tool_name":"Write"}]}`
	got = RenderStreamLine([]byte(denied))
	if len(got) != 1 || got[0] != "finished: success · 1 tool call(s) denied" {
		t.Errorf("denied result = %q", got)
	}
}

// A tool with a long or multi-line input is summarized on one line; one
// with no recognizable field shows just its name.
func TestToolLine(t *testing.T) {
	long := strings.Repeat("x", 100)
	got := toolLine("Bash", []byte(`{"command":"echo a\necho b `+long+`"}`))
	if !strings.HasPrefix(got, ToolGlyph+" Bash: echo a echo b ") || !strings.HasSuffix(got, "…") || strings.Contains(got, "\n") {
		t.Errorf("toolLine = %q, want a one-line truncated summary", got)
	}
	if got := toolLine("TodoWrite", []byte(`{"todos":[{"content":"a"}]}`)); got != ToolGlyph+" TodoWrite" {
		t.Errorf("toolLine with no summary field = %q", got)
	}
	if got := toolLine("Odd", []byte(`not json`)); got != ToolGlyph+" Odd" {
		t.Errorf("toolLine with bad input = %q", got)
	}
}
