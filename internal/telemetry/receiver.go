package telemetry

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxBody bounds one export, compressed or not.
const maxBody = 16 << 20

// Sink receives one export's events for one step run. It is called on the
// HTTP handler goroutine before the response is written, so an export the
// exporter saw acknowledged has been stored.
type Sink func(stepRunID int64, evs []Event) error

// Receiver is a localhost OTLP/HTTP-JSON endpoint. The step run an export
// belongs to is in the URL path, behind a random token that keeps stray
// local processes from writing rows.
type Receiver struct {
	srv   *http.Server
	ln    net.Listener
	token string
}

// Listen starts a receiver on an ephemeral 127.0.0.1 port.
func Listen(sink Sink) (*Receiver, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listening for telemetry: %w", err)
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("generating telemetry token: %w", err)
	}
	r := &Receiver{ln: ln, token: hex.EncodeToString(b[:])}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /{token}/step-runs/{id}/v1/logs", r.handler(sink, DecodeLogs))
	mux.HandleFunc("POST /{token}/step-runs/{id}/v1/metrics", r.handler(sink, DecodeMetrics))
	r.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = r.srv.Serve(ln) }()
	return r, nil
}

func (r *Receiver) endpoint(stepRunID int64, signal string) string {
	return fmt.Sprintf("http://%s/%s/step-runs/%d/v1/%s", r.Addr(), r.token, stepRunID, signal)
}

// LogsEndpoint is the OTLP logs URL for one step run.
func (r *Receiver) LogsEndpoint(stepRunID int64) string { return r.endpoint(stepRunID, "logs") }

// MetricsEndpoint is the OTLP metrics URL for one step run.
func (r *Receiver) MetricsEndpoint(stepRunID int64) string { return r.endpoint(stepRunID, "metrics") }

// Addr is the listening host:port.
func (r *Receiver) Addr() string { return r.ln.Addr().String() }

// Close stops accepting and waits for in-flight exports to finish.
func (r *Receiver) Close(ctx context.Context) error { return r.srv.Shutdown(ctx) }

func (r *Receiver) handler(sink Sink, decode func([]byte) ([]Event, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.PathValue("token") != r.token {
			http.NotFound(w, req)
			return
		}
		id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "bad step run id", http.StatusBadRequest)
			return
		}
		if mt, _, err := mime.ParseMediaType(req.Header.Get("Content-Type")); err == nil && mt != "application/json" {
			http.Error(w, "only application/json is accepted", http.StatusUnsupportedMediaType)
			return
		}
		var src io.Reader = http.MaxBytesReader(w, req.Body, maxBody)
		if strings.EqualFold(req.Header.Get("Content-Encoding"), "gzip") {
			gz, err := gzip.NewReader(src)
			if err != nil {
				http.Error(w, "bad gzip body", http.StatusBadRequest)
				return
			}
			defer gz.Close()
			src = io.LimitReader(gz, maxBody)
		}
		body, err := io.ReadAll(src)
		if err != nil {
			http.Error(w, "reading body", http.StatusBadRequest)
			return
		}
		evs, err := decode(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(evs) > 0 {
			if err := sink(id, evs); err != nil {
				http.Error(w, errors.New("storing telemetry").Error(), http.StatusInternalServerError)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}
}
