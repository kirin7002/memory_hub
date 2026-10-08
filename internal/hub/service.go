package hub

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"memoryhub/internal/auth"
	"strings"
	"time"
)

type Service struct {
	Pool  *pgxpool.Pool
	Key   []byte
	Lease time.Duration
}
type mutation struct {
	s      *Service
	tx     pgx.Tx
	ctx    context.Context
	p      auth.Principal
	seq    Seq
	policy Seq
	ops    []Operation
}
type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func ID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}
func encode(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
func owner(p auth.Principal) string { return p.Device + ":" + p.Agent }

func (s *Service) Authorize(ctx context.Context, p auth.Principal) error {
	return authorize(ctx, s.Pool, p)
}
func authorize(ctx context.Context, q querier, p auth.Principal) error {
	var status, scope string
	err := q.QueryRow(ctx, `SELECT status,scope_key FROM devices WHERE tenant_id=$1 AND user_id=$2 AND device_id=$3`, p.Tenant, p.User, p.Device).Scan(&status, &scope)
	if err == pgx.ErrNoRows || (err == nil && (status != "active" || scope != "personal")) {
		return fail(404, "not_found")
	}
	return err
}

func (s *Service) mutate(ctx context.Context, p auth.Principal, id, operation, target string, request any, f func(*mutation) (any, Seq, error)) (Receipt, error) {
	var receipt Receipt
	if id == "" || len(id) > 128 {
		return receipt, fail(400, "invalid_request_id")
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return receipt, err
	}
	defer tx.Rollback(ctx)
	var seq, policy Seq
	err = tx.QueryRow(ctx, `SELECT committed_seq,policy_epoch FROM user_stream_heads WHERE tenant_id=$1 AND user_id=$2 FOR UPDATE`, p.Tenant, p.User).Scan(&seq, &policy)
	if err == pgx.ErrNoRows {
		return receipt, fail(404, "not_found")
	}
	if err != nil {
		return receipt, err
	}
	if err = authorize(ctx, tx, p); err != nil {
		return receipt, err
	}
	// Check object visibility before returning even a previously stored receipt.
	if target != "" {
		var exists bool
		table, column := "contexts", "context_id"
		if operation == "message_accepted" || operation == "execution_imported" || operation == "conversation_deleted" {
			table, column = "conversations", "conversation_id"
		}
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+table+` WHERE tenant_id=$1 AND user_id=$2 AND `+column+`=$3 AND deleted_at IS NULL)`, p.Tenant, p.User, target).Scan(&exists)
		if err != nil {
			return receipt, err
		}
		if !exists {
			return receipt, fail(404, "not_found")
		}
	}
	digest := sha256.Sum256(encode(struct {
		Op, Target, Device string
		Request            any
	}{operation, target, p.Device, request}))
	var hash, response []byte
	err = tx.QueryRow(ctx, `SELECT request_hash,response_json FROM mutation_receipts WHERE tenant_id=$1 AND user_id=$2 AND request_id=$3`, p.Tenant, p.User, id).Scan(&hash, &response)
	if err == nil {
		if !hmac.Equal(hash, digest[:]) {
			return receipt, fail(409, "idempotency_conflict")
		}
		err = json.Unmarshal(response, &receipt)
		return receipt, err
	}
	if err != pgx.ErrNoRows {
		return receipt, err
	}
	m := &mutation{s: s, tx: tx, ctx: ctx, p: p, seq: seq + 1, policy: policy, ops: []Operation{}}
	value, version, err := f(m)
	if err != nil {
		return receipt, err
	}
	receipt = Receipt{ID: id, Seq: m.seq, ContextVersion: version, Value: value}
	change := Change{Seq: m.seq, Type: operation, Policy: m.policy, Operations: m.ops}
	payload := encode(change)
	if len(payload) > 1024*1024 {
		return receipt, fail(413, "payload_too_large")
	}
	if _, err = tx.Exec(ctx, `UPDATE user_stream_heads SET committed_seq=$3,policy_epoch=$4,updated_at=clock_timestamp() WHERE tenant_id=$1 AND user_id=$2`, p.Tenant, p.User, m.seq, m.policy); err != nil {
		return receipt, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sync_changes(tenant_id,user_id,user_seq,change_type,scope_refs,policy_epoch,payload_json) VALUES($1,$2,$3,$4,'[]',$5,$6)`, p.Tenant, p.User, m.seq, operation, m.policy, payload); err != nil {
		return receipt, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO mutation_receipts(tenant_id,user_id,request_id,operation,request_hash,response_json,user_seq) VALUES($1,$2,$3,$4,$5,$6,$7)`, p.Tenant, p.User, id, operation, digest[:], encode(receipt), m.seq); err != nil {
		return receipt, err
	}
	return receipt, tx.Commit(ctx)
}

