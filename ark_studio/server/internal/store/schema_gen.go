// Code generated from arkstudio/db.py, org.py and agent_tokens.py by
// tests/python/contract/gen_schema.py; DO NOT EDIT. The schema must stay identical to Python.

package store

// SchemaVersion is db.SCHEMA_VERSION.
const SchemaVersion = 12

// ProjectSchema is db.SCHEMA verbatim.
const ProjectSchema = `
CREATE TABLE IF NOT EXISTS meta(
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS units(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  uid TEXT NOT NULL UNIQUE,
  type TEXT NOT NULL,
  title TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'open',
  importance TEXT NOT NULL DEFAULT 'p2',
  start_at TEXT,
  end_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  origin TEXT NOT NULL DEFAULT 'manual',
  source_ref TEXT,
  source_title TEXT,
  meta_json TEXT NOT NULL DEFAULT '{}',
  archived INTEGER NOT NULL DEFAULT 0,
  assignee TEXT
);
CREATE INDEX IF NOT EXISTS units_type ON units(type, archived);
CREATE INDEX IF NOT EXISTS units_status ON units(status, archived);
CREATE INDEX IF NOT EXISTS units_time ON units(start_at, end_at);
CREATE INDEX IF NOT EXISTS units_updated ON units(updated_at);
CREATE UNIQUE INDEX IF NOT EXISTS units_source_ref
  ON units(source_ref) WHERE source_ref IS NOT NULL;

CREATE TABLE IF NOT EXISTS unit_tags(
  unit_id INTEGER NOT NULL REFERENCES units(id) ON DELETE CASCADE,
  tag TEXT NOT NULL,
  PRIMARY KEY(unit_id, tag)
);
CREATE INDEX IF NOT EXISTS unit_tags_tag ON unit_tags(tag);

CREATE TABLE IF NOT EXISTS unit_links(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  source_id INTEGER NOT NULL REFERENCES units(id) ON DELETE CASCADE,
  target_id INTEGER NOT NULL REFERENCES units(id) ON DELETE CASCADE,
  relation TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  created_by TEXT NOT NULL DEFAULT 'human',
  UNIQUE(source_id, target_id, relation)
);
CREATE INDEX IF NOT EXISTS links_source ON unit_links(source_id);
CREATE INDEX IF NOT EXISTS links_target ON unit_links(target_id);

CREATE TABLE IF NOT EXISTS unit_media(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  unit_id INTEGER NOT NULL REFERENCES units(id) ON DELETE CASCADE,
  url TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'link',
  caption TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS media_unit ON unit_media(unit_id);

-- v4: comments are their own rows on a unit, not lines in its body, so they
-- survive body rewrites.  They are never deleted; resolving is reversible.
CREATE TABLE IF NOT EXISTS unit_comments(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  unit_id INTEGER NOT NULL REFERENCES units(id) ON DELETE CASCADE,
  author TEXT NOT NULL,
  kind TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL,
  reply_to INTEGER REFERENCES unit_comments(id),
  resolved_at TEXT,
  resolved_by TEXT
);
CREATE INDEX IF NOT EXISTS comments_unit ON unit_comments(unit_id, created_at);

CREATE TABLE IF NOT EXISTS activity(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at TEXT NOT NULL,
  actor TEXT NOT NULL,
  action TEXT NOT NULL,
  unit_id INTEGER,
  detail_json TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS activity_at ON activity(at);

CREATE TABLE IF NOT EXISTS chat_messages(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at TEXT NOT NULL,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  tools_json TEXT NOT NULL DEFAULT '[]',
  meta_json TEXT NOT NULL DEFAULT '{}',
  -- v10: the account that owns the message ('' = shared, written before
  -- chat history became per user).
  user TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS chat_at ON chat_messages(at);

-- v6: a topic unit holds a curated subgraph of other units (see
-- Store.design_topic).  include/exclude rows are explicit membership on top
-- of the topic's rule; every row also carries the member's narrative
-- position, outline parent (0 = top level, NULL = follow part_of), canvas x/y
-- and topic-local caption (layout rows only that).
CREATE TABLE IF NOT EXISTS topic_members(
  topic_id INTEGER NOT NULL REFERENCES units(id) ON DELETE CASCADE,
  unit_id INTEGER NOT NULL REFERENCES units(id) ON DELETE CASCADE,
  mode TEXT NOT NULL DEFAULT 'include' CHECK(mode IN ('include','layout','exclude')),
  position INTEGER NOT NULL DEFAULT 0,
  parent_id INTEGER,
  x REAL,
  y REAL,
  w REAL,
  caption TEXT NOT NULL DEFAULT '',
  added_at TEXT NOT NULL,
  added_by TEXT NOT NULL DEFAULT 'human',
  updated_at TEXT NOT NULL,
  PRIMARY KEY(topic_id, unit_id)
);
CREATE INDEX IF NOT EXISTS topic_members_unit ON topic_members(unit_id);

CREATE TABLE IF NOT EXISTS counters(
  prefix TEXT PRIMARY KEY,
  n INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS node_text_history(
  unit_id INTEGER PRIMARY KEY REFERENCES units(id) ON DELETE CASCADE,
  enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN (0,1)),
  head TEXT
);
CREATE TABLE IF NOT EXISTS node_text_pending(
  unit_id INTEGER PRIMARY KEY REFERENCES units(id) ON DELETE CASCADE,
  edited_at REAL NOT NULL,
  checked_at REAL NOT NULL DEFAULT 0,
  actor TEXT NOT NULL
);
`

