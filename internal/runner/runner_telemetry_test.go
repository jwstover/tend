package runner

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/telemetry"
	"github.com/jwstover/tend/internal/workflow"
)

const cannedLogs = `{"resourceLogs":[{"resource":{"attributes":[]},"scopeLogs":[{"logRecords":[
{"timeUnixNano":"1700000000000000000","attributes":[
 {"key":"event.name","value":{"stringValue":"api_request"}},
 {"key":"session.id","value":{"stringValue":"sess-1"}},
 {"key":"model","value":{"stringValue":"claude-x"}},
 {"key":"query_source","value":{"stringValue":"main"}},
 {"key":"input_tokens","value":{"intValue":"11"}},
 {"key":"output_tokens","value":{"intValue":"22"}},
 {"key":"cache_read_tokens","value":{"intValue":"33"}},
 {"key":"cache_creation_tokens","value":{"intValue":"44"}},
 {"key":"cost_usd","value":{"doubleValue":0.5}},
 {"key":"duration_ms","value":{"intValue":"900"}}]},
{"timeUnixNano":"1700000001000000000","attributes":[
 {"key":"event.name","value":{"stringValue":"api_error"}},
 {"key":"status_code","value":{"stringValue":"429"}}]}
]}]}]}`

func postJSON(url, body string) (int, error) {
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// oneStep sets up a single-step workflow and returns its run.
func oneStep(f *fixture) workflow.Run {
	f.step("implement", workflow.StepAgent)
	return f.run()
}

func TestRunStoresStepTelemetry(t *testing.T) {
	f := newFixture(t)
	run := oneStep(f)
	var badStatus, missingStatus int
	f.exec.handle = func(_ context.Context, req StepExec) (agent.HeadlessResult, error) {
		want := "/step-runs/" + itoa(req.StepRun.ID) + "/v1/logs"
		if !strings.Contains(req.Telemetry.Logs, want) {
			t.Errorf("Logs endpoint %q lacks %q", req.Telemetry.Logs, want)
		}
		// A mangled token is refused and stores nothing.
		bad := strings.Replace(req.Telemetry.Logs, "/step-runs/", "x/step-runs/", 1)
		badStatus, _ = postJSON(bad, cannedLogs)
		// A step run that does not exist fails the sink.
		missing := strings.Replace(req.Telemetry.Logs, "/step-runs/"+itoa(req.StepRun.ID)+"/", "/step-runs/99999/", 1)
		missingStatus, _ = postJSON(missing, cannedLogs)
		if code, err := postJSON(req.Telemetry.Logs, cannedLogs); err != nil || code != 200 {
			t.Errorf("POST logs = %d, %v", code, err)
		}
		return success("x"), nil
	}
	r := f.runner()
	r.Telemetry = true
	if err := r.Run(f.ctx, run.ID, false); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := f.getRun(run.ID).State; got != workflow.RunDone {
		t.Fatalf("run state = %s, want done", got)
	}
	if badStatus != 404 {
		t.Errorf("mangled token status = %d, want 404", badStatus)
	}
	if missingStatus != 500 {
		t.Errorf("unknown step run status = %d, want 500", missingStatus)
	}

	srs := f.stepRuns(run.ID)
	evs, err := f.s.ListStepRunEvents(f.ctx, srs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("events = %+v, want 2", evs)
	}
	a := evs[0]
	if a.Name != "api_request" || a.SessionID != "sess-1" || a.Model != "claude-x" || a.QuerySource != "main" ||
		a.Tokens.Input != 11 || a.Tokens.Output != 22 || a.Tokens.CacheRead != 33 || a.Tokens.CacheCreation != 44 ||
		a.CostUSD != 0.5 || a.DurationMS != 900 {
		t.Errorf("api_request = %+v", a)
	}
	if evs[1].Name != "api_error" || evs[1].StatusCode != 429 {
		t.Errorf("api_error = %+v", evs[1])
	}
	if !srs[0].Usage.IsZero() {
		t.Errorf("Usage = %+v: telemetry must not touch the result-event totals", srs[0].Usage)
	}
}

func TestRunTelemetryStopsWithTheRun(t *testing.T) {
	f := newFixture(t)
	run := oneStep(f)
	var endpoint string
	f.exec.handle = func(_ context.Context, req StepExec) (agent.HeadlessResult, error) {
		endpoint = req.Telemetry.Logs
		return success("x"), nil
	}
	r := f.runner()
	r.Telemetry = true
	if err := r.Run(f.ctx, run.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := postJSON(endpoint, cannedLogs); err == nil {
		t.Error("POST after Run returned succeeded, want connection refused")
	}
}

func TestRunTelemetryOffByDefault(t *testing.T) {
	f := newFixture(t)
	run := oneStep(f)
	if err := f.runner().Run(f.ctx, run.ID, false); err != nil {
		t.Fatal(err)
	}
	for _, req := range f.exec.requests() {
		if req.Telemetry != (agent.TelemetryEndpoints{}) {
			t.Errorf("Telemetry = %+v, want zero", req.Telemetry)
		}
	}
}

func TestStepEventMapping(t *testing.T) {
	ev := stepEvent(7, telemetry.Event{Name: "api_request", Attributes: map[string]string{
		"agent_name": "reviewer", "skill.name": "s", "mcp_server_name": "tend",
		"input_tokens": "12.0", "cost_usd": "x", "status_code": "429",
	}})
	if ev.StepRunID != 7 || ev.AgentName != "reviewer" || ev.SkillName != "s" || ev.MCPServerName != "tend" ||
		ev.Tokens.Input != 12 || ev.CostUSD != 0 || ev.StatusCode != 429 || ev.Attributes["agent_name"] != "reviewer" {
		t.Errorf("stepEvent = %+v", ev)
	}
}
