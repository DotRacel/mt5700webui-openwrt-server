package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	fanQueryCommand   = "AT+FAN?"
	fanSetPrefix      = "AT+FAN="
	fanResponsePrefix = "+FAN: "
	chipTempCommand   = "AT^CHIPTEMP?"
)

// FanPoint maps module temperature in Celsius to a requested fan percentage.
type FanPoint struct {
	Temperature float64 `json:"temperature"`
	Speed       int     `json:"speed"`
}

func defaultFanCurve() []FanPoint {
	return []FanPoint{{35, 25}, {50, 40}, {65, 70}, {75, 100}}
}

func parseFanCurve(raw string) ([]FanPoint, error) {
	var points []FanPoint
	if err := json.Unmarshal([]byte(raw), &points); err != nil {
		return nil, err
	}
	if err := validateFanCurve(points); err != nil {
		return nil, err
	}
	return points, nil
}

func validateFanCurve(points []FanPoint) error {
	if len(points) < 2 || len(points) > 12 {
		return errors.New("风扇曲线需要 2 到 12 个节点")
	}
	for i, p := range points {
		if math.IsNaN(p.Temperature) || math.IsInf(p.Temperature, 0) || p.Temperature < 0 || p.Temperature > 120 {
			return fmt.Errorf("节点 %d 的温度必须在 0–120°C", i+1)
		}
		if p.Speed < 0 || p.Speed > 100 {
			return fmt.Errorf("节点 %d 的转速必须在 0–100%%", i+1)
		}
		if i > 0 && p.Temperature <= points[i-1].Temperature {
			return errors.New("温度节点必须严格递增")
		}
		if i > 0 && p.Speed < points[i-1].Speed {
			return errors.New("温度升高时转速不能降低")
		}
	}
	return nil
}

func fanSpeed(points []FanPoint, temperature float64) int {
	if temperature <= points[0].Temperature {
		return points[0].Speed
	}
	for i := 1; i < len(points); i++ {
		if temperature <= points[i].Temperature {
			lo, hi := points[i-1], points[i]
			ratio := (temperature - lo.Temperature) / (hi.Temperature - lo.Temperature)
			return int(math.Round(float64(lo.Speed) + ratio*float64(hi.Speed-lo.Speed)))
		}
	}
	return points[len(points)-1].Speed
}

type fanSample struct {
	Raw         string    `json:"raw"`
	Sensors     []float64 `json:"sensors"`
	Temperature float64   `json:"temperature"`
	SampledAt   time.Time `json:"sampled_at"`
}

func parseFanSample(raw string, at time.Time) (fanSample, error) {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "^CHIPTEMP:") {
			continue
		}
		fields := strings.Split(strings.TrimSpace(strings.TrimPrefix(line, "^CHIPTEMP:")), ",")
		if len(fields) < 12 {
			return fanSample{}, errors.New("模块温度应答缺少传感器数据")
		}
		sample := fanSample{Raw: raw, SampledAt: at, Sensors: make([]float64, 0, len(fields))}
		for _, field := range fields {
			value, err := strconv.Atoi(strings.TrimSpace(field))
			if err != nil || value < 0 || value > 1500 {
				return fanSample{}, errors.New("模块温度应答包含无效数值")
			}
			celsius := float64(value) / 10
			sample.Sensors = append(sample.Sensors, celsius)
			if celsius > sample.Temperature {
				sample.Temperature = celsius
			}
		}
		return sample, nil
	}
	return fanSample{}, errors.New("未收到 ^CHIPTEMP 应答")
}

type fanStatus struct {
	Sample   *fanSample `json:"sample,omitempty"`
	Speed    *int       `json:"speed,omitempty"`
	Error    string     `json:"error,omitempty"`
	Script   string     `json:"script"`
	ScriptOK bool       `json:"script_ok"`
}

type fanConfigDTO struct {
	Enabled  bool       `json:"enabled"`
	Interval int        `json:"interval"`
	Curve    []FanPoint `json:"curve"`
	Status   *fanStatus `json:"status,omitempty"`
}

type FanController struct {
	client *ATClient
	log    *Logger

	// sampleMu merges simultaneous WebUI and background temperature requests.
	sampleMu sync.Mutex
	setMu    sync.Mutex
	mu       sync.RWMutex
	cfg      FanConfig
	sample   *fanSample
	speed    *int
	lastErr  string
	wake     chan struct{}
}

func NewFanController(cfg FanConfig, client *ATClient, log *Logger) *FanController {
	return &FanController{cfg: cfg, client: client, log: log, wake: make(chan struct{}, 1)}
}

func (f *FanController) Config() fanConfigDTO {
	f.mu.RLock()
	defer f.mu.RUnlock()
	curve := append([]FanPoint(nil), f.cfg.Curve...)
	status := &fanStatus{Speed: f.speed, Error: f.lastErr, Script: f.cfg.Script, ScriptOK: scriptReady(f.cfg.Script)}
	if f.sample != nil {
		copy := *f.sample
		status.Sample = &copy
	}
	return fanConfigDTO{Enabled: f.cfg.Enabled, Interval: int(f.cfg.Interval / time.Second), Curve: curve, Status: status}
}

func scriptReady(path string) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func (f *FanController) Set(dto fanConfigDTO) error {
	f.setMu.Lock()
	defer f.setMu.Unlock()
	if dto.Interval < 2 || dto.Interval > 60 {
		return errors.New("采样间隔必须在 2–60 秒")
	}
	if err := validateFanCurve(dto.Curve); err != nil {
		return err
	}
	if err := writeFanUCI(dto); err != nil {
		return err
	}
	f.sampleMu.Lock()
	f.mu.Lock()
	f.cfg.Enabled = dto.Enabled
	f.cfg.Interval = time.Duration(dto.Interval) * time.Second
	f.cfg.Curve = append([]FanPoint(nil), dto.Curve...)
	f.sample = nil // Force one fresh sample and immediate curve application.
	f.speed = nil  // Reapply after a curve change.
	f.mu.Unlock()
	f.sampleMu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
	return nil
}

