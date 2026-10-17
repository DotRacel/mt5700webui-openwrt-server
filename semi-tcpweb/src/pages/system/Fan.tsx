import React, { useEffect, useRef, useState } from 'react';
import { Banner, Button, InputNumber, Space, Spin, Switch, Toast, Typography } from '@douyinfe/semi-ui';
import { useATReady } from '@/hooks/useATReady';
import { fetchFan, saveFan, type FanConfig, type FanPoint } from '@/modem/fan';
import { PageCard, SectionHeader } from '@/ui/widgets';

const WIDTH = 720;
const HEIGHT = 320;
const LEFT = 54;
const RIGHT = 22;
const TOP = 20;
const BOTTOM = 42;
const x = (temperature: number) => LEFT + (temperature / 120) * (WIDTH - LEFT - RIGHT);
const y = (speed: number) => TOP + ((100 - speed) / 100) * (HEIGHT - TOP - BOTTOM);
const clamp = (value: number, low: number, high: number) => Math.max(low, Math.min(high, value));

const FanCurve: React.FC<{ points: FanPoint[]; current?: number; onChange: (points: FanPoint[]) => void }> = ({ points, current, onChange }) => {
  const svg = useRef<SVGSVGElement>(null);
  const dragging = useRef<number | null>(null);

  const move = (index: number, temperature: number, speed: number) => {
    const previous = points[index - 1];
    const next = points[index + 1];
    const copy = points.map((p) => ({ ...p }));
    copy[index] = {
      temperature: clamp(Math.round(temperature), previous ? previous.temperature + 1 : 0, next ? next.temperature - 1 : 120),
      speed: clamp(Math.round(speed), previous ? previous.speed : 0, next ? next.speed : 100),
    };
    onChange(copy);
  };

  const handleMove = (event: React.PointerEvent<SVGSVGElement>) => {
    const index = dragging.current;
    if (index === null || !svg.current) return;
    const matrix = svg.current.getScreenCTM();
    if (!matrix) return;
    const point = new DOMPoint(event.clientX, event.clientY).matrixTransform(matrix.inverse());
    move(index, ((point.x - LEFT) / (WIDTH - LEFT - RIGHT)) * 120, 100 - ((point.y - TOP) / (HEIGHT - TOP - BOTTOM)) * 100);
  };

  const line = [{ temperature: 0, speed: points[0].speed }, ...points, { temperature: 120, speed: points[points.length - 1].speed }]
    .map((p) => `${x(p.temperature)},${y(p.speed)}`).join(' ');
  const liveSpeed = (() => {
    if (current == null) return null;
    if (current <= points[0].temperature) return points[0].speed;
    for (let index = 1; index < points.length; index++) {
      const point = points[index];
      if (current <= point.temperature) {
        const prev = points[index - 1];
        return Math.round(prev.speed + ((current - prev.temperature) / (point.temperature - prev.temperature)) * (point.speed - prev.speed));
      }
    }
    return points[points.length - 1].speed;
  })();

  return (
    <div className="fan-curve-wrap">
      <svg ref={svg} className="fan-curve" viewBox={`0 0 ${WIDTH} ${HEIGHT}`} role="img" aria-label="风扇温度调速曲线" onPointerMove={handleMove} onPointerUp={() => { dragging.current = null; }} onPointerCancel={() => { dragging.current = null; }}>
        {[0, 25, 50, 75, 100].map((speed) => <g key={speed}><line x1={LEFT} y1={y(speed)} x2={WIDTH - RIGHT} y2={y(speed)} className="fan-grid-line" /><text x={LEFT - 9} y={y(speed) + 4} textAnchor="end">{speed}%</text></g>)}
        {[0, 30, 60, 90, 120].map((temperature) => <g key={temperature}><line x1={x(temperature)} y1={TOP} x2={x(temperature)} y2={HEIGHT - BOTTOM} className="fan-grid-line" /><text x={x(temperature)} y={HEIGHT - 13} textAnchor="middle">{temperature}°</text></g>)}
        <polyline points={line} fill="none" className="fan-curve-line" />
        {current != null && liveSpeed != null && <g><line x1={x(current)} y1={TOP} x2={x(current)} y2={HEIGHT - BOTTOM} className="fan-current-line" /><circle cx={x(current)} cy={y(liveSpeed)} r="7" className="fan-current-point" /></g>}
        {points.map((p, index) => <circle key={index} cx={x(p.temperature)} cy={y(p.speed)} r="11" className="fan-node" tabIndex={0} role="slider" aria-label={`节点 ${index + 1}，${p.temperature}°C，${p.speed}%`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={p.speed} onPointerDown={(event) => { dragging.current = index; event.currentTarget.setPointerCapture(event.pointerId); }} onKeyDown={(event) => { if (event.key === 'ArrowUp' || event.key === 'ArrowDown') { event.preventDefault(); move(index, p.temperature, p.speed + (event.key === 'ArrowUp' ? 1 : -1)); } else if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') { event.preventDefault(); move(index, p.temperature + (event.key === 'ArrowRight' ? 1 : -1), p.speed); } }} />)}
      </svg>
      <Typography.Text type="tertiary" size="small">拖动节点调整曲线；也可使用方向键或下方输入框精确设置。节点之间按直线插值。</Typography.Text>
    </div>
  );
};

const FanPage: React.FC = () => {
  const [config, setConfig] = useState<FanConfig | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);

  const reload = async () => {
    try {
      const next = await fetchFan();
      setConfig(next);
    } finally {
      setLoading(false);
    }
  };
  useATReady(() => { void reload(); });
  useEffect(() => {
    const timer = window.setInterval(() => { void fetchFan().then((next) => { if (next) setConfig((prev) => prev ? { ...prev, status: next.status } : next); }); }, 5000);
    return () => window.clearInterval(timer);
  }, []);

  const updatePoint = (index: number, field: keyof FanPoint, value: number) => {
    if (!config) return;
    const curve = config.curve.map((point) => ({ ...point }));
    curve[index][field] = value;
    setConfig({ ...config, curve });
  };
  const save = async () => {
    if (!config) return;
    setSaving(true);
    try {
      await saveFan(config);
      Toast.success('风扇曲线已保存');
      await reload();
    } catch (error) {
      Toast.error(error instanceof Error ? error.message : '保存失败');
    } finally {
      setSaving(false);
    }
  };

  if (loading) return <Spin />;
  if (!config) return <Banner type="warning" description="当前服务不支持风扇控制，请升级 at-webserver。" />;
  const sample = config.status?.sample;
  return <div className="page-stack">
    <SectionHeader title="风扇控制" desc="由 at-webserver 持续读取模组温度，并按温度曲线调用本机 Shell 脚本" />
    <PageCard title="实时状态" extra={<Button size="small" onClick={() => void reload()}>刷新</Button>}>
      <div className="fan-status">
        <div><strong>{sample ? `${sample.temperature.toFixed(1)}°C` : '—'}</strong><span>最高模组温度</span></div>
        <div><strong>{config.status?.speed == null ? '—' : `${config.status.speed}%`}</strong><span>最近执行的转速</span></div>
        <div><strong>{sample ? new Date(sample.sampled_at).toLocaleTimeString() : '—'}</strong><span>采样时间</span></div>
      </div>
      {config.status?.error && <Banner type="warning" description={config.status.error} />}
    </PageCard>
    <PageCard title="温度调速曲线" hint="最高传感器温度决定目标转速">
      <Space className="fan-controls">
        <Switch checked={config.enabled} onChange={(enabled) => setConfig({ ...config, enabled })} aria-label="启用风扇控制" />
        <Typography.Text>启用持续温度监测</Typography.Text>
        <Typography.Text>采样间隔</Typography.Text>
        <InputNumber min={2} max={60} value={config.interval} onChange={(value) => setConfig({ ...config, interval: Number(value) || 5 })} style={{ width: 84 }} />
        <Typography.Text>秒</Typography.Text>
      </Space>
      <FanCurve points={config.curve} current={sample?.temperature} onChange={(curve) => setConfig({ ...config, curve })} />
      <div className="fan-point-list">
        {config.curve.map((point, index) => <div className="fan-point-row" key={index}>
          <span>节点 {index + 1}</span>
          <InputNumber min={0} max={120} value={point.temperature} onChange={(value) => updatePoint(index, 'temperature', Number(value))} suffix="°C" />
          <InputNumber min={0} max={100} value={point.speed} onChange={(value) => updatePoint(index, 'speed', Number(value))} suffix="%" />
          <Button disabled={config.curve.length <= 2} onClick={() => setConfig({ ...config, curve: config.curve.filter((_, i) => i !== index) })}>删除</Button>
        </div>)}
      </div>
      <Space className="fan-actions">
        <Button disabled={config.curve.length >= 12} onClick={() => {
          const curve = [...config.curve];
          const index = curve.findIndex((point, i) => i > 0 && point.temperature - curve[i - 1].temperature >= 2);
          if (index < 0) return Toast.warning('没有足够的温度间隔插入节点');
          const a = curve[index - 1], b = curve[index];
          curve.splice(index, 0, { temperature: Math.round((a.temperature + b.temperature) / 2), speed: Math.round((a.speed + b.speed) / 2) });
          setConfig({ ...config, curve });
        }}>添加节点</Button>
        <Button theme="solid" type="primary" loading={saving} onClick={() => void save()}>保存并应用</Button>
      </Space>
    </PageCard>
    <PageCard title="Shell 调速脚本" hint="脚本由路由器本机执行，不接触模块串口">
      <Typography.Paragraph>在 LuCI 的 AT WebServer 设置中填写脚本绝对路径。脚本以 <code>/bin/sh 脚本路径 转速百分比 温度°C</code> 调用，同时收到 <code>FAN_PERCENT</code> 和 <code>FAN_TEMP_C</code> 环境变量。温度读取失败时传入 100% 转速。</Typography.Paragraph>
      <Typography.Text>当前路径：{config.status?.script || '未配置'}；{config.status?.script_ok ? '脚本已就绪' : '脚本未就绪'}</Typography.Text>
    </PageCard>
  </div>;
};

export default FanPage;
