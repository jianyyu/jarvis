from __future__ import annotations

import os
import subprocess
from datetime import datetime
from pathlib import Path

from prompt_toolkit import PromptSession
from prompt_toolkit.formatted_text import HTML
from prompt_toolkit.key_binding import KeyBindings
from prompt_toolkit.keys import Keys

from jarvis.commands import parse_command
from jarvis.config import JarvisConfig, load_config, save_config
from jarvis.display import (
    clear_screen,
    console,
    render_references,
    render_task_info,
    render_task_list,
)
from jarvis.models import Task, generate_task_id, name_to_worktree_slug
from jarvis import enrichment, launcher, store, worktree


class JarvisApp:
    """Main interactive application for Jarvis."""

    def __init__(self) -> None:
        self.config: JarvisConfig = load_config()
        self.tasks: list[Task] = []
        self.selected_idx: int = 0
        self.show_done: bool = False
        self.running: bool = True
        self.filter_text: str = ""
        self.session: PromptSession = PromptSession()

    def reload_tasks(self) -> None:
        """Reload tasks from the store."""
        status = None if self.show_done else "active"
        self.tasks = store.list_tasks(status=status)

        # Apply filter if set
        if self.filter_text:
            lower_filter = self.filter_text.lower()
            self.tasks = [
                t for t in self.tasks if lower_filter in t.name.lower()
            ]

        # Clamp selected index
        if self.tasks:
            self.selected_idx = max(0, min(self.selected_idx, len(self.tasks) - 1))
        else:
            self.selected_idx = 0

    def draw(self) -> None:
        """Redraw the task list."""
        render_task_list(
            self.tasks,
            self.selected_idx,
            show_done=self.show_done,
            filter_text=self.filter_text,
        )

    def selected_task(self) -> Task | None:
        """Return the currently selected task, or None."""
        if not self.tasks or self.selected_idx >= len(self.tasks):
            return None
        return self.tasks[self.selected_idx]

    # -- Command handlers ---------------------------------------------------

    def cmd_new(self, inline_name: str = "") -> None:
        """Create a new task."""
        if inline_name:
            name = inline_name
        else:
            console.print()
            try:
                name = PromptSession().prompt(
                    HTML("  <b>What are you working on?</b> > "),
                )
            except (KeyboardInterrupt, EOFError):
                return

            name = name.strip()
            if not name:
                console.print("  Cancelled.", style="dim")
                return

        console.print(f"  Setting up task...", style="dim")

        # Derive slug and create worktree
        existing_slugs = {
            Path(t.cwd).name for t in store.list_tasks() if t.cwd
        }
        slug = name_to_worktree_slug(name, existing_slugs)

        repo_path = self.config.effective_repo_path()
        base_dir = self.config.effective_worktree_base_dir()

        if not repo_path or not base_dir:
            console.print(
                "  [red]Error:[/red] Could not detect repo path. "
                "Run jarvis from inside a git repository or set repo_path in ~/.jarvis/config.yaml"
            )
            return

        console.print(f"  Creating worktree...", style="dim")
        try:
            cwd, branch_name = worktree.create_worktree(slug, base_dir, repo_path)
        except subprocess.CalledProcessError as e:
            console.print(f"  [red]Error creating worktree:[/red] {e.stderr or e}")
            _pause()
            return
        task = Task(
            id=generate_task_id(),
            name=name,
            cwd=cwd,
            branches=[branch_name],
        )
        store.create_task(task)

        # Install Claude Code hooks for auto-enrichment
        enrichment.install_hooks(task)

        console.print(f'  Created task: [bold]"{name}"[/bold]')
        console.print(f"  Worktree: [dim]{cwd}[/dim]")
        console.print()

        # Save config if first run
        if not self.config.repo_path:
            self.config.repo_path = repo_path
            save_config(self.config)

        self.reload_tasks()
        # Select the newly created task
        for i, t in enumerate(self.tasks):
            if t.id == task.id:
                self.selected_idx = i
                break

        # Auto-launch Claude Code for the new task
        launcher.open_claude(task, self.config)
        self.reload_tasks()

    def cmd_chat(self, inline_name: str = "") -> None:
        """Create a lightweight task with no worktree — just open Claude Code in the main repo."""
        name = inline_name.strip() if inline_name else ""
        if not name:
            name = f"Chat {datetime.now().strftime('%b %d %H:%M')}"

        repo_path = self.config.effective_repo_path()
        if not repo_path:
            console.print(
                "  [red]Error:[/red] Could not detect repo path. "
                "Run jarvis from inside a git repository or set repo_path in ~/.jarvis/config.yaml"
            )
            return

        task = Task(
            id=generate_task_id(),
            name=name,
            cwd=repo_path,
        )
        store.create_task(task)

        # Install hooks in the main repo for session tracking
        enrichment.install_hooks(task)

        console.print(f'  Created chat task: [bold]"{name}"[/bold]')
        console.print()

        # Save config if first run
        if not self.config.repo_path:
            self.config.repo_path = repo_path
            save_config(self.config)

        self.reload_tasks()
        for i, t in enumerate(self.tasks):
            if t.id == task.id:
                self.selected_idx = i
                break

        launcher.open_claude(task, self.config)
        self.reload_tasks()

    def cmd_open(self) -> None:
        """Open Claude Code for the selected task."""
        task = self.selected_task()
        if not task:
            console.print("  No task selected.", style="dim")
            _pause()
            return

        launcher.open_claude(task, self.config)

        # After Claude exits, reload state
        self.reload_tasks()

    def cmd_done(self) -> None:
        """Mark the selected task as done."""
        task = self.selected_task()
        if not task:
            console.print("  No task selected.", style="dim")
            _pause()
            return

        console.print()
        console.print(f'  Mark [bold]"{task.name}"[/bold] as done?')

        # Check if branches are merged
        repo_path = self.config.effective_repo_path()
        has_unmerged = False
        if task.branches and repo_path:
            for branch in task.branches:
                if not worktree.is_branch_merged(branch, repo_path):
                    has_unmerged = True
                    break

        if has_unmerged:
            console.print("  [yellow]Warning:[/yellow] Branch not merged.", style="yellow")

        try:
            confirm = self.session.prompt(
                HTML("  <b>Proceed? (y/n)</b> > "),
            )
        except (KeyboardInterrupt, EOFError):
            return

        if confirm.strip().lower() not in ("y", "yes"):
            console.print("  Cancelled.", style="dim")
            _pause()
            return

        # Remove worktree (but not the main repo for chat tasks)
        is_chat = not task.branches and task.cwd == repo_path
        if task.cwd and repo_path and not is_chat:
            worktree.remove_worktree(task.cwd, repo_path)

        enrichment.uninstall_hooks(task)

        # Update task status
        task.status = "done"
        store.update_task(task)

        msg = "Task marked done." if is_chat else "Task marked done. Worktree removed."
        console.print(f"  {msg}", style="green")
        _pause()

        self.reload_tasks()

    def cmd_info(self) -> None:
        """Show info panel for the selected task."""
        task = self.selected_task()
        if not task:
            console.print("  No task selected.", style="dim")
            _pause()
            return
        render_task_info(task)
        _pause()

    def cmd_refs(self) -> None:
        """Show references for the selected task."""
        task = self.selected_task()
        if not task:
            console.print("  No task selected.", style="dim")
            _pause()
            return
        render_references(task)
        _pause()

    def cmd_note(self) -> None:
        """Open notes in $EDITOR for the selected task."""
        task = self.selected_task()
        if not task:
            console.print("  No task selected.", style="dim")
            _pause()
            return

        notes_path = store.get_notes_path(task.id)
        if not notes_path.exists():
            notes_path.parent.mkdir(parents=True, exist_ok=True)
            notes_path.write_text(f"# {task.name}\n")

        ALLOWED_EDITORS = {"vim", "nvim", "nano", "vi", "emacs", "code"}
        editor = os.environ.get("EDITOR", "vim")
        if editor not in ALLOWED_EDITORS:
            editor = "vim"
        subprocess.run([editor, str(notes_path)])

    def cmd_ls(self) -> None:
        """Toggle showing done tasks."""
        self.show_done = not self.show_done
        self.reload_tasks()

    def cmd_search(self, query: str) -> None:
        """Filter task list by query."""
        self.filter_text = query
        self.selected_idx = 0
        self.reload_tasks()

    def handle_input(self, user_input: str) -> None:
        """Dispatch user input to the appropriate command handler."""
        cmd, args = parse_command(user_input)

        if cmd == "noop":
            return
        elif cmd == "q":
            self.running = False
        elif cmd == "new":
            self.cmd_new(args[0] if args else "")
        elif cmd == "chat":
            self.cmd_chat(args[0] if args else "")
        elif cmd == "cc":
            self.cmd_open()
        elif cmd == "done":
            self.cmd_done()
        elif cmd == "info":
            self.cmd_info()
        elif cmd == "refs":
            self.cmd_refs()
        elif cmd == "note":
            self.cmd_note()
        elif cmd == "ls":
            self.cmd_ls()
        elif cmd == "search":
            self.cmd_search(args[0] if args else "")
        elif cmd == "select":
            # Try numeric selection
            text = args[0] if args else ""
            try:
                idx = int(text) - 1  # 1-based to 0-based
                if 0 <= idx < len(self.tasks):
                    self.selected_idx = idx
            except ValueError:
                # Treat as search
                self.cmd_search(text)

    def run_interactive(self) -> None:
        """Run the main interactive loop with arrow key navigation."""
        bindings = KeyBindings()

        @bindings.add(Keys.Up)
        def _up(event) -> None:
            if self.tasks and self.selected_idx > 0:
                self.selected_idx -= 1
                self.draw()

        @bindings.add(Keys.Down)
        def _down(event) -> None:
            if self.tasks and self.selected_idx < len(self.tasks) - 1:
                self.selected_idx += 1
                self.draw()

        @bindings.add(Keys.Enter)
        def _enter(event) -> None:
            buf = event.app.current_buffer
            if buf.text.strip():
                # Text in buffer — submit it as input
                buf.validate_and_handle()
            else:
                # Empty buffer — open the selected task
                event.app.exit(result="__open__")

        @bindings.add(Keys.ControlC)
        def _quit(event) -> None:
            event.app.exit(result="__quit__")

        while self.running:
            self.reload_tasks()
            self.draw()

            try:
                result = self.session.prompt(
                    HTML("  <b>&gt;</b> "),
                    key_bindings=bindings,
                )
            except KeyboardInterrupt:
                self.running = False
                break
            except EOFError:
                self.running = False
                break

            if result == "__quit__":
                self.running = False
                break
            elif result == "__open__":
                self.cmd_open()
            elif result is not None:
                # Reset filter when entering a command
                if result.strip().startswith("/"):
                    self.filter_text = ""
                self.handle_input(result)

        clear_screen()
        console.print("  [dim]Goodbye.[/dim]")
        console.print()



def _pause() -> None:
    """Wait for user to press enter before continuing."""
    try:
        PromptSession().prompt(HTML("  <dim>Press enter to continue...</dim>"))
    except (KeyboardInterrupt, EOFError):
        pass


def main() -> None:
    """Entry point for jarvis."""
    app = JarvisApp()
    try:
        app.run_interactive()
    except KeyboardInterrupt:
        clear_screen()
        console.print("  [dim]Goodbye.[/dim]")
        console.print()


if __name__ == "__main__":
    main()
