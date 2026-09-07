# Implementation plan

This project is implemented as a modular marketplace service and an independently
deployable demo restaurant. The implementation order deliberately follows a
vertical business slice instead of completing technical layers in isolation.

1. Define customer and restaurant journeys and their failure branches.
2. Fix the order state machine and data ownership boundaries.
3. Define OpenAPI before HTTP handlers.
4. Introduce PostgreSQL migrations and immutable order snapshots.
5. Implement catalogue, cart, checkout and order tracking.
6. Implement the partner menu and order-status API.
7. Deliver order events through a transactional outbox.
8. Implement the demo restaurant webhook consumer.
9. Cover state transitions and the complete Compose scenario with tests.

Key constraints: one restaurant per cart, server-side price calculation,
idempotent checkout, at-least-once webhook delivery, idempotent webhook handling,
and no external HTTP request inside a database transaction.

