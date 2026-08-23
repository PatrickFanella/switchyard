package controlplane

import (
	"context"
	"database/sql"
	"time"
)

const (
	maxArtifactReportDays      = 90
	artifactRetentionSemantics = "retention_until is declared metadata; actual pruning deletes artifacts only when terminal run.created_at is older than the configured run-retention cutoff"
)

func validateArtifactReportInput(input ArtifactReportInput) error {
	if input.Start.IsZero() || input.End.IsZero() || input.End.Before(input.Start) {
		return ErrInvalid
	}
	start := utcDay(input.Start)
	end := utcDay(input.End)
	if end.Sub(start) > (maxArtifactReportDays-1)*24*time.Hour {
		return ErrInvalid
	}
	return nil
}

func utcDay(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func denseArtifactBuckets(start, end time.Time, sparse map[string]ArtifactDailyBucket) []ArtifactDailyBucket {
	buckets := make([]ArtifactDailyBucket, 0, int(utcDay(end).Sub(utcDay(start))/(24*time.Hour))+1)
	for day := utcDay(start); !day.After(utcDay(end)); day = day.AddDate(0, 0, 1) {
		date := day.Format(time.DateOnly)
		bucket := sparse[date]
		bucket.Date = date
		bucket.NetChange = bucket.Created - bucket.Pruned
		bucket.NetBytes = bucket.CreatedBytes - bucket.PrunedBytes
		buckets = append(buckets, bucket)
	}
	return buckets
}

func (s *MemoryStore) ArtifactReport(_ context.Context, input ArtifactReportInput) (ArtifactReport, error) {
	if err := validateArtifactReportInput(input); err != nil {
		return ArtifactReport{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	report := ArtifactReport{
		WindowStart: input.Start.UTC(), WindowEnd: input.End.UTC(), Bucket: "day",
		ObservationStartedAt: s.artifactObservationStarted, CreatedTotal: s.artifactsCreated,
		CreatedBytesTotal: s.artifactBytesCreated, PrunedTotal: s.artifactsPruned,
		PrunedBytesTotal: s.artifactBytesPruned, RetentionSemantics: artifactRetentionSemantics,
	}
	for _, artifacts := range s.artifacts {
		for _, artifact := range artifacts {
			report.Retained++
			report.RetainedBytes += int64(artifact.ByteSize)
			if artifact.RetentionUntil == nil {
				report.DeclaredRetentionMissing++
			} else if !artifact.RetentionUntil.After(input.End) {
				report.DeclaredRetentionExpired++
				report.DeclaredExpiredBytes += int64(artifact.ByteSize)
			}
		}
	}
	sparse := map[string]ArtifactDailyBucket{}
	for date, bucket := range s.artifactDaily {
		if date >= utcDay(input.Start).Format(time.DateOnly) && date <= utcDay(input.End).Format(time.DateOnly) {
			sparse[date] = bucket
		}
	}
	report.Buckets = denseArtifactBuckets(input.Start, input.End, sparse)
	return report, nil
}

func (s *PostgresStore) ArtifactReport(ctx context.Context, input ArtifactReportInput) (ArtifactReport, error) {
	if err := validateArtifactReportInput(input); err != nil {
		return ArtifactReport{}, err
	}
	report := ArtifactReport{WindowStart: input.Start.UTC(), WindowEnd: input.End.UTC(), Bucket: "day", RetentionSemantics: artifactRetentionSemantics}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return ArtifactReport{}, err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `select observation_started_at, created_count, created_bytes, pruned_count, pruned_bytes, pruned_bytes_unmeasured from artifact_lifecycle_totals where singleton=true`).Scan(
		&report.ObservationStartedAt, &report.CreatedTotal, &report.CreatedBytesTotal, &report.PrunedTotal, &report.PrunedBytesTotal, &report.PrunedBytesUnmeasuredTotal,
	); err != nil {
		return ArtifactReport{}, err
	}
	if err := tx.QueryRowContext(ctx, `select count(*), coalesce(sum(byte_size) filter (where storage_class='inline-data-uri'),0), count(*) filter (where storage_class<>'inline-data-uri'), count(*) filter (where retention_until is null), count(*) filter (where retention_until <= $1), coalesce(sum(byte_size) filter (where retention_until <= $1 and storage_class='inline-data-uri'),0), count(*) filter (where retention_until <= $1 and storage_class<>'inline-data-uri') from artifacts`, input.End.UTC()).Scan(
		&report.Retained, &report.RetainedBytes, &report.RetainedBytesUnmeasured, &report.DeclaredRetentionMissing, &report.DeclaredRetentionExpired, &report.DeclaredExpiredBytes, &report.DeclaredExpiredBytesUnmeasured,
	); err != nil {
		return ArtifactReport{}, err
	}
	rows, err := tx.QueryContext(ctx, `select measurement_date, created_count, created_bytes, pruned_count, pruned_bytes, pruned_bytes_unmeasured from artifact_lifecycle_daily where measurement_date between $1::date and $2::date order by measurement_date`, utcDay(input.Start).Format(time.DateOnly), utcDay(input.End).Format(time.DateOnly))
	if err != nil {
		return ArtifactReport{}, err
	}
	defer rows.Close()
	sparse := map[string]ArtifactDailyBucket{}
	for rows.Next() {
		var date time.Time
		var bucket ArtifactDailyBucket
		if err := rows.Scan(&date, &bucket.Created, &bucket.CreatedBytes, &bucket.Pruned, &bucket.PrunedBytes, &bucket.PrunedBytesUnmeasured); err != nil {
			return ArtifactReport{}, err
		}
		bucket.Date = date.UTC().Format(time.DateOnly)
		sparse[bucket.Date] = bucket
	}
	if err := rows.Err(); err != nil {
		return ArtifactReport{}, err
	}
	report.Buckets = denseArtifactBuckets(input.Start, input.End, sparse)
	if err := tx.Commit(); err != nil {
		return ArtifactReport{}, err
	}
	return report, nil
}
