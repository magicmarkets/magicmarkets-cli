## What does this change do, and why?

<!-- Explain the why, not just the what — reviewers can read the diff. -->

## How was this verified?

<!--
Tests alone don't prove a command works. For anything with a runtime
surface, say what you actually ran: a read-only command against the live
API, or a local stub server for write paths.

Never place a real order to test a change (see README > Conventions and
invariants).
-->

- [ ] `make fmt && make lint && make test` pass
- [ ] Added/updated a test if this touches money, prices, or the trading gate
- [ ] Ran `make update-spec` and `make test` if this touches the vendored OpenAPI spec
- [ ] Exercised the change directly (command run, or how — see above)

## Anything reviewers should pay extra attention to?

<!-- Spec quirks worked around, deliberate scope cuts, follow-up work, etc. -->
