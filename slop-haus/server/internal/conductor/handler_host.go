package conductor

import (
	"context"
	"encoding/json"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

var hostErrTable = httperr.Table{
	Rules: []httperr.Rule{
		{Err: ErrNotConductor, Kind: httperr.Forbidden, Message: "only a conductor's host may do that"},
	},
	Unexpected: "authenticate conductor host",
}

// hostCaller is the conductor whose host is calling. Whether the credential
// is a conductor's is the service's to say; this maps its refusal.
func (h *Handler) hostCaller(ctx context.Context) (*Host, error) {
	host, err := h.svc.AuthenticateHost(ctx)
	if err != nil {
		return nil, httperr.Map(h.logger, err, &hostErrTable)
	}
	return host, nil
}

// WorkItemBody is one item of a conductor's queue. Which fields are set
// follows type.
// WorkAttachmentBody is a staged file a message carries.
type WorkAttachmentBody struct {
	ID          string `json:"id" format:"uuid"`
	Filename    string `json:"filename" doc:"the client's name for the file: display only, never a path"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

type WorkItemBody struct {
	Seq          int64  `json:"seq" doc:"Acknowledge by polling with ack set to the highest handled; handle in order and skip a seq handled already"`
	Type         string `json:"type" enum:"start,message,stop,interrupt,clear,compact"`
	Conversation string `json:"conversation" doc:"The conversation, whose id is the harness session id"`
	// start
	Resume bool `json:"resume,omitempty" doc:"the session exists: resume it rather than create it"`
	// message
	MessageSeq  *int64               `json:"message_seq,omitempty"`
	Text        string               `json:"text,omitempty"`
	Attachments []WorkAttachmentBody `json:"attachments,omitempty" doc:"files the owner staged with the message: fetch each with GET /api/agent/attachments/{id}"`
	// interrupt
	Turn *int64 `json:"turn,omitempty" doc:"the turn to stop: act only if it is the conversation's current one"`
}

func workItemBody(w *WorkItem) WorkItemBody {
	b := WorkItemBody{Seq: w.Seq, Type: string(w.Kind), Conversation: w.Conversation.String()}
	switch w.Kind {
	case WorkStart:
		b.Resume = w.Resume
	case WorkMessage:
		b.MessageSeq, b.Text = w.MessageSeq, w.Text
		for _, a := range w.Attachments {
			b.Attachments = append(b.Attachments, WorkAttachmentBody{ID: a.ID.String(), Filename: a.Filename, ContentType: a.ContentType, SizeBytes: a.SizeBytes})
		}
	case WorkInterrupt:
		b.Turn = w.Turn
	}
	return b
}

type PollRequestBody struct {
	HostID       string   `json:"host_id" format:"uuid" doc:"The host process, chosen by it at startup. A second one is refused while the first polls"`
	Ack          int64    `json:"ack,omitempty" minimum:"0" doc:"The highest item seq handled; items up to it are not sent again"`
	WaitSeconds  int      `json:"wait_seconds,omitempty" minimum:"0" maximum:"60" doc:"How long to hold the request open when there is no work (capped by the server)"`
	Capabilities []string `json:"capabilities,omitempty" maxItems:"20" doc:"What this host can do (e.g. interrupt); the board offers an action only to a host that reports it. A host that sends none is taken to be on older code"`
	Version      string   `json:"version,omitempty" maxLength:"64" doc:"The host's own version, for the board to show"`
}

type PollConductorWorkRequest struct {
	Body PollRequestBody
}

type PollConductorWorkResponse struct {
	Body struct {
		Reason string         `json:"reason" enum:"work,timeout,restart" doc:"restart: the server is deploying; poll again"`
		Items  []WorkItemBody `json:"items"`
		Acked  int64          `json:"acked" doc:"The highest seq acknowledged so far"`
	}
}

// PollConductorWork is the host's long poll: its queue's unacknowledged
// items, in order, waiting for some when there are none. At-least-once: see
// Service.Poll.
func (h *Handler) PollConductorWork(ctx context.Context, input *PollConductorWorkRequest) (*PollConductorWorkResponse, error) {
	host, err := h.hostCaller(ctx)
	if err != nil {
		return nil, err
	}
	hostID, err := parseID(input.Body.HostID, "host_id")
	if err != nil {
		return nil, err
	}
	res, err := h.svc.Poll(ctx, host, &PollRequest{
		HostID: hostID, Ack: input.Body.Ack, Wait: time.Duration(input.Body.WaitSeconds) * time.Second,
		Capabilities: input.Body.Capabilities, Version: input.Body.Version,
	})
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &PollConductorWorkResponse{}
	resp.Body.Reason, resp.Body.Acked = string(res.Reason), res.Acked
	resp.Body.Items = make([]WorkItemBody, len(res.Items))
	for i := range res.Items {
		resp.Body.Items[i] = workItemBody(&res.Items[i])
	}
	return resp, nil
}

type ReleaseConductorRequest struct {
	Body struct {
		HostID string `json:"host_id" format:"uuid" doc:"The host process giving up the conductor"`
	}
}

// ReleaseConductor is a host that is exiting letting go of its conductor: see
// Service.Release. 204 whether or not it was still the host.
func (h *Handler) ReleaseConductor(ctx context.Context, input *ReleaseConductorRequest) (*struct{}, error) {
	host, err := h.hostCaller(ctx)
	if err != nil {
		return nil, err
	}
	hostID, err := parseID(input.Body.HostID, "host_id")
	if err != nil {
		return nil, err
	}
	if err := h.svc.Release(ctx, host, hostID); err != nil {
		return nil, h.mapError(err)
	}
	return nil, nil
}

// LayoutEntryBody is one directory of the host's workspace. Sanitised again
// by agent.ParseLayout: a host is self-asserting, like a hook script.
type LayoutEntryBody struct {
	Path   string `json:"path" maxLength:"255"`
	Git    bool   `json:"git"`
	Origin string `json:"origin,omitempty" maxLength:"500"`
}

type ReportLayoutRequest struct {
	Body struct {
		Root  LayoutEntryBody   `json:"root"`
		Repos []LayoutEntryBody `json:"repos" maxItems:"50"`
	}
}

// ReportLayout is the host saying what its workspace looks like on disk. A
// layout that does not parse is refused: unlike a hook, this call has no other
// job to protect.
func (h *Handler) ReportLayout(ctx context.Context, input *ReportLayoutRequest) (*struct{}, error) {
	host, err := h.hostCaller(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(map[string]any{"layout": input.Body})
	if err != nil {
		return nil, huma.Error500InternalServerError("internal server error")
	}
	layout := agent.ParseLayout(raw)
	if layout == nil {
		return nil, huma.Error422UnprocessableEntity("not a workspace layout: the root's path must be \".\"")
	}
	if err := h.svc.ReportLayout(ctx, host, layout); err != nil {
		h.logger.Error("record conductor workspace layout", "conductor_id", host.ConductorID(), "err", err)
		return nil, huma.Error500InternalServerError("could not record the workspace layout")
	}
	return nil, nil
}

// HostEventBody is one thing the host reports. Which of text, reason and
// repo is read follows kind.
type HostEventBody struct {
	HostSeq int64  `json:"host_seq" minimum:"1" doc:"The host's own number for it, per conversation, from 1 and without gaps"`
	Kind    string `json:"kind" enum:"assistant,turn_end,ready,failed,repo_state,activity,unsupported,context,context_cleared,context_compacted"`
	Turn    *int64 `json:"turn,omitempty"`
	// Interrupted is set on a turn_end whose turn ended because the owner
	// stopped it (an interrupt work item), however the host stopped it.
	Interrupted bool `json:"interrupted,omitempty"`
	// Text is a batch of the model's output, for assistant.
	Text string `json:"text,omitempty" maxLength:"65536"`
	// Block is the model message the text belongs to, for assistant.
	Block string `json:"block,omitempty" maxLength:"128"`
	// Activity is the tool call, for activity.
	Activity *Activity `json:"activity,omitempty"`
	// Reason is why the harness process could not start, for failed; the work kind the host does not know, for unsupported.
	Reason string `json:"reason,omitempty" maxLength:"2048"`
	// WorkSeq is the seq of the start item this ready or failed answers.
	// One that answers an earlier start than the conversation is on (a stopped
	// session's, arriving late) is not recorded.
	WorkSeq int64 `json:"work_seq,omitempty" minimum:"1" doc:"for ready and failed: the seq of the start item being answered; for repo_state: the one the session is on"`
	// Repo is where the repository stands after a turn, for repo_state.
	Repo *RepoStateBody `json:"repo,omitempty"`
	// ContextTokens and CanCompact are for context; a field left out is kept as it was.
	ContextTokens *int64 `json:"context_tokens,omitempty" minimum:"0" doc:"for context: the input side of the newest turn's usage (cache read + cache write + input); 0 is unknown"`
	CanCompact    *bool  `json:"can_compact,omitempty" doc:"for context: whether the harness can compact the session"`
}

func (e *HostEventBody) event() HostEvent {
	ev := HostEvent{HostSeq: e.HostSeq, Kind: EventKind(e.Kind), Turn: e.Turn, Interrupted: e.Interrupted, WorkSeq: e.WorkSeq,
		ContextTokens: e.ContextTokens, CanCompact: e.CanCompact}
	switch ev.Kind {
	case EventAssistant:
		ev.Body, ev.Block = e.Text, e.Block
	case EventActivity:
		if e.Activity != nil {
			ev.Activity = e.Activity
		}
	case EventFailed:
		ev.Body = e.Reason
	case EventUnsupported:
		ev.Body = e.Reason
	case EventRepoState:
		if e.Repo != nil {
			ev.Repo = e.Repo.state()
		}
	}
	return ev
}

type ReportEventsRequest struct {
	ConversationID string `path:"conversation_id"`
	Body           struct {
		HostID string          `json:"host_id" format:"uuid"`
		Events []HostEventBody `json:"events" maxItems:"200"`
	}
}

type ReportEventsResponse struct {
	Body struct {
		Acked int64 `json:"acked" doc:"The highest host_seq the server has. After a lost reply or a 409, send from acked+1"`
	}
}

// ReportConductorEvents stores what the host reports for one conversation,
// once and in order: see Service.Report. Usage is reported through
// /hooks/usage, tagged with the conversation.
func (h *Handler) ReportConductorEvents(ctx context.Context, input *ReportEventsRequest) (*ReportEventsResponse, error) {
	host, err := h.hostCaller(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(input.ConversationID, "conversation id")
	if err != nil {
		return nil, err
	}
	hostID, err := parseID(input.Body.HostID, "host_id")
	if err != nil {
		return nil, err
	}
	events := make([]HostEvent, len(input.Body.Events))
	for i := range input.Body.Events {
		events[i] = input.Body.Events[i].event()
	}
	acked, err := h.svc.Report(ctx, host, id, &ReportRequest{HostID: hostID, Events: events})
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ReportEventsResponse{}
	resp.Body.Acked = acked
	return resp, nil
}

type HostBody struct {
	Online         bool     `json:"online" doc:"whether a host has polled lately"`
	Capabilities   []string `json:"capabilities,omitempty" doc:"what the host that polled last said it can do; empty for a host on older code"`
	Version        string   `json:"version,omitempty" doc:"the version the host that polled last reported; empty for a host on older code"`
	CurrentVersion string   `json:"current_version,omitempty" doc:"the build id of the host package this server ships; a host that reports another version is behind it"`
}

type HostResponse struct {
	Body HostBody
}

// GetConductorHost is what the Remote Control page shows of a machine: whether
// its host is there.
func (h *Handler) GetConductorHost(ctx context.Context, input *conductorPath) (*HostResponse, error) {
	owned, err := h.authorize(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	status, err := h.svc.HostInfo(ctx, owned)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &HostResponse{Body: HostBody{Online: status.Online, Capabilities: nonNil(status.Capabilities), Version: status.Version, CurrentVersion: status.CurrentVersion}}, nil
}