// RevisionSchema is db.REVISION_SCHEMA verbatim (applied after migrate).
const RevisionSchema = `
-- change_counter.n goes up by one for every row inserted, updated or
-- deleted in a table that remote clients render.  Store.revision() includes
-- it, so a committed write always changes the revision even when it lands in
-- the same second as the previous one (updated_at has 1 s resolution) or only
-- moves a topic member.  The triggers live in the database file, so writes
-- from any process or ARK implementation count; a rolled-back transaction
-- rolls the counter back with it.
CREATE TABLE IF NOT EXISTS change_counter(
  id INTEGER PRIMARY KEY CHECK(id = 1),
  n INTEGER NOT NULL DEFAULT 0
);
INSERT OR IGNORE INTO change_counter(id, n) VALUES(1, 0);
CREATE TRIGGER IF NOT EXISTS units_rev_ins AFTER INSERT ON units BEGIN UPDATE change_counter SET n = n + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS units_rev_upd AFTER UPDATE ON units BEGIN UPDATE change_counter SET n = n + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS units_rev_del AFTER DELETE ON units BEGIN UPDATE change_counter SET n = n + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS unit_tags_rev_ins AFTER INSERT ON unit_tags BEGIN UPDATE change_counter SET n = n + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS unit_tags_rev_upd AFTER UPDATE ON unit_tags BEGIN UPDATE change_counter SET n = n + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS unit_tags_rev_del AFTER DELETE ON unit_tags BEGIN UPDATE change_counter SET n = n + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS unit_links_rev_ins AFTER INSERT ON unit_links BEGIN UPDATE change_counter SET n = n + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS unit_links_rev_upd AFTER UPDATE ON unit_links BEGIN UPDATE change_counter SET n = n + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS unit_links_rev_del AFTER DELETE ON unit_links BEGIN UPDATE change_counter SET n = n + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS unit_media_rev_ins AFTER INSERT ON unit_media BEGIN UPDATE change_counter SET n = n + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS unit_media_rev_upd AFTER UPDATE ON unit_media BEGIN UPDATE change_counter SET n = n + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS unit_media_rev_del AFTER DELETE ON unit_media BEGIN UPDATE change_counter SET n = n + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS unit_comments_rev_ins AFTER INSERT ON unit_comments BEGIN UPDATE change_counter SET n = n + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS unit_comments_rev_upd AFTER UPDATE ON unit_comments BEGIN UPDATE change_counter SET n = n + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS unit_comments_rev_del AFTER DELETE ON unit_comments BEGIN UPDATE change_counter SET n = n + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS topic_members_rev_ins AFTER INSERT ON topic_members BEGIN UPDATE change_counter SET n = n + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS topic_members_rev_upd AFTER UPDATE ON topic_members BEGIN UPDATE change_counter SET n = n + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS topic_members_rev_del AFTER DELETE ON topic_members BEGIN UPDATE change_counter SET n = n + 1 WHERE id = 1; END;
`

