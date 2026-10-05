package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/waterma1one/rtsp-viewer/backend/internal/stream"
)

// DemoStream is a bundled test stream offered to the UI.
type DemoStream struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type Server struct {
	Manager   *stream.Manager
	Validator *URLValidator
	Demos     []DemoStream
	// AllowedOrigins are host patterns (path.Match syntax) allowed to call
	// the API and open WebSockets from a browser, e.g. "*.onrender.com".
	AllowedOrigins []string
	Log            *slog.Logger
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /api/demo-streams", s.handleDemos)
	mux.HandleFunc("GET /api/validate", s.handleValidate)
	mux.HandleFunc("GET /ws", s.handleWS)
	return s.cors(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "activeStreams": s.Manager.ActiveStreams()})
}

func (s *Server) handleDemos(w http.ResponseWriter, _ *http.Request) {
	demos := s.Demos
	if demos == nil {
		demos = []DemoStream{}
	}
	writeJSON(w, http.StatusOK, demos)
}

// handleValidate lets the UI reject a bad URL in the form, before it
// creates a tile and opens a WebSocket.
func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	u, err := s.Validator.Validate(r.Context(), r.URL.Query().Get("url"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": userMessage(err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": u})
}

// userMessage strips the ErrInvalidURL prefix for display.
func userMessage(err error) string {
	msg := err.Error()
	if errors.Is(err, ErrInvalidURL) {
		msg = strings.TrimPrefix(msg, ErrInvalidURL.Error()+": ")
	}
	return msg
}

func (s *Server) originAllowed(origin string) bool {
	if origin == "" {
		return false
	}
	host := origin
	if i := strings.Index(origin, "://"); i >= 0 {
		host = origin[i+3:]
	}
	for _, p := range s.AllowedOrigins {
		pattern := p
		target := host
		if strings.Contains(p, "://") {
			target = origin
		}
		if ok, _ := path.Match(strings.ToLower(pattern), strings.ToLower(target)); ok {
			return true
		}
	}
	return false
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); s.originAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
