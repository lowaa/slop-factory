package change

import (
	"context"
	"fmt"
	"path"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// deployOnlyDrift is a head behind the base only by commits to its repo's
// deploy-only files, which the gate lets merge without taking the base in.
type deployOnlyDrift struct {
	// files is how many files the base changed since the head's merge base.
	files int
	// tipSHA is the base's tip the drift was read up to.
	tipSHA string
}

// deployOnly decides whether a head that does not contain the base's tip may
// merge anyway. CI committing image tags to the base moves it many times a
// day, and taking each one in costs a full run of the checks for a tree no
// check reads differently.
//
// It holds only when everything the base changed since the head's merge base
// is a file the repo's manifest entry names as deploy-only, changed only in
// lines its rule allows, and the change itself touches none of those files:
// then the tree that lands is the tested head's own changes plus deploy
// state. Everything else -- no rules, an approval that covers the head only
// by its diff, a comparison that cannot be read whole, a rename, a file or
// line no rule allows -- is behind_base, with why in the error. It is called
// only once the head is known to be behind; detail is that why, for the
// audit.
func (s *Service) deployOnly(ctx context.Context, tenant, agentID uuid.UUID, changes codehost.Changes, rec *Record, headSHA string, tgt *target, exactHead bool) (drift *deployOnlyDrift, detail string, err error) {
	refuse := func(format string, args ...any) (*deployOnlyDrift, string, error) {
		why := fmt.Sprintf(format, args...)
		return nil, why, fmt.Errorf("%w (not only deploy-only files: %s)", behindBase(tgt.base), why)
	}
	rules := tgt.deployOnly
	if len(rules) == 0 {
		return nil, "", behindBase(tgt.base)
	}
	// A head the human was not shown is vouched for by its diff against the
	// tip, which sameDiff requires the head to contain.
	if !exactHead {
		return refuse("the approval covers this head only by its diff against the base's tip")
	}
	url := s.hosts.ChangeURL(rec.Change)
	diff, err := changes.Diff(ctx, tenant, rec.Change, headSHA)
	if err != nil {
		s.record(ctx, audit.ChangeOperationFailed, s.attrs(tenant, agentID, rec.TicketID, "merge_drift", "change", url, "head_sha", headSHA, "err", err.Error())...)
		return refuse("the change's own files could not be read: %v", err)
	}
	if diff.Base != tgt.base {
		return nil, "", fmt.Errorf("%w: it targets %q on the host, not %q", ErrWrongBase, diff.Base, tgt.base)
	}
	if diff.ContainsTip() {
		// The comparison said behind; a base that is no longer ahead was
		// rewritten in between, and nothing read so far describes it.
		return refuse("the base changed while it was compared")
	}
	base, err := changes.CompareCommits(ctx, tenant, rec.Change, diff.MergeBaseSHA, diff.BaseSHA)
	if err != nil {
		s.record(ctx, audit.ChangeOperationFailed, s.attrs(tenant, agentID, rec.TicketID, "merge_drift", "change", url, "head_sha", headSHA, "err", err.Error())...)
		return refuse("what the base changed since %s could not be read: %v", diff.MergeBaseSHA, err)
	}
	if base.BaseSHA != diff.MergeBaseSHA || base.MergeBaseSHA != diff.MergeBaseSHA {
		return refuse("the base's history since %s could not be compared", diff.MergeBaseSHA)
	}
	ours := make(map[string]bool, len(diff.Files))
	for _, f := range diff.Files {
		moved := f.Status == codehost.DiffRenamed || f.Status == codehost.DiffCopied
		if !f.Status.DescribedByPatch() && f.Status != codehost.DiffModeChanged && !moved {
			return refuse("this change has a file the host reports in a status that cannot be checked: %s", f.Path)
		}
		if moved && f.PreviousPath == "" {
			return refuse("this change %s %s from a file the host did not name", fileMove(&f), f.Path)
		}
		// A file renamed or copied away from a deploy-only path would carry
		// the base's change to it into the merge, under a name no rule was
		// asked about, and one moved onto such a path would hide the base's
		// change to what was there.
		for _, p := range []string{f.PreviousPath, f.Path} {
			if !f.Status.DescribedByPatch() && p != "" && deployOnlyRuleFor(rules, p) != nil {
				return refuse("this change %s %s, a deploy-only file", fileMove(&f), p)
			}
		}
		ours[f.Path] = true
		if f.PreviousPath != "" {
			ours[f.PreviousPath] = true
		}
	}
	for i := range base.Files {
		if why := deployOnlyFile(&base.Files[i], rules, ours); why != "" {
			return refuse("%s", why)
		}
	}
	return &deployOnlyDrift{files: len(base.Files), tipSHA: diff.BaseSHA}, "", nil
}

// deployOnlyFile is why one file the base changed is not deploy drift the
// change may lag, or "" when it is.
func deployOnlyFile(f *codehost.FileDiff, rules []scope.DeployOnlyRule, ours map[string]bool) string {
	// Only a rename between two files of the same rule is drift: a copy or
	// a mode change leaves content or permissions no rule speaks for.
	switch f.Status {
	case codehost.DiffRenamed:
		if f.PreviousPath == "" {
			return fmt.Sprintf("%s was renamed from a file the host did not name", f.Path)
		}
		from := deployOnlyRuleFor(rules, f.PreviousPath)
		if from == nil || from != deployOnlyRuleFor(rules, f.Path) {
			return fmt.Sprintf("%s was renamed from %s, which is not a deploy-only file of the same rule", f.Path, f.PreviousPath)
		}
		if ours[f.PreviousPath] {
			return fmt.Sprintf("%s is deploy-only, and this change edits it too", f.PreviousPath)
		}
	case codehost.DiffCopied, codehost.DiffModeChanged, codehost.DiffOther:
		return fmt.Sprintf("%s was copied, changed in mode or changed in a way the host did not describe", f.Path)
	}
	rule := deployOnlyRuleFor(rules, f.Path)
	if rule == nil {
		return fmt.Sprintf("%s is not a deploy-only file", f.Path)
	}
	if ours[f.Path] {
		return fmt.Sprintf("%s is deploy-only, and this change edits it too", f.Path)
	}
	re, err := rule.LinesRegexp()
	if err != nil {
		return fmt.Sprintf("%s: its rule's lines do not compile: %v", f.Path, err)
	}
	if re == nil {
		return ""
	}
	lines, err := codehost.ChangedLines(f.Patch)
	if err != nil {
		return fmt.Sprintf("%s: its patch cannot be checked against its rule: %v", f.Path, err)
	}
	for _, line := range lines {
		if !re.MatchString(line) {
			return fmt.Sprintf("%s changes a line its rule does not allow: %q", f.Path, clip(line))
		}
	}
	return ""
}

// fileMove is what happened to a file that a patch alone does not describe,
// as a past participle for the refusal.
func fileMove(f *codehost.FileDiff) string {
	switch f.Status {
	case codehost.DiffRenamed:
		return "renamed"
	case codehost.DiffCopied:
		return "copied"
	case codehost.DiffModeChanged:
		return "changed the mode of"
	}
	return "changed in an unknown way"
}

// deployOnlyRuleFor is the first rule whose pattern matches the file.
func deployOnlyRuleFor(rules []scope.DeployOnlyRule, file string) *scope.DeployOnlyRule {
	for i := range rules {
		if ok, err := path.Match(rules[i].Path, file); err == nil && ok {
			return &rules[i]
		}
	}
	return nil
}

func clip(s string) string {
	if r := []rune(s); len(r) > 80 {
		return string(r[:80]) + "…"
	}
	return s
}
