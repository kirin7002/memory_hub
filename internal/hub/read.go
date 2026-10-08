package hub

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"memoryhub/internal/auth"
	"strconv"
	"strings"
	"time"
)

func (s *Service) read(ctx context.Context, p auth.Principal) (pgx.Tx, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	if err = authorize(ctx, tx, p); err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}
func events(ctx context.Context, q querier, p auth.Principal, sql string, args ...any) ([]Event, error) {
	rows, err := q.Query(ctx, sql, args...)
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
	result := []Event{}
	for _, id := range ids {
		e, err := loadEvent(ctx, q, p, id)
		if err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, nil
}
func (s *Service) Events(ctx context.Context, p auth.Principal, id string, after Seq) (map[string]any, error) {
	tx, err := s.read(ctx, p)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var exists bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM conversations WHERE tenant_id=$1 AND user_id=$2 AND conversation_id=$3 AND deleted_at IS NULL)`, p.Tenant, p.User, id).Scan(&exists)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fail(404, "not_found")
	}
	ev, err := events(ctx, tx, p, `SELECT event_id FROM conversation_events WHERE tenant_id=$1 AND user_id=$2 AND conversation_id=$3 AND deleted_at IS NULL AND conv_seq>$4 ORDER BY conv_seq LIMIT 201`, p.Tenant, p.User, id, after)
	if err != nil {
		return nil, err
	}
	more := len(ev) > 200
	if more {
		ev = ev[:200]
	}
	next := after
	if len(ev) > 0 {
		next = ev[len(ev)-1].ConvSeq
	}
	return map[string]any{"events": ev, "has_more": more, "next_after_conv_seq": next}, nil
}

type AssembleRequest struct {
	Min      Seq    `json:"min_user_seq"`
	Profile  string `json:"model_profile_id"`
	Budget   int    `json:"input_budget_tokens"`
	Reserve  int    `json:"output_reserve_tokens"`
	RunID    string `json:"run_id,omitempty"`
	Expected Seq    `json:"expected_context_version,omitempty"`
}

func (s *Service) Assemble(ctx context.Context, p auth.Principal, id string, r AssembleRequest) (map[string]any, error) {
	windows := map[string]int{"mock-16k": 16384, "car-local-8k": 8192, "phone-local-8k": 8192}
	if r.Profile == "" {
		r.Profile = "mock-16k"
	}
	window, ok := windows[r.Profile]
	if !ok {
		return nil, fail(422, "model_profile_unknown")
	}
	if r.Reserve == 0 {
		r.Reserve = 2048
	}
	if r.Budget == 0 {
		r.Budget = 5000
	}
	if r.Reserve < 0 || r.Budget < 1 || r.Budget > window-r.Reserve-512 {
		return nil, fail(422, "context_budget_invalid")
	}
	tx, err := s.read(ctx, p)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	c, err := loadContext(ctx, tx, p, id, false)
	if err != nil {
		return nil, err
	}
	var seq, policy Seq
	err = tx.QueryRow(ctx, `SELECT committed_seq,policy_epoch FROM user_stream_heads WHERE tenant_id=$1 AND user_id=$2`, p.Tenant, p.User).Scan(&seq, &policy)
	if err != nil {
		return nil, err
	}
	if r.Min > seq {
		return nil, fail(400, "watermark_invalid")
	}
	if r.RunID != "" {
		var valid bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE tenant_id=$1 AND user_id=$2 AND run_id=$3 AND context_id=$4 AND status='running' AND lease_owner=$5 AND accepted_context_version=$6 AND accepted_source_revision=$7 AND accepted_policy_epoch=$8 AND lease_until>clock_timestamp())`, p.Tenant, p.User, r.RunID, c.ID, owner(p), r.Expected, c.Source, policy).Scan(&valid)
		if err != nil {
			return nil, err
		}
		if !valid || c.Version != r.Expected {
			return nil, fail(409, "stale_run")
		}
	}
	ev, err := events(ctx, tx, p, `SELECT e.event_id FROM conversation_events e JOIN context_sources cs USING(tenant_id,user_id,conversation_id) JOIN conversations cv USING(tenant_id,user_id,conversation_id) WHERE e.tenant_id=$1 AND e.user_id=$2 AND cs.context_id=$3 AND cs.removed_at_seq IS NULL AND e.deleted_at IS NULL AND cv.deleted_at IS NULL ORDER BY e.user_seq,e.conversation_id,e.conv_seq LIMIT 501`, p.Tenant, p.User, id)
	if err != nil {
		return nil, err
	}
	if len(ev) > 500 {
		return nil, fail(422, "context_history_limit_exceeded")
	}
	ev, err = assess(ctx, tx, p, ev, seq)
	if err != nil {
		return nil, err
	}
	ev, clockReview, err := orderTimeline(ev)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT event_id,resolution,reason_code,proposal_id FROM (SELECT DISTINCT ON(event_id) event_id,resolution,reason_code,proposal_id FROM input_processing_revisions WHERE tenant_id=$1 AND user_id=$2 AND context_id=$3 ORDER BY event_id,user_seq DESC) r WHERE resolution IN ('pending','needs_clarification') ORDER BY event_id`, p.Tenant, p.User, id)
	if err != nil {
		return nil, err
	}
	resolutions := []Resolution{}
	required := map[string]bool{}
	pendingIDs := map[string]bool{}
	for rows.Next() {
		var r Resolution
		if err = rows.Scan(&r.EventID, &r.Status, &r.Reason, &r.ProposalID); err != nil {
			rows.Close()
			return nil, err
		}
		resolutions = append(resolutions, r)
		required[r.EventID] = true
		if r.Status == "pending" {
			pendingIDs[r.EventID] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, slot := range c.State.Slots {
		for _, id := range slot.Sources {
			required[id] = true
		}
	}
	for _, proposal := range c.State.Proposals {
		for _, id := range proposal.Sources {
			required[id] = true
		}
	}
	// Include complete causal dependency groups before optional older messages.
	byID := map[string]Event{}
	for _, e := range ev {
		byID[e.ID] = e
	}
	var include func(string) error
	selected := map[string]bool{}
	include = func(id string) error {
		if selected[id] {
			return nil
		}
		e, ok := byID[id]
		if !ok {
			return fail(422, "missing_dependency")
		}
		selected[id] = true
		for _, parent := range e.Timing.Parents {
			if err := include(parent); err != nil {
				return err
			}
		}
		return nil
	}
	for id := range required {
		if err = include(id); err != nil {
			return nil, err
		}
	}
	cost := len(encode(c.State)) + 512
	for id := range selected {
		cost += len(encode(byID[id]))
	}
	if cost > r.Budget {
		return nil, fail(422, "context_budget_exceeded")
	}
	for i := len(ev) - 1; i >= 0; i-- {
		e := ev[i]
		if selected[e.ID] {
			continue
		}
		candidate := map[string]bool{}
		var gather func(string) bool
		gather = func(id string) bool {
			if selected[id] || candidate[id] {
				return true
			}
			e, ok := byID[id]
			if !ok {
				return false
			}
			candidate[id] = true
			for _, parent := range e.Timing.Parents {
				if !gather(parent) {
					return false
				}
			}
			return true
		}
		if !gather(e.ID) {
			continue
		}
		extra := 0
		for id := range candidate {
			extra += len(encode(byID[id]))
		}
		if cost+extra <= r.Budget {
			for id := range candidate {
				selected[id] = true
			}
			cost += extra
		}
	}
	history := []Event{}
	reported := []Event{}
	pending := []Event{}
	ids := []string{}
	omitted := []string{}
	for _, e := range ev {
		if !selected[e.ID] {
			omitted = append(omitted, e.ID)
			continue
		}
		ids = append(ids, e.ID)
		if pendingIDs[e.ID] {
			pending = append(pending, e)
		}
		if e.Status == "reported" {
			reported = append(reported, e)
		} else {
			history = append(history, e)
		}
	}
	payload := map[string]any{"context_id": c.ID, "context_version": c.Version, "source_revision": c.Source, "summary_version": c.Summary, "policy_epoch": policy, "as_of_user_seq": seq, "execution_target": c.Target, "state": c.State, "confirmed_state": c.State.Slots, "pending_inputs": pending, "input_resolutions": resolutions, "pending_proposals": c.State.Proposals, "conversation_history": history, "reported_history": reported, "tool_facts": []any{}, "summary": nil, "manifest": map[string]any{"schema_version": 2, "model_profile_id": r.Profile, "included_event_ids": ids, "omitted_event_ids": omitted, "missing_dependencies": []string{}, "incomplete": len(omitted) > 0, "offline_usable": len(omitted) == 0 && c.State.Status != "cancelled" && c.State.Status != "completed", "requires_refresh": false, "estimated_input_tokens": cost, "token_count_mode": "conservative_byte_estimate", "history_order": "event_time_causal", "algorithm_version": timelineAlgorithm, "needs_clock_review": clockReview}}
	confirmed := map[string]Slot{}
	for field, slot := range c.State.Slots {
		if slot.Status == "confirmed" {
			confirmed[field] = slot
		}
	}
	payload["confirmed_state"] = confirmed
	sum := sha256.Sum256(encode(payload))
	payload["content_hash"] = hex.EncodeToString(sum[:])
	payload["bundle_id"] = ID("bundle")
	payload["cache"] = map[string]any{"max_age_seconds": 300}
	payload["freshness"] = map[string]any{"generated_at": time.Now().UTC()}
	return payload, nil
}

type Cursor struct {
	Tenant, User, Device, Scope string
	Seq                         Seq
	Policy                      Seq
	Expires                     int64
}

func (s *Service) signCursor(p auth.Principal, seq, policy Seq) string {
	body := base64.RawURLEncoding.EncodeToString(encode(Cursor{p.Tenant, p.User, p.Device, "personal", seq, policy, time.Now().Add(7 * 24 * time.Hour).Unix()}))
	mac := hmac.New(sha256.New, s.Key)
	mac.Write([]byte("memoryhub:sync:" + body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *Service) verifyCursor(p auth.Principal, token string) (Cursor, error) {
	var c Cursor
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return c, fail(400, "cursor_invalid")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return c, fail(400, "cursor_invalid")
	}
	mac := hmac.New(sha256.New, s.Key)
	mac.Write([]byte("memoryhub:sync:" + parts[0]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return c, fail(400, "cursor_invalid")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(raw, &c) != nil || c.Tenant != p.Tenant || c.User != p.User || c.Device != p.Device || c.Scope != "personal" {
		return c, fail(400, "cursor_invalid")
	}
	if c.Expires < time.Now().Unix() {
		return c, fail(410, "snapshot_required")
	}
	return c, nil
}
func (s *Service) Snapshot(ctx context.Context, p auth.Principal) (map[string]any, error) {
	tx, err := s.read(ctx, p)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var seq, policy Seq
	err = tx.QueryRow(ctx, `SELECT committed_seq,policy_epoch FROM user_stream_heads WHERE tenant_id=$1 AND user_id=$2`, p.Tenant, p.User).Scan(&seq, &policy)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT conversation_id,context_id,title,status,head_seq FROM conversations WHERE tenant_id=$1 AND user_id=$2 AND deleted_at IS NULL ORDER BY conversation_id LIMIT 101`, p.Tenant, p.User)
	if err != nil {
		return nil, err
	}
	convs := []Conversation{}
	for rows.Next() {
		var c Conversation
		if err = rows.Scan(&c.ID, &c.ContextID, &c.Title, &c.Status, &c.Head); err != nil {
			rows.Close()
			return nil, err
		}
		convs = append(convs, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(convs) > 100 {
		return nil, fail(413, "snapshot_too_large")
	}
	contexts := []Context{}
	rows, err = tx.Query(ctx, `SELECT context_id FROM contexts WHERE tenant_id=$1 AND user_id=$2 AND deleted_at IS NULL ORDER BY context_id LIMIT 101`, p.Tenant, p.User)
	if err != nil {
		return nil, err
	}
	contextIDs := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		contextIDs = append(contextIDs, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(contextIDs) > 100 {
		return nil, fail(413, "snapshot_too_large")
	}
	for _, id := range contextIDs {
		c, err := loadContext(ctx, tx, p, id, false)
		if err != nil {
			return nil, err
		}
		contexts = append(contexts, c)
	}
	history := []Event{}
	ranges := []map[string]any{}
	hasEarlier := false
	for _, conv := range convs {
		ev, err := events(ctx, tx, p, `SELECT event_id FROM (SELECT event_id,conv_seq FROM conversation_events WHERE tenant_id=$1 AND user_id=$2 AND conversation_id=$3 AND deleted_at IS NULL ORDER BY conv_seq DESC LIMIT 20) e ORDER BY conv_seq`, p.Tenant, p.User, conv.ID)
		if err != nil {
			return nil, err
		}
		history = append(history, ev...)
		earlier := conv.Head > Seq(len(ev))
		hasEarlier = hasEarlier || earlier
		first := Seq(0)
		if len(ev) > 0 {
			first = ev[0].ConvSeq
		}
		ranges = append(ranges, map[string]any{"conversation_id": conv.ID, "first_included_conv_seq": first, "earlier_history_available": earlier})
	}
	// Snapshot the currently referenced runs so a fresh device can restore execution status.
	// Execution tokens and lease owners remain private to the claim/renew responses.
	rows, err = tx.Query(ctx, `SELECT r.run_id,r.context_id,r.accepted_context_version,r.status,r.lease_until,r.accepted_source_revision,r.accepted_policy_epoch FROM contexts c JOIN agent_runs r ON r.tenant_id=c.tenant_id AND r.user_id=c.user_id AND r.run_id=c.active_run_id WHERE c.tenant_id=$1 AND c.user_id=$2 AND c.deleted_at IS NULL ORDER BY c.context_id`, p.Tenant, p.User)
	if err != nil {
		return nil, err
	}
	activeRuns := []Run{}
	for rows.Next() {
		var run Run
		if err = rows.Scan(&run.ID, &run.ContextID, &run.Version, &run.Status, &run.Lease, &run.Source, &run.Policy); err != nil {
			rows.Close()
			return nil, err
		}
		activeRuns = append(activeRuns, run)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"conversations": convs, "contexts": contexts, "events": history, "active_runs": activeRuns, "history_ranges": ranges, "as_of_user_seq": seq, "policy_epoch": policy, "cursor": s.signCursor(p, seq, policy), "earlier_history_available": hasEarlier}
	if len(encode(payload)) > 2*1024*1024 {
		return nil, fail(413, "snapshot_too_large")
	}
	return payload, nil
}
func (s *Service) Changes(ctx context.Context, p auth.Principal, token string) (map[string]any, error) {
	c, err := s.verifyCursor(p, token)
	if err != nil {
		return nil, err
	}
	tx, err := s.read(ctx, p)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var w, policy, floor Seq
	err = tx.QueryRow(ctx, `SELECT committed_seq,policy_epoch,min_available_seq FROM user_stream_heads WHERE tenant_id=$1 AND user_id=$2`, p.Tenant, p.User).Scan(&w, &policy, &floor)
	if err != nil {
		return nil, err
	}
	if c.Policy != policy || c.Seq < floor-1 {
		return nil, fail(410, "snapshot_required")
	}
	if c.Seq > w {
		return nil, fail(400, "cursor_invalid")
	}
	rows, err := tx.Query(ctx, `SELECT user_seq,payload_json FROM sync_changes WHERE tenant_id=$1 AND user_id=$2 AND user_seq>$3 AND user_seq<=$4 ORDER BY user_seq LIMIT 100`, p.Tenant, p.User, c.Seq, w)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	changes := []json.RawMessage{}
	next := c.Seq
	size := 0
	for rows.Next() {
		var seq Seq
		var b []byte
		if err = rows.Scan(&seq, &b); err != nil {
			return nil, err
		}
		if size+len(b) > 2*1024*1024 {
			break
		}
		changes = append(changes, json.RawMessage(b))
		size += len(b)
		next = seq
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"changes": changes, "next_cursor": s.signCursor(p, next, policy), "scanned_through_seq": next, "server_watermark": w, "policy_epoch": policy, "has_more": next < w}, nil
}
func (s *Service) Ack(ctx context.Context, p auth.Principal, token string) error {
	c, err := s.verifyCursor(p, token)
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = authorize(ctx, tx, p); err != nil {
		return err
	}
	var policy, seq Seq
	err = tx.QueryRow(ctx, `SELECT policy_epoch,committed_seq FROM user_stream_heads WHERE tenant_id=$1 AND user_id=$2 FOR UPDATE`, p.Tenant, p.User).Scan(&policy, &seq)
	if err != nil {
		return err
	}
	if policy != c.Policy {
		return fail(410, "snapshot_required")
	}
	if c.Seq > seq {
		return fail(400, "cursor_invalid")
	}
	_, err = tx.Exec(ctx, `INSERT INTO device_sync_state(tenant_id,user_id,device_id,scope_key,applied_seq,policy_epoch) VALUES($1,$2,$3,'personal',$4,$5) ON CONFLICT(tenant_id,user_id,device_id,scope_key) DO UPDATE SET applied_seq=CASE WHEN device_sync_state.policy_epoch=excluded.policy_epoch THEN greatest(device_sync_state.applied_seq,excluded.applied_seq) ELSE excluded.applied_seq END,policy_epoch=excluded.policy_epoch,updated_at=clock_timestamp()`, p.Tenant, p.User, p.Device, c.Seq, c.Policy)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func ParseSeq(v string) (Seq, error) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, fail(400, "invalid_sequence")
	}
	return Seq(n), nil
}
