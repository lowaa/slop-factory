package variantcompare

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// measure reads one figure of an attempt; ok is false when it is not known,
// which is never zero.
type measure struct {
	info MeasureInfo
	read func(a *attemptData) (v float64, ok bool)
}

func figure(key, group, unit, doc string, read func(a *attemptData) (float64, bool)) measure {
	return measure{info: MeasureInfo{Key: key, Group: group, Unit: unit, Doc: doc}, read: read}
}

func known(n int64) (float64, bool) { return float64(n), true }

func ptr64(p *int64) (float64, bool) {
	if p == nil {
		return 0, false
	}
	return float64(*p), true
}

func ptrInt(p *int) (float64, bool) {
	if p == nil {
		return 0, false
	}
	return float64(*p), true
}

func countFlagged(a *attemptData, pick func(ticket.SignalOwnTestBody) bool) (float64, bool) {
	if a.signals == nil || len(a.signals.OwnTests) == 0 {
		return 0, false
	}
	n := 0
	for _, t := range a.signals.OwnTests {
		if pick(t) {
			n++
		}
	}
	return float64(n), true
}

func files(a *attemptData, pick func(c *ticket.SignalCheatingBody) ticket.SignalFilesBody) (float64, bool) {
	if a.signals == nil {
		return 0, false
	}
	f := pick(&a.signals.Cheating)
	if !f.Known {
		return 0, false
	}
	return float64(len(f.Files)), true
}

func failedTests(a *attemptData, pick func(c *ticket.SignalChecksBody) *[]ticket.SignalFailedTestBody) (float64, bool) {
	if a.signals == nil {
		return 0, false
	}
	l := pick(&a.signals.Checks)
	if l == nil {
		return 0, false
	}
	return float64(len(*l)), true
}

// costMicros is the cost of the figures in one billing mode. The modes are
// never summed: metered is money spent, subscription an estimate.
func costMicros(figures []ticket.CostFigure, mode string, noneIsZero bool) (float64, bool) {
	var total int64
	seen := false
	for _, f := range figures {
		if f.BillingMode != mode {
			continue
		}
		// The cost covers the priced records only, so a figure with unpriced
		// ones is a lower bound, not a cost to take a difference of.
		if f.CostMicros == nil || f.UnpricedRecords > 0 {
			return 0, false
		}
		total += *f.CostMicros
		seen = true
	}
	if !seen {
		return 0, noneIsZero
	}
	return float64(total), true
}

func tokens(figures []ticket.CostFigure, out bool, noneIsZero bool) (float64, bool) {
	if len(figures) == 0 {
		return 0, noneIsZero
	}
	var total int64
	for _, f := range figures {
		if out {
			total += f.OutputTokens
		} else {
			total += f.InputTokens
		}
	}
	return float64(total), true
}

