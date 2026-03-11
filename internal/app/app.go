package app

import (
	"net/http"

	"webhook-catcher/internal/httpapi"
	"webhook-catcher/internal/infra/config"
	"webhook-catcher/internal/infra/logging"
	"webhook-catcher/internal/infra/metrics"
	"webhook-catcher/internal/repository"
	"webhook-catcher/internal/service"
)

func NewWithStore(cfg config.Config, store repository.Store) http.Handler {
	logger := logging.New()
	recorder := metrics.NewRecorder()
	svc := service.New(store, cfg.PublicBaseURL, cfg.MaxPayloadBytes, cfg.DefaultRetentionDays)
	api := httpapi.New(cfg, svc, logger, recorder)
	return api.Handler()
}
