import { useState, useEffect, useCallback } from 'react';
import { Link } from 'react-router-dom';
import { getActivity } from '../services/api';
import type { ActivityData, SyncActivity } from '../types';

// Spinner component for sync indicators
const Spinner = ({ className = '' }: { className?: string }) => (
  <svg className={`animate-spin ${className}`} xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24">
    <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4"></circle>
    <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path>
  </svg>
);

// Progress bar component
const ProgressBar = ({ current, total }: { current: number; total: number }) => {
  const percentage = total > 0 ? Math.round((current / total) * 100) : 0;
  return (
    <div className="w-full">
      <div className="flex justify-between text-xs text-gray-400 mb-1">
        <span>Calendars: {current} / {total}</span>
        <span>{percentage}%</span>
      </div>
      <div className="w-full bg-zinc-700 rounded-full h-2">
        <div
          className="bg-blue-500 h-2 rounded-full transition-all duration-500"
          style={{ width: `${percentage}%` }}
        />
      </div>
    </div>
  );
};

// Status badge component
const StatusBadge = ({ status }: { status: SyncActivity['status'] }) => {
  const config = {
    running: { bg: 'bg-blue-900/50', text: 'text-blue-400', label: 'Running' },
    completed: { bg: 'bg-green-900/50', text: 'text-green-400', label: 'Completed' },
    partial: { bg: 'bg-yellow-900/50', text: 'text-yellow-400', label: 'Partial' },
    error: { bg: 'bg-red-900/50', text: 'text-red-400', label: 'Error' },
  };
  const { bg, text, label } = config[status] || config.error;

  return (
    <span className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-medium ${bg} ${text}`}>
      {status === 'running' && <Spinner className="h-3 w-3 mr-1" />}
      {label}
    </span>
  );
};

// Format relative time
const formatRelativeTime = (dateStr: string): string => {
  const date = new Date(dateStr);
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffSecs = Math.floor(diffMs / 1000);
  const diffMins = Math.floor(diffSecs / 60);
  const diffHours = Math.floor(diffMins / 60);

  if (diffSecs < 60) return `${diffSecs}s ago`;
  if (diffMins < 60) return `${diffMins}m ago`;
  if (diffHours < 24) return `${diffHours}h ago`;
  return date.toLocaleDateString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
};

// Active sync card
const ActiveSyncCard = ({ activity }: { activity: SyncActivity }) => (
  <div className="bg-zinc-900 rounded-lg border border-blue-500/50 p-4">
    <div className="flex items-start justify-between mb-3">
      <div>
        <h3 className="font-medium text-white flex items-center">
          <Spinner className="h-4 w-4 mr-2 text-blue-400" />
          {activity.source_name}
        </h3>
        <p className="text-xs text-gray-400 mt-1">
          Started {formatRelativeTime(activity.started_at)}
          {activity.duration && ` - ${activity.duration}`}
        </p>
      </div>
      <StatusBadge status={activity.status} />
    </div>

    {activity.current_calendar && (
      <p className="text-sm text-blue-400 mb-3">
        Syncing: {activity.current_calendar}
      </p>
    )}

    <ProgressBar current={activity.calendars_synced} total={activity.total_calendars} />

    <div className="mt-4 grid grid-cols-4 gap-2 text-center">
      <div>
        <p className="text-lg font-semibold text-blue-400">{activity.events_created}</p>
        <p className="text-xs text-gray-500">Created</p>
      </div>
      <div>
        <p className="text-lg font-semibold text-purple-400">{activity.events_updated}</p>
        <p className="text-xs text-gray-500">Updated</p>
      </div>
      <div>
        <p className="text-lg font-semibold text-orange-400">{activity.events_deleted}</p>
        <p className="text-xs text-gray-500">Deleted</p>
      </div>
      <div>
        <p className="text-lg font-semibold text-gray-400">{activity.events_skipped}</p>
        <p className="text-xs text-gray-500">Skipped</p>
      </div>
    </div>
  </div>
);

// Recent sync card
const RecentSyncCard = ({ activity }: { activity: SyncActivity }) => (
  <div className="bg-zinc-900 rounded-lg border border-zinc-800 p-4">
    <div className="flex items-start justify-between mb-3">
      <div>
        <h3 className="font-medium text-white">{activity.source_name}</h3>
        <p className="text-xs text-gray-400 mt-1">
          {activity.completed_at ? formatRelativeTime(activity.completed_at) : formatRelativeTime(activity.started_at)}
          {activity.duration && ` - Duration: ${activity.duration}`}
        </p>
      </div>
      <StatusBadge status={activity.status} />
    </div>

    {activity.message && (
      <p className={`text-sm mb-3 ${activity.status === 'error' ? 'text-red-400' : 'text-gray-400'}`}>
        {activity.message}
      </p>
    )}

    {activity.errors && activity.errors.length > 0 && (
      <div className="mb-3 p-2 bg-red-900/20 rounded border border-red-900/50">
        <p className="text-xs text-red-400 font-medium mb-1">Errors ({activity.errors.length}):</p>
        <ul className="text-xs text-red-400/80 space-y-1">
          {activity.errors.slice(0, 3).map((err, i) => (
            <li key={i} className="truncate">{err}</li>
          ))}
          {activity.errors.length > 3 && (
            <li className="text-red-400/60">...and {activity.errors.length - 3} more</li>
          )}
        </ul>
      </div>
    )}

    <div className="grid grid-cols-5 gap-2 text-center">
      <div>
        <p className="text-sm font-semibold text-white">{activity.calendars_synced}/{activity.total_calendars}</p>
        <p className="text-xs text-gray-500">Calendars</p>
      </div>
      <div>
        <p className="text-sm font-semibold text-blue-400">{activity.events_created}</p>
        <p className="text-xs text-gray-500">Created</p>
      </div>
      <div>
        <p className="text-sm font-semibold text-purple-400">{activity.events_updated}</p>
        <p className="text-xs text-gray-500">Updated</p>
      </div>
      <div>
        <p className="text-sm font-semibold text-orange-400">{activity.events_deleted}</p>
        <p className="text-xs text-gray-500">Deleted</p>
      </div>
      <div>
        <p className="text-sm font-semibold text-gray-400">{activity.events_skipped}</p>
        <p className="text-xs text-gray-500">Skipped</p>
      </div>
    </div>
  </div>
);

export default function Activity() {
  const [activity, setActivity] = useState<ActivityData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const hasActiveSyncs = activity && activity.active.length > 0;

  const loadActivity = useCallback(async (showLoading = false) => {
    if (showLoading) setLoading(true);
    try {
      const data = await getActivity();
      setActivity(data);
      setError(null);
    } catch (err) {
      setError('Failed to load activity data');
      console.error(err);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    loadActivity(true);
  }, [loadActivity]);

  // Auto-refresh when there are active syncs or every 10 seconds otherwise
  useEffect(() => {
    const interval = setInterval(() => {
      loadActivity(false);
    }, hasActiveSyncs ? 2000 : 10000);

    return () => clearInterval(interval);
  }, [hasActiveSyncs, loadActivity]);

  if (loading) {
    return <div className="text-center py-12 text-gray-400">Loading...</div>;
  }

  if (error) {
    return <div className="text-center py-12 text-red-400">{error}</div>;
  }

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
        <div>
          <h1 className="text-2xl font-bold text-white flex items-center">
            Activity
            {hasActiveSyncs && (
              <span className="ml-3 flex items-center text-sm font-normal text-blue-400">
                <Spinner className="h-4 w-4 mr-1" />
                {activity.active.length} sync{activity.active.length > 1 ? 's' : ''} in progress
              </span>
            )}
          </h1>
          <p className="text-sm text-gray-400">Real-time sync activity and history</p>
        </div>
        <Link
          to="/"
          className="inline-flex items-center px-4 py-2 rounded bg-zinc-800 hover:bg-zinc-700 text-white text-sm font-medium transition-colors"
        >
          Back to Dashboard
        </Link>
      </div>

      {/* Active Syncs */}
      {hasActiveSyncs && (
        <div>
          <div className="flex items-center space-x-2 mb-4">
            <Spinner className="h-5 w-5 text-blue-400" />
            <h2 className="text-lg font-semibold text-white">Active Syncs</h2>
            <span className="text-xs text-gray-400">Auto-refreshing every 2s</span>
          </div>
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
            {activity.active.map((act) => (
              <ActiveSyncCard key={act.source_id} activity={act} />
            ))}
          </div>
        </div>
      )}

      {/* No Active Syncs Message */}
      {!hasActiveSyncs && (
        <div className="bg-zinc-900 rounded-lg border border-zinc-800 p-8 text-center">
          <div className="text-gray-500 mb-2">
            <svg className="h-12 w-12 mx-auto" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={1.5} d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z" />
            </svg>
          </div>
          <h3 className="text-lg font-medium text-white">No Active Syncs</h3>
          <p className="text-sm text-gray-400 mt-1">All sources are idle. Trigger a sync from the dashboard to see real-time progress.</p>
        </div>
      )}

      {/* Recent Syncs */}
      {activity && activity.recent.length > 0 && (
        <div>
          <h2 className="text-lg font-semibold text-white mb-4">Recent Syncs</h2>
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
            {activity.recent.map((act, index) => (
              <RecentSyncCard key={`${act.source_id}-${index}`} activity={act} />
            ))}
          </div>
        </div>
      )}

      {/* No Recent Syncs */}
      {activity && activity.recent.length === 0 && !hasActiveSyncs && (
        <div className="text-center py-8 text-gray-400">
          <p>No sync activity recorded yet.</p>
        </div>
      )}
    </div>
  );
}
