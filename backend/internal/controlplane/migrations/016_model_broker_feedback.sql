create table if not exists model_broker_feedback (
  id text primary key,
  request_id text not null references model_broker_requests(id) on delete cascade,
  rating integer not null check (rating between -1 and 1),
  comment text not null default '',
  created_at timestamptz not null default now()
);

create index if not exists model_broker_feedback_created_idx on model_broker_feedback(created_at desc, id desc);
create index if not exists model_broker_feedback_request_idx on model_broker_feedback(request_id, created_at desc);
