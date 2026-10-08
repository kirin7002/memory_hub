package hub

import (
	"context"
	"github.com/jackc/pgx/v5"
	"memoryhub/internal/auth"
)

func (s *Service) GetRun(ctx context.Context, p auth.Principal, id string) (Run, error) {
	var run Run
	if err := s.Authorize(ctx, p); err != nil {
		return run, err
	}
	err := s.Pool.QueryRow(ctx, `SELECT r.run_id,r.context_id,r.accepted_context_version,r.status,r.lease_until,r.accepted_source_revision,r.accepted_policy_epoch FROM agent_runs r JOIN contexts c USING(tenant_id,user_id,context_id) WHERE r.tenant_id=$1 AND r.user_id=$2 AND r.run_id=$3 AND c.deleted_at IS NULL`, p.Tenant, p.User, id).Scan(&run.ID, &run.ContextID, &run.Version, &run.Status, &run.Lease, &run.Source, &run.Policy)
	if err == pgx.ErrNoRows {
		return run, fail(404, "not_found")
	}
	return run, err
}
func (s *Service) CancelRun(ctx context.Context, p auth.Principal, id string, r RunRequest) (Receipt, error) {
	run, err := s.GetRun(ctx, p, id)
	if err != nil {
		return Receipt{}, err
	}
	request := struct {
		RunID   string
		Request RunRequest
	}{id, r}
	return s.mutate(ctx, p, r.ID, "run_cancelled", run.ContextID, request, func(m *mutation) (any, Seq, error) {
		c, err := m.load(run.ContextID)
		if err != nil {
			return nil, 0, err
		}
		if c.ActiveRun == nil || *c.ActiveRun != id {
			return map[string]any{"run_id": id, "changed": false}, c.Version, nil
		}
		if r.Expected != c.Version {
			return nil, 0, fail(409, "context_version_conflict")
		}
		if err = m.invalidate(&c); err != nil {
			return nil, 0, err
		}
		if _, err = m.tx.Exec(ctx, `UPDATE agent_runs SET status='cancelled' WHERE tenant_id=$1 AND user_id=$2 AND run_id=$3`, p.Tenant, p.User, id); err != nil {
			return nil, 0, err
		}
		if err = m.save(&c, true); err != nil {
			return nil, 0, err
		}
		m.ops = append(m.ops, Operation{Kind: "upsert_run", ContextID: c.ID, Value: map[string]any{"run_id": id, "status": "cancelled"}})
		return map[string]any{"run_id": id, "changed": true}, c.Version, nil
	})
}
func (s *Service) FailRun(ctx context.Context, p auth.Principal, id string, r RunRequest) (Receipt, error) {
	contextID, err := s.runContext(ctx, p, id)
	if err != nil {
		return Receipt{}, err
	}
	request := struct {
		RunID   string
		Request RunRequest
	}{id, r}
	return s.mutate(ctx, p, r.ID, "run_failed", contextID, request, func(m *mutation) (any, Seq, error) {
		run, c, err := m.loadRun(id, r)
		if err != nil {
			return nil, 0, err
		}
		if _, err = m.tx.Exec(ctx, `UPDATE agent_runs SET status='failed',last_error_code='device_reported_failure',updated_at=clock_timestamp() WHERE tenant_id=$1 AND user_id=$2 AND run_id=$3`, p.Tenant, p.User, id); err != nil {
			return nil, 0, err
		}
		c.ActiveRun = nil
		if err = m.save(&c, false); err != nil {
			return nil, 0, err
		}
		run.Status = "failed"
		run.Token = 0
		m.ops = append(m.ops, Operation{Kind: "upsert_run", ContextID: c.ID, Value: run})
		return map[string]any{"run_id": id, "status": "failed"}, c.Version, nil
	})
}
func (s *Service) ListConversations(ctx context.Context, p auth.Principal, after string) (map[string]any, error) {
	tx, err := s.read(ctx, p)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT conversation_id,context_id,title,status,head_seq FROM conversations WHERE tenant_id=$1 AND user_id=$2 AND deleted_at IS NULL AND conversation_id>$3 ORDER BY conversation_id LIMIT 101`, p.Tenant, p.User, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Conversation{}
	for rows.Next() {
		var c Conversation
		if err = rows.Scan(&c.ID, &c.ContextID, &c.Title, &c.Status, &c.Head); err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	more := len(list) > 100
	if more {
		list = list[:100]
	}
	next := ""
	if more {
		next = list[len(list)-1].ID
	}
	return map[string]any{"conversations": list, "next_after_id": next, "has_more": more}, nil
}
func (s *Service) ActiveContexts(ctx context.Context, p auth.Principal, after string) (map[string]any, error) {
	tx, err := s.read(ctx, p)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT context_id FROM contexts WHERE tenant_id=$1 AND user_id=$2 AND deleted_at IS NULL AND status IN ('active','awaiting_confirmation','needs_revalidation') AND context_id>$3 ORDER BY context_id LIMIT 101`, p.Tenant, p.User, after)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	more := len(ids) > 100
	if more {
		ids = ids[:100]
	}
	list := []Context{}
	for _, id := range ids {
		c, err := loadContext(ctx, tx, p, id, false)
		if err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	next := ""
	if more {
		next = ids[len(ids)-1]
	}
	return map[string]any{"contexts": list, "next_after_id": next, "has_more": more}, nil
}
