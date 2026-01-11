import { useState, useEffect } from 'react';
import { Link } from 'react-router-dom';
import { getDashboardStats, getSources, triggerSync } from '../services/api';
import type { DashboardStats, Source } from '../types';

export default function Dashboard() {
  const [stats, setStats] = useState<DashboardStats | null>(null);
  const [sources, setSources] = useState<Source[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [syncingId, setSyncingId] = useState<string | null>(null);

  useEffect(() => {
    loadData();
  }, []);

  const loadData = async () => {
    try {
      const [statsData, sourcesData] = await Promise.all([
        getDashboardStats(),
        getSources(),
      ]);
      setStats(statsData);
      setSources(sourcesData);
    } catch (err) {
      setError('Failed to load dashboard data');
      console.error(err);
    } finally {
      setLoading(false);
    }
  };

  const handleSync = async (id: string) => {
    setSyncingId(id);
    try {
      await triggerSync(id);
      await loadData();
    } catch (err) {
      console.error('Sync failed:', err);
    } finally {
      setSyncingId(null);
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
        <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
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
        </div>
      )}

      {/* Sources Table */}
      <div className="bg-zinc-900 rounded-lg border border-zinc-800 overflow-hidden">
        <div className="px-4 py-3 border-b border-zinc-800">
          <h2 className="text-sm font-semibold text-white">Calendar Sources</h2>
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
                  <th className="px-4 py-3 text-left text-xs font-medium text-gray-400 uppercase">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-zinc-800">
                {sources.map((source) => (
                  <tr key={source.id} className="hover:bg-zinc-800/50">
                    <td className="px-4 py-3">
                      <div className="font-medium text-white">{source.name}</div>
                      <div className="text-xs text-gray-500 truncate max-w-xs">{source.source_url}</div>
                    </td>
                    <td className="px-4 py-3 text-gray-400">{source.source_type}</td>
                    <td className="px-4 py-3">
                      {source.enabled ? (
                        <span
                          className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-medium ${
                            source.sync_status === 'success'
                              ? 'bg-green-900/50 text-green-400'
                              : source.sync_status === 'error'
                              ? 'bg-red-900/50 text-red-400'
                              : source.sync_status === 'running'
                              ? 'bg-yellow-900/50 text-yellow-400'
                              : 'bg-zinc-800 text-gray-400'
                          }`}
                        >
                          {source.sync_status === 'success'
                            ? 'Synced'
                            : source.sync_status === 'error'
                            ? 'Error'
                            : source.sync_status === 'running'
                            ? 'Running'
                            : 'Pending'}
                        </span>
                      ) : (
                        <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-zinc-800 text-gray-500">
                          Disabled
                        </span>
                      )}
                    </td>
                    <td className="px-4 py-3 text-gray-400">{formatDate(source.last_sync_at)}</td>
                    <td className="px-4 py-3">
                      <div className="flex items-center space-x-3">
                        <button
                          onClick={() => handleSync(source.id)}
                          disabled={!source.enabled || syncingId === source.id}
                          className="text-red-400 hover:text-red-300 text-xs font-medium disabled:opacity-50"
                        >
                          {syncingId === source.id ? '...' : 'Sync'}
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
                ))}
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
