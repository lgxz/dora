#!/usr/bin/env python3

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import sqlite3
import sys

import yaml
import base64
import binascii

from dataclasses import asdict, is_dataclass, make_dataclass
from typing import Any, Callable, Iterator

from wcwidth import wcswidth


# ======================================================================
# Input sources
# ======================================================================

SOURCE_SQLITE = "sqlite"
SOURCE_JSONL = "jsonl"
SOURCE_AUTO = "auto"

SOURCES = (
    SOURCE_AUTO,
    SOURCE_SQLITE,
    SOURCE_JSONL,
)


# ======================================================================
# Colors
# ======================================================================

RESET = "\033[0m"

FIELD = "\033[36m"        # cyan
OUTPUT = "\033[32m"       # green
ERROR = "\033[31m"        # red
META = "\033[90m"         # gray
HEADER = "\033[2m"        # dim
SEPARATOR = "\033[90m"    # gray
ASSISTANT = "\033[37m"    # white

ROLE_COLORS = {
    "system": META,
    "user": FIELD,
    "assistant": ASSISTANT,
    "tool": OUTPUT,
}


# ======================================================================
# ANSI / terminal text
# ======================================================================

ANSI_RE = re.compile(
    r"\x1b(?:"
    r"\[[0-?]*[ -/]*[@-~]"
    r"|"
    r"\][^\x07]*(?:\x07|\x1b\\)"
    r")"
)

BREAK_CHARS = frozenset(
    ",.;:!?，。；：！？、"
    ")]}>）】》」』"
    "/\\_-="
)


def color_text(
    text: str,
    color: str,
    *,
    enabled: bool = True,
) -> str:
    if not enabled:
        return text

    return f"{color}{text}{RESET}"


def tokenize_ansi(
    text: str,
) -> list[tuple[str, int]]:
    """
    Split text into ANSI escape sequences and visible characters.

    ANSI sequences have display width 0.
    """
    tokens = []
    pos = 0

    for match in ANSI_RE.finditer(text):
        visible = text[pos:match.start()]

        for char in visible:
            width = wcswidth(char)
            tokens.append((char, max(width, 0)))

        tokens.append((match.group(0), 0))
        pos = match.end()

    visible = text[pos:]

    for char in visible:
        width = wcswidth(char)
        tokens.append((char, max(width, 0)))

    return tokens


def display_width(
    text: str,
) -> int:
    """
    Return terminal display width.

    ANSI escape sequences do not contribute to width.
    """
    return sum(
        width
        for _, width in tokenize_ansi(text)
    )


def pad_left(
    text: str,
    width: int,
) -> str:
    """
    Right-align text using terminal display width.
    """
    return (
        " " * max(width - display_width(text), 0)
        + text
    )


def terminal_width() -> int:
    try:
        if sys.stdout.isatty():
            return shutil.get_terminal_size(
                fallback=(120, 24)
            ).columns

        with open("/dev/tty", "rb") as tty:
            return os.get_terminal_size(
                tty.fileno()
            ).columns

    except (OSError, ValueError):
        return 120


# ======================================================================
# Wrapping
# ======================================================================

def wrap_line(
    text: str,
    width: int,
) -> list[str]:
    """
    Wrap one logical line according to terminal display width.

    ANSI sequences:
      - have zero display width
      - are never split
      - are preserved verbatim

    Prefer breaking after whitespace or punctuation.
    """
    if width <= 0:
        return [text]

    if display_width(text) <= width:
        return [text]

    tokens = tokenize_ansi(text)
    lines = []
    start = 0

    while start < len(tokens):
        current_width = 0
        break_at = None
        hard_break_at = start

        for index in range(start, len(tokens)):
            token, token_width = tokens[index]

            if current_width + token_width > width:
                break

            current_width += token_width
            hard_break_at = index + 1

            if token_width and (
                token.isspace()
                or token in BREAK_CHARS
            ):
                break_at = index + 1

        if break_at is None:
            break_at = hard_break_at

        if break_at <= start:
            break_at = start + 1

        lines.append(
            "".join(
                token
                for token, _ in tokens[start:break_at]
            )
        )

        start = break_at

    return lines or [""]


def wrap_text(
    text: str,
    width: int,
) -> list[tuple[str, bool]]:
    """
    Wrap text while preserving logical newlines.

    Returns:
        (line, wrapped)

    wrapped=False:
        Original logical line.

    wrapped=True:
        Automatically wrapped continuation.
    """
    result = []

    for original_line in text.splitlines() or [""]:
        lines = wrap_line(
            original_line,
            width,
        )

        for index, line in enumerate(lines):
            result.append(
                (line, index > 0)
            )

    return result


# ======================================================================
# JSON / Python conversion
# ======================================================================

def decode_json(
    value: Any,
) -> Any:
    """
    Decode a JSON string.

    Invalid JSON is returned unchanged.
    """
    if not isinstance(value, str):
        return value

    try:
        return json.loads(value)
    except json.JSONDecodeError:
        return value


def convert_value(
    column: str,
    value: Any,
    *,
    role: str | None = None,
) -> Any:
    """
    Convert SQLite values into Python values.

    *_json:
        Decode automatically.

    messages.content where role == tool:
        Decode automatically.
    """
    if column.endswith("_json"):
        return decode_json(value)

    if column == "content" and role == "tool":
        return decode_json(value)

    return value


def convert_row(
    row: Any,
) -> dict[str, Any]:
    """
    Convert one database / JSONL row into decoded values.

    Accepts any mapping (sqlite3.Row, dict, ...), so it works for both
    the SQLite and the JSONL loader.
    """
    values = dict(row)
    role = values.get("role")

    return {
        column: convert_value(
            column,
            value,
            role=role,
        )
        for column, value in values.items()
    }


def _decoded_key(
    key: str,
    existing_keys: set[str],
) -> str:
    base = key[:-6]  # remove "_bytes"

    if base and base not in existing_keys:
        return base

    candidate = f"{base}_decoded"
    index = 2

    while candidate in existing_keys:
        candidate = f"{base}_decoded_{index}"
        index += 1

    return candidate


