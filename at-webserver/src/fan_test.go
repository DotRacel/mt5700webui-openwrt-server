package main

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFanCurveInterpolationAndValidation(t *testing.T) {
	curve := defaultFanCurve()
	for _, test := range []struct {
		temperature float64
		speed       int
	}{
		{20, 25}, {35, 25}, {42.5, 33}, {65, 70}, {70, 85}, {90, 100},
	} {
		if got := fanSpeed(curve, test.temperature); got != test.speed {
			t.Errorf("%.1f°C: got %d%%, want %d%%", test.temperature, got, test.speed)
		}
	}
	for _, curve := range [][]FanPoint{
		{{40, 30}},
		{{40, 30}, {40, 60}},
		{{40, 60}, {60, 30}},
		{{40, 30}, {60, 101}},
	} {
		if err := validateFanCurve(curve); err == nil {
			t.Errorf("invalid curve accepted: %#v", curve)
		}
	}
}

func TestFanSampleParsesHottestSensor(t *testing.T) {
	raw := "^CHIPTEMP: 432,445,451,398,401,407,428,431,539,442,419,423\r\nOK"
	sample, err := parseFanSample(raw, time.Now())
	if err != nil || sample.Temperature != 53.9 || len(sample.Sensors) != 12 {
		t.Fatalf("sample=%+v err=%v", sample, err)
	}
	if _, err := parseFanSample("^CHIPTEMP: 432,broken\r\nOK", time.Now()); err == nil {
		t.Fatal("malformed sensor reading accepted")
	}
}

func TestFanShellHookReceivesSpeedAndFailSafe(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "output")
	script := filepath.Join(dir, "fan.sh")
	if err := os.WriteFile(script, []byte("echo \"$1:$2:$FAN_PERCENT\" >> "+output+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	config := FanConfig{Enabled: true, Interval: 5 * time.Second, Curve: defaultFanCurve(), Script: script}
	fan := NewFanController(config, nil, NewLogger(LevelError))
	fan.apply(config, 50)
	fan.apply(config, 50)         // Same speed is not applied twice.
	fan.apply(config, 0)          // Valid cool reading returns to the first curve point.
	fan.apply(config, math.NaN()) // Sensor failure requests full speed.
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "40:50.0:40\n25:0.0:25\n100:NaN:100\n" {
		t.Fatalf("unexpected shell arguments: %q", data)
	}
}

func TestFanTemperatureQueriesShareOneSample(t *testing.T) {
	rig := newTestRig(t, "", NotificationConfig{})
	config := FanConfig{Enabled: true, Interval: 5 * time.Second, Curve: defaultFanCurve()}
	fan := NewFanController(config, rig.client, NewLogger(LevelError))
	rig.ws.AttachFan(fan)

	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := fan.Sample(context.Background())
			errors <- err
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	response := rig.ws.runCommand(chipTempCommand)
	if !response.Success || response.Data == nil || !strings.Contains(*response.Data, "^CHIPTEMP:") {
		t.Fatalf("unexpected WebUI response: %+v", response)
	}
	count := 0
	for _, command := range rig.modem.commands() {
		if command == chipTempCommand {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected one modem query, got %d", count)
	}
}

func TestFanWebSocketConfigAndLocalScriptBoundary(t *testing.T) {
	withFakeUCI(t, "")
	rig := newTestRig(t, "", NotificationConfig{})
	config := FanConfig{Enabled: false, Interval: 5 * time.Second, Script: "/etc/at-webserver/local.sh", Curve: defaultFanCurve()}
	rig.ws.AttachFan(NewFanController(config, rig.client, NewLogger(LevelError)))

	query := rig.ws.runCommand(fanQueryCommand)
	if !query.Success || query.Data == nil || !strings.Contains(*query.Data, config.Script) {
		t.Fatalf("fan query failed: %+v", query)
	}
	newConfig := fanConfigDTO{Enabled: true, Interval: 3, Curve: []FanPoint{{40, 30}, {70, 100}}, Status: &fanStatus{Script: "/tmp/untrusted.sh"}}
	payload, _ := json.Marshal(newConfig)
	set := rig.ws.runCommand(fanSetPrefix + string(payload))
	if !set.Success {
		t.Fatalf("fan set failed: %+v", set)
	}
	got := rig.ws.fan.Config()
	if !got.Enabled || got.Interval != 3 || got.Status.Script != config.Script {
		t.Fatalf("unexpected config or script changed by WebSocket: %+v", got)
	}

	invalid := rig.ws.runCommand(fanSetPrefix + `{"enabled":true,"interval":1,"curve":[{"temperature":40,"speed":20},{"temperature":50,"speed":30}]}`)
	if invalid.Success {
		t.Fatal("invalid sampling interval accepted")
	}
}
