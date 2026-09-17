"""entire-tail pane link: print the focused iTerm2 session id, one per line.

This is the whole Python half. It reports and nothing else — pairing, the
decision and the tab switch all stay in Go, so the only thing that has to be
correct here is "which session did the user just select".

iTerm2 has no way to tell a plain terminal program that its neighbour was
clicked; the Python API is the only event-driven route (`it2api monitor-focus`
awaits exactly one update and exits). Polling AppleScript works but wakes iTerm
twice a second forever, which is what this avoids.

Two deliberate choices:

  * `asyncio.run` over `iterm2.run_forever`. run_forever retries the connection
    indefinitely, and a user who clicked Deny on iTerm's "allow this script to
    connect" prompt would be re-prompted forever. Failing to connect must be a
    clean non-zero exit so the daemon can give up.
  * printing a line is also the liveness check. If the daemon dies and its pipe
    closes, the next print raises and this process exits rather than lingering
    as an orphan holding an API connection.
"""

import asyncio
import sys

import iterm2


def _focused_id(app):
    """The session the user is actually looking at, or None.

    The FocusUpdate is deliberately ignored as a source of truth and used only
    as a "something moved, look again" trigger. `active_session_changed` names
    the new active session of ANY window, including one with no focus — measured
    on iTerm2 3.6.11:

        update[active_session_changed.session_id=5A50D36E]  app_key_session=802CB271

    and that is fatal here rather than merely imprecise. Switching a partner's
    tab is exactly such a change, so a watcher that trusts the update reports
    our own action back to us, the daemon pairs it and switches the other side,
    and two linked pairs flip tabs forever. The key window's current session
    cannot loop that way: our tab switch never moves focus, so it never changes
    this answer.
    """
    window = app.current_terminal_window
    if window is None:
        return None
    tab = window.current_tab
    if tab is None:
        return None
    session = tab.current_session
    # Transiently None between a tab change and the app's view catching up.
    return session.session_id if session is not None else None


async def _main(connection):
    app = await iterm2.async_get_app(connection)
    # A connected watcher is not the same thing as a running one: with iTerm's
    # Python API switched off, this process starts, fails to connect and dies,
    # over and over. Announcing the working connection is what lets the daemon
    # report the difference instead of claiming success for a restart loop.
    print("!ready", flush=True)
    last = None
    async with iterm2.FocusMonitor(connection) as monitor:
        while True:
            await monitor.async_get_next_update()
            session_id = _focused_id(app)
            if not session_id or session_id == last:
                continue
            last = session_id
            print(session_id, flush=True)


async def _connect():
    connection = await iterm2.Connection.async_create()
    await _main(connection)


if __name__ == "__main__":
    try:
        asyncio.run(_connect())
    except (BrokenPipeError, KeyboardInterrupt):
        sys.exit(0)
    except Exception as exc:  # connection refused, permission denied, iTerm quit
        print("pane-link: {}".format(exc), file=sys.stderr)
        sys.exit(1)
