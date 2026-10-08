package hub

import (
	"github.com/jackc/pgx/v5"
	"time"
)

var processBoot = ID("boot")
var processStarted = time.Now()

// commandEvent records a user command using the cloud event writer.
func (m *mutation) commandEvent(c Context, r Command) (string, error) {
	var convID string
	err := m.tx.QueryRow(m.ctx, `SELECT cs.conversation_id FROM context_sources cs JOIN conversations cv USING(tenant_id,user_id,conversation_id) WHERE cs.tenant_id=$1 AND cs.user_id=$2 AND cs.context_id=$3 AND cs.removed_at_seq IS NULL AND cv.deleted_at IS NULL ORDER BY cs.bound_at_seq DESC,cs.conversation_id LIMIT 1`, m.p.Tenant, m.p.User, c.ID).Scan(&convID)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	conv, err := m.conversation(convID)
	if err != nil {
		return "", err
	}
	var exists bool
	err = m.tx.QueryRow(m.ctx, `SELECT EXISTS(SELECT 1 FROM conversation_events WHERE tenant_id=$1 AND user_id=$2 AND event_id=$3)`, m.p.Tenant, m.p.User, r.ID).Scan(&exists)
	if err != nil {
		return "", err
	}
	if exists {
		return "", fail(409, "idempotency_conflict")
	}
	origin := "system-" + processBoot + "-" + c.ID
	_, err = m.tx.Exec(m.ctx, `INSERT INTO event_streams(tenant_id,user_id,origin_id,context_id,owner_kind,owner_principal_id,producer_id) VALUES($1,$2,$3,$4,'cloud','memoryhub','context_commands') ON CONFLICT DO NOTHING`, m.p.Tenant, m.p.User, origin, c.ID)
	if err != nil {
		return "", err
	}
	var stream Seq
	var previous *string
	err = m.tx.QueryRow(m.ctx, `SELECT max_accepted_stream_seq,last_accepted_event_id FROM event_streams WHERE tenant_id=$1 AND user_id=$2 AND origin_id=$3 FOR UPDATE`, m.p.Tenant, m.p.User, origin).Scan(&stream, &previous)
	if err != nil {
		return "", err
	}
	stream++
	parents := []string{}
	if previous != nil {
		parents = append(parents, *previous)
	}
	for _, pr := range c.State.Proposals {
		if pr.ID == r.ProposalID {
			parents = append(parents, pr.Sources...)
			break
		}
	}
	now := time.Now().UTC()
	timing := Timing{Schema: 1, Origin: origin, Boot: processBoot, Segment: processBoot, Stream: stream, Mono: Seq(time.Since(processStarted).Nanoseconds()), SourceTime: &now, Parents: parents}
	conv.Head++
	e := Event{ID: r.ID, Conversation: conv.ID, ContextID: c.ID, UserSeq: m.seq, ConvSeq: conv.Head, Type: "context_command", Actor: "system", Status: "final", Text: r.Name, Timing: timing, Observed: c.Version}
	msg := Message{ID: r.ID, Text: r.Name, Observed: c.Version, Timing: timing}
	err = m.tx.QueryRow(m.ctx, `INSERT INTO conversation_events(tenant_id,user_id,event_id,conversation_id,context_id,user_seq,conv_seq,event_type,actor_type,status,content_json,metadata_json,source_time,origin_id,stream_seq,boot_id,clock_segment_id,monotonic_ns) VALUES($1,$2,$3,$4,$5,$6,$7,'context_command','system','final',$8,$9,$10,$11,$12,$13,$13,$14) RETURNING accepted_at`, m.p.Tenant, m.p.User, e.ID, conv.ID, c.ID, m.seq, conv.Head, encode(map[string]any{"text": r.Name, "proposal_id": r.ProposalID}), encode(map[string]any{"message": msg}), now, origin, stream, processBoot, timing.Mono).Scan(&e.Accepted)
	if err != nil {
		return "", err
	}
	if _, err = m.tx.Exec(m.ctx, `UPDATE conversations SET head_seq=$4,latest_change_seq=$5,updated_at=clock_timestamp() WHERE tenant_id=$1 AND user_id=$2 AND conversation_id=$3`, m.p.Tenant, m.p.User, conv.ID, conv.Head, m.seq); err != nil {
		return "", err
	}
	if _, err = m.tx.Exec(m.ctx, `UPDATE event_streams SET max_accepted_stream_seq=$4,last_accepted_event_id=$5 WHERE tenant_id=$1 AND user_id=$2 AND origin_id=$3`, m.p.Tenant, m.p.User, origin, stream, r.ID); err != nil {
		return "", err
	}
	m.ops = append(m.ops, Operation{Kind: "append_event", ContextID: c.ID, ConversationID: conv.ID, Value: e})
	return e.ID, nil
}
