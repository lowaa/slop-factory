# State machines

Each diagram is generated from the transition table named under it, and a
test fails when the two differ. After changing a table, rewrite them with
`UPDATE_DOCS=1 go test -p 1 -run Diagram ./internal/ticket ./internal/change ./internal/landing`
(`-p 1`: the three packages write this one file).

## Ticket

Every move of a ticket's status, by any route (a person, the claim, an agent
leaving its lease). Table: `ticketTransitions` in `internal/ticket/states.go`.

<!-- state-diagram:ticket -->
```mermaid
stateDiagram-v2
    preparing --> parked
    preparing --> open
    preparing --> blocked
    preparing --> done
    preparing --> cancelled
    parked --> preparing
    parked --> done
    parked --> cancelled
    open --> preparing
    open --> parked
    open --> in_progress
    open --> in_merge_queue
    open --> blocked
    open --> discussion
    open --> review
    open --> done
    open --> cancelled
    in_progress --> preparing
    in_progress --> open
    in_progress --> in_merge_queue
    in_progress --> blocked
    in_progress --> discussion
    in_progress --> review
    in_progress --> done
    in_progress --> cancelled
    in_merge_queue --> preparing
    in_merge_queue --> open
    in_merge_queue --> blocked
    in_merge_queue --> discussion
    in_merge_queue --> review
    in_merge_queue --> done
    in_merge_queue --> cancelled
    blocked --> preparing
    blocked --> open
    blocked --> in_progress
    blocked --> discussion
    blocked --> review
    blocked --> done
    blocked --> cancelled
    discussion --> preparing
    discussion --> open
    discussion --> in_progress
    discussion --> blocked
    discussion --> review
    discussion --> done
    discussion --> cancelled
    review --> preparing
    review --> open
    review --> blocked
    review --> discussion
    review --> done
    review --> cancelled
    done --> preparing
    done --> open
    done --> in_progress
    done --> blocked
    done --> discussion
    done --> review
    done --> cancelled
    cancelled --> preparing
    cancelled --> open
    cancelled --> in_progress
    cancelled --> blocked
    cancelled --> discussion
    cancelled --> review
    cancelled --> done
```
<!-- /state-diagram:ticket -->

## Attempt

One attempt at a ticket. Table: `attemptTransitions` in
`internal/ticket/states.go`.

<!-- state-diagram:attempt -->
```mermaid
stateDiagram-v2
    queued --> in_progress
    queued --> in_merge_queue
    queued --> discussion
    queued --> review
    queued --> done
    queued --> closed
    in_progress --> queued
    in_progress --> in_merge_queue
    in_progress --> discussion
    in_progress --> review
    in_progress --> done
    in_progress --> closed
    in_merge_queue --> queued
    in_merge_queue --> discussion
    in_merge_queue --> review
    in_merge_queue --> done
    in_merge_queue --> closed
    discussion --> queued
    discussion --> in_progress
    discussion --> review
    discussion --> done
    discussion --> closed
    review --> queued
    review --> discussion
    review --> done
    review --> closed
    done --> queued
    done --> in_progress
    done --> discussion
    done --> review
    done --> closed
    closed --> queued
    closed --> in_progress
    closed --> discussion
    closed --> review
    closed --> done
```
<!-- /state-diagram:attempt -->

## Landing

A change's landing, from finished work to merged. Table:
`landingTransitions` in `internal/change/landing_state.go`.

<!-- state-diagram:landing -->
```mermaid
stateDiagram-v2
    open --> merging
    open --> merged
    open --> closed
    open --> superseded
    merging --> open
    merging --> merged
    merging --> closed
    closed --> superseded
```
<!-- /state-diagram:landing -->

## Landing queue entry

An entry in the merge queue, by phase. Table: `transitions` in
`internal/landing/states.go`.

<!-- state-diagram:landing_queue_entry -->
```mermaid
stateDiagram-v2
    queued --> serial_merging_main
    queued --> serial_merging
    queued --> batch_testing
    queued --> merged
    queued --> back_with_agent
    queued --> dequeued
    serial_merging_main --> serial_waiting_checks
    serial_merging_main --> merged
    serial_merging_main --> back_with_agent
    serial_merging_main --> dequeued
    serial_waiting_checks --> serial_merging_main
    serial_waiting_checks --> serial_merging
    serial_waiting_checks --> merged
    serial_waiting_checks --> back_with_agent
    serial_waiting_checks --> dequeued
    serial_merging --> serial_merging_main
    serial_merging --> merged
    serial_merging --> back_with_agent
    serial_merging --> dequeued
    batch_testing --> batch_merging
    batch_testing --> merged
    batch_testing --> released
    batch_testing --> back_with_agent
    batch_testing --> dequeued
    batch_merging --> batch_testing
    batch_merging --> merged
    batch_merging --> back_with_agent
    batch_merging --> dequeued
    released --> queued
    back_with_agent --> queued
```
<!-- /state-diagram:landing_queue_entry -->
