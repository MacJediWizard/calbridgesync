export interface User {
  id: string;
  email: string;
  name: string;
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
