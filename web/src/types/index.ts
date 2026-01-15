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
  sync_days_past: number;
  sync_direction: 'one_way' | 'two_way';
  conflict_strategy: string;
  selected_calendars: string[];
  enabled: boolean;
  sync_status: string;
  last_sync_at: string | null;
  next_sync_at: string | null;
  is_stale: boolean;
  created_at: string;
  updated_at: string;
}

export interface Calendar {
  name: string;
  path: string;
  color?: string;
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
  sync_days_past: number;
  sync_direction: 'one_way' | 'two_way';
  conflict_strategy: string;
  selected_calendars: string[];
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

export interface AlertPreferences {
  email_enabled: boolean | null;
  webhook_enabled: boolean | null;
  webhook_url: string;
  cooldown_minutes: number | null;
}

export interface SyncActivity {
  source_id: string;
  source_name: string;
  status: 'running' | 'completed' | 'error' | 'partial';
  current_calendar?: string;
  total_calendars: number;
  calendars_synced: number;
  events_processed: number;
  events_created: number;
  events_updated: number;
  events_deleted: number;
  events_skipped: number;
  started_at: string;
  completed_at?: string;
  duration?: string;
  message?: string;
  errors?: string[];
}

export interface ActivityData {
  active: SyncActivity[];
  recent: SyncActivity[];
}
