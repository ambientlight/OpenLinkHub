package rgb

import (
	"OpenLinkHub/src/gputint"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func checkStormFrames(t *testing.T, base [3]byte) {
	t.Helper()
	start := Color{Red: 32, Green: 62, Blue: 77, Brightness: 0.5}
	end := Color{Red: 255, Green: 255, Blue: 255, Brightness: 0.5}
	originalStart, originalEnd := start, end
	renderer := New(64, 4, &start, &end, 1, 20, time.Second, true)
	for frame := 0; frame < 2000; frame++ {
		renderer.Storm()
		if len(renderer.Output) != 64*3 {
			t.Fatalf("frame %d has wrong output size: %d", frame, len(renderer.Output))
		}
		for led := 0; led < 64; led++ {
			pixel := [3]byte{renderer.Output[led*3], renderer.Output[led*3+1], renderer.Output[led*3+2]}
			if pixel != base && pixel != [3]byte{127, 127, 127} {
				t.Fatalf("unexpected or black pixel at frame %d LED %d: %v", frame, led, pixel)
			}
		}
	}
	if start != originalStart || end != originalEnd || renderer.RgbModeSpeed != 4 {
		t.Fatal("renderer mutated the saved colors or speed")
	}
}

func TestStormRetainsBlueWithGPUInputDisabled(t *testing.T) {
	gputint.Stop()
	checkStormFrames(t, [3]byte{16, 31, 38})
}

func TestStormUsesGPUInputWithoutBlackFramesOrProfileMutation(t *testing.T) {
	directory := t.TempDir()
	// Only this test process sees the fake sampler. No hardware or running
	// OpenLinkHub instance is opened by these renderer tests.
	program := "#!/bin/sh\nprintf 'GPU-test, 100\\n'\n"
	if err := os.WriteFile(filepath.Join(directory, "nvidia-smi"), []byte(program), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	settings := gputint.Config{Enabled: true, GPUUUIDs: []string{"GPU-test"},
		BusyColor: [3]float64{62, 32, 77}, SmoothingSeconds: 0.05, SampleIntervalMilliseconds: 100}
	if err := gputint.Start(&settings, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gputint.Stop)
	deadline := time.Now().Add(3 * time.Second)
	for gputint.Status().SmoothedPercent < 100 {
		if time.Now().After(deadline) {
			t.Fatalf("GPU input did not reach 100%%: %+v", gputint.Status())
		}
		time.Sleep(5 * time.Millisecond)
	}
	checkStormFrames(t, [3]byte{31, 16, 38})
}

func TestStormTransitionsDoNotEmitResetFrames(t *testing.T) {
	directory := t.TempDir()
	loadFile := filepath.Join(directory, "load")
	program := "#!/bin/sh\nprintf 'GPU-test, '\ncat \"$OPENLINKHUB_TEST_LOAD_FILE\"\n"
	if err := os.WriteFile(filepath.Join(directory, "nvidia-smi"), []byte(program), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPENLINKHUB_TEST_LOAD_FILE", loadFile)
	setLoad := func(value string) {
		t.Helper()
		if err := os.WriteFile(loadFile+".tmp", []byte(value+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(loadFile+".tmp", loadFile); err != nil {
			t.Fatal(err)
		}
	}
	setLoad("0")
	settings := gputint.Config{Enabled: true, GPUUUIDs: []string{"GPU-test"},
		BusyColor: [3]float64{62, 32, 77}, SmoothingSeconds: 0.05, SampleIntervalMilliseconds: 100}
	if err := gputint.Start(&settings, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gputint.Stop)
	start := Color{Red: 32, Green: 62, Blue: 77, Brightness: 0.5}
	end := Color{Red: 255, Green: 255, Blue: 255, Brightness: 0.5}
	renderer := New(64, 4, &start, &end, 1, 20, time.Second, true)
	for _, phase := range []struct {
		load string
		want float64
	}{{"100", 100}, {"0", 0}, {"100", 100}, {"0", 0}} {
		setLoad(phase.load)
		deadline := time.Now().Add(3 * time.Second)
		for {
			renderer.Storm()
			for led := 0; led < 64; led++ {
				r, g, b := renderer.Output[led*3], renderer.Output[led*3+1], renderer.Output[led*3+2]
				white := r == 127 && g == 127 && b == 127
				base := r >= 16 && r <= 31 && g >= 16 && g <= 31 && b == 38
				if !white && !base {
					t.Fatalf("reset/invalid pixel during load transition to %s: %v", phase.load, []byte{r, g, b})
				}
			}
			state := gputint.Status()
			if state.Error == "" && state.RawPercent == phase.want && state.SmoothedPercent == phase.want {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("transition did not finish: %+v", state)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	if start.Red != 32 || start.Green != 62 || start.Blue != 77 || end.Red != 255 {
		t.Fatal("transition mutated a stored profile color")
	}
}
