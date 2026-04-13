import { useState, useEffect } from 'react';
import { getAuditLogs } from '../services/api';

interface AuditLogEntry {
  id: string;
  action: string;
  resource_type: string;
  resource_id: string;
  details: string;
  ip_address: string;
  created_at: string;
}

const actionColors: Record<string, string> = {
  'source.create': 'bg-green-900/50 text-green-400',
  'source.update': 'bg-blue-900/50 text-blue-400',
  'source.delete': 'bg-red-900/50 text-red-400',
  'source.toggle': 'bg-yellow-900/50 text-yellow-400',
  'sync.trigger': 'bg-purple-900/50 text-purple-400',
  'settings.update': 'bg-cyan-900/50 text-cyan-400',
  'export.calendars': 'bg-zinc-800 text-gray-400',
};

export default function AuditLog() {
  const [logs, setLogs] = useState<AuditLogEntry[]>([]);
  const [page, setPage] = useState(1);
  const [totalPages, setTotalPages] = useState(1);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    loadLogs();
  }, [page]);

  const loadLogs = async () => {
    setLoading(true);
    try {
      const data = await getAuditLogs(page);
      setLogs(data.logs || []);
      setTotalPages(data.total_pages);
    } catch {
      console.error('Failed to load audit logs');
    } finally {
      setLoading(false);
    }
  };

  const formatDate = (dateStr: string) => {
    return new Date(dateStr).toLocaleString('en-US', {
      month: 'short', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit',
    });
  };

  if (loading && logs.length === 0) {
    return <div className="text-center py-12 text-gray-400">Loading...</div>;
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold text-white" style={{ fontFamily: 'Orbitron, sans-serif' }}>Audit Log</h1>
        <p className="text-sm text-gray-400 mt-1">Track who did what and when on this instance</p>
      </div>

      {logs.length === 0 ? (
        <div className="text-center py-12 text-gray-500">No audit entries yet. Actions will be logged here as they happen.</div>
      ) : (
        <div className="bg-zinc-900 rounded-lg border border-zinc-800 overflow-hidden">
          <div className="overflow-x-auto">
            <table className="w-full text-left">
              <thead className="text-xs text-gray-400 uppercase bg-zinc-800/50">
                <tr>
                  <th className="px-4 py-3">Time</th>
                  <th className="px-4 py-3">Action</th>
                  <th className="px-4 py-3">Resource</th>
                  <th className="px-4 py-3">Details</th>
                  <th className="px-4 py-3">IP</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-zinc-800">
                {logs.map((log) => (
                  <tr key={log.id} className="hover:bg-zinc-800/50">
                    <td className="px-4 py-3 text-gray-400 whitespace-nowrap text-sm">{formatDate(log.created_at)}</td>
                    <td className="px-4 py-3">
                      <span className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-medium ${actionColors[log.action] || 'bg-zinc-800 text-gray-400'}`}>
                        {log.action}
                      </span>
                    </td>
                    <td className="px-4 py-3 text-gray-300 text-sm">
                      {log.resource_type}
                      {log.resource_id && (
                        <span className="text-gray-500 ml-1 text-xs">{log.resource_id.slice(0, 8)}...</span>
                      )}
                    </td>
                    <td className="px-4 py-3 text-gray-400 text-sm max-w-xs truncate">{log.details || '-'}</td>
                    <td className="px-4 py-3 text-gray-500 text-xs font-mono">{log.ip_address}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          {totalPages > 1 && (
            <div className="flex justify-between items-center p-4 border-t border-zinc-800">
              <button
                onClick={() => setPage(p => Math.max(1, p - 1))}
                disabled={page <= 1}
                className="px-3 py-1 rounded bg-zinc-800 text-gray-300 text-sm disabled:opacity-50"
              >
                Previous
              </button>
              <span className="text-gray-400 text-sm">Page {page} of {totalPages}</span>
              <button
                onClick={() => setPage(p => Math.min(totalPages, p + 1))}
                disabled={page >= totalPages}
                className="px-3 py-1 rounded bg-zinc-800 text-gray-300 text-sm disabled:opacity-50"
              >
                Next
              </button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
