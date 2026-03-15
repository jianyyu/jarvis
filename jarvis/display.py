from __future__ import annotations

import os
from datetime import datetime, timezone

from rich.console import Console
from rich.panel import Panel
from rich.table import Table
from rich.text import Text

from jarvis.models import Task

console = Console()


def format_relative_time(dt: datetime) -> str:
    """Convert a datetime to a human-readable relative time string."""
    now = datetime.now(timezone.utc)
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    delta = now - dt
    seconds = int(delta.total_seconds())

    if seconds < 60:
        return "just now"
    minutes = seconds // 60
    if minutes < 60:
        return f"{minutes}m ago"
    hours = minutes // 60
    if hours < 24:
        return f"{hours}h ago"
    days = hours // 24
    if days < 7:
        return f"{days}d ago"
    weeks = days // 7
    if weeks < 5:
        return f"{weeks}w ago"
    months = days // 30
    if months < 12:
        return f"{months}mo ago"
    years = days // 365
    return f"{years}y ago"


def _parse_iso(iso_str: str) -> datetime:
    """Parse an ISO format datetime string."""
    try:
        return datetime.fromisoformat(iso_str)
    except (ValueError, TypeError):
        return datetime.now(timezone.utc)


def clear_screen() -> None:
    """Clear the terminal screen."""
    console.clear()


def render_task_list(
    tasks: list[Task],
    selected_idx: int,
    show_done: bool = False,
    filter_text: str = "",
) -> None:
    """Render the interactive task picker list."""
    clear_screen()

    active_count = sum(1 for t in tasks if t.status == "active")
    header = Text()
    header.append("  JARVIS", style="bold cyan")
    header.append(f" -- {active_count} active task{'s' if active_count != 1 else ''}", style="dim")
    console.print()
    console.print(header)
    console.print()

    if filter_text:
        console.print(f"  / {filter_text}", style="yellow")
        console.print()

    if not tasks:
        console.print("  No tasks yet. Type ", style="dim", end="")
        console.print("/new", style="bold green", end="")
        console.print(" to create one.", style="dim")
        console.print()
        _render_footer()
        return

    # Determine terminal width for alignment
    try:
        term_width = os.get_terminal_size().columns
    except OSError:
        term_width = 80
    # Max name width: leave room for prefix (4 chars) + time (10 chars) + padding
    max_name_width = term_width - 20

    for i, task in enumerate(tasks):
        is_selected = i == selected_idx
        is_done = task.status == "done"

        # Build the line
        prefix = "  \u276f " if is_selected else "    "
        icon = "\u2713" if is_done else "\u25cf"
        icon_style = "green" if is_done else "cyan"

        name = task.name
        if len(name) > max_name_width:
            name = name[: max_name_width - 3] + "..."

        time_str = format_relative_time(_parse_iso(task.updated_at))

        line = Text()
        if is_selected:
            line.append(prefix, style="bold cyan")
            line.append(icon, style=icon_style)
            line.append(f" {name}", style="bold white")
        else:
            line.append(prefix, style="dim")
            line.append(icon, style=icon_style if not is_done else "dim green")
            line.append(f" {name}", style="white" if not is_done else "dim")

        # Right-align the time
        name_len = len(prefix) + 2 + len(name)  # prefix + icon + space + name
        padding = max(2, term_width - name_len - len(time_str) - 2)
        line.append(" " * padding, style="dim")
        line.append(time_str, style="dim")

        console.print(line)

    console.print()
    _render_footer()


def _render_footer() -> None:
    """Render the hint bar at the bottom."""
    footer = Text()
    hints = [
        ("\u2191\u2193", "navigate"),
        ("enter", "open"),
        ("/new", ""),
        ("/chat", ""),
        ("/done", ""),
        ("/info", ""),
        ("/q", ""),
    ]
    for key, desc in hints:
        footer.append(f"  {key}", style="bold")
        if desc:
            footer.append(f" {desc}", style="dim")
    console.print(footer)


def render_task_info(task: Task) -> None:
    """Render a detailed info panel for a task."""
    table = Table(show_header=False, box=None, padding=(0, 2))
    table.add_column("Key", style="bold")
    table.add_column("Value")

    status_icon = "\u25cf" if task.status == "active" else "\u2713"
    status_style = "cyan" if task.status == "active" else "green"
    status_text = Text()
    status_text.append(f"{status_icon} ", style=status_style)
    status_text.append(task.status)
    table.add_row("Status", status_text)

    if task.branches:
        table.add_row("Branches", "\n".join(task.branches))
    else:
        table.add_row("Branches", Text("none", style="dim"))

    cwd_display = task.cwd or Text("none", style="dim")
    table.add_row("Worktree", cwd_display)

    created = _parse_iso(task.created_at)
    table.add_row("Created", created.strftime("%Y-%m-%d %H:%M"))

    session_count = len(task.claude_sessions)
    if session_count > 0:
        latest = task.claude_sessions[-1]
        latest_time = format_relative_time(_parse_iso(latest.started_at))
        table.add_row("Sessions", f"{session_count} (latest: {latest_time})")
    else:
        table.add_row("Sessions", "0")

    if task.references:
        refs_text = "\n".join(
            f"\u2022 {r.type.capitalize()}: {r.label}" for r in task.references
        )
        table.add_row("References", refs_text)

    if task.tags:
        table.add_row("Tags", ", ".join(task.tags))

    panel = Panel(
        table,
        title=f"[bold]{task.name}[/bold]",
        border_style="cyan",
        expand=False,
        padding=(1, 2),
    )
    console.print()
    console.print(panel)
    console.print()


def render_references(task: Task) -> None:
    """Render the references for a task."""
    if not task.references:
        console.print(f"  No references for '{task.name}'.", style="dim")
        console.print()
        return

    table = Table(title=f"References: {task.name}", box=None, padding=(0, 2))
    table.add_column("Type", style="bold")
    table.add_column("Label")
    table.add_column("URL", style="dim")
    table.add_column("Added", style="dim")

    for ref in task.references:
        added = format_relative_time(_parse_iso(ref.added_at))
        table.add_row(ref.type, ref.label, ref.url, added)

    console.print()
    console.print(table)
    console.print()
