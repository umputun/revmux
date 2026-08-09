---
description: grounding — whether what a filed item claims is true of the code as it stands today
---
## Lens: grounding

Settle what is factually true before anyone argues about what to do. A thread built on a claim the
repository does not support is an argument nobody can win, and a request for something that already
ships is a documentation gap rather than a decision.

Read the code the item is about — the files it points at, and the ones it should have pointed at.
Every answer cites a file and a line, or says explicitly that you looked and found nothing.

- does the described behavior actually happen? Trace the path that produces it. If you cannot
  reproduce the claim by reading, that is a finding, not a blank: say so, and say where you looked
- is the capability already there under another name, another flag, another command, or a setting
  nobody documented?
- is the item a duplicate of something already filed, open or closed?
- does the item describe the project as it is, or as it was? A report against behavior that has since
  changed is answered by the change, not by the argument it starts
- if the item proposes an approach, does the code permit it? Name what would have to move first

Report what you established, never what you infer from it. "The path is guarded at that line" and
"the report is wrong" are different claims, and only the first is yours to make from reading. A claim
you checked and confirmed is worth as much as one you refuted — say which of the two you did.
