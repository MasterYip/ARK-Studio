// A stand-in for `claude -p --output-format stream-json --verbose` whose
// messages are hard to fit on a phone: long paths, URLs and tokens, a code
// block with a long line, a wide table, an image, a long ARK UPDATE turn,
// tool commands with long output, notices and an error.  Prints them and
// keeps running so the session stays active.  Reads the prompt on stdin.
const out = (o) => process.stdout.write(`${JSON.stringify(o)}\n`);
const S = 'wide-session';
let n = 0;
const ev = (o) => { n += 1; return { ...o, session_id: S, uuid: `w${n}` }; };

const path = '/data/team/projects/prj_0a1b2c3d4e/runtime/sessions/sess_0123456789abcdef0123456789abcdef/artifacts/output/final_report_with_a_very_long_name_v2_FINAL_really_final.md';
const url = 'https://ark.example.com/api/agent/v1/media/med_9f8e7d6c5b4a39281706f5e4d3c2b1a0/raw?download=1&name=very_long_attachment_name_without_any_spaces_at_all.tar.gz';
const hash = 'sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a089f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08';
const b64 = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg'.repeat(4);
const codeLine = `python3 tools/train.py --config configs/very/long/path/to/experiment_config_with_many_options.yaml --output ${path} --seed 1234567890`;
const math = '$\sum_{i=1}^{100} x_i^2 + \frac{a_1+a_2+a_3+a_4+a_5+a_6+a_7+a_8+a_9+a_{10}+a_{11}+a_{12}}{b_1 b_2 b_3 b_4 b_5 b_6 b_7 b_8 b_9 b_{10} b_{11} b_{12}}$';
const table = [
  '| run | dataset | checkpoint | reward | success | notes |',
  '|---|---|---|---|---|---|',
  `| r1 | hoi_grasp_benchmark_v3 | ${hash.slice(0, 48)} | 0.8123 | 91.2% | baseline_without_any_augmentation |`,
  '| r2 | hoi_grasp_benchmark_v3 | ckpt_epoch_0040_step_0012800.pt | 0.8551 | 93.4% | with_contact_reward_and_curriculum |',
].join('\n');
const assistant = [
  `Wrote the report to \`${path}\`.`,
  '',
  `Download: ${url}`,
  '',
  `Checksum ${hash}`,
  '',
  '```bash',
  codeLine,
  '```',
  '',
  table,
  '',
  '![ARK mark](/brand/ark-mark.png)',
  '',
  `Inline base64 \`${b64}\` and math ${math} end.`,
  '',
  `$$${math.slice(1, -1)} + ${math.slice(1, -1)}$$`,
  '',
  `## Results_for_${hash.slice(7, 70)}`,
  '',
  `> Quoted log line ${path}:${hash}`,
  '',
  `- item with [a long link label ${url}](${url})`,
  `  1. nested \`${path}\``,
].join('\n');
const update = `ARK UPDATE for ARK session sess_0123456789abcdef0123456789abcdef, ARK project ARK_Develop (prj_0a1b2c3d4e): 1 new update(s) from people while you were idle; 1 GPU job(s) you queued ended:\n- A reviewer commented on T-0123 ${url}\n- gpu_job_0123456789abcdef0123456789abcdef finished (exit 0)\n  full log: GET /api/agent/v1/gpu/jobs/gpu_job_0123456789abcdef0123456789abcdef/log?tail=400&format=plain\nRead GET /api/agent/v1/updates, act on them under the core rules, then go on.`;
const userToken = `token_${'A1b2C3d4E5f6G7h8'.repeat(12)}`;
const output = Array.from({ length: 12 }, (_, i) => `${String(i).padStart(4, '0')} ${path}:${i * 17}: ${'x'.repeat(30)}${hash}`).join('\n');

out(ev({ type: 'system', subtype: 'init', cwd: '/w', model: 'claude-opus-4-1', tools: ['Bash', 'Read'] }));
out(ev({ type: 'user', message: { role: 'user', content: [{ type: 'text', text: userToken }] }, parent_tool_use_id: null }));
out(ev({ type: 'assistant', message: { content: [{ type: 'text', text: assistant }] }, parent_tool_use_id: null }));
out(ev({ type: 'assistant', message: { content: [{ type: 'tool_use', id: 'toolu_W1', name: 'Bash', input: { command: `${codeLine} && grep -rn ${hash} ${path}`, description: 'Train' } }] }, parent_tool_use_id: null }));
out(ev({ type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 'toolu_W1', content: output }] }, parent_tool_use_id: null }));
out(ev({ type: 'assistant', message: { content: [{ type: 'tool_use', id: 'toolu_W2', name: 'Read', input: { file_path: path } }] }, parent_tool_use_id: null }));
out(ev({ type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 'toolu_W2', content: output }] }, parent_tool_use_id: null }));
// One run of four kinds of tools: the row's summary is a long one-line label.
const tool = (id, name, input) => {
  out(ev({ type: 'assistant', message: { content: [{ type: 'tool_use', id, name, input }] }, parent_tool_use_id: null }));
  out(ev({ type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: id, content: 'ok' }] }, parent_tool_use_id: null }));
};
out(ev({ type: 'assistant', message: { content: [{ type: 'text', text: 'Next step.' }] }, parent_tool_use_id: null }));
tool('toolu_M1', 'Edit', { file_path: path, old_string: 'a', new_string: 'b' });
tool('toolu_M2', 'Edit', { file_path: `${path}.bak`, old_string: 'a', new_string: 'b' });
tool('toolu_M3', 'Bash', { command: 'pytest -q tests/test_reward.py', description: 'Test' });
tool('toolu_M4', 'Grep', { pattern: 'contact_reward', path: '/w' });
tool('toolu_M5', 'Read', { file_path: path });
tool('toolu_M6', 'Read', { file_path: `${path}.log` });
// A sub-agent with a long description.
out(ev({ type: 'assistant', message: { content: [{ type: 'text', text: 'Asking a sub-agent.' }] }, parent_tool_use_id: null }));
out(ev({ type: 'assistant', message: { content: [{ type: 'tool_use', id: 'toolu_SA', name: 'Agent', input: { subagent_type: 'general-purpose', description: 'Survey contact rewards across all hand-object interaction benchmarks and baselines', prompt: 'survey' } }] }, parent_tool_use_id: null }));
out(ev({ type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 'toolu_SA', content: [{ type: 'text', text: 'Report.' }] }] }, tool_use_result: { status: 'completed', agentId: 'sa1', totalTokens: 9000 }, parent_tool_use_id: null }));
out({ type: 'notice', text: `Session moved to ${path} after ${url}` });
out({ type: 'error', message: `Request failed: ${url} ${hash}` });
out(ev({ type: 'user', message: { role: 'user', content: [{ type: 'text', text: update }] }, parent_tool_use_id: null }));
out(ev({ type: 'assistant', message: { content: [{ type: 'text', text: `Done. See ${path} and ${userToken}.` }] }, parent_tool_use_id: null }));
process.stdin.resume();
setTimeout(() => process.exit(0), Number(process.env.FAKE_STREAM_LIFE_MS || 600000));
