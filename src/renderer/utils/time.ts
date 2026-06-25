const CHINA_TIME_ZONE = 'Asia/Shanghai';

function parseTimestamp(value?: string | null) {
  if (!value) return null;
  const raw = String(value).trim();
  if (!raw) return null;

  if (/^\d{2}:\d{2}:\d{2}$/.test(raw)) {
    return new Date(`${new Date().toISOString().slice(0, 10)}T${raw}Z`);
  }

  let normalized = raw.includes(' ') ? raw.replace(' ', 'T') : raw;
  if (!/[zZ]$|[+-]\d{2}:?\d{2}$/.test(normalized)) normalized += 'Z';

  const date = new Date(normalized);
  return Number.isNaN(date.getTime()) ? null : date;
}

function normalizeDurationMs(value?: number | null) {
  if (value === null || typeof value === 'undefined') return null;
  const duration = Number(value);
  if (!Number.isFinite(duration) || duration < 0) return null;
  return duration;
}

export function getTimestampMs(value?: string | null) {
  const date = parseTimestamp(value);
  return date?.getTime() ?? 0;
}

function chinaParts(date: Date) {
  const parts = new Intl.DateTimeFormat('zh-CN', {
    timeZone: CHINA_TIME_ZONE,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  }).formatToParts(date);

  return Object.fromEntries(parts.map((part) => [part.type, part.value]));
}

export function formatChinaTime(value?: string | null) {
  const date = parseTimestamp(value);
  if (!date) return value || '';
  const parts = chinaParts(date);
  return `${parts.hour}:${parts.minute}:${parts.second}`;
}

export function formatChinaDateTime(value?: string | null) {
  const date = parseTimestamp(value);
  if (!date) return value || '';
  const parts = chinaParts(date);
  return `${parts.year}-${parts.month}-${parts.day} ${parts.hour}:${parts.minute}:${parts.second}`;
}

export function formatDuration(milliseconds?: number | null, fallback = '未开始') {
  const duration = normalizeDurationMs(milliseconds);
  if (duration === null) return fallback;

  const totalSeconds = duration > 0 ? Math.max(1, Math.round(duration / 1000)) : 0;
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;

  if (hours > 0) {
    return minutes > 0 ? `${hours}小时${minutes}分` : `${hours}小时`;
  }

  if (minutes > 0) {
    return seconds > 0 ? `${minutes}分${seconds}秒` : `${minutes}分`;
  }

  return `${seconds}秒`;
}

export function getRunningDurationMs(durationMs?: number | null, startedAt?: string | null, now = Date.now()) {
  const baseDuration = normalizeDurationMs(durationMs) ?? 0;
  const startedDate = parseTimestamp(startedAt);
  if (!startedDate) return normalizeDurationMs(durationMs);

  return baseDuration + Math.max(0, now - startedDate.getTime());
}
