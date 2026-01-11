import { useState, useEffect } from 'react';
import { useParams, Link } from 'react-router-dom';
import { getSource, getSourceLogs } from '../services/api';
import type { Source, SyncLog } from '../types';

export default function SourceLogs() {
  const { id } = useParams<{ id: string }>();
  const [source, setSource] = useState<Source | null>(null);
  const [logs, setLogs] = useState<SyncLog[]>([]);
  const [page, setPage] = useState(1);
  const [totalPages, setTotalPages] = useState(1);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    loadData();
  }, [id, page]);

  const loadData = async () => {
    if (!id) return;
    setLoading(true);
    try {
      const [sourceData, logsData] = await Promise.all([getSource(id), getSourceLogs(id, page)]);
      setSource(sourceData);
      setLogs(logsData.logs || []);
      setTotalPages(logsData.total_pages || 1);
    } catch (err) {
      setError('Failed to load logs');
      console.error(err);
    } finally {
      setLoading(false);
    }
  };

  const formatDate = (dateStr: string) => {
    return new Date(dateStr).toLocaleDateString('en-US', {
      month: 'short',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    });
  };

  const formatDuration = (duration: number | null) => {
    if (duration === null) return '-';
    return `${duration.toFixed(2)}s`;
  };

  if (loading && !source) {
    return <div className="text-center py-12 text-gray-400">Loading...</div>;
  }

  if (error) {
    return <div className="text-center py-12 text-red-400">{error}</div>;
  }

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center space-x-4">
        <Link
          to="/sources"
          className="px-3 py-2 rounded bg-gray-800 text-gray-400 hover:text-white hover:bg-gray-700 transition-colors text-sm"
        >
          ← Back
        </Link>
        <div>
          <h1 className="text-2xl font-bold text-white">Sync Logs</h1>
          <p className="text-sm text-gray-400">{source?.name}</p>
        </div>
      </div>

      {/* Logs Table */}
      <div className="bg-gray-800 rounded-lg border border-gray-700 overflow-hidden">
        {logs.length > 0 ? (
          <>
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead className="bg-gray-900/50">
                  <tr>
                    <th className="px-4 py-3 text-left text-xs font-medium text-gray-400 uppercase">Time</th>
                    <th className="px-4 py-3 text-left text-xs font-medium text-gray-400 uppercase">Status</th>
                    <th className="px-4 py-3 text-left text-xs font-medium text-gray-400 uppercase">Message</th>
                    <th className="px-4 py-3 text-left text-xs font-medium text-gray-400 uppercase">Duration</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-gray-700">
                  {logs.map((log) => (
                    <tr key={log.id} className="hover:bg-gray-700/30">
                      <td className="px-4 py-3 text-gray-400 whitespace-nowrap">{formatDate(log.created_at)}</td>
                      <td className="px-4 py-3 whitespace-nowrap">
                        <span
                          className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-medium ${
                            log.status === 'success'
                              ? 'bg-green-900/50 text-green-400'
                              : log.status === 'error'
                              ? 'bg-red-900/50 text-red-400'
                              : 'bg-gray-700 text-gray-400'
                          }`}
                        >
                          {log.status === 'success' ? 'OK' : log.status === 'error' ? 'Error' : log.status}
                        </span>
                      </td>
                      <td className="px-4 py-3 text-gray-300">
                        <div>{log.message}</div>
                        {log.details && (
                          <details className="mt-1">
                            <summary className="text-xs text-indigo-400 cursor-pointer hover:text-indigo-300">
                              Details
                            </summary>
                            <pre className="mt-1 text-xs text-gray-400 bg-gray-900 p-2 rounded overflow-x-auto">
                              {log.details}
                            </pre>
                          </details>
                        )}
                      </td>
                      <td className="px-4 py-3 text-gray-400 whitespace-nowrap">{formatDuration(log.duration)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>

            {totalPages > 1 && (
              <div className="px-4 py-3 flex items-center justify-between border-t border-gray-700">
                <p className="text-sm text-gray-400">
                  Page {page} of {totalPages}
                </p>
                <div className="flex space-x-2">
                  {page > 1 && (
                    <button
                      onClick={() => setPage(page - 1)}
                      className="px-3 py-1.5 rounded text-sm text-gray-400 bg-gray-700 hover:bg-gray-600 hover:text-white"
                    >
                      Prev
                    </button>
                  )}
                  {page < totalPages && (
                    <button
                      onClick={() => setPage(page + 1)}
                      className="px-3 py-1.5 rounded text-sm text-gray-400 bg-gray-700 hover:bg-gray-600 hover:text-white"
                    >
                      Next
                    </button>
                  )}
                </div>
              </div>
            )}
          </>
        ) : (
          <div className="text-center py-12 px-4">
            <h3 className="text-base font-medium text-white">No sync logs yet</h3>
            <p className="mt-1 text-sm text-gray-400">Logs will appear after the first sync.</p>
          </div>
        )}
      </div>
    </div>
  );
}