func writeFanUCI(dto fanConfigDTO) error {
	curve, err := json.Marshal(dto.Curve)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	values := [][2]string{{"fan_enabled", "0"}, {"fan_interval", strconv.Itoa(dto.Interval)}, {"fan_curve", string(curve)}}
	if dto.Enabled {
		values[0][1] = "1"
	}
	for _, item := range values {
		if out, err := exec.CommandContext(ctx, "uci", "set", "at-webserver.config."+item[0]+"="+item[1]).CombinedOutput(); err != nil {
			return fmt.Errorf("保存风扇配置失败: %s: %w", strings.TrimSpace(string(out)), err)
		}
	}
	if out, err := exec.CommandContext(ctx, "uci", "commit", "at-webserver").CombinedOutput(); err != nil {
		return fmt.Errorf("提交风扇配置失败: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (f *FanController) Run(ctx context.Context) {
	for {
		f.mu.RLock()
		cfg := f.cfg
		f.mu.RUnlock()
		if cfg.Enabled {
			if _, err := f.Sample(ctx); err != nil {
				f.log.Warnf("模块温度采样失败: %v", err)
			}
		}
		timer := time.NewTimer(cfg.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-f.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (f *FanController) Sample(ctx context.Context) (fanSample, error) {
	f.sampleMu.Lock()
	defer f.sampleMu.Unlock()
	f.mu.RLock()
	cfg, cached := f.cfg, f.sample
	f.mu.RUnlock()
	if cached != nil && time.Since(cached.SampledAt) < cfg.Interval {
		return *cached, nil
	}
	if f.client.LongCommandActive() {
		err := errors.New("模组正在执行长命令，温度暂不可用")
		f.mu.Lock()
		f.lastErr = err.Error()
		f.mu.Unlock()
		f.apply(cfg, math.NaN())
		return fanSample{}, err
	}
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	response, err := f.client.SendCommand(queryCtx, chipTempCommand)
	if err == nil {
		var sample fanSample
		sample, err = parseFanSample(response.Text(), time.Now())
		if err == nil {
			f.mu.Lock()
			f.sample = &sample
			f.lastErr = ""
			f.mu.Unlock()
			f.apply(cfg, sample.Temperature)
			return sample, nil
		}
	}
	f.mu.Lock()
	f.lastErr = err.Error()
	f.mu.Unlock()
	f.apply(cfg, math.NaN()) // Sensor failure fails safe to full speed.
	return fanSample{}, err
}

func (f *FanController) apply(cfg FanConfig, temperature float64) {
	if cfg.Script == "" {
		return
	}
	speed := 100
	if !math.IsNaN(temperature) {
		speed = fanSpeed(cfg.Curve, temperature)
	}
	f.mu.RLock()
	previous := f.speed
	f.mu.RUnlock()
	if previous != nil && *previous == speed {
		return
	}
	if !scriptReady(cfg.Script) {
		f.mu.Lock()
		f.lastErr = "风扇脚本不存在或不是普通文件: " + cfg.Script
		f.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/sh", cfg.Script, strconv.Itoa(speed), fmt.Sprintf("%.1f", temperature))
	command.Env = append(os.Environ(), "FAN_PERCENT="+strconv.Itoa(speed), fmt.Sprintf("FAN_TEMP_C=%.1f", temperature))
	if out, err := command.CombinedOutput(); err != nil {
		f.mu.Lock()
		f.lastErr = fmt.Sprintf("风扇脚本失败: %v: %s", err, strings.TrimSpace(string(out)))
		f.mu.Unlock()
		return
	}
	f.mu.Lock()
	f.speed = &speed
	f.mu.Unlock()
}

func (f *FanController) TemperatureResponse() *atCommandResponse {
	f.mu.RLock()
	enabled := f.cfg.Enabled
	f.mu.RUnlock()
	if !enabled {
		return nil
	}
	sample, err := f.Sample(context.Background())
	if err != nil {
		return errResponse(err.Error())
	}
	return &atCommandResponse{Success: true, Data: ptr(sample.Raw)}
}

func (s *WSServer) handleFanCommand(command string) *atCommandResponse {
	trimmed := strings.TrimSpace(command)
	if trimmed == chipTempCommand && s.fan != nil {
		return s.fan.TemperatureResponse()
	}
	if trimmed == fanQueryCommand {
		if s.fan == nil {
			return errResponse("当前构建未启用风扇控制")
		}
		payload, err := json.Marshal(s.fan.Config())
		if err != nil {
			return errResponse(err.Error())
		}
		return &atCommandResponse{Success: true, Data: ptr(fanResponsePrefix + string(payload) + "\r\nOK")}
	}
	if strings.HasPrefix(trimmed, fanSetPrefix) {
		if s.fan == nil {
			return errResponse("当前构建未启用风扇控制")
		}
		var dto fanConfigDTO
		if err := json.Unmarshal([]byte(strings.TrimPrefix(trimmed, fanSetPrefix)), &dto); err != nil {
			return errResponse("无效的风扇配置: " + err.Error())
		}
		if err := s.fan.Set(dto); err != nil {
			return errResponse(err.Error())
		}
		return &atCommandResponse{Success: true, Data: ptr(fanResponsePrefix + "OK\r\nOK")}
	}
	return nil
}
