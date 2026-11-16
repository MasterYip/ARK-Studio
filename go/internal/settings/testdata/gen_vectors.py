"""Regenerate vectors.json from arkstudio.settings: python -B gen_vectors.py

Each case is a JSON input text; the outputs are json.dumps of the Python
result, or {"error": message} for SettingsError.
"""
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[4]))
from arkstudio import settings as s  # noqa: E402

styles = [
    'null', '1', '"x"', '{}', '{"head":"open"}', '{"head":"bogus","placement":"both"}',
    '{"size":2}', '{"size":"1.2345"}', '{"size":" 2.5 "}', '{"size":true}', '{"size":null}',
    '{"size":0.4}', '{"size":2.5001}', '{"size":"nan"}', '{"size":"inf"}', '{"size":1.0005}',
    '{"size":1.2344999}', '{"size":"1_5"}', '{"size":[1]}', '{"zzz":1,"aaa":2,"head":"bar"}',
    '{"head":"diamond_filled","placement":"none","size":0.5}', '{"placement":1}',
]
relations = [
    'null', '[]', '{}', '{"supports":{"head":"open"},"bogus":{"head":"open"}}',
    '{"relates_to":{},"cites":{"size":9},"blocks":{"placement":"source","size":1.5}}',
    '{"unset":"x"}',
    # main 480f4ad: per-relation "reverse" (boolean, display only)
    '{"derived_from":{"reverse":false},"depends_on":{"reverse":true,"head":"open","size":2}}',
    '{"cites":{"size":1.5,"reverse":true,"placement":"both"}}', '{"cites":{"reverse":1}}',
    '{"cites":{"reverse":"true"}}', '{"cites":{"reverse":null}}', '{"supports":{"reverse":true,"zz":1}}',
]
arrows = ['null', '[]', '{}', '{"mind_tree":1}', '{"mind_tree":true,"style":{"head":"circle"}}',
          '{"relations":null}', '{"relations":{}}']
styles_rev = ['{"reverse":true}', '{"head":"open","reverse":false}']
arrows += ['{"style":%s}' % x for x in styles + styles_rev] + ['{"relations":%s}' % x for x in relations]
patches = ['null', '{}', '{"zz":1,"aa":2}', '{"mind_tree":"yes"}', '{"mind_tree":false}']
patches += ['{"style":%s}' % x for x in styles + styles_rev] + ['{"relations":%s}' % x for x in relations]
personal = ['{"arrows":%s}' % x for x in patches + [
    '{"style":{"head":"open","placement":"both","size":2}}',
    '{"style":{"head":"open","placement":"both","size":2},"relations":{"cites":{"head":"bar"}},"mind_tree":true}',
    '{"style":{"head":"open","placement":"both"}}', '{"style":{"size":1}}']]
personal += ['{"graph":{"repulsion":1,"distance":2,"gravity":0,"damping":0.5},'
             '"arrows":{"style":{"head":"open","placement":"both","size":2}},"palette":null}']


def run(fn, text):
    try:
        return json.dumps(fn(json.loads(text)), ensure_ascii=False)
    except s.SettingsError as exc:
        return json.dumps({"error": str(exc)})


out = {
    "normalize_arrows": [[t, run(s.normalize_arrows, t)] for t in arrows],
    "update_arrows": [[t, run(lambda v: s.update_arrows({"relations": {"cites": {"head": "bar"}}}, v), t)]
                      for t in patches],
    "personal_appearance": [[t, run(s.personal_appearance, t)] for t in personal],
    "project_settings": [[t, run(lambda v: s.project_settings({"settings": v}), t)]
                         for t in ['{}', '{"arrows":{"style":{"size":"2"},"mind_tree":true}}',
                                   '{"sessions":{"accept_uploads":true}}', '{"sessions":{"accept_uploads":1}}',
                                   '{"sessions":[]}']],
    "update_sessions": [[t, run(lambda v: s.update_sessions({"accept_uploads": True}, v), t)]
                        for t in ['{}', '{"accept_uploads":false}', '{"accept_uploads":1}', '[]', 'null',
                                  '{"x":1,"accept_uploads":true}', '{"accept_uploads":null}']],
}
Path(__file__).with_name("vectors.json").write_text(json.dumps(out, ensure_ascii=True, indent=1),
                                                    encoding="utf-8")
