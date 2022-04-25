package metrics

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics is used to expose container image version checks as prometheus
// metrics.
type Metrics struct {
	*http.Server

	registry              *prometheus.Registry
	containerImageVersion *prometheus.GaugeVec
	log                   *logrus.Entry

	// container cache stores a cache of a container's current image, version,
	// and the latest
	containerCache map[string]cacheItem
	mu             sync.Mutex
}

type cacheItem struct {
	image          string
	currentVersion string
	latestVersion  string
	os             string
	arch           string
}

// Entry is a struct containing a single metrics label set
type Entry struct {
	Namespace      string
	Pod            string
	Container      string
	ImageURL       string
	IsLatest       bool
	CurrentVersion string
	LatestVersion  string
	OS             string
	Arch           string
}

func New(log *logrus.Entry) *Metrics {
	containerImageVersion := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "version_checker",
			Name:      "is_latest_version",
			Help:      "Where the container in use is using the latest upstream registry version",
		},
		[]string{
			"namespace", "pod", "container", "image", "current_version", "latest_version", "architecture", "os",
		},
	)

	registry := prometheus.NewRegistry()
	registry.MustRegister(containerImageVersion)

	return &Metrics{
		log:                   log.WithField("module", "metrics"),
		registry:              registry,
		containerImageVersion: containerImageVersion,
		containerCache:        make(map[string]cacheItem),
	}
}

// Run will run the metrics server
func (m *Metrics) Run(servingAddress string) error {
	router := http.NewServeMux()
	router.Handle("/metrics", promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{}))

	ln, err := net.Listen("tcp", servingAddress)
	if err != nil {
		return err
	}

	m.Server = &http.Server{
		Addr:           ln.Addr().String(),
		ReadTimeout:    8 * time.Second,
		WriteTimeout:   8 * time.Second,
		MaxHeaderBytes: 1 << 15, // 1 MiB
		Handler:        router,
	}

	go func() {
		m.log.Infof("serving metrics on %s/metrics", ln.Addr())

		if err := m.Serve(ln); err != nil {
			m.log.Errorf("failed to serve prometheus metrics: %s", err)
			return
		}
	}()

	return nil
}

func (m *Metrics) AddImage(entry *Entry) {
	// Remove old image url/version if it exists
	m.RemoveImage(entry.Namespace, entry.Pod, entry.Container)

	m.mu.Lock()
	defer m.mu.Unlock()

	isLatestF := 0.0
	if entry.IsLatest {
		isLatestF = 1.0
	}

	m.containerImageVersion.With(
		m.buildLabels(entry),
	).Set(isLatestF)

	index := m.latestImageIndex(entry.Namespace, entry.Pod, entry.Container)
	m.containerCache[index] = cacheItem{
		image:          entry.ImageURL,
		currentVersion: entry.CurrentVersion,
		latestVersion:  entry.LatestVersion,
		os:             entry.OS,
		arch:           entry.Arch,
	}
}

func (m *Metrics) RemoveImage(namespace, pod, container string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	index := m.latestImageIndex(namespace, pod, container)
	item, ok := m.containerCache[index]
	if !ok {
		return
	}

	m.containerImageVersion.Delete(
		m.buildLabels(&Entry{
			Namespace:      namespace,
			Pod:            pod,
			Container:      container,
			ImageURL:       item.image,
			CurrentVersion: item.currentVersion,
			LatestVersion:  item.latestVersion,
			OS:             item.os,
			Arch:           item.arch,
		}),
	)
	delete(m.containerCache, index)
}

func (m *Metrics) latestImageIndex(namespace, pod, container string) string {
	return strings.Join([]string{namespace, pod, container}, "")
}

func (m *Metrics) buildLabels(entry *Entry) prometheus.Labels {
	return prometheus.Labels{
		"namespace":       entry.Namespace,
		"pod":             entry.Pod,
		"container":       entry.Container,
		"image":           entry.ImageURL,
		"current_version": entry.CurrentVersion,
		"latest_version":  entry.LatestVersion,
		"architecture":    entry.Arch,
		"os":              entry.OS,
	}
}

func (m *Metrics) Shutdown() error {
	// If metrics server is not started than exit early
	if m.Server == nil {
		return nil
	}

	m.log.Info("shutting down prometheus metrics server...")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	if err := m.Server.Shutdown(ctx); err != nil {
		return fmt.Errorf("prometheus metrics server shutdown failed: %s", err)
	}

	m.log.Info("prometheus metrics server gracefully stopped")

	return nil
}