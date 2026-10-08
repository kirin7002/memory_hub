package hub

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"memoryhub/internal/auth"
	"sort"
	"strings"
	"time"
)

const timelineAlgorithm = "timeline-v1"

type timelineCursor struct {
	Tenant, User, Device, Conversation, Algorithm string
	Watermark, Policy                             Seq
	Position                                      int
	Expires                                       int64
}

func (s *Service) timelineToken(c timelineCursor) string {
	body := base64.RawURLEncoding.EncodeToString(encode(c))
	mac := hmac.New(sha256.New, s.Key)
	mac.Write([]byte("memoryhub:timeline:" + body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *Service) timelineDecode(p auth.Principal, id, token string) (timelineCursor, error) {
	var c timelineCursor
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return c, fail(400, "cursor_invalid")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return c, fail(400, "cursor_invalid")
	}
	mac := hmac.New(sha256.New, s.Key)
	mac.Write([]byte("memoryhub:timeline:" + parts[0]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return c, fail(400, "cursor_invalid")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(raw, &c) != nil || c.Tenant != p.Tenant || c.User != p.User || c.Device != p.Device || c.Conversation != id || c.Algorithm != timelineAlgorithm || c.Position < 0 {
		return c, fail(400, "cursor_invalid")
	}
	if c.Expires < time.Now().Unix() {
		return c, fail(410, "timeline_reset_required")
	}
	return c, nil
}
func assess(ctx context.Context, q querier, p auth.Principal, ev []Event, w Seq) ([]Event, error) {
	ids := []string{}
	for _, e := range ev {
		ids = append(ids, e.ID)
	}
	rows, err := q.Query(ctx, `SELECT DISTINCT ON(event_id) event_id,estimated_occurred_at,earliest_at,latest_at,time_quality FROM event_time_assessments WHERE tenant_id=$1 AND user_id=$2 AND event_id=ANY($3::text[]) AND published_user_seq<=$4 ORDER BY event_id,published_user_seq DESC`, p.Tenant, p.User, ids, w)
	if err != nil {
		return nil, err
	}
	type assessment struct {
		estimated, earliest, latest *time.Time
		quality                     string
	}
	byID := map[string]assessment{}
	for rows.Next() {
		var id string
		var a assessment
		if err = rows.Scan(&id, &a.estimated, &a.earliest, &a.latest, &a.quality); err != nil {
			rows.Close()
			return nil, err
		}
		byID[id] = a
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range ev {
		e := &ev[i]
		if a, ok := byID[e.ID]; ok {
			e.Estimated = a.estimated
			e.Earliest = a.earliest
			e.Latest = a.latest
			e.TimeQuality = a.quality
		} else if e.Timing.SourceTime != nil {
			e.Estimated = e.Timing.SourceTime
			e.TimeQuality = "estimated"
		} else {
			v := e.Accepted
			e.Estimated = &v
			e.TimeQuality = "unknown"
		}
	}
	return ev, nil
}

// orderTimeline retains causal edges even when a clock interval contradicts them.
// The caller bounds the graph to 1000 events; pairwise interval edges are bounded.
func orderTimeline(ev []Event) ([]Event, bool, error) {
	byID := map[string]int{}
	for i, e := range ev {
		byID[e.ID] = i
	}
	hard := map[[2]int]bool{}
	for i, e := range ev {
		for _, parent := range e.Timing.Parents {
			j, ok := byID[parent]
			if !ok {
				return nil, false, fail(422, "missing_dependency")
			}
			if j == i {
				return nil, false, fail(422, "causal_cycle")
			}
			hard[[2]int{j, i}] = true
		}
	}
	streams := map[string][]int{}
	for i, e := range ev {
		if e.Timing.Origin != "" {
			streams[e.Timing.Origin] = append(streams[e.Timing.Origin], i)
		}
	}
	for _, stream := range streams {
		sort.Slice(stream, func(i, j int) bool { return ev[stream[i]].Timing.Stream < ev[stream[j]].Timing.Stream })
		for i := 1; i < len(stream); i++ {
			hard[[2]int{stream[i-1], stream[i]}] = true
		}
	}
	all := map[[2]int]bool{}
	for edge := range hard {
		all[edge] = true
	}
	for i, a := range ev {
		if a.TimeQuality != "bounded" || a.Latest == nil {
			continue
		}
		for j, b := range ev {
			if i != j && b.TimeQuality == "bounded" && b.Earliest != nil && a.Latest.Before(*b.Earliest) {
				all[[2]int{i, j}] = true
			}
		}
	}
	sortGraph := func(edges map[[2]int]bool) ([]Event, bool) {
		degree := make([]int, len(ev))
		adj := make([][]int, len(ev))
		for edge := range edges {
			adj[edge[0]] = append(adj[edge[0]], edge[1])
			degree[edge[1]]++
		}
		ready := []int{}
		for i, d := range degree {
			if d == 0 {
				ready = append(ready, i)
			}
		}
		out := []Event{}
		less := func(i, j int) bool {
			a, b := ev[ready[i]], ev[ready[j]]
			at, bt := a.Accepted, b.Accepted
			if a.Estimated != nil {
				at = *a.Estimated
			}
			if b.Estimated != nil {
				bt = *b.Estimated
			}
			if !at.Equal(bt) {
				return at.Before(bt)
			}
			if a.Timing.Origin != b.Timing.Origin {
				return a.Timing.Origin < b.Timing.Origin
			}
			if a.Timing.Stream != b.Timing.Stream {
				return a.Timing.Stream < b.Timing.Stream
			}
			return a.ID < b.ID
		}
		for len(ready) > 0 {
			sort.Slice(ready, less)
			n := ready[0]
			ready = ready[1:]
			e := ev[n]
			if len(e.Timing.Parents) > 0 {
				e.OrderingBasis = "causal"
			} else if e.TimeQuality == "bounded" {
				e.OrderingBasis = "time_interval"
			} else {
				e.OrderingBasis = "estimated_or_fallback_unproven"
			}
			out = append(out, e)
			for _, j := range adj[n] {
				degree[j]--
				if degree[j] == 0 {
					ready = append(ready, j)
				}
			}
		}
		return out, len(out) == len(ev)
	}
	out, ok := sortGraph(all)
	if ok {
		return out, false, nil
	}
	out, ok = sortGraph(hard)
	if !ok {
		return nil, false, fail(422, "causal_cycle")
	}
	for i := range out {
		if out[i].TimeQuality == "bounded" {
			out[i].TimeQuality = "inconsistent"
			out[i].Earliest = nil
			out[i].Latest = nil
			out[i].OrderingBasis = "causal_clock_conflict"
		}
	}
	return out, true, nil
}

func (s *Service) Timeline(ctx context.Context, p auth.Principal, id, token string) (map[string]any, error) {
	tx, err := s.read(ctx, p)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var currentW, policy Seq
	err = tx.QueryRow(ctx, `SELECT committed_seq,policy_epoch FROM user_stream_heads WHERE tenant_id=$1 AND user_id=$2`, p.Tenant, p.User).Scan(&currentW, &policy)
	if err != nil {
		return nil, err
	}
	c := timelineCursor{Tenant: p.Tenant, User: p.User, Device: p.Device, Conversation: id, Algorithm: timelineAlgorithm, Watermark: currentW, Policy: policy, Expires: time.Now().Add(time.Hour).Unix()}
	if token != "" {
		c, err = s.timelineDecode(p, id, token)
		if err != nil {
			return nil, err
		}
		if c.Policy != policy {
			return nil, fail(410, "timeline_reset_required")
		}
		if c.Watermark > currentW {
			return nil, fail(400, "cursor_invalid")
		}
	}
	var contextID string
	err = tx.QueryRow(ctx, `SELECT context_id FROM conversations WHERE tenant_id=$1 AND user_id=$2 AND conversation_id=$3 AND deleted_at IS NULL`, p.Tenant, p.User, id).Scan(&contextID)
	if err != nil {
		return nil, fail(404, "not_found")
	}
	ev, err := events(ctx, tx, p, `SELECT e.event_id FROM conversation_events e JOIN context_sources cs USING(tenant_id,user_id,conversation_id) JOIN conversations cv USING(tenant_id,user_id,conversation_id) WHERE e.tenant_id=$1 AND e.user_id=$2 AND cs.context_id=$3 AND cs.bound_at_seq<=$4 AND (cs.removed_at_seq IS NULL OR cs.removed_at_seq>$4) AND cv.deleted_at IS NULL AND e.deleted_at IS NULL AND e.user_seq<=$4 ORDER BY e.user_seq,e.conv_seq LIMIT 1001`, p.Tenant, p.User, contextID, c.Watermark)
	if err != nil {
		return nil, err
	}
	if len(ev) > 1000 {
		return nil, fail(413, "timeline_too_large")
	}
	ev, err = assess(ctx, tx, p, ev, c.Watermark)
	if err != nil {
		return nil, err
	}
	ev, review, err := orderTimeline(ev)
	if err != nil {
		return nil, err
	}
	visible := []Event{}
	for _, e := range ev {
		if e.Conversation == id && e.Type != "event_time_assessed" {
			visible = append(visible, e)
		}
	}
	if c.Position > len(visible) {
		return nil, fail(400, "cursor_invalid")
	}
	end := c.Position + 100
	if end > len(visible) {
		end = len(visible)
	}
	page := visible[c.Position:end]
	next := ""
	if end < len(visible) {
		c.Position = end
		next = s.timelineToken(c)
	}
	return map[string]any{"events": page, "next_cursor": next, "has_more": next != "", "as_of_user_seq": c.Watermark, "policy_epoch": policy, "algorithm_version": timelineAlgorithm, "needs_clock_review": review, "incomplete": false, "independent_order_may_be_uncertain": true}, nil
}
