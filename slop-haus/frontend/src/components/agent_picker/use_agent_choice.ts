import { useState } from "react";
import type { DecisionInput } from "@/api";

export type AgentChoice = "existing" | "new";

export interface AgentChoiceState {
  choice: AgentChoice;
  setChoice: (choice: AgentChoice) => void;
  agentId: string;
  setAgentId: (id: string) => void;
  agentName: string;
  setAgentName: (name: string) => void;
  // complete is whether an approval could be sent as things stand: an
  // existing agent actually picked, or a new one actually named.
  complete: boolean;
  approval: () => DecisionInput;
}

export function useAgentChoice(initialName = ""): AgentChoiceState {
  const [choice, setChoice] = useState<AgentChoice>("new");
  const [agentId, setAgentId] = useState("");
  const [agentName, setAgentName] = useState(initialName);

  const complete = choice === "existing" ? agentId !== "" : agentName.trim() !== "";
  const approval = (): DecisionInput =>
    choice === "existing" ? { approve: true, agent_id: agentId } : { approve: true, agent_name: agentName.trim() };

  return { choice, setChoice, agentId, setAgentId, agentName, setAgentName, complete, approval };
}
