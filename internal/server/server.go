// Package server exposes moraine's HTTP transport: a net/http ServeMux wiring
// the embedded UI assets and the JSON API onto the store. It depends on store
// and thumb, never the reverse (Constitution Principle III). Errors are
// machine-readable and actionable (Principle VI).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/sgaunet/moraine/internal/photo"
	"github.com/sgaunet/moraine/internal/store"
	"github.com/sgaunet/moraine/web"
)

// Thumbnailer produces a thumbnail (or placeholder) for a photo. Injected so
// the server stays decoupled from the thumbnail implementation and testable.
type Thumbnailer interface {
	// Thumbnail returns the encoded image, its MIME type and a validator ETag.
	Thumbnail(path string, format photo.Format) (data []byte, contentType, etag string, err error)
}

// Server holds the wired HTTP handler and its dependencies.
type Server struct {
	store  *store.Store
	thumbs Thumbnailer
	log    *slog.Logger
	mux    *http.ServeMux
	srv    *http.Server
}

// New builds a Server and registers all routes.
func New(st *store.Store, thumbs Thumbnailer, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{store: st, thumbs: thumbs, log: log, mux: http.NewServeMux()}
	s.routes()
	return s
}

// Handler exposes the mux (useful for httptest).
func (s *Server) Handler() http.Handler { return s.mux }

// routes registers every endpoint. Handlers live in sibling files.
func (s *Server) routes() {
	// Static UI (embedded).
	s.mux.HandleFunc("GET /{$}", s.handleIndex)
	assetsFS, _ := fs.Sub(web.FS, ".")
	s.mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServerFS(assetsFS)))

	// API + media (US1).
	s.mux.HandleFunc("GET /api/groups", s.handleGroups)
	s.mux.HandleFunc("GET /thumb/{photoID}", s.handleThumb)
	s.mux.HandleFunc("GET /photo/{photoID}", s.handlePhoto) // US5

	// Mutations.
	s.mux.HandleFunc("POST /api/photos/{photoID}/move", s.handleMovePhoto) // US3
	s.mux.HandleFunc("PATCH /api/groups/{groupID}", s.handlePatchGroup)    // US4
	s.mux.HandleFunc("POST /api/groups/{groupID}/commit", s.handleCommit)  // US2
	s.mux.HandleFunc("POST /api/groups", s.handleCreateGroup)              // optional (Polish)

	// JSON 404 for anything else.
	s.mux.HandleFunc("/", s.handleNotFound)
}

func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	data, err := web.FS.ReadFile("index.html")
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal", "interface indisponible")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, http.StatusNotFound, "not_found",
		"ressource introuvable : "+r.Method+" "+r.URL.Path)
}

// ---- Response helpers (Principle VI) ---------------------------------------

// errorResponse is the machine-readable, actionable error envelope.
type errorResponse struct {
	Error   string `json:"error"`   // stable machine code
	Message string `json:"message"` // actionable explanation
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		s.log.Error("encode response", "err", err)
	}
}

func (s *Server) writeError(w http.ResponseWriter, status int, code, message string) {
	s.writeJSON(w, status, errorResponse{Error: code, Message: message})
}

// ok is the trivial success envelope {"ok": true}.
type ok struct {
	OK bool `json:"ok"`
}

func (s *Server) writeOK(w http.ResponseWriter) {
	s.writeJSON(w, http.StatusOK, ok{OK: true})
}

// ---- Lifecycle --------------------------------------------------------------

// Start serves on addr until ctx is cancelled, then shuts down gracefully.
func (s *Server) Start(ctx context.Context, addr string) error {
	s.srv = &http.Server{
		Addr:              addr,
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- s.srv.ListenAndServe() }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
