create index if not exists runs_retention_idx on runs(created_at) where status in ('succeeded', 'failed', 'canceled', 'paused', 'timed_out');
create index if not exists model_broker_requests_retention_idx on model_broker_requests(created_at) where status in ('completed', 'failed', 'canceled');