def _decode_bytes_value(
    value: str,
) -> Any:
    """
    Decode a Base64-encoded value.

    If the decoded UTF-8 data is valid JSON, return the parsed JSON value.
    Otherwise return the decoded UTF-8 string.

    Raise ValueError if the value is not valid Base64 or UTF-8.
    """
    try:
        decoded = base64.b64decode(
            value,
            validate=True,
        ).decode("utf-8")
    except (binascii.Error, UnicodeDecodeError) as exc:
        raise ValueError from exc

    try:
        return json.loads(decoded)
    except json.JSONDecodeError:
        return decoded


def to_json_value(
    value: Any,
) -> Any:
    """
    Convert Python values into JSON-compatible values.

    For dictionary keys ending with '_bytes', try to Base64-decode the
    value as UTF-8 text. The original value is preserved, and the decoded
    value is added under a non-conflicting key.

    If the decoded value contains valid JSON, parse it recursively.
    """
    if is_dataclass(value):
        return to_json_value(asdict(value))

    if isinstance(value, dict):
        result = {}

        for key, item in value.items():
            key = str(key)
            result[key] = to_json_value(item)

            if key.endswith("_bytes") and isinstance(item, str):
                try:
                    decoded = _decode_bytes_value(item)
                except ValueError:
                    continue

                decoded_key = _decoded_key(
                    key,
                    set(result),
                )
                result[decoded_key] = to_json_value(decoded)

        return result

    if isinstance(value, (list, tuple)):
        return [
            to_json_value(item)
            for item in value
        ]

    if isinstance(value, bytes):
        return value.hex()

    return value


def save_json(
    data: Any,
    filename: str,
) -> None:
    with open(
        filename,
        "w",
        encoding="utf-8",
    ) as fp:
        json.dump(
            to_json_value(data),
            fp,
            ensure_ascii=False,
            indent=2,
        )
        fp.write("\n")


# ======================================================================
# SQLite type -> Python type
# ======================================================================

def sqlite_type_to_python(
    sqlite_type: str,
) -> type:
    """
    Map SQLite declared type to Python type.

    SQLite uses type affinity rather than strict static typing.
    Unknown / NUMERIC types are mapped to Any.
    """
    if not sqlite_type:
        return Any

    type_name = sqlite_type.upper().strip()

    base = type_name.split(
        "(",
        1,
    )[0].strip()

    if "INT" in base:
        return int

    if any(
        x in base
        for x in ("CHAR", "CLOB", "TEXT")
    ):
        return str

    if any(
        x in base
        for x in ("REAL", "FLOA", "DOUB")
    ):
        return float

    if "BLOB" in base:
        return bytes

    return Any


# ======================================================================
# Dynamic dataclass
# ======================================================================

# PRAGMA table_info() column ->
#   (JSONL schema dict key, whether the attribute is optional)
PRAGMA_FIELDS = {
    "name": ("name", False),
    "type": ("type", False),
    "notnull": ("notnull", True),
    "pk": ("pk", True),
}


def pragma_columns(
    rows: list[sqlite3.Row],
) -> list[dict[str, Any]]:
    """
    Convert PRAGMA table_info() rows into column dicts for make_model().

    sqlite3.Row has no .get(), so columns are read one by one; anything
    missing falls back to the same defaults SQLite uses.
    """
    columns = []

    for row in rows:
        keys = row.keys()
        column = {}

        for pragma_key, (key, optional) in (
            PRAGMA_FIELDS.items()
        ):
            if pragma_key in keys:
                column[key] = row[pragma_key]
            elif not optional:
                column[key] = ""
            else:
                column[key] = 0

        columns.append(column)

    return columns


def make_model(
    name: str,
    schema: list[Any],
    type_overrides: dict[str, type] | None = None,
):
    """
    Dynamically create a dataclass from column definitions.

    Each column is a dict produced by pragma_columns() with:
        name     - field name
        type     - Python type, or a SQLite type name ("TEXT", "INT", ...)
                   that is mapped through sqlite_type_to_python()
        notnull  - False marks the field as Optional
        pk       - True marks the field as non-Optional
    """
    type_overrides = type_overrides or {}
    model_fields = []

    for column in schema:
        column_name = column["name"]

        column_type = column["type"]

        if isinstance(column_type, type):
            default_type = column_type
        else:
            default_type = sqlite_type_to_python(
                column_type or ""
            )

        python_type = type_overrides.get(
            column_name,
            default_type,
        )

        if (
            not column["notnull"]
            and not column["pk"]
            and python_type is not Any
        ):
            python_type = python_type | None

        model_fields.append(
            (column_name, python_type)
        )

    return make_dataclass(
        name,
        model_fields,
    )


# ======================================================================
# Database
# ======================================================================

class Database:
    """
    Read-only SQLite database wrapper.
    """

    def __init__(
        self,
        filename: str,
        *,
        detect_types: bool = False,
    ):
        flags = 0

        if detect_types:
            flags = (
                sqlite3.PARSE_DECLTYPES
                | sqlite3.PARSE_COLNAMES
            )

        self.conn = sqlite3.connect(
            f"file:{filename}?mode=ro",
            uri=True,
            detect_types=flags,
        )

        self.conn.row_factory = sqlite3.Row
        self.conn.text_factory = self.sqlite_text_factory
        # lambda b: b.decode("utf-8", errors="replace")

    def sqlite_text_factory(self, data: bytes) -> str:
        try:
            return data.decode("utf-8")
        except UnicodeDecodeError:
            pass

        try:
            return data.decode("gb18030")
        except UnicodeDecodeError:
            pass

        return data.decode("utf-8", errors="replace")

        # UTF-8 decode failed and GB18030 decode also failed.
        # Preserve valid UTF-8 and represent invalid bytes as \\xNN.
        result = []
        pos = 0

        print("mixed", data)
        while pos < len(data):
            try:
                # Decode the remaining data. If successful, we're done.
                result.append(data[pos:].decode("utf-8"))
                break
            except UnicodeDecodeError as e:
                # Valid UTF-8 prefix before the invalid byte(s).
                if e.start > 0:
                    result.append(data[pos:pos + e.start].decode("utf-8"))
                    pos += e.start

                # e.start points to the first invalid byte.
                # Skip the invalid byte and represent it as \\xNN.
                result.append(f"\\x{data[pos]:02x}")
                pos += 1

        return "".join(result)

    def execute(
        self,
        sql: str,
        params=(),
    ) -> sqlite3.Cursor:
        return self.conn.execute(
            sql,
            params,
        )

    def tables(self) -> list[str]:
        rows = self.execute(
            """
            SELECT name
            FROM sqlite_master
            WHERE type = 'table'
              AND name NOT LIKE 'sqlite_%'
            ORDER BY name
            """
        )

        return [
            row["name"]
            for row in rows
        ]

    def table_schema(
        self,
        table: str,
    ) -> list[sqlite3.Row]:
        rows = self.execute(
            f'PRAGMA table_info("{table}")'
        ).fetchall()

        if not rows:
            raise ValueError(
                f"table not found: {table}"
            )

        return rows

    def close(self) -> None:
        self.conn.close()

    def __enter__(self) -> Database:
        return self

    def __exit__(
        self,
        exc_type,
        exc,
        tb,
    ) -> None:
        self.close()


