package dsr

import (
	"time"

	"github.com/google/uuid"
)

// Type is what a requester is asking for. The GDPR types and the CCPA/CPRA
// ones are distinct values because the law that governs a request, and so
// its deadline, follows from which the requester invoked.
type Type string

const (
	TypeAccess        Type = "access"
	TypeRectification Type = "rectification"
	TypeErasure       Type = "erasure"
	TypeRestriction   Type = "restriction"
	TypeObjection     Type = "objection"
	TypePortability   Type = "portability"
	TypeCCPAKnow      Type = "ccpa_know"
	TypeCCPADelete    Type = "ccpa_delete"
	TypeCCPACorrect   Type = "ccpa_correct"
	TypeCCPAOptOut    Type = "ccpa_opt_out"
	TypeCCPALimit     Type = "ccpa_limit"
	// TypeOther is a request that does not say which right it invokes. It
	// takes the shorter regime's deadline: working to the stricter clock
	// can only be early.
	TypeOther Type = "other"
)

var typeRegimes = map[Type]Regime{
	TypeAccess: RegimeGDPR, TypeRectification: RegimeGDPR, TypeErasure: RegimeGDPR,
	TypeRestriction: RegimeGDPR, TypeObjection: RegimeGDPR, TypePortability: RegimeGDPR,
	TypeCCPAKnow: RegimeCCPA, TypeCCPADelete: RegimeCCPA, TypeCCPACorrect: RegimeCCPA,
	TypeCCPAOptOut: RegimeCCPA, TypeCCPALimit: RegimeCCPA,
	TypeOther: RegimeGDPR,
}

// Types lists every request type.
func Types() []Type {
	return []Type{TypeAccess, TypeRectification, TypeErasure, TypeRestriction, TypeObjection, TypePortability,
		TypeCCPAKnow, TypeCCPADelete, TypeCCPACorrect, TypeCCPAOptOut, TypeCCPALimit, TypeOther}
}

func (t Type) Valid() bool { _, ok := typeRegimes[t]; return ok }

// NeedsVerification is whether acting on the request discloses, changes or
// deletes someone's data, which has to be the requester's to ask for. An
// opt-out or limit request takes effect without proving identity.
func (t Type) NeedsVerification() bool {
	switch t {
	case TypeAccess, TypeRectification, TypeErasure, TypePortability, TypeCCPAKnow, TypeCCPADelete, TypeCCPACorrect:
		return true
	}
	return false
}

// Regime is the body of law whose deadline a request runs to.
type Regime string

const (
	RegimeGDPR Regime = "gdpr"
	RegimeCCPA Regime = "ccpa"
)

// Regime is the regime t's deadline comes from.
func (t Type) Regime() Regime { return typeRegimes[t] }

// Status is where a request is in its handling.
type Status string

const (
	StatusReceived Status = "received"
	// StatusVerifying is waiting on the requester to prove who they are.
	StatusVerifying  Status = "verifying"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
	StatusRefused    Status = "refused"
	StatusWithdrawn  Status = "withdrawn"
)

func (s Status) Valid() bool {
	switch s {
	case StatusReceived, StatusVerifying, StatusInProgress, StatusCompleted, StatusRefused, StatusWithdrawn:
		return true
	}
	return false
}

// Closed is whether no further handling happens: a closed request never
// reopens, a fresh one is logged instead.
func (s Status) Closed() bool {
	return s == StatusCompleted || s == StatusRefused || s == StatusWithdrawn
}

// Source is how a request reached the operator.
type Source string

const (
	SourcePublicForm Source = "public_form"
	SourceEmail      Source = "email"
	SourceOther      Source = "other"
)

func (s Source) Valid() bool { return s == SourcePublicForm || s == SourceEmail || s == SourceOther }

// VerificationMethod is how a requester's identity was established.
type VerificationMethod string

const (
	VerifiedEmailReply   VerificationMethod = "email_reply"
	VerifiedAccountLogin VerificationMethod = "account_login"
	VerifiedDocuments    VerificationMethod = "documents"
	VerifiedOther        VerificationMethod = "other"
)

func (m VerificationMethod) Valid() bool {
	switch m {
	case VerifiedEmailReply, VerifiedAccountLogin, VerifiedDocuments, VerifiedOther:
		return true
	}
	return false
}

// Request is one data subject request. It holds personal data: its
// requester, their email and what they wrote. Nothing here belongs in a log
// line.
type Request struct {
	ID             uuid.UUID
	RequesterName  string
	RequesterEmail string
	Type           Type
	Source         Source
	// AuthorisedAgent is a request made on someone's behalf. The CCPA
	// requires the agent's authority to be checked too, which the operator
	// records as verification.
	AuthorisedAgent    bool
	Details            string
	Verified           bool
	VerificationMethod VerificationMethod
	VerifiedAt         *time.Time
	Status             Status
	ReceivedAt         time.Time
	DueAt              time.Time
	Extended           bool
	ExtensionReason    string
	ClosedAt           *time.Time
	// AlertedSoonAt and AlertedOverdueAt are when the operator was told the
	// request is near, and past, its due date; set once each, so an alert
	// does not repeat however often the check runs.
	AlertedSoonAt    *time.Time
	AlertedOverdueAt *time.Time
}

// NewRequest is what is known when a request is logged.
type NewRequest struct {
	RequesterName   string
	RequesterEmail  string
	Type            Type
	Source          Source
	AuthorisedAgent bool
	Details         string
	// ReceivedAt is when the request reached the operator, which starts its
	// clock. Zero is now; an operator logging an email passes when it
	// arrived.
	ReceivedAt time.Time
	// Actor is who logged it, as the audit trail records.
	Actor string
}

// EventKind is what an audit event records.
type EventKind string

const (
	EventReceived  EventKind = "received"
	EventStatus    EventKind = "status_changed"
	EventVerified  EventKind = "verified"
	EventExtended  EventKind = "extended"
	EventNote      EventKind = "note"
	EventAlertSoon EventKind = "alert_due_soon"
	EventAlertLate EventKind = "alert_overdue"
)

// Event is one entry in a request's audit trail, appended and never
// changed. Detail is free text and can hold personal data.
type Event struct {
	ID         int64
	RequestID  uuid.UUID
	At         time.Time
	Actor      string
	Kind       EventKind
	FromStatus Status
	ToStatus   Status
	Detail     string
}

// AlertKind is which threshold an alert is for.
type AlertKind string

const (
	AlertDueSoon AlertKind = "due_soon"
	AlertOverdue AlertKind = "overdue"
)

// Alert is a request the operator has not yet been told about at a
// threshold.
type Alert struct {
	Kind    AlertKind
	Request *Request
	// ClaimedAt is the mark the claim left on the request; releasing the
	// alert undoes that mark and no other.
	ClaimedAt time.Time
}

// Filter narrows List.
type Filter struct {
	// OpenOnly leaves out closed requests.
	OpenOnly bool
	// Limit caps the rows; zero is DefaultListLimit.
	Limit int
}

// DefaultListLimit bounds a List that names no limit.
const DefaultListLimit = 200
