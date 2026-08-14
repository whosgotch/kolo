# Roadmap

One feature: long-lived shared agents that several people use at once. Nothing
else is built until that is good.

Each step should be demonstrable on two machines, because a multiplayer feature
that works on one proves nothing.

## 1. An agent on a lent machine

`kolo host` connects to the hub and spawns an agent when asked. The hub keeps the
list; the browser creates and stops.

*Demo: create an agent from a browser on another machine, watch it appear, stop
it.*

## 2. Watch it

Screen frames go host → hub → browsers. Joining repaints what is already there.
Several people watch at once.

*Demo: two browsers on the same agent, seeing the same thing.*

## 3. Talk to it

Messages go browser → hub → the queue, released by the gate. Attributed to the
member the hub authenticated, not a name they typed.

*Demo: two people sending work to one agent, each seeing who asked for what.*

The gap here has closed. The detector could tell idle from dialog but not from
**busy running a shell command** — a state where the input box is still on screen
and an injected line is silently swallowed (`docs/probe-findings.md` #4). What
tells them apart turned out to be the hint under the box, which the recordings
show changing from "? for shortcuts" to "esc to interrupt". Busy is now a state
of its own, and one the page can explain a wait with rather than only hold on.

## 4. Use it without the host

Answer a dialog, interrupt, restart, start fresh — all from the browser.

Until this exists the product does not work: every permission prompt stops the
agent until the host walks back to their keyboard, and the host is not supposed
to be there at all. An agent nobody can interrupt is worse — it goes down a wrong
path and the org watches.

All four are built. Answering and interrupting send no keystroke anybody chose:
the choices are read off the screen and offered as buttons, an answer is the
number of the option plus the label the member was shown, and it is refused if
the screen has moved on to a different question. Interrupt is Esc, and only while
the agent is working — Esc means cancel at a dialog and clear at an input box,
and neither is what somebody pressing stop is asking for.

Restart and start fresh are a different mechanism: the process, and the resume
command in the per-agent adapter. Neither needs a particular screen, because
killing a process is safe on every screen there is. A restart resumes; a restart
whose resume is refused starts clean and says so, because silent context loss is
worse than visible context loss.

## 5. More than one

Several agents on a host, and the list becomes the front door rather than the
terminal. One agent per directory, enforced.

*Demo: three agents in three directories, people moving between them.*

## 6. A second agent kind

Screen markers and a resume command for one more agent. This is what proves the
seam is real rather than a shape Claude Code happens to fit.

## Then

The log — who asked for what, when, who stopped it — durable across restarts.
Enough to open an agent in the morning and know what happened overnight. It is
also what stands in for roles, so it is not optional for long.

## Already built

- Running an agent under a PTY, modelling its screen with vt10x, and repainting
  that screen for someone who joins mid-session.
- The queue and the gate, and the findings behind them.

Both survive the change of shape. What goes with it: the host's raw-mode
passthrough, `-lan` and its URL secret, guest nicknames, and presence as a list
of members who are online.

## Not doing

- Projects, promoted skills, channels, per-member personal agents, roles. Each
  was scoped and cut. They are additions to a working product and there is not
  one yet.
- A tunnel. The outbound connection removes the need.
- Peer-to-peer. It makes membership and history hard and cannot become a hosted
  product without a rewrite.
