package gputint

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func settings() Config {
	return Config{Enabled: true, GPUUUIDs: []string{"GPU-a", "GPU-b", "GPU-c", "GPU-d"},
		BusyColor: [3]float64{62, 32, 77}, SmoothingSeconds: 5, SampleIntervalMilliseconds: 500}
}

func TestLinearRiseAndFallBetweenSamples(t *testing.T) {
	start := time.Unix(1000, 0)
	sensor := newInput(settings(), start)
	sensor.update(start, 100)
	for _, test := range []struct{ seconds, want float64 }{{0, 0}, {0.125, 2.5}, {1.25, 25}, {2.5, 50}, {5, 100}, {8, 100}} {
		now := start.Add(time.Duration(test.seconds * float64(time.Second)))
		if got := sensor.averageLocked(now); math.Abs(got-test.want) > 1e-9 {
			t.Fatalf("at %.3fs: got %v, want %v", test.seconds, got, test.want)
		}
	}
	sensor.update(start.Add(8*time.Second), 0)
	for _, test := range []struct{ seconds, want float64 }{{8, 100}, {8.125, 97.5}, {9.25, 75}, {10.5, 50}, {13, 0}, {18, 0}} {
		now := start.Add(time.Duration(test.seconds * float64(time.Second)))
		if got := sensor.averageLocked(now); math.Abs(got-test.want) > 1e-9 {
			t.Fatalf("at %.3fs: got %v, want %v", test.seconds, got, test.want)
		}
	}
}

func TestIrregularSamplesUseElapsedTime(t *testing.T) {
	start := time.Unix(1000, 0)
	sensor := newInput(settings(), start)
	sensor.update(start, 100)
	sensor.update(start.Add(time.Second), 0)
	if got := sensor.averageLocked(start.Add(5 * time.Second)); got != 20 {
		t.Fatalf("got %v, want 20", got)
	}
	if got := sensor.averageLocked(start.Add(5500 * time.Millisecond)); got != 10 {
		t.Fatalf("got %v, want 10", got)
	}
	sensor.update(start.Add(20*time.Second), 0)
	if got := sensor.averageLocked(start.Add(21 * time.Second)); got != 0 {
		t.Fatalf("stale history survived pruning: %v", got)
	}
}

func TestMissingTelemetryHoldsTintUntilRecovery(t *testing.T) {
	start := time.Unix(1000, 0)
	sensor := newInput(settings(), start)
	sensor.update(start, 100)
	sensor.hold(start.Add(2*time.Second), errors.New("GPU unavailable"))
	if got := sensor.averageLocked(start.Add(time.Hour)); got != 40 {
		t.Fatalf("failed telemetry changed the held tint: %v", got)
	}
	sensor.update(start.Add(time.Hour), 100)
	if got := sensor.averageLocked(start.Add(time.Hour + 2500*time.Millisecond)); got != 70 {
		t.Fatalf("recovery did not ramp smoothly: %v", got)
	}
}

func TestAggregateRequiresEverySelectedGPU(t *testing.T) {
	uuids := settings().GPUUUIDs
	data := "GPU-a, 100\nGPU-b, 0\nGPU-c, 0\nGPU-d, 0\nGPU-extra, 100\n"
	if got, err := parseGPUUsage(data, uuids); err != nil || got != 25 {
		t.Fatalf("aggregate = %v, %v", got, err)
	}
	for _, invalid := range []string{
		"GPU-a, 100\nGPU-b, 0\nGPU-c, 0\n",
		"GPU-a, N/A\nGPU-b, 0\nGPU-c, 0\nGPU-d, 0\n",
		"GPU-a, NaN\nGPU-b, 0\nGPU-c, 0\nGPU-d, 0\n",
		"GPU-a, 101\nGPU-b, 0\nGPU-c, 0\nGPU-d, 0\n",
		data + "GPU-a, 10\n",
	} {
		if _, err := parseGPUUsage(invalid, uuids); err == nil {
			t.Fatalf("accepted invalid telemetry: %q", invalid)
		}
	}
}

func TestDisabledInputPreservesExactColor(t *testing.T) {
	if err := Start(nil, nil); err != nil {
		t.Fatal(err)
	}
	r, g, b := Color(32, 62, 77)
	if r != 32 || g != 62 || b != 77 || Status().Enabled {
		t.Fatal("disabled input changed the original Storm color")
	}
}

func TestRejectInvalidConfig(t *testing.T) {
	for _, mutate := range []func(*Config){
		func(c *Config) { c.GPUUUIDs = nil },
		func(c *Config) { c.GPUUUIDs = []string{"GPU-a", "GPU-a"} },
		func(c *Config) { c.BusyColor[0] = math.NaN() },
		func(c *Config) { c.SmoothingSeconds = -1 },
		func(c *Config) { c.SampleIntervalMilliseconds = 1 },
	} {
		cfg := settings()
		mutate(&cfg)
		if err := Start(&cfg, nil); err == nil {
			Stop()
			t.Fatal("invalid config started a sampler")
		}
	}
}

func TestSamplerAndRenderReadersAreRaceFree(t *testing.T) {
	sensor := newInput(settings(), time.Now())
	var workers sync.WaitGroup
	for n := 0; n < 5; n++ {
		workers.Add(1)
		go func(writer bool) {
			defer workers.Done()
			for i := 0; i < 1000; i++ {
				if writer {
					sensor.update(time.Now(), float64(i%101))
				} else {
					sensor.mu.RLock()
					value := sensor.averageLocked(time.Now())
					sensor.mu.RUnlock()
					if value < 0 || value > 100 || math.IsNaN(value) {
						t.Errorf("invalid concurrent average: %v", value)
					}
				}
			}
		}(n == 0)
	}
	workers.Wait()
}

func TestSamplerStopsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sensor := newInput(settings(), time.Now())
	entered := make(chan struct{})
	go sensor.run(ctx, func(ctx context.Context) (float64, error) {
		close(entered)
		<-ctx.Done()
		return 0, ctx.Err()
	}, nil)
	<-entered
	cancel()
	select {
	case <-sensor.done:
	case <-time.After(time.Second):
		t.Fatal("sampler did not stop on cancellation")
	}
}

func TestActualGPUQuery(t *testing.T) {
	selected := os.Getenv("OPENLINKHUB_TEST_GPU_UUIDS")
	if selected == "" {
		t.Skip("set OPENLINKHUB_TEST_GPU_UUIDS for a read-only hardware check")
	}
	value, err := readGPUUsage(context.Background(), strings.Split(selected, ","))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual aggregate GPU utilization: %.2f%%", value)
}
