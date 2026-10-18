export const defaultQuotaTimezone = 'Asia/Shanghai'
export const calendarDaySeconds = 86_400

export interface QuotaPeriod {
  period_seconds?: number
  period_mode?: 'calendar_day'
  timezone?: string
}

export function quotaPeriodIdentity(rule: QuotaPeriod): string {
  return `${rule.period_mode ?? ''}:${rule.period_seconds ?? 0}`
}

export function quotaPeriodFields(rule: QuotaPeriod): QuotaPeriod {
  return {
    period_seconds: rule.period_seconds,
    ...(rule.period_mode === 'calendar_day'
      ? { period_mode: 'calendar_day', timezone: rule.timezone ?? defaultQuotaTimezone }
      : {}),
  }
}

export function isQuotaTimezone(value: unknown): value is string {
  if (typeof value !== 'string' || !value || value === 'Local' || value.length > 64) return false
  try {
    new Intl.DateTimeFormat('en', { timeZone: value })
    return true
  } catch {
    return false
  }
}

export function isQuotaPeriodValid(rule: QuotaPeriod): boolean {
  const seconds = rule.period_seconds
  if (!Number.isSafeInteger(seconds) || seconds! < 60 || seconds! > 31_536_000) return false
  if (rule.period_mode === 'calendar_day') {
    return seconds === calendarDaySeconds && isQuotaTimezone(rule.timezone ?? defaultQuotaTimezone)
  }
  return !rule.period_mode && !rule.timezone
}
