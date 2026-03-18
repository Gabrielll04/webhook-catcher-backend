package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

func (a *API) newRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(chimiddleware.StripSlashes)

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		reqID := requestIDFromContext(r.Context())
		writeError(w, http.StatusNotFound, reqID, "route_not_found", "Rota nao encontrada", nil)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		reqID := requestIDFromContext(r.Context())
		writeError(w, http.StatusMethodNotAllowed, reqID, "method_not_allowed", "Metodo nao suportado", nil)
	})

	r.Get("/health", a.handleHealth)
	r.Get("/ready", a.handleReady)
	r.Post("/internal/retention/run", a.handleRetention)

	r.MethodFunc(http.MethodGet, "/hook/{token}", a.handleCapture)
	r.MethodFunc(http.MethodPost, "/hook/{token}", a.handleCapture)
	r.MethodFunc(http.MethodPut, "/hook/{token}", a.handleCapture)
	r.MethodFunc(http.MethodPatch, "/hook/{token}", a.handleCapture)
	r.MethodFunc(http.MethodDelete, "/hook/{token}", a.handleCapture)
	r.MethodFunc(http.MethodHead, "/hook/{token}", a.handleCapture)
	r.MethodFunc(http.MethodOptions, "/hook/{token}", a.handleCapture)

	r.Route("/v1/inboxes", func(r chi.Router) {
		r.Post("/", a.handleCreateInbox)
		r.Get("/", a.handleListInboxes)
		r.Get("/{id}", a.handleGetInboxRoute)
		r.Patch("/{id}", a.handlePatchInboxRoute)
		r.Delete("/{id}", a.handleDeleteInboxRoute)
		r.Get("/{id}/requests", a.handleListRequestsRoute)
		r.Get("/{id}/requests/{request_id}", a.handleGetRequestDetailsRoute)
		r.Get("/{id}/stream", a.handleInboxStreamRoute)
	})

	r.Route("/v1/monitoring", func(r chi.Router) {
		r.Get("/summary", a.handleMonitoringSummary)
		r.Get("/timeseries", a.handleMonitoringTimeseries)
		r.Get("/breakdown", a.handleMonitoringBreakdown)
		r.Get("/inboxes/health", a.handleMonitoringInboxesHealth)
		r.Get("/live", a.handleMonitoringLiveStream)
	})

	return r
}

func (a *API) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": time.Now().UTC().Format(time.RFC3339Nano)})
}

func (a *API) handleReady(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	if err := a.services.Ready(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, reqID, "not_ready", "Dependencias indisponiveis", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

func (a *API) handleGetInboxRoute(w http.ResponseWriter, r *http.Request) {
	a.handleGetInbox(w, r, chi.URLParam(r, "id"))
}

func (a *API) handlePatchInboxRoute(w http.ResponseWriter, r *http.Request) {
	a.handlePatchInbox(w, r, chi.URLParam(r, "id"))
}

func (a *API) handleDeleteInboxRoute(w http.ResponseWriter, r *http.Request) {
	a.handleDeleteInbox(w, r, chi.URLParam(r, "id"))
}

func (a *API) handleListRequestsRoute(w http.ResponseWriter, r *http.Request) {
	a.handleListRequests(w, r, chi.URLParam(r, "id"))
}

func (a *API) handleGetRequestDetailsRoute(w http.ResponseWriter, r *http.Request) {
	a.handleGetRequestDetails(w, r, chi.URLParam(r, "id"), chi.URLParam(r, "request_id"))
}

func (a *API) handleInboxStreamRoute(w http.ResponseWriter, r *http.Request) {
	a.handleInboxStream(w, r, chi.URLParam(r, "id"))
}
