import { ATService } from '@/services/at';

export type FanPoint = { temperature: number; speed: number };
export type FanSample = {
  raw: string;
  sensors: number[];
  temperature: number;
  sampled_at: string;
};
export type FanStatus = {
  sample?: FanSample;
  speed?: number;
  error?: string;
  script: string;
  script_ok: boolean;
};
export type FanConfig = {
  enabled: boolean;
  interval: number;
  curve: FanPoint[];
  status?: FanStatus;
};

export async function fetchFan(): Promise<FanConfig | null> {
  const res = await ATService.getInstance().sendCommand('AT+FAN?');
  if (!res.success || !res.data) return null;
  const payload = String(res.data).match(/\+FAN:\s*(\{[^\r\n]*\})/)?.[1];
  if (!payload) return null;
  try {
    return JSON.parse(payload) as FanConfig;
  } catch {
    return null;
  }
}

export async function saveFan(config: FanConfig): Promise<void> {
  const { status, ...payload } = config;
  void status;
  const res = await ATService.getInstance().sendCommand('AT+FAN=' + JSON.stringify(payload));
  if (!res.success) throw new Error(String(res.error || '保存风扇配置失败'));
}