func loadContext(ctx context.Context, q querier, p auth.Principal, id string, lock bool) (Context, error) {
	var c Context
	var state []byte
	sql := `SELECT context_id,scope_type,task_id,context_version,source_revision,summary_version,execution_target,state_json,fencing_token,active_run_id FROM contexts WHERE tenant_id=$1 AND user_id=$2 AND context_id=$3 AND deleted_at IS NULL`
	if lock {
		sql += " FOR UPDATE"
	}
	err := q.QueryRow(ctx, sql, p.Tenant, p.User, id).Scan(&c.ID, &c.Scope, &c.TaskID, &c.Version, &c.Source, &c.Summary, &c.Target, &state, &c.Token, &c.ActiveRun)
	if err == pgx.ErrNoRows {
		return c, fail(404, "not_found")
	}
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(state, &c.State)
	if err == nil {
		c.State.PendingInputs, err = pendingInputIDs(ctx, q, p, c.ID)
	}
	return c, err
}
func (m *mutation) load(id string) (Context, error) { return loadContext(m.ctx, m.tx, m.p, id, true) }
func (m *mutation) save(c *Context, bump bool) error {
	var err error
	c.State.PendingInputs, err = pendingInputIDs(m.ctx, m.tx, m.p, c.ID)
	if err != nil {
		return err
	}
	if bump {
		c.Version++
	}
	state := encode(c.State)
	if len(state) > 64*1024 {
		return fail(413, "context_too_large")
	}
	_, err = m.tx.Exec(m.ctx, `UPDATE contexts SET state_json=$4,status=$5,context_version=$6,source_revision=$7,fencing_token=$8,active_run_id=$9,execution_target=$10,latest_change_seq=$11,updated_at=clock_timestamp() WHERE tenant_id=$1 AND user_id=$2 AND context_id=$3`, m.p.Tenant, m.p.User, c.ID, state, c.State.Status, c.Version, c.Source, c.Token, c.ActiveRun, c.Target, m.seq)
	if err != nil {
		return err
	}
	if bump {
		_, err = m.tx.Exec(m.ctx, `INSERT INTO context_revisions(tenant_id,user_id,context_id,version,user_seq,state_json,execution_target) VALUES($1,$2,$3,$4,$5,$6,$7)`, m.p.Tenant, m.p.User, c.ID, c.Version, m.seq, state, c.Target)
		if err != nil {
			return err
		}
	}
	public := *c
	m.ops = append(m.ops, Operation{Kind: "upsert_context", ContextID: c.ID, Value: public})
	return nil
}
func (m *mutation) invalidate(c *Context) error {
	if _, err := m.tx.Exec(m.ctx, `UPDATE agent_runs SET status='superseded',updated_at=clock_timestamp() WHERE tenant_id=$1 AND user_id=$2 AND context_id=$3 AND status='running'`, m.p.Tenant, m.p.User, c.ID); err != nil {
		return err
	}
	c.Token++
	c.ActiveRun = nil
	return nil
}
func (m *mutation) newContext(id, scope, task string) (Context, error) {
	var c Context
	if id == "" {
		id = ID("ctx")
	}
	if len(id) > 128 {
		return c, fail(422, "id_too_long")
	}
	if task != "chat" && task != "trip_planning" {
		return c, fail(422, "schema_invalid")
	}
	c = Context{ID: id, Scope: scope, Source: 1, Target: "device", State: initialState(task)}
	if scope == "task" {
		v := ID("task")
		c.TaskID = &v
	}
	_, err := m.tx.Exec(m.ctx, `INSERT INTO contexts(tenant_id,user_id,context_id,scope_type,task_id,state_json) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, m.p.Tenant, m.p.User, c.ID, c.Scope, c.TaskID, encode(c.State))
	if err != nil {
		return c, err
	}
	var count int
	err = m.tx.QueryRow(m.ctx, `SELECT count(*) FROM context_revisions WHERE tenant_id=$1 AND user_id=$2 AND context_id=$3`, m.p.Tenant, m.p.User, c.ID).Scan(&count)
	if err != nil {
		return c, err
	}
	if count > 0 {
		return c, fail(409, "resource_exists")
	}
	_, err = m.tx.Exec(m.ctx, `INSERT INTO context_revisions(tenant_id,user_id,context_id,version,user_seq,state_json,execution_target) VALUES($1,$2,$3,0,$4,$5,'device')`, m.p.Tenant, m.p.User, c.ID, m.seq, encode(c.State))
	return c, err
}

func (s *Service) CreateContext(ctx context.Context, p auth.Principal, id string, r CreateContext) (Receipt, error) {
	return s.mutate(ctx, p, id, "context_created", "", r, func(m *mutation) (any, Seq, error) {
		c, err := m.newContext(r.ID, "task", r.TaskType)
		if err != nil {
			return nil, 0, err
		}
		err = m.save(&c, false)
		return c, c.Version, err
	})
}
func (s *Service) CreateConversation(ctx context.Context, p auth.Principal, id string, r CreateConversation) (Receipt, error) {
	return s.mutate(ctx, p, id, "conversation_created", r.ContextID, r, func(m *mutation) (any, Seq, error) {
		var c Context
		var err error
		if r.ContextID == "" {
			task := r.TaskType
			if task == "" {
				task = "chat"
			}
			scope := "conversation"
			if task != "chat" {
				scope = "task"
			}
			c, err = m.newContext("", scope, task)
		} else {
			c, err = m.load(r.ContextID)
			if err == nil && c.Scope != "task" {
				err = fail(422, "task_context_required")
			}
		}
		if err != nil {
			return nil, 0, err
		}
		conv := Conversation{ID: r.ID, ContextID: c.ID, Title: r.Title, Status: "active"}
		if conv.ID == "" {
			conv.ID = ID("conv")
		}
		if len(conv.ID) > 128 {
			return nil, 0, fail(422, "id_too_long")
		}
		if len(conv.Title) > 512 {
			return nil, 0, fail(422, "title_too_long")
		}
		tag, err := m.tx.Exec(m.ctx, `INSERT INTO conversations(tenant_id,user_id,conversation_id,context_id,title) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, p.Tenant, p.User, conv.ID, c.ID, conv.Title)
		if err != nil {
			return nil, 0, err
		}
		if tag.RowsAffected() == 0 {
			return nil, 0, fail(409, "resource_exists")
		}
		_, err = m.tx.Exec(m.ctx, `INSERT INTO context_sources(tenant_id,user_id,context_id,conversation_id,bound_at_seq) VALUES($1,$2,$3,$4,$5)`, p.Tenant, p.User, c.ID, conv.ID, m.seq)
		if err != nil {
			return nil, 0, err
		}
		if r.ContextID != "" {
			c.Source++
			if err = m.invalidate(&c); err != nil {
				return nil, 0, err
			}
		}
		if err = m.save(&c, r.ContextID != ""); err != nil {
			return nil, 0, err
		}
		m.ops = append(m.ops, Operation{Kind: "upsert_conversation", ConversationID: conv.ID, ContextID: c.ID, Value: conv})
		ack := conv
		ack.Title = ""
		return ack, c.Version, nil
	})
}

