package hub

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"memoryhub/internal/auth"
	"time"
)

func (m *mutation) loadRun(id string, r RunRequest) (Run, Context, error) {
	var run Run
	var c Context
	var contextID string
	err := m.tx.QueryRow(m.ctx, `SELECT context_id FROM agent_runs WHERE tenant_id=$1 AND user_id=$2 AND run_id=$3`, m.p.Tenant, m.p.User, id).Scan(&contextID)
	if err == pgx.ErrNoRows {
		return run, c, fail(404, "not_found")
	}
	if err != nil {
		return run, c, err
	}
	c, err = m.load(contextID)
	if err != nil {
		return run, c, err
	}
	err = m.tx.QueryRow(m.ctx, `SELECT run_id,context_id,accepted_context_version,fencing_token,status,lease_until,lease_owner,executor_device_id,accepted_source_revision,accepted_policy_epoch FROM agent_runs WHERE tenant_id=$1 AND user_id=$2 AND run_id=$3 FOR UPDATE`, m.p.Tenant, m.p.User, id).Scan(&run.ID, &run.ContextID, &run.Version, &run.Token, &run.Status, &run.Lease, &run.Owner, &run.Device, &run.Source, &run.Policy)
	if err != nil {
		return run, c, err
	}
	var now time.Time
	if err = m.tx.QueryRow(m.ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return run, c, err
	}
	if run.Owner != owner(m.p) || run.Device != m.p.Device || run.Status != "running" || c.ActiveRun == nil || *c.ActiveRun != run.ID || run.Token != c.Token || run.Token != r.Token || run.Version != c.Version || r.Expected != c.Version || !run.Lease.After(now) {
		return run, c, fail(409, "stale_run")
	}
	if run.Source != c.Source || run.Policy != m.policy {
		return run, c, fail(409, "stale_source")
	}
	return run, c, nil
}
func (s *Service) Claim(ctx context.Context, p auth.Principal, id string, r RunRequest) (Receipt, error) {
	return s.mutate(ctx, p, r.ID, "run_claimed", id, r, func(m *mutation) (any, Seq, error) {
		c, err := m.load(id)
		if err != nil {
			return nil, 0, err
		}
		if c.Version != r.Expected {
			return nil, 0, fail(409, "context_version_conflict")
		}
		if c.Target != "device" {
			return nil, 0, fail(409, "execution_target_conflict")
		}
		if c.State.Status == "cancelled" || c.State.Status == "completed" {
			return nil, 0, fail(409, "task_stopped")
		}
		var busy bool
		err = m.tx.QueryRow(m.ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE tenant_id=$1 AND user_id=$2 AND context_id=$3 AND status='running' AND lease_until>clock_timestamp())`, p.Tenant, p.User, id).Scan(&busy)
		if err != nil {
			return nil, 0, err
		}
		if busy {
			return nil, 0, fail(409, "run_busy")
		}
		if err = m.invalidate(&c); err != nil {
			return nil, 0, err
		}
		var trigger string
		err = m.tx.QueryRow(ctx, `SELECT e.event_id FROM conversation_events e JOIN context_sources cs USING(tenant_id,user_id,conversation_id) WHERE e.tenant_id=$1 AND e.user_id=$2 AND cs.context_id=$3 AND cs.removed_at_seq IS NULL AND e.event_type='user_message' AND e.deleted_at IS NULL ORDER BY e.user_seq DESC,e.conv_seq DESC LIMIT 1`, p.Tenant, p.User, c.ID).Scan(&trigger)
		if err == pgx.ErrNoRows {
			return nil, 0, fail(409, "no_input")
		}
		if err != nil {
			return nil, 0, err
		}
		run := Run{ID: ID("run"), ContextID: c.ID, Version: c.Version, Source: c.Source, Policy: m.policy, Token: c.Token, Owner: owner(p), Device: p.Device, Status: "running"}
		err = m.tx.QueryRow(m.ctx, `INSERT INTO agent_runs(tenant_id,user_id,run_id,context_id,executor_kind,executor_device_id,base_context_version,accepted_context_version,fencing_token,status,lease_owner,lease_until,accepted_source_revision,accepted_policy_epoch,trigger_event_id) VALUES($1,$2,$3,$4,'device',$5,$6,$6,$7,'running',$8,clock_timestamp()+($9::bigint*interval '1 millisecond'),$10,$11,$12) RETURNING lease_until`, p.Tenant, p.User, run.ID, c.ID, p.Device, c.Version, c.Token, run.Owner, s.Lease.Milliseconds(), c.Source, m.policy, trigger).Scan(&run.Lease)
		if err != nil {
			return nil, 0, err
		}
		c.ActiveRun = &run.ID
		if err = m.save(&c, false); err != nil {
			return nil, 0, err
		}
		public := run
		public.Token = 0
		m.ops = append(m.ops, Operation{Kind: "upsert_run", ContextID: c.ID, Value: public})
		return run, c.Version, nil
	})
}
func (s *Service) Renew(ctx context.Context, p auth.Principal, id string, r RunRequest) (Run, error) {
	var run Run
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return run, err
	}
	defer tx.Rollback(ctx)
	// Match the public mutation lock order so renew cannot race an invalidation.
	var seq, policy Seq
	err = tx.QueryRow(ctx, `SELECT committed_seq,policy_epoch FROM user_stream_heads WHERE tenant_id=$1 AND user_id=$2 FOR UPDATE`, p.Tenant, p.User).Scan(&seq, &policy)
	if err != nil {
		return run, err
	}
	if err = authorize(ctx, tx, p); err != nil {
		return run, err
	}
	m := &mutation{s: s, tx: tx, ctx: ctx, p: p, seq: seq, policy: policy}
	run, _, err = m.loadRun(id, r)
	if err != nil {
		return run, err
	}
	err = tx.QueryRow(ctx, `UPDATE agent_runs SET lease_until=clock_timestamp()+($4::bigint*interval '1 millisecond'),updated_at=clock_timestamp() WHERE tenant_id=$1 AND user_id=$2 AND run_id=$3 RETURNING lease_until`, p.Tenant, p.User, id, s.Lease.Milliseconds()).Scan(&run.Lease)
	if err != nil {
		return run, err
	}
	return run, tx.Commit(ctx)
}

func (m *mutation) interpret(c *Context, inputs []string, patches []Proposal) error {
	if len(inputs) == 0 || len(inputs) > 100 || len(patches) > 20 {
		return fail(422, "interpretation_invalid")
	}
	sources := map[string]Event{}
	states := map[string]string{}
	proposalIDs := map[string]string{}
	fields := map[string]bool{}
	for _, id := range inputs {
		if _, ok := sources[id]; ok {
			return fail(422, "duplicate_input")
		}
		e, err := loadEvent(m.ctx, m.tx, m.p, id)
		if err != nil || e.Type != "user_message" || e.ContextID != c.ID {
			return fail(422, "source_invalid")
		}
		var resolution string
		err = m.tx.QueryRow(m.ctx, `SELECT resolution FROM input_processing_revisions WHERE tenant_id=$1 AND user_id=$2 AND context_id=$3 AND event_id=$4 ORDER BY user_seq DESC LIMIT 1`, m.p.Tenant, m.p.User, c.ID, id).Scan(&resolution)
		if err != nil {
			return err
		}
		if resolution != "pending" {
			return fail(409, "input_already_processed")
		}
		sources[id] = e
		states[id] = "applied"
	}
	for _, patch := range patches {
		if fields[patch.Field] {
			return fail(422, "ambiguous_field_patch")
		}
		fields[patch.Field] = true
		if err := validateField(c.State.TaskType, patch.Field, patch.Value); err != nil {
			return err
		}
		if len(patch.Sources) == 0 {
			return fail(422, "source_invalid")
		}
		confirmed := false
		stale := false
		latestInput := Seq(0)
		for _, id := range patch.Sources {
			e, ok := sources[id]
			if !ok {
				return fail(422, "source_invalid")
			}
			confirmed = confirmed || directValue(e, patch.Field, patch.Value)
			stale = stale || e.Revalidation
			if e.UserSeq > latestInput {
				latestInput = e.UserSeq
			}
		}
		current, exists := c.State.Slots[patch.Field]
		outdated := exists && current.Status == "confirmed" && current.ConfirmedAt > latestInput
		if confirmed && outdated && current.Basis == "user_confirmation" && current.Value != patch.Value {
			for _, id := range patch.Sources {
				states[id] = "superseded"
			}
			continue
		}
		conflict := (stale || outdated) && exists && current.Status == "confirmed" && current.Value != patch.Value
		if confirmed && !conflict {
			remaining := []Proposal{}
			for _, old := range c.State.Proposals {
				if old.Field != patch.Field {
					remaining = append(remaining, old)
					continue
				}
				for _, oldID := range old.Sources {
					if _, isCurrent := sources[oldID]; !isCurrent {
						if err := m.resolve(*c, oldID, "superseded", "explicit_field_revision", "", patch.Sources...); err != nil {
							return err
						}
					}
				}
			}
			c.State.Proposals = remaining
			c.State.Slots[patch.Field] = Slot{Value: patch.Value, Status: "confirmed", Sources: patch.Sources, Basis: "registered_user_rule", ConfirmedAt: m.seq}
		} else {
			patch.ID = ID("proposal")
			patch.Reason = "inferred_value"
			if conflict {
				patch.Reason = "independent_branch_conflict"
			}
			c.State.Proposals = append(c.State.Proposals, patch)
			c.State.Status = "awaiting_confirmation"
			for _, id := range patch.Sources {
				states[id] = "needs_clarification"
				proposalIDs[id] = patch.ID
			}
		}
	}
	if len(c.State.Proposals) > 20 {
		return fail(422, "too_many_proposals")
	}
	if len(c.State.Proposals) == 0 && c.State.Status == "awaiting_confirmation" {
		c.State.Status = "active"
	}
	for _, id := range inputs {
		reason := "registered_interpretation"
		if states[id] == "needs_clarification" {
			reason = "confirmation_required"
		}
		if states[id] == "superseded" {
			reason = "superseded_by_explicit_confirmation"
			evidence := []string{}
			for _, patch := range patches {
				for _, source := range patch.Sources {
					if source == id {
						evidence = append(evidence, c.State.Slots[patch.Field].Sources...)
					}
				}
			}
			if err := m.resolve(*c, id, states[id], reason, proposalIDs[id], evidence...); err != nil {
				return err
			}
			continue
		}
		if err := m.resolve(*c, id, states[id], reason, proposalIDs[id]); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) Interpret(ctx context.Context, p auth.Principal, id string, r Interpretation) (Receipt, error) {
	contextID, err := s.runContext(ctx, p, id)
	if err != nil {
		return Receipt{}, err
	}
	request := struct {
		RunID          string
		Interpretation Interpretation
	}{id, r}
	return s.mutate(ctx, p, r.ID, "interpretation_accepted", contextID, request, func(m *mutation) (any, Seq, error) {
		run, c, err := m.loadRun(id, r.RunRequest)
		if err != nil {
			return nil, 0, err
		}
		if err = m.interpret(&c, r.Inputs, r.Patches); err != nil {
			return nil, 0, err
		}
		if err = m.save(&c, true); err != nil {
			return nil, 0, err
		}
		_, err = m.tx.Exec(ctx, `UPDATE agent_runs SET accepted_context_version=$4,updated_at=clock_timestamp() WHERE tenant_id=$1 AND user_id=$2 AND run_id=$3`, p.Tenant, p.User, id, c.Version)
		run.Version = c.Version
		return run, c.Version, err
	})
}
func (s *Service) Result(ctx context.Context, p auth.Principal, id string, r Result) (Receipt, error) {
	contextID, err := s.runContext(ctx, p, id)
	if err != nil {
		return Receipt{}, err
	}
	request := struct {
		RunID  string
		Result Result
	}{id, r}
	return s.mutate(ctx, p, r.ID, "run_completed", contextID, request, func(m *mutation) (any, Seq, error) {
		run, c, err := m.loadRun(id, r.RunRequest)
		if err != nil {
			return nil, 0, err
		}
		var convID, triggerID string
		// A shared task can span conversations; reply to the input that started this Run.
		err = m.tx.QueryRow(ctx, `SELECT e.conversation_id,e.event_id FROM agent_runs r JOIN conversation_events e ON e.tenant_id=r.tenant_id AND e.user_id=r.user_id AND e.event_id=r.trigger_event_id JOIN context_sources cs ON cs.tenant_id=e.tenant_id AND cs.user_id=e.user_id AND cs.conversation_id=e.conversation_id AND cs.context_id=r.context_id WHERE r.tenant_id=$1 AND r.user_id=$2 AND r.run_id=$3 AND cs.removed_at_seq IS NULL AND e.deleted_at IS NULL`, p.Tenant, p.User, run.ID).Scan(&convID, &triggerID)
		if err != nil {
			return nil, 0, err
		}
		conv, err := m.conversation(convID)
		if err != nil {
			return nil, 0, err
		}
		timing := r.Timing
		foundTrigger := false
		for _, parent := range timing.Parents {
			foundTrigger = foundTrigger || parent == triggerID
		}
		if !foundTrigger {
			timing.Parents = append(append([]string{}, timing.Parents...), triggerID)
		}
		e, _, err := m.append(&conv, c, Message{ID: r.EventID, Text: r.Text, Observed: c.Version, Timing: timing}, "assistant_message", "final")
		if err != nil {
			return nil, 0, err
		}
		_, err = m.tx.Exec(ctx, `UPDATE agent_runs SET status='succeeded',updated_at=clock_timestamp() WHERE tenant_id=$1 AND user_id=$2 AND run_id=$3`, p.Tenant, p.User, id)
		if err != nil {
			return nil, 0, err
		}
		c.ActiveRun = nil
		if err = m.save(&c, false); err != nil {
			return nil, 0, err
		}
		run.Status = "succeeded"
		run.Token = 0
		m.ops = append(m.ops, Operation{Kind: "upsert_run", ContextID: c.ID, Value: run})
		return map[string]any{"event_id": e.ID, "conv_seq": e.ConvSeq, "run_id": run.ID, "status": "succeeded"}, c.Version, nil
	})
}

func (s *Service) Import(ctx context.Context, p auth.Principal, r Execution) (Receipt, error) {
	return s.mutate(ctx, p, r.ID, "execution_imported", r.Conversation, r, func(m *mutation) (any, Seq, error) {
		if len(r.Events) == 0 || len(r.Events) > 100 {
			return nil, 0, fail(422, "batch_invalid")
		}
		conv, err := m.conversation(r.Conversation)
		if err != nil {
			return nil, 0, err
		}
		if conv.ContextID != r.ContextID {
			return nil, 0, fail(422, "context_invalid")
		}
		c, err := m.load(r.ContextID)
		if err != nil {
			return nil, 0, err
		}
		baseCurrent := r.Base == c.Version && r.Source == c.Source && r.Policy == m.policy
		addedInputs := []string{}
		for _, event := range r.Events {
			if event.Type != "user_message" && event.Type != "assistant_message" && event.Type != "tool_call" && event.Type != "tool_result" {
				return nil, 0, fail(422, "event_type_invalid")
			}
			status := "reported"
			if event.Type == "user_message" {
				status = "final"
			}
			e, added, err := m.append(&conv, c, event.Message, event.Type, status)
			if err != nil {
				return nil, 0, err
			}
			if event.Type == "user_message" && added {
				addedInputs = append(addedInputs, e.ID)
				if err = m.resolve(c, e.ID, "pending", "offline_import", ""); err != nil {
					return nil, 0, err
				}
			}
		}
		if len(addedInputs) > 0 {
			if err = m.invalidate(&c); err != nil {
				return nil, 0, err
			}
			if err = m.save(&c, true); err != nil {
				return nil, 0, err
			}
		}
		status := "needs_revalidation"
		if baseCurrent {
			status = "imported"
		}
		_, err = m.tx.Exec(ctx, `INSERT INTO device_executions(tenant_id,user_id,execution_id,device_id,conversation_id,context_id,base_context_version,base_source_revision,base_policy_epoch,imported_context_version,base_was_current,status,trace_json,proposal_json) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, p.Tenant, p.User, r.ID, p.Device, conv.ID, c.ID, r.Base, r.Source, r.Policy, c.Version, baseCurrent, status, encode(r.Events), encode(r.Patches))
		if err != nil {
			return nil, 0, err
		}
		value := map[string]any{"execution_id": r.ID, "records_persisted": true, "state_applied": false, "base_was_current": baseCurrent, "imported_context_version": c.Version, "status": status}
		m.ops = append(m.ops, Operation{Kind: "upsert_execution", ContextID: c.ID, Value: value})
		return value, c.Version, nil
	})
}
func (s *Service) Reconcile(ctx context.Context, p auth.Principal, id string, r RunRequest) (Receipt, error) {
	var target string
	err := s.Pool.QueryRow(ctx, `SELECT context_id FROM device_executions WHERE tenant_id=$1 AND user_id=$2 AND execution_id=$3 AND device_id=$4`, p.Tenant, p.User, id, p.Device).Scan(&target)
	if err == pgx.ErrNoRows {
		return Receipt{}, fail(404, "not_found")
	}
	if err != nil {
		return Receipt{}, err
	}
	request := struct {
		ExecutionID string
		Request     RunRequest
	}{id, r}
	return s.mutate(ctx, p, r.ID, "execution_reconciled", target, request, func(m *mutation) (any, Seq, error) {
		var device, contextID, status string
		var baseCurrent bool
		var imported, source, policy Seq
		var trace, patches []byte
		err := m.tx.QueryRow(ctx, `SELECT device_id,context_id,status,base_was_current,imported_context_version,base_source_revision,base_policy_epoch,trace_json,proposal_json FROM device_executions WHERE tenant_id=$1 AND user_id=$2 AND execution_id=$3 FOR UPDATE`, p.Tenant, p.User, id).Scan(&device, &contextID, &status, &baseCurrent, &imported, &source, &policy, &trace, &patches)
		if err == pgx.ErrNoRows {
			return nil, 0, fail(404, "not_found")
		}
		if err != nil {
			return nil, 0, err
		}
		if device != p.Device {
			return nil, 0, fail(404, "not_found")
		}
		c, err := m.load(contextID)
		if err != nil {
			return nil, 0, err
		}
		if !baseCurrent || status != "imported" || c.Version != imported || c.Version != r.Expected || c.Source != source || m.policy != policy || c.Target != "device" || c.State.Status == "cancelled" || c.State.Status == "completed" {
			return nil, 0, fail(409, "execution_requires_revalidation")
		}
		var busy bool
		err = m.tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE tenant_id=$1 AND user_id=$2 AND context_id=$3 AND status='running' AND lease_until>clock_timestamp())`, p.Tenant, p.User, c.ID).Scan(&busy)
		if err != nil {
			return nil, 0, err
		}
		if busy {
			return nil, 0, fail(409, "run_busy")
		}
		var events []OfflineEvent
		var proposals []Proposal
		if err = json.Unmarshal(trace, &events); err != nil {
			return nil, 0, err
		}
		if err = json.Unmarshal(patches, &proposals); err != nil {
			return nil, 0, err
		}
		inputs := []string{}
		for _, e := range events {
			if e.Type == "user_message" {
				inputs = append(inputs, e.ID)
			}
		}
		if err = m.interpret(&c, inputs, proposals); err != nil {
			return nil, 0, err
		}
		c.Source++
		if err = m.invalidate(&c); err != nil {
			return nil, 0, err
		}
		if err = m.save(&c, true); err != nil {
			return nil, 0, err
		}
		_, err = m.tx.Exec(ctx, `UPDATE device_executions SET status='reconciled',updated_at=clock_timestamp() WHERE tenant_id=$1 AND user_id=$2 AND execution_id=$3`, p.Tenant, p.User, id)
		if err != nil {
			return nil, 0, err
		}
		value := map[string]any{"execution_id": id, "records_persisted": true, "state_applied": true, "status": "reconciled"}
		m.ops = append(m.ops, Operation{Kind: "upsert_execution", ContextID: c.ID, Value: value})
		return value, c.Version, nil
	})
}