# ======================================================================
# Generic Table
# ======================================================================

class Table:
    """
    Generic SQLite table accessor.
    """

    def __init__(
        self,
        db: Database,
        name: str,
        *,
        model_name: str | None = None,
        type_overrides: dict[str, type] | None = None,
    ):
        self.db = db
        self.name = name

        self.model = make_model(
            model_name or name,
            pragma_columns(
                db.table_schema(name)
            ),
            type_overrides,
        )

    def _from_row(
        self,
        row: sqlite3.Row,
    ) -> Any:
        return self.model(
            **convert_row(row)
        )

    def _rows(
        self,
        sql: str,
        params=(),
    ) -> Iterator[Any]:
        cursor = self.db.execute(
            sql,
            params,
        )

        for row in cursor:
            yield self._from_row(row)

    def all(self) -> Iterator[Any]:
        yield from self._rows(
            f'''
            SELECT *
            FROM "{self.name}"
            '''
        )

    def first(self) -> Any | None:
        row = self.db.execute(
            f'''
            SELECT *
            FROM "{self.name}"
            LIMIT 1
            '''
        ).fetchone()

        if row is None:
            return None

        return self._from_row(row)

    def get(
        self,
        value: Any,
        column: str = "id",
    ) -> Any | None:
        row = self.db.execute(
            f'''
            SELECT *
            FROM "{self.name}"
            WHERE "{column}" = ?
            LIMIT 1
            ''',
            (value,),
        ).fetchone()

        if row is None:
            return None

        return self._from_row(row)

    def where(
        self,
        condition: str,
        params=(),
    ) -> Iterator[Any]:
        yield from self._rows(
            f'''
            SELECT *
            FROM "{self.name}"
            WHERE {condition}
            ''',
            params,
        )

    def order_by(
        self,
        column: str,
        *,
        descending: bool = False,
    ) -> Iterator[Any]:
        direction = "DESC" if descending else "ASC"

        yield from self._rows(
            f'''
            SELECT *
            FROM "{self.name}"
            ORDER BY "{column}" {direction}
            '''
        )

    def count(self) -> int:
        row = self.db.execute(
            f'''
            SELECT COUNT(*)
            FROM "{self.name}"
            '''
        ).fetchone()

        return row[0]

    def count_where(
        self,
        condition: str,
        params=(),
    ) -> int:
        row = self.db.execute(
            f'''
            SELECT COUNT(*)
            FROM "{self.name}"
            WHERE {condition}
            ''',
            params,
        ).fetchone()

        return row[0]


# ======================================================================
# SQLite -> Python
# ======================================================================

def load_database(
    filename: str,
) -> dict[str, list[Any]]:
    """
    Load SQLite data into Python dataclasses.

    All columns are preserved.
    """
    with Database(filename) as db:
        turns = list(
            Table(db, "turns").all()
        )

        messages = list(
            Table(db, "messages").all()
        )

    return {
        "turns": turns,
        "messages": messages,
    }


# ======================================================================
# JSONL -> Python
# ======================================================================

TURN_TYPE = str
"""Python type used for the turn id in JSONL mode.

The JSONL format stores turn ids as JSON numbers. Override this (or the
corresponding type_overrides entry) when ids are strings.
"""

class JsonlError(ValueError):
    """Malformed JSONL input."""


def _jsonl_get(
    obj: dict[str, Any],
    *keys: str,
    default: Any = None,
) -> Any:
    """
    Return the first key that exists in obj.

    Unlike dict.get(), an existing key with value None is still returned,
    so explicit nulls are distinguishable from absent keys.
    """
    for key in keys:
        if key in obj:
            return obj[key]

    return default


def read_jsonl(
    filename: str,
) -> Iterator[dict[str, Any]]:
    """
    Yield JSON objects from a JSONL file, one per non-empty line.

    Blank lines are ignored. Malformed lines raise JsonlError with the
    file name and line number; `#` comments are not supported.
    """
    with open(
        filename,
        "r",
        encoding="utf-8",
        errors="replace",
    ) as fp:
        for lineno, line in enumerate(fp, 1):
            line = line.strip()

            if not line:
                continue

            try:
                record = json.loads(line)
            except json.JSONDecodeError as exc:
                raise JsonlError(
                    f"{filename}:{lineno}: invalid JSON: {exc.msg}"
                ) from exc

            if not isinstance(record, dict):
                raise JsonlError(
                    f"{filename}:{lineno}: expected a JSON object, "
                    f"got {type(record).__name__}"
                )

            yield record


def jsonl_record_type(
    obj: dict[str, Any],
) -> str:
    """
    Return the record type of one JSONL object.

    Supported keys:
        t      - record type used by agent session logs (header/turn/...)
        type   - alias of "t"
        record - alias; an "attempt" record wraps its payload under it
    """
    value = _jsonl_get(obj, "t", "type", "record")

    if value is None:
        return ""

    return str(value)


