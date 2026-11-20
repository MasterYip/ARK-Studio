// A stand-in for `claude -p --output-format stream-json --verbose`.
//   default: prints the recorded fixture up to its first task notification
//            and keeps running (the session stays active);
//   FAKE_STREAM_EVENTS=<n>: prints a synthetic stream of about n events in
//            the shapes of a real long session (sub-agents with nested
//            messages, foreground and background Bash tasks, task progress,
//            tool results of tens of KB, bookkeeping events), then keeps
//            streaming FAKE_STREAM_TAIL more events, one every 250 ms.
// Reads the prompt on stdin (ignored).
const { readFileSync } = require('node:fs');
const path = require('node:path');

const out = (o) => process.stdout.write(`${typeof o === 'string' ? o : JSON.stringify(o)}\n`);

function recorded() {
  const lines = readFileSync(path.join(__dirname, '../../../ark_studio/server/internal/sessions/testdata/claude_stream_subagents.jsonl'), 'utf8')
    .split('\n').filter(Boolean);
  const cut = lines.findIndex((line) => line.includes('"task_notification"') && line.includes('bash_2'));
  for (const line of lines.slice(0, cut)) out(line);
}

// Deterministic pseudo-random text.
let seed = 7;
const rnd = () => { seed = (seed * 1103515245 + 12345) % 2147483648; return seed / 2147483648; };
const words = 'reward survey benchmark agent policy contact hand object pose motion diffusion dataset evaluation metric baseline'.split(' ');
const text = (n) => Array.from({ length: n }, () => words[Math.floor(rnd() * words.length)]).join(' ');
const blob = (kb) => Array.from({ length: Math.ceil(kb * 1024 / 80) }, (_, i) => `${String(i).padStart(5, '0')} ${text(10)}`).join('\n');

function* synthetic(total) {
  const S = 'synthetic-session';
  let n = 0;
  let t = 0;
  const ev = (o) => { n += 1; return { ...o, session_id: S, uuid: `u${n}` }; };
  yield ev({ type: 'system', subtype: 'init', cwd: '/w', model: 'claude-opus-4-1', tools: ['Task', 'Bash', 'Read'] });
  const background = [];
  while (n < total) {
    t += 1;
    yield ev({ type: 'assistant', message: { content: [{ type: 'thinking', thinking: text(40) }] }, parent_tool_use_id: null });
    yield ev({ type: 'system', subtype: 'thinking_tokens', estimated_tokens: 50 });
    yield ev({ type: 'assistant', message: { content: [{ type: 'text', text: `## Step ${t}\n\n${text(60)}\n\n- ${text(8)}\n- ${text(8)}` }] }, parent_tool_use_id: null });
    const r = rnd();
    if (r < 0.35) {
      // A sub-agent with its own nested stream.
      const id = `toolu_ag${t}`;
      yield ev({ type: 'assistant', message: { content: [{ type: 'tool_use', id, name: 'Agent', input: { subagent_type: 'general-purpose', description: `Survey part ${t}`, prompt: text(80), run_in_background: rnd() < 0.3 } }] }, parent_tool_use_id: null });
      yield ev({ type: 'system', subtype: 'task_started', task_id: `a${t}`, tool_use_id: id, description: `Survey part ${t}`, task_type: 'local_agent', is_backgrounded: false });
      yield ev({ type: 'user', message: { role: 'user', content: [{ type: 'text', text: text(80) }] }, parent_tool_use_id: id });
      const steps = 4 + Math.floor(rnd() * 8);
      for (let k = 0; k < steps; k += 1) {
        yield ev({ type: 'assistant', message: { content: [{ type: 'thinking', thinking: text(30) }] }, parent_tool_use_id: id });
        yield ev({ type: 'assistant', message: { content: [{ type: 'tool_use', id: `${id}_${k}`, name: 'Read', input: { file_path: `/data/papers/p${k}.md` } }], usage: { input_tokens: 900, output_tokens: 40 } }, parent_tool_use_id: id });
        yield ev({ type: 'system', subtype: 'task_progress', task_id: `a${t}`, tool_use_id: id, description: `Reading p${k}.md`, usage: { total_tokens: 1000 * k, tool_uses: k, duration_ms: 900 * k }, last_tool_name: 'Read' });
        yield ev({ type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: `${id}_${k}`, content: blob(6 + rnd() * 20) }] }, parent_tool_use_id: id });
      }
      yield ev({ type: 'assistant', message: { content: [{ type: 'text', text: `Report ${t}: ${text(120)}` }] }, parent_tool_use_id: id });
      yield ev({ type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: id, content: [{ type: 'text', text: `Report ${t}: ${text(200)}` }] }] }, tool_use_result: { status: 'completed', agentId: `a${t}`, totalTokens: 9000 }, parent_tool_use_id: null });
      yield ev({ type: 'system', subtype: 'task_notification', task_id: `a${t}`, tool_use_id: id, status: 'completed', summary: `Survey part ${t}` });
    } else if (r < 0.5) {
      const id = `toolu_bg${t}`;
      background.push({ id, task: `bash_${t}` });
      yield ev({ type: 'assistant', message: { content: [{ type: 'tool_use', id, name: 'Bash', input: { command: `python3 train.py --run ${t} --epochs 40 --data /data/hoi/${text(3).replace(/ /g, '_')}`, description: `Train run ${t}`, run_in_background: true } }] }, parent_tool_use_id: null });
      yield ev({ type: 'system', subtype: 'task_started', task_id: `bash_${t}`, tool_use_id: id, description: `Train run ${t}`, task_type: 'local_bash', is_backgrounded: true });
      yield ev({ type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: id, content: `Command running in background with ID: bash_${t}` }] }, parent_tool_use_id: null });
      if (background.length > 3) {
        const done = background.shift();
        yield ev({ type: 'system', subtype: 'task_notification', task_id: done.task, tool_use_id: done.id, status: rnd() < 0.2 ? 'failed' : 'completed', summary: `Background command finished (exit code 0)` });
      }
    } else {
      const id = `toolu_sh${t}`;
      yield ev({ type: 'assistant', message: { content: [{ type: 'tool_use', id, name: 'Bash', input: { command: `cd /tmp/rua_a && for r in $(seq 1 ${t}); do grep -rn "${words[t % words.length]}" papers/ | head -n 400; done`, description: `Search ${t}` } }] }, parent_tool_use_id: null });
      yield ev({ type: 'system', subtype: 'task_started', task_id: `b${t}`, tool_use_id: id, description: `Search ${t}`, task_type: 'local_bash', is_backgrounded: false });
      yield ev({ type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: id, content: blob(10 + rnd() * 40) }] }, parent_tool_use_id: null });
      yield ev({ type: 'system', subtype: 'task_notification', task_id: `b${t}`, tool_use_id: id, status: 'completed', summary: `Search ${t}` });
    }
  }
}

const total = Number(process.env.FAKE_STREAM_EVENTS || 0);
if (!total) recorded();
else {
  const tail = Number(process.env.FAKE_STREAM_TAIL || 0);
  const gen = synthetic(total + tail);
  let i = 0;
  for (const event of gen) {
    out(event);
    i += 1;
    if (i >= total) break;
  }
  if (tail) {
    const timer = setInterval(() => {
      const next = gen.next();
      if (next.done) { clearInterval(timer); return; }
      out(next.value);
    }, 250);
  }
}
process.stdin.resume();
setTimeout(() => process.exit(0), Number(process.env.FAKE_STREAM_LIFE_MS || 600000));