func (m *mutation) conversation(id string) (Conversation, error) {
	var c Conversation
	err := m.tx.QueryRow(m.ctx, `SELECT conversation_id,context_id,title,status,head_seq FROM conversations WHERE tenant_id=$1 AND user_id=$2 AND conversation_id=$3 AND deleted_at IS NULL FOR UPDATE`, m.p.Tenant, m.p.User, id).Scan(&c.ID, &c.ContextID, &c.Title, &c.Status, &c.Head)
	if err == pgx.ErrNoRows {
		return c, fail(404, "not_found")
	}
	return c, err
}
func loadEvent(ctx context.Context, q querier, p auth.Principal, id string) (Event, error) {
	var e Event
	var content, meta []byte
	err := q.QueryRow(ctx, `SELECT e.event_id,e.conversation_id,e.context_id,e.user_seq,e.conv_seq,e.event_type,e.actor_type,e.device_id,e.status,e.content_json,e.metadata_json,e.accepted_at FROM conversation_events e JOIN conversations c USING(tenant_id,user_id,conversation_id) WHERE e.tenant_id=$1 AND e.user_id=$2 AND e.event_id=$3 AND e.deleted_at IS NULL AND c.deleted_at IS NULL`, p.Tenant, p.User, id).Scan(&e.ID, &e.Conversation, &e.ContextID, &e.UserSeq, &e.ConvSeq, &e.Type, &e.Actor, &e.Device, &e.Status, &content, &meta, &e.Accepted)
	if err == pgx.ErrNoRows {
		return e, fail(404, "not_found")
	}
	if err != nil {
		return e, err
	}
	var text struct {
		Text string `json:"text"`
	}
	var md struct {
		Message      Message `json:"message"`
		Revalidation bool    `json:"requires_revalidation"`
	}
	if err = json.Unmarshal(content, &text); err != nil {
		return e, err
	}
	if err = json.Unmarshal(meta, &md); err != nil {
		return e, err
	}
	e.Text = text.Text
	e.Timing = md.Message.Timing
	e.Observed = md.Message.Observed
	e.Revalidation = md.Revalidation
	return e, nil
}
func (m *mutation) append(conv *Conversation, c Context, r Message, typ, status string) (Event, bool, error) {
	var e Event
	if r.ID == "" || len(r.ID) > 128 || r.Text == "" || len(r.Text) > 32*1024 {
		return e, false, fail(422, "invalid_event")
	}
	var existing, original []byte
	hash := sha256.Sum256(encode(struct {
		Message      Message
		Type, Device string
	}{r, typ, m.p.Device}))
	err := m.tx.QueryRow(m.ctx, `SELECT metadata_json,content_json FROM conversation_events WHERE tenant_id=$1 AND user_id=$2 AND event_id=$3`, m.p.Tenant, m.p.User, r.ID).Scan(&existing, &original)
	if err == nil {
		var md struct {
			Hash string `json:"input_hash"`
		}
		if json.Unmarshal(existing, &md) != nil || md.Hash != hex.EncodeToString(hash[:]) {
			return e, false, fail(409, "idempotency_conflict")
		}
		e, err = loadEvent(m.ctx, m.tx, m.p, r.ID)
		if err != nil {
			return e, false, err
		}
		if e.Conversation != conv.ID {
			return e, false, fail(409, "idempotency_conflict")
		}
		return e, false, nil
	}
	if err != pgx.ErrNoRows {
		return e, false, err
	}
	t := r.Timing
	if t.Schema != 1 || t.Origin == "" || len(t.Origin) > 128 || t.Boot == "" || len(t.Boot) > 128 || t.Segment == "" || len(t.Segment) > 128 || len(t.Parents) > 16 || t.Stream < 1 {
		return e, false, fail(422, "timing_required")
	}
	_, err = m.tx.Exec(m.ctx, `INSERT INTO event_streams(tenant_id,user_id,origin_id,context_id,owner_kind,owner_principal_id,device_id,producer_id) VALUES($1,$2,$3,$4,'device',$5,$6,$7) ON CONFLICT DO NOTHING`, m.p.Tenant, m.p.User, t.Origin, c.ID, owner(m.p), m.p.Device, m.p.Agent)
	if err != nil {
		return e, false, err
	}
	var own, contextID string
	var max Seq
	var previous *string
	err = m.tx.QueryRow(m.ctx, `SELECT owner_principal_id,context_id,max_accepted_stream_seq,last_accepted_event_id FROM event_streams WHERE tenant_id=$1 AND user_id=$2 AND origin_id=$3 FOR UPDATE`, m.p.Tenant, m.p.User, t.Origin).Scan(&own, &contextID, &max, &previous)
	if err != nil {
		return e, false, err
	}
	if own != owner(m.p) || contextID != c.ID || t.Stream <= max {
		return e, false, fail(409, "invalid_stream")
	}
	hasPrevious := previous == nil
	for _, parent := range t.Parents {
		if previous != nil && parent == *previous {
			hasPrevious = true
		}
		parentEvent, err := loadEvent(m.ctx, m.tx, m.p, parent)
		if err != nil {
			return e, false, fail(409, "parent_not_committed")
		}
		if parentEvent.ContextID != c.ID {
			return e, false, fail(422, "parent_scope_invalid")
		}
	}
	if !hasPrevious {
		return e, false, fail(409, "parent_not_committed")
	}
	if t.Calibration != nil {
		var valid bool
		err = m.tx.QueryRow(m.ctx, `SELECT EXISTS(SELECT 1 FROM time_calibrations WHERE tenant_id=$1 AND user_id=$2 AND device_id=$3 AND calibration_id=$4 AND boot_id=$5 AND clock_segment_id=$6)`, m.p.Tenant, m.p.User, m.p.Device, *t.Calibration, t.Boot, t.Segment).Scan(&valid)
		if err != nil {
			return e, false, err
		}
		if !valid {
			return e, false, fail(422, "calibration_invalid")
		}
	}
	conv.Head++
	actor := "agent"
	if typ == "user_message" {
		actor = "user"
	} else if typ == "tool_result" {
		actor = "tool"
	}
	e = Event{ID: r.ID, Conversation: conv.ID, ContextID: c.ID, UserSeq: m.seq, ConvSeq: conv.Head, Type: typ, Actor: actor, Device: &m.p.Device, Status: status, Text: r.Text, Timing: t, Observed: r.Observed, Revalidation: typ == "user_message" && r.Observed != c.Version}
	meta := encode(map[string]any{"message": r, "requires_revalidation": e.Revalidation, "input_hash": hex.EncodeToString(hash[:])})
	err = m.tx.QueryRow(m.ctx, `INSERT INTO conversation_events(tenant_id,user_id,event_id,conversation_id,context_id,user_seq,conv_seq,event_type,actor_type,device_id,status,content_json,metadata_json,source_time,origin_id,stream_seq,boot_id,clock_segment_id,monotonic_ns,calibration_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20) RETURNING accepted_at`, m.p.Tenant, m.p.User, e.ID, conv.ID, c.ID, m.seq, conv.Head, typ, actor, m.p.Device, status, encode(map[string]string{"text": r.Text}), meta, t.SourceTime, t.Origin, t.Stream, t.Boot, t.Segment, t.Mono, t.Calibration).Scan(&e.Accepted)
	if err != nil {
		return e, false, err
	}
	if _, err = m.tx.Exec(m.ctx, `UPDATE conversations SET head_seq=$4,latest_change_seq=$5,updated_at=clock_timestamp() WHERE tenant_id=$1 AND user_id=$2 AND conversation_id=$3`, m.p.Tenant, m.p.User, conv.ID, conv.Head, m.seq); err != nil {
		return e, false, err
	}
	if _, err = m.tx.Exec(m.ctx, `UPDATE event_streams SET max_accepted_stream_seq=$4,last_accepted_event_id=$5 WHERE tenant_id=$1 AND user_id=$2 AND origin_id=$3`, m.p.Tenant, m.p.User, t.Origin, t.Stream, r.ID); err != nil {
		return e, false, err
	}
	m.ops = append(m.ops, Operation{Kind: "append_event", ContextID: c.ID, ConversationID: conv.ID, Value: e})
	return e, true, nil
}
func (m *mutation) resolve(c Context, event, status, reason, proposal string, evidence ...string) error {
	var pid *string
	if proposal != "" {
		pid = &proposal
	}
	if evidence == nil {
		evidence = []string{}
	}
	_, err := m.tx.Exec(m.ctx, `INSERT INTO input_processing_revisions(tenant_id,user_id,context_id,event_id,user_seq,decision_context_version,resolution,reason_code,proposal_id,evidence_event_ids) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(tenant_id,user_id,context_id,event_id,user_seq) DO UPDATE SET resolution=excluded.resolution,reason_code=excluded.reason_code,proposal_id=excluded.proposal_id,evidence_event_ids=excluded.evidence_event_ids`, m.p.Tenant, m.p.User, c.ID, event, m.seq, c.Version+1, status, reason, pid, encode(evidence))
	if err == nil {
		m.ops = append(m.ops, Operation{Kind: "input_resolution", ContextID: c.ID, Value: Resolution{event, status, reason, pid}})
	}
	return err
}
func (s *Service) Message(ctx context.Context, p auth.Principal, convID string, r Message) (Receipt, error) {
	return s.mutate(ctx, p, r.ID, "message_accepted", convID, r, func(m *mutation) (any, Seq, error) {
		conv, err := m.conversation(convID)
		if err != nil {
			return nil, 0, err
		}
		c, err := m.load(conv.ContextID)
		if err != nil {
			return nil, 0, err
		}
		e, created, err := m.append(&conv, c, r, "user_message", "final")
		if err != nil {
			return nil, 0, err
		}
		if created {
			if err = m.resolve(c, e.ID, "pending", "new_input", ""); err != nil {
				return nil, 0, err
			}
			if err = m.invalidate(&c); err != nil {
				return nil, 0, err
			}
			if err = m.save(&c, true); err != nil {
				return nil, 0, err
			}
		}
		return map[string]any{"event_id": e.ID, "conv_seq": e.ConvSeq, "requires_revalidation": e.Revalidation, "records_persisted": true, "state_applied": false}, c.Version, nil
	})
}