class _JsonlColumns:
    """
    Ordered column set with first-seen types, used to build a dataclass.

    The schema is a dict, so missing values naturally become None when a
    record is turned into the dataclass.
    """

    def __init__(self):
        self.schema: dict[str, dict[str, Any]] = {}

    def add(
        self,
        key: str,
        value: Any,
    ) -> None:
        column = self.schema.get(key)

        if column is None:
            column = {"name": key, "type": Any, "notnull": False, "pk": False}
            self.schema[key] = column

        if column["type"] is Any and value is not None:
            column["type"] = type(value)

    def add_mapping(
        self,
        values: dict[str, Any],
    ) -> None:
        for key, value in values.items():
            self.add(str(key), value)

    def model(
        self,
        name: str,
        type_overrides: dict[str, type] | None = None,
    ):
        type_overrides = type_overrides or {}

        schema = [
            {
                **column,
                "type": type_overrides.get(
                    column["name"],
                    column["type"],
                ),
            }
            for column in self.schema.values()
        ]

        return make_model(
            name,
            schema,
        )

    def convert(
        self,
        values: dict[str, Any],
    ) -> dict[str, Any]:
        return convert_row(
            {
                column: values.get(column)
                for column in self.schema
            }
        )


def _jsonl_messages(
    obj: dict[str, Any],
) -> list[dict[str, Any]]:
    """
    Extract the message list of one round-like JSONL record.

    Accepted shapes:
        {"messages": [...]}            explicit message list
        {"assistant": {...}, "tools": [...]}
        {"role": "...", "content": ...}
    """
    messages = _jsonl_get(obj, "messages")

    if isinstance(messages, list):
        return [
            message
            for message in messages
            if isinstance(message, dict)
        ]

    result = []

    assistant = _jsonl_get(obj, "assistant")

    if isinstance(assistant, dict):
        result.append(assistant)

    tools = _jsonl_get(obj, "tools")

    if isinstance(tools, list):
        result.extend(
            tool
            for tool in tools
            if isinstance(tool, dict)
        )

    if result:
        return result

    if "role" in obj:
        return [obj]

    return []


def _jsonl_turn_id(
    obj: dict[str, Any],
    index: int,
) -> int:
    value = _jsonl_get(obj, "id", "turn", "turn_id")

    if value is None and "turn" in obj:
        value = obj["turn"]

    return value if value is not None else index


def _jsonl_round_index(
    obj: dict[str, Any],
    index: int,
) -> int:
    value = _jsonl_get(obj, "round_index", "i", "round")

    return value if value is not None else index


def _jsonl_pairs(
    records: list[dict[str, Any]],
) -> tuple[
    set[tuple[Any, Any]],
    dict[tuple[Any, Any], list[int]],
]:
    """
    Return (message-bearing rounds, attempts per round).

    An "attempt" record may carry several candidates for one round; only
    the one that was actually chosen (the matching round) is displayed.
    """
    rounds = set()
    attempts: dict[tuple[Any, Any], list[int]] = {}

    for index, obj in enumerate(records):
        record_type = jsonl_record_type(obj)
        turn_id = _jsonl_turn_id(obj, index)
        round_index = _jsonl_round_index(obj, index)

        if record_type == "round":
            rounds.add((turn_id, round_index))

        elif record_type == "attempt":
            attempts.setdefault(
                (turn_id, round_index),
                [],
            ).append(index)

    return rounds, attempts


def _is_jsonl_turn(
    record_type: str,
    obj: dict[str, Any],
) -> bool:
    if record_type == "turn":
        return True

    return (
        record_type == ""
        and any(
            key in obj
            for key in ("system", "user", "context")
        )
    )


def load_jsonl(
    filename: str,
) -> dict[str, list[Any]]:
    """
    Load JSONL agent logs into Python dataclasses.

    Recognized record types (key "t", or "type"/"record"):

        header    ignored
        turn      one row per turn; the final "end" record of the turn
                  contributes status / usage / total_usage / at
        round     one assistant message plus one message per tool result;
                  the optional "usage" belongs to the assistant message.
                  Every other field of the round (turn, i, ...) is kept.
        attempt   a candidate response for the same round. Records whose
                  round was never stored as a "round" are kept as an
                  assistant message, otherwise the chosen "round" wins.
        end       merged into its turn, never displayed on its own
        other     a round-like / message-like object is still displayed,
                  anything else is ignored

    Records written by `--json` ({role, content, tool_call_id}) are
    accepted as messages. Totals stay correct because `usage` is read
    alongside the SQLite-style `usage_json` field.

    All fields are preserved; columns that only some records have are
    None for the others.
    """
    records = list(
        read_jsonl(filename)
    )

    rounds, attempts = _jsonl_pairs(records)

    turns_columns = _JsonlColumns()
    messages_columns = _JsonlColumns()

    turns: list[dict[str, Any]] = []
    messages: list[dict[str, Any]] = []
    ends: dict[Any, dict[str, Any]] = {}

    def add_message(
        values: dict[str, Any],
    ) -> None:
        messages_columns.add_mapping(values)
        messages.append(values)

    for index, obj in enumerate(records):
        record_type = jsonl_record_type(obj)

        if record_type in ("header", "end"):
            if record_type == "end":
                ends[_jsonl_turn_id(obj, index)] = obj

            continue

        turn_id = _jsonl_turn_id(obj, index)

        if _is_jsonl_turn(record_type, obj):
            values = {
                key: value
                for key, value in obj.items()
                if key not in ("t", "type", "record")
            }
            values["id"] = turn_id

            turns_columns.add_mapping(values)
            turns.append(values)

            continue

        round_index = _jsonl_round_index(obj, index)

        pair = (turn_id, round_index)

        if record_type == "attempt" and pair in rounds:
            continue

        if record_type == "attempt" and not (
            isinstance(obj.get("assistant"), dict)
            or isinstance(obj.get("tools"), list)
            or isinstance(obj.get("messages"), list)
        ):
            # no stored round for this attempt: show its payload
            values = (
                obj["record"]
                if isinstance(obj.get("record"), dict)
                else obj
            )
            values = dict(values)
            values.pop("round_index", None)
            values["turn_id"] = turn_id
            values["round_index"] = round_index
            add_message(values)

            continue

        round_messages = _jsonl_messages(obj)

        if (
            not round_messages
            and record_type not in ("round", "response")
            and "role" not in obj
        ):
            continue

        round_usage = (
            _jsonl_get(obj, "usage")
            if record_type == "round"
            else None
        )

        extra = {
            key: value
            for key, value in obj.items()
            if key in ("turn", "i")
        }

        for position, message in enumerate(round_messages):
            values = dict(message)
            values["turn_id"] = turn_id
            values["round_index"] = round_index

            if "position" not in values:
                values["position"] = position

            values.update(extra)

            if (
                position == 0
                and round_usage is not None
                and "usage" not in values
            ):
                values["usage"] = round_usage

            add_message(values)

    for turn in turns:
        end = ends.get(turn.get("id"))

        if end is None:
            continue

        for key, value in end.items():
            if key in ("t", "type", "record", "turn", "id"):
                continue

            turn[key] = value

    # columns may only appear on the "end" record (e.g. total_usage)
    for turn in turns:
        turns_columns.add_mapping(turn)

    def build(
        columns: _JsonlColumns,
        name: str,
        type_overrides: dict[str, type] | None,
        values_list: list[dict[str, Any]],
    ) -> list[Any]:
        model = columns.model(
            name,
            type_overrides,
        )

        return [
            model(**columns.convert(values))
            for values in values_list
        ]

    return {
        "turns": build(
            turns_columns,
            "turns",
            {"id": TURN_TYPE},
            turns,
        ),
        "messages": build(
            messages_columns,
            "messages",
            None,
            messages,
        ),
    }

