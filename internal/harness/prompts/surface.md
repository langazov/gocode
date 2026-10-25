# WHAT YOU CAN CHANGE

The harness is a tree of components. Each has a different runtime cost and a different reach:

| path | what it is | when it is in context |
|---|---|---|
| `systemprompt.md` | role and procedure | ALWAYS ON -- every token competes with the agent's own output budget |
| `guardrails.md` | standing rules | ALWAYS ON -- same cost |
| `LongTermMEMORY.md` | durable domain facts | ALWAYS ON -- same cost |
| `tool_guidance.md` | how to use the tools | ALWAYS ON -- same cost |
| `skills/<name>/SKILL.md` | a reusable workflow | loaded ON DEMAND, only when it is invoked |
| `agent/<name>.md` | a delegated subagent | runs in its OWN context via the `task` tool, returns text |
| `scripts/<name>.py`, `scripts/<name>.sh` | a deterministic helper | run on demand via bash |
| `hooks.json` | declarative tool rules | ALWAYS ENFORCED, and acts INSIDE the tool loop |

The three that are easy to get wrong:

`skills/` -- frontmatter needs `name:` matching the directory and a `description:`, or it is
dropped silently at load. Two further rules below decide whether it survives validation at all:
it must be named in the always-on text, and any helper it calls must travel with it.

`agent/` -- frontmatter needs `mode: subagent` and `description:`. It sees only what its
own file and the call say, never the main conversation, and it returns text. So it is worth it
for exactly one shape of problem: a bounded sub-task whose INPUT is large and whose ANSWER is
small. It is a bad trade when the output is as big as the input.

`hooks.json` -- the only component that acts in the tool loop rather than in the prompt, and
therefore the only way to enforce something the model cannot skip. Prose can be stopped
reading; a rule cannot. Its shape is exactly:

    {"rules": [
      {"when": "before", "tool": "<tool name or *>", "match": "<regex over the JSON call arguments>",
       "action": "deny", "message": "<why, and what to do instead>"},
      {"when": "after", "tool": "<tool name or *>", "match": "<regex over the tool output>",
       "action": "append", "message": "<the hint the agent sees after that output>"}
    ]}

A `before` rule refuses a matching call and shows its message as the call's result; an `after`
rule appends its message to a matching output. An empty `match` matches every call of that tool.

Change only what a trajectory shows needs changing -- but WHERE you change something, the FORM
matters as much as the content, and the cheap form is not the obvious one.

ALWAYS-ON PROSE IS THE EXPENSIVE OPTION. Every word of it is in context on every run, competing
with the agent's own output budget -- and the most costly failure on this work is a run that
reasons until the budget is gone and produces nothing. Adding prose makes that MORE likely, not
less. An on-demand component costs nothing until the moment it is invoked.

So prefer, in this order:
  - a SKILL for anything with a recognisable trigger -- a procedure for a document type, a
    format, a recurring situation. This is the default form for new instruction.
  - a SCRIPT for a deterministic operation the runs hand-rolled and got wrong, or did slowly.
  - a SUBAGENT for a bounded investigation whose input is large and whose answer is small.
  - a HOOKS RULE when a recurring, mechanical problem in the tool loop is better handled there
    than in prose. It acts inside the loop, so it cannot be ignored AND it costs nothing in
    always-on budget. The recurring shapes: refusing a call that keeps going wrong (with the
    right alternative in the message), and appending a targeted hint after an error signature
    that keeps recurring.
  - ALWAYS-ON TEXT last, and only for a short rule that genuinely applies to every run. If you
    add always-on text, cut something else to pay for it.

TWO RULES THAT DISCARD THE WHOLE PROPOSAL. Validation runs before any rollout, and a harness
that fails it is thrown away entire.

1. EVERY SKILL NEEDS A TRIGGER LINE -- the most violated rule here by a wide margin. If you
   create or keep `skills/<name>/SKILL.md`, its directory name must appear VERBATIM in
   `systemprompt.md` or `guardrails.md`, in the SAME edit:

       When <the situation this skill handles>, load the `<name>` skill.

   An unnamed skill is never opened anyway: the line is what makes it exist.

2. EVERY SCRIPT YOU NAME MUST EXIST. A `scripts/<file>.py` invoked from a SKILL.md or from
   always-on text must ship in the same proposal, a skill's own helper at
   `skills/<skill-name>/scripts/<file>.py`; one dropped loose in the skill directory is OFF THE
   EDITABLE SURFACE and silently discarded.

THE DELIVERABLE'S SHAPE BELONGS TO THE REQUEST, NOT TO YOU. A harness changes HOW the agent
works; it must never change WHAT SHAPE it delivers. Where the task names files, fields, sections
or an order, that shape is part of what was asked for. A component that tells the agent to add
explanatory files, restate a derivation inside the artifact, rename things for clarity, or
surface intermediate values so a reviewer can check them is CHANGING THE DELIVERABLE -- and a
downstream reader keys on exact names and positions. Guidance that makes the agent's reasoning
visible belongs in the harness text or the trajectory, never in the artifact.

# HOW TO MAKE A CHANGE

The harness is in `context/harness/`, and it is YOURS TO EDIT. Open the files, change them in
place with your normal tools, and create new ones where a new component is the right lever.
Your edits on disk ARE the proposal -- there is no separate format to reproduce them in, and
nothing you write in this reply changes the harness.

- Files you do not touch are inherited unchanged. Do not rewrite a file to keep it the same.
- Delete a file to remove that component.
- If the evidence does not argue for any change, leave everything as it is and say KEEP_BASE
  in your notes. That is a real answer, not a failure: adaptation the evidence does not support
  costs context and buys nothing.

Then reply with ONE `<notes>` block and nothing else: for each change, the moment in a
trajectory that motivated it. The notes are read by a person, not applied by a machine.
