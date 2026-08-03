package license

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/easyp-tech/service/internal/core"
)

// Metrics содержит Prometheus-метрики лицензирования.
//
// Между тремя гейджами есть разница, и она важна для алертов: `valid` отвечает
// «платит ли эта установка прямо сейчас», `expiry` — «сколько осталось», а
// `in_grace` — «срок уже вышел, идёт отсчёт до отключения». Первый не заменяет
// два других: в грейс-периоде установка ещё Enterprise, но это временно.
type Metrics struct {
	valid         prometheus.Gauge       // {namespace}_license_valid: 1=enterprise, 0=community
	expiryTS      prometheus.Gauge       // {namespace}_license_expiry_timestamp_seconds
	inGrace       prometheus.Gauge       // {namespace}_license_in_grace: 1=выдан по грейсу
	featureDenied *prometheus.CounterVec // {namespace}_license_feature_denied_total{feature}
}

// NewMetrics создаёт и регистрирует метрики лицензирования в реестре.
func NewMetrics(reg *prometheus.Registry, namespace string) *Metrics {
	metricObj := &Metrics{
		valid: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "license_valid",
			Help:      "Whether an Enterprise licence is in force (1) or the service runs in community mode (0).",
		}),
		expiryTS: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "license_expiry_timestamp_seconds",
			Help:      "Unix timestamp the licence expires at; 0 when no licence is configured.",
		}),
		inGrace: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "license_in_grace",
			Help:      "Whether the licence has expired and the service is running on its grace period (1) or not (0).",
		}),
		featureDenied: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "license_feature_denied_total",
			Help:      "Total number of feature access denials.",
		}, []string{"feature"}),
	}

	if reg != nil {
		reg.MustRegister(metricObj.valid, metricObj.expiryTS, metricObj.inGrace, metricObj.featureDenied)
	}

	return metricObj
}

// observe publishes the state the claims describe.
//
// Note that "valid" tracks the tier rather than the absence of an error.
// ValidateLicense reports community mode as a successful result — it is a
// legitimate configuration, not a failure — so keying this off the error would
// leave the gauge at 1 on an installation with no licence at all.
func (m *Metrics) observe(claims core.LicenseClaims) {
	if claims.Tier == core.LicenseTierEnterprise {
		m.valid.Set(1)
	} else {
		m.valid.Set(0)
	}

	if claims.ExpiresAt.IsZero() {
		m.expiryTS.Set(0)
	} else {
		m.expiryTS.Set(float64(claims.ExpiresAt.Unix()))
	}

	if claims.InGrace {
		m.inGrace.Set(1)
	} else {
		m.inGrace.Set(0)
	}
}
