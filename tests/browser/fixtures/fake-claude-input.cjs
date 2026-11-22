#!/usr/bin/env node
// A fake long-lived Claude Code process (claude -p --input-format
// stream-json) for browser tests: every user message on stdin is one turn
// that ends with a `result`.  "start background" leaves a background
// command, a background sub-agent and the ARK watcher running after the
// turn; "finish background" ends the command and the sub-agent.
const readline = require('node:readline');

let n = 0;
const out = (v) => { n += 1; process.stdout.write(`${JSON.stringify({ session_id: 'fake-input-0001', uuid: `u${n}`, ...v })}\n`); };
const say = (text) => out({ type: 'assistant', message: { content: [{ type: 'text', text }] } });
const result = () => out({ type: 'result', subtype: 'success', result: '', is_error: false });
const toolUse = (id, name, input) => out({ type: 'assistant', message: { content: [{ type: 'tool_use', id, name, input }] } });
const toolResult = (id, content) => out({ type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: id, content }] } });

out({ type: 'system', subtype: 'init' });
readline.createInterface({ input: process.stdin }).on('line', (line) => {
  let msg;
  try { msg = JSON.parse(line); } catch { return; }
  if (msg.type === 'control_request') {
    out({ type: 'control_response', response: { subtype: 'success', request_id: msg.request_id } });
    return;
  }
  if (msg.type !== 'user') return;
  const text = (msg.message?.content || []).filter((c) => c.type === 'text').map((c) => c.text).join('');
  say(`echo: ${text.split('\n')[0]}`);
  if (text.startsWith('start background')) {
    toolUse('toolu_build', 'Bash', { command: 'npm run build', description: 'Build the site', run_in_background: true });
    out({ type: 'system', subtype: 'task_started', task_id: 'sh_build', tool_use_id: 'toolu_build', description: 'Build the site', task_type: 'local_bash', is_backgrounded: true });
    toolResult('toolu_build', 'Command running in background with ID: sh_build');
    toolUse('toolu_watch', 'Bash', { command: 'sh ark-watch.sh', description: 'ARK update watcher', run_in_background: true });
    out({ type: 'system', subtype: 'task_started', task_id: 'sh_watch', tool_use_id: 'toolu_watch', description: 'ARK update watcher', task_type: 'local_bash', is_backgrounded: true });
    toolResult('toolu_watch', 'Command running in background with ID: sh_watch');
    toolUse('toolu_agent', 'Task', { description: 'Survey the code', subagent_type: 'general-purpose', prompt: 'survey', run_in_background: true });
    out({ type: 'system', subtype: 'task_started', task_id: 'a_survey', tool_use_id: 'toolu_agent', description: 'Survey the code', task_type: 'local_agent', is_backgrounded: true });
    toolResult('toolu_agent', 'Async agent launched. agentId: a_survey');
    say('Started the build and a survey in the background.');
  }
  if (text.startsWith('finish background')) {
    out({ type: 'system', subtype: 'task_notification', task_id: 'sh_build', tool_use_id: 'toolu_build', status: 'completed', summary: 'Build finished' });
    out({ type: 'system', subtype: 'task_notification', task_id: 'a_survey', tool_use_id: 'toolu_agent', status: 'completed', summary: 'Survey done' });
  }
  result();
});
setTimeout(() => process.exit(0), Number(process.env.FAKE_STREAM_LIFE_MS || 600000));
