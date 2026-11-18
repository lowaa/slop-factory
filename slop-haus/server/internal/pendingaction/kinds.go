package pendingaction

// Subject is what a proposal is about.
type Subject string

const (
	// SubjectTicket proposals name a ticket (pending_actions.ticket_id) and are
	// checked against it when they are made and approved.
	SubjectTicket Subject = "ticket"
	// SubjectTenant proposals are about the tenant as a whole -- its workspace
	// manifest, say. They carry no ticket: the row's tenant is the subject.
	SubjectTenant Subject = "tenant"
)

// Role is the tenant role a proposal's approver must hold.
type Role string

const (
	// RoleMember is any member of the tenant.
	RoleMember Role = "member"
	// RoleOwner is a member who owns the tenant.
	RoleOwner Role = "owner"
)

// KindSpec is the rules that belong to a kind rather than to a proposal.
type KindSpec struct {
	Subject Subject
	// Approver is the least role that may approve or reject the kind.
	Approver Role
	// AutoApprovable says the proposer's owner may switch on applying the kind
	// as it is proposed. A kind that is not never is applied by a click.
	AutoApprovable bool
	// Normalise checks and cleans the payload of a tenant kind, which has no
	// ticket for the shared rules to check it against. A tenant kind without
	// one cannot be proposed.
	Normalise func(Payload) (Payload, error)
	// ActionLine says what approving a proposal of the tenant kind does, built
	// from its structured payload and never the proposer's words.
	ActionLine func(*Action) string
}

// ticketKind is the rule of the kinds about one ticket, which any member
// decides and which can be auto-approved.
var ticketKind = KindSpec{Subject: SubjectTicket, Approver: RoleMember, AutoApprovable: true}

// kindSpecs is the one table of kinds. The database holds the same split
// between ticket and ticket-less kinds as a CHECK
// (pending_actions_ticket_by_kind); a kind added here that has no ticket
// needs that CHECK, and the kind CHECK, widened with it.
var kindSpecs = map[Kind]KindSpec{
	KindDiscussionAnswer: ticketKind,
	KindReady:            ticketKind,
	KindStatus:           ticketKind,
	KindReviewDecision:   ticketKind,
	KindEdit:             ticketKind,
	// The manifest loosens guards (deploy-only rules the merge gate, shared
	// paths the scope exclusivity), so an owner alone decides it and no
	// auto-approve setting reaches it: an agent that could would choose its
	// own gate.
	// Closing a ticket as done on the code host's word is the owner's call
	// (ticket.ErrNotTenantOwner), and an agent that could close its own
	// tickets by proposing would make "done" its own word: never auto-approved.
	KindCloseMerged:  {Subject: SubjectTicket, Approver: RoleOwner},
	KindManifestEdit: {Subject: SubjectTenant, Approver: RoleOwner, Normalise: normaliseManifestEdit, ActionLine: manifestEditLine},
}

// Spec is the kind's rules. A kind with none gets the strictest: owners only,
// never auto-approved, so a kind forgotten here fails closed.
func (k Kind) Spec() KindSpec {
	if spec, ok := kindSpecs[k]; ok {
		return spec
	}
	return KindSpec{Subject: SubjectTenant, Approver: RoleOwner}
}

// AutoApprovableKinds are the kinds a person may switch auto-approve on for.
func AutoApprovableKinds() []Kind {
	var out []Kind
	for _, k := range Kinds {
		if k.Spec().AutoApprovable {
			out = append(out, k)
		}
	}
	return out
}
