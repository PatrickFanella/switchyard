drop index if exists broker.request_event_search_idx;

alter table broker.request_event
  drop column if exists search_tsv;

alter table broker.request_event
  add column search_tsv tsvector generated always as (
    to_tsvector('simple', coalesce(client,'') || ' ' || coalesce(model,'') || ' ' || coalesce(path,'') || ' ' || coalesce(status,'') || ' ' || coalesce(request_body_text,'') || ' ' || coalesce(response_body_text,''))
  ) stored;

create index if not exists request_event_search_idx on broker.request_event using gin (search_tsv);
