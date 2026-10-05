import { useEffect, useMemo, useState } from 'react';
import { getSyncLimits } from '../services/api';

const PRESETS = [300, 900, 1800, 3600, 7200, 21600, 86400];

function formatSeconds(s: number): string {
  if (s < 60) return `${s} sec`;
  if (s < 3600) return `${Math.round(s / 60)} min`;
  const h = s / 3600;
  return `${Number.isInteger(h) ? h : h.toFixed(1)} hour${h === 1 ? '' : 's'}`;
}

interface Props {
  value: number;
  onChange: (value: number) => void;
}

// Interval picker limited to the server's [MIN_SYNC_INTERVAL,
// MAX_SYNC_INTERVAL], so it never offers a value the API rejects.
export default function SyncIntervalSelect({ value, onChange }: Props) {
  const [limits, setLimits] = useState<{ min: number; max: number } | null>(null);

  useEffect(() => {
    getSyncLimits()
      .then(l => setLimits({ min: l.min_sync_interval, max: l.max_sync_interval }))
      .catch(err => console.error('Failed to load sync interval limits', err));
  }, []);

  const options = useMemo(
    () => (limits ? PRESETS.filter(p => p >= limits.min && p <= limits.max) : PRESETS),
    [limits],
  );

  // If the current value is outside the bounds, move it to the nearest
  // allowed preset (or bound) so the form submits what the select shows.
  useEffect(() => {
    if (!limits || (value >= limits.min && value <= limits.max)) return;
    const allowed = options.length > 0 ? options : [limits.min];
    const nearest = allowed.reduce((a, b) => (Math.abs(b - value) < Math.abs(a - value) ? b : a));
    onChange(nearest);
  }, [limits, value, options, onChange]);

  // Show an in-range stored value that isn't a preset rather than
  // silently displaying a different option.
  const shown = options.includes(value) ? options : [...options, value].sort((a, b) => a - b);

  return (
    <select
      name="sync_interval"
      id="sync_interval"
      value={value}
      onChange={e => onChange(parseInt(e.target.value, 10))}
      required
      className="w-full"
    >
      {shown.map(s => (
        <option key={s} value={s}>
          {formatSeconds(s)}
        </option>
      ))}
    </select>
  );
}
