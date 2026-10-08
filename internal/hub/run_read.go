package hub

import (
	"context"
	"github.com/jackc/pgx/v5"
	"memoryhub/internal/auth"
)

func (s *Service) runContext(ctx context.Context, p auth.Principal, id string) (string, error) {
	var contextID string
	err := s.Pool.QueryRow(ctx, `SELECT r.context_id FROM agent_runs r JOIN contexts c USING(tenant_id,user_id,context_id) WHERE r.tenant_id=$1 AND r.user_id=$2 AND r.run_id=$3 AND r.executor_device_id=$4 AND r.lease_owner=$5 AND c.deleted_at IS NULL`, p.Tenant, p.User, id, p.Device, owner(p)).Scan(&contextID)
	if err == pgx.ErrNoRows {
		return "", fail(404, "not_found")
	}
	return contextID, err
}