// measures are listed in the order the response shows them. Worker cost is
// unknown for an attempt with no usage reported; guidance charged to none is
// zero, since nothing was declared against the attempt.
var measures = []measure{
	figure("worker_input_tokens", GroupCost, "tokens", "Input tokens the attempt's workers used.",
		func(a *attemptData) (float64, bool) { return tokens(a.workerCost, false, false) }),
	figure("worker_output_tokens", GroupCost, "tokens", "Output tokens the attempt's workers produced.",
		func(a *attemptData) (float64, bool) { return tokens(a.workerCost, true, false) }),
	figure("worker_cost_metered_micros", GroupCost, "micro-dollars", "Money the workers spent on metered billing.",
		func(a *attemptData) (float64, bool) { return costMicros(a.workerCost, "metered", false) }),
	figure("worker_cost_subscription_micros", GroupCost, "micro-dollars", "API-equivalent estimate of subscription usage; not money spent.",
		func(a *attemptData) (float64, bool) { return costMicros(a.workerCost, "subscription", false) }),
	figure("guidance_input_tokens", GroupCost, "tokens", "Input tokens of guidance charged to the attempt by declaration.",
		func(a *attemptData) (float64, bool) { return tokens(a.guidanceCost, false, true) }),
	figure("guidance_output_tokens", GroupCost, "tokens", "Output tokens of guidance charged to the attempt by declaration.",
		func(a *attemptData) (float64, bool) { return tokens(a.guidanceCost, true, true) }),
	figure("guidance_cost_metered_micros", GroupCost, "micro-dollars", "Metered cost of the charged guidance.",
		func(a *attemptData) (float64, bool) { return costMicros(a.guidanceCost, "metered", true) }),
	figure("guidance_cost_subscription_micros", GroupCost, "micro-dollars", "Subscription estimate of the charged guidance.",
		func(a *attemptData) (float64, bool) { return costMicros(a.guidanceCost, "subscription", true) }),

	figure("human_comments", GroupHuman, "comments", "Comments people wrote in the attempt's conversation.",
		func(a *attemptData) (float64, bool) { return known(a.row.HumanComments) }),
	figure("human_words", GroupHuman, "words", "Words people wrote in the attempt's conversation.",
		func(a *attemptData) (float64, bool) { return known(a.row.HumanWords) }),
	figure("discussions", GroupHuman, "rounds", "Times the worker stopped with ticket_discuss.",
		func(a *attemptData) (float64, bool) {
			if a.signals == nil {
				return 0, false
			}
			return ptr64(a.signals.Autonomy.Discussions)
		}),
	figure("scope_extension_denials", GroupHuman, "denials", "Scope extensions refused; granted ones are not recorded.",
		func(a *attemptData) (float64, bool) {
			if a.signals == nil {
				return 0, false
			}
			return ptr64(a.signals.Autonomy.ScopeExtensionDenials)
		}),
	figure("blocked_handbacks", GroupHuman, "handbacks", "Times the worker handed the ticket back blocked.",
		func(a *attemptData) (float64, bool) {
			if a.signals == nil {
				return 0, false
			}
			return ptr64(a.signals.Autonomy.BlockedHandbacks)
		}),

	figure("checks_passed", GroupQuality, "1 yes, 0 no", "CI result on the change's head; unknown is absent.",
		func(a *attemptData) (float64, bool) {
			if a.signals == nil {
				return 0, false
			}
			switch a.signals.Checks.Passed {
			case "yes":
				return 1, true
			case "no":
				return 0, true
			}
			return 0, false
		}),
	figure("changes_requested", GroupQuality, "1 yes, 0 no", "Whether changes were requested on the attempt.",
		func(a *attemptData) (float64, bool) {
			if a.row.ChangesRequested {
				return 1, true
			}
			return 0, true
		}),

	figure("regressions", GroupSignals, "tests", "Tests that passed on the base and fail on the head.",
		func(a *attemptData) (float64, bool) {
			return failedTests(a, func(c *ticket.SignalChecksBody) *[]ticket.SignalFailedTestBody { return c.Regressions })
		}),
	figure("own_test_failures", GroupSignals, "tests", "The attempt's own tests failing on the head.",
		func(a *attemptData) (float64, bool) {
			return failedTests(a, func(c *ticket.SignalChecksBody) *[]ticket.SignalFailedTestBody { return c.OwnTestFailures })
		}),
	figure("own_tests_fail_on_base", GroupSignals, "files", "Test files the attempt added or changed that fail on the base.",
		func(a *attemptData) (float64, bool) {
			return countFlagged(a, func(t ticket.SignalOwnTestBody) bool { return t.Class == "fails-on-base" })
		}),
	figure("own_tests_flagged", GroupSignals, "files", "Test files the attempt added or changed that were flagged.",
		func(a *attemptData) (float64, bool) {
			return countFlagged(a, func(t ticket.SignalOwnTestBody) bool { return t.Flagged })
		}),
	figure("tests_deleted", GroupSignals, "files", "Test files deleted.",
		func(a *attemptData) (float64, bool) {
			return files(a, func(c *ticket.SignalCheatingBody) ticket.SignalFilesBody { return c.TestsDeleted })
		}),
	figure("skips_added", GroupSignals, "files", "Files where test skips were added.",
		func(a *attemptData) (float64, bool) {
			return files(a, func(c *ticket.SignalCheatingBody) ticket.SignalFilesBody { return c.SkipsAdded })
		}),
	figure("assertions_removed", GroupSignals, "files", "Files where assertions were removed.",
		func(a *attemptData) (float64, bool) {
			return files(a, func(c *ticket.SignalCheatingBody) ticket.SignalFilesBody { return c.AssertionsRemoved })
		}),
	figure("out_of_scope_files", GroupSignals, "files", "Files changed outside the ticket's scope.",
		func(a *attemptData) (float64, bool) {
			return files(a, func(c *ticket.SignalCheatingBody) ticket.SignalFilesBody { return c.OutOfScope })
		}),
	figure("reruns", GroupSignals, "runs", "CI runs after the first.",
		func(a *attemptData) (float64, bool) {
			if a.signals == nil {
				return 0, false
			}
			return ptrInt(a.signals.Checks.Reruns)
		}),
	figure("lines_changed", GroupSignals, "lines", "Lines added plus removed, generated files left out.",
		func(a *attemptData) (float64, bool) {
			if a.signals == nil || !a.signals.Size.Known {
				return 0, false
			}
			return float64(a.signals.Size.LinesAdded + a.signals.Size.LinesRemoved), true
		}),
	figure("runs", GroupSignals, "leases", "Leases the attempt was worked under.",
		func(a *attemptData) (float64, bool) {
			if a.signals == nil {
				return 0, false
			}
			return ptr64(a.signals.Time.Runs)
		}),
	figure("turns", GroupSignals, "requests", "Model requests across the attempt's runs.",
		func(a *attemptData) (float64, bool) {
			if a.signals == nil {
				return 0, false
			}
			return ptr64(a.signals.Time.Turns)
		}),
	figure("wall_clock_seconds", GroupSignals, "seconds", "First claim to the end of the last run, waiting on a person included.",
		func(a *attemptData) (float64, bool) {
			if a.signals == nil {
				return 0, false
			}
			return ptr64(a.signals.Time.WallClockSeconds)
		}),

	figure("human_wait_seconds", GroupNotCount, "seconds", "Time the attempt waited in discussion for a person. Shown, not counted as cost.",
		func(a *attemptData) (float64, bool) { return known(a.row.HumanWaitSeconds) }),
}

func measureInfos() []MeasureInfo {
	out := make([]MeasureInfo, len(measures))
	for i := range measures {
		out[i] = measures[i].info
	}
	return out
}
