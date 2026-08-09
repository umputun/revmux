---
description: cost — what implementing a filed item reaches into, and whether the work is proportionate to its value
---
## Lens: cost

Price the item by reading the code it would touch. Not in hours — in reach: what has to change, what
each of those changes drags with it, and what is permanently harder afterwards.

Trace it concretely, citing files:

- the surfaces it changes and everything already coupled to them — callers, stored shapes, wire
  formats, the command-line surface, anything another program parses
- whether it fits the structure that is there or needs that structure bent first. A feature that needs
  a refactor before it is possible costs the refactor as well
- what it turns into a commitment: a value that becomes a promise, an internal detail that becomes
  public, a default that can never move again
- the ongoing cost once it merges — the tests that must keep passing, the documents that must stay in
  step, the second way of doing something that now has to be maintained beside the first
- what it forecloses: the change that becomes hard because this one happened first

Then weigh it. Cost alone decides nothing — an expensive change worth making is worth making. Say what
the value would have to be for the price to be right, so the maintainer can check that against the
value he believes it has. Where the price is small, say that as plainly: "one file, an afternoon" is a
finding too, and the kind that ends an argument.

Do not estimate what you did not read. A guess at reach reads exactly like a measurement and is worth
less than naming the parts you could not trace.
