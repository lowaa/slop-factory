import { describe, expect, it } from "vitest";
import { makeProposal } from "@/test/fixtures";
import { proposalsForConversation } from "./proposals_for";

const mine7 = makeProposal({ id: "a", agent_name: "mine", ticket_id: 7 });
const mine8 = makeProposal({ id: "b", agent_name: "mine", ticket_id: 8 });
const other7 = makeProposal({ id: "c", agent_name: "someone-else", ticket_id: 7 });

describe("proposalsForConversation", () => {
  it("shows every proposal the remote control made, and nobody else's", () => {
    expect(proposalsForConversation([mine7, mine8, other7], "mine")).toEqual([mine7, mine8]);
  });
});
