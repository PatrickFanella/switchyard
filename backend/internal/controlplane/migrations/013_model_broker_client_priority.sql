alter table model_broker_requests
  add column if not exists client_priority integer not null default 0;

create index if not exists idx_model_broker_requests_status_priority_created
  on model_broker_requests(status, client_priority desc, created_at asc);