// ChangeLogSchema is db.CHANGE_LOG_SCHEMA verbatim (applied after RevisionSchema).
const ChangeLogSchema = `
CREATE TABLE IF NOT EXISTS change_log(
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL,
  ref INTEGER NOT NULL,
  tree INTEGER NOT NULL DEFAULT 0
);
CREATE TRIGGER IF NOT EXISTS units_log_ins AFTER INSERT ON units BEGIN INSERT INTO change_log(kind,ref,tree) VALUES('unit',NEW.id,0); END;
CREATE TRIGGER IF NOT EXISTS units_log_upd AFTER UPDATE ON units BEGIN INSERT INTO change_log(kind,ref,tree) VALUES('unit',NEW.id,(OLD.assignee IS NOT NEW.assignee)); END;
CREATE TRIGGER IF NOT EXISTS units_log_del AFTER DELETE ON units BEGIN INSERT INTO change_log(kind,ref,tree) VALUES('unit',OLD.id,0); END;
CREATE TRIGGER IF NOT EXISTS unit_links_log_ins AFTER INSERT ON unit_links BEGIN INSERT INTO change_log(kind,ref,tree) VALUES('link',NEW.id,0);INSERT INTO change_log(kind,ref,tree) VALUES('unit',NEW.source_id,(NEW.relation='part_of'));INSERT INTO change_log(kind,ref,tree) VALUES('unit',NEW.target_id,0); END;
CREATE TRIGGER IF NOT EXISTS unit_links_log_upd AFTER UPDATE ON unit_links BEGIN INSERT INTO change_log(kind,ref,tree) VALUES('link',NEW.id,0);INSERT INTO change_log(kind,ref,tree) VALUES('unit',OLD.source_id,(OLD.relation='part_of' OR NEW.relation='part_of'));INSERT INTO change_log(kind,ref,tree) VALUES('unit',NEW.source_id,(OLD.relation='part_of' OR NEW.relation='part_of'));INSERT INTO change_log(kind,ref,tree) VALUES('unit',OLD.target_id,0);INSERT INTO change_log(kind,ref,tree) VALUES('unit',NEW.target_id,0); END;
CREATE TRIGGER IF NOT EXISTS unit_links_log_del AFTER DELETE ON unit_links BEGIN INSERT INTO change_log(kind,ref,tree) VALUES('link',OLD.id,0);INSERT INTO change_log(kind,ref,tree) VALUES('unit',OLD.source_id,(OLD.relation='part_of'));INSERT INTO change_log(kind,ref,tree) VALUES('unit',OLD.target_id,0); END;
CREATE TRIGGER IF NOT EXISTS topic_members_log_ins AFTER INSERT ON topic_members BEGIN INSERT INTO change_log(kind,ref,tree) VALUES('unit',NEW.topic_id,0);INSERT INTO change_log(kind,ref,tree) VALUES('unit',NEW.unit_id,0); END;
CREATE TRIGGER IF NOT EXISTS topic_members_log_upd AFTER UPDATE ON topic_members BEGIN INSERT INTO change_log(kind,ref,tree) VALUES('unit',OLD.topic_id,0);INSERT INTO change_log(kind,ref,tree) VALUES('unit',OLD.unit_id,0);INSERT INTO change_log(kind,ref,tree) VALUES('unit',NEW.topic_id,0);INSERT INTO change_log(kind,ref,tree) VALUES('unit',NEW.unit_id,0); END;
CREATE TRIGGER IF NOT EXISTS topic_members_log_del AFTER DELETE ON topic_members BEGIN INSERT INTO change_log(kind,ref,tree) VALUES('unit',OLD.topic_id,0);INSERT INTO change_log(kind,ref,tree) VALUES('unit',OLD.unit_id,0); END;
CREATE TRIGGER IF NOT EXISTS unit_tags_log_ins AFTER INSERT ON unit_tags BEGIN INSERT INTO change_log(kind,ref,tree) VALUES('unit',NEW.unit_id,0); END;
CREATE TRIGGER IF NOT EXISTS unit_tags_log_upd AFTER UPDATE ON unit_tags BEGIN INSERT INTO change_log(kind,ref,tree) VALUES('unit',OLD.unit_id,0);INSERT INTO change_log(kind,ref,tree) VALUES('unit',NEW.unit_id,0); END;
CREATE TRIGGER IF NOT EXISTS unit_tags_log_del AFTER DELETE ON unit_tags BEGIN INSERT INTO change_log(kind,ref,tree) VALUES('unit',OLD.unit_id,0); END;
CREATE TRIGGER IF NOT EXISTS unit_media_log_ins AFTER INSERT ON unit_media BEGIN INSERT INTO change_log(kind,ref,tree) VALUES('unit',NEW.unit_id,0); END;
CREATE TRIGGER IF NOT EXISTS unit_media_log_upd AFTER UPDATE ON unit_media BEGIN INSERT INTO change_log(kind,ref,tree) VALUES('unit',OLD.unit_id,0);INSERT INTO change_log(kind,ref,tree) VALUES('unit',NEW.unit_id,0); END;
CREATE TRIGGER IF NOT EXISTS unit_media_log_del AFTER DELETE ON unit_media BEGIN INSERT INTO change_log(kind,ref,tree) VALUES('unit',OLD.unit_id,0); END;
CREATE TRIGGER IF NOT EXISTS unit_comments_log_ins AFTER INSERT ON unit_comments BEGIN INSERT INTO change_log(kind,ref,tree) VALUES('unit',NEW.unit_id,0); END;
CREATE TRIGGER IF NOT EXISTS unit_comments_log_upd AFTER UPDATE ON unit_comments BEGIN INSERT INTO change_log(kind,ref,tree) VALUES('unit',OLD.unit_id,0);INSERT INTO change_log(kind,ref,tree) VALUES('unit',NEW.unit_id,0); END;
CREATE TRIGGER IF NOT EXISTS unit_comments_log_del AFTER DELETE ON unit_comments BEGIN INSERT INTO change_log(kind,ref,tree) VALUES('unit',OLD.unit_id,0); END;
`

