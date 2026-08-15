package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/udaykishore-resu/guest-score/backend/internal/domain"
)

// errorBody is the single error shape the whole API returns, so a client can
// write one error handler rather than one per endpoint.
type errorBody struct {
	Error struct {
		Code    string             `json:"code"`
		Message string             `json:"message"`
		Fields  domain.FieldErrors `json:"fields,omitempty"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		// The status line is already committed at this point, so the only
		// useful action left is to make the failure visible in the logs.
		slog.Error("encoding response failed", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, msg string, fields domain.FieldErrors) {
	var body errorBody
	body.Error.Code = code
	body.Error.Message = msg
	body.Error.Fields = fields
	writeJSON(w, status, body)
}

// decodeJSON reads a request body strictly: unknown fields are an error, the
// body is size-capped, and trailing content after the JSON value is rejected.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("could not parse request body: %w", err)
	}
	if dec.More() {
		return fmt.Errorf("request body must contain exactly one JSON object")
	}
	return nil
}

// --- Middleware --------------------------------------------------------------

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

func withLogging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		log.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.bytes,
			"dur", time.Since(start).Round(time.Microsecond).String(),
		)
	})
}

// withCORS keeps the split dev setup (Vite on 5173, API on 8080) working. In
// the single-binary production build the SPA is same-origin and this is inert.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withRecover turns a handler panic into a 500 instead of killing the process
// and dropping every other in-flight connection.
func withRecover(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Error("panic recovered", "path", r.URL.Path, "value", v)
				writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }
