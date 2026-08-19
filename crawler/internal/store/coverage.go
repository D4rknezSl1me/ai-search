package store

import "context"

// LabelCount is a single (label, count) row from a grouped aggregate.
type LabelCount struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

// Coverage summarizes what the crawler has collected and the state of its
// frontier — the operator/visibility surface (docs/12 Phase 4 "coverage/stats").
type Coverage struct {
	TotalDocuments  int          `json:"total_documents"`
	Sources         int          `json:"sources"`
	ByContentType   []LabelCount `json:"by_content_type"`
	ByLang          []LabelCount `json:"by_lang"`
	FrontierByState []LabelCount `json:"frontier_by_state"`
	WithValidators  int          `json:"with_validators"`  // frontier rows carrying an ETag/Last-Modified
	Recrawlable     int          `json:"recrawlable"`      // FETCHED rows eligible for recrawl
}

func (s *Store) labelCounts(ctx context.Context, query string) ([]LabelCount, error) {
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LabelCount{}
	for rows.Next() {
		var lc LabelCount
		if err := rows.Scan(&lc.Label, &lc.Count); err != nil {
			return nil, err
		}
		out = append(out, lc)
	}
	return out, rows.Err()
}

// CoverageStats gathers the coverage summary in a handful of aggregate queries.
func (s *Store) CoverageStats(ctx context.Context) (*Coverage, error) {
	c := &Coverage{}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM documents`).Scan(&c.TotalDocuments); err != nil {
		return nil, err
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM sources`).Scan(&c.Sources); err != nil {
		return nil, err
	}
	var err error
	// content_type is stored with an optional "; charset=…" — group by the base type.
	if c.ByContentType, err = s.labelCounts(ctx, `
		SELECT split_part(content_type, ';', 1) AS ct, count(*)
		FROM documents GROUP BY ct ORDER BY count(*) DESC LIMIT 10`); err != nil {
		return nil, err
	}
	if c.ByLang, err = s.labelCounts(ctx, `
		SELECT COALESCE(NULLIF(lang, ''), 'unknown') AS l, count(*)
		FROM documents GROUP BY l ORDER BY count(*) DESC LIMIT 10`); err != nil {
		return nil, err
	}
	if c.FrontierByState, err = s.labelCounts(ctx, `
		SELECT state, count(*) FROM frontier_urls GROUP BY state ORDER BY count(*) DESC`); err != nil {
		return nil, err
	}
	if err = s.pool.QueryRow(ctx,
		`SELECT count(*) FROM frontier_urls WHERE etag IS NOT NULL OR last_modified IS NOT NULL`,
	).Scan(&c.WithValidators); err != nil {
		return nil, err
	}
	if err = s.pool.QueryRow(ctx,
		`SELECT count(*) FROM frontier_urls WHERE state = 'FETCHED' AND last_fetched_at IS NOT NULL`,
	).Scan(&c.Recrawlable); err != nil {
		return nil, err
	}
	return c, nil
}
