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
    row: sqlite3.Row,
) -> dict[str, Any]:
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

def make_model(
    name: str,
    schema: list[sqlite3.Row],
    type_overrides: dict[str, type] | None = None,
):
    """
    Dynamically create a dataclass from PRAGMA table_info().
    """
    type_overrides = type_overrides or {}
    model_fields = []

    for column in schema:
        column_name = column["name"]

        python_type = type_overrides.get(
            column_name,
            sqlite_type_to_python(
                column["type"]
            ),
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
            db.table_schema(name),
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
# Presentation
# ======================================================================

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
    Return the first existing nested dictionary value.
    """
    for path in paths:
        current = value

        for key in path:
            if not isinstance(current, dict):
                break

            current = current.get(key)
        else:
            if current is not None:
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
        turns.usage_json
        + all messages.usage_json with the same turn_id.
    """
    totals: dict[Any, list[int]] = {}

    for turn in turns:
        add_token_usage(
            totals,
            getattr(turn, "id", None),
            getattr(turn, "usage_json", None),
        )

    for message in messages:
        add_token_usage(
            totals,
            getattr(message, "turn_id", None),
            getattr(message, "usage_json", None),
        )

    if not totals:
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

def record_rows(
    record: Any,
    *,
    hidden: set[str] | None = None,
) -> list[tuple[str, Any]]:
    """
    Convert a Python record into display rows.

    All dataclass fields are displayed unless explicitly hidden.
    """
    hidden = hidden or set()
    rows = []

    for key, value in vars(record).items():
        if key in hidden:
            continue

        if value is None or value == "":
            continue

        if (
            key == "content"
            and getattr(record, "role", None) == "tool"
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
        getattr(
            turn,
            "usage_json",
            None,
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
        getattr(
            record,
            "usage_json",
            None,
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
) -> None:
    first = True

    for record in records:
        if not first:
            print()

        first = False

        rows = record_rows(
            record,
            hidden=hidden_fields,
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

def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Display SQLite turn/message data."
    )

    parser.add_argument(
        "database",
        help="SQLite database",
    )

    parser.add_argument(
        "--json",
        metavar="FILE",
        help="export decoded SQLite data as JSON",
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

    data = load_database(
        args.database,
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
    )

    print_token_summary(
        data["turns"],
        data["messages"],
        config,
    )


if __name__ == "__main__":
    main()
