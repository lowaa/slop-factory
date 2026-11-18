import type { StewardDiscussions, StewardDiscussionScore } from "@/api";

export function score(
  over: Partial<StewardDiscussionScore> = {},
): StewardDiscussionScore {
  return {
    category: "merge_continue",
    decisions: 4,
    agreed: 3,
    disagreed: 1,
    agreement_rate: 0.75,
    warn_few_decisions: false,
    warn_low_agreement: false,
    ...over,
  };
}

const none = { decisions: 0, agreed: 0, disagreed: 0, agreement_rate: null };

export const NO_DISCUSSIONS: StewardDiscussions = {
  labels: [],
  labels_total: score({ category: "all", ...none }),
  answers: [],
  answers_total: score({ category: "all", ...none }),
  label_disagreements: [],
  answer_disagreements: [],
};

export function discussions(
  over: Partial<StewardDiscussions> = {},
): StewardDiscussions {
  return {
    ...NO_DISCUSSIONS,
    labels: [
      score(),
      score({
        category: "yours",
        decisions: 2,
        agreed: 1,
        disagreed: 1,
        agreement_rate: 0.5,
        warn_few_decisions: true,
        warn_low_agreement: true,
      }),
    ],
    labels_total: score({
      category: "all",
      decisions: 6,
      agreed: 4,
      disagreed: 2,
      agreement_rate: 4 / 6,
    }),
    answers: [score()],
    answers_total: score({ category: "all" }),
    ...over,
  };
}

export const EDITED_DRAFT: StewardDiscussions["answer_disagreements"][number] =
  {
    category: "merge_continue",
    ticket_id: 12,
    attempt: 1,
    round: 2,
    how: "edit",
    original_body: "Continue with the merge.",
    final_body: "Continue, but wait for the migration first.",
    reason: "needs the migration",
    at: "2026-01-01T11:00:00Z",
  };
