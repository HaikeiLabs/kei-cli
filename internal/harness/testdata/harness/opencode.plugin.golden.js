// managed by kei harness sync
const report = async (payload) => { try { const child = Bun.spawn(['kei-proxy', 'hook', 'opencode'], { stdin: 'pipe', stdout: 'ignore', stderr: 'ignore' }); child.stdin.write(JSON.stringify(payload)); child.stdin.end(); } catch {} };
const sessionEvents = { 'session.created': 'SessionStart', 'session.idle': 'Stop', 'session.deleted': 'SessionEnd' };
const replies = { once: 'approved', always: 'approved', reject: 'denied' };
const asks = new Map();
const remember = (permission) => { try { if (!permission?.id) return; if (asks.size >= 256) asks.delete(asks.keys().next().value); asks.set(permission.id, { tool_name: permission.type, tool_use_id: permission.callID }); } catch {} };
const onEvent = (event) => {
  const lifecycle = sessionEvents[event?.type];
  if (lifecycle) { const p = event.properties ?? {}; void report({ hook_event_name: lifecycle, session_id: p.info?.id ?? p.sessionID }); return; }
  if (event?.type !== 'permission.replied') return;
  const p = event.properties ?? {};
  const ask = asks.get(p.permissionID);
  if (!ask) return;
  asks.delete(p.permissionID);
  void report({ phase: 'permission_reply', tool_name: ask.tool_name, session_id: p.sessionID, tool_use_id: ask.tool_use_id, native_decision: replies[p.response] ?? 'unknown' });
};
export const KeiAudit = async () => ({
  'tool.execute.before': async (input, output) => { void report({ phase: 'pre', tool_name: input?.tool, session_id: input?.sessionID, tool_use_id: input?.callID, tool_input: output?.args }); },
  'tool.execute.after': async (input, output) => { void report({ phase: 'post', tool_name: input?.tool, session_id: input?.sessionID, tool_use_id: input?.callID, tool_input: input?.args }); },
  'permission.ask': async (input, output) => { remember(input); void report({ phase: 'ask', tool_name: input?.type, session_id: input?.sessionID, tool_use_id: input?.callID, native_decision: output?.status }); },
  event: async (input) => { try { onEvent(input?.event); } catch {} },
});
