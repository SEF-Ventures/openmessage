"""Tests for om-export. Run: python3 -I -m pytest test_om_export.py (or python3 test_om_export.py)."""
import importlib.machinery
import importlib.util
import json
import sqlite3
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
_loader = importlib.machinery.SourceFileLoader("om_export", str(HERE / "om-export"))
_spec = importlib.util.spec_from_loader("om_export", _loader)
om = importlib.util.module_from_spec(_spec)
_loader.exec_module(om)

SCHEMA = """
CREATE TABLE conversations (conversation_id TEXT PRIMARY KEY, name TEXT, is_group INTEGER, participants TEXT,
  last_message_ts INTEGER, unread_count INTEGER, source_platform TEXT DEFAULT 'sms');
CREATE TABLE messages (message_id TEXT PRIMARY KEY, conversation_id TEXT, sender_name TEXT, sender_number TEXT,
  body TEXT, timestamp_ms INTEGER, status TEXT, is_from_me INTEGER, media_id TEXT, mime_type TEXT,
  source_platform TEXT DEFAULT 'sms');
CREATE TABLE drafts (draft_id TEXT PRIMARY KEY, conversation_id TEXT, body TEXT, created_at INTEGER);
"""


def make_db(path):
    con = sqlite3.connect(path)
    con.executescript(SCHEMA)
    con.executemany("INSERT INTO conversations VALUES (?,?,?,?,?,?,?)", [
        ("c1", "Pat", 0, "[]", 1_700_000_002_000, 0, "sms"),
        ("c2", "Crew", 1, "[]", 1_700_000_003, 0, ""),
        ("w1", "WA group", 1, "[]", 1_700_000_004_000, 0, "whatsapp"),
    ])
    con.executemany("INSERT INTO messages VALUES (?,?,?,?,?,?,?,?,?,?,?)", [
        ("m1", "c1", "Pat", "+10000000001", "hi", 1_700_000_001_000, "delivered", 0, "", "", "sms"),
        ("m2", "c1", "Shane", "+10000000009", "yo", 1_700_000_002_000, "sent", 1, "", "", "sms"),
        ("m3", "c2", "Lee", "+10000000002", "", 1_700_000_003, "delivered", 0, "", "image/jpeg", ""),
        ("w1m", "w1", "Kim", "+10000000003", "skip me", 1_700_000_004_000, "delivered", 0, "", "", "whatsapp"),
    ])
    con.execute("INSERT INTO drafts VALUES ('d1', 'c1', 'secret body', 1700000005000)")
    con.commit()
    con.close()


class ExportTest(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        make_db(self.tmp / "messages.db")
        self.out = self.tmp / "out"
        self.out.mkdir()

    def read(self, name):
        return [json.loads(l) for l in (self.out / name).read_text().splitlines()]

    def test_matches_gmcli_export_schema(self):
        con = sqlite3.connect(f"file:{self.tmp / 'messages.db'}?mode=ro", uri=True)
        n_msgs, n_convs, n_drafts, newest = om.export(con, self.out)
        self.assertEqual((n_msgs, n_convs, n_drafts), (3, 2, 1))
        self.assertEqual(newest, 1_700_000_003_000)
        msgs = self.read("messages.jsonl")
        gmcli_keys = {"schema_version", "source", "message_id", "conversation_id", "timestamp_ms", "direction",
                      "sender_name", "sender_number", "body", "has_media", "media"}
        for m in msgs:
            self.assertEqual(set(m), gmcli_keys)
            self.assertEqual(m["schema_version"], 1)
        by_id = {m["message_id"]: m for m in msgs}
        self.assertNotIn("w1m", by_id)
        self.assertEqual(by_id["m2"]["direction"], "outbound")
        self.assertEqual(by_id["m2"]["sender_name"], "Me")
        self.assertEqual(by_id["m2"]["sender_number"], "")
        self.assertEqual(by_id["m1"]["direction"], "inbound")
        self.assertTrue(by_id["m3"]["has_media"])
        self.assertEqual(by_id["m3"]["media"], [{"mime_type": "image/jpeg"}])
        self.assertEqual(by_id["m3"]["timestamp_ms"], 1_700_000_003_000)
        convs = {c["conversation_id"]: c for c in self.read("conversations.jsonl")}
        self.assertEqual(set(convs), {"c1", "c2"})
        self.assertEqual(set(convs["c1"]), {"conversation_id", "name", "is_group", "last_message_ms"})
        self.assertTrue(convs["c2"]["is_group"])
        drafts = self.read("drafts.jsonl")
        self.assertEqual(drafts[0]["conversation_name"], "Pat")
        self.assertNotIn("body", drafts[0])

    def test_health_mapping(self):
        class Fake:
            def __init__(self, google):
                self.google = google

            def tool(self, name, arguments=None):
                return {"structuredContent": {"google": self.google}}

        self.assertEqual(om.health(Fake({"paired": False}))["problem"], "unpaired")
        self.assertEqual(om.health(Fake({"paired": True, "needs_pairing": True}))["problem"], "session_invalid")
        self.assertEqual(om.health(Fake({"paired": True, "connected": True}))["problem"], None)
        self.assertEqual(om.health(Fake({"paired": True, "connected": False}))["problem"], "disconnected")

    def test_short_disconnect_is_not_an_alert(self):
        t = 10 * om.SYNC_DOWN_MS
        state = om.settle({"checked_ms": t}, {"problem": "disconnected", "connected": False},
                          {"last_connect_ms": t - 60_000})
        self.assertIsNone(state["problem"])
        state = om.settle({"checked_ms": t}, {"problem": "disconnected", "connected": False},
                          {"last_connect_ms": t - om.SYNC_DOWN_MS - 1})
        self.assertEqual(state["problem"], "sync_down")


if __name__ == "__main__":
    unittest.main()