class _YamlDumper(yaml.SafeDumper):
    pass


def _represent_str(dumper, value):
    node = dumper.represent_str(value)
    if "\n" in value:
        node.style = "|"
    return node


_YamlDumper.add_representer(str, _represent_str)


def format_value(value: Any) -> str:
    """Convert a Python value into terminal display text.

    Strings are preserved.
    dict/list are pretty-printed as YAML.
    Multiline strings use YAML block scalars.
    """
    if isinstance(value, str):
        return value

    if isinstance(value, (dict, list)):
        return yaml.dump(
            to_json_value(value),
            Dumper=_YamlDumper,
            allow_unicode=True,
            sort_keys=False,
            default_flow_style=False,
            width=10**9,
        ).rstrip("\n")

    return str(value)

def get_nested(
    value: Any,
    *paths: tuple[str, ...],
) -> Any:
    """
    Return the first existing non-None nested dictionary value.

    Every path is tried, so a key that exists but holds None does not
    hide a value available under a later path.
    """
    for path in paths:
        current = value

        for key in path:
            if not isinstance(current, dict):
                break

            current = current.get(key)

            if current is None:
                break
        else:
            return current

    return None


def get_token_usage(
    value: Any,
) -> tuple[int, int, int, int, int]:
    """
    Return input, uncached, cached, output and reasoning tokens.

    uncached is always calculated as:
        input - cached
    """
    if not isinstance(value, dict):
        return 0, 0, 0, 0, 0

    input_tokens = get_nested(
        value,
        ("input_tokens",),
        ("prompt_tokens",),
    ) or 0

    cached_tokens = get_nested(
        value,
        ("input_details", "cached_tokens"),
        ("input_tokens_details", "cached_tokens"),
        ("prompt_tokens_details", "cached_tokens"),
    ) or 0

    output_tokens = get_nested(
        value,
        ("output_tokens",),
        ("completion_tokens",),
    ) or 0

    reasoning_tokens = get_nested(
        value,
        ("output_tokens_details", "reasoning_tokens"),
        ("completion_tokens_details", "reasoning_tokens"),
    ) or 0

    uncached_tokens = max(
        input_tokens - cached_tokens,
        0,
    )

    return (
        int(input_tokens),
        int(uncached_tokens),
        int(cached_tokens),
        int(output_tokens),
        int(reasoning_tokens),
    )


def format_usage(
    value: Any,
) -> str | None:
    """
    Format token usage.

    Example:

        ↑38,861 (205 + *38,656) · ↓565 [#123]
    """
    (
        input_tokens,
        uncached_tokens,
        cached_tokens,
        output_tokens,
        reasoning_tokens,
    ) = get_token_usage(value)

    if not any(
        (
            input_tokens,
            cached_tokens,
            output_tokens,
            reasoning_tokens,
        )
    ):
        return None

    parts = []

    if input_tokens:
        input_text = f"↑{input_tokens:,}"

        if cached_tokens:
            input_text += (
                f" ({uncached_tokens:,}"
                f" + *{cached_tokens:,})"
            )

        parts.append(input_text)

    if output_tokens:
        output_text = f"↓{output_tokens:,}"

        if reasoning_tokens:
            output_text += (
                f" [#{reasoning_tokens:,}]"
            )

        parts.append(output_text)

    return " · ".join(parts)


# ======================================================================
# Token summary
# ======================================================================

def add_token_usage(
    totals: dict[Any, list[int]],
    turn_id: Any,
    usage: Any,
) -> None:
    """
    Add one usage record to a turn.

    Values are:
        [input, uncached, cached, output, reasoning]
    """
    if turn_id is None:
        return

    values = get_token_usage(usage)

    if turn_id not in totals:
        totals[turn_id] = [0] * 5

    for index, value in enumerate(values):
        totals[turn_id][index] += value


def token_summary_widths(
    rows: list[tuple[Any, list[int]]],
    total: list[int],
) -> tuple[int, list[int]]:
    """
    Calculate natural column widths.

    The first column is the turn id.
    The second area contains all token fields.
    """
    headers = [
        "↑input",
        "uncached",
        "*cached",
        "↓output",
        "#reasoning",
    ]

    turn_width = max(
        len("turn"),
        len("total"),
    )

    for turn_id, _ in rows:
        turn_width = max(
            turn_width,
            display_width(str(turn_id)),
        )

    widths = [
        display_width(header)
        for header in headers
    ]

    for _, values in rows:
        for index, value in enumerate(values):
            widths[index] = max(
                widths[index],
                len(f"{value:,}"),
            )

    for index, value in enumerate(total):
        widths[index] = max(
            widths[index],
            len(f"{value:,}"),
        )

    return turn_width, widths


