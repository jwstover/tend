// Package telemetry decodes the OpenTelemetry that claude exports and
// serves a localhost receiver for it (tend task #31). Standard library
// only, like internal/usage: it knows OTLP/HTTP-JSON and nothing about
// tend's store, workflows or agents.
package telemetry

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Event is one claude_code.* log record (or a metric point), flattened.
type Event struct {
	// Name is the event name without the "claude_code." prefix:
	// "api_request", "api_error", "tool_result", "session.count", ...
	Name string
	// Time is the record's timeUnixNano, falling back to
	// observedTimeUnixNano, then to the moment of decoding.
	Time time.Time
	// Attributes are the record's attributes with the resource's merged
	// underneath (the record wins).
	Attributes map[string]string
}

type kv struct {
	Key   string   `json:"key"`
	Value anyValue `json:"value"`
}

// anyValue is the subset of OTLP's AnyValue tend reads. Arrays and kvlists
// are kept as their raw JSON text.
type anyValue struct {
	StringValue *string         `json:"stringValue"`
	IntValue    json.RawMessage `json:"intValue"`
	DoubleValue *float64        `json:"doubleValue"`
	BoolValue   *bool           `json:"boolValue"`
	ArrayValue  json.RawMessage `json:"arrayValue"`
	KvlistValue json.RawMessage `json:"kvlistValue"`
}

// String is the canonical text of the value: an int as decimal, a double
// without an exponent, a bool as true/false.
func (v anyValue) String() string {
	switch {
	case v.StringValue != nil:
		return *v.StringValue
	case len(v.IntValue) > 0:
		return strings.Trim(strings.TrimSpace(string(v.IntValue)), `"`)
	case v.DoubleValue != nil:
		return strconv.FormatFloat(*v.DoubleValue, 'f', -1, 64)
	case v.BoolValue != nil:
		return strconv.FormatBool(*v.BoolValue)
	case len(v.ArrayValue) > 0:
		return string(v.ArrayValue)
	case len(v.KvlistValue) > 0:
		return string(v.KvlistValue)
	}
	return ""
}

type resource struct {
	Attributes []kv `json:"attributes"`
}

type exportLogs struct {
	ResourceLogs []struct {
		Resource  resource `json:"resource"`
		ScopeLogs []struct {
			LogRecords []struct {
				TimeUnixNano         json.RawMessage `json:"timeUnixNano"`
				ObservedTimeUnixNano json.RawMessage `json:"observedTimeUnixNano"`
				Body                 anyValue        `json:"body"`
				Attributes           []kv            `json:"attributes"`
			} `json:"logRecords"`
		} `json:"scopeLogs"`
	} `json:"resourceLogs"`
}

type exportMetrics struct {
	ResourceMetrics []struct {
		Resource     resource `json:"resource"`
		ScopeMetrics []struct {
			Metrics []struct {
				Name string `json:"name"`
				Sum  struct {
					DataPoints []struct {
						Attributes   []kv            `json:"attributes"`
						TimeUnixNano json.RawMessage `json:"timeUnixNano"`
						AsInt        json.RawMessage `json:"asInt"`
						AsDouble     *float64        `json:"asDouble"`
					} `json:"dataPoints"`
				} `json:"sum"`
			} `json:"metrics"`
		} `json:"scopeMetrics"`
	} `json:"resourceMetrics"`
}

const namePrefix = "claude_code."

// DecodeLogs flattens an OTLP logs export into events. Records with no
// event name are skipped; only a body that is not JSON at all is an error.
func DecodeLogs(body []byte) ([]Event, error) {
	var ex exportLogs
	if err := json.Unmarshal(body, &ex); err != nil {
		return nil, fmt.Errorf("decoding OTLP logs: %w", err)
	}
	var out []Event
	for _, rl := range ex.ResourceLogs {
		res := attrMap(rl.Resource.Attributes, nil)
		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				attrs := attrMap(lr.Attributes, res)
				name := attrs["event.name"]
				if name == "" {
					name = lr.Body.String()
				}
				name = strings.TrimPrefix(name, namePrefix)
				if name == "" {
					continue
				}
				t := nanoTime(lr.TimeUnixNano)
				if t.IsZero() {
					t = nanoTime(lr.ObservedTimeUnixNano)
				}
				if t.IsZero() {
					t = time.Now()
				}
				out = append(out, Event{Name: name, Time: t, Attributes: attrs})
			}
		}
	}
	return out, nil
}

// DecodeMetrics keeps only claude_code.session.count, whose start_type
// says whether a session began fresh or as a resume. Every other metric
// duplicates what the api_request events already carry.
func DecodeMetrics(body []byte) ([]Event, error) {
	var ex exportMetrics
	if err := json.Unmarshal(body, &ex); err != nil {
		return nil, fmt.Errorf("decoding OTLP metrics: %w", err)
	}
	var out []Event
	for _, rm := range ex.ResourceMetrics {
		res := attrMap(rm.Resource.Attributes, nil)
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name != namePrefix+"session.count" {
					continue
				}
				for _, dp := range m.Sum.DataPoints {
					attrs := attrMap(dp.Attributes, res)
					switch {
					case len(dp.AsInt) > 0:
						attrs["value"] = strings.Trim(strings.TrimSpace(string(dp.AsInt)), `"`)
					case dp.AsDouble != nil:
						attrs["value"] = strconv.FormatFloat(*dp.AsDouble, 'f', -1, 64)
					default:
						attrs["value"] = "0"
					}
					t := nanoTime(dp.TimeUnixNano)
					if t.IsZero() {
						t = time.Now()
					}
					out = append(out, Event{Name: "session.count", Time: t, Attributes: attrs})
				}
			}
		}
	}
	return out, nil
}

// attrMap flattens attributes over a copy of base.
func attrMap(list []kv, base map[string]string) map[string]string {
	m := make(map[string]string, len(list)+len(base))
	for k, v := range base {
		m[k] = v
	}
	for _, a := range list {
		m[a.Key] = a.Value.String()
	}
	return m
}

// nanoTime parses a string- or number-encoded unix-nanosecond timestamp;
// the zero time when absent or unparseable.
func nanoTime(raw json.RawMessage) time.Time {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if s == "" || s == "null" {
		return time.Time{}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}
