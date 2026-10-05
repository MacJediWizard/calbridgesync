import type { CalendarConfig } from '../types';

export interface StripAlarmsScope {
  // At least one calendar syncs one-way, so "Ignore alarms" takes effect.
  applies: boolean;
  // Some, but not all, calendars sync one-way.
  partial: boolean;
}

// The backend applies "Ignore alarms" per calendar, only where the
// effective direction is one-way. The effective direction is the
// calendar's override, falling back to the source default. Keep this in
// sync with getSyncDirectionForCalendar in internal/caldav/sync.go. (#217)
export function stripAlarmsScope(
  sourceDirection: 'one_way' | 'two_way',
  selectedCalendars: CalendarConfig[],
): StripAlarmsScope {
  const directions = selectedCalendars.length > 0
    ? selectedCalendars.map(c => c.sync_direction || sourceDirection)
    : [sourceDirection];
  const oneWay = directions.filter(d => d === 'one_way').length;
  return { applies: oneWay > 0, partial: oneWay > 0 && oneWay < directions.length };
}
