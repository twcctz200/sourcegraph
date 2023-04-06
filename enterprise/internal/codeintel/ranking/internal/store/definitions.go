package store

import (
	"context"
	"time"

	"github.com/keegancsmith/sqlf"
	otlog "github.com/opentracing/opentracing-go/log"

	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/ranking/shared"
	"github.com/sourcegraph/sourcegraph/internal/database/basestore"
	"github.com/sourcegraph/sourcegraph/internal/database/batch"
	"github.com/sourcegraph/sourcegraph/internal/observation"
)

func (s *store) InsertDefinitionsForRanking(
	ctx context.Context,
	rankingGraphKey string,
	definitions chan shared.RankingDefinitions,
) (err error) {
	ctx, _, endObservation := s.operations.insertDefinitionsForRanking.With(ctx, &err, observation.Args{})
	defer endObservation(1, observation.Args{})

	return s.withTransaction(ctx, func(tx *store) error {
		inserter := func(inserter *batch.Inserter) error {
			for definition := range definitions {
				if err := inserter.Insert(ctx, definition.UploadID, definition.SymbolName, definition.DocumentPath, rankingGraphKey); err != nil {
					return err
				}
			}

			return nil
		}

		if err := batch.WithInserter(
			ctx,
			tx.db.Handle(),
			"codeintel_ranking_definitions",
			batch.MaxNumPostgresParameters,
			[]string{
				"upload_id",
				"symbol_name",
				"document_path",
				"graph_key",
			},
			inserter,
		); err != nil {
			return err
		}

		return nil
	})
}

func (s *store) VacuumAbandonedDefinitions(ctx context.Context, graphKey string, batchSize int) (_ int, err error) {
	ctx, _, endObservation := s.operations.vacuumAbandonedDefinitions.With(ctx, &err, observation.Args{LogFields: []otlog.Field{}})
	defer endObservation(1, observation.Args{})

	count, _, err := basestore.ScanFirstInt(s.db.Query(ctx, sqlf.Sprintf(vacuumAbandonedDefinitionsQuery, graphKey, graphKey, batchSize)))
	return count, err
}

const vacuumAbandonedDefinitionsQuery = `
WITH
locked_definitions AS (
	SELECT id
	FROM codeintel_ranking_definitions
	WHERE (graph_key < %s OR graph_key > %s)
	ORDER BY graph_key, id
	FOR UPDATE SKIP LOCKED
	LIMIT %s
),
deleted_definitions AS (
	DELETE FROM codeintel_ranking_definitions
	WHERE id IN (SELECT id FROM locked_definitions)
	RETURNING 1
)
SELECT COUNT(*) FROM deleted_definitions
`

func (s *store) VacuumStaleDefinitions(ctx context.Context, graphKey string) (
	numDefinitionRecordsScanned int,
	numStaleDefinitionRecordsDeleted int,
	err error,
) {
	ctx, _, endObservation := s.operations.vacuumStaleDefinitions.With(ctx, &err, observation.Args{LogFields: []otlog.Field{}})
	defer endObservation(1, observation.Args{})

	rows, err := s.db.Query(ctx, sqlf.Sprintf(
		vacuumStaleDefinitionsQuery,
		graphKey, int(threshold/time.Hour), vacuumBatchSize,
	))
	if err != nil {
		return 0, 0, err
	}
	defer func() { err = basestore.CloseRows(rows, err) }()

	for rows.Next() {
		if err := rows.Scan(
			&numDefinitionRecordsScanned,
			&numStaleDefinitionRecordsDeleted,
		); err != nil {
			return 0, 0, err
		}
	}

	return numDefinitionRecordsScanned, numStaleDefinitionRecordsDeleted, nil
}

const vacuumStaleDefinitionsQuery = `
WITH
locked_definitions AS (
	SELECT
		rd.id,
		u.repository_id,
		rd.upload_id
	FROM codeintel_ranking_definitions rd
	JOIN lsif_uploads u ON u.id = rd.upload_id
	WHERE
		rd.graph_key = %s AND
		(rd.last_scanned_at IS NULL OR NOW() - rd.last_scanned_at >= %s * '1 hour'::interval)
	ORDER BY rd.last_scanned_at ASC NULLS FIRST, rd.id
	FOR UPDATE SKIP LOCKED
	LIMIT %s
),
candidates AS (
	SELECT
		ld.id,
		uvt.is_default_branch IS TRUE AS safe
	FROM locked_definitions ld
	LEFT JOIN lsif_uploads_visible_at_tip uvt ON uvt.repository_id = ld.repository_id AND uvt.upload_id = ld.upload_id
),
updated_definitions AS (
	UPDATE codeintel_ranking_definitions
	SET last_scanned_at = NOW()
	WHERE id IN (SELECT c.id FROM candidates c WHERE c.safe)
),
deleted_definitions AS (
	DELETE FROM codeintel_ranking_definitions
	WHERE id IN (SELECT c.id FROM candidates c WHERE NOT c.safe)
	RETURNING 1
)
SELECT
	(SELECT COUNT(*) FROM candidates),
	(SELECT COUNT(*) FROM deleted_definitions)
`