// ChangeLogKeep is db.CHANGE_LOG_KEEP.
const ChangeLogKeep = 20000

// ChangeLogID is db.CHANGE_LOG_ID (meta key).
const ChangeLogID = "change_log_id"

// OrgSchemaVersion is org.ORG_SCHEMA_VERSION.
const OrgSchemaVersion = 3

// OrgSchema is org.SCHEMA verbatim.
const OrgSchema = `
CREATE TABLE IF NOT EXISTS meta(
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS orgs(
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS users(
  id TEXT PRIMARY KEY,
  username TEXT NOT NULL UNIQUE COLLATE NOCASE,
  display_name TEXT NOT NULL DEFAULT '',
  password_hash TEXT,
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS memberships(
  org_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  role TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(org_id, user_id)
);
CREATE TABLE IF NOT EXISTS teams(
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE(org_id, name)
);
CREATE TABLE IF NOT EXISTS team_members(
  team_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  added_at TEXT NOT NULL,
  PRIMARY KEY(team_id, user_id)
);
CREATE TABLE IF NOT EXISTS projects(
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  name TEXT NOT NULL,
  path TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  created_by TEXT
);
CREATE TABLE IF NOT EXISTS project_grants(
  project_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  permission TEXT NOT NULL,
  granted_by TEXT,
  created_at TEXT NOT NULL,
  PRIMARY KEY(project_id, user_id)
);
CREATE TABLE IF NOT EXISTS team_grants(
  project_id TEXT NOT NULL,
  team_id TEXT NOT NULL,
  permission TEXT NOT NULL,
  granted_by TEXT,
  created_at TEXT NOT NULL,
  PRIMARY KEY(project_id, team_id)
);
CREATE TABLE IF NOT EXISTS user_settings(
  user_id TEXT NOT NULL,
  scope TEXT NOT NULL,
  data_json TEXT NOT NULL DEFAULT '{}',
  updated_at TEXT NOT NULL,
  PRIMARY KEY(user_id, scope)
);
CREATE TABLE IF NOT EXISTS user_secrets(
  user_id TEXT NOT NULL,
  scope TEXT NOT NULL,
  data_json TEXT NOT NULL DEFAULT '{}',
  updated_at TEXT NOT NULL,
  PRIMARY KEY(user_id, scope)
);
CREATE TABLE IF NOT EXISTS linux_accounts(
  user_id TEXT NOT NULL,
  account TEXT NOT NULL,
  assigned_by TEXT,
  created_at TEXT NOT NULL,
  PRIMARY KEY(user_id, account)
);
CREATE TABLE IF NOT EXISTS audit(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at TEXT NOT NULL,
  user_id TEXT,
  action TEXT NOT NULL,
  project_id TEXT,
  detail_json TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS project_grants_user ON project_grants(user_id);
CREATE INDEX IF NOT EXISTS team_grants_team ON team_grants(team_id);
CREATE INDEX IF NOT EXISTS audit_at ON audit(at);
`

