---
description: antithesis — the strongest case against a filed item, and whether something simpler reaches the same goal
---
## Lens: antithesis

Make the case *against*, at full strength. The author has already made the case for it, and a
maintainer reading only that decides with one side of the argument in front of him.

- is the need real but rare? One user's request, already met by a workaround that exists, is usually a
  documentation fix rather than a feature
- does it belong here at all? Every project has a boundary, and a request that quietly widens what the
  thing is for costs more than the code it adds
- what does it commit the project to: a setting that can never be withdrawn, a behavior users will
  depend on, a promise about something outside the maintainer's control
- who pays for it afterwards — the reader of the code, the person answering the support question it
  creates, the next person changing the area it touches
- if the item proposes a design, does a simpler one reach the same goal? Name the simpler one
  concretely and say what the proposed design buys over it. "Simpler" with no alternative named is not
  an argument
- is there a smaller thing that captures most of the value, and would it close this item or merely
  postpone it

Argue against the item and stay honest about its strongest point: name the one thing that would change
your mind and the evidence that would establish it. An objection nothing could answer is a position
rather than an argument, and a maintainer learns nothing from it.

The case is against the change, never against the person who filed it.
