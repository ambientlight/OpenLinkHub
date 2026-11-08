// GPU utilization input for Storm. Sampling runs independently of RGB rendering.
package gputint

import (
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Config struct {
	Enabled                    bool       `json:"enabled"`
	GPUUUIDs                   []string   `json:"gpuUUIDs"`
	BusyColor                  [3]float64 `json:"busyColor"`
	SmoothingSeconds           float64    `json:"smoothingSeconds"`
	SampleIntervalMilliseconds int        `json:"sampleIntervalMilliseconds"`
}

type Snapshot struct {
	Enabled         bool      `json:"enabled"`
	RawPercent      float64   `json:"rawPercent"`
	SmoothedPercent float64   `json:"smoothedPercent"`
	LastSuccess     time.Time `json:"lastSuccess"`
	Error           string    `json:"error,omitempty"`
}

type point struct {
	when  time.Time
	value float64
}

type input struct {
	mu          sync.RWMutex
	config      Config
	window      time.Duration
	points      []point
	raw         float64
	lastSuccess time.Time
	lastError   string
	cancel      context.CancelFunc
	done        chan struct{}
}

var active atomic.Pointer[input]

func normalize(config Config) (Config, error) {
	if len(config.GPUUUIDs) == 0 {
		return config, fmt.Errorf("gpuStorm.gpuUUIDs must select at least one GPU")
	}
	seen := make(map[string]bool)
	for _, uuid := range config.GPUUUIDs {
		if !strings.HasPrefix(uuid, "GPU-") || seen[uuid] {
			return config, fmt.Errorf("gpuStorm.gpuUUIDs contains an invalid or duplicate UUID")
		}
		seen[uuid] = true
	}
	for _, value := range config.BusyColor {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 255 {
			return config, fmt.Errorf("gpuStorm.busyColor components must be between 0 and 255")
		}
	}
	if config.SmoothingSeconds == 0 {
		config.SmoothingSeconds = 5
	}
	if math.IsNaN(config.SmoothingSeconds) || math.IsInf(config.SmoothingSeconds, 0) ||
		config.SmoothingSeconds < 0.05 || config.SmoothingSeconds > 60 {
		return config, fmt.Errorf("gpuStorm.smoothingSeconds must be between 0.05 and 60")
	}
	if config.SampleIntervalMilliseconds == 0 {
		config.SampleIntervalMilliseconds = 500
	}
	if config.SampleIntervalMilliseconds < 100 || config.SampleIntervalMilliseconds > 10000 {
		return config, fmt.Errorf("gpuStorm.sampleIntervalMilliseconds must be between 100 and 10000")
	}
	config.GPUUUIDs = append([]string(nil), config.GPUUUIDs...)
	return config, nil
}

func newInput(config Config, now time.Time) *input {
	window := time.Duration(config.SmoothingSeconds * float64(time.Second))
	return &input{
		config: config, window: window,
		points: []point{{when: now.Add(-window), value: 0}},
		done:   make(chan struct{}),
	}
}

// Start enables one shared sampler for every Storm renderer. A nil or disabled
// configuration leaves the existing effect unchanged.
func Start(config *Config, report func(error)) error {
	Stop()
	if config == nil || !config.Enabled {
		return nil
	}
	settings, err := normalize(*config)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	sensor := newInput(settings, time.Now())
	sensor.cancel = cancel
	active.Store(sensor)
	go sensor.run(ctx, func(ctx context.Context) (float64, error) {
		return readGPUUsage(ctx, settings.GPUUUIDs)
	}, report)
	return nil
}

func Stop() {
	if sensor := active.Swap(nil); sensor != nil {
		sensor.cancel()
		<-sensor.done
	}
}

func readGPUUsage(ctx context.Context, uuids []string) (float64, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=uuid,utilization.gpu",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return 0, fmt.Errorf("GPU utilization query: %w", err)
	}
	return parseGPUUsage(string(data), uuids)
}

