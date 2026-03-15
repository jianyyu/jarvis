from __future__ import annotations


KNOWN_COMMANDS = {"new", "chat", "done", "info", "refs", "note", "ls", "q", "cc", "task"}


def parse_command(user_input: str) -> tuple[str, list[str]]:
    """Parse a slash command or plain input.

    Returns (command_name, args). If the input doesn't start with '/',
    returns ("select", [input]) for potential task selection or search.
    """
    stripped = user_input.strip()

    if not stripped:
        return ("noop", [])

    if not stripped.startswith("/"):
        return ("select", [stripped])

    # Strip the leading slash and split into parts
    parts = stripped[1:].split(None, 1)
    if not parts:
        return ("noop", [])

    cmd = parts[0].lower()
    args = [parts[1]] if len(parts) > 1 else []

    if cmd in KNOWN_COMMANDS:
        return (cmd, args)

    # Unknown slash command — treat as search filter
    return ("search", [stripped[1:]])