func (s *Service) Command(ctx context.Context, p auth.Principal, id string, r Command) (Receipt, error) {
	return s.mutate(ctx, p, r.ID, "context_command", id, r, func(m *mutation) (any, Seq, error) {
		c, err := m.load(id)
		if err != nil {
			return nil, 0, err
		}
		if c.Version != r.Expected {
			return nil, 0, fail(409, "context_version_conflict")
		}
		commandEventID, err := m.commandEvent(c, r)
		if err != nil {
			return nil, 0, err
		}
		switch r.Name {
		case "cancel_task":
			c.State.Status = "cancelled"
		case "resume_task":
			if c.State.Status != "cancelled" && c.State.Status != "completed" {
				return nil, 0, fail(422, "task_not_stopped")
			}
			c.State.Status = "awaiting_confirmation"
			for field, slot := range c.State.Slots {
				slot.Status = "proposed"
				slot.Basis = ""
				c.State.Slots[field] = slot
				c.State.Proposals = append(c.State.Proposals, Proposal{ID: ID("proposal"), Field: field, Value: slot.Value, Sources: slot.Sources, Reason: "resume_requires_confirmation"})
			}
		case "set_execution_target":
			if r.Target != "device" {
				return nil, 0, fail(422, "cloud_executor_not_implemented")
			}
			c.Target = r.Target
		case "confirm_slot":
			index := -1
			for i, v := range c.State.Proposals {
				if v.ID == r.ProposalID {
					index = i
					break
				}
			}
			if index < 0 {
				return nil, 0, fail(409, "proposal_invalid")
			}
			pr := c.State.Proposals[index]
			if c.State.Status == "cancelled" || c.State.Status == "completed" {
				return nil, 0, fail(409, "task_stopped")
			}
			if err = validateField(c.State.TaskType, pr.Field, pr.Value); err != nil {
				return nil, 0, err
			}
			sources := append([]string{}, pr.Sources...)
			if commandEventID != "" {
				sources = append(sources, commandEventID)
			}
			c.State.Slots[pr.Field] = Slot{Value: pr.Value, Status: "confirmed", Sources: sources, Basis: "user_confirmation", ConfirmedAt: m.seq}
			c.State.Proposals = append(c.State.Proposals[:index], c.State.Proposals[index+1:]...)
			for _, event := range pr.Sources {
				e, err := loadEvent(m.ctx, m.tx, p, event)
				if err != nil {
					return nil, 0, err
				}
				if e.ContextID != c.ID {
					return nil, 0, fail(422, "source_invalid")
				}
				if e.Type == "user_message" {
					if err = m.resolve(c, event, "applied", "user_confirmation", "", commandEventID); err != nil {
						return nil, 0, err
					}
				}
			}
			if len(c.State.Proposals) == 0 {
				c.State.Status = "active"
			}
		default:
			return nil, 0, fail(422, "command_invalid")
		}
		if err = m.invalidate(&c); err != nil {
			return nil, 0, err
		}
		err = m.save(&c, true)
		return map[string]any{"context_id": c.ID, "command": r.Name}, c.Version, err
	})
}
func validateField(task, field, value string) error {
	if task != "trip_planning" || len(value) == 0 || len(value) > 256 {
		return fail(422, "schema_invalid")
	}
	switch field {
	case "destination":
		return nil
	case "departure_time":
		if _, err := time.Parse(time.RFC3339, value); err == nil {
			return nil
		}
	}
	return fail(422, "schema_invalid")
}
func directValue(e Event, field, value string) bool {
	text := strings.TrimSpace(e.Text)
	switch field {
	case "destination":
		for _, prefix := range []string{"目的地是", "改去", "去"} {
			if text == prefix+value {
				return true
			}
		}
	case "departure_time":
		return text == "出发时间是"+value
	}
	return false
}

func (s *Service) GetContext(ctx context.Context, p auth.Principal, id string) (Context, error) {
	tx, err := s.read(ctx, p)
	if err != nil {
		return Context{}, err
	}
	defer tx.Rollback(ctx)
	return loadContext(ctx, tx, p, id, false)
}
func (s *Service) String() string { return fmt.Sprintf("MemoryHub PostgreSQL lease=%s", s.Lease) }
