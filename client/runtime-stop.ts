export type StopOutput={decision?:'block';reason?:string;systemMessage?:string};

/**
 * The Stop/SubagentStop policy is harness-independent. Only the remediation
 * commands differ, because a Codex session must run `forge codex retry` and a
 * Claude session must run `forge claude retry`. The policy never grants
 * authority: it asks the agent to settle work that forged already knows about.
 */
export function stopDecision(state:any,input:any,policy:'strict'|'advisory'='strict',command='forge codex'):StopOutput{
 if(!state.active&&!state.pending&&!state.unknown)return {};
 const reason=state.pending?`Forge has an unresolved execution request. Run ${command} retry before claiming success.`:`Forge still holds ${state.issue||'an issue'}. Record truthful evidence and close it if complete, or release it when handing off. Use ${command} pause before waiting for user input.`;
 const waiting=/(?:need|await|waiting).*(?:your|user).*(?:approval|confirmation|input)|需要.*(?:你|用户).*(?:确认|批准|输入)/i.test(String(input.last_assistant_message??''));
 if(policy==='strict'&&!state.paused&&!input.stop_hook_active&&!waiting)return {decision:'block',reason};
 return {systemMessage:reason+' Automatic renewal is paused; the lease will expire unless this thread resumes safely.'};
}
