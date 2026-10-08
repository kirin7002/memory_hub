package hub

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

type Seq int64

func (s Seq) MarshalJSON() ([]byte, error) { return json.Marshal(strconv.FormatInt(int64(s), 10)) }
func (s *Seq) UnmarshalJSON(b []byte) error {
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return fmt.Errorf("sequence must be a decimal string")
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return fmt.Errorf("invalid sequence")
	}
	*s = Seq(n)
	return nil
}

type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string           { return e.Code + ": " + e.Message }
func fail(status int, code string) error { return &Error{status, code, code} }

type Slot struct {
	Value       string   `json:"value"`
	Status      string   `json:"status"`
	Sources     []string `json:"source_event_ids"`
	Basis       string   `json:"confirmation_basis,omitempty"`
	ConfirmedAt Seq      `json:"confirmed_at_user_seq,omitempty"`
}
type Proposal struct {
	ID      string   `json:"proposal_id"`
	Field   string   `json:"field"`
	Value   string   `json:"value"`
	Sources []string `json:"source_event_ids"`
	Reason  string   `json:"reason"`
}
type State struct {
	Schema        int             `json:"schema_version"`
	TaskType      string          `json:"task_type"`
	Slots         map[string]Slot `json:"slots"`
	Proposals     []Proposal      `json:"pending_proposals"`
	Status        string          `json:"task_status"`
	PendingInputs []string        `json:"pending_interpretation_event_ids"`
}
type Context struct {
	ID        string  `json:"context_id"`
	Scope     string  `json:"scope_type"`
	TaskID    *string `json:"task_id,omitempty"`
	Version   Seq     `json:"context_version"`
	Source    Seq     `json:"source_revision"`
	Summary   Seq     `json:"summary_version"`
	Target    string  `json:"execution_target"`
	State     State   `json:"state"`
	Token     Seq     `json:"-"`
	ActiveRun *string `json:"-"`
}
type Conversation struct {
	ID        string `json:"conversation_id"`
	ContextID string `json:"context_id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Head      Seq    `json:"head_seq"`
}
type Timing struct {
	Schema      int        `json:"schema_version"`
	Origin      string     `json:"origin_id"`
	Boot        string     `json:"boot_id"`
	Segment     string     `json:"clock_segment_id"`
	Stream      Seq        `json:"stream_seq"`
	Mono        Seq        `json:"monotonic_ns"`
	SourceTime  *time.Time `json:"source_time,omitempty"`
	Calibration *string    `json:"calibration_ref,omitempty"`
	Parents     []string   `json:"causal_parent_event_ids"`
}
type Event struct {
	ID            string     `json:"event_id"`
	Conversation  string     `json:"conversation_id"`
	ContextID     string     `json:"context_id"`
	UserSeq       Seq        `json:"user_seq"`
	ConvSeq       Seq        `json:"conv_seq"`
	Type          string     `json:"type"`
	Actor         string     `json:"actor_type"`
	Device        *string    `json:"device_id,omitempty"`
	Status        string     `json:"status"`
	Text          string     `json:"text"`
	Timing        Timing     `json:"timing"`
	Accepted      time.Time  `json:"accepted_at"`
	Observed      Seq        `json:"observed_context_version"`
	Revalidation  bool       `json:"requires_revalidation"`
	Estimated     *time.Time `json:"estimated_occurred_at,omitempty"`
	Earliest      *time.Time `json:"earliest_at,omitempty"`
	Latest        *time.Time `json:"latest_at,omitempty"`
	TimeQuality   string     `json:"time_quality,omitempty"`
	OrderingBasis string     `json:"ordering_basis,omitempty"`
}
type Message struct {
	ID       string `json:"event_id"`
	Text     string `json:"text"`
	Observed Seq    `json:"observed_context_version"`
	Timezone string `json:"timezone"`
	Offline  bool   `json:"offline"`
	Timing   Timing `json:"timing"`
}
type Operation struct {
	Kind           string `json:"op"`
	ContextID      string `json:"context_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	Value          any    `json:"value"`
}
type Change struct {
	Seq        Seq         `json:"user_seq"`
	Type       string      `json:"change_type"`
	Policy     Seq         `json:"policy_epoch"`
	Operations []Operation `json:"operations"`
}
type Receipt struct {
	ID             string `json:"request_id"`
	Seq            Seq    `json:"user_seq"`
	ContextVersion Seq    `json:"context_version,omitempty"`
	Value          any    `json:"value,omitempty"`
}
type CreateConversation struct {
	ID        string `json:"conversation_id"`
	ContextID string `json:"context_id,omitempty"`
	Title     string `json:"title"`
	TaskType  string `json:"task_type,omitempty"`
}
type CreateContext struct {
	ID       string `json:"context_id"`
	TaskType string `json:"task_type"`
}
type Command struct {
	ID         string `json:"command_id"`
	Expected   Seq    `json:"expected_context_version"`
	Name       string `json:"command"`
	ProposalID string `json:"proposal_id,omitempty"`
	Target     string `json:"execution_target,omitempty"`
}
type RunRequest struct {
	ID       string `json:"request_id"`
	Expected Seq    `json:"expected_context_version"`
	Token    Seq    `json:"token"`
}
type Run struct {
	ID        string    `json:"run_id"`
	ContextID string    `json:"context_id"`
	Version   Seq       `json:"accepted_context_version"`
	Token     Seq       `json:"token,omitempty"`
	Status    string    `json:"status"`
	Lease     time.Time `json:"lease_until"`
	Owner     string    `json:"-"`
	Device    string    `json:"-"`
	Source    Seq       `json:"source_revision"`
	Policy    Seq       `json:"policy_epoch"`
}
type Interpretation struct {
	RunRequest
	Inputs  []string   `json:"input_event_ids"`
	Patches []Proposal `json:"patches"`
}
type Result struct {
	RunRequest
	EventID string `json:"event_id"`
	Text    string `json:"text"`
	Timing  Timing `json:"timing"`
}
type Execution struct {
	ID           string         `json:"execution_id"`
	Conversation string         `json:"conversation_id"`
	ContextID    string         `json:"context_id"`
	Base         Seq            `json:"base_context_version"`
	Source       Seq            `json:"base_source_revision"`
	Policy       Seq            `json:"base_policy_epoch"`
	Events       []OfflineEvent `json:"events"`
	Patches      []Proposal     `json:"patches"`
}
type OfflineEvent struct {
	Message
	Type string `json:"type"`
}
type Resolution struct {
	EventID    string  `json:"event_id"`
	Status     string  `json:"resolution"`
	Reason     string  `json:"reason_code"`
	ProposalID *string `json:"proposal_id,omitempty"`
}

func initialState(task string) State {
	return State{Schema: 1, TaskType: task, Slots: map[string]Slot{}, Proposals: []Proposal{}, Status: "active", PendingInputs: []string{}}
}
