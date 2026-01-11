export interface User {
  id: string;
  email: string;
  name: string;
  avatar?: string;
}

export interface Source {
  id: string;
  name: string;
  source_type: string;
  source_url: string;
  source_username: string;
  dest_url: string;
  dest_username: string;
  sync_interval: number;
  sync_direction: 'one_way' | 'two_way';
  conflict_strategy: string;
  enabled: boolean;
  sync_status: string;
  last_sync_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface SyncLog {
  id: string;
  source_id: string;
  status: string;
  message: string;
  details: string | null;
  events_created: number;
  events_updated: number;
  events_deleted: number;
  events_skipped: number;
  calendars_synced: number;
  events_processed: number;
  duration: number | null;
  created_at: string;
}

export interface DashboardStats {
  total_sources: number;
  active_sources: number;
  syncs_today: number;
  failed_syncs: number;
}

export interface SourceFormData {
  name: string;
  source_type: string;
  source_url: string;
  source_username: string;
  source_password: string;
  dest_url: string;
  dest_username: string;
  dest_password: string;
  sync_interval: number;
  sync_direction: 'one_way' | 'two_way';
  conflict_strategy: string;
}

export interface ApiResponse<T> {
  data?: T;
  error?: string;
}

export interface AuthStatus {
  authenticated: boolean;
  user?: User;
}

export interface SyncHistoryPoint {
  date: string;
  success: number;
  partial: number;
  error: number;
  events_created: number;
  events_updated: number;
  events_deleted: number;
}

export interface SyncSummary {
  total_syncs: number;
  success_rate: number;
  total_created: number;
  total_updated: number;
  total_deleted: number;
  avg_duration_secs: number;
}

export interface SyncHistory {
  history: SyncHistoryPoint[];
  summary: SyncSummary;
}

export interface MalformedEvent {
  id: string;
  source_id: string;
  source_name: string;
  event_path: string;
  error_message: string;
  discovered_at: string;
}
