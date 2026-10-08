package hub

import (
	"context"
	"memoryhub/internal/auth"
)

func pendingInputIDs(ctx context.Context, q querier, p auth.Principal, id string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT event_id FROM (SELECT DISTINCT ON(event_id) event_id,resolution FROM input_processing_revisions WHERE tenant_id=$1 AND user_id=$2 AND context_id=$3 ORDER BY event_id,user_seq DESC) r WHERE resolution='pending' ORDER BY event_id LIMIT 1001`, p.Tenant, p.User, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) > 1000 {
		return nil, fail(422, "pending_input_limit")
	}
	return ids, nil
}
