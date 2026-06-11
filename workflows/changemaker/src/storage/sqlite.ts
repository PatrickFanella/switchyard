import { mkdirSync } from 'node:fs';
import { dirname } from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import type { ChangeEvent } from '../types.js';

export class ChangemakerStore {
  private readonly db: DatabaseSync;

  constructor(dbPath: string) {
    mkdirSync(dirname(dbPath), { recursive: true });
    this.db = new DatabaseSync(dbPath);
    this.ensureSchema();
  }

  close(): void {
    this.db.close();
  }

  listCoveredChangeIds(): Set<string> {
    const rows = this.db.prepare('select distinct change_id from post_change_links').all() as Array<{
      change_id: string;
    }>;
    return new Set(rows.map((row) => row.change_id));
  }

  getLastGeneratedPost(): { id: string; generatedAt: string; path: string; kind: string } | undefined {
    return this.db
      .prepare(
        `select id, generated_at as generatedAt, path, kind
        from generated_posts
        order by generated_at desc
        limit 1`,
      )
      .get() as { id: string; generatedAt: string; path: string; kind: string } | undefined;
  }

  recordChangeRun(input: { id: string; generatedAt: string; path: string; changes: ChangeEvent[]; kind?: string }): void {
    const insertPost = this.db.prepare(
      'insert or replace into generated_posts (id, generated_at, title, path, kind) values (?, ?, ?, ?, ?)',
    );
    const insertLink = this.db.prepare(
      'insert or ignore into post_change_links (post_id, change_id) values (?, ?)',
    );

    this.db.exec('begin');
    try {
      insertPost.run(input.id, input.generatedAt, 'Normalized changes collection', input.path, input.kind ?? 'changes');
      for (const change of input.changes) {
        insertLink.run(input.id, change.id);
      }
      this.db.exec('commit');
    } catch (error) {
      this.db.exec('rollback');
      throw error;
    }
  }

  private ensureSchema(): void {
    this.db.exec(`
      create table if not exists generated_posts (
        id text primary key,
        generated_at text not null,
        title text not null,
        path text not null,
        kind text not null default 'post'
      );

      create table if not exists post_change_links (
        post_id text not null,
        change_id text not null,
        primary key (post_id, change_id),
        foreign key (post_id) references generated_posts(id) on delete cascade
      );

      create index if not exists idx_post_change_links_change_id
        on post_change_links(change_id);

      create table if not exists provider_cache (
        cache_key text primary key,
        provider text not null,
        model text not null,
        prompt_version text not null,
        value_json text not null,
        created_at text not null
      );
    `);
  }

  getProviderCache<T>(cacheKey: string): T | undefined {
    const row = this.db.prepare('select value_json from provider_cache where cache_key = ?').get(cacheKey) as
      | { value_json: string }
      | undefined;
    return row ? (JSON.parse(row.value_json) as T) : undefined;
  }

  setProviderCache(input: {
    cacheKey: string;
    provider: string;
    model: string;
    promptVersion: string;
    value: unknown;
  }): void {
    this.db
      .prepare(
        `insert or replace into provider_cache
        (cache_key, provider, model, prompt_version, value_json, created_at)
        values (?, ?, ?, ?, ?, ?)`,
      )
      .run(
        input.cacheKey,
        input.provider,
        input.model,
        input.promptVersion,
        JSON.stringify(input.value),
        new Date().toISOString(),
      );
  }
}