def fit_token_widths(
    turn_width: int,
    widths: list[int],
    config: OutputConfig,
) -> tuple[int, list[int], int]:
    """
    Fit token columns into the configured terminal width.

    Token columns are separated by spaces, not vertical borders.
    The function reduces inter-column spacing before reducing
    numeric column widths.
    """
    border_width = (
        turn_width
        + 1
        + 1
    )

    natural_width = (
        turn_width
        + 1
        + sum(widths)
        + 2 * (len(widths) - 1)
        + 1
    )

    if natural_width <= config.width:
        return turn_width, widths, 2

    spacing = 1

    available = (
        config.width
        - turn_width
        - 2
    )

    minimum = len(widths)

    if available < minimum:
        return (
            turn_width,
            [max(1, width) for width in widths],
            spacing,
        )

    widths = widths[:]

    while (
        sum(widths)
        + spacing * (len(widths) - 1)
        > available
    ):
        candidates = [
            index
            for index, width in enumerate(widths)
            if width > 1
        ]

        if not candidates:
            break

        index = max(
            candidates,
            key=lambda item: widths[item],
        )

        widths[index] -= 1

    return (
        turn_width,
        widths,
        spacing,
    )


def token_border(
    config: OutputConfig,
    turn_width: int,
    *,
    top: bool,
) -> None:
    """
    Print the top or bottom border of the token table.

    The token area is intentionally treated as one column.
    """
    middle = "╦" if top else "╩"
    right = "╗" if top else "╝"

    right_width = max(
        config.width
        - turn_width
        - display_width(middle)
        - display_width(right),
        0,
    )

    line = (
        "═" * turn_width
        + middle
        + "═" * right_width
        + right
    )

    print(
        color_text(
            line,
            SEPARATOR,
            enabled=config.color,
        )
    )


def token_separator(
    config: OutputConfig,
    turn_width: int,
) -> None:
    """Print the separator between token table sections."""
    right_width = max(
        config.width
        - turn_width
        - display_width("╬")
        - display_width("╣"),
        0,
    )

    line = (
        "═" * turn_width
        + "╬"
        + "═" * right_width
        + "╣"
    )

    print(
        color_text(
            line,
            SEPARATOR,
            enabled=config.color,
        )
    )


def print_token_summary(
    turns: list[Any],
    messages: list[Any],
    config: OutputConfig,
) -> None:
    """
    Print total token usage for each turn.

    A turn total consists of:
        turns.usage_json (or turns.usage)
        + all messages.usage_json (or messages.usage) with the same turn_id.
    """
    totals: dict[Any, list[int]] = {}

    for turn in turns:
        add_token_usage(
            totals,
            getattr(turn, "id", None),
            get_nested(
                vars(turn),
                ("usage_json",),
                ("usage",),
            ),
        )

    for message in messages:
        add_token_usage(
            totals,
            getattr(message, "turn_id", None),
            get_nested(
                vars(message),
                ("usage_json",),
                ("usage",),
            ),
        )

    if not any(
        any(values)
        for values in totals.values()
    ):
        return

    rows = sorted(
        totals.items(),
        key=lambda item: item[0],
    )

    grand_total = [0] * 5

    for _, values in rows:
        for index, value in enumerate(values):
            grand_total[index] += value

    turn_width, widths = token_summary_widths(
        rows,
        grand_total,
    )

    turn_width, widths, spacing = fit_token_widths(
        turn_width,
        widths,
        config,
    )

    print()
    print_header(
        "Token Usage",
        config,
    )

    token_border(
        config,
        turn_width,
        top=True,
    )

    separator = color_text(
        "║",
        SEPARATOR,
        enabled=config.color,
    )

    headers = [
        "↑input",
        "↗uncached",
        "*cached",
        "↓output",
        "#reasoning",
    ]

    header_values = [
        pad_left(
            header,
            width,
        )
        for header, width in zip(
            headers,
            widths,
        )
    ]

    header_line = (
        pad_left("turn", turn_width)
        + separator
        + (" " * spacing).join(header_values)
    )

    print(header_line)

    token_separator(
        config,
        turn_width,
    )

    def print_row(
        label: str,
        values: list[int],
    ) -> None:
        left = color_text(
            pad_left(
                label,
                turn_width,
            ),
            FIELD,
            enabled=config.color,
        )

        right = (" " * spacing).join(
            pad_left(
                f"{value:,}",
                width,
            )
            for value, width in zip(
                values,
                widths,
            )
        )

        print(
            left
            + separator
            + right
        )

    for turn_id, values in rows:
        print_row(
            str(turn_id),
            values,
        )

    token_separator(
        config,
        turn_width,
    )

    print_row(
        "total",
        grand_total,
    )

    token_border(
        config,
        turn_width,
        top=False,
    )


# ======================================================================
# Output configuration
# ======================================================================

class OutputConfig:
    def __init__(
        self,
        *,
        width: int | None = None,
        wrap: bool = True,
        color: bool = True,
        footer: bool = True,
    ):
        self.width = (
            width
            if width is not None
            else terminal_width()
        )

        self.wrap = wrap
        self.color = color
        self.footer = footer


# ======================================================================
# Record -> display rows
# ======================================================================

# Alias tables per input source: raw field name -> canonical field name
# expected by MESSAGE_HIDDEN_FIELDS / print_message_header.
#
# Each input format names the same data differently, so the translation is
# kept per source instead of as one flat table; only the source that
# actually produces the raw name lists it here. Formats that already use
# the canonical names (SQLite) need no entry at all.
#
# A listed alias row is hidden only when its value equals the canonical
# field's value (see record_rows), i.e. when it duplicates data already
# shown in the header; a field that carries different data is still shown.
MESSAGE_FIELD_ALIASES: dict[str, dict[str, str]] = {
    # "turn"/"i" are the JSONL names of turn_id/round_index.
    SOURCE_JSONL: {
        "turn": "turn_id",
        "i": "round_index",
    },
}


