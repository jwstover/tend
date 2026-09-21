package telemetry

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

const logsBody = `{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"claude-code"}},{"key":"model","value":{"stringValue":"resource-model"}}]},
"scopeLogs":[{"logRecords":[
{"timeUnixNano":"1700000000000000000","attributes":[{"key":"event.name","value":{"stringValue":"api_request"}},{"key":"input_tokens","value":{"intValue":"12"}},{"key":"output_tokens","value":{"intValue":7}},{"key":"cost_usd","value":{"doubleValue":0.25}},{"key":"ok","value":{"boolValue":true}},{"key":"model","value":{"stringValue":"m1"}}]},
{"observedTimeUnixNano":"1700000001000000000","body":{"stringValue":"claude_code.tool_result"}},
{"attributes":[]}
]}]}]}`

func TestDecodeLogs(t *testing.T) {
	evs, err := DecodeLogs([]byte(logsBody))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("got %d events, want 2", len(evs))
	}
	a := evs[0]
	if a.Name != "api_request" || a.Time.UnixNano() != 1700000000000000000 {
		t.Errorf("event 0 = %+v", a)
	}
	for k, want := range map[string]string{"input_tokens": "12", "output_tokens": "7", "cost_usd": "0.25", "ok": "true", "model": "m1", "service.name": "claude-code"} {
		if a.Attributes[k] != want {
			t.Errorf("attr %s = %q, want %q", k, a.Attributes[k], want)
		}
	}
	if evs[1].Name != "tool_result" || evs[1].Time.UnixNano() != 1700000001000000000 {
		t.Errorf("event 1 = %+v", evs[1])
	}
	if _, err := DecodeLogs([]byte("{nope")); err == nil {
		t.Error("malformed body: want error")
	}
}

func TestDecodeMetrics(t *testing.T) {
	body := `{"resourceMetrics":[{"resource":{"attributes":[{"key":"host","value":{"stringValue":"h"}}]},"scopeMetrics":[{"metrics":[
{"name":"claude_code.session.count","sum":{"dataPoints":[{"attributes":[{"key":"start_type","value":{"stringValue":"resume"}}],"timeUnixNano":"1700000000000000000","asInt":"1"}]}},
{"name":"claude_code.token.usage","sum":{"dataPoints":[{"asInt":"5"}]}}]}]}]}`
	evs, err := DecodeMetrics([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Name != "session.count" || evs[0].Attributes["start_type"] != "resume" ||
		evs[0].Attributes["value"] != "1" || evs[0].Attributes["host"] != "h" {
		t.Fatalf("events = %+v", evs)
	}
}

type got struct {
	id  int64
	evs []Event
}

func newRcv(t *testing.T) (*Receiver, *[]got, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var gs []got
	r, err := Listen(func(id int64, evs []Event) error {
		mu.Lock()
		defer mu.Unlock()
		gs = append(gs, got{id, evs})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	return r, &gs, &mu
}

func post(t *testing.T, url, ctype, enc string, body []byte) int {
	t.Helper()
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", ctype)
	if enc != "" {
		req.Header.Set("Content-Encoding", enc)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestReceiver(t *testing.T) {
	r, gs, mu := newRcv(t)
	url := r.LogsEndpoint(42)
	if c := post(t, url, "application/json", "", []byte(logsBody)); c != 200 {
		t.Fatalf("status %d", c)
	}
	var zb bytes.Buffer
	zw := gzip.NewWriter(&zb)
	_, _ = zw.Write([]byte(logsBody))
	_ = zw.Close()
	if c := post(t, url, "application/json", "gzip", zb.Bytes()); c != 200 {
		t.Errorf("gzip status %d", c)
	}
	mu.Lock()
	if len(*gs) != 2 || (*gs)[0].id != 42 || len((*gs)[0].evs) != 2 {
		t.Errorf("sink got %+v", *gs)
	}
	mu.Unlock()

	bad := strings.Replace(url, r.token, "wrong", 1)
	for name, tc := range map[string]struct {
		url, ctype string
		body       string
		want       int
	}{
		"bad token": {bad, "application/json", logsBody, 404},
		"bad json":  {url, "application/json", "{", 400},
		"protobuf":  {url, "application/x-protobuf", logsBody, 415},
		"bad id":    {strings.Replace(url, "/42/", "/x/", 1), "application/json", logsBody, 400},
	} {
		if c := post(t, tc.url, tc.ctype, "", []byte(tc.body)); c != tc.want {
			t.Errorf("%s: status %d, want %d", name, c, tc.want)
		}
	}

	sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.Close(sctx); err != nil {
		t.Fatal(err)
	}
	if _, err := http.Post(url, "application/json", strings.NewReader("{}")); err == nil {
		t.Error("post after Close succeeded")
	}
}