// AgentTokenSchema is agent_tokens.SCHEMA verbatim.
const AgentTokenSchema = `
CREATE TABLE IF NOT EXISTS agent_tokens(
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  label TEXT NOT NULL DEFAULT '',
  token_hash TEXT NOT NULL UNIQUE,
  prefix TEXT NOT NULL,
  scope TEXT NOT NULL DEFAULT 'write',
  created_at TEXT NOT NULL,
  expires_at TEXT,
  last_used_at TEXT,
  revoked_at TEXT
);
CREATE INDEX IF NOT EXISTS agent_tokens_user ON agent_tokens(user_id);
`

// UserProfileColumns is org.USER_PROFILE_COLUMNS.
var UserProfileColumns = [][2]string{
	{"bio", "TEXT NOT NULL DEFAULT ''"},
	{"email", "TEXT NOT NULL DEFAULT ''"},
	{"avatar_kind", "TEXT NOT NULL DEFAULT 'initials'"},
	{"avatar_color", "TEXT NOT NULL DEFAULT ''"},
	{"avatar_path", "TEXT NOT NULL DEFAULT ''"},
	{"prefs_json", "TEXT NOT NULL DEFAULT '{}'"},
	{"last_project_id", "TEXT NOT NULL DEFAULT ''"},
}

// Relations is the key order of tags.RELATIONS.
var Relations = []string{"part_of", "derived_from", "supports", "contradicts", "depends_on", "blocks", "cites", "inspires", "targets", "produces", "validates", "assigned_to", "runs_on", "supersedes", "uses", "relates_to", "unset"}

// SymmetricRelations are the relations whose reverse label is their label.
var SymmetricRelations = []string{"relates_to", "unset"}

// KnownTypes is tags.KNOWN_TYPES in order: id, label, color.
var KnownTypes = [][3]string{
	{"idea", "Idea", "#f59e0b"},
	{"question", "Question", "#38bdf8"},
	{"claim", "Claim", "#a78bfa"},
	{"evidence", "Evidence", "#34d399"},
	{"story", "Story", "#fb7185"},
	{"related_work", "Related work", "#2dd4bf"},
	{"topic", "Topic", "#6366f1"},
	{"method", "Method", "#fb923c"},
	{"experiment", "Experiment", "#c084fc"},
	{"run", "Run", "#94a3b8"},
	{"task", "Task", "#60a5fa"},
	{"milestone", "Milestone", "#818cf8"},
	{"artifact", "Artifact", "#22d3ee"},
	{"resource", "Resource", "#4ade80"},
	{"agent", "Agent", "#facc15"},
	{"host", "Host", "#a3a3a3"},
	{"production", "Production", "#e879f9"},
	{"outreach", "Outreach", "#f87171"},
	{"decision", "Decision", "#fcd34d"},
	{"source", "Source", "#7dd3fc"},
	{"note", "Note", "#cbd5e1"},
}

// TypePrefixes is the uid prefix of every tags.KNOWN_TYPES entry.
var TypePrefixes = map[string]string{"idea": "I", "question": "Q", "claim": "C", "evidence": "E", "story": "S", "related_work": "RW", "topic": "TP", "method": "M", "experiment": "X", "run": "RUN", "task": "T", "milestone": "MS", "artifact": "F", "resource": "R", "agent": "A", "host": "H", "production": "P", "outreach": "O", "decision": "D", "source": "SRC", "note": "N"}

// KnownStatuses is tags.KNOWN_STATUSES in order.
var KnownStatuses = []string{"inbox", "open", "active", "review", "blocked", "done", "abandoned", "archived"}

// KnownImportance is tags.KNOWN_IMPORTANCE in order.
var KnownImportance = []string{"p0", "p1", "p2", "p3"}

// DefaultTopics is Store.DEFAULT_TOPICS: key, title, type.
var DefaultTopics = [][3]string{
	{"story", "Story", "story"},
	{"related", "Related work", "related_work"},
	{"method", "Method", "method"},
	{"experiment", "Experiment", "experiment"},
}

// DefaultTopicsFlag is Store.DEFAULT_TOPICS_FLAG.
const DefaultTopicsFlag = "topics.defaults_v1"