def message_field_aliases(
    source: str,
) -> dict[str, str]:
    """
    Return the alias table that applies to the given input source.

    Unknown / alias-less sources get an empty table, so nothing is
    translated and no field is dropped by accident.
    """
    return MESSAGE_FIELD_ALIASES.get(
        source,
        {},
    )


def record_rows(
    record: Any,
    *,
    hidden: set[str] | None = None,
    expand: tuple[str, ...] = ("content",),
    aliases: dict[str, str] | None = None,
) -> list[tuple[str, Any]]:
    """
    Convert a Python record into display rows.

    Dataclass fields are displayed unless explicitly hidden.

    tool messages keep their payload in a JSON object; for every field
    named in `expand` (default: "content"), the object is flattened into
    one row per subkey.

    `aliases` maps an alias field to its canonical field. An alias row is
    hidden only when its value equals the canonical field's value (i.e.
    it is a pure duplicate already shown in the header). If the alias
    carries any other value it is kept, so unrelated columns that happen
    to share a JSONL alias name are never dropped.
    """
    hidden = hidden or set()
    aliases = aliases or {}
    role = getattr(record, "role", None)
    fields = vars(record)
    rows = []

    for key, value in fields.items():
        if key in hidden:
            continue

        canonical = aliases.get(key)

        if (
            canonical is not None
            and canonical in fields
            and fields[canonical] == value
        ):
            continue

        if value is None or value == "":
            continue

        if (
            key in expand
            and role == "tool"
            and isinstance(value, dict)
        ):
            for subkey, subvalue in value.items():
                if subvalue is None or subvalue == "":
                    continue

                rows.append(
                    (subkey, subvalue)
                )

            continue

        rows.append((key, value))

    return rows


def value_color_for_key(
    key: str,
    *,
    role: str | None = None,
) -> str | None:
    if key == "stdout":
        return OUTPUT

    if key == "stderr":
        return ERROR

    if key == "content" and role == "assistant":
        return ASSISTANT

    return None


# ======================================================================
# Section border
# ======================================================================

def print_section_border(
    config: OutputConfig,
    label_width: int,
    *,
    top: bool,
) -> None:
    """
    Print a complete section border.
    """
    middle = "╦" if top else "╩"
    right = "╗" if top else "╝"

    right_width = max(
        config.width
        - label_width
        - display_width(middle)
        - display_width(right),
        0,
    )

    line = (
        "═" * label_width
        + middle
        + "═" * right_width
        + right
    )

    print(
        color_text(
            line,
            SEPARATOR,
            enabled=config.color,
        )
    )


# ======================================================================
# Header
# ======================================================================

def print_turn_header(
    turn: Any,
    config: OutputConfig,
) -> None:
    parts = []

    if hasattr(turn, "id"):
        parts.append(f"id {turn.id}")

    if hasattr(turn, "round_count"):
        parts.append(
            f"rounds {turn.round_count}"
        )

    if hasattr(turn, "status") and hasattr(
        turn,
        "committed_at",
    ):
        parts.append(
            f"{turn.status} @ {turn.committed_at}"
        )

    usage_text = format_usage(
        get_nested(
            vars(turn),
            ("usage_json",),
            ("usage",),
        )
    )

    if usage_text:
        parts.append(usage_text)

    print_header(
        " · ".join(parts),
        config,
    )


def print_message_header(
    record: Any,
    config: OutputConfig,
) -> None:
    parts = []

    if hasattr(record, "turn_id"):
        parts.append(
            f"turn {record.turn_id}"
        )

    if hasattr(record, "round_index"):
        parts.append(
            f"round {record.round_index}"
        )

    if hasattr(record, "position"):
        parts.append(
            f"pos {record.position}"
        )

    role = getattr(
        record,
        "role",
        None,
    )

    if role is not None:
        parts.append(str(role))

    tool_call_id = getattr(
        record,
        "tool_call_id",
        None,
    )

    if tool_call_id:
        parts.append(tool_call_id)

    job_id = getattr(
        record,
        "job_id",
        None,
    )

    if job_id:
        parts.append(job_id)

    usage_text = format_usage(
        get_nested(
            vars(record),
            ("usage_json",),
            ("usage",),
        )
    )

    if usage_text:
        parts.append(usage_text)

    plain = " / ".join(parts[:3])

    if len(parts) > 3:
        plain += (
            " · "
            + " · ".join(parts[3:])
        )

    if config.color and role is not None:
        role_text = str(role)
        marker = f" · {role_text}"
        index = plain.find(marker)

        if index >= 0:
            plain = (
                plain[:index]
                + " · "
                + color_text(
                    role_text,
                    ROLE_COLORS.get(
                        role_text,
                        META,
                    ),
                )
                + plain[
                    index + len(marker):
                ]
            )

    print_header(
        plain,
        config,
    )


def print_header(
    text: str,
    config: OutputConfig,
) -> None:
    """
    Print header outside the table.
    """
    if config.wrap:
        lines = wrap_text(
            text,
            config.width,
        )
    else:
        lines = [
            (line, False)
            for line in (
                text.splitlines()
                or [""]
            )
        ]

    for line, _ in lines:
        print(line)


# ======================================================================
# Table rows
# ======================================================================

def print_rows(
    rows: list[tuple[str, Any]],
    config: OutputConfig,
    *,
    role: str | None = None,
) -> None:
    if not rows:
        return

    label_width = max(
        display_width(key)
        for key, _ in rows
    )

    separator = color_text(
        "║",
        SEPARATOR,
        enabled=config.color,
    )

    wrapped_separator = color_text(
        "╠",
        SEPARATOR,
        enabled=config.color,
    )

    available_width = max(
        config.width
        - label_width
        - display_width("║"),
        1,
    )

    for key, value in rows:
        text = format_value(value)

        label = color_text(
            pad_left(
                key,
                label_width,
            ),
            FIELD,
            enabled=config.color,
        )

        if config.wrap:
            lines = wrap_text(
                text,
                available_width,
            )
        else:
            lines = [
                (line, False)
                for line in (
                    text.splitlines()
                    or [""]
                )
            ]

        value_color = value_color_for_key(
            key,
            role=role,
        )

        for line_index, (
            line,
            wrapped,
        ) in enumerate(lines):
            if line_index == 0:
                prefix = (
                    f"{label}{separator}"
                )
            elif wrapped:
                prefix = (
                    f"{' ' * label_width}"
                    f"{wrapped_separator}"
                )
            else:
                prefix = (
                    f"{' ' * label_width}"
                    f"{separator}"
                )

            if value_color:
                line = color_text(
                    line,
                    value_color,
                    enabled=config.color,
                )

            print(
                prefix + line
            )


