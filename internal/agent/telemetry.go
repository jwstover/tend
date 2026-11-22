package agent

// TelemetryEndpoints are the per-step-run OTLP/HTTP URLs a workflow
// runner's receiver listens on (tend task #31).
type TelemetryEndpoints struct {
	Logs, Metrics string
}

// Env is the environment that makes claude export OpenTelemetry to those
// URLs as JSON, nil when neither is set. Appended after os.Environ() so
// that, os/exec keeping the last duplicate, tend's values override any
// OTel settings in the user's shell -- for these step processes only.
// The signal-specific endpoint variables are used verbatim by exporters,
// so the step run id in the path survives. OTEL_LOG_TOOL_DETAILS is
// deliberately left alone.
func (t TelemetryEndpoints) Env() []string {
	if t.Logs == "" && t.Metrics == "" {
		return nil
	}
	env := []string{
		"CLAUDE_CODE_ENABLE_TELEMETRY=1",
		"OTEL_EXPORTER_OTLP_PROTOCOL=http/json",
		"OTEL_TRACES_EXPORTER=none",
	}
	if t.Logs != "" {
		env = append(env,
			"OTEL_LOGS_EXPORTER=otlp",
			"OTEL_EXPORTER_OTLP_LOGS_PROTOCOL=http/json",
			"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT="+t.Logs,
			// Localhost is cheap, and a short interval shrinks the gap
			// where a short step exits before its exporter flushes.
			"OTEL_LOGS_EXPORT_INTERVAL=1000",
		)
	}
	if t.Metrics != "" {
		env = append(env,
			"OTEL_METRICS_EXPORTER=otlp",
			"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL=http/json",
			"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT="+t.Metrics,
			"OTEL_METRIC_EXPORT_INTERVAL=1000",
			// Delta, so every point is an increment and rows can be summed.
			"OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=delta",
		)
	}
	return env
}
