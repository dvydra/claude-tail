"""Set the title of the iTerm tab holding one session.

Usage: tabtitle.py <session-uuid> <title>

The same as Edit Tab Title: it names the tab, not the pane, so it stays put
when a pane's shell or agent sets its own title. Run by
`entire-tail threads title` through `uv run --with iterm2`.
"""

import sys

import iterm2

session_id, title = sys.argv[1], sys.argv[2]


async def main(connection):
    app = await iterm2.async_get_app(connection)
    session = app.get_session_by_id(session_id)
    if session is None or session.tab is None:
        print(f"no tab for session {session_id}", file=sys.stderr)
        sys.exit(1)
    await session.tab.async_set_title(title)


iterm2.run_until_complete(main)
