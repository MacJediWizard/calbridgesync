import { useState, useEffect, useCallback } from 'react';
import { Link } from 'react-router-dom';
import { getDashboardStats, getSources, triggerSync, getSyncHistory, getMalformedEvents, deleteMalformedEvent, deleteAllMalformedEvents } from '../services/api';
import type { DashboardStats, Source, SyncHistory, MalformedEvent } from '../types';
import {
  AreaChart,
  Area,
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  Legend,
} from 'recharts';

// Spinner component for sync indicators
const Spinner = ({ className = '' }: { className?: string }) => (
  <svg className={`animate-spin ${className}`} xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24">
    <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4"></circle>
    <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path>
  </svg>
);

export default function Dashboard() {
  const [stats, setStats] = useState<DashboardStats | null>(null);
  const [sources, setSources] = useState<Source[]>([]);
  const [syncHistory, setSyncHistory] = useState<SyncHistory | null>(null);
  const [malformedEvents, setMalformedEvents] = useState<MalformedEvent[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [syncingId, setSyncingId] = useState<string | null>(null);
  const [deletingEventId, setDeletingEventId] = useState<string | null>(null);
  const [deletingAll, setDeletingAll] = useState(false);
  const [refreshCountdown, setRefreshCountdown] = useState(0);

  // Check if any source is currently syncing
  const syncingSources = sources.filter(s => s.sync_status === 'running');
  const isAnySyncing = syncingSources.length > 0 || syncingId !== null;

  // Continue refreshing for a bit after sync stops to catch final status update
  const shouldAutoRefresh = isAnySyncing || refreshCountdown > 0;

  const loadData = useCallback(async (showLoading = false) => {
    if (showLoading) setLoading(true);
    try {
      const [statsData, sourcesData, historyData, malformedData] = await Promise.all([
        getDashboardStats(),
        getSources(),
        getSyncHistory(7),
        getMalformedEvents(),
      ]);
      setStats(statsData);
      setSources(sourcesData);
      setSyncHistory(historyData);
      setMalformedEvents(malformedData);
    } catch (err) {
      setError('Failed to load dashboard data');
      console.error(err);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    loadData(true);
  }, [loadData]);

  // Start countdown when syncing stops to catch final status update
  useEffect(() => {
    if (isAnySyncing) {
      setRefreshCountdown(5); // Will do 5 more refreshes after sync appears complete
    }
  }, [isAnySyncing]);

  // Auto-refresh when syncing is active or during countdown
  useEffect(() => {
    if (!shouldAutoRefresh) return;

    const interval = setInterval(() => {
      loadData(false);
      if (!isAnySyncing && refreshCountdown > 0) {
        setRefreshCountdown(prev => prev - 1);
      }
    }, 3000); // Refresh every 3 seconds

    return () => clearInterval(interval);
  }, [shouldAutoRefresh, isAnySyncing, refreshCountdown, loadData]);

  const handleSync = async (id: string) => {
    setSyncingId(id);
    try {
      await triggerSync(id);
      // Don't clear syncingId immediately - let auto-refresh detect when done
      await loadData(false);
    } catch (err) {
      console.error('Sync failed:', err);
      setSyncingId(null);
    }
  };

  // Clear syncingId when the source is no longer running
  useEffect(() => {
    if (syncingId) {
      const source = sources.find(s => s.id === syncingId);
      if (source && source.sync_status !== 'running') {
        setSyncingId(null);
      }
    }
  }, [sources, syncingId]);

  const handleDeleteMalformedEvent = async (id: string) => {
    if (!confirm('Delete this corrupted event from the source calendar?')) return;
    setDeletingEventId(id);
    try {
      await deleteMalformedEvent(id);
      setMalformedEvents((prev) => prev.filter((e) => e.id !== id));
    } catch (err) {
      console.error('Failed to delete malformed event:', err);
    } finally {
      setDeletingEventId(null);
    }
  };

  const handleDeleteAllMalformedEvents = async () => {
    if (!confirm(`Delete all ${malformedEvents.length} corrupted events? This will remove them from the database records only.`)) return;
    setDeletingAll(true);
    try {
      await deleteAllMalformedEvents();
      setMalformedEvents([]);
    } catch (err) {
      console.error('Failed to delete all malformed events:', err);
    } finally {
      setDeletingAll(false);
    }
  };

  if (loading) {
    return <div className="text-center py-12 text-gray-400">Loading...</div>;
  }

  if (error) {
    return <div className="text-center py-12 text-red-400">{error}</div>;
  }

  const formatDate = (dateStr: string | null) => {
    if (!dateStr) return 'Never';
    return new Date(dateStr).toLocaleDateString('en-US', {
      month: 'short',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
    });
  };

  return (
    <div className="space-y-6">
      {/* Sync In Progress Banner */}
      {shouldAutoRefresh && (
        <div className={`border rounded-lg px-4 py-3 flex items-center space-x-3 ${
          isAnySyncing
            ? 'bg-blue-900/30 border-blue-500/50 animate-pulse'
            : 'bg-green-900/30 border-green-500/50'
        }`}>
          <Spinner className={`h-5 w-5 ${isAnySyncing ? 'text-blue-400' : 'text-green-400'}`} />
          <div className="flex-1">
            <p className={`font-medium ${isAnySyncing ? 'text-blue-400' : 'text-green-400'}`}>
              {isAnySyncing ? (
                <>
                  Sync in progress
                  {syncingSources.length > 0 && (
                    <span className="text-blue-300 font-normal ml-2">
                      - {syncingSources.map(s => s.name).join(', ')}
                    </span>
                  )}
                </>
              ) : (
                'Updating status...'
              )}
            </p>
            <p className={`text-xs mt-0.5 ${isAnySyncing ? 'text-blue-400/70' : 'text-green-400/70'}`}>
              Auto-refreshing every 3 seconds...
            </p>
          </div>
        </div>
      )}

      {/* Header */}
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
        <div>
          <h1 className="text-2xl font-bold text-white">Dashboard</h1>
          <p className="text-sm text-gray-400">Monitor your calendar sync status</p>
        </div>
        <Link
          to="/sources/add"
          className="inline-flex items-center px-4 py-2 rounded bg-red-600 hover:bg-red-700 text-white text-sm font-medium transition-colors"
        >
          + Add Source
        </Link>
      </div>

      {/* Stats Grid */}
      {stats && (
        <div className="grid grid-cols-2 lg:grid-cols-5 gap-4">
          <div className="bg-zinc-900 rounded-lg p-4 border border-zinc-800">
            <p className="text-xs text-gray-400 uppercase tracking-wide">Total Sources</p>
            <p className="mt-1 text-2xl font-bold text-white">{stats.total_sources}</p>
          </div>
          <div className="bg-zinc-900 rounded-lg p-4 border border-zinc-800">
            <p className="text-xs text-gray-400 uppercase tracking-wide">Active</p>
            <p className="mt-1 text-2xl font-bold text-green-400">{stats.active_sources}</p>
          </div>
          <div className="bg-zinc-900 rounded-lg p-4 border border-zinc-800">
            <p className="text-xs text-gray-400 uppercase tracking-wide">Syncs Today</p>
            <p className="mt-1 text-2xl font-bold text-white">{stats.syncs_today}</p>
          </div>
          <div className="bg-zinc-900 rounded-lg p-4 border border-zinc-800">
            <p className="text-xs text-gray-400 uppercase tracking-wide">Failed</p>
            <p className={`mt-1 text-2xl font-bold ${stats.failed_syncs > 0 ? 'text-red-400' : 'text-gray-500'}`}>
              {stats.failed_syncs}
            </p>
          </div>
          <div className="bg-zinc-900 rounded-lg p-4 border border-zinc-800">
            <p className="text-xs text-gray-400 uppercase tracking-wide">Stale</p>
            <p className={`mt-1 text-2xl font-bold ${sources.filter(s => s.is_stale && s.enabled).length > 0 ? 'text-orange-400' : 'text-gray-500'}`}>
              {sources.filter(s => s.is_stale && s.enabled).length}
            </p>
          </div>
        </div>
      )}

      {/* Corrupted Events Alert */}
      {malformedEvents.length > 0 && (
        <div className="bg-zinc-900 rounded-lg border border-red-900/50 overflow-hidden">
          <div className="px-4 py-3 border-b border-red-900/50 bg-red-900/20 flex items-center justify-between">
            <div className="flex items-center space-x-2">
              <span className="text-red-400">!</span>
              <h2 className="text-sm font-semibold text-red-400">
                Corrupted Events ({malformedEvents.length})
              </h2>
              <span className="text-xs text-gray-400">- invalid iCal format, cannot sync</span>
            </div>
            <button
              onClick={handleDeleteAllMalformedEvents}
              disabled={deletingAll}
              className="px-3 py-1 rounded text-xs font-medium bg-red-900/30 text-red-400 hover:bg-red-900/50 disabled:opacity-50"
            >
              {deletingAll ? 'Deleting...' : 'Delete All'}
            </button>
          </div>
          <div className="divide-y divide-zinc-800">
            {malformedEvents.map((event) => (
              <div key={event.id} className="px-4 py-3 flex items-center justify-between hover:bg-zinc-800/50">
                <div className="flex-1 min-w-0">
                  <div className="flex items-center space-x-2">
                    <span className="text-xs font-medium text-gray-400">{event.source_name}</span>
                  </div>
                  <p className="text-sm text-white truncate mt-1" title={event.event_path}>
                    {event.event_path.split('/').pop()}
                  </p>
                  <p className="text-xs text-red-400/80 mt-1">{event.error_message}</p>
                </div>
                <button
                  onClick={() => handleDeleteMalformedEvent(event.id)}
                  disabled={deletingEventId === event.id}
                  className="ml-4 px-3 py-1 rounded text-xs font-medium bg-red-900/30 text-red-400 hover:bg-red-900/50 disabled:opacity-50"
                >
                  {deletingEventId === event.id ? '...' : 'Delete'}
                </button>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Charts Section */}
      {syncHistory && syncHistory.history.length > 0 && (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
          {/* Sync Status Chart */}
          <div className="bg-zinc-900 rounded-lg border border-zinc-800 p-4">
            <h3 className="text-sm font-semibold text-white mb-4">Sync Status (7 Days)</h3>
            <div className="h-48">
              <ResponsiveContainer width="100%" height="100%">
                <AreaChart data={syncHistory.history} margin={{ top: 5, right: 5, left: -20, bottom: 5 }}>
                  <CartesianGrid strokeDasharray="3 3" stroke="#333" />
                  <XAxis dataKey="date" tick={{ fill: '#888', fontSize: 11 }} axisLine={{ stroke: '#444' }} />
                  <YAxis tick={{ fill: '#888', fontSize: 11 }} axisLine={{ stroke: '#444' }} allowDecimals={false} />
                  <Tooltip
                    contentStyle={{ backgroundColor: '#18181b', border: '1px solid #333', borderRadius: '6px' }}
                    labelStyle={{ color: '#fff' }}
                  />
                  <Legend wrapperStyle={{ fontSize: '11px' }} />
                  <Area
                    type="monotone"
                    dataKey="success"
                    stackId="1"
                    stroke="#22c55e"
                    fill="#22c55e"
                    fillOpacity={0.6}
                    name="Success"
                  />
                  <Area
                    type="monotone"
                    dataKey="partial"
                    stackId="1"
                    stroke="#eab308"
                    fill="#eab308"
                    fillOpacity={0.6}
                    name="Partial"
                  />
                  <Area
                    type="monotone"
                    dataKey="error"
                    stackId="1"
                    stroke="#ef4444"
                    fill="#ef4444"
                    fillOpacity={0.6}
                    name="Error"
                  />
                </AreaChart>
              </ResponsiveContainer>
            </div>
          </div>

          {/* Events Chart */}
          <div className="bg-zinc-900 rounded-lg border border-zinc-800 p-4">
            <h3 className="text-sm font-semibold text-white mb-4">Events Activity (7 Days)</h3>
            <div className="h-48">
              <ResponsiveContainer width="100%" height="100%">
                <BarChart data={syncHistory.history} margin={{ top: 5, right: 5, left: -20, bottom: 5 }}>
                  <CartesianGrid strokeDasharray="3 3" stroke="#333" />
                  <XAxis dataKey="date" tick={{ fill: '#888', fontSize: 11 }} axisLine={{ stroke: '#444' }} />
                  <YAxis tick={{ fill: '#888', fontSize: 11 }} axisLine={{ stroke: '#444' }} allowDecimals={false} />
                  <Tooltip
                    contentStyle={{ backgroundColor: '#18181b', border: '1px solid #333', borderRadius: '6px' }}
                    labelStyle={{ color: '#fff' }}
                  />
                  <Legend wrapperStyle={{ fontSize: '11px' }} />
                  <Bar dataKey="events_created" fill="#3b82f6" name="Created" radius={[2, 2, 0, 0]} />
                  <Bar dataKey="events_updated" fill="#8b5cf6" name="Updated" radius={[2, 2, 0, 0]} />
                  <Bar dataKey="events_deleted" fill="#f97316" name="Deleted" radius={[2, 2, 0, 0]} />
                </BarChart>
              </ResponsiveContainer>
            </div>
          </div>

          {/* Summary Stats */}
          <div className="bg-zinc-900 rounded-lg border border-zinc-800 p-4 lg:col-span-2">
            <h3 className="text-sm font-semibold text-white mb-4">7-Day Summary</h3>
            <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-4">
              <div>
                <p className="text-xs text-gray-500">Total Syncs</p>
                <p className="text-lg font-semibold text-white">{syncHistory.summary.total_syncs}</p>
              </div>
              <div>
                <p className="text-xs text-gray-500">Success Rate</p>
                <p className="text-lg font-semibold text-green-400">{syncHistory.summary.success_rate.toFixed(1)}%</p>
              </div>
              <div>
                <p className="text-xs text-gray-500">Events Created</p>
                <p className="text-lg font-semibold text-blue-400">{syncHistory.summary.total_created}</p>
              </div>
              <div>
                <p className="text-xs text-gray-500">Events Updated</p>
                <p className="text-lg font-semibold text-purple-400">{syncHistory.summary.total_updated}</p>
              </div>
              <div>
                <p className="text-xs text-gray-500">Events Deleted</p>
                <p className="text-lg font-semibold text-orange-400">{syncHistory.summary.total_deleted}</p>
              </div>
              <div>
                <p className="text-xs text-gray-500">Avg Duration</p>
                <p className="text-lg font-semibold text-white">{syncHistory.summary.avg_duration_secs.toFixed(1)}s</p>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* Sources Table */}
      <div className="bg-zinc-900 rounded-lg border border-zinc-800 overflow-hidden">
        <div className="px-4 py-3 border-b border-zinc-800 flex items-center justify-between">
          <h2 className="text-sm font-semibold text-white">Calendar Sources</h2>
          {shouldAutoRefresh && (
            <div className={`flex items-center space-x-2 text-xs ${isAnySyncing ? 'text-blue-400' : 'text-green-400'}`}>
              <Spinner className="h-3 w-3" />
              <span>{isAnySyncing ? 'Syncing...' : 'Updating...'}</span>
            </div>
          )}
        </div>
        {sources.length > 0 ? (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="bg-black/50">
                <tr>
                  <th className="px-4 py-3 text-left text-xs font-medium text-gray-400 uppercase">Name</th>
                  <th className="px-4 py-3 text-left text-xs font-medium text-gray-400 uppercase">Type</th>
                  <th className="px-4 py-3 text-left text-xs font-medium text-gray-400 uppercase">Status</th>
                  <th className="px-4 py-3 text-left text-xs font-medium text-gray-400 uppercase">Last Sync</th>
                  <th className="px-4 py-3 text-left text-xs font-medium text-gray-400 uppercase">Next Sync</th>
                  <th className="px-4 py-3 text-left text-xs font-medium text-gray-400 uppercase">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-zinc-800">
                {sources.map((source) => {
                  const isSourceSyncing = source.sync_status === 'running' || syncingId === source.id;
                  return (
                    <tr key={source.id} className={`hover:bg-zinc-800/50 ${isSourceSyncing ? 'bg-blue-900/10' : ''}`}>
                      <td className="px-4 py-3">
                        <div className="flex items-center space-x-2">
                          {isSourceSyncing && <Spinner className="h-4 w-4 text-blue-400" />}
                          <div>
                            <div className="font-medium text-white">{source.name}</div>
                            <div className="text-xs text-gray-500 truncate max-w-xs">{source.source_url}</div>
                          </div>
                        </div>
                      </td>
                      <td className="px-4 py-3 text-gray-400">{source.source_type}</td>
                      <td className="px-4 py-3">
                        <div className="flex items-center space-x-2">
                          {source.enabled ? (
                            <span
                              className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-medium ${
                                source.sync_status === 'success'
                                  ? 'bg-green-900/50 text-green-400'
                                  : source.sync_status === 'partial'
                                  ? 'bg-yellow-900/50 text-yellow-400'
                                  : source.sync_status === 'error'
                                  ? 'bg-red-900/50 text-red-400'
                                  : source.sync_status === 'running'
                                  ? 'bg-blue-900/50 text-blue-400 animate-pulse'
                                  : 'bg-zinc-800 text-gray-400'
                              }`}
                            >
                              {source.sync_status === 'running' && (
                                <Spinner className="h-3 w-3 mr-1" />
                              )}
                              {source.sync_status === 'success'
                                ? 'Synced'
                                : source.sync_status === 'partial'
                                ? 'Partial'
                                : source.sync_status === 'error'
                                ? 'Error'
                                : source.sync_status === 'running'
                                ? 'Syncing'
                                : 'Pending'}
                            </span>
                          ) : (
                            <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-zinc-800 text-gray-500">
                              Disabled
                            </span>
                          )}
                          {source.is_stale && source.enabled && (
                            <span
                              className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-orange-900/50 text-orange-400"
                              title="Source hasn't synced within expected interval"
                            >
                              Stale
                            </span>
                          )}
                        </div>
                      </td>
                      <td className="px-4 py-3 text-gray-400">{formatDate(source.last_sync_at)}</td>
                      <td className="px-4 py-3 text-gray-400">
                        {source.enabled ? formatDate(source.next_sync_at) : '-'}
                      </td>
                      <td className="px-4 py-3">
                        <div className="flex items-center space-x-3">
                          <button
                            onClick={() => handleSync(source.id)}
                            disabled={!source.enabled || isSourceSyncing}
                            className={`inline-flex items-center text-xs font-medium disabled:opacity-50 ${
                              isSourceSyncing
                                ? 'text-blue-400'
                                : 'text-red-400 hover:text-red-300'
                            }`}
                          >
                            {isSourceSyncing ? (
                              <>
                                <Spinner className="h-3 w-3 mr-1" />
                                Syncing
                              </>
                            ) : (
                              'Sync'
                            )}
                          </button>
                          <Link
                            to={`/sources/${source.id}/edit`}
                            className="text-gray-400 hover:text-white text-xs font-medium"
                          >
                            Edit
                          </Link>
                          <Link
                            to={`/sources/${source.id}/logs`}
                            className="text-gray-400 hover:text-white text-xs font-medium"
                          >
                            Logs
                          </Link>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        ) : (
          <div className="text-center py-12 px-4">
            <h3 className="text-base font-medium text-white">No calendar sources yet</h3>
            <p className="mt-1 text-sm text-gray-400">Add your first source to begin syncing.</p>
            <Link
              to="/sources/add"
              className="mt-4 inline-flex items-center px-4 py-2 rounded bg-red-600 hover:bg-red-700 text-white text-sm font-medium transition-colors"
            >
              + Add Your First Source
            </Link>
          </div>
        )}
      </div>
    </div>
  );
}
