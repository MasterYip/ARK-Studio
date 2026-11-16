import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import tempfile
import unittest
from urllib.parse import parse_qs, urlencode

from arkstudio.server import filters_from_query
from tests.python.store.test_store import make_store


class GraphSnapshotTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.store = make_store(Path(self.tmp.name))

    def tearDown(self):
        self.store.close()
        self.tmp.cleanup()

    def test_local_filters_match_sql_graph(self):
        node = os.environ.get('ARK_NODE_EXE') or shutil.which('node')
        if not node:
            self.skipTest('Node is required for frontend/backend parity')
        units = []
        for i in range(48):
            units.append(self.store.create_unit({
                'title':f'Node {i:02d}', 'type':['claim','idea','task'][i%3],
                'status':['open','done','review'][i%3], 'importance':f'p{i%4}',
                'body':'x'*400+f' Hidden{i%5}', 'tags':[f'batch:{i%4}']+([f'topic:{i%3}'] if i%2 else []),
                'assignee':'alice' if i%10==0 else None,
                'start_at':f'2026-03-{i%28+1:02d}' if i%4 else None,
                'end_at':f'2026-05-{i%28+1:02d}' if i%3 else None,
            }))
        for i in range(1, len(units)):
            self.store.create_link(units[i]['id'], units[(i-1)//3]['id'], 'part_of')
        queries = [{}, {'types':['claim']}, {'types_none':'1'}, {'statuses':['review','done']},
                   {'importance':['p0','p1']}, {'tags':['batch:1','batch:2']},
                   {'tags':['batch:1','topic:1']}, {'tags_unset':['topic']},
                   {'tags':['topic:1'], 'tags_unset':['topic']}, {'roots':'1'},
                   {'assignee':['alice']}, {'assignee':['none']}, {'assignee':['me']},
                   {'q':'hidden3'}, {'q':'hidden_'}, {'q':'Node 0%'},
                   {'from':'2026-03-12','to':'2026-03-18'}, {'from':'2026-04-01'},
                   {'to':'2026-03-10'}, {'overdue':'1'}, {'created_since':'2099-01-01'},
                   {'types':['claim'], 'context':'1'}]
        queries += [{'sort':sort, 'limit':13} for sort in ['title','type','created','updated','start','end','importance']]
        expected = []
        for query in queries:
            filters = filters_from_query(parse_qs(urlencode(query, doseq=True)), me='alice')
            data = self.store.graph(filters, context=query.get('context')=='1', limit=query.get('limit',700))
            expected.append({'ids':[n['id'] for n in data['nodes']], 'context':data['context_count'],
                             'edges':[e['id'] for e in data['edges']], 'total':data['total']})
        fixture = Path(self.tmp.name) / 'parity.json'
        fixture.write_text(json.dumps({'snapshot':self.store.graph_snapshot(), 'queries':queries, 'me':'alice'}))
        result = subprocess.run([node,'--experimental-vm-modules','scripts/dev/graph_filter_query.mjs',str(fixture)],
                                cwd=Path(__file__).resolve().parents[3], capture_output=True, text=True, check=True)
        actual = json.loads(result.stdout)['results']
        for query, want, got in zip(queries, expected, actual):
            got.pop('ms')
            self.assertEqual(want, got, query)

    def test_snapshot_batches_large_parameter_lists_and_refreshes_after_writes(self):
        unit = self.store.create_unit({'title':'Original','type':'claim','body':'full document'})
        with self.store.con:
            for i in range(1100):
                self.store.con.execute('INSERT INTO units(uid,type,title,created_at,updated_at) VALUES(?,?,?,?,?)',
                                       (f'B-{i}','idea',f'Bulk {i}','2026-01-01','2026-01-01'))
        if hasattr(self.store.con, 'setlimit'):
            self.store.con.setlimit(sqlite3.SQLITE_LIMIT_VARIABLE_NUMBER, 999)
        snapshot = self.store.graph_snapshot()
        self.assertEqual(1101,len(snapshot['nodes']))
        self.assertEqual('full document',next(n for n in snapshot['nodes'] if n['id']==unit['id'])['search_body'])
        self.store.update_unit(unit['id'], {'title':'Updated'})
        self.assertEqual('Updated',next(n for n in self.store.graph_snapshot()['nodes'] if n['id']==unit['id'])['title'])