# ======================================================================
# Records
# ======================================================================

TURN_HIDDEN_FIELDS = {
    "id",
    "round_count",
    "status",
    "committed_at",
    "usage_json",
}

MESSAGE_HIDDEN_FIELDS = {
    # These are the canonical field names expected to be parsed into the
    # message header (see print_message_header); the table shows everything
    # else. Other input formats may name the same data differently, so a new
    # field is only picked up correctly after MESSAGE_FIELD_ALIASES maps its
    # raw name (for that input source) onto one of the names below.
    # The lookup itself goes through message_field_aliases(source).
    # "turn"/"i", for example, are the JSONL aliases of
    # turn_id/round_index.
    "turn_id",
    "round_index",
    "position",
    "role",
    "usage_json",
    "tool_call_id",
}


def print_records(
    records: Iterator[Any],
    *,
    hidden_fields: set[str],
    header: Callable[[Any, OutputConfig], None],
    config: OutputConfig,
    aliases: dict[str, str] | None = None,
) -> None:
    """
    Print one table per record.

    `aliases` is the field alias table of the input source (see
    message_field_aliases); it is passed to record_rows.
    """
    first = True

    for record in records:
        if not first:
            print()

        first = False

        rows = record_rows(
            record,
            hidden=hidden_fields,
            aliases=aliases,
        )

        header(
            record,
            config,
        )

        if not rows:
            continue

        label_width = max(
            display_width(key)
            for key, _ in rows
        )

        print_section_border(
            config,
            label_width,
            top=True,
        )

        print_rows(
            rows,
            config,
            role=getattr(
                record,
                "role",
                None,
            ),
        )

        if config.footer:
            print_section_border(
                config,
                label_width,
                top=False,
            )


# ======================================================================
# CLI
# ======================================================================

JSONL_SUFFIXES = (
    ".jsonl",
    ".ndjson",
)


def detect_source(filename: str) -> str:
    """
    Guess the input format from the file name and its first bytes.

    File name wins:
        *.jsonl / *.ndjson  -> jsonl

    SQLite database files start with the magic string "SQLite format 3\0";
    anything else that is not an SQLite database is treated as JSONL.
    """
    lower = filename.lower()

    if lower.endswith(JSONL_SUFFIXES):
        return SOURCE_JSONL

    try:
        with open(filename, "rb") as fp:
            head = fp.read(16)
    except OSError:
        head = b""

    if head.startswith(b"SQLite format 3\x00"):
        return SOURCE_SQLITE

    return SOURCE_JSONL


def resolve_source(
    filename: str,
    source: str = SOURCE_AUTO,
) -> str:
    """
    Turn --source (possibly "auto") into the concrete input format.
    """
    if source == SOURCE_AUTO:
        return detect_source(filename)

    if source not in (SOURCE_SQLITE, SOURCE_JSONL):
        raise ValueError(
            f"unknown source: {source}"
        )

    return source


def load_source(
    filename: str,
    source: str = SOURCE_AUTO,
) -> dict[str, list[Any]]:
    """
    Load turns / messages from a SQLite database or a JSONL log.
    """
    source = resolve_source(filename, source)

    if source == SOURCE_JSONL:
        return load_jsonl(filename)

    return load_database(filename)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description=(
            "Display turn/message data from an SQLite database "
            "or a JSONL agent log."
        ),
        epilog=(
            "The input format is auto-detected from the file name "
            "(.jsonl/.ndjson) and the SQLite magic header; "
            "use --source to override it."
        ),
    )

    parser.add_argument(
        "input",
        metavar="FILE",
        help="SQLite database (.db/.sqlite) or JSONL log (.jsonl)",
    )

    parser.add_argument(
        "--source",
        choices=SOURCES,
        default=SOURCE_AUTO,
        help="input format; default: auto-detect",
    )

    parser.add_argument(
        "--json",
        metavar="FILE",
        help="export decoded input data as JSON",
    )

    parser.add_argument(
        "--width",
        type=int,
        metavar="N",
        help="output width; default: terminal width",
    )

    parser.add_argument(
        "--no-wrap",
        action="store_true",
        help="disable automatic text wrapping",
    )

    parser.add_argument(
        "--no-color",
        action="store_true",
        help="disable ANSI colors",
    )

    parser.add_argument(
        "--no-footer",
        action="store_true",
        help="disable bottom table border",
    )

    return parser.parse_args()


# ======================================================================
# Main
# ======================================================================

def main() -> None:
    args = parse_args()

    if args.width is not None and args.width <= 0:
        raise SystemExit(
            "--width must be greater than zero"
        )

    if not os.path.isfile(args.input):
        raise SystemExit(
            f"input file not found: {args.input}"
        )

    source = resolve_source(
        args.input,
        args.source,
    )

    try:
        data = load_source(
            args.input,
            source,
        )
    except (JsonlError, sqlite3.DatabaseError) as exc:
        raise SystemExit(
            f"failed to load {args.input} as {source}: {exc}"
        )

    if args.json:
        save_json(
            data,
            args.json,
        )

    config = OutputConfig(
        width=args.width,
        wrap=not args.no_wrap,
        color=not args.no_color,
        footer=not args.no_footer,
    )

    print_records(
        iter(data["turns"]),
        hidden_fields=TURN_HIDDEN_FIELDS,
        header=print_turn_header,
        config=config,
    )

    print_records(
        iter(data["messages"]),
        hidden_fields=MESSAGE_HIDDEN_FIELDS,
        header=print_message_header,
        config=config,
        aliases=message_field_aliases(source),
    )

    print_token_summary(
        data["turns"],
        data["messages"],
        config,
    )


if __name__ == "__main__":
    main()