func parseGPUUsage(data string, uuids []string) (float64, error) {
	rows, err := csv.NewReader(strings.NewReader(data)).ReadAll()
	if err != nil {
		return 0, err
	}
	wanted := make(map[string]bool, len(uuids))
	for _, uuid := range uuids {
		wanted[uuid] = true
	}
	values := make(map[string]float64, len(uuids))
	for _, row := range rows {
		if len(row) != 2 {
			return 0, fmt.Errorf("unexpected GPU utilization output")
		}
		uuid := strings.TrimSpace(row[0])
		if !wanted[uuid] {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(row[1]), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 100 {
			return 0, fmt.Errorf("invalid utilization for %s", uuid)
		}
		if _, exists := values[uuid]; exists {
			return 0, fmt.Errorf("duplicate utilization for %s", uuid)
		}
		values[uuid] = value
	}
	if len(values) != len(uuids) || len(uuids) == 0 {
		return 0, fmt.Errorf("GPU utilization is missing for a configured GPU")
	}
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total / float64(len(values)), nil
}

func (sensor *input) averageLocked(now time.Time) float64 {
	start := now.Add(-sensor.window)
	held := sensor.points[0].value
	area := 0.0
	for _, sample := range sensor.points {
		if !sample.when.After(start) {
			held = sample.value
			continue
		}
		if sample.when.After(now) {
			break
		}
		area += sample.when.Sub(start).Seconds() * held
		start, held = sample.when, sample.value
	}
	value := (area + now.Sub(start).Seconds()*held) / sensor.window.Seconds()
	return math.Max(0, math.Min(100, value))
}

func (sensor *input) update(now time.Time, value float64) {
	sensor.mu.Lock()
	defer sensor.mu.Unlock()
	if sensor.points[len(sensor.points)-1].value != value {
		sensor.points = append(sensor.points, point{when: now, value: value})
	}
	cutoff := now.Add(-sensor.window)
	for len(sensor.points) > 1 && !sensor.points[1].when.After(cutoff) {
		sensor.points = sensor.points[1:]
	}
	sensor.raw, sensor.lastSuccess, sensor.lastError = value, now, ""
}

func (sensor *input) hold(now time.Time, err error) {
	sensor.mu.Lock()
	defer sensor.mu.Unlock()
	value := sensor.averageLocked(now)
	sensor.points = []point{{when: now.Add(-sensor.window), value: value}}
	sensor.lastError = err.Error()
}

func (sensor *input) run(ctx context.Context, query func(context.Context) (float64, error), report func(error)) {
	defer close(sensor.done)
	ticker := time.NewTicker(time.Duration(sensor.config.SampleIntervalMilliseconds) * time.Millisecond)
	defer ticker.Stop()
	lastError := ""
	for {
		value, err := query(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			sensor.hold(time.Now(), err)
			if report != nil && lastError != err.Error() {
				report(err)
			}
			lastError = err.Error()
		} else {
			sensor.update(time.Now(), value)
			if report != nil && lastError != "" {
				report(nil)
			}
			lastError = ""
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Color reads only in-memory samples. It never changes a profile, restarts an
// animation, performs I/O, or queries a GPU from the RGB rendering loop.
func Color(red, green, blue float64) (float64, float64, float64) {
	if sensor := active.Load(); sensor != nil {
		sensor.mu.RLock()
		factor := sensor.averageLocked(time.Now()) / 100
		sensor.mu.RUnlock()
		busy := sensor.config.BusyColor
		return red + (busy[0]-red)*factor, green + (busy[1]-green)*factor, blue + (busy[2]-blue)*factor
	}
	return red, green, blue
}

func Status() Snapshot {
	if sensor := active.Load(); sensor != nil {
		sensor.mu.RLock()
		defer sensor.mu.RUnlock()
		return Snapshot{
			Enabled: true, RawPercent: sensor.raw, SmoothedPercent: sensor.averageLocked(time.Now()),
			LastSuccess: sensor.lastSuccess, Error: sensor.lastError,
		}
	}
	return Snapshot{}
}